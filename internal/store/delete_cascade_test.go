package store

import (
	"context"
	"path/filepath"
	"testing"

	"hornedviper/internal/domain"
)

func TestDeleteTaskRemovesAllTaskScopedRows(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delete-task.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "delete", domain.ModeCTF, nil)
	_, _ = st.SetGoal(ctx, task.ID, "goal", nil)
	_, _ = st.RecordFact(ctx, task.ID, 0, 0, domain.FactSpec{Summary: "f"})
	_, _ = st.RecordFinding(ctx, task.ID, 0, 0, domain.FindingSpec{Title: "x", Evidence: "flag{x}"})
	_, _ = st.RecordAssetForTask(ctx, task.ID, domain.AssetSpec{Type: "host", Value: "x.test"})
	_ = st.AddTaskFlag(ctx, task.ID, "a-01", "flag{x}", "found")
	_ = st.MarkChallengeCompleted(ctx, task.ID, "a-01")
	_ = st.RecordTraffic(ctx, domain.Traffic{TaskID: task.ID, Method: "GET", Host: "x.test", URL: "http://x.test/"})
	_ = st.RecordLLMCall(ctx, domain.LLMCall{TaskID: task.ID, Role: "worker"})
	if err := st.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"SELECT COUNT(*) FROM tasks WHERE id=?", "SELECT COUNT(*) FROM exploration_nodes WHERE task_id=?",
		"SELECT COUNT(*) FROM asset_tasks WHERE task_id=?", "SELECT COUNT(*) FROM task_flags WHERE task_id=?",
		"SELECT COUNT(*) FROM challenge_completions WHERE task_id=?",
		"SELECT COUNT(*) FROM llm_calls WHERE task_id=?",
	}
	for _, q := range checks {
		var n int
		if err := st.db.QueryRow(q, task.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("query %q = %d", q, n)
		}
	}
	if titles, err := st.DeletedTitles(ctx, "task"); err != nil || titles[task.ID] != task.Title {
		t.Fatalf("deleted task title=%q err=%v", titles[task.ID], err)
	}
	// Task traffic and LLM records are audit history and must remain.
	for _, q := range []string{
		"SELECT COUNT(*) FROM traffic WHERE task_id=?",
	} {
		var n int
		if err := st.db.QueryRow(q, task.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("audit records missing: %q", q)
		}
	}
}

func TestDeleteConversationRemovesAllConversationScopedRows(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "delete-conv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	conv, err := st.CreateConversation(ctx, "delete", "High", "workspace-write")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.CreateConversationMessage(ctx, conv.ID, "user", "hi", "done")
	_ = st.SetConversationSummary(ctx, conv.ID, "s", 1)
	_ = st.RecordTraffic(ctx, domain.Traffic{ConversationID: conv.ID, Method: "GET", Host: "x.test", URL: "http://x.test/"})
	_ = st.RecordLLMCall(ctx, domain.LLMCall{ConversationID: conv.ID, Role: "session_agent"})
	if err := st.DeleteConversation(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"SELECT COUNT(*) FROM conversations WHERE id=?", "SELECT COUNT(*) FROM conversation_messages WHERE conversation_id=?",
		"SELECT COUNT(*) FROM conversation_summaries WHERE conversation_id=?",
		"SELECT COUNT(*) FROM llm_calls WHERE conversation_id=?",
	}
	for _, q := range checks {
		var n int
		if err := st.db.QueryRow(q, conv.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("query %q = %d", q, n)
		}
	}
	if titles, err := st.DeletedTitles(ctx, "conversation"); err != nil || titles[conv.ID] != conv.Title {
		t.Fatalf("deleted conversation title=%q err=%v", titles[conv.ID], err)
	}
	// Conversation traffic and LLM records are retained as audit history.
	for _, q := range []string{
		"SELECT COUNT(*) FROM traffic WHERE conversation_id=?",
	} {
		var n int
		if err := st.db.QueryRow(q, conv.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("audit records missing: %q", q)
		}
	}
}
