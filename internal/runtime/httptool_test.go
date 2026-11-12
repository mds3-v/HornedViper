package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hornedviper/internal/domain"
)

func TestHTTPRequestToolRecordsRawPackets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var got domain.Traffic
	tl := httpRequestTool(1, 2, 3, func(t domain.Traffic) { got = t }, nil, nil, nil, nil, false, nil, nil, nil)
	res, err := tl.Call(context.Background(), json.RawMessage(`{
		"method":"POST","url":"`+srv.URL+`/login",
		"headers":{"X-Test":"1"},"body":"username=admin"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res)
	}
	if !strings.HasPrefix(got.RequestRaw, "POST /login HTTP/1.1") {
		t.Fatalf("raw request = %q", got.RequestRaw)
	}
	if !strings.Contains(got.RequestRaw, "X-Test: 1") || !strings.Contains(got.RequestRaw, "username=admin") {
		t.Fatalf("raw request missing headers/body: %q", got.RequestRaw)
	}
	if !strings.HasPrefix(got.ResponseRaw, "HTTP/1.1 200 OK") || !strings.Contains(got.ResponseRaw, `{"ok":true}`) {
		t.Fatalf("raw response = %q", got.ResponseRaw)
	}
	if got.TaskID != 1 || got.WorkID != 2 || got.IntentID != 3 || got.StatusCode != 200 {
		t.Fatalf("traffic metadata = %+v", got)
	}
}

func TestHTTPRequestBlocksDuplicateFlagSubmission(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"correct":true}`))
	}))
	defer srv.Close()
	tl := httpRequestTool(1, 2, 3, nil,
		func(context.Context, int64, string) (bool, error) { return true, nil }, nil, nil, nil, false, nil, nil, nil)
	res, err := tl.Call(context.Background(), json.RawMessage(`{
		"method":"POST","url":"`+srv.URL+`/openapi/v1/challenges/submit",
		"body":"{\"flag\":\"flag{dupe}\",\"unique_code\":\"c-01\"}"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expected duplicate to be blocked: %+v", res)
	}
	if calls != 0 {
		t.Fatalf("duplicate request reached server %d times", calls)
	}
}

func TestHTTPRequestRecordsAcceptedDirectSubmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"correct":true}`))
	}))
	defer srv.Close()
	recorded := ""
	tl := httpRequestTool(1, 2, 3, nil,
		func(context.Context, int64, string) (bool, error) { return false, nil },
		func(_ context.Context, _ int64, flag string, accepted bool, _ string) error {
			if accepted {
				recorded = flag
			}
			return nil
		}, nil, nil, false, nil, nil, nil)
	res, err := tl.Call(context.Background(), json.RawMessage(`{
		"method":"POST","url":"`+srv.URL+`/openapi/v1/challenges/submit",
		"body":"{\"flag\":\"flag{new}\",\"unique_code\":\"c-02\"}"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || recorded != "flag{new}" {
		t.Fatalf("recorded=%q result=%+v", recorded, res)
	}
}

func TestContainerGuardBlocksRepeatedStartAndEarlyClose(t *testing.T) {
	g := newContainerGuard()
	if blocked, _ := g.before(1, "f2-08", "start"); blocked {
		t.Fatal("first start blocked")
	}
	g.after(1, "f2-08", "start", 200, false, false, false)
	if blocked, _ := g.before(1, "f2-08", "start"); !blocked {
		t.Fatal("repeated start not blocked")
	}
	if blocked, _ := g.before(1, "f2-08", "close"); !blocked {
		t.Fatal("early close not blocked")
	}
	g.after(1, "f2-08", "submit", 200, true, false, false)
	if blocked, _ := g.before(1, "f2-08", "close"); !blocked {
		t.Fatal("partial close should be blocked")
	}
	g.after(1, "f2-08", "submit", 200, true, true, false)
	if blocked, _ := g.before(1, "f2-08", "close"); blocked {
		t.Fatal("completed close blocked")
	}
	g.after(1, "f2-08", "close", 200, false, false, false)
	if blocked, _ := g.before(1, "f2-08", "start"); !blocked {
		t.Fatal("completed challenge start not blocked")
	}
}

func TestContainerGuardRejectsSubmitWithoutActiveContainer(t *testing.T) {
	g := newContainerGuard()
	if blocked, _ := g.before(1, "f2-08", "submit"); !blocked {
		t.Fatal("submit without active container should be blocked")
	}
	g.after(1, "f2-08", "start", 200, false, false, false)
	if blocked, _ := g.before(1, "f2-08", "submit"); blocked {
		t.Fatal("submit with active container blocked")
	}
	g.after(1, "f2-08", "submit", 200, true, true, false)
	g.after(1, "f2-08", "close", 200, false, false, false)
	if blocked, _ := g.before(1, "f2-08", "submit"); blocked {
		t.Fatal("completed challenge should allow idempotent submit")
	}
}

func TestSandboxRejectsCrossTaskArtifacts(t *testing.T) {
	root := "/Users/mds3/Documents/ctf-agent-vibecoding"
	runDir := root + "/artifacts/task-12/work-1"
	if got := sandboxViolation("Bash", json.RawMessage(`{"command":"cd `+root+`/artifacts/task-7 && cat work-53/evidence-summary.txt"}`), 12, runDir); got == "" {
		t.Fatal("cross-task bash access not blocked")
	}
	if got := sandboxViolation("Read", json.RawMessage(`{"path":"`+root+`/artifacts/task-7/work-53/evidence-summary.txt"}`), 12, runDir); got == "" {
		t.Fatal("cross-task file read not blocked")
	}
	if got := sandboxViolation("Read", json.RawMessage(`{"path":"`+runDir+`/notes.txt"}`), 12, runDir); got != "" {
		t.Fatalf("own task file blocked: %s", got)
	}
}

func TestHTTPRequestBlocksCompletedChallenge(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tl := httpRequestTool(1, 2, 3, nil, nil, nil, nil, nil, false,
		func(context.Context, int64, string) (bool, error) { return true, nil }, nil, nil)
	res, err := tl.Call(context.Background(), json.RawMessage(`{
		"method":"POST","url":"`+srv.URL+`/openapi/v1/challenges/start?unique_code=a-05"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("completed challenge start not blocked: %+v", res)
	}
	if calls != 0 {
		t.Fatalf("request reached server %d times", calls)
	}
}

func TestHTTPSubmitRecordsCompletionImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"correct":true,"correct_flag_count":1,"total_flag_count":1}`))
	}))
	defer srv.Close()
	recorded := ""
	tl := httpRequestTool(1, 2, 3, nil, nil, nil, nil, nil, false, nil,
		func(_ context.Context, _ int64, challenge string) error { recorded = challenge; return nil }, nil)
	res, err := tl.Call(context.Background(), json.RawMessage(`{
		"method":"POST","url":"`+srv.URL+`/openapi/v1/challenges/submit",
		"body":"{\"unique_code\":\"c-03\",\"flag\":\"flag{ok}\"}"
	}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if recorded != "c-03" {
		t.Fatalf("completion record=%q", recorded)
	}
}

func TestContainerGuardMaxActive409IsNotActive(t *testing.T) {
	g := &containerGuard{state: map[string]string{}}
	g.after(1, "a-18", "start", 409, false, false, false)
	if blocked, _ := g.before(1, "a-18", "submit"); !blocked {
		t.Fatal("max-active 409 must not count as an active container")
	}
	g2 := &containerGuard{state: map[string]string{}}
	g2.after(1, "a-18", "start", 409, false, false, true)
	if blocked, _ := g2.before(1, "a-18", "submit"); blocked {
		t.Fatal("already-active 409 should allow submit")
	}
}
