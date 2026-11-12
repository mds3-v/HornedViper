package runtime

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"hornedviper/internal/domain"
)

const maxHTTPBody = 2 << 20

// httpRequestTool performs an HTTP(S) request and records the raw request and
// response packets. It is the worker's preferred way to talk to a target.
func httpRequestTool(taskID, workID, intentID int64, sink func(domain.Traffic),
	flagAlreadySubmitted func(context.Context, int64, string) (bool, error),
	flagSubmissionRecorded func(context.Context, int64, string, bool, string) error,
	containerBefore func(int64, string, string) (bool, string),
	containerAfter func(int64, string, string, int, bool, bool, bool),
	allowNoContainer bool,
	challengeCompleted func(context.Context, int64, string) (bool, error),
	challengeCompletedRecord func(context.Context, int64, string) error,
	containerJudge func(context.Context, int64, string, string, int, string) (bool, string)) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "HTTPRequest",
		Description: "Send an HTTP(S) request and return the raw response. Use this for CTF/pentest web requests; the raw request and response are recorded in Traffic. Supports method, headers and body.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"method": map[string]any{"type": "string", "description": "HTTP method, default GET"},
				"url":    map[string]any{"type": "string", "description": "Absolute http(s) URL"},
				"headers": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"body":     map[string]any{"type": "string", "description": "Request body"},
				"insecure": map[string]any{"type": "boolean", "description": "Skip TLS certificate verification"},
			},
			"required": []any{"url"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Method   string            `json:"method"`
				URL      string            `json:"url"`
				Headers  map[string]string `json:"headers"`
				Body     string            `json:"body"`
				Insecure bool              `json:"insecure"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid HTTPRequest input: " + err.Error()), nil
			}
			target := strings.TrimSpace(in.URL)
			u, err := url.Parse(target)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return tool.Errorf("HTTPRequest requires an absolute http(s) URL"), nil
			}
			method := strings.ToUpper(strings.TrimSpace(in.Method))
			if method == "" {
				method = http.MethodGet
			}
			containerCode, containerAction := ctfContainerAction(method, u.Path, []byte(in.Body))
			if containerCode == "" {
				containerCode = strings.TrimSpace(u.Query().Get("unique_code"))
			}
			submitFlag, isSubmit := ctfSubmitFlag(method, u, in.Body)
			if challengeCompleted != nil && containerCode != "" && (containerAction == "start" || containerAction == "submit") {
				if done, _ := challengeCompleted(ctx, taskID, containerCode); done {
					return tool.Errorf("challenge " + containerCode + " is already completed; no further start or flag submission"), nil
				}
			}
			if isSubmit && containerCode == "" {
				return tool.Errorf("refusing flag submission without a challenge target (unique_code)"), nil
			}
			if containerCode != "" && containerBefore != nil && !(isSubmit && allowNoContainer) {
				if blocked, msg := containerBefore(taskID, containerCode, containerAction); blocked {
					return tool.Errorf(msg), nil
				}
			}
			if isSubmit && submitFlag != "" && flagAlreadySubmitted != nil {
				already, checkErr := flagAlreadySubmitted(ctx, taskID, submitFlag)
				if checkErr == nil && already {
					return tool.Errorf("duplicate flag submission blocked: " + submitFlag), nil
				}
			}
			newReq := func() (*http.Request, error) {
				req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(in.Body))
				if err != nil {
					return nil, err
				}
				for k, v := range in.Headers {
					req.Header.Set(k, v)
				}
				if req.Header.Get("User-Agent") == "" {
					req.Header.Set("User-Agent", "HornedViper/1.0")
				}
				return req, nil
			}
			sendReq, err := newReq()
			if err != nil {
				return tool.Errorf("build request: " + err.Error()), nil
			}
			dumpReq, err := newReq()
			if err != nil {
				return tool.Errorf("build request: " + err.Error()), nil
			}
			rawReq, _ := httputil.DumpRequestOut(dumpReq, true)

			tr := &http.Transport{}
			if in.Insecure {
				tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			}
			client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
			start := time.Now()
			resp, err := client.Do(sendReq)
			elapsed := time.Since(start).Milliseconds()
			if err != nil {
				rec := domain.Traffic{
					TaskID: taskID, WorkID: workID, IntentID: intentID,
					Method: method, Host: u.Host, URL: target,
					RequestRaw: string(rawReq), RequestSize: len(rawReq),
					DurationMS: elapsed, Error: err.Error(), CreatedAt: time.Now(),
				}
				if sink != nil {
					sink(rec)
				}
				return tool.Errorf("HTTPRequest failed: " + err.Error()), nil
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))

			var rawResp bytes.Buffer
			fmt.Fprintf(&rawResp, "HTTP/%d.%d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.Status)
			_ = resp.Header.Write(&rawResp)
			rawResp.WriteString("\r\n")
			rawResp.Write(body)

			rec := domain.Traffic{
				TaskID: taskID, WorkID: workID, IntentID: intentID,
				Method: method, Host: u.Host, URL: target, StatusCode: resp.StatusCode,
				ContentType: resp.Header.Get("Content-Type"),
				RequestRaw:  string(rawReq), ResponseRaw: rawResp.String(),
				RequestSize: len(rawReq), ResponseSize: rawResp.Len(),
				DurationMS: elapsed, CreatedAt: time.Now(),
			}
			if sink != nil {
				sink(rec)
			}

			if isSubmit && submitFlag != "" && flagSubmissionRecorded != nil {
				var result map[string]any
				_ = json.Unmarshal(body, &result)
				accepted, _ := result["correct"].(bool)
				if a, ok := result["accepted"].(bool); ok {
					accepted = a
				}
				if accepted {
					if err := flagSubmissionRecorded(ctx, taskID, submitFlag, true, string(body)); err != nil {
						log.Printf("[runtime] record accepted flag %s: %v", submitFlag, err)
					}
				}
			}

			if containerCode != "" {
				accepted := false
				completed := false
				var result map[string]any
				_ = json.Unmarshal(body, &result)
				accepted, _ = result["correct"].(bool)
				if a, ok := result["accepted"].(bool); ok {
					accepted = a
				}
				correct, _ := result["correct_flag_count"].(float64)
				total, _ := result["total_flag_count"].(float64)
				completed = total > 0 && correct >= total
				judgedActive := false
				if containerAction == "start" && resp.StatusCode != http.StatusOK && containerJudge != nil {
					judgedActive, _ = containerJudge(ctx, taskID, containerCode, containerAction, resp.StatusCode, string(body))
				}
				if containerAfter != nil {
					containerAfter(taskID, containerCode, containerAction, resp.StatusCode, accepted, completed, judgedActive)
				}
				if completed && challengeCompletedRecord != nil {
					if err := challengeCompletedRecord(ctx, taskID, containerCode); err != nil {
						log.Printf("[runtime] record completion %s: %v", containerCode, err)
					}
				}
			}

			out := rawResp.String()
			if len(out) > 64<<10 {
				out = out[:64<<10] + "\n... [raw response truncated for model context; full packet stored in Traffic]"
			}
			return tool.Text(out), nil
		},
	})
}

var httpFlagPattern = regexp.MustCompile(`(?i)flag\{[^}]{1,1000}\}`)

func ctfSubmitFlag(method string, u *url.URL, body string) (string, bool) {
	if method != http.MethodPost || !strings.Contains(strings.ToLower(u.Path), "submit") {
		return "", false
	}
	text := body + " " + u.RawQuery
	m := httpFlagPattern.FindString(text)
	return strings.TrimSpace(m), true
}
