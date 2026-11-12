package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"hornedviper/internal/config"
	"hornedviper/internal/domain"
	"hornedviper/internal/kernel"
	"hornedviper/internal/llmtest"
	"hornedviper/internal/loghub"
	"hornedviper/internal/markdown"
	"hornedviper/internal/runtime"
	"hornedviper/internal/store"
	"hornedviper/internal/submit"
	"hornedviper/internal/version"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

//go:embed web/templates/*.html web/static/*
var webFS embed.FS

type Config struct {
	MaxWorkers           int
	Scheduler            kernel.SchedulerConfig
	AutoApproveDangerous bool
	SubmitCommand        string // optional CLI override; config.json is the default
	AutoResume           bool   // resume tasks that were active on the previous process
	DrainWindow          time.Duration
	MaxContainers        int
	Logs                 *loghub.Hub
}

// RuntimeBuilder builds a runtime from the current project config. It is called
// per task, so Settings changes apply to new tasks without restarting the server.
type RuntimeBuilder func(config.Config) (runtime.Runtime, error)

type taskRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	engine *kernel.Engine
}

type Server struct {
	st       *store.Store
	cfgStore *config.Store
	build    RuntimeBuilder
	cfg      Config
	logs     *loghub.Hub

	mu         sync.Mutex
	runs       map[int64]*taskRun
	jobMu      sync.Mutex
	jobs       map[string]*tool.Manager
	convMu     sync.Mutex
	convCancel map[int64]context.CancelFunc
	chatMu     sync.Mutex
	chatCancel map[int64]context.CancelFunc
	activeMu   sync.Mutex
	active     map[string][]*activeTool
	activeSeq  int
}

type activeTool struct {
	ID      string
	Name    string
	Command string
	Started time.Time
}

func New(st *store.Store, cfgStore *config.Store, build RuntimeBuilder, cfg Config) *Server {
	if cfg.MaxWorkers <= 0 {
		// 3 container-capable workers + 1 reserved fast worker.
		cfg.MaxWorkers = 4
	}
	srv := &Server{st: st, cfgStore: cfgStore, build: build, cfg: cfg, logs: cfg.Logs, runs: map[int64]*taskRun{}, jobs: map[string]*tool.Manager{}, convCancel: map[int64]context.CancelFunc{}, chatCancel: map[int64]context.CancelFunc{}, active: map[string][]*activeTool{}}
	srv.seedToolCatalog(context.Background())
	if n, err := srv.st.CleanInvalidAnchors(context.Background()); err == nil && n > 0 {
		log.Printf("[startup] removed %d unproven graph anchors", n)
	}
	srv.reconcile()
	go srv.maintenanceLoop()
	return srv
}

// reconcile pauses tasks left running by a previous process. Their engine and
// child sessions are gone, so showing them as running would be misleading.
func (s *Server) maintenanceLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if n, err := s.st.ReapExpiredWork(context.Background()); err == nil && n > 0 {
			log.Printf("[maintenance] reaped %d expired work leases", n)
		}
	}
}

func (s *Server) reconcile() {
	ctx := context.Background()
	_ = s.st.FailStaleThinking(ctx)
	tasks, err := s.st.ListTasks(ctx, 1000)
	if err != nil {
		return
	}
	for _, t := range tasks {
		goal, _ := s.st.CurrentGoal(ctx, t.ID)
		switch t.Status {
		case domain.TaskRunning, domain.TaskPlanning:
			if err := s.st.SetTaskStatus(ctx, t.ID, domain.TaskPaused); err == nil {
				_ = s.st.CancelActiveWork(ctx, t.ID, "server restarted", false, false)
				if goal != nil && (goal.State == domain.GoalOpen || goal.State == domain.GoalInProgress) {
					_ = s.st.SetGoalState(ctx, goal.ID, domain.GoalPaused, "system")
				}
				_, _ = s.st.AppendEvent(ctx, t.ID, domain.EventTaskPaused, "system", "startup", "task", t.ID,
					map[string]any{"reason": "server restarted"})
				if s.cfg.AutoResume {
					if err := s.ResumeTask(ctx, t.ID); err == nil {
						log.Printf("[task %d] auto-resumed after restart", t.ID)
					} else {
						log.Printf("[task %d] auto-resume failed: %v", t.ID, err)
					}
				}
			}
		case domain.TaskPaused:
			_ = s.st.CancelActiveWork(ctx, t.ID, "server restarted", false, false)
			if goal != nil && (goal.State == domain.GoalOpen || goal.State == domain.GoalInProgress) {
				_ = s.st.SetGoalState(ctx, goal.ID, domain.GoalPaused, "system")
			}
		case domain.TaskCancelled, domain.TaskFailed:
			_ = s.st.CancelActiveWork(ctx, t.ID, "task ended", true, true)
			s.abandonGoal(ctx, t.ID, "system", "任务已结束")
		case domain.TaskSettling:
			if err := s.st.SetTaskStatus(ctx, t.ID, domain.TaskFailed); err == nil {
				_ = s.st.CancelActiveWork(ctx, t.ID, "server restarted during settling", true, true)
				s.abandonGoal(ctx, t.ID, "system", "任务在收尾阶段中断")
				_, _ = s.st.AppendEvent(ctx, t.ID, domain.EventTaskStopped, "system", "startup", "task", t.ID,
					map[string]any{"reason": "server restarted during settling"})
			}
		case domain.TaskCompleted:
			_ = s.st.CancelActiveWork(ctx, t.ID, "task completed", true, true)
			if goal != nil && (goal.State == domain.GoalOpen || goal.State == domain.GoalInProgress || goal.State == domain.GoalPaused) {
				_ = s.st.SetGoalState(ctx, goal.ID, domain.GoalMet, "system")
			}
		}
	}
	_ = s.st.CompactStreamingActivities(ctx)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	css, _ := fs.ReadFile(webFS, "web/static/style.css")
	mux.HandleFunc("/static/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(css)
	})
	js, _ := fs.ReadFile(webFS, "web/static/settings.js")
	mux.HandleFunc("/static/settings.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write(js)
	})
	jobsJS, _ := fs.ReadFile(webFS, "web/static/jobs.js")
	mux.HandleFunc("/static/jobs.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write(jobsJS)
	})
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/tasks", s.handleCreateTask)
	mux.HandleFunc("/tasks/auto-ctf", s.handleAutoCTF)
	mux.HandleFunc("/tasks/", s.handleTask)
	mux.HandleFunc("/findings", s.handleFindings)
	mux.HandleFunc("/assets", s.handleAssets)
	mux.HandleFunc("/approvals", s.handleApprovals)
	mux.HandleFunc("/approval-records", s.handleApprovalRecords)
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/chat", s.handleChat)
	mux.HandleFunc("/chat/", s.handleChatSend)
	mux.HandleFunc("/new", s.handleNewConversation)
	mux.HandleFunc("/conversation/", s.handleConversation)
	mux.HandleFunc("/api/conversation/", s.handleConversationMessages)
	mux.HandleFunc("/api/jobs", s.handleJobs)
	mux.HandleFunc("/api/jobs/output", s.handleJobs)
	mux.HandleFunc("/api/jobs/stop", s.handleJobs)
	mux.HandleFunc("/traffic", s.handleTraffic)
	mux.HandleFunc("/tools", s.handleTools)
	mux.HandleFunc("/tools/toggle", s.handleToolToggle)
	mux.HandleFunc("/tools/save", s.handleToolSave)
	mux.HandleFunc("/tools/delete", s.handleToolDelete)
	mux.HandleFunc("/tools/restore", s.handleToolRestore)
	mux.HandleFunc("/tools/category/save", s.handleToolCategorySave)
	mux.HandleFunc("/tools/category/delete", s.handleToolCategoryDelete)
	mux.HandleFunc("/agents", s.handleAgents)
	mux.HandleFunc("/agents/new", s.handleAgentNew)
	mux.HandleFunc("/agents/", s.handleAgent)
	mux.HandleFunc("/intercept-rules", s.handleRules)
	mux.HandleFunc("/intercept-rules/new", s.handleRuleNew)
	mux.HandleFunc("/intercept-rules/", s.handleRule)
	mux.HandleFunc("/logs", s.handleLogs)
	mux.HandleFunc("/attachments", s.handleAttachment)
	mux.HandleFunc("/reports", s.handleReportDownload)
	mux.HandleFunc("/llm-records", s.handleLLMRecords)
	mux.HandleFunc("/llm-calls/", s.handleLLMCall)
	mux.HandleFunc("/api/logs/stream", s.handleLogStream)
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/settings/test", s.handleSettingsTest)
	mux.HandleFunc("/settings/providers", s.handleProvidersCreate)
	mux.HandleFunc("/settings/providers/new", s.handleNewProvider)
	mux.HandleFunc("/settings/providers/", s.handleProvider)
	mux.HandleFunc("/api", s.handleAPIPage)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Current})
	})
	mux.HandleFunc("/api/tasks", s.handleAPITasks)
	mux.HandleFunc("/api/tasks/", s.handleAPITask)
	return loggingMiddleware(mux)
}

// StartTask creates a task, starts its engine, and returns immediately.
func (s *Server) StartTask(ctx context.Context, title string, mode domain.TaskMode, goalText string) (*domain.Task, error) {
	return s.StartTaskWithBudget(ctx, title, mode, goalText, nil)
}

