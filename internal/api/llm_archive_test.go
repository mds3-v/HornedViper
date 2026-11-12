package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hornedviper/internal/config"
	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

func TestArchiveLLMCallsWritesGzipJSONL(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, _ := st.CreateTask(ctx, "archive", domain.ModeCTF, nil)
	if err := st.RecordLLMCall(ctx, domain.LLMCall{TaskID: task.ID, Role: "worker", RequestJSON: "req", ResponseJSON: "resp"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cs, err := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := cs.Get()
	cfg.ArtifactDir = dir
	if err := cs.Save(cfg); err != nil {
		t.Fatal(err)
	}
	srv := &Server{st: st, cfgStore: cs}
	if err := srv.archiveLLMCalls(ctx, "task", task.ID, "archive"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "llm-archive", "task-*.jsonl.gz"))
	if len(files) != 1 {
		t.Fatalf("archive files=%v", files)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	dec := json.NewDecoder(gz)
	var header map[string]any
	if err := dec.Decode(&header); err != nil {
		t.Fatal(err)
	}
	var call domain.LLMCall
	if err := dec.Decode(&call); err != nil {
		t.Fatal(err)
	}
	if call.RequestJSON != "req" || call.ResponseJSON != "resp" {
		t.Fatalf("call=%+v", call)
	}
}
