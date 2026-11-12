package rules

import (
	"testing"

	"hornedviper/internal/domain"
)

func TestCheck(t *testing.T) {
	e := New([]domain.InterceptRule{
		{Name: "rm", Priority: 10, Target: "input", MatchType: "regex", Pattern: `rm\s+-rf`, Mode: "ask", Enabled: true},
		{Name: "exfil", Priority: 20, Target: "input", MatchType: "string", Pattern: "pastebin", Mode: "deny", Enabled: true},
	})
	if mode, name := e.Check("Bash", []byte(`{"command":"rm -rf /tmp/x"}`)); mode != "ask" || name != "rm" {
		t.Fatalf("rm check = %s %s", mode, name)
	}
	if mode, name := e.Check("WebFetch", []byte(`{"url":"https://pastebin.com/x"}`)); mode != "deny" || name != "exfil" {
		t.Fatalf("exfil check = %s %s", mode, name)
	}
	if mode, _ := e.Check("Read", []byte(`{"path":"a.txt"}`)); mode != "allow" {
		t.Fatalf("read check = %s", mode)
	}
}
