package store

import (
	"context"
	"testing"
	"time"

	"hornedviper/internal/domain"
)

func TestClaimFastIntentUsesReservedSelector(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task, _ := st.CreateTask(ctx, "fast", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	create := func(summary string, priority int, budget map[string]any) *domain.Intent {
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
	normal := create("start and solve a-10", 9, nil)
	fast := create("submit the captured flag", 1, map[string]any{"fast_lane": true})

	wantFast := true
	_, got, err := st.ClaimNextIntentFiltered(ctx, task.ID, "worker-fast", time.Minute, 5*time.Minute, &wantFast)
	if err != nil || got == nil || got.ID != fast.ID {
		t.Fatalf("fast claim got=%v err=%v", got, err)
	}
	wantNormal := false
	_, got, err = st.ClaimNextIntentFiltered(ctx, task.ID, "worker-normal", time.Minute, 5*time.Minute, &wantNormal)
	if err != nil || got == nil || got.ID != normal.ID {
		t.Fatalf("normal claim got=%v err=%v", got, err)
	}
}

func TestClaimIntentPriorityAging(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task, _ := st.CreateTask(ctx, "aging", domain.ModeCTF, nil)
	goal, _ := st.SetGoal(ctx, task.ID, "goal", nil)
	create := func(summary string, priority int) *domain.Intent {
		i, err := st.CreateIntent(ctx, task.ID, domain.IntentSpec{
			Summary: summary, Rationale: "test", GoalIDs: []int64{goal.ID},
			Priority: priority, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
		}, 0, domain.IntentProposed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.ApproveIntent(ctx, task.ID, i.ID, domain.ApprovalApproved, "policy:ctf", "ctf", domain.RiskLow, nil); err != nil {
			t.Fatal(err)
		}
		return i
	}
	old := create("old low priority", 1)
	_ = create("new high priority", 2)
	// Make old 30 minutes old: with 5-minute aging it gains +6 effective priority.
	if _, err := st.db.ExecContext(ctx, `UPDATE exploration_nodes SET created_at=? WHERE id=?`, nowMS()-30*60*1000, old.ID); err != nil {
		t.Fatal(err)
	}
	wantNormal := false
	_, got, err := st.ClaimNextIntentFiltered(ctx, task.ID, "worker", time.Minute, 5*time.Minute, &wantNormal)
	if err != nil || got == nil || got.ID != old.ID {
		t.Fatalf("aged claim got=%v err=%v, want old=%d", got, err, old.ID)
	}
}
