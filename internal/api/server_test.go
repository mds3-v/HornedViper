package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"hornedviper/internal/config"
	"hornedviper/internal/domain"
	"hornedviper/internal/kernel"
	"hornedviper/internal/runtime"
	"hornedviper/internal/store"
)

type blockingRuntime struct {
	release chan struct{}
}

func (b *blockingRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{Intents: []domain.IntentSpec{{
		Summary: "hold", Rationale: "test", Priority: 1, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSingle,
	}}}, nil
}

func (b *blockingRuntime) Work(ctx context.Context, _ *domain.WorkerRequest) (*domain.WorkResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return &domain.WorkResult{Status: "done"}, nil
	}
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestTaskPauseResumeStop(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	cfgStore, err := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg := cfgStore.Get()
	cfg.Runtime = "fake"
	if err := cfgStore.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	blocking := &blockingRuntime{release: make(chan struct{})}
	srv := New(st, cfgStore, func(config.Config) (runtime.Runtime, error) {
		return blocking, nil
	}, Config{
		MaxWorkers: 1,
		Scheduler:  kernel.SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 30 * time.Millisecond},
	})

	task, err := srv.StartTask(ctx, "pause test", domain.ModeCTF, "hold the task")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitFor(t, "running", func() bool {
		t, _ := st.GetTask(ctx, task.ID)
		return t != nil && t.Status == domain.TaskRunning
	})

	if err := srv.PauseTask(ctx, task.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitFor(t, "paused", func() bool {
		t, _ := st.GetTask(ctx, task.ID)
		return t != nil && t.Status == domain.TaskPaused
	})
	if g, _ := st.CurrentGoal(ctx, task.ID); g == nil || g.State != domain.GoalPaused {
		t.Fatalf("goal after pause = %+v", g)
	}
	waitFor(t, "run stopped", func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.runs[task.ID] == nil
	})

	if err := srv.ResumeTask(ctx, task.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitFor(t, "running again", func() bool {
		t, _ := st.GetTask(ctx, task.ID)
		return t != nil && t.Status == domain.TaskRunning
	})
	if g, _ := st.CurrentGoal(ctx, task.ID); g == nil || g.State != domain.GoalInProgress {
		t.Fatalf("goal after resume = %+v", g)
	}

	if err := srv.StopTask(ctx, task.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "cancelled", func() bool {
		t, _ := st.GetTask(ctx, task.ID)
		return t != nil && t.Status == domain.TaskCancelled
	})
	if g, _ := st.CurrentGoal(ctx, task.ID); g == nil || g.State != domain.GoalAbandoned {
		t.Fatalf("goal after stop = %+v", g)
	}
}
