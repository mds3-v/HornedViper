package llmtest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"

	"hornedviper/internal/config"
)

// Result is a successful connectivity test.
type Result struct {
	Text    string
	Model   string
	Latency time.Duration
}

// Provider performs one minimal completion against a provider config.
// It validates endpoint, API key, model and wire format end-to-end.
func Provider(ctx context.Context, p config.Provider) (Result, error) {
	if strings.TrimSpace(p.APIKey) == "" {
		return Result{}, fmt.Errorf("no API key configured for %s", p.Name)
	}
	var format llm.Format
	switch strings.ToLower(strings.TrimSpace(p.Format)) {
	case "", "openai":
		format = llm.FormatOpenAI
	case "openai-responses", "responses":
		format = llm.FormatOpenAIResponses
	case "anthropic":
		format = llm.FormatAnthropic
	default:
		return Result{}, fmt.Errorf("unsupported format %q", p.Format)
	}
	prov, err := llm.NewProvider(llm.Config{
		Format: format, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model,
	})
	if err != nil {
		return Result{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	msg, _, _, err := prov.Complete(runCtx, llm.CompletionRequest{
		System:    []string{"You are a connectivity check. Reply briefly."},
		Messages:  []llm.Message{llm.UserText("Reply with exactly: OK")},
		MaxTokens: 32,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Text: strings.TrimSpace(msg.Text()), Model: p.Model, Latency: time.Since(start)}, nil
}
