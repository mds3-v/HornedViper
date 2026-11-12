package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

type swarmTestRuntime struct{}

func (swarmTestRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}
func testLeadResponse(n int) *domain.LeadResponse {
	out := &domain.LeadResponse{}
	for i := 0; i < n; i++ {
		out.Lanes = append(out.Lanes, domain.LanePlan{Name: fmt.Sprintf("lead-%d", i+1), Brief: fmt.Sprintf("lead brief %d", i+1)})
	}
	return out
}

func (swarmTestRuntime) Lead(_ context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	return testLeadResponse(req.Lanes), nil
}

func (swarmTestRuntime) Work(_ context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	return &domain.WorkResult{
		Status: "done",
		Facts: []domain.FactSpec{{
			Summary: "lane " + req.Work.LaneID, Evidence: "test evidence", Confidence: domain.ConfidenceObserved,
		}},
	}, nil
}

func TestSwarmCreatesLanesAndMerges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, err := st.CreateTask(ctx, "swarm", domain.ModeCTF, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetGoal(ctx, task.ID, "swarm goal", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "swarm test", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 3, "quorum": 2, "strategies": []any{
			map[string]any{"name": "s1", "brief": "brief one"},
			map[string]any{"name": "s2", "brief": "brief two"},
			map[string]any{"name": "s3", "brief": "brief three"},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, swarmTestRuntime{}, Config{
		MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond},
	})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(200 * time.Millisecond)
	final, _ := st.GetIntent(ctx, intent.ID)
	if final == nil || final.State != domain.IntentSucceeded {
		// The engine may still be between merge and planner; wait briefly.
		time.Sleep(200 * time.Millisecond)
		final, _ = st.GetIntent(ctx, intent.ID)
	}
	if final == nil || final.State != domain.IntentSucceeded {
		t.Fatalf("intent state = %v, want succeeded", final)
	}
	if n, _, _ := st.SwarmLaneTerminal(ctx, intent.ID); n != 3 {
		t.Fatalf("lanes = %d, want 3", n)
	}
	facts, _ := st.ListFacts(ctx, task.ID, 20)
	if len(facts) == 0 {
		t.Fatal("expected merged facts")
	}
	cancel()
	<-done
}

