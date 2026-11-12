package runtime

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// containerGuard makes challenge start/close idempotent within a task.
//
// The benchmark API allows only three active challenge containers. Without a
// local owner, workers repeatedly start already-active challenges and close
// partially solved instances before their flags are credited.
type containerGuard struct {
	mu    sync.Mutex
	state map[string]string // taskID|challenge -> active|partial|completed|closed
}

var uniqueCodePattern = regexp.MustCompile(`(?i)"unique_code"\s*:\s*"([^"]+)"`)

func newContainerGuard() *containerGuard {
	return &containerGuard{state: map[string]string{}}
}

func containerKey(taskID int64, challenge string) string {
	return strings.TrimSpace(strings.ToLower(challenge))
}

func (g *containerGuard) before(taskID int64, challenge, action string) (bool, string) {
	if g == nil || challenge == "" {
		return false, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.state[keyFor(taskID, challenge)]
	switch action {
	case "start":
		switch state {
		case "active", "partial", "completed":
			return true, "challenge " + challenge + " is already active in this task; reuse the existing container"
		}
	case "close":
		if state == "active" || state == "partial" {
			return true, "refusing to close challenge " + challenge + " before its flags are accepted"
		}
	case "submit":
		if state == "" {
			return true, "refusing submit for challenge " + challenge + " with no active container; start it first"
		}
		if state == "closed" {
			return true, "refusing submit for closed challenge " + challenge + "; reopen it first"
		}
	}
	return false, ""
}

func (g *containerGuard) after(taskID int64, challenge, action string, status int, accepted, completed bool, judgedActive bool) {
	if g == nil || challenge == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := keyFor(taskID, challenge)
	switch action {
	case "start":
		// Success is protocol-level. Ambiguous responses are classified by the
		// LLM judge, not by hardcoded message matching.
		if status == 200 || judgedActive {
			g.state[key] = "active"
		}
	case "submit":
		if completed {
			g.state[key] = "completed"
		} else if accepted {
			g.state[key] = "partial"
		}
	case "close":
		if (status == 200 || status == 409) && g.state[key] != "completed" {
			g.state[key] = "closed"
		}
	}
}

func keyFor(taskID int64, challenge string) string {
	return strings.TrimSpace(strings.ToLower(challenge)) + "|" + itoa(taskID)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func ctfContainerAction(method, path string, body []byte) (string, string) {
	lower := strings.ToLower(path)
	action := ""
	switch {
	case strings.Contains(lower, "/challenges/start"):
		action = "start"
	case strings.Contains(lower, "/challenges/close"):
		action = "close"
	case strings.Contains(lower, "/challenges/submit"):
		action = "submit"
	default:
		return "", ""
	}
	code := ""
	// Query is handled by caller from URL; body JSON often carries unique_code.
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		if v, ok := payload["unique_code"].(string); ok {
			code = strings.TrimSpace(v)
		}
	}
	if code == "" {
		if m := uniqueCodePattern.FindSubmatch(body); len(m) > 1 {
			code = strings.TrimSpace(string(m[1]))
		}
	}
	return code, action
}
