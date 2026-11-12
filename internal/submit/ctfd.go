package submit

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"hornedviper/internal/domain"
	"hornedviper/internal/platform"
	"hornedviper/internal/store"
)

var flagPattern = regexp.MustCompile(`(?i)[a-z0-9_]{2,}\{[^}\s]+\}`)

// TaskCTFd submits flags using the per-task CTF configuration document stored in
// task.Budget["ctf"]. If no CTF config is present it can fall back to the
// configured submit command.
type TaskCTFd struct {
	st       *store.Store
	fallback domain.Submitter
}

func NewTaskCTFd(st *store.Store, fallback domain.Submitter) *TaskCTFd {
	return &TaskCTFd{st: st, fallback: fallback}
}

func (s *TaskCTFd) Enabled() bool { return true }

func (s *TaskCTFd) Submit(ctx context.Context, task *domain.Task, finding domain.Finding) (domain.SubmitResult, error) {
	if task == nil {
		return domain.SubmitResult{}, fmt.Errorf("nil task")
	}
	url, token, challengeID, ok := taskCTFConfig(task)
	if !ok {
		if s.fallback != nil && s.fallback.Enabled() {
			return s.fallback.Submit(ctx, task, finding)
		}
		return domain.SubmitResult{}, fmt.Errorf("task #%d has no per-task CTF config", task.ID)
	}
	flag := extractFlag(finding)
	if flag == "" {
		return domain.SubmitResult{}, fmt.Errorf("no flag-like value found in finding #%d", finding.ID)
	}
	res, err := platform.NewCTFd(url, token).Submit(ctx, challengeID, flag)
	if err != nil {
		return domain.SubmitResult{}, err
	}
	return domain.SubmitResult{Accepted: res.Accepted, Flag: flag, Message: res.Message}, nil
}

func taskCTFConfig(task *domain.Task) (url, token string, challengeID int64, ok bool) {
	raw, exists := task.Budget["ctf"]
	if !exists {
		return "", "", 0, false
	}
	cfg, exists := raw.(map[string]any)
	if !exists {
		return "", "", 0, false
	}
	platformName, _ := cfg["platform"].(string)
	if platformName != "" && !strings.EqualFold(platformName, "ctfd") {
		return "", "", 0, false
	}
	url, _ = cfg["url"].(string)
	token, _ = cfg["token"].(string)
	switch v := cfg["challenge_id"].(type) {
	case float64:
		challengeID = int64(v)
	case int64:
		challengeID = v
	case string:
		challengeID, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	}
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	return url, strings.TrimSpace(token), challengeID, url != "" && token != "" && challengeID > 0
}

func extractFlag(f domain.Finding) string {
	for _, text := range []string{f.Evidence, f.Title} {
		if m := flagPattern.FindString(text); m != "" {
			return strings.TrimSpace(m)
		}
	}
	return ""
}