func TestShardCreatesDisjointLanesAndMerges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "shard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "shard", domain.ModeCTF, nil)
	goal, err := st.SetGoal(ctx, task.ID, "shard goal", nil)
	if err != nil {
		t.Fatal(err)
	}
	goalID := goal.ID
	var assetIDs []int64
	for _, host := range []string{"a.example", "b.example", "c.example", "d.example"} {
		a, err := st.RecordAsset(ctx, domain.AssetSpec{Type: "host", Value: host})
		if err != nil {
			t.Fatal(err)
		}
		assetIDs = append(assetIDs, a.ID)
		if err := st.AttachAnchor(ctx, goalID, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "shard assets", Rationale: "batch", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionShard, AssetIDs: assetIDs,
		Budget: map[string]any{"lanes": 2, "min_success": 1},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, swarmTestRuntime{}, Config{
		MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond},
	})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(250 * time.Millisecond)
	run, err := st.GetSwarmRun(ctx, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Mode != "shard" || run.Lanes != 2 {
		t.Fatalf("run = mode %s lanes %d", run.Mode, run.Lanes)
	}
	seen := map[int64]bool{}
	for _, lane := range run.LaneAssets {
		for _, id := range lane {
			if seen[id] {
				t.Fatalf("asset %d assigned twice", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != len(assetIDs) {
		t.Fatalf("assigned assets = %d, want %d", len(seen), len(assetIDs))
	}
	final, _ := st.GetIntent(ctx, intent.ID)
	if final == nil || final.State != domain.IntentSucceeded {
		t.Fatalf("intent state = %v, want succeeded", final)
	}
	cancel()
	<-done
}

type multiFlagRuntime struct{}

func (multiFlagRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}

func (multiFlagRuntime) Lead(_ context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	return testLeadResponse(req.Lanes), nil
}

func (multiFlagRuntime) Work(_ context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	switch req.Work.LaneID {
	case "lane-1":
		return &domain.WorkResult{Status: "done", Findings: []domain.FindingSpec{{
			Title: "multi flag finding", Severity: "high", Evidence: "flag{alpha} flag{beta}",
		}}}, nil
	case "lane-2":
		return &domain.WorkResult{Status: "done", Findings: []domain.FindingSpec{{
			Title: "alpha only", Severity: "high", Evidence: "flag{alpha}",
		}}}, nil
	default:
		return &domain.WorkResult{Status: "done", Facts: []domain.FactSpec{{
			Summary: "lane done", Evidence: "test", Confidence: domain.ConfidenceObserved,
		}}}, nil
	}
}

type recordingSubmitter struct {
	flags []string
}

func (r *recordingSubmitter) Enabled() bool { return true }
func (r *recordingSubmitter) Submit(_ context.Context, _ *domain.Task, f domain.Finding) (domain.SubmitResult, error) {
	r.flags = append(r.flags, f.Evidence)
	return domain.SubmitResult{Flag: f.Evidence, Accepted: true}, nil
}

func TestSwarmSubmitsFlagsAtLaneCompletion(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "multiflag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "multi flag", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "collect flags", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "multi flag swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 3, "quorum": 2, "strategies": []any{
			map[string]any{"name": "s1", "brief": "brief one"},
			map[string]any{"name": "s2", "brief": "brief two"},
			map[string]any{"name": "s3", "brief": "brief three"},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	sub := &recordingSubmitter{}
	e := New(st, multiFlagRuntime{}, Config{
		MaxWorkers: 2, Lease: time.Minute, Submitter: sub,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond},
	})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(250 * time.Millisecond)
	final, _ := st.GetIntent(ctx, intent.ID)
	if final == nil || final.State != domain.IntentSucceeded {
		t.Fatalf("intent state = %v, want succeeded", final)
	}
	seen := map[string]bool{}
	for _, f := range sub.flags {
		seen[f] = true
	}
	if !seen["flag{alpha}"] || !seen["flag{beta}"] {
		t.Fatalf("expected both lane flags submitted immediately; flags=%v", sub.flags)
	}
	if seen["flag{alpha} flag{beta}"] {
		t.Fatalf("compound flag string submitted: %v", sub.flags)
	}
	cancel()
	<-done
}

type failingSwarmRuntime struct{}

func (failingSwarmRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}
func (failingSwarmRuntime) Lead(_ context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	return testLeadResponse(req.Lanes), nil
}

func (failingSwarmRuntime) Work(context.Context, *domain.WorkerRequest) (*domain.WorkResult, error) {
	return &domain.WorkResult{Status: "failed", Error: "simulated lane failure", Facts: []domain.FactSpec{{
		Summary: "partial fact", Evidence: "partial output", Confidence: domain.ConfidenceInferred,
	}}}, nil
}

func TestFailedSwarmLaneIsQuarantined(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "quarantine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "quarantine", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "test", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "fail swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm, Budget: map[string]any{"lanes": 2, "quorum": 2, "strategies": []any{
			map[string]any{"name": "s1", "brief": "brief one"},
			map[string]any{"name": "s2", "brief": "brief two"},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, failingSwarmRuntime{}, Config{
		MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond},
	})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(250 * time.Millisecond)
	items, err := st.ListQuarantine(ctx, task.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("quarantine items = %d, want 2", len(items))
	}
	if items[0].SourceKind != "failed_lane" {
		t.Fatalf("source kind = %s", items[0].SourceKind)
	}
	final, _ := st.GetIntent(ctx, intent.ID)
	if final == nil || final.State != domain.IntentFailed {
		t.Fatalf("intent state = %v, want failed", final)
	}
	cancel()
	<-done
}

func TestShardAssetMustBelongToTask(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "asset-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, _ := st.CreateTask(ctx, "owner", domain.ModeCTF, nil)
	b, _ := st.CreateTask(ctx, "other", domain.ModeCTF, nil)
	ga, _ := st.SetGoal(ctx, a.ID, "a", nil)
	_, _ = st.SetGoal(ctx, b.ID, "b", nil)
	asset, err := st.RecordAsset(ctx, domain.AssetSpec{Type: "host", Value: "only-b.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AttachAnchor(ctx, ga.ID, asset.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.TaskAssets(ctx, b.ID, []int64{asset.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("task B resolved task A asset: %+v", got)
	}
	got, err = st.TaskAssets(ctx, a.ID, []int64{asset.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("owner task resolved %d assets, want 1", len(got))
	}
}

func TestLanePolicySelectsByTaskMode(t *testing.T) {
	ctf := lanePolicyForTask(&domain.Task{Mode: domain.ModeCTF})
	if ctf.Name() != "ctf" || !ctf.FlagQuorumEnabled() || !ctf.SubmitFlags() {
		t.Fatalf("ctf policy = %+v", ctf)
	}
	generic := lanePolicyForTask(&domain.Task{Mode: domain.ModePentest})
	if generic.Name() != "generic" || generic.FlagQuorumEnabled() || generic.SubmitFlags() {
		t.Fatalf("generic policy = %+v", generic)
	}
}

type leadTestRuntime struct{ swarmTestRuntime }

func (leadTestRuntime) Lead(context.Context, *domain.LeadRequest) (*domain.LeadResponse, error) {
	return &domain.LeadResponse{Lanes: []domain.LanePlan{
		{Name: "lead-a", Brief: "lead brief a"},
		{Name: "lead-b", Brief: "lead brief b"},
	}}, nil
}

func TestSwarmUsesExplicitLeadPlan(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "lead.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "lead", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "lead goal", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "lead swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget:        map[string]any{"lanes": 2, "quorum": 1, "lane_planning": "lead"},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, leadTestRuntime{}, Config{MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(200 * time.Millisecond)
	run, err := st.GetSwarmRun(ctx, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Strategies) != 2 || run.Strategies[0] != "lead-a" || run.Strategies[1] != "lead-b" {
		t.Fatalf("lead strategies = %v", run.Strategies)
	}
	cancel()
	<-done
}

type noLeadRuntime struct{}

func (noLeadRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}
func (noLeadRuntime) Work(context.Context, *domain.WorkerRequest) (*domain.WorkResult, error) {
	return &domain.WorkResult{Status: "done", Facts: []domain.FactSpec{{Summary: "x", Evidence: "y", Confidence: domain.ConfidenceObserved}}}, nil
}

func TestIncompletePlannerSwarmPlanIsBlocked(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "incomplete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "incomplete", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "goal", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "partial swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget:        map[string]any{"lanes": 2, "quorum": 1, "strategies": []any{map[string]any{"name": "only", "brief": "only brief"}}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, noLeadRuntime{}, Config{MaxWorkers: 1, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(150 * time.Millisecond)
	final, _ := st.GetIntent(ctx, intent.ID)
	if final == nil || final.State != domain.IntentBlocked {
		t.Fatalf("intent state = %v, want blocked", final)
	}
	if _, err := st.GetSwarmRun(ctx, intent.ID); err == nil {
		t.Fatal("incomplete plan created swarm run")
	}
	cancel()
	<-done
}

func TestPlannerSwarmStrategyIsIgnoredForLead(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "repair", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "repair goal", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "repair swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 2, "quorum": 1, "strategies": []any{
			map[string]any{"name": "keep-me", "brief": "planner brief"},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, leadTestRuntime{}, Config{MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(200 * time.Millisecond)
	run, err := st.GetSwarmRun(ctx, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Strategies) != 2 || run.Strategies[0] != "lead-a" {
		t.Fatalf("strategies = %v, want lead-authored plan", run.Strategies)
	}
	cancel()
	<-done
}

func TestRepeatedIncompleteSwarmPlansPauseTask(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "stalled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "stalled", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "goal", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
			Summary: "partial " + string(rune('a'+i)), Rationale: "test", Priority: 5, Risk: domain.RiskLow,
			ExecutionMode: domain.ExecutionSwarm,
			Budget:        map[string]any{"lanes": 2, "quorum": 1, "strategies": []any{map[string]any{"name": "only", "brief": "only brief"}}},
		}, 0, domain.IntentApproved); err != nil {
			t.Fatal(err)
		}
	}
	e := New(st, noLeadRuntime{}, Config{MaxWorkers: 1, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(200 * time.Millisecond)
	final, _ := st.GetTask(ctx, task.ID)
	if final == nil || final.Status != domain.TaskPaused {
		t.Fatalf("task status = %v, want paused", final)
	}
	cancel()
	<-done
}

type slowLaneRuntime struct{ swarmTestRuntime }

func (slowLaneRuntime) Work(ctx context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	if req.Work.LaneID == "lane-2" {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return swarmTestRuntime{}.Work(ctx, req)
}

func TestSwarmDrainWindowDefersMerge(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "drain", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "drain goal", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "drain swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 2, "quorum": 1, "strategies": []any{
			map[string]any{"name": "s1", "brief": "brief one"},
			map[string]any{"name": "s2", "brief": "brief two"},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, slowLaneRuntime{}, Config{MaxWorkers: 2, Lease: time.Minute, DrainWindow: 150 * time.Millisecond,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(80 * time.Millisecond)
	run, err := st.GetSwarmRun(ctx, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "running" {
		t.Fatalf("run state during drain = %s, want running", run.State)
	}
	time.Sleep(250 * time.Millisecond)
	run, _ = st.GetSwarmRun(ctx, intent.ID)
	if run == nil || run.State != "done" {
		t.Fatalf("run state after drain = %v, want done", run)
	}
	cancel()
	<-done
}

type conflictRuntime struct{}

func (conflictRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}
func (conflictRuntime) Lead(_ context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	return testLeadResponse(req.Lanes), nil
}

func (conflictRuntime) Work(_ context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	challenge := "c-01"
	if req.Work.LaneID == "lane-2" {
		challenge = "c-02"
	}
	return &domain.WorkResult{
		Status: "done",
		Facts:  []domain.FactSpec{{Summary: "fact " + req.Work.LaneID, Evidence: "evidence", Confidence: domain.ConfidenceObserved}},
		Findings: []domain.FindingSpec{{
			Title: challenge + " flag", Severity: "high", Evidence: "flag{same}",
		}},
	}, nil
}

func TestSwarmQuarantinesCrossChallengeFlagWithoutHumanGate(t *testing.T) {
	if got := challengeFromText("c-01 flag"); got != "c-01" {
		t.Fatalf("challenge parse = %q", got)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "conflict", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "collect", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "conflict swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 2, "quorum": 2, "strategies": []any{
			map[string]any{"name": "s1", "brief": "brief one"},
			map[string]any{"name": "s2", "brief": "brief two"},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, conflictRuntime{}, Config{MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(250 * time.Millisecond)
	conflicts, err := st.ListConflicts(ctx, task.ID, 10)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v err=%v", conflicts, err)
	}
	if conflicts[0].Type != "flag_challenge_mismatch" || conflicts[0].Status != "quarantined" {
		t.Fatalf("conflict = %+v", conflicts[0])
	}
	final, _ := st.GetIntent(ctx, intent.ID)
	if final == nil || final.State == domain.IntentBlocked {
		t.Fatalf("CTF conflict should not human-gate; state=%v", final)
	}
	cancel()
	<-done
}

type findingObservationRuntime struct{}

func (findingObservationRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}
func (findingObservationRuntime) Lead(_ context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	return testLeadResponse(req.Lanes), nil
}

func (findingObservationRuntime) Work(_ context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	obs := "observed"
	if req.Work.LaneID == "lane-2" {
		obs = "not_observed"
	}
	return &domain.WorkResult{
		Status: "done",
		Facts:  []domain.FactSpec{{Summary: "fact " + req.Work.LaneID, Evidence: "evidence", Confidence: domain.ConfidenceObserved}},
		Findings: []domain.FindingSpec{{
			Title: "same subject", Severity: "medium", Evidence: "http response",
			SubjectKey: "asset:1|lfi", CaseKey: "GET /x?lang=a", Observation: obs,
		}},
	}, nil
}

func TestSameCaseOppositeObservationIsConflict(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "finding-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "finding conflict", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "verify", nil); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "finding conflict", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 2, "quorum": 2, "strategies": []any{
			map[string]any{"name": "s1", "brief": "brief one"},
			map[string]any{"name": "s2", "brief": "brief two"},
		}},
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, findingObservationRuntime{}, Config{MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(250 * time.Millisecond)
	conflicts, err := st.ListConflicts(ctx, task.ID, 10)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v err=%v", conflicts, err)
	}
	if conflicts[0].Type != "inconsistent_observation" {
		t.Fatalf("type = %s", conflicts[0].Type)
	}
	final, _ := st.GetIntent(ctx, intent.ID)
	if final == nil || final.State == domain.IntentBlocked {
		t.Fatalf("CTF conflict should not human-gate: %v", final)
	}
	cancel()
	<-done
}

type hintCaptureRuntime struct {
	swarmTestRuntime
	hints []string
}

func (r *hintCaptureRuntime) Lead(_ context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	r.hints = append([]string(nil), req.Hints...)
	return testLeadResponse(req.Lanes), nil
}

func TestPlannerStrategiesReachLeadAsHints(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hints.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "hints", domain.ModeCTF, nil)
	if _, err := st.SetGoal(ctx, task.ID, "goal", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "hint swarm", Rationale: "test", Priority: 5, Risk: domain.RiskLow,
		ExecutionMode: domain.ExecutionSwarm,
		Budget: map[string]any{"lanes": 2, "quorum": 1, "strategies": []any{
			map[string]any{"name": "planner-hint", "brief": "try this direction"},
		}},
	}, 0, domain.IntentApproved); err != nil {
		t.Fatal(err)
	}
	rt := &hintCaptureRuntime{}
	e := New(st, rt, Config{MaxWorkers: 2, Lease: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	time.Sleep(200 * time.Millisecond)
	if len(rt.hints) != 1 || rt.hints[0] != "planner-hint: try this direction" {
		t.Fatalf("lead hints = %v", rt.hints)
	}
	cancel()
	<-done
}

type fastSlotRuntime struct {
	normalStarted chan string
	fastStarted   chan string
}

func (r *fastSlotRuntime) Plan(context.Context, *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	return &domain.PlannerResponse{}, nil
}
func (r *fastSlotRuntime) Work(ctx context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	if domain.IsFastIntent(req.Intent) {
		select {
		case r.fastStarted <- req.Intent.Summary:
		default:
		}
		return &domain.WorkResult{Status: "done", Facts: []domain.FactSpec{{Summary: "submitted " + req.Intent.Summary, Evidence: "200", Confidence: domain.ConfidenceObserved}}}, nil
	}
	select {
	case r.normalStarted <- req.Intent.Summary:
	default:
	}
	<-ctx.Done()
	return &domain.WorkResult{Status: "blocked", Error: "cancelled", Partial: true}, nil
}

func TestFastSubmitSlotRunsAlongsideThreeNormalWorkers(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fastslot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, _ := st.CreateTask(ctx, "fastslot", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	mk := func(summary string, priority int, budget map[string]any) *domain.Intent {
		i, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
			Summary: summary, Rationale: "test", GoalIDs: []int64{goal.ID},
			Priority: priority, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle, Budget: budget,
		}, 0, domain.IntentProposed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.ApproveIntent(ctx, task.ID, i.ID, domain.ApprovalApproved, "policy:ctf", "ctf", domain.RiskLow, nil); err != nil {
			t.Fatal(err)
		}
		return i
	}
	mk("start and solve a-10", 9, nil)
	mk("start and solve a-11", 8, nil)
	mk("start and solve a-12", 7, nil)
	fast := mk("submit the captured flag", 1, map[string]any{"fast_lane": true})
	_ = fast
	rt := &fastSlotRuntime{normalStarted: make(chan string, 3), fastStarted: make(chan string, 1)}
	e := New(st, rt, Config{MaxWorkers: 4, Lease: time.Minute, WorkerTimeout: time.Minute,
		Scheduler: SchedulerConfig{Debounce: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MinGap: time.Millisecond}})
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task.ID) }()
	deadline := time.After(800 * time.Millisecond)
	normal := 0
	fastSeen := false
	for normal < 3 || !fastSeen {
		select {
		case <-rt.normalStarted:
			normal++
		case <-rt.fastStarted:
			fastSeen = true
		case <-deadline:
			t.Fatalf("normal=%d fast=%v; reserved slot did not run", normal, fastSeen)
		}
	}
	cancel()
	<-done
}

func TestTargetProgressDoesNotCountAsFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "progress", domain.ModeCTF, nil)
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	intent := &domain.Intent{TaskID: task.ID, Summary: "Resume b-02: partial progress"}
	// No output failure counts.
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{Status: "blocked", Error: "no finish"}, "no finish")
	if got := e.targetState("b-02").Attempts; got != 1 {
		t.Fatalf("no-output attempts=%d", got)
	}
	// New facts are progress, not failure; they reset attempts.
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{Status: "blocked", Partial: true,
		Facts: []domain.FactSpec{{Summary: "new fact", Evidence: "obs"}}}, "partial")
	state := e.targetState("b-02")
	if state.Attempts != 0 || state.ProgressCount != 1 {
		t.Fatalf("partial progress state=%+v", state)
	}
	// A later no-output failure starts a new streak.
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{Status: "failed"}, "failed")
	if got := e.targetState("b-02").Attempts; got != 1 {
		t.Fatalf("after reset attempts=%d", got)
	}
}

func TestTargetAcceptedFlagClearsFailures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "solved.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "solved", domain.ModeCTF, nil)
	flag := "flag{solved}"
	sum := sha256.Sum256([]byte(flag))
	if err := st.RecordFlagSubmission(ctx, task.ID, 0, flag, hex.EncodeToString(sum[:]), true, "accepted"); err != nil {
		t.Fatal(err)
	}
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	intent := &domain.Intent{TaskID: task.ID, Summary: "Resume b-02"}
	state := e.targetState("b-02")
	state.Attempts = 2
	state.ExpectedFlags = 1
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{Status: "done",
		Findings: []domain.FindingSpec{{Title: "solved", Severity: "high", Evidence: flag}}}, "")
	if !state.Solved || state.Attempts != 0 {
		t.Fatalf("solved state=%+v", state)
	}
}

func TestTargetCooldownExhaustionAndManualRecover(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "retry", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	spec := func(priority int, budget map[string]any) domain.IntentSpec {
		return domain.IntentSpec{Summary: "Resume b-02", Rationale: "test", GoalIDs: []int64{goal.ID},
			Priority: priority, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle, Budget: budget}
	}
	state := e.targetState("b-02")
	state.Attempts = 3
	state.CooldownUntil = time.Now().Add(time.Minute)
	_ = e.createIntent(ctx, task.ID, task, goal, spec(5, nil))
	if n, _ := st.ListIntents(ctx, task.ID); len(n) != 0 {
		t.Fatalf("cooldown created intent: %d", len(n))
	}

	state.CooldownUntil = time.Now().Add(-time.Second)
	_ = e.createIntent(ctx, task.ID, task, goal, spec(5, nil))
	if n, _ := st.ListIntents(ctx, task.ID); len(n) != 0 {
		t.Fatalf("no-strategy retry created intent: %d", len(n))
	}

	state.CooldownUntil = time.Now().Add(-time.Second)
	_ = e.createIntent(ctx, task.ID, task, goal, spec(5, map[string]any{"strategy_tag": "new-path"}))
	list, _ := st.ListIntents(ctx, task.ID)
	if len(list) != 1 {
		t.Fatalf("new strategy created %d intents", len(list))
	}
	if list[0].Priority != 3 {
		t.Fatalf("priority=%d, want 3", list[0].Priority)
	}

	state.Attempts = 5
	state.Exhausted = true
	_ = e.createIntent(ctx, task.ID, task, goal, spec(5, map[string]any{"strategy_tag": "another"}))
	if n, _ := st.ListIntents(ctx, task.ID); len(n) != 1 {
		t.Fatalf("exhausted created intent: %d", len(n))
	}

	_ = e.createIntent(ctx, task.ID, task, goal, spec(5, map[string]any{"manual_recover": true}))
	if state.Exhausted || state.Attempts != 0 {
		t.Fatalf("manual recover state=%+v", state)
	}
}

func TestDuplicateFactDoesNotResetTargetFailures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dedupe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "dedupe", domain.ModeCTF, nil)
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	intent := &domain.Intent{TaskID: task.ID, Summary: "Resume b-02"}
	fact := domain.FactSpec{Summary: "same fact", Evidence: "same evidence"}
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{Status: "blocked", Facts: []domain.FactSpec{fact}}, "first")
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{Status: "blocked", Facts: []domain.FactSpec{fact}}, "duplicate")
	state := e.targetState("b-02")
	if state.Attempts != 1 || state.ProgressCount != 1 {
		t.Fatalf("duplicate fact accounting state=%+v", state)
	}
}

