package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Challenge struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Value       int    `json:"value"`
	Solves      int    `json:"solves"`
	Solved      bool   `json:"solved_by_me"`
}

type SubmitResult struct {
	Accepted bool
	Status   string
	Message  string
	Flag     string
}

// CTFd is a minimal CTFd API client (challenge listing + flag submission).
type CTFd struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewCTFd(baseURL, token string) *CTFd {
	return &CTFd{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *CTFd) Enabled() bool { return c != nil && c.BaseURL != "" && c.Token != "" }

func (c *CTFd) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ctfd %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *CTFd) List(ctx context.Context) ([]Challenge, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/v1/challenges", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Success bool        `json:"success"`
		Data    []Challenge `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("ctfd challenges: %w", err)
	}
	if !payload.Success && payload.Data == nil {
		return nil, fmt.Errorf("ctfd challenges: unsuccessful response")
	}
	return payload.Data, nil
}

func (c *CTFd) Submit(ctx context.Context, challengeID int64, flag string) (SubmitResult, error) {
	data, err := c.do(ctx, http.MethodPost, "/api/v1/challenges/attempt", map[string]any{
		"challenge_id": challengeID,
		"submission":   flag,
	})
	if err != nil {
		return SubmitResult{}, err
	}
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return SubmitResult{}, fmt.Errorf("ctfd submit: %w", err)
	}
	return SubmitResult{
		Accepted: strings.EqualFold(payload.Data.Status, "correct"),
		Status:   payload.Data.Status,
		Message:  payload.Data.Message,
		Flag:     flag,
	}, nil
}
