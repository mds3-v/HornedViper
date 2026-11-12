package store

import (
	"context"
	"path/filepath"
	"testing"

	"hornedviper/internal/domain"
)

func TestLLMCallGroupsAndSearch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "llm-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		if err := st.RecordLLMCall(ctx, domain.LLMCall{TaskID: 7, Role: "worker", Model: "m", RequestJSON: "needle"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordLLMCall(ctx, domain.LLMCall{ConversationID: 3, Role: "session_agent", Model: "m", RequestJSON: "other"}); err != nil {
		t.Fatal(err)
	}
	groups, err := st.LLMCallGroups(ctx)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	total, err := st.CountLLMCallsSearch(ctx, 7, 0, "needle")
	if err != nil || total != 7 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	page, err := st.ListLLMCallsPageSearch(ctx, 7, 0, "needle", 5, 5)
	if err != nil || len(page) != 2 {
		t.Fatalf("page=%d err=%v", len(page), err)
	}
}
