package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"hornedviper/internal/domain"
)

// archiveLLMCalls writes the full LLM audit trail for a deleted task or
// conversation to a gzip JSONL file before the rows are removed from SQLite.
func (s *Server) archiveLLMCalls(ctx context.Context, kind string, id int64, title string) error {
	var calls []domain.LLMCall
	var err error
	switch kind {
	case "task":
		calls, err = s.st.ListLLMCallsForTask(ctx, id)
	case "conversation":
		calls, err = s.st.ListLLMCallsForConversation(ctx, id)
	default:
		return fmt.Errorf("unknown archive kind %q", kind)
	}
	if err != nil {
		return err
	}
	if len(calls) == 0 {
		return nil
	}
	dir := s.cfgStore.Get().ArtifactDir
	if dir == "" {
		return fmt.Errorf("artifact directory is not configured")
	}
	archiveDir := filepath.Join(dir, "llm-archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%d-%s.jsonl.gz", kind, id, time.Now().Format("20060102-150405"))
	tmp, err := os.CreateTemp(archiveDir, ".archive-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	gz := gzip.NewWriter(tmp)
	enc := json.NewEncoder(gz)
	if err := enc.Encode(map[string]any{
		"archive": map[string]any{
			"kind": kind, "id": id, "title": title, "archived_at": time.Now().Format(time.RFC3339),
		},
	}); err != nil {
		return err
	}
	for i := range calls {
		if err := enc.Encode(calls[i]); err != nil {
			return err
		}
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	final := filepath.Join(archiveDir, name)
	if err := os.Rename(tmpName, final); err != nil {
		return err
	}
	ok = true
	log.Printf("[archive] %s #%d LLM calls=%d file=%s", kind, id, len(calls), final)
	return nil
}
