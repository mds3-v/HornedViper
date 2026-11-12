package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Autumn-27/norma/tool"

	"hornedviper/internal/api"
	"hornedviper/internal/config"
	"hornedviper/internal/customtools"
	"hornedviper/internal/domain"
	"hornedviper/internal/kernel"
	"hornedviper/internal/loghub"
	"hornedviper/internal/rules"
	agentruntime "hornedviper/internal/runtime"
	"hornedviper/internal/store"
	"hornedviper/internal/submit"
)

func main() {
	dbPath := flag.String("db", "hornedviper.db", "SQLite database path")
	configPath := flag.String("config", "config.json", "project config path")
	mode := flag.String("mode", "ctf", "ctf or pentest (demo only)")
	goalText := flag.String("goal", "demo: recover the flag from demo.local", "task goal (demo only)")
	timeout := flag.Duration("timeout", 30*time.Second, "demo timeout")
	runtimeName := flag.String("runtime", "", "fake or norma (overrides config)")
	providerName := flag.String("provider", "", "deepseek, openai, anthropic or zhipu (overrides config)")
	format := flag.String("format", "", "openai or anthropic (overrides provider)")
	baseURL := flag.String("base-url", "", "provider base URL (overrides provider)")
	modelName := flag.String("model", "", "model name (overrides provider)")
	apiKey := flag.String("api-key", "", "provider API key (overrides config; prefer the Settings page)")
	artifactDir := flag.String("artifacts", "", "artifact directory (overrides config)")
	serve := flag.String("serve", "", "HTTP listen address (e.g. :8787); empty runs the demo")
	submitCmd := flag.String("submit-cmd", envOr("HV_SUBMIT_CMD", ""), "optional shell command to submit a ctf flag; overrides config")
	fakeRisky := flag.Bool("fake-risky", envOr("HV_FAKE_RISKY", "") == "1", "demo: make the fake exploit intent high risk")
	fakeDelay := flag.Duration("fake-delay", 20*time.Millisecond, "fake runtime step delay (useful for pause/stop demos)")
	flag.Parse()

	m := domain.TaskMode(*mode)
	if m != domain.ModeCTF && m != domain.ModePentest {
		fmt.Fprintf(os.Stderr, "invalid -mode %q (want ctf or pentest)\n", *mode)
		os.Exit(2)
	}

	logs := loghub.New(5000)
	log.SetOutput(io.MultiWriter(os.Stderr, logs))

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Persist logs across restarts. Flush the pre-store buffer first, then
	// stream new lines through a buffered writer so logging never blocks agents.
	_ = st.PruneLogs(context.Background(), 50000)
	for _, line := range logs.Snapshot() {
		_ = st.RecordLog(context.Background(), line.Text, time.Now().UnixMilli())
	}
	logCh := make(chan loghub.Line, 2048)
	go func() {
		for line := range logCh {
			_ = st.RecordLog(context.Background(), line.Text, time.Now().UnixMilli())
		}
	}()
	logs.SetSink(func(line loghub.Line) {
		select {
		case logCh <- line:
		default:
		}
	})

	cfgStore, err := config.NewStore(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	buildRuntime := func(cfg config.Config) (agentruntime.Runtime, error) {
		name := cfg.Runtime
		if *runtimeName != "" {
			name = *runtimeName
		}
		if name == "" {
			name = "norma"
		}
		if name == "fake" {
			return &agentruntime.FakeRuntime{StepDelay: *fakeDelay, Risky: *fakeRisky}, nil
		}
		if name != "norma" {
			return nil, fmt.Errorf("invalid runtime %q (want fake or norma)", name)
		}

		provider := cfg.DefaultProvider
		if *providerName != "" {
			provider = *providerName
		}
		p, err := cfg.Provider(provider)
		if err != nil {
			return nil, err
		}
		if *format != "" {
			p.Format = *format
		}
		if *baseURL != "" {
			p.BaseURL = *baseURL
		}
		if *modelName != "" {
			p.Model = *modelName
		}
		if *apiKey != "" {
			p.APIKey = *apiKey
		}
		if p.APIKey == "" {
			return nil, fmt.Errorf("provider %q has no API key; set it in config.json or the Settings page", provider)
		}
		dir := cfg.ArtifactDir
		if *artifactDir != "" {
			dir = *artifactDir
		}
		ruleList, _ := st.ListInterceptRules(context.Background())
		ruleEngine := rules.New(ruleList)
		return agentruntime.NewNormaRuntime(agentruntime.NormaConfig{
			Format: p.Format, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model,
			ProviderName: provider, ArtifactDir: dir,
			Trace: func(a domain.Activity) {
				_ = st.RecordActivity(context.Background(), a)
			},
			Traffic: func(t domain.Traffic) {
				_ = st.RecordTraffic(context.Background(), t)
			},
			LLMCall: func(c domain.LLMCall) {
				_ = st.RecordLLMCall(context.Background(), c)
			},
			Guard: func(tool string, input json.RawMessage) (string, string) {
				return ruleEngine.Check(tool, input)
			},
			ToolEnabled: func(scope, name string) bool {
				return st.ToolEnabled(context.Background(), scope, name)
			},
			CustomTools: func(scope string) []tool.CoreTool {
				return customtools.Load(context.Background(), st, scope)
			},
			QuarantineList: func(ctx context.Context, taskID int64, limit int) ([]domain.QuarantineDigest, error) {
				return st.QuarantineDigest(ctx, taskID, limit)
			},
			QuarantineRead: func(ctx context.Context, id int64) (*domain.QuarantineItem, error) {
				return st.GetQuarantine(ctx, id)
			},
			FlagAlreadySubmitted: func(ctx context.Context, taskID int64, flag string) (bool, error) {
				sum := sha256.Sum256([]byte(flag))
				return st.FlagAlreadySubmitted(ctx, taskID, hex.EncodeToString(sum[:]))
			},
			FlagSubmissionRecorded: func(ctx context.Context, taskID int64, flag string, accepted bool, message string) error {
				sum := sha256.Sum256([]byte(flag))
				return st.RecordFlagSubmission(ctx, taskID, 0, flag, hex.EncodeToString(sum[:]), accepted, message)
			},
			ChallengeCompleted: func(ctx context.Context, taskID int64, challenge string) (bool, error) {
				return st.IsChallengeCompleted(ctx, taskID, challenge)
			},
			ChallengeCompletedRecord: func(ctx context.Context, taskID int64, challenge string) error {
				return st.MarkChallengeCompleted(ctx, taskID, challenge)
			},
		})
	}

	if *serve != "" {
		srv := api.New(st, cfgStore, buildRuntime, api.Config{
			MaxWorkers: 4,
			Scheduler: kernel.SchedulerConfig{
				Debounce: time.Second,
				MaxDelay: 5 * time.Minute,
				MinGap:   time.Second,
			},
			SubmitCommand: *submitCmd,
			AutoResume:    true,
			DrainWindow:   5 * time.Minute,
			MaxContainers: 3,
			Logs:          logs,
		})
		log.Printf("HornedViper UI on http://%s", *serve)
		log.Fatal(http.ListenAndServe(*serve, srv.Handler()))
		return
	}

	appCfg := cfgStore.Get()
	rt, err := buildRuntime(appCfg)
	if err != nil {
		log.Fatalf("runtime: %v\n\nConfigure an API key in %s or the Settings page, or run with -runtime fake.", err, *configPath)
	}
	submitCommand := appCfg.SubmitCommand
	if *submitCmd != "" {
		submitCommand = *submitCmd
	}
	var submitter domain.Submitter
	if submitCommand != "" {
		submitter = submit.NewCommand(submitCommand)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	task, err := st.CreateTask(ctx, "HornedViper Phase 0 demo", m, map[string]any{"max_minutes": 10})
	if err != nil {
		log.Fatalf("create task: %v", err)
	}
	goal, err := st.SetGoal(ctx, task.ID, *goalText, nil)
	if err != nil {
		log.Fatalf("set goal: %v", err)
	}
	if _, err := st.AddConstraint(ctx, task.ID, "deny", "do not leave the demo target scope"); err != nil {
		log.Fatalf("add constraint: %v", err)
	}

	e := kernel.New(st, rt, kernel.Config{
		MaxWorkers: 4,
		Lease:      5 * time.Minute,
		Scheduler: kernel.SchedulerConfig{
			Debounce: 20 * time.Millisecond,
			MaxDelay: 100 * time.Millisecond,
			MinGap:   10 * time.Millisecond,
		},
		AutoApproveDangerous: m == domain.ModePentest,
		Submitter:            submitter,
	})

	fmt.Printf("task #%d mode=%s runtime=%s provider=%s goal=%q\n",
		task.ID, task.Mode, appCfg.Runtime, appCfg.DefaultProvider, goal.Text)
	if err := e.Run(ctx, task.ID); err != nil {
		log.Fatalf("engine: %v", err)
	}

	finalTask, _ := st.GetTask(ctx, task.ID)
	finalGoal, _ := st.GetGoal(ctx, goal.ID)
	counts, _ := st.GraphCounts(ctx, task.ID)
	facts, _ := st.ListFacts(ctx, task.ID, 20)
	findings, _ := st.ListFindings(ctx, task.ID, 20)
	assets, _ := st.ListAssets(ctx, 20)
	events, _ := st.ListEvents(ctx, task.ID, 100)

	fmt.Printf("\n=== result ===\n")
	fmt.Printf("task:   %s\n", finalTask.Status)
	if finalGoal != nil {
		fmt.Printf("goal:   %s\n", finalGoal.State)
	}
	fmt.Printf("graph:  %v\n", counts)
	fmt.Printf("assets: %d, facts: %d, findings: %d, events: %d\n", len(assets), len(facts), len(findings), len(events))

	fmt.Printf("\n=== assets ===\n")
	for _, a := range assets {
		fmt.Printf("  #%d %-10s %s\n", a.ID, a.Type, a.Value)
	}
	fmt.Printf("\n=== facts ===\n")
	for _, f := range facts {
		fmt.Printf("  #%d [%s] %s\n", f.ID, f.Confidence, f.Summary)
	}
	fmt.Printf("\n=== findings ===\n")
	for _, f := range findings {
		fmt.Printf("  #%d [%s] %s — %s\n", f.ID, f.Severity, f.Title, f.Evidence)
	}
	fmt.Printf("\n=== event timeline ===\n")
	for _, ev := range events {
		fmt.Printf("  %s  %-22s %s\n", ev.CreatedAt.Format("15:04:05.000"), ev.Type, ev.ActorID)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
