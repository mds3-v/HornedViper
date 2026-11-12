package submit

import (
	"context"
	"strings"
	"testing"

	"hornedviper/internal/domain"
)

func TestCommandSubmitterAccepts(t *testing.T) {
	s := NewCommand(`printf 'accepted %s' "$HV_FLAG"`)
	if !s.Enabled() {
		t.Fatal("expected enabled")
	}
	res, err := s.Submit(context.Background(), &domain.Task{ID: 1}, domain.Finding{
		ID: 2, Evidence: "flag{demo_test}",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !res.Accepted || res.Flag != "flag{demo_test}" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Message, "accepted") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestCommandSubmitterRejects(t *testing.T) {
	s := NewCommand("exit 1")
	res, err := s.Submit(context.Background(), &domain.Task{ID: 1}, domain.Finding{
		ID: 2, Evidence: "flag{demo_test}",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Accepted {
		t.Fatalf("expected rejected, got %+v", res)
	}
}

func TestCommandSubmitterPlaceholder(t *testing.T) {
	s := NewCommand("printf 'ok %s' {flag}")
	res, err := s.Submit(context.Background(), &domain.Task{ID: 1}, domain.Finding{
		ID: 2, Evidence: "flag{demo_test}",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !res.Accepted || !strings.Contains(res.Message, "flag{demo_test}") {
		t.Fatalf("result = %+v", res)
	}
}

func TestCommandSubmitterNoFlag(t *testing.T) {
	s := NewCommand("true")
	if _, err := s.Submit(context.Background(), &domain.Task{ID: 1}, domain.Finding{ID: 2, Evidence: "no flag"}); err == nil {
		t.Fatal("expected error when no flag is present")
	}
}
