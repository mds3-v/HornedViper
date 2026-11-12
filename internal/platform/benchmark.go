package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Benchmark is the TSecBench-style challenge API client. It currently only
// needs the close operation; submission stays in the task worker.
type Benchmark struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewBenchmark(baseURL, token string) *Benchmark {
	return &Benchmark{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (b *Benchmark) Close(ctx context.Context, challenge string) error {
	if b == nil || b.BaseURL == "" || b.Token == "" {
		return fmt.Errorf("benchmark: missing base URL or token")
	}
	challenge = strings.TrimSpace(strings.TrimPrefix(challenge, "challenge:"))
	target := b.BaseURL + "/openapi/v1/challenges/close?unique_code=" + url.QueryEscape(challenge)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("BENCHMARK_TOKEN", b.Token)
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// The platform may already have closed/finished the challenge. Treat the
	// explicit already-closed/finished responses as idempotent success.
	text := strings.ToLower(string(body))
	if resp.StatusCode == http.StatusConflict && (strings.Contains(text, "already") || strings.Contains(text, "finished") || strings.Contains(text, "closed")) {
		return nil
	}
	return fmt.Errorf("benchmark close %s: HTTP %d: %s", challenge, resp.StatusCode, strings.TrimSpace(string(body)))
}

// Challenge is the subset of the benchmark challenge list used for completion
// reconciliation.
type BenchmarkChallenge struct {
	UniqueCode       string `json:"unique_code"`
	TotalScore       int    `json:"total_score"`
	FlagCount        int    `json:"flag_count"`
	CorrectFlagCount int    `json:"correct_flag_count"`
	IsCompleted      bool   `json:"is_completed"`
	ContainerStatus  string `json:"container_status"`
}

func (b *Benchmark) List(ctx context.Context) ([]BenchmarkChallenge, error) {
	if b == nil || b.BaseURL == "" || b.Token == "" {
		return nil, fmt.Errorf("benchmark: missing base URL or token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.BaseURL+"/openapi/v1/challenges", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("BENCHMARK_TOKEN", b.Token)
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("benchmark list: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out []BenchmarkChallenge
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("benchmark list JSON: %w", err)
	}
	return out, nil
}
