package store

import (
	"context"
	"path/filepath"
	"testing"

	"hornedviper/internal/domain"
)

func TestFindingGroupsAndSearch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "findings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task1, _ := st.CreateTask(ctx, "one", domain.ModeCTF, nil)
	task2, _ := st.CreateTask(ctx, "two", domain.ModeCTF, nil)
	for i := 0; i < 7; i++ {
		if _, err := st.RecordFinding(ctx, task1.ID, 0, 0, domain.FindingSpec{Title: "needle", Severity: "high", Evidence: "ev"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RecordFinding(ctx, task2.ID, 0, 0, domain.FindingSpec{Title: "other", Severity: "low", Evidence: "ev"}); err != nil {
		t.Fatal(err)
	}
	groups, err := st.FindingGroups(ctx)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	total, err := st.CountFindingsSearch(ctx, task1.ID, "needle")
	if err != nil || total != 7 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	page, err := st.ListFindingsPageSearch(ctx, task1.ID, "needle", 5, 5)
	if err != nil || len(page) != 2 {
		t.Fatalf("page=%d err=%v", len(page), err)
	}
}