func (s *Server) StartTaskWithBudget(ctx context.Context, title string, mode domain.TaskMode, goalText string, budget map[string]any) (*domain.Task, error) {
	if strings.TrimSpace(title) == "" {
		title = "Untitled task"
	}
	if strings.TrimSpace(goalText) == "" {
		goalText = "demo: recover the flag"
	}
	if budget == nil {
		budget = map[string]any{}
	}
	eng, err := s.newEngine()
	if err != nil {
		return nil, err
	}
	task, err := s.st.CreateTask(ctx, title, mode, budget)
	if err != nil {
		return nil, err
	}
	goal, err := s.st.SetGoal(ctx, task.ID, goalText, nil)
	if err != nil {
		return nil, err
	}
	task.GoalID = goal.ID
	log.Printf("[task %d] started title=%q mode=%s", task.ID, task.Title, task.Mode)
	s.launch(task.ID, eng)
	return task, nil
}

func (s *Server) newEngine() (*kernel.Engine, error) {
	appCfg := s.cfgStore.Get()
	rt, err := s.build(appCfg)
	if err != nil {
		return nil, err
	}
	submitCommand := appCfg.SubmitCommand
	if s.cfg.SubmitCommand != "" {
		submitCommand = s.cfg.SubmitCommand
	}
	var fallback domain.Submitter
	if submitCommand != "" {
		fallback = submit.NewCommand(submitCommand)
	}
	submitter := submit.NewTaskCTFd(s.st, fallback)
	return kernel.New(s.st, rt, kernel.Config{
		MaxWorkers:           s.cfg.MaxWorkers,
		Lease:                10 * time.Minute,
		Scheduler:            s.cfg.Scheduler,
		AutoApproveDangerous: s.cfg.AutoApproveDangerous,
		DrainWindow:          s.cfg.DrainWindow,
		MaxContainers:        s.cfg.MaxContainers,
		Submitter:            submitter,
		CloseChallenge:       s.closeChallenge,
		ReconcileChallenges:  s.reconcileCompletedChallenges,
	}), nil
}

func (s *Server) launch(taskID int64, eng *kernel.Engine) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &taskRun{cancel: cancel, done: make(chan struct{}), engine: eng}
	s.mu.Lock()
	if old := s.runs[taskID]; old != nil {
		old.cancel()
	}
	s.runs[taskID] = run
	s.mu.Unlock()
	go s.runTask(ctx, taskID, run)
}

func (s *Server) runTask(ctx context.Context, taskID int64, run *taskRun) {
	defer close(run.done)
	err := run.engine.Run(ctx, taskID)
	if errors.Is(err, context.DeadlineExceeded) {
		bg := context.Background()
		_ = s.st.SetTaskStatus(bg, taskID, domain.TaskFailed)
		_ = s.st.CancelActiveWork(bg, taskID, "hard task time budget exceeded", true, true)
		s.abandonGoal(bg, taskID, "engine", "任务超出硬时间预算")
		_, _ = s.st.AppendEvent(bg, taskID, domain.EventTaskStopped, "system", "budget", "task", taskID,
			map[string]any{"reason": "hard_max_minutes exceeded"})
		log.Printf("[task %d] hard time budget exceeded", taskID)
	} else if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[api] task %d ended: %v", taskID, err)
		task, _ := s.st.GetTask(context.Background(), taskID)
		if task != nil {
			switch task.Status {
			case domain.TaskCompleted, domain.TaskCancelled, domain.TaskFailed, domain.TaskPaused:
			default:
				bg := context.Background()
				_ = s.st.SetTaskStatus(bg, taskID, domain.TaskFailed)
				_ = s.st.CancelActiveWork(bg, taskID, "engine error", true, true)
				s.abandonGoal(bg, taskID, "engine", "任务执行失败")
				_, _ = s.st.AppendEvent(bg, taskID, domain.EventTaskStopped, "system", "engine", "task", taskID,
					map[string]any{"reason": "error", "error": err.Error()})
			}
		}
	}
	s.mu.Lock()
	if s.runs[taskID] == run {
		delete(s.runs, taskID)
	}
	s.mu.Unlock()
}

// PauseTask stops new planning/execution and marks the task paused.
func (s *Server) PauseTask(ctx context.Context, taskID int64) error {
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	switch task.Status {
	case domain.TaskRunning, domain.TaskPlanning:
	default:
		return fmt.Errorf("cannot pause a task in status %s", task.Status)
	}
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	if run == nil {
		return fmt.Errorf("task %d is not running", taskID)
	}
	log.Printf("[task %d] paused", taskID)
	if err := s.st.SetTaskStatus(ctx, taskID, domain.TaskPaused); err != nil {
		return err
	}
	if goal, _ := s.st.CurrentGoal(ctx, taskID); goal != nil && (goal.State == domain.GoalOpen || goal.State == domain.GoalInProgress) {
		_ = s.st.SetGoalState(ctx, goal.ID, domain.GoalPaused, "human")
	}
	_ = s.st.CancelActiveWork(ctx, taskID, "task paused", false, false)
	run.cancel()
	_, _ = s.st.AppendEvent(ctx, taskID, domain.EventTaskPaused, "human", "local", "task", taskID, nil)
	return nil
}

// ResumeTask restarts a paused task with a fresh engine run.
func (s *Server) ResumeTask(ctx context.Context, taskID int64) error {
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	if task.Status != domain.TaskPaused {
		return fmt.Errorf("task %d is not paused (status %s)", taskID, task.Status)
	}
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	if run != nil {
		select {
		case <-run.done:
		case <-time.After(2 * time.Second):
			return fmt.Errorf("task %d is still stopping; retry in a moment", taskID)
		}
	}
	eng, err := s.newEngine()
	if err != nil {
		return err
	}
	log.Printf("[task %d] resumed", taskID)
	if goal, _ := s.st.CurrentGoal(ctx, taskID); goal != nil && goal.State == domain.GoalPaused {
		_ = s.st.SetGoalState(ctx, goal.ID, domain.GoalInProgress, "human")
	}
	if err := s.st.SetTaskStatus(ctx, taskID, domain.TaskRunning); err != nil {
		return err
	}
	_, _ = s.st.AppendEvent(ctx, taskID, domain.EventTaskResumed, "human", "local", "task", taskID, nil)
	s.launch(taskID, eng)
	return nil
}

// StopTask cancels any live run and marks the task cancelled.
func (s *Server) StopTask(ctx context.Context, taskID int64) error {
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	switch task.Status {
	case domain.TaskCompleted, domain.TaskCancelled, domain.TaskFailed:
		return nil
	}
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	log.Printf("[task %d] stopped", taskID)
	if err := s.st.SetTaskStatus(ctx, taskID, domain.TaskCancelled); err != nil {
		return err
	}
	if run != nil {
		run.cancel()
	}
	_ = s.st.CancelActiveWork(ctx, taskID, "task stopped", true, true)
	s.abandonGoal(ctx, taskID, "human", "任务已停止")
	_, _ = s.st.AppendEvent(ctx, taskID, domain.EventTaskStopped, "human", "local", "task", taskID, nil)
	return nil
}

func (s *Server) abandonGoal(ctx context.Context, taskID int64, by, reason string) {
	goalID, changed, err := s.st.AbandonGoal(ctx, taskID)
	if err != nil || !changed || goalID == 0 {
		return
	}
	_, _ = s.st.AppendEvent(ctx, taskID, domain.EventGoalAbandoned, by, by, "goal", goalID,
		map[string]any{"reason": reason})
}

type statusBar struct {
	Label   string
	Count   int
	Percent int
}