func TestTargetStateSnapshotForPlanner(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	s := e.targetState("b-02")
	s.Attempts = 5
	s.Exhausted = true
	s.ExhaustedReason = "two flags unreachable"
	s.LastFailureReason = "timeout"
	views := e.targetSnapshots()
	if len(views) != 1 || views[0].Target != "b-02" || !views[0].Exhausted || views[0].ExhaustedReason != "two flags unreachable" {
		t.Fatalf("snapshot=%+v", views)
	}
}

func TestSemanticFactFingerprintNormalizesCommonWording(t *testing.T) {
	a := domain.FactSpec{Summary: "port 80 open", Detail: "http service"}
	b := domain.FactSpec{Summary: "80/tcp is open", Detail: "HTTP service"}
	if semanticFactFingerprint(a) != semanticFactFingerprint(b) {
		t.Fatalf("semantic mismatch: %q vs %q", semanticFactFingerprint(a), semanticFactFingerprint(b))
	}
}

func TestAcceptedSingleFlagCancelsSameTargetIntents(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "singleflag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "single", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	mk := func(summary, target string) *domain.Intent {
		i, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
			Summary: summary, TargetKey: target, StrategyTag: "test", Rationale: "test",
			GoalIDs: []int64{goal.ID}, Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
		}, 0, domain.IntentProposed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.ApproveIntent(ctx, task.ID, i.ID, domain.ApprovalApproved, "policy:ctf", "ctf", domain.RiskLow, nil); err != nil {
			t.Fatal(err)
		}
		return i
	}
	first := mk("solve b-02", "b-02")
	other := mk("verify b-02", "b-02")
	unrelated := mk("solve c-01", "c-01")
	flag := "flag{one}"
	sum := sha256.Sum256([]byte(flag))
	if err := st.RecordFlagSubmission(ctx, task.ID, 0, flag, hex.EncodeToString(sum[:]), true, "accepted"); err != nil {
		t.Fatal(err)
	}
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	e.targetState("b-02").ExpectedFlags = 1
	e.recordTargetOutcome(ctx, task.ID, first, &domain.WorkResult{Status: "done",
		Findings: []domain.FindingSpec{{Title: "b-02 solved", Evidence: flag}}}, "")
	got, _ := st.GetIntent(ctx, other.ID)
	if got.State != domain.IntentCancelled {
		t.Fatalf("other target state=%s", got.State)
	}
	got, _ = st.GetIntent(ctx, unrelated.ID)
	if got.State != domain.IntentApproved {
		t.Fatalf("unrelated state=%s", got.State)
	}
}

