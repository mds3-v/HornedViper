package api

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"hornedviper/internal/domain"
	"hornedviper/internal/platform"
)

var (
	benchmarkBasePattern  = regexp.MustCompile(`(?im)^\s*BENCHMARK_BASE_URL\s*[:=]\s*(\S+)`)
	benchmarkTokenPattern = regexp.MustCompile(`(?im)^\s*BENCHMARK_TOKEN\s*[:=]\s*(\S+)`)
)

// closeChallenge directly closes a completed benchmark challenge so its
// container slot is released without waiting for another Planner or Worker
// turn. Credentials come from task.budget.ctf or the uploaded API document.
func (s *Server) closeChallenge(ctx context.Context, task *domain.Task, challenge string) error {
	base, token, err := s.benchmarkCredentials(task)
	if err != nil {
		return err
	}
	return platform.NewBenchmark(base, token).Close(ctx, challenge)
}

func (s *Server) benchmarkCredentials(task *domain.Task) (string, string, error) {
	if task == nil {
		return "", "", fmt.Errorf("benchmark: task is nil")
	}
	base, token := "", ""
	if ctf, ok := task.Budget["ctf"].(map[string]any); ok {
		if p, _ := ctf["platform"].(string); strings.EqualFold(p, "ctfd") {
			return "", "", fmt.Errorf("close challenge: CTFd has no benchmark close endpoint")
		}
		base, _ = ctf["url"].(string)
		token, _ = ctf["token"].(string)
	}
	if base == "" || token == "" {
		if atts, ok := task.Budget["attachments"].([]any); ok {
			for _, raw := range atts {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				path, _ := m["path"].(string)
				if path == "" {
					continue
				}
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				text := string(data)
				if base == "" {
					if mm := benchmarkBasePattern.FindStringSubmatch(text); len(mm) > 1 {
						base = strings.TrimRight(strings.TrimSpace(mm[1]), "/")
					}
				}
				if token == "" {
					if mm := benchmarkTokenPattern.FindStringSubmatch(text); len(mm) > 1 {
						token = strings.TrimSpace(mm[1])
					}
				}
			}
		}
	}
	if base == "" || token == "" {
		return "", "", fmt.Errorf("benchmark base URL/token not found")
	}
	return base, token, nil
}

func (s *Server) reconcileCompletedChallenges(ctx context.Context, task *domain.Task) ([]string, error) {
	base, token, err := s.benchmarkCredentials(task)
	if err != nil {
		return nil, err
	}
	challenges, err := platform.NewBenchmark(base, token).List(ctx)
	if err != nil {
		return nil, err
	}
	var completed []string
	for _, ch := range challenges {
		if !ch.IsCompleted && (ch.FlagCount <= 0 || ch.CorrectFlagCount < ch.FlagCount) {
			continue
		}
		if ch.UniqueCode == "" {
			continue
		}
		if err := s.st.MarkChallengeCompleted(ctx, task.ID, ch.UniqueCode); err != nil {
			continue
		}
		completed = append(completed, ch.UniqueCode)
	}
	return completed, nil
}
