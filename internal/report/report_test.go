package report

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

func TestBuildTaskIncludesCoreSections(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "报告测试", domain.ModeCTF, nil)
	_, _ = st.SetGoal(ctx, task.ID, "完成测试题目", nil)
	_, _ = st.RecordFinding(ctx, task.ID, 0, 0, domain.FindingSpec{
		Title: "发现 flag", Severity: "high", Evidence: "flag{test}",
	})
	if err := st.AddTaskFlag(ctx, task.ID, "a-01", "flag{test}", "found"); err != nil {
		t.Fatal(err)
	}
	doc, err := BuildTask(ctx, st, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# HornedViper 任务报告", "Flag 结果", "a-01", "flag{test}", "Findings"} {
		if !strings.Contains(doc.Content, want) {
			t.Fatalf("missing %q in report:\n%s", want, doc.Content)
		}
	}
	if !strings.Contains(doc.Filename, "task-") || !strings.HasSuffix(doc.Filename, ".md") {
		t.Fatalf("filename=%q", doc.Filename)
	}
}