func TestDeferFreezesOnlySameStrategy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "defer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "defer", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	mk := func(summary, strategy string, fast bool) *domain.Intent {
		budget := map[string]any{}
		if fast {
			budget["fast_lane"] = true
		}
		i, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
			Summary: summary, TargetKey: "b-02", StrategyTag: strategy, Rationale: "test",
			GoalIDs: []int64{goal.ID}, Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle, Budget: budget,
		}, 0, domain.IntentProposed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.ApproveIntent(ctx, task.ID, i.ID, domain.ApprovalApproved, "policy:ctf", "ctf", domain.RiskLow, nil); err != nil {
			t.Fatal(err)
		}
		return i
	}
	same := mk("resume b-02 same", "oa-auth", false)
	other := mk("resume b-02 other", "mysql-creds", false)
	fast := mk("submit b-02 flag", "verify", true)
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	e.deferTargetIntents(ctx, task.ID, "b-02", "oa-auth", false, "cooldown")
	for id, want := range map[int64]domain.IntentState{same.ID: domain.IntentDeferred, other.ID: domain.IntentApproved, fast.ID: domain.IntentApproved} {
		got, _ := st.GetIntent(ctx, id)
		if got.State != want {
			t.Fatalf("intent %d state=%s want=%s", id, got.State, want)
		}
	}
}

