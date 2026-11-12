package customtools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

type httpConfig struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// BuildHTTP turns one managed HTTP tool into a Norma tool. The model supplies
// an `input` string; it is substituted into {input} in URL, headers and body.
func BuildHTTP(t domain.ManagedTool) (tool.CoreTool, error) {
	var cfg httpConfig
	if err := json.Unmarshal([]byte(t.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("invalid HTTP config: %w", err)
	}
	cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("HTTP url is required")
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = t.Display
	}
	return tool.Build(tool.Spec{
		Name:        t.Name,
		Description: desc,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Optional text substituted into {input} in the configured URL, headers and body.",
				},
			},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Input string `json:"input"`
			}
			if len(input) > 0 {
				if err := json.Unmarshal(input, &in); err != nil {
					return tool.Errorf("invalid input: " + err.Error()), nil
				}
			}
			repl := func(s string) string {
				s = strings.ReplaceAll(s, "{{input}}", in.Input)
				return strings.ReplaceAll(s, "{input}", in.Input)
			}
			url := repl(cfg.URL)
			body := repl(cfg.Body)
			var reader io.Reader
			if body != "" {
				reader = strings.NewReader(body)
			}
			req, err := http.NewRequestWithContext(ctx, cfg.Method, url, reader)
			if err != nil {
				return tool.Errorf("build request: " + err.Error()), nil
			}
			for k, v := range cfg.Headers {
				req.Header.Set(k, repl(v))
			}
			if body != "" && req.Header.Get("Content-Type") == "" {
				req.Header.Set("Content-Type", "application/json")
			}
			client := &http.Client{Timeout: 60 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return tool.Errorf("request failed: " + err.Error()), nil
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			text := fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, strings.TrimSpace(string(data)))
			if resp.StatusCode >= 400 {
				return tool.Errorf(text), nil
			}
			return tool.Text(text), nil
		},
	}), nil
}

// Load returns enabled custom HTTP tools for one scope.
func Load(ctx context.Context, st *store.Store, scope string) []tool.CoreTool {
	items, err := st.CustomToolsForScope(ctx, scope)
	if err != nil {
		return nil
	}
	out := make([]tool.CoreTool, 0, len(items))
	for _, item := range items {
		t, err := BuildHTTP(item)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}
