package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

func TestReportExporterWritesDownloadableFile(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "api-report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "报告导出", domain.ModeCTF, nil)
	_, _ = st.SetGoal(ctx, task.ID, "goal", nil)
	dir := t.TempDir()
	exp := &reportExporter{st: st, artifactDir: dir}
	out, err := exp.export(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.URL, "/reports?task=") {
		t.Fatalf("url=%q", out.URL)
	}
	if !isReportCommand("/导出报告 " + "12") {
		t.Fatal("command not detected")
	}
	if reportTaskIDFromText("/导出报告 12") != 12 {
		t.Fatal("task id not parsed")
	}
}