func TestMergerSubmitsNewlyMergedFlag(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "merger-submit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "merger-submit", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "merge fragments", TargetKey: "challenge:x-01", Rationale: "test",
		GoalIDs: []int64{goal.ID}, Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSwarm,
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, _ := json.Marshal(domain.WorkResult{Status: "done", Findings: []domain.FindingSpec{{
		Title: "assembled flag", Severity: "high", Evidence: "flag{merged_fragment}",
	}}})
	run := &store.SwarmRun{TaskID: task.ID, IntentID: intent.ID, Lanes: 2, Quorum: 1, Mode: "swarm", State: "running"}
	sub := &recordingSubmitter{}
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1, Submitter: sub})
	e.mergeSwarm(ctx, task.ID, intent.ID, run, []store.SwarmLaneResult{{IntentID: intent.ID, LaneID: "lane-1", Status: "done", ResultJSON: string(resultJSON)}}, ctfLanePolicy{})
	seen := false
	for _, f := range sub.flags {
		if f == "flag{merged_fragment}" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("merged flag not submitted: %v", sub.flags)
	}
}

func TestCrossTaskEvidenceIsRejected(t *testing.T) {
	if !crossTaskReference("see artifacts/task-7/work-53/evidence-summary.txt", 12) {
		t.Fatal("cross-task artifact not detected")
	}
	if crossTaskReference("see artifacts/task-12/work-53/notes.txt", 12) {
		t.Fatal("own task artifact rejected")
	}
	if !crossTaskReference("sqlite3 hornedviper.db", 12) {
		t.Fatal("host database access not detected")
	}
}

