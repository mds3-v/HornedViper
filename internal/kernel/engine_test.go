package kernel

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"hornedviper/internal/domain"
	"hornedviper/internal/runtime"
	"hornedviper/internal/store"
	"hornedviper/internal/submit"
)

func TestEngineEndToEndWithFakeRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := store.Open(filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	task, err := st.CreateTask(ctx, "engine test", domain.ModeCTF, nil)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	goal, err := st.SetGoal(ctx, task.ID, "find the demo flag", nil)
	if err != nil {
		t.Fatalf("goal: %v", err)
	}

	e := New(st, &runtime.FakeRuntime{StepDelay: time.Millisecond}, Config{
		MaxWorkers: 2,
		Lease:      time.Minute,
		Scheduler: SchedulerConfig{
			Debounce: 10 * time.Millisecond,
			MaxDelay: 50 * time.Millisecond,
			MinGap:   5 * time.Millisecond,
		},
	})
	if err := e.Run(ctx, task.ID); err != nil {
		t.Fatalf("run: %v", err)
	}

	finalTask, _ := st.GetTask(ctx, task.ID)
	if finalTask.Status != domain.TaskCompleted {
		t.Fatalf("task status = %s, want completed", finalTask.Status)
	}
	finalGoal, _ := st.GetGoal(ctx, goal.ID)
	if finalGoal.State != domain.GoalMet {
		t.Fatalf("goal state = %s, want met", finalGoal.State)
	}
	facts, _ := st.ListFacts(ctx, task.ID, 10)
	findings, _ := st.ListFindings(ctx, task.ID, 10)
	if len(facts) < 2 {
		t.Fatalf("facts = %d, want >= 2", len(facts))
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	events, _ := st.ListEvents(ctx, task.ID, 100)
	var accepted bool
	for _, ev := range events {
		if ev.Type == domain.EventGoalAccepted {
			accepted = true
		}
	}
	if !accepted {
		t.Fatal("expected goal.accepted event")
	}
}

func TestEngineSubmitsFlagInCTFMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := store.Open(filepath.Join(t.TempDir(), "submit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	task, _ := st.CreateTask(ctx, "submit test", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "get flag", nil); err != nil {
		t.Fatalf("goal: %v", err)
	}
	e := New(st, &runtime.FakeRuntime{StepDelay: time.Millisecond}, Config{
		MaxWorkers: 1,
		Lease:      time.Minute,
		Scheduler:  SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 30 * time.Millisecond},
		Submitter:  submit.NewCommand(`printf 'accepted %s' "$HV_FLAG"`),
	})
	if err := e.Run(ctx, task.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	events, _ := st.ListEvents(ctx, task.ID, 100)
	var submitted, accepted bool
	for _, ev := range events {
		switch ev.Type {
		case domain.EventFlagSubmitted:
			submitted = true
		case domain.EventFlagAccepted:
			accepted = true
		}
	}
	if !submitted || !accepted {
		t.Fatalf("submitted=%v accepted=%v events=%+v", submitted, accepted, events)
	}
}
