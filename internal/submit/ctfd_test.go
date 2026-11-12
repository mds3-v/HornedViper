package submit

import (
	"testing"

	"hornedviper/internal/domain"
)

func TestExtractFlag(t *testing.T) {
	if got := extractFlag(domain.Finding{Evidence: "response contains FLAG{abc_123}"}); got != "FLAG{abc_123}" {
		t.Fatalf("flag = %q", got)
	}
	if got := extractFlag(domain.Finding{Title: "no flag"}); got != "" {
		t.Fatalf("flag = %q", got)
	}
}

func TestTaskCTFConfig(t *testing.T) {
	task := &domain.Task{Budget: map[string]any{"ctf": map[string]any{
		"platform": "ctfd", "url": "https://x/", "token": "t", "challenge_id": float64(7),
	}}}
	url, token, id, ok := taskCTFConfig(task)
	if !ok || url != "https://x" || token != "t" || id != 7 {
		t.Fatalf("config = %q %q %d %v", url, token, id, ok)
	}
	if _, _, _, ok := taskCTFConfig(&domain.Task{}); ok {
		t.Fatal("empty budget must not be a CTF config")
	}
}