func TestExpectedFlagsInferredFromFactsEnablesClose(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "expected.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "expected", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "solve x-01", TargetKey: "challenge:x-01", Rationale: "test",
		GoalIDs: []int64{goal.ID}, Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	flag := "flag{inferred}"
	sum := sha256.Sum256([]byte(flag))
	if err := st.RecordFlagSubmission(ctx, task.ID, 0, flag, hex.EncodeToString(sum[:]), true, "accepted"); err != nil {
		t.Fatal(err)
	}
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{
		Status:   "done",
		Facts:    []domain.FactSpec{{Summary: "x-01 flag_count=1", Evidence: "api", Confidence: domain.ConfidenceObserved}},
		Findings: []domain.FindingSpec{{Title: "x-01 solved", Evidence: flag}},
	}, "")
	if !e.targetState("challenge:x-01").Solved {
		t.Fatal("expected target to be marked solved from fact flag_count")
	}
	list, _ := st.ListIntents(ctx, task.ID)
	for _, in := range list {
		if strings.HasPrefix(in.Summary, "Close completed challenge") {
			t.Fatalf("close created without Planner: %+v", list)
		}
	}
	_ = e.createIntent(ctx, task.ID, task, goal, domain.IntentSpec{
		Summary: "Planner close x-01", TargetKey: "challenge:x-01", StrategyTag: "close", CloseTarget: true,
		GoalIDs: []int64{goal.ID}, Priority: 10, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	})
	list, _ = st.ListIntents(ctx, task.ID)
	found := false
	for _, in := range list {
		if strings.HasPrefix(in.Summary, "Close completed challenge") && strings.Contains(in.Summary, "x-01") {
			found = true
		}
	}
	if !found {
		t.Fatalf("planner close did not create close intent: %+v", list)
	}
}