type dashboardData struct {
	Title            string
	Tab              string
	Tasks            []domain.Task
	RecentEvents     []domain.Event
	LatestFindings   []findingRow
	StatusBars       []statusBar
	Active           int
	Total            int
	Running          int
	Completed        int
	SuccessRate      int
	AvgDuration      string
	Findings         int
	Assets           int
	PendingApprovals int
	TokenTotal       int
	TokenInput       int
	TokenOutput      int
	TokenCacheRead   int
	TokenCalls       int
	TaskPagination   PageInfo
	RuntimeName      string
	ProviderName     string
	ProviderModel    string
	ProviderReady    bool
	ConfigPath       string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	tasks, err := s.st.ListTasks(ctx, 5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	appCfg := s.cfgStore.Get()
	p := appCfg.Providers[appCfg.DefaultProvider]
	data := dashboardData{
		Title: "仪表盘", Tasks: tasks, Total: len(tasks),
		RuntimeName: appCfg.Runtime, ProviderName: p.Name, ProviderModel: p.Model,
		ProviderReady: p.APIKey != "", ConfigPath: s.cfgStore.Path(),
	}
	counts := map[string]int{}
	var allFindings []findingRow
	terminal, completedForRate := 0, 0
	var totalDuration time.Duration
	for _, t := range tasks {
		counts[string(t.Status)]++
		switch t.Status {
		case domain.TaskRunning, domain.TaskPlanning, domain.TaskSettling:
			data.Active++
			data.Running++
		case domain.TaskCompleted:
			data.Completed++
		}
		switch t.Status {
		case domain.TaskCompleted, domain.TaskFailed, domain.TaskCancelled:
			terminal++
			if t.Status == domain.TaskCompleted {
				completedForRate++
				end := time.Now()
				if t.EndedAt != nil {
					end = *t.EndedAt
				}
				start := t.CreatedAt
				if t.StartedAt != nil {
					start = *t.StartedAt
				}
				if end.After(start) {
					totalDuration += end.Sub(start)
				}
			}
		}
		findings, _ := s.st.ListFindings(ctx, t.ID, 1000)
		for _, f := range findings {
			allFindings = append(allFindings, findingRow{TaskTitle: t.Title, Finding: f})
		}
		data.Findings += len(findings)
		pending, _ := s.st.PendingApprovals(ctx, t.ID)
		data.PendingApprovals += len(pending)
	}
	sort.Slice(allFindings, func(i, j int) bool {
		return allFindings[i].Finding.CreatedAt.After(allFindings[j].Finding.CreatedAt)
	})
	if len(allFindings) > 100 {
		allFindings = allFindings[:100]
	}
	data.LatestFindings = allFindings
	if terminal > 0 {
		data.SuccessRate = completedForRate * 100 / terminal
	}
	if completedForRate > 0 {
		data.AvgDuration = (totalDuration / time.Duration(completedForRate)).Round(time.Second).String()
	} else {
		data.AvgDuration = "—"
	}

	statuses := []struct {
		Key   string
		Label string
	}{
		{"running", "运行中"}, {"paused", "已暂停"}, {"completed", "已完成"},
		{"cancelled", "已停止"}, {"failed", "失败"},
	}
	maxCount := 0
	for _, st := range statuses {
		if counts[st.Key] > maxCount {
			maxCount = counts[st.Key]
		}
	}
	for _, st := range statuses {
		pct := 0
		if maxCount > 0 {
			pct = counts[st.Key] * 100 / maxCount
		}
		data.StatusBars = append(data.StatusBars, statusBar{Label: st.Label, Count: counts[st.Key], Percent: pct})
	}

	assets, _ := s.st.ListAssets(ctx, 5000)
	data.Assets = len(assets)
	usage, _ := s.st.TokenUsage(ctx)
	data.TokenInput, data.TokenOutput = usage.Input, usage.Output
	data.TokenTotal = usage.Input + usage.Output
	data.TokenCacheRead, data.TokenCalls = usage.CacheRead, usage.Calls
	data.RecentEvents, _ = s.st.ListRecentEvents(ctx, 50)
	data.Tasks = tasks
	s.render(w, "dashboard.html", data)
}

type traceCall struct {
	Call    domain.LLMCall
	System  []string
	Prompt  string
	Tools   []string
	Usage   string
	IsError bool
}

type traceGroup struct {
	Key         string
	Title       string
	Status      string
	IsTaskLevel bool
	Works       []*domain.Work
	Calls       []traceCall
	Activities  []domain.Activity
}

type taskData struct {
	Title                   string
	Tab                     string
	Task                    *domain.Task
	Goal                    *domain.Goal
	Counts                  map[string]int
	Intents                 []*domain.Intent
	IntentByID              map[int64]*domain.Intent
	Works                   []*domain.Work
	Findings                []domain.Finding
	Assets                  []domain.Asset
	Events                  []domain.Event
	Activities              []domain.Activity
	Pending                 []domain.Approval
	Pagination              PageInfo
	WorkPagination          PageInfo
	IntentTotal             int
	WorkTotal               int
	FindingTotal            int
	AssetTotal              int
	EventTotal              int
	ActivityTotal           int
	PendingTotal            int
	TraceGroups             []traceGroup
	TraceNav                []traceGroup
	TraceChallenge          string
	TracePagination         PageInfo
	TraceActivityPagination PageInfo
	Flags                   []domain.TaskFlag
	FlagTotal               int
	FlagPagination          PageInfo
	TraceCalls              int
	TraceTools              int
	TraceErrors             int
	Graph                   *taskGraphView
	Swarms                  []swarmView
	Quarantine              []domain.QuarantineItem
	QuarantineTotal         int
	QuarantinePagination    PageInfo
	Conflicts               []domain.Conflict
	ConflictTotal           int
	TargetStates            []domain.TargetStateView
	IntentPlans             map[int64]*swarmView
	ActiveContainers        int
	MaxContainers           int
	ReportRows              []reportFindingRow
	ReportPagination        PageInfo
	ReportTotal             int
}

type reportFindingRow struct {
	VulnType    string
	Description string
	Assets      string
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	taskID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "export" && r.Method == http.MethodGet {
		s.exportTaskReport(w, r, taskID)
		return
	}

	if len(parts) == 3 && parts[1] == "lanes" && r.Method == http.MethodGet {
		laneID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.renderLane(w, r, taskID, laneID)
		return
	}
	if len(parts) == 4 && parts[1] == "conflicts" && parts[3] == "resolve" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		items, _ := s.st.ListConflicts(r.Context(), taskID, 1000)
		found := false
		for _, item := range items {
			if item.ID == id {
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		decision := r.FormValue("decision")
		switch decision {
		case "keep_all", "quarantine_all", "needs_human":
		default:
			http.Error(w, "invalid conflict decision", http.StatusBadRequest)
			return
		}
		if err := s.st.ResolveConflict(r.Context(), id, decision, "web review"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=conflicts", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 4 && parts[1] == "quarantine" && parts[3] == "promote" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		item, err := s.st.GetQuarantine(r.Context(), id)
		if err != nil || item == nil || item.TaskID != taskID {
			http.NotFound(w, r)
			return
		}
		if _, err := s.st.PromoteQuarantine(r.Context(), id, "web"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=quarantine", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 4 && parts[1] == "quarantine" && parts[3] == "status" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		item, err := s.st.GetQuarantine(r.Context(), id)
		if err != nil || item == nil || item.TaskID != taskID {
			http.NotFound(w, r)
			return
		}
		status := r.FormValue("status")
		switch status {
		case "open", "promoted", "dismissed":
		default:
			http.Error(w, "invalid quarantine status", http.StatusBadRequest)
			return
		}
		if err := s.st.SetQuarantineStatus(r.Context(), id, status, "web"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=quarantine", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) >= 2 && parts[1] == "flags" && r.Method == http.MethodPost {
		if len(parts) == 3 && parts[2] == "delete" {
			id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
			if err := s.st.DeleteTaskFlag(r.Context(), taskID, id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=flags", taskID), http.StatusSeeOther)
			return
		}
		challenge := strings.TrimSpace(r.FormValue("challenge"))
		flag := strings.TrimSpace(r.FormValue("flag"))
		if flag == "" {
			http.Error(w, "flag 不能为空", http.StatusBadRequest)
			return
		}
		if err := s.st.AddTaskFlag(r.Context(), taskID, challenge, flag, r.FormValue("status")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=flags", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 2 && parts[1] == "delete" && r.Method == http.MethodPost {
		if err := s.DeleteTask(r.Context(), taskID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/tasks", http.StatusSeeOther)
		return
	}
	if len(parts) == 2 && parts[1] == "rename" && r.Method == http.MethodPost {
		title := strings.TrimSpace(r.FormValue("title"))
		if title == "" {
			http.Error(w, "任务标题不能为空", http.StatusBadRequest)
			return
		}
		if err := s.st.RenameTask(r.Context(), taskID, title); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 3 && parts[1] == "chat" && parts[2] == "stop" && r.Method == http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]any{"stopped": s.stopTaskChat(r.Context(), taskID)})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		var err error
		switch parts[1] {
		case "pause":
			err = s.PauseTask(r.Context(), taskID)
		case "resume":
			err = s.ResumeTask(r.Context(), taskID)
		case "stop":
			err = s.StopTask(r.Context(), taskID)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d", taskID), http.StatusSeeOther)
		return
	}

	if len(parts) == 3 && parts[1] == "approvals" && r.Method == http.MethodPost {
		approvalID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		decision := domain.ApprovalDecision(r.FormValue("decision"))
		if decision != domain.ApprovalApproved && decision != domain.ApprovalRejected {
			http.Error(w, "decision must be approved or rejected", http.StatusBadRequest)
			return
		}
		reason := r.FormValue("reason")
		s.resolveApproval(r.Context(), taskID, approvalID, decision, "human:local", reason)
		next := r.FormValue("next")
		if !strings.HasPrefix(next, "/") {
			next = fmt.Sprintf("/tasks/%d?tab=approvals", taskID)
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodGet || len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	s.renderTask(w, r, taskID)
}

func formatTracePrompt(system []string, messages []llm.Message) string {
	var b strings.Builder
	for _, s := range system {
		if strings.TrimSpace(s) == "" {
			continue
		}
		b.WriteString("[system]\n")
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	for _, m := range messages {
		for _, c := range m.Content {
			switch c.Type {
			case llm.BlockText:
				if strings.TrimSpace(c.Text) == "" {
					continue
				}
				fmt.Fprintf(&b, "[%s]\n%s\n\n", m.Role, c.Text)
			case llm.BlockThinking:
				if strings.TrimSpace(c.Thinking) == "" {
					continue
				}
				fmt.Fprintf(&b, "[assistant thinking]\n%s\n\n", c.Thinking)
			case llm.BlockToolUse:
				fmt.Fprintf(&b, "[assistant tool_use: %s]\n%s\n\n", c.Name, string(c.Input))
			case llm.BlockToolResult:
				var parts []string
				for _, inner := range c.Content {
					if inner.Text != "" {
						parts = append(parts, inner.Text)
					}
				}
				fmt.Fprintf(&b, "[tool_result]\n%s\n\n", strings.Join(parts, "\n"))
			}
		}
	}
	text := strings.TrimSpace(b.String())
	if len([]rune(text)) > 32000 {
		text = string([]rune(text)[:32000]) + "\n…（已截断，完整内容见请求）"
	}
	return text
}

func buildTaskTrace(calls []domain.LLMCall, activities []domain.Activity, works []*domain.Work, intents map[int64]*domain.Intent, findings []domain.Finding, flags []domain.TaskFlag) ([]traceGroup, int, int, int) {
	findingsByIntent := map[int64]string{}
	for _, f := range findings {
		findingsByIntent[f.IntentID] += f.Title + "\n" + f.Evidence + "\n"
	}
	byWork := map[int64]int{}
	groupIndex := map[string]int{}
	groups := []traceGroup{}
	for _, work := range works {
		intent := intents[work.IntentID]
		text := work.StopReason + "\n" + findingsByIntent[work.IntentID]
		if intent != nil {
			text += intent.Summary
		}
		key, title := traceChallenge(text, intent)
		idx, ok := groupIndex[key]
		if !ok {
			idx = len(groups)
			groupIndex[key] = idx
			groups = append(groups, traceGroup{Key: key, Title: title})
		}
		groups[idx].Works = append(groups[idx].Works, work)
		byWork[work.ID] = idx
	}
	solved := map[string]bool{}
	for _, f := range flags {
		if f.Challenge != "" && f.Challenge != "未标注" {
			solved[strings.ToLower(f.Challenge)] = true
		}
	}
	for i := range groups {
		switch {
		case groups[i].Key == "planning" || groups[i].Key == "prep":
			groups[i].Status = "other"
		case solved[strings.ToLower(groups[i].Key)]:
			groups[i].Status = "solved"
		default:
			failed := 0
			for _, w := range groups[i].Works {
				if w.State == domain.WorkFailed || w.State == domain.WorkCancelled || w.State == domain.WorkLost || w.State == domain.WorkTimedOut {
					failed++
				}
			}
			if len(groups[i].Works) > 0 && failed == len(groups[i].Works) {
				groups[i].Status = "failed"
			} else {
				groups[i].Status = "active"
			}
		}
	}
	taskLevel := traceGroup{Key: "planning", Title: "任务级规划", Status: "other", IsTaskLevel: true}
	for _, act := range activities {
		if act.WorkID == 0 {
			taskLevel.Activities = append(taskLevel.Activities, act)
			continue
		}
		if idx, ok := byWork[act.WorkID]; ok {
			groups[idx].Activities = append(groups[idx].Activities, act)
		}
	}
	totalCalls, totalTools, totalErrors := 0, 0, 0
	for i := len(calls) - 1; i >= 0; i-- { // store returns newest first
		call := calls[i]
		var req struct {
			System   []string
			Messages []llm.Message
			Tools    []struct{ Name string }
		}
		_ = json.Unmarshal([]byte(call.RequestJSON), &req)
		var usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		}
		_ = json.Unmarshal([]byte(call.UsageJSON), &usage)
		tc := traceCall{
			Call: call, System: req.System, Prompt: formatTracePrompt(req.System, req.Messages),
			Usage: fmt.Sprintf("%d / %d", usage.Input, usage.Output), IsError: call.Error != "",
		}
		for _, t := range req.Tools {
			tc.Tools = append(tc.Tools, t.Name)
		}
		totalCalls++
		totalTools += len(tc.Tools)
		if tc.IsError {
			totalErrors++
		}
		if call.WorkID == 0 {
			taskLevel.Calls = append(taskLevel.Calls, tc)
			continue
		}
		if idx, ok := byWork[call.WorkID]; ok {
			groups[idx].Calls = append(groups[idx].Calls, tc)
		} else {
			taskLevel.Calls = append(taskLevel.Calls, tc)
		}
	}
	if len(taskLevel.Calls) > 0 || len(taskLevel.Activities) > 0 {
		groups = append([]traceGroup{taskLevel}, groups...)
	}
	return groups, totalCalls, totalTools, totalErrors
}

var traceChallengePattern = regexp.MustCompile(`(?i)\b([a-z]{1,3}\d{0,3}-\d{1,3}|[a-z]{1,2}\d{1,3})\b`)

func traceChallenge(text string, intent *domain.Intent) (string, string) {
	counts := map[string]int{}
	order := []string{}
	for _, m := range traceChallengePattern.FindAllStringSubmatch(text, -1) {
		code := strings.ToLower(m[1])
		if strings.HasPrefix(code, "v") && !strings.Contains(code, "-") {
			continue
		}
		if _, ok := counts[code]; !ok {
			order = append(order, code)
		}
		counts[code]++
	}
	best, bestN := "", 0
	for _, code := range order {
		if counts[code] > bestN {
			best, bestN = code, counts[code]
		}
	}
	if best != "" {
		return best, strings.ToUpper(best)
	}
	return "prep", "准备 / 侦察 / 其他"
}

// DeleteTask stops a live run, then permanently deletes the task and its
// task-scoped artifacts. It refuses to delete while the engine is still stopping.
func (s *Server) DeleteTask(ctx context.Context, taskID int64) error {
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	if run != nil {
		run.cancel()
		select {
		case <-run.done:
		case <-time.After(10 * time.Second):
			return fmt.Errorf("task %d is still stopping; retry in a moment", taskID)
		}
	}
	s.chatMu.Lock()
	if cancel := s.chatCancel[taskID]; cancel != nil {
		cancel()
		delete(s.chatCancel, taskID)
	}
	s.chatMu.Unlock()
	if err := s.archiveLLMCalls(ctx, "task", taskID, task.Title); err != nil {
		return fmt.Errorf("archive task LLM records: %w", err)
	}
	if err := s.st.DeleteTask(ctx, taskID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.runs, taskID)
	s.mu.Unlock()
	cfg := s.cfgStore.Get()
	if strings.TrimSpace(cfg.ArtifactDir) != "" {
		if err := os.RemoveAll(filepath.Join(cfg.ArtifactDir, fmt.Sprintf("task-%d", taskID))); err != nil {
			log.Printf("[task %d] remove files: %v", taskID, err)
		}
	}
	log.Printf("[task %d] deleted", taskID)
	return nil
}

func (s *Server) renderLane(w http.ResponseWriter, r *http.Request, taskID, workID int64) {
	ctx := r.Context()
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil || task == nil {
		http.NotFound(w, r)
		return
	}
	work, err := s.st.GetWork(ctx, workID)
	if err != nil || work == nil || work.TaskID != taskID || work.LaneID == "" {
		http.NotFound(w, r)
		return
	}
	intent, _ := s.st.GetIntent(ctx, work.IntentID)
	run, _ := s.st.GetSwarmRun(ctx, work.IntentID)
	allResults, _ := s.st.ListLaneResults(ctx, work.IntentID)
	var result *store.SwarmLaneResult
	for i := range allResults {
		if allResults[i].WorkID == workID {
			result = &allResults[i]
		}
	}
	allCalls, _ := s.st.ListLLMCalls(ctx, taskID, 5000)
	var calls []domain.LLMCall
	for _, c := range allCalls {
		if c.WorkID == workID {
			calls = append(calls, c)
		}
	}
	allActs, _ := s.st.ListActivities(ctx, taskID, 5000)
	var acts []domain.Activity
	for _, a := range allActs {
		if a.WorkID == workID {
			acts = append(acts, a)
		}
	}
	allQ, _ := s.st.ListQuarantine(ctx, taskID, 1000)
	var qs []domain.QuarantineItem
	for _, q := range allQ {
		if q.WorkID == workID {
			qs = append(qs, q)
		}
	}
	artifacts, _ := s.st.ListWorkArtifacts(ctx, taskID, workID, 100)
	s.render(w, "lane.html", newLaneView(task, work, intent, run, result, calls, acts, qs, artifacts))
}

func (s *Server) renderTask(w http.ResponseWriter, r *http.Request, taskID int64) {
	ctx := r.Context()
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil || task == nil {
		http.NotFound(w, r)
		return
	}
	goal, _ := s.st.CurrentGoal(ctx, taskID)
	counts, _ := s.st.GraphCounts(ctx, taskID)
	intents, _ := s.st.ListIntents(ctx, taskID)
	works, _ := s.st.ListWorks(ctx, taskID)
	findings, _ := s.st.ListFindings(ctx, taskID, 5000)
	assets, _ := s.st.ListTaskAssets(ctx, taskID, 5000)
	events, _ := s.st.ListEvents(ctx, taskID, 5000)
	activities, _ := s.st.ListActivities(ctx, taskID, 5000)
	pending, _ := s.st.PendingApprovals(ctx, taskID)
	byID := map[int64]*domain.Intent{}
	for _, in := range intents {
		byID[in.ID] = in
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "overview"
	}
	data := taskData{
		Title: fmt.Sprintf("#%d %s", task.ID, task.Title), Tab: tab,
		Task: task, Goal: goal, Counts: counts, IntentByID: byID,
		IntentTotal: len(intents), WorkTotal: len(works), FindingTotal: len(findings),
		AssetTotal: len(assets), EventTotal: len(events), ActivityTotal: len(activities),
		PendingTotal: len(pending),
	}
	intentPage := newPageInfo(r, len(intents))
	workPage := newPageInfoFor(r, len(works), "wpage")
	activityPage := newPageInfo(r, len(activities))
	findingPage := newPageInfo(r, len(findings))
	assetPage := newPageInfo(r, len(assets))
	eventPage := newPageInfo(r, len(events))
	pendingPage := newPageInfo(r, len(pending))

	switch tab {
	case "flags":
		_ = s.st.SyncTaskFlags(ctx, taskID)
		flags, _ := s.st.ListTaskFlags(ctx, taskID)
		data.FlagTotal = len(flags)
		data.FlagPagination = newPageInfoSize(r, len(flags), "fpage", 8)
		data.Flags = paginateSlice(flags, data.FlagPagination)
	case "graph":
		g, _ := s.st.ListTaskGraph(ctx, taskID, 200)
		total := 0
		for _, n := range counts {
			total += n
		}
		data.Graph = buildTaskGraph(g, total)
	case "trace":
		calls, _ := s.st.ListLLMCalls(ctx, taskID, 5000)
		_ = s.st.SyncTaskFlags(ctx, taskID)
		flags, _ := s.st.ListTaskFlags(ctx, taskID)
		data.TraceGroups, data.TraceCalls, data.TraceTools, data.TraceErrors = buildTaskTrace(calls, activities, works, byID, findings, flags)
		data.TraceNav = data.TraceGroups
		key := strings.TrimSpace(r.URL.Query().Get("challenge"))
		data.TraceChallenge = key
		if key == "" {
			data.TraceGroups = nil
		} else {
			for _, g := range data.TraceGroups {
				if g.Key == key {
					cp := newPageInfoSize(r, len(g.Calls), "cpage", 5)
					g.Calls = paginateSlice(g.Calls, cp)
					ap := newPageInfoSize(r, len(g.Activities), "apage", 5)
					g.Activities = paginateSlice(g.Activities, ap)
					data.TraceGroups = []traceGroup{g}
					data.TracePagination, data.TraceActivityPagination = cp, ap
					break
				}
			}
		}
	case "plan":
		data.Intents = paginateSlice(intents, intentPage)
		data.Pagination = intentPage
		plans := map[int64]*swarmView{}
		if runs, err := s.st.ListSwarmRuns(ctx, taskID); err == nil {
			for _, run := range runs {
				lanes, _ := s.st.ListLaneResults(ctx, run.IntentID)
				v := newSwarmView(run, lanes)
				plans[run.IntentID] = &v
			}
		}
		data.IntentPlans = plans
		data.MaxContainers = s.cfg.MaxContainers
		s.mu.Lock()
		if run := s.runs[taskID]; run != nil && run.engine != nil {
			data.TargetStates = run.engine.TargetStates()
		}
		s.mu.Unlock()
		for _, w := range works {
			if w.LaneID != "" && (w.State == domain.WorkRunning || w.State == domain.WorkLeased) {
				data.ActiveContainers++
			}
		}
	case "conflicts":
		data.Conflicts, _ = s.st.ListConflicts(ctx, taskID, 100)
		data.ConflictTotal = len(data.Conflicts)
	case "report":
		assetByID := map[int64]string{}
		for _, a := range assets {
			assetByID[a.ID] = a.Value
		}
		rows := make([]reportFindingRow, 0, len(findings))
		for _, f := range findings {
			desc := strings.TrimSpace(f.Evidence)
			if desc == "" {
				desc = strings.TrimSpace(f.SubjectKey)
			}
			var assetValues []string
			for _, id := range f.AssetIDs {
				if v := strings.TrimSpace(assetByID[id]); v != "" {
					assetValues = append(assetValues, v)
				}
			}
			if len(assetValues) == 0 && strings.TrimSpace(f.SubjectKey) != "" {
				assetValues = append(assetValues, f.SubjectKey)
			}
			rows = append(rows, reportFindingRow{
				VulnType:    strings.TrimSpace(f.Title),
				Description: desc,
				Assets:      strings.Join(assetValues, ", "),
			})
		}
		data.ReportTotal = len(rows)
		data.ReportPagination = newPageInfoSize(r, len(rows), "rpage", 8)
		data.ReportRows = paginateSlice(rows, data.ReportPagination)
	case "quarantine":
		items, _ := s.st.ListQuarantine(ctx, taskID, 1000)
		data.QuarantineTotal = len(items)
		data.QuarantinePagination = newPageInfoSize(r, len(items), "qpage", 10)
		data.Quarantine = paginateSlice(items, data.QuarantinePagination)
	case "sessions":
		data.Works = paginateSlice(works, workPage)
		data.WorkPagination = workPage
		data.Activities = paginateSlice(activities, activityPage)
		data.Pagination = activityPage
		if runs, err := s.st.ListSwarmRuns(ctx, taskID); err == nil {
			for _, run := range runs {
				lanes, _ := s.st.ListLaneResults(ctx, run.IntentID)
				data.Swarms = append(data.Swarms, newSwarmView(run, lanes))
			}
		}
	case "approvals":
		data.Pending = paginateSlice(pending, pendingPage)
		data.Pagination = pendingPage
	case "findings":
		data.Findings = paginateSlice(findings, findingPage)
		data.Pagination = findingPage
	case "assets":
		data.Assets = paginateSlice(assets, assetPage)
		data.Pagination = assetPage
	case "events":
		data.Events = paginateSlice(events, eventPage)
		data.Pagination = eventPage
	default:
		data.Intents = intents
		data.Works = works
		if len(events) > 10 {
			data.Events = events[:10]
		} else {
			data.Events = events
		}
		if len(findings) > 10 {
			data.Findings = findings[:10]
		} else {
			data.Findings = findings
		}
	}
	s.render(w, "task.html", data)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.renderTasksPage(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/tasks", http.StatusSeeOther)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "解析上传文件失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "任务标题不能为空", http.StatusBadRequest)
		return
	}
	mode := domain.TaskMode(r.FormValue("mode"))
	if mode != domain.ModePentest {
		mode = domain.ModeCTF
	}
	goal := r.FormValue("goal")
	budget := map[string]any{}
	// CTF credentials are optional and may be embedded directly in the goal.
	// If a URL + token is present, use it for flag submission; otherwise the
	// agent starts with reconnaissance and decides whether the task is viable.
	if mode == domain.ModeCTF {
		if ctf, err := parseCTFConfig(goal); err == nil {
			budget["ctf"] = ctf
		}
	}
	task, err := s.StartTaskWithBudget(r.Context(), title, mode, goal, budget)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.MultipartForm != nil {
		atts, err := s.saveUploads(r.Context(), task.ID, "uploads", r.MultipartForm.File["files"], parseUploadPaths(r.FormValue("file_paths")), true)
		if err != nil {
			log.Printf("[task %d] save uploads: %v", task.ID, err)
		} else if len(atts) > 0 {
			task.Budget["attachments"] = atts
			_ = s.st.UpdateTaskBudget(r.Context(), task.ID, task.Budget)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/tasks/%d", task.ID), http.StatusSeeOther)
}

type tasksPageData struct {
	Title      string
	Tab        string
	Tasks      []domain.Task
	Pagination PageInfo
	Synced     int
}

func (s *Server) renderTasksPage(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.st.ListTasks(r.Context(), 5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := newPageInfo(r, len(tasks))
	synced, _ := strconv.Atoi(r.URL.Query().Get("synced"))
	s.render(w, "tasks.html", tasksPageData{Title: "任务", Tab: "tasks", Tasks: paginateSlice(tasks, page), Pagination: page, Synced: synced})
}

type findingRow struct {
	TaskTitle string
	Finding   domain.Finding
}

type findingsPageData struct {
	Title          string
	Tab            string
	Findings       []findingRow
	Pagination     PageInfo
	TaskGroups     []trafficGroup
	TaskPagination PageInfo
	Mode           string
	GroupTitle     string
	BackURL        string
	FilterTaskID   int64
	SearchQuery    string
	SearchClearURL string
}

func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tasks, _ := s.st.ListTasks(ctx, 5000)
	titles := map[int64]string{}
	for _, t := range tasks {
		titles[t.ID] = t.Title
	}
	if deleted, _ := s.st.DeletedTitles(ctx, "task"); len(deleted) > 0 {
		for id, title := range deleted {
			if titles[id] == "" {
				titles[id] = title
			}
		}
	}
	qTask, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(search)) > 200 {
		search = string([]rune(search)[:200])
	}
	if qTask > 0 {
		total, _ := s.st.CountFindingsSearch(ctx, qTask, search)
		page := newPageInfo(r, total)
		findings, _ := s.st.ListFindingsPageSearch(ctx, qTask, search, page.PerPage, (page.Page-1)*page.PerPage)
		rows := make([]findingRow, 0, len(findings))
		for _, f := range findings {
			rows = append(rows, findingRow{TaskTitle: titles[qTask], Finding: f})
		}
		s.render(w, "findings.html", findingsPageData{
			Title: "发现", Tab: "findings", Findings: rows, Pagination: page,
			Mode: "task", GroupTitle: fmt.Sprintf("任务 #%d %s", qTask, titles[qTask]),
			BackURL: "/findings", FilterTaskID: qTask, SearchQuery: search,
			SearchClearURL: fmt.Sprintf("/findings?task=%d", qTask),
		})
		return
	}
	groups, _ := s.st.FindingGroups(ctx)
	taskGroups := make([]trafficGroup, 0, len(groups))
	for _, g := range groups {
		taskGroups = append(taskGroups, trafficGroup{Kind: "任务", TaskID: g.TaskID, Title: titles[g.TaskID],
			Count: g.Count, LastAt: g.LastAt, URL: fmt.Sprintf("/findings?task=%d", g.TaskID)})
	}
	sort.Slice(taskGroups, func(i, j int) bool {
		if taskGroups[i].LastAt.Equal(taskGroups[j].LastAt) {
			return taskGroups[i].Count > taskGroups[j].Count
		}
		return taskGroups[i].LastAt.After(taskGroups[j].LastAt)
	})
	page := newPageInfoSize(r, len(taskGroups), "tpage", 5)
	s.render(w, "findings.html", findingsPageData{
		Title: "发现", Tab: "findings", TaskGroups: paginateSlice(taskGroups, page), TaskPagination: page,
	})
}

type assetsPageData struct {
	Title          string
	Tab            string
	Assets         []domain.Asset
	Pagination     PageInfo
	TaskGroups     []trafficGroup
	TaskPagination PageInfo
	Mode           string
	GroupTitle     string
	BackURL        string
	FilterTaskID   int64
	SearchQuery    string
	SearchClearURL string
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tasks, _ := s.st.ListTasks(ctx, 5000)
	titles := map[int64]string{}
	for _, t := range tasks {
		titles[t.ID] = t.Title
	}
	if deleted, _ := s.st.DeletedTitles(ctx, "task"); len(deleted) > 0 {
		for id, title := range deleted {
			if titles[id] == "" {
				titles[id] = title
			}
		}
	}
	qTask, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len([]rune(search)) > 200 {
		search = string([]rune(search)[:200])
	}
	match := func(a domain.Asset) bool {
		if search == "" {
			return true
		}
		return strings.Contains(strings.ToLower(a.Type+" "+a.Value+" "+a.CanonicalKey+" "+a.Source), search)
	}
	if qTask > 0 {
		assets, _ := s.st.ListTaskAssets(ctx, qTask, 5000)
		filtered := assets[:0]
		for _, a := range assets {
			if match(a) {
				filtered = append(filtered, a)
			}
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].LastSeen.After(filtered[j].LastSeen) })
		page := newPageInfo(r, len(filtered))
		s.render(w, "assets.html", assetsPageData{
			Title: "资产", Tab: "assets", Assets: paginateSlice(filtered, page), Pagination: page,
			Mode: "task", GroupTitle: fmt.Sprintf("任务 #%d %s", qTask, titles[qTask]),
			BackURL: "/assets", FilterTaskID: qTask, SearchQuery: search,
			SearchClearURL: fmt.Sprintf("/assets?task=%d", qTask),
		})
		return
	}
	groups := make([]trafficGroup, 0)
	for _, t := range tasks {
		assets, _ := s.st.ListTaskAssets(ctx, t.ID, 5000)
		if len(assets) == 0 {
			continue
		}
		var last time.Time
		for _, a := range assets {
			if a.LastSeen.After(last) {
				last = a.LastSeen
			}
		}
		groups = append(groups, trafficGroup{Kind: "任务", TaskID: t.ID, Title: t.Title,
			Count: len(assets), LastAt: last, URL: fmt.Sprintf("/assets?task=%d", t.ID)})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].LastAt.After(groups[j].LastAt) })
	page := newPageInfoSize(r, len(groups), "tpage", 5)
	s.render(w, "assets.html", assetsPageData{Title: "资产", Tab: "assets", TaskGroups: paginateSlice(groups, page), TaskPagination: page})
}

type approvalRow struct {
	TaskTitle     string
	IntentSummary string
	Approval      domain.Approval
}

type approvalsPageData struct {
	Title          string
	Tab            string
	Approvals      []approvalRow
	Pagination     PageInfo
	TaskGroups     []trafficGroup
	TaskPagination PageInfo
	Mode           string
	GroupTitle     string
	BackURL        string
	FilterTaskID   int64
	SearchQuery    string
	SearchClearURL string
}

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tasks, _ := s.st.ListTasks(ctx, 5000)
	titles := map[int64]string{}
	for _, t := range tasks {
		titles[t.ID] = t.Title
	}
	if deleted, _ := s.st.DeletedTitles(ctx, "task"); len(deleted) > 0 {
		for id, title := range deleted {
			if titles[id] == "" {
				titles[id] = title
			}
		}
	}
	var all []approvalRow
	for _, t := range tasks {
		pending, _ := s.st.PendingApprovals(ctx, t.ID)
		for _, a := range pending {
			summary := ""
			if in, _ := s.st.GetIntent(ctx, a.IntentID); in != nil {
				summary = in.Summary
			}
			all = append(all, approvalRow{TaskTitle: t.Title, IntentSummary: summary, Approval: a})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Approval.CreatedAt.After(all[j].Approval.CreatedAt) })
	qTask, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if qTask > 0 {
		filtered := all[:0]
		for _, row := range all {
			if row.Approval.TaskID != qTask {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(row.IntentSummary+" "+row.TaskTitle+" "+string(row.Approval.Risk)), search) {
				continue
			}
			filtered = append(filtered, row)
		}
		page := newPageInfo(r, len(filtered))
		s.render(w, "approvals.html", approvalsPageData{
			Title: "审批", Tab: "approvals", Approvals: paginateSlice(filtered, page), Pagination: page,
			Mode: "task", GroupTitle: fmt.Sprintf("任务 #%d %s", qTask, titles[qTask]), BackURL: "/approvals",
			FilterTaskID: qTask, SearchQuery: search, SearchClearURL: fmt.Sprintf("/approvals?task=%d", qTask),
		})
		return
	}
	counts := map[int64]int{}
	last := map[int64]time.Time{}
	for _, row := range all {
		counts[row.Approval.TaskID]++
		if row.Approval.CreatedAt.After(last[row.Approval.TaskID]) {
			last[row.Approval.TaskID] = row.Approval.CreatedAt
		}
	}
	groups := make([]trafficGroup, 0, len(counts))
	for id, n := range counts {
		groups = append(groups, trafficGroup{Kind: "任务", TaskID: id, Title: titles[id], Count: n, LastAt: last[id], URL: fmt.Sprintf("/approvals?task=%d", id)})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].LastAt.After(groups[j].LastAt) })
	page := newPageInfoSize(r, len(groups), "tpage", 5)
	s.render(w, "approvals.html", approvalsPageData{Title: "审批", Tab: "approvals", TaskGroups: paginateSlice(groups, page), TaskPagination: page})
}

type eventsPageData struct {
	Title      string
	Tab        string
	Events     []domain.Event
	Pagination PageInfo
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.st.ListRecentEvents(r.Context(), 5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := newPageInfo(r, len(events))
	s.render(w, "events.html", eventsPageData{Title: "事件", Tab: "events", Events: paginateSlice(events, page), Pagination: page})
}

type apiPageData struct {
	Title string
	Tab   string
}

func (s *Server) handleAPIPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "api.html", apiPageData{Title: "API", Tab: "api"})
}

type settingsData struct {
	Title        string
	Tab          string
	Config       config.Config
	Names        []string
	Masked       map[string]string
	Saved        bool
	Tested       string
	TestProvider string
	TestOK       bool
	TestMessage  string
	TestLatency  string
	TestModel    string
}

type providerData struct {
	Title       string
	Tab         string
	Key         string
	Provider    config.Provider
	IsNew       bool
	Presets     []config.Preset
	PresetKey   string
	Masked      string
	Tested      bool
	TestOK      bool
	TestMessage string
	TestLatency string
	TestModel   string
}

type presetGroupView struct {
	Category string
	Presets  []config.Preset
	Added    map[string]bool
}

type presetsData struct {
	Title  string
	Tab    string
	Query  string
	Groups []presetGroupView
}

func (s *Server) settingsView(cfg config.Config, saved bool) settingsData {
	names := cfg.Names()
	masked := map[string]string{}
	for _, name := range names {
		masked[name] = config.MaskedKey(cfg.Providers[name].APIKey)
	}
	return settingsData{
		Title: "Settings", Tab: "settings", Config: cfg, Names: names,
		Masked: masked, Saved: saved,
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		cfg := s.cfgStore.Get()
		if v := r.FormValue("runtime"); v == "fake" || v == "norma" {
			cfg.Runtime = v
		}
		if v := strings.TrimSpace(r.FormValue("default_provider")); v != "" {
			cfg.DefaultProvider = v
		}
		if v := strings.TrimSpace(r.FormValue("artifact_dir")); v != "" {
			cfg.ArtifactDir = v
		}
		cfg.SubmitCommand = r.FormValue("submit_command")
		cfg.Normalize()
		if err := s.cfgStore.Save(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
		return
	}
	s.render(w, "settings.html", s.settingsView(s.cfgStore.Get(), r.URL.Query().Get("saved") == "1"))
}

func (s *Server) providerFromForm(r *http.Request, key string) config.Provider {
	cfg := s.cfgStore.Get()
	p, exists := cfg.Providers[key]
	if !exists {
		if preset, ok := config.PresetByKey(r.FormValue("preset")); ok {
			p = config.Provider{
				Name: preset.Name, Description: preset.Description, Format: preset.Format,
				BaseURL: preset.BaseURL, Model: preset.Model,
			}
		}
	}
	if v := strings.TrimSpace(r.FormValue("name")); v != "" {
		p.Name = v
	}
	if v := strings.TrimSpace(r.FormValue("description")); v != "" {
		p.Description = v
	}
	if v := strings.TrimSpace(r.FormValue("format")); v != "" {
		p.Format = v
	}
	if v := strings.TrimSpace(r.FormValue("base_url")); v != "" {
		p.BaseURL = v
	}
	if v := strings.TrimSpace(r.FormValue("model")); v != "" {
		p.Model = v
	}
	if r.FormValue("clear_key") == "1" {
		p.APIKey = ""
	}
	if v := strings.TrimSpace(r.FormValue("api_key")); v != "" {
		p.APIKey = v
	}
	return p
}

func (s *Server) providerView(key string, p config.Provider, isNew bool, presetKey string) providerData {
	return providerData{
		Title: "Provider", Tab: "settings", Key: key, Provider: p, IsNew: isNew,
		Presets: config.Presets(), PresetKey: presetKey, Masked: config.MaskedKey(p.APIKey),
	}
}

func (s *Server) handleSettingsTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	presetKey := strings.TrimSpace(r.FormValue("preset"))
	if key == "" {
		if presetKey != "" {
			key = presetKey
		} else {
			key = "custom"
		}
	}
	cfg := s.cfgStore.Get()
	_, exists := cfg.Providers[key]
	p := s.providerFromForm(r, key)
	data := s.providerView(key, p, !exists, presetKey)
	data.Tested = true
	res, err := llmtest.Provider(r.Context(), p)
	if err != nil {
		data.TestMessage = err.Error()
	} else {
		data.TestOK = true
		data.TestMessage = res.Text
		data.TestLatency = res.Latency.Round(time.Millisecond).String()
		data.TestModel = res.Model
	}
	s.render(w, "provider.html", data)
}

func (s *Server) handleNewProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if presetKey := strings.TrimSpace(r.URL.Query().Get("preset")); presetKey != "" {
		preset, ok := config.PresetByKey(presetKey)
		if !ok {
			http.NotFound(w, r)
			return
		}
		p := config.Provider{
			Name: preset.Name, Description: preset.Description, Format: preset.Format,
			BaseURL: preset.BaseURL, Model: preset.Model,
		}
		s.render(w, "provider.html", s.providerView(preset.Key, p, true, preset.Key))
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cfg := s.cfgStore.Get()
	added := map[string]bool{}
	for _, name := range cfg.Names() {
		added[name] = true
	}
	var groups []presetGroupView
	for _, category := range config.PresetCategories() {
		var list []config.Preset
		for _, p := range config.Presets() {
			if p.Category != category {
				continue
			}
			if q != "" {
				hay := strings.ToLower(p.Name + " " + p.Key + " " + p.Model + " " + p.Description)
				if !strings.Contains(hay, strings.ToLower(q)) {
					continue
				}
			}
			list = append(list, p)
		}
		if len(list) > 0 {
			groups = append(groups, presetGroupView{Category: category, Presets: list, Added: added})
		}
	}
	s.render(w, "presets.html", presetsData{Title: "New provider", Tab: "settings", Query: q, Groups: groups})
}

func (s *Server) handleProvidersCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	cfg := s.cfgStore.Get()
	key := strings.TrimSpace(r.FormValue("key"))
	presetKey := strings.TrimSpace(r.FormValue("preset"))
	if key == "" {
		if presetKey != "" {
			key = presetKey
		} else {
			for i := 1; ; i++ {
				candidate := fmt.Sprintf("custom-%d", i)
				if _, exists := cfg.Providers[candidate]; !exists {
					key = candidate
					break
				}
			}
		}
	}
	p := s.providerFromForm(r, key)
	if p.Name == "" {
		p.Name = key
	}
	cfg.Providers[key] = p
	if !containsString(cfg.ProviderOrder, key) {
		cfg.ProviderOrder = append(cfg.ProviderOrder, key)
	}
	cfg.Normalize()
	if err := s.cfgStore.Save(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleProvider(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/settings/providers/"), "/")
	if rest == "reorder" {
		s.handleReorder(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	key := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "delete":
		s.handleProviderDelete(w, r, key)
		return
	case "default":
		s.handleProviderDefault(w, r, key)
		return
	case "test":
		s.handleProviderTest(w, r, key)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	cfg := s.cfgStore.Get()
	p, exists := cfg.Providers[key]
	if !exists {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		p = s.providerFromForm(r, key)
		cfg.Providers[key] = p
		cfg.Normalize()
		if err := s.cfgStore.Save(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	s.render(w, "provider.html", s.providerView(key, p, false, ""))
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	cfg := s.cfgStore.Get()
	if _, ok := cfg.Providers[key]; !ok {
		http.NotFound(w, r)
		return
	}
	if len(cfg.Providers) <= 1 {
		http.Error(w, "cannot delete the last provider", http.StatusBadRequest)
		return
	}
	delete(cfg.Providers, key)
	if cfg.DefaultProvider == key {
		cfg.DefaultProvider = ""
	}
	cfg.Normalize()
	if err := s.cfgStore.Save(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleProviderDefault(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	cfg := s.cfgStore.Get()
	if _, ok := cfg.Providers[key]; !ok {
		http.NotFound(w, r)
		return
	}
	cfg.DefaultProvider = key
	if err := s.cfgStore.Save(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	cfg := s.cfgStore.Get()
	p, ok := cfg.Providers[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := s.settingsView(cfg, false)
	data.Tested = key
	res, err := llmtest.Provider(r.Context(), p)
	if err != nil {
		data.TestMessage = err.Error()
	} else {
		data.TestOK = true
		data.TestMessage = res.Text
		data.TestLatency = res.Latency.Round(time.Millisecond).String()
		data.TestModel = res.Model
	}
	s.render(w, "settings.html", data)
}

func (s *Server) handleReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	cfg := s.cfgStore.Get()
	if len(in.Order) != len(cfg.Providers) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order must include every provider"})
		return
	}
	seen := map[string]bool{}
	for _, name := range in.Order {
		if _, ok := cfg.Providers[name]; !ok || seen[name] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid provider order"})
			return
		}
		seen[name] = true
	}
	cfg.ProviderOrder = in.Order
	if err := s.cfgStore.Save(cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func (s *Server) handleAPITasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := s.st.ListTasks(r.Context(), 100)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		publicTasks := make([]*domain.Task, 0, len(tasks))
		for i := range tasks {
			publicTasks = append(publicTasks, domain.PublicTask(&tasks[i]))
		}
		writeJSON(w, http.StatusOK, publicTasks)
	case http.MethodPost:
		var in struct {
			Title string          `json:"title"`
			Mode  domain.TaskMode `json:"mode"`
			Goal  string          `json:"goal"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if in.Mode != domain.ModePentest {
			in.Mode = domain.ModeCTF
		}
		task, err := s.StartTask(r.Context(), in.Title, in.Mode, in.Goal)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, domain.PublicTask(task))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPITask(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	taskID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	if len(parts) == 3 && parts[1] == "chat" && parts[2] == "messages" && r.Method == http.MethodGet {
		s.handleTaskChatMessages(w, r, taskID)
		return
	}
	if len(parts) == 4 && parts[1] == "targets" && parts[3] == "recover" && r.Method == http.MethodPost {
		s.mu.Lock()
		run := s.runs[taskID]
		s.mu.Unlock()
		if run == nil || run.engine == nil {
			http.Error(w, "task is not running", http.StatusConflict)
			return
		}
		if !run.engine.RecoverTarget(r.Context(), taskID, parts[2]) {
			http.Error(w, "invalid target", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=overview", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 3 && parts[1] == "lanes" && r.Method == http.MethodGet {
		laneID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.renderLane(w, r, taskID, laneID)
		return
	}
	if len(parts) == 4 && parts[1] == "conflicts" && parts[3] == "resolve" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		items, _ := s.st.ListConflicts(r.Context(), taskID, 1000)
		found := false
		for _, item := range items {
			if item.ID == id {
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		decision := r.FormValue("decision")
		switch decision {
		case "keep_all", "quarantine_all", "needs_human":
		default:
			http.Error(w, "invalid conflict decision", http.StatusBadRequest)
			return
		}
		if err := s.st.ResolveConflict(r.Context(), id, decision, "web review"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=conflicts", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 4 && parts[1] == "quarantine" && parts[3] == "promote" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		item, err := s.st.GetQuarantine(r.Context(), id)
		if err != nil || item == nil || item.TaskID != taskID {
			http.NotFound(w, r)
			return
		}
		if _, err := s.st.PromoteQuarantine(r.Context(), id, "web"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=quarantine", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 4 && parts[1] == "quarantine" && parts[3] == "status" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		item, err := s.st.GetQuarantine(r.Context(), id)
		if err != nil || item == nil || item.TaskID != taskID {
			http.NotFound(w, r)
			return
		}
		status := r.FormValue("status")
		switch status {
		case "open", "promoted", "dismissed":
		default:
			http.Error(w, "invalid quarantine status", http.StatusBadRequest)
			return
		}
		if err := s.st.SetQuarantineStatus(r.Context(), id, status, "web"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=quarantine", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) >= 2 && parts[1] == "flags" && r.Method == http.MethodPost {
		if len(parts) == 3 && parts[2] == "delete" {
			id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
			if err := s.st.DeleteTaskFlag(r.Context(), taskID, id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=flags", taskID), http.StatusSeeOther)
			return
		}
		challenge := strings.TrimSpace(r.FormValue("challenge"))
		flag := strings.TrimSpace(r.FormValue("flag"))
		if flag == "" {
			http.Error(w, "flag 不能为空", http.StatusBadRequest)
			return
		}
		if err := s.st.AddTaskFlag(r.Context(), taskID, challenge, flag, r.FormValue("status")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d?tab=flags", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 2 && parts[1] == "delete" && r.Method == http.MethodPost {
		if err := s.DeleteTask(r.Context(), taskID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/tasks", http.StatusSeeOther)
		return
	}
	if len(parts) == 2 && parts[1] == "rename" && r.Method == http.MethodPost {
		title := strings.TrimSpace(r.FormValue("title"))
		if title == "" {
			http.Error(w, "任务标题不能为空", http.StatusBadRequest)
			return
		}
		if err := s.st.RenameTask(r.Context(), taskID, title); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/tasks/%d", taskID), http.StatusSeeOther)
		return
	}
	if len(parts) == 3 && parts[1] == "chat" && parts[2] == "stop" && r.Method == http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]any{"stopped": s.stopTaskChat(r.Context(), taskID)})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		var err error
		switch parts[1] {
		case "pause":
			err = s.PauseTask(ctx, taskID)
		case "resume":
			err = s.ResumeTask(ctx, taskID)
		case "stop":
			err = s.StopTask(ctx, taskID)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		task, _ := s.st.GetTask(ctx, taskID)
		writeJSON(w, http.StatusOK, domain.PublicTask(task))
		return
	}

	if len(parts) == 3 && parts[1] == "approvals" && r.Method == http.MethodPost {
		approvalID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		decision := domain.ApprovalDecision(in.Decision)
		if decision != domain.ApprovalApproved && decision != domain.ApprovalRejected {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "decision must be approved or rejected"})
			return
		}
		approval, err := s.resolveApproval(ctx, taskID, approvalID, decision, "human:api", in.Reason)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, approval)
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil || task == nil {
		http.NotFound(w, r)
		return
	}
	goal, _ := s.st.CurrentGoal(ctx, taskID)
	counts, _ := s.st.GraphCounts(ctx, taskID)
	intents, _ := s.st.ListIntents(ctx, taskID)
	works, _ := s.st.ListWorks(ctx, taskID)
	findings, _ := s.st.ListFindings(ctx, taskID, 100)
	events, _ := s.st.ListEvents(ctx, taskID, 200)
	pending, _ := s.st.PendingApprovals(ctx, taskID)
	writeJSON(w, http.StatusOK, map[string]any{
		"task": domain.PublicTask(task), "goal": goal, "counts": counts, "intents": intents,
		"works": works, "findings": findings, "events": events, "pending_approvals": pending,
	})
}

func (s *Server) resolveApproval(ctx context.Context, taskID, approvalID int64, decision domain.ApprovalDecision, by, reason string) (*domain.Approval, error) {
	approval, err := s.st.ResolveApproval(ctx, approvalID, decision, by, reason)
	if err != nil {
		return nil, err
	}
	log.Printf("[approval] task=%d intent=%d decision=%s by=%s", taskID, approval.IntentID, decision, by)
	typ := domain.EventIntentApproved
	if decision == domain.ApprovalRejected {
		typ = domain.EventIntentRejected
	}
	_, _ = s.st.AppendEvent(ctx, taskID, typ, "human", by, "intent", approval.IntentID,
		map[string]any{"reason": reason})
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	if run != nil && run.engine != nil {
		run.engine.Notify(&domain.Event{
			TaskID: taskID, Type: domain.EventTaskResumed,
			ActorType: "human", ActorID: by,
			SubjectType: "intent", SubjectID: approval.IntentID,
		})
	}
	return approval, nil
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	tmpl, err := template.New("layout").Funcs(template.FuncMap{
		"statusClass":               statusClass,
		"statusLabel":               statusLabel,
		"intentLabel":               intentLabel,
		"workLabel":                 workLabel,
		"riskLabel":                 riskLabel,
		"modeLabel":                 modeLabel,
		"formatLabel":               formatLabel,
		"eventLabel":                eventLabel,
		"activityLabel":             activityLabel,
		"assetTypeLabel":            assetTypeLabel,
		"roleLabel":                 roleLabel,
		"scopeLabel":                scopeLabel,
		"subjectLabel":              subjectLabel,
		"markdown":                  markdown.Render,
		"formatNumber":              formatNumber,
		"thinkingSteps":             thinkingSteps,
		"thinkStepKind":             thinkStepKind,
		"thinkDuration":             thinkDuration,
		"graphKindLabel":            graphKindLabel,
		"graphStateLabel":           graphStateLabel,
		"attachmentURL":             attachmentURL,
		"conversationAttachmentURL": conversationAttachmentURL,
		"shortTime":                 func(t time.Time) string { return t.Format("01-02 15:04:05") },
		"version":                   func() string { return version.Current },
		"jsonPretty": func(v any) string {
			b, _ := json.MarshalIndent(v, "", "  ")
			return string(b)
		},
	}).ParseFS(webFS, "web/templates/layout.html", "web/templates/thinking.html", "web/templates/conversation_messages.html", "web/templates/chat_messages.html", "web/templates/"+page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("[api] render %s: %v", page, err)
	}
}

func statusClass(v any) string {
	s := fmt.Sprint(v)
	switch s {
	case "completed", "succeeded", "done", "met", "approved":
		return "ok"
	case "running", "claimed", "queued", "planning", "settling":
		return "run"
	case "failed", "rejected", "cancelled", "blocked", "timed_out":
		return "bad"
	case "pending", "paused", "proposed", "medium", "abandoned":
		return "warn"
	case "low":
		return "ok"
	case "high", "critical":
		return "bad"
	default:
		return "muted"
	}
}

func statusLabel(v any) string {
	switch fmt.Sprint(v) {
	case "created":
		return "已创建"
	case "planning":
		return "规划中"
	case "running":
		return "运行中"
	case "settling":
		return "收尾中"
	case "completed":
		return "已完成"
	case "paused":
		return "已暂停"
	case "cancelled":
		return "已停止"
	case "failed":
		return "失败"
	case "open":
		return "进行中"
	case "in_progress":
		return "进行中"
	case "met":
		return "已达成"
	case "abandoned":
		return "已放弃"
	case "proposed":
		return "已提议"
	case "approved":
		return "已批准"
	case "queued":
		return "排队中"
	case "claimed":
		return "已领取"
	case "succeeded":
		return "成功"
	case "blocked":
		return "阻塞"
	case "leased":
		return "已租用"
	case "timed_out":
		return "超时"
	case "lost":
		return "丢失"
	case "pending":
		return "待审批"
	case "rejected":
		return "已拒绝"
	case "expired":
		return "已过期"
	default:
		return fmt.Sprint(v)
	}
}

func intentLabel(v any) string {
	if fmt.Sprint(v) == "cancelled" {
		return "已取消"
	}
	if fmt.Sprint(v) == "deferred" {
		return "已延迟"
	}
	return statusLabel(v)
}

func workLabel(v any) string {
	if fmt.Sprint(v) == "cancelled" {
		return "已取消"
	}
	return statusLabel(v)
}

func riskLabel(v any) string {
	switch fmt.Sprint(v) {
	case "info":
		return "信息"
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "critical":
		return "严重"
	default:
		return fmt.Sprint(v)
	}
}

func modeLabel(v any) string {
	switch fmt.Sprint(v) {
	case "ctf":
		return "CTF"
	case "pentest":
		return "渗透测试"
	default:
		return fmt.Sprint(v)
	}
}

func formatLabel(v any) string {
	switch fmt.Sprint(v) {
	case "openai":
		return "聊天补全"
	case "openai-responses":
		return "Responses 接口"
	case "anthropic":
		return "Anthropic 消息"
	default:
		return fmt.Sprint(v)
	}
}

func eventLabel(v any) string {
	switch fmt.Sprint(v) {
	case "task.started":
		return "任务开始"
	case "task.resumed":
		return "任务继续"
	case "task.paused":
		return "任务暂停"
	case "task.stopped":
		return "任务停止"
	case "goal.set":
		return "设置目标"
	case "goal.met_proposed":
		return "目标达成提议"
	case "goal.accepted":
		return "目标达成"
	case "goal.rejected":
		return "目标拒绝"
	case "goal.abandoned":
		return "目标放弃"
	case "hint.added":
		return "添加提示"
	case "constraint.added":
		return "添加约束"
	case "intent.proposed":
		return "意图提议"
	case "intent.approved":
		return "意图通过"
	case "intent.rejected":
		return "意图拒绝"
	case "work.claimed":
		return "领取工作"
	case "swarm.lane_done":
		return "并行 Lane 完成"
	case "swarm.merged":
		return "并行结果合并"
	case "swarm.failed":
		return "并行执行失败"
	case "work.finished":
		return "工作完成"
	case "work.failed":
		return "工作失败"
	case "work.timed_out":
		return "工作超时"
	case "fact.recorded":
		return "记录事实"
	case "finding.reported":
		return "记录发现"
	case "asset.recorded":
		return "记录资产"
	case "flag.submitted":
		return "提交 Flag"
	case "flag.accepted":
		return "Flag 通过"
	case "flag.rejected":
		return "Flag 拒绝"
	case "planner.no_intent":
		return "无新意图"
	default:
		return fmt.Sprint(v)
	}
}

func activityLabel(v any) string {
	switch fmt.Sprint(v) {
	case "prompt":
		return "提示词"
	case "thinking":
		return "思考"
	case "assistant_text":
		return "模型文本"
	case "tool_use":
		return "工具调用"
	case "tool_result":
		return "工具结果"
	case "usage":
		return "Token 用量"
	case "result":
		return "结果"
	case "error":
		return "错误"
	case "fact":
		return "事实"
	case "finding":
		return "发现"
	case "work_claimed":
		return "领取工作"
	case "work_finished":
		return "工作完成"
	default:
		return fmt.Sprint(v)
	}
}

func roleLabel(v any) string {
	switch fmt.Sprint(v) {
	case "planner":
		return "规划者"
	case "worker":
		return "执行者"
	case "mainagent":
		return "主控"
	case "session_agent":
		return "会话 Agent"
	case "engine":
		return "引擎"
	case "system":
		return "系统"
	case "human":
		return "人工"
	case "submitter":
		return "提交器"
	case "chat_summary":
		return "对话摘要"
	default:
		return fmt.Sprint(v)
	}
}

func subjectLabel(v any) string {
	switch fmt.Sprint(v) {
	case "task":
		return "任务"
	case "goal":
		return "目标"
	case "intent":
		return "意图"
	case "work":
		return "执行"
	case "fact":
		return "事实"
	case "finding":
		return "发现"
	case "asset":
		return "资产"
	default:
		return fmt.Sprint(v)
	}
}

func scopeLabel(v any) string {
	switch fmt.Sprint(v) {
	case "in_scope":
		return "范围内"
	case "out_of_scope":
		return "范围外"
	case "unknown", "":
		return "未知"
	default:
		return fmt.Sprint(v)
	}
}

func assetTypeLabel(v any) string {
	switch fmt.Sprint(v) {
	case "root_domain":
		return "根域名"
	case "subdomain":
		return "子域名"
	case "ip":
		return "IP"
	case "service":
		return "服务"
	case "app":
		return "应用"
	case "endpoint":
		return "端点"
	default:
		return fmt.Sprint(v)
	}
}

func formatNumber(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// thinkingSteps splits a streamed reasoning block into timeline rows.
func thinkingSteps(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n\n")
	if len(parts) == 1 {
		parts = strings.Split(s, "\n")
	}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func thinkStepKind(s string) string {
	if strings.HasPrefix(s, "运行命令 ") {
		return "运行命令"
	}
	return "思考"
}

func thinkDuration(ms int64) string {
	seconds := ms / 1000
	if seconds < 60 {
		return fmt.Sprintf("用时 %d 秒", seconds)
	}
	minutes := seconds / 60
	rest := seconds % 60
	if rest == 0 {
		return fmt.Sprintf("用时 %d 分钟", minutes)
	}
	return fmt.Sprintf("用时 %d 分 %d 秒", minutes, rest)
}
