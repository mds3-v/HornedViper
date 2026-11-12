package kernel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hornedviper/internal/config"
	"hornedviper/internal/domain"
	"hornedviper/internal/runtime"
	"hornedviper/internal/store"
)

// liveWorkerRuntime keeps the test focused on two real Worker sessions; the
// planner is deterministic and empty so no extra model planning is paid for.
type liveWorkerRuntime struct {
	worker *runtime.NormaRuntime
}

func (r liveWorkerRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}
func (r liveWorkerRuntime) Work(ctx context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	return r.worker.Work(ctx, req)
}
func (r liveWorkerRuntime) Lead(ctx context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	return r.worker.Lead(ctx, req)
}

// TestLiveLLMSwarm is opt-in because it calls a real provider.
//
//	HV_LIVE_LLM=1 HV_CONFIG=../../config.json go test ./internal/kernel -run TestLiveLLMSwarm -v -timeout 3m
func TestLiveLLMSwarm(t *testing.T) {
	if os.Getenv("HV_LIVE_LLM") != "1" {
		t.Skip("set HV_LIVE_LLM=1 to run the paid live LLM smoke test")
	}
	cfgPath := os.Getenv("HV_CONFIG")
	if cfgPath == "" {
		cfgPath = "../../config.json"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[cfg.DefaultProvider]
	if p.APIKey == "" {
		t.Fatalf("provider %s has no API key", cfg.DefaultProvider)
	}
	rt, err := runtime.NewNormaRuntime(runtime.NormaConfig{
		Format: p.Format, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model,
		ProviderName: p.Name, ArtifactDir: t.TempDir(), MaxDuration: 45 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := liveWorkerRuntime{worker: rt}

	st, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	task, err := st.CreateTask(ctx, "live swarm smoke", domain.ModeCTF, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetGoal(ctx, task.ID, "report one live smoke fact", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "live worker smoke", Rationale: "exercise real lane sessions",
		Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 2, "quorum": 1, "strategies": []any{
			map[string]any{"name": "live-a", "brief": "Call record_facts once with summary 'live smoke' and evidence 'live smoke evidence'. Do not use the network. Then call finish with status done."},
			map[string]any{"name": "live-b", "brief": "Call record_facts once with summary 'live smoke' and evidence 'live smoke evidence'. Do not use the network. Then call finish with status done."},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, worker, Config{
		MaxWorkers: 2, Lease: 2 * time.Minute,
		Scheduler: SchedulerConfig{Debounce: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond, MinGap: 5 * time.Millisecond},
	})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	deadline := time.Now().Add(90 * time.Second)
	var final *domain.Intent
	for time.Now().Before(deadline) {
		final, _ = st.GetIntent(ctx, intent.ID)
		if final != nil && (final.State == domain.IntentSucceeded || final.State == domain.IntentFailed) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	cancel()
	<-done
	checkCtx := context.Background()
	if final == nil || final.State != domain.IntentSucceeded {
		t.Fatalf("live intent state = %v, want succeeded", final)
	}
	results, _ := st.ListLaneResults(checkCtx, intent.ID)
	doneLanes := 0
	for _, r := range results {
		if r.Status == "done" {
			doneLanes++
		}
	}
	if doneLanes == 0 {
		t.Fatal("no real lane completed")
	}
	facts, _ := st.ListFacts(checkCtx, task.ID, 10)
	if len(facts) == 0 {
		t.Fatal("real worker did not produce the requested fact")
	}
}

func TestLiveLLMShard(t *testing.T) {
	if os.Getenv("HV_LIVE_LLM") != "1" {
		t.Skip("set HV_LIVE_LLM=1 to run the paid live LLM smoke test")
	}
	cfgPath := os.Getenv("HV_CONFIG")
	if cfgPath == "" {
		cfgPath = "../../config.json"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[cfg.DefaultProvider]
	if p.APIKey == "" {
		t.Fatalf("provider %s has no API key", cfg.DefaultProvider)
	}
	rt, err := runtime.NewNormaRuntime(runtime.NormaConfig{
		Format: p.Format, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model,
		ProviderName: p.Name, ArtifactDir: t.TempDir(), MaxDuration: 45 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "live-shard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	task, err := st.CreateTask(ctx, "live shard smoke", domain.ModeCTF, nil)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := st.SetGoal(ctx, task.ID, "report one live shard fact", nil)
	if err != nil {
		t.Fatal(err)
	}
	var assetIDs []int64
	for _, host := range []string{"live-a.example", "live-b.example"} {
		a, err := st.RecordAsset(ctx, domain.AssetSpec{Type: "host", Value: host})
		if err != nil {
			t.Fatal(err)
		}
		assetIDs = append(assetIDs, a.ID)
		if err := st.AttachAnchor(ctx, goal.ID, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "live shard smoke", Rationale: "exercise real shard sessions",
		Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionShard, AssetIDs: assetIDs,
		Budget: map[string]any{"lanes": 2, "min_success": 1},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, liveWorkerRuntime{worker: rt}, Config{
		MaxWorkers: 2, Lease: 2 * time.Minute,
		Scheduler: SchedulerConfig{Debounce: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond, MinGap: 5 * time.Millisecond},
	})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	deadline := time.Now().Add(90 * time.Second)
	var final *domain.Intent
	for time.Now().Before(deadline) {
		final, _ = st.GetIntent(ctx, intent.ID)
		if final != nil && (final.State == domain.IntentSucceeded || final.State == domain.IntentFailed) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	cancel()
	<-done
	checkCtx := context.Background()
	if final == nil || final.State != domain.IntentSucceeded {
		t.Fatalf("live shard intent state = %v, want succeeded", final)
	}
	results, _ := st.ListLaneResults(checkCtx, intent.ID)
	doneLanes := 0
	for _, r := range results {
		if r.Status == "done" {
			doneLanes++
		}
	}
	if doneLanes == 0 {
		t.Fatal("no real shard lane completed")
	}
}

func TestLiveLLMLeadPlan(t *testing.T) {
	if os.Getenv("HV_LIVE_LLM") != "1" {
		t.Skip("set HV_LIVE_LLM=1 to run the paid live LLM smoke test")
	}
	cfgPath := os.Getenv("HV_CONFIG")
	if cfgPath == "" {
		cfgPath = "../../config.json"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[cfg.DefaultProvider]
	if p.APIKey == "" {
		t.Fatalf("provider %s has no API key", cfg.DefaultProvider)
	}
	rt, err := runtime.NewNormaRuntime(runtime.NormaConfig{
		Format: p.Format, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model,
		ProviderName: p.Name, ArtifactDir: t.TempDir(), MaxDuration: 45 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	task := &domain.Task{ID: 1, Mode: domain.ModeCTF, Title: "lead smoke"}
	goal := &domain.Goal{ID: 1, Text: "find a flag"}
	intent := &domain.Intent{ID: 1, Summary: "investigate a web target", Rationale: "test lane planning"}
	out, err := rt.Lead(ctx, &domain.LeadRequest{Task: task, Goal: goal, Intent: intent, Lanes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Lanes) != 2 {
		t.Fatalf("lanes = %d, want 2", len(out.Lanes))
	}
	if out.Lanes[0].Name == out.Lanes[1].Name || out.Lanes[0].Brief == out.Lanes[1].Brief {
		t.Fatalf("lead returned duplicate plans: %+v", out.Lanes)
	}
}

func TestLiveLLMConflictReview(t *testing.T) {
	if os.Getenv("HV_LIVE_LLM") != "1" {
		t.Skip("set HV_LIVE_LLM=1 to run the paid live LLM smoke test")
	}
	cfgPath := os.Getenv("HV_CONFIG")
	if cfgPath == "" {
		cfgPath = "../../config.json"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[cfg.DefaultProvider]
	if p.APIKey == "" {
		t.Fatalf("provider %s has no API key", cfg.DefaultProvider)
	}
	rt, err := runtime.NewNormaRuntime(runtime.NormaConfig{
		Format: p.Format, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model,
		ProviderName: p.Name, ArtifactDir: t.TempDir(), MaxDuration: 45 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := rt.ReviewConflicts(ctx, &domain.ConflictReviewRequest{
		Task:   &domain.Task{ID: 1, Mode: domain.ModeCTF, Title: "conflict smoke"},
		Intent: &domain.Intent{ID: 1, Summary: "verify one flag", Rationale: "test"},
		Conflicts: []domain.Conflict{{
			ID: 1, TaskID: 1, IntentID: 1, Type: "flag_challenge_mismatch",
			Subject: "flag{same}", SubjectHash: "hash1",
			LaneIDs:  []string{"lane-1", "lane-2"},
			Evidence: []string{"c-01 says flag", "c-02 says same flag"},
			Status:   "open",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Decisions) == 0 {
		t.Fatal("no conflict decision")
	}
	switch resp.Decisions[0].Decision {
	case "keep_all", "quarantine_all", "needs_human":
	default:
		t.Fatalf("invalid decision: %+v", resp.Decisions[0])
	}
}