func TestPlannerCloseTargetDirect(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "planner-close.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "planner-close", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1,
		CloseChallenge: func(_ context.Context, _ *domain.Task, challenge string) error {
			if challenge != "challenge:b-02" {
				t.Fatalf("challenge=%q", challenge)
			}
			return nil
		}})
	state := e.targetState("challenge:b-02")
	state.AcceptedFlags = 1
	state.ExpectedFlags = 1
	state.Solved = true
	_ = e.createIntent(ctx, task.ID, task, goal, domain.IntentSpec{
		Summary: "Planner close b-02", TargetKey: "challenge:b-02", StrategyTag: "close", CloseTarget: true,
		GoalIDs: []int64{goal.ID}, Priority: 10, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	})
	if !state.CloseRequested {
		t.Fatal("direct close not executed")
	}
	list, _ := st.ListIntents(ctx, task.ID)
	for _, in := range list {
		if strings.HasPrefix(in.Summary, "Close completed challenge") {
			t.Fatalf("unexpected fallback close intent: %+v", list)
		}
	}
}

func TestPlatformCompletedFactAllowsPlannerClose(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "completed-fact.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "completed-fact", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "check a-05", TargetKey: "challenge:a-05", Rationale: "test",
		GoalIDs: []int64{goal.ID}, Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1,
		CloseChallenge: func(_ context.Context, _ *domain.Task, challenge string) error {
			closed = true
			if challenge != "challenge:a-05" {
				t.Fatalf("challenge=%q", challenge)
			}
			return nil
		}})
	e.recordTargetOutcome(ctx, task.ID, intent, &domain.WorkResult{
		Status: "done",
		Facts:  []domain.FactSpec{{Summary: `{"flag_count":1,"is_completed":true,"correct_flag_count":1}`, Evidence: "catalog", Confidence: domain.ConfidenceObserved}},
	}, "")
	state := e.targetState("challenge:a-05")
	if !state.Solved || !state.PlatformCompleted {
		t.Fatalf("state=%+v", state)
	}
	_ = e.createIntent(ctx, task.ID, task, goal, domain.IntentSpec{
		Summary: "Planner close a-05", TargetKey: "challenge:a-05", StrategyTag: "close", CloseTarget: true,
		GoalIDs: []int64{goal.ID}, Priority: 10, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	})
	if !closed {
		t.Fatal("platform-completed target was not closed")
	}
}

