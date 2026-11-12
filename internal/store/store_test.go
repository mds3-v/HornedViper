package store

import (
	"context"
	"testing"
	"time"

	"hornedviper/internal/domain"
)

func TestStoreEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	task, err := st.CreateTask(ctx, "test", domain.ModeCTF, map[string]any{"max_minutes": 5})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := st.SetTaskStatus(ctx, task.ID, domain.TaskPlanning); err != nil {
		t.Fatalf("planning: %v", err)
	}
	if err := st.SetTaskStatus(ctx, task.ID, domain.TaskRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	goal, err := st.SetGoal(ctx, task.ID, "get the flag", []string{"finding"})
	if err != nil {
		t.Fatalf("goal: %v", err)
	}
	if _, err := st.AddConstraint(ctx, task.ID, "deny", "no delete"); err != nil {
		t.Fatalf("constraint: %v", err)
	}
	if _, err := st.AddTodo(ctx, task.ID, "recon first"); err != nil {
		t.Fatalf("todo: %v", err)
	}

	spec := domain.IntentSpec{
		Summary: "recon", Rationale: "start", GoalIDs: []int64{goal.ID},
		Priority: 5, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	}
	intent, err := st.CreateIntent(ctx, task.ID, spec, 0, domain.IntentProposed)
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	if _, err := st.ApproveIntent(ctx, task.ID, intent.ID, domain.ApprovalApproved, "policy:ctf", "ctf", domain.RiskLow, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	work, claimed, err := st.ClaimNextIntent(ctx, task.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if work == nil || claimed == nil {
		t.Fatal("expected a claimed work")
	}
	if claimed.ID != intent.ID {
		t.Fatalf("claimed wrong intent: %d != %d", claimed.ID, intent.ID)
	}

	asset, err := st.RecordAsset(ctx, domain.AssetSpec{Type: "endpoint", Value: "https://demo.local/"})
	if err != nil {
		t.Fatalf("asset: %v", err)
	}
	fact, err := st.RecordFact(ctx, task.ID, intent.ID, work.ID, domain.FactSpec{
		Summary: "reachable", Detail: "200", Evidence: "curl -> 200",
		Confidence: domain.ConfidenceObserved, AssetIDs: []int64{asset.ID},
	})
	if err != nil {
		t.Fatalf("fact: %v", err)
	}
	finding, err := st.RecordFinding(ctx, task.ID, intent.ID, work.ID, domain.FindingSpec{
		Title: "flag", Severity: "high", Evidence: "flag{test}", AssetIDs: []int64{asset.ID},
	})
	if err != nil {
		t.Fatalf("finding: %v", err)
	}
	if _, err := st.RecordEvidence(ctx, task.ID, work.ID, domain.EvidenceSpec{Type: "file", Path: "artifacts/x"}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if err := st.FinishWork(ctx, work.ID, domain.WorkSucceeded, "", 10, 20); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := st.SetGoalState(ctx, goal.ID, domain.GoalMet, "test"); err != nil {
		t.Fatalf("goal met: %v", err)
	}

	facts, err := st.ListFacts(ctx, task.ID, 10)
	if err != nil || len(facts) != 1 || facts[0].ID != fact.ID {
		t.Fatalf("facts: %v %+v", err, facts)
	}
	findings, err := st.ListFindings(ctx, task.ID, 10)
	if err != nil || len(findings) != 1 || findings[0].ID != finding.ID {
		t.Fatalf("findings: %v %+v", err, findings)
	}
	if _, err := st.AppendEvent(ctx, task.ID, domain.EventFactRecorded, "worker", "w1", "fact", fact.ID, nil); err != nil {
		t.Fatalf("event: %v", err)
	}
	events, err := st.ListEvents(ctx, task.ID, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events: %v %+v", err, events)
	}
	counts, err := st.GraphCounts(ctx, task.ID)
	if err != nil || counts["fact"] != 1 || counts["finding"] != 1 || counts["goal"] != 1 || counts["intent"] != 1 {
		t.Fatalf("counts: %v %+v", err, counts)
	}
}

func TestResolveApprovalAndFlagDedupe(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	task, _ := st.CreateTask(ctx, "approval", domain.ModePentest, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "test", nil)
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "dangerous", Rationale: "test", GoalIDs: []int64{goal.ID},
		Priority: 8, Risk: domain.RiskHigh, DangerousActions: []string{"modify"},
		ExecutionMode: domain.ExecutionSingle,
	}, 0, domain.IntentProposed)
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	if _, err := st.CreatePendingApproval(ctx, task.ID, intent.ID, domain.RiskHigh, []string{"modify"}); err != nil {
		t.Fatalf("pending: %v", err)
	}
	pending, err := st.PendingApprovals(ctx, task.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending list: %v %+v", err, pending)
	}
	if _, err := st.ResolveApproval(ctx, pending[0].ID, domain.ApprovalApproved, "human:test", "ok"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, _ := st.GetIntent(ctx, intent.ID)
	if got.State != domain.IntentApproved {
		t.Fatalf("intent state = %s", got.State)
	}

	hash := "abc123"
	ok, err := st.FlagAlreadySubmitted(ctx, task.ID, hash)
	if err != nil || ok {
		t.Fatalf("dedupe before: %v %v", ok, err)
	}
	if err := st.RecordFlagSubmission(ctx, task.ID, 1, "flag{x}", hash, true, "accepted"); err != nil {
		t.Fatalf("record: %v", err)
	}
	ok, err = st.FlagAlreadySubmitted(ctx, task.ID, hash)
	if err != nil || !ok {
		t.Fatalf("dedupe after: %v %v", ok, err)
	}
}

func TestTaskFlagAttributionUsesStructuredIntentTarget(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task, _ := st.CreateTask(ctx, "flags", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	intent, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
		Summary: "solve a-08", TargetKey: "challenge:a-08", Rationale: "x", GoalIDs: []int64{goal.ID},
		Priority: 1, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
	}, 0, domain.IntentApproved)
	if err != nil {
		t.Fatal(err)
	}
	flag := "flag{attributed}"
	if _, err := st.RecordFinding(ctx, task.ID, intent.ID, 0, domain.FindingSpec{
		Title: "cve-2025-9999 exploit", Severity: "high", Evidence: flag,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordFact(ctx, task.ID, intent.ID, 0, domain.FactSpec{
		Summary: "whole-filesystem scan", Evidence: "found " + flag, Confidence: domain.ConfidenceObserved,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncTaskFlags(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListTaskFlags(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("flags=%d %+v", len(rows), rows)
	}
	if rows[0].Challenge != "a-08" {
		t.Fatalf("challenge=%q, want a-08", rows[0].Challenge)
	}
}

func TestTaskAssetsAreScoped(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t1, _ := st.CreateTask(ctx, "one", domain.ModeCTF, nil)
	t2, _ := st.CreateTask(ctx, "two", domain.ModeCTF, nil)
	a1, err := st.RecordAssetForTask(ctx, t1.ID, domain.AssetSpec{Type: "host", Value: "shared.example"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := st.RecordAssetForTask(ctx, t2.ID, domain.AssetSpec{Type: "host", Value: "shared.example"})
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID != a2.ID {
		t.Fatalf("assets should be globally deduped: %d != %d", a1.ID, a2.ID)
	}
	if _, err := st.RecordAssetForTask(ctx, t2.ID, domain.AssetSpec{Type: "host", Value: "only-two.example"}); err != nil {
		t.Fatal(err)
	}
	one, _ := st.ListTaskAssets(ctx, t1.ID, 100)
	two, _ := st.ListTaskAssets(ctx, t2.ID, 100)
	if len(one) != 1 || len(two) != 2 {
		t.Fatalf("one=%d two=%d", len(one), len(two))
	}
}