func TestFailuresForTargetArePassedToNextWorker(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "failures.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "failures", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "solve b-02", TargetKey: "challenge:b-02", StrategyTag: "oa",
		GoalIDs: []int64{goal.ID}, Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := st.ClaimNextIntent(ctx, task.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishWork(ctx, work.ID, domain.WorkFailed, "same PoC blocked", 0, 0); err != nil {
		t.Fatal(err)
	}
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 1})
	failures := e.failuresForTarget(ctx, task.ID, "challenge:b-02")
	if len(failures) != 1 || failures[0].Reason != "same PoC blocked" {
		t.Fatalf("failures=%+v", failures)
	}
	_ = intent
}

func TestReconcileActiveFromWorks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "reconcile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "reconcile", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "work", TargetKey: "challenge:x-01", Rationale: "test",
		GoalIDs: []int64{goal.ID}, Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := st.ClaimNextIntent(ctx, task.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 4})
	e.active = 4
	e.reconcileActive(ctx, task.ID)
	if e.active != 1 {
		t.Fatalf("active=%d, want 1", e.active)
	}
	_ = work
	_ = intent
}

func TestIdleWorkersAreReported(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "idle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "idle", domain.ModeCTF, nil)
	_, _ = st.SetGoal(ctx, task.ID, "goal", nil)
	e := New(st, swarmTestRuntime{}, Config{MaxWorkers: 4, IdleReportAfter: time.Millisecond})
	e.active = 2
	e.reconcileActive(ctx, task.ID)
	e.checkIdle(ctx, task.ID)
	time.Sleep(2 * time.Millisecond)
	e.checkIdle(ctx, task.ID)
	events, _ := st.ListEvents(ctx, task.ID, 20)
	found := false
	for _, ev := range events {
		if ev.Type == domain.EventWorkersIdle {
			found = true
		}
	}
	if !found {
		t.Fatalf("idle event missing: %+v", events)
	}
}
