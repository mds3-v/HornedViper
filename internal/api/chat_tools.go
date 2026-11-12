package api

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"hornedviper/internal/customtools"
	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

type chatToolSet struct {
	st             *store.Store
	taskID         int64
	conversationID int64
	artifactDir    string
	workDir        string
	ownerType      string
	ownerID        int64
	jobs           *tool.Manager
	full           bool
	permissionMode string
}

func (c *chatToolSet) tools() []tool.CoreTool {
	scope := toolScopeSession
	if !c.full {
		scope = toolScopeTaskChat
	}
	tools := []tool.CoreTool{
		c.getWorkTrace(), c.searchTask(), c.getLLMCall(), c.readAttachment(),
	}
	if !c.full {
		tools = append(tools, customtools.Load(context.Background(), c.st, scope)...)
		return c.filterTools(scope, tools)
	}
	readTools := []tool.CoreTool{
		tool.NewRead(), tool.NewGlob(), tool.NewGrep(), tool.NewLS(),
		c.readImage(), c.webSearch(), c.webFetch(),
	}
	tools = append(tools, readTools...)
	if c.permissionMode == "read-only" {
		// Custom HTTP tools can perform writes, so they stay out of view-only
		// sessions. Built-in read tools remain available.
		return c.filterTools(scope, tools)
	}
	if c.permissionMode == "workspace-write" {
		tools = append(tools,
			tool.NewBash(), c.pythonTool(), c.workspaceWrite(tool.NewWrite()),
			c.workspaceWrite(tool.NewEdit()), c.workspaceWrite(tool.NewMultiEdit()),
			c.todoWrite(),
		)
	} else {
		tools = append(tools,
			tool.NewBash(), c.pythonTool(), tool.NewWrite(), tool.NewEdit(), tool.NewMultiEdit(),
			c.todoWrite(),
		)
	}
	tools = append(tools,
		c.aliasJob("job_list", tool.NewTaskList(), "List background jobs."),
		c.aliasJob("job_output", tool.NewTaskOutput(), "Read output from a background job."),
		c.aliasJob("job_kill", tool.NewTaskStop(), "Stop a background job."),
	)
	tools = append(tools, customtools.Load(context.Background(), c.st, scope)...)
	return c.filterTools(scope, tools)
}

func (c *chatToolSet) filterTools(scope string, tools []tool.CoreTool) []tool.CoreTool {
	if c.st == nil {
		return tools
	}
	settings, err := c.st.ToolSettings(context.Background())
	if err != nil {
		return tools
	}
	out := make([]tool.CoreTool, 0, len(tools))
	for _, t := range tools {
		if enabled, ok := settings[scope+"\x00"+t.Name()]; ok && !enabled {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (c *chatToolSet) schemas() []llm.ToolSchema {
	return toolSchemas(c.tools())
}

func toolSchemas(list []tool.CoreTool) []llm.ToolSchema {
	out := make([]llm.ToolSchema, 0, len(list))
	for _, t := range list {
		out = append(out, llm.ToolSchema{Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema()})
	}
	return out
}

func (c *chatToolSet) getWorkTrace() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "get_work_trace",
		Description: "Read raw tool calls and tool results for one Work. Use this when the user asks what a Work did, which command was run, or where evidence came from.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"work_id": map[string]any{"type": "integer"},
				"limit":   map[string]any{"type": "integer", "description": "default 40"},
				"offset":  map[string]any{"type": "integer", "description": "default 0"},
			},
			"required": []any{"work_id"},
		},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				WorkID int64 `json:"work_id"`
				Limit  int   `json:"limit"`
				Offset int   `json:"offset"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			if in.Limit <= 0 || in.Limit > 100 {
				in.Limit = 40
			}
			works, _ := c.st.ListWorks(ctx, c.taskID)
			var header string
			for _, w := range works {
				if w.ID == in.WorkID {
					header = fmt.Sprintf("Work #%d intent #%d worker=%s state=%s stop=%s\n", w.ID, w.IntentID, w.WorkerID, w.State, w.StopReason)
					break
				}
			}
			if header == "" {
				return tool.Errorf(fmt.Sprintf("work #%d not found", in.WorkID)), nil
			}
			acts, _ := c.st.ListActivities(ctx, c.taskID, 5000)
			var lines []string
			for _, a := range acts {
				if a.WorkID != in.WorkID || (a.Kind != "tool_use" && a.Kind != "tool_result") {
					continue
				}
				lines = append(lines, fmt.Sprintf("- [%s] %s %s", a.Kind, a.Tool, truncateText(a.Detail, 1200)))
			}
			if in.Offset >= len(lines) {
				return tool.Text(header + "（没有更多记录）"), nil
			}
			end := in.Offset + in.Limit
			if end > len(lines) {
				end = len(lines)
			}
			return tool.Text(header + strings.Join(lines[in.Offset:end], "\n") +
				fmt.Sprintf("\n（返回 %d 条，共 %d 条）", end-in.Offset, len(lines))), nil
		},
	})
}

func (c *chatToolSet) searchTask() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "search_task",
		Description: "Search this task's tool calls, tool results, findings and LLM output for a keyword. Returns matching records with Work ids.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer", "description": "default 20"},
			},
			"required": []any{"query"},
		},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			if in.Limit <= 0 || in.Limit > 50 {
				in.Limit = 20
			}
			q := strings.ToLower(strings.TrimSpace(in.Query))
			if q == "" {
				return tool.Errorf("query is required"), nil
			}
			var lines []string
			acts, _ := c.st.ListActivities(ctx, c.taskID, 5000)
			for _, a := range acts {
				hay := strings.ToLower(a.Summary + " " + a.Detail + " " + a.Tool)
				if strings.Contains(hay, q) {
					lines = append(lines, fmt.Sprintf("- [%s] work #%d %s: %s", a.Kind, a.WorkID, a.Tool, truncateText(a.Summary+" "+a.Detail, 500)))
				}
			}
			findings, _ := c.st.ListFindings(ctx, c.taskID, 200)
			for _, f := range findings {
				hay := strings.ToLower(f.Title + " " + f.Evidence)
				if strings.Contains(hay, q) {
					lines = append(lines, fmt.Sprintf("- [finding] #%d %s: %s", f.ID, f.Title, truncateText(f.Evidence, 500)))
				}
			}
			if len(lines) == 0 {
				return tool.Text("没有匹配记录"), nil
			}
			if len(lines) > in.Limit {
				lines = lines[:in.Limit]
			}
			return tool.Text(strings.Join(lines, "\n")), nil
		},
	})
}

func (c *chatToolSet) getLLMCall() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "get_llm_call",
		Description: "Read one raw LLM call by id, including request, response, thinking and tool calls.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer"},
			},
			"required": []any{"id"},
		},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			calls, _ := c.st.ListLLMCalls(ctx, c.taskID, 5000)
			for _, call := range calls {
				if call.ID != in.ID {
					continue
				}
				return tool.Text(fmt.Sprintf("LLM #%d role=%s model=%s latency=%dms\nrequest:\n%s\nresponse:\n%s\nthinking:\n%s\ntool_calls:\n%s",
					call.ID, call.Role, call.Model, call.LatencyMS,
					truncateText(call.RequestJSON, 4000), truncateText(call.ResponseJSON, 4000),
					truncateText(call.Thinking, 4000), truncateText(call.ToolCallsJSON, 4000))), nil
			}
			return tool.Errorf(fmt.Sprintf("llm call #%d not found", in.ID)), nil
		},
	})
}

func (c *chatToolSet) workspaceWrite(inner tool.CoreTool) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        inner.Name(),
		Description: inner.Description(),
		Schema:      inner.InputSchema(),
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
			var m map[string]any
			_ = json.Unmarshal(input, &m)
			p := ""
			if v, ok := m["file_path"].(string); ok {
				p = v
			} else if v, ok := m["path"].(string); ok {
				p = v
			}
			if p != "" {
				abs := p
				if !filepath.IsAbs(abs) {
					abs = filepath.Join(c.workDir, abs)
				}
				abs, _ = filepath.Abs(abs)
				base, _ := filepath.Abs(c.workDir)
				if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
					return tool.Errorf("workspace-write: path outside workspace: " + p), nil
				}
			}
			return inner.Call(ctx, input, tc)
		},
	})
}

func (c *chatToolSet) aliasJob(name string, inner tool.CoreTool, desc string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        name,
		Description: desc,
		Schema:      inner.InputSchema(),
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
			return inner.Call(ctx, input, tc)
		},
	})
}

func (s *Server) chatJobManager(key string) *tool.Manager {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if m := s.jobs[key]; m != nil {
		return m
	}
	m, err := tool.NewManager(key)
	if err != nil {
		return nil
	}
	s.jobs[key] = m
	return m
}

func (c *chatToolSet) pythonTool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "Python",
		Description: "Run Python 3 code in the working directory. Use it for decoding, brute force, crypto, and file analysis.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":              map[string]any{"type": "string"},
				"timeout_ms":        map[string]any{"type": "integer", "description": "default 120000, max 600000"},
				"run_in_background": map[string]any{"type": "boolean", "description": "Run long Python work as a managed background job."},
			},
			"required": []any{"code"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Code            string `json:"code"`
				TimeoutMS       int    `json:"timeout_ms"`
				RunInBackground bool   `json:"run_in_background"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			if in.TimeoutMS <= 0 || in.TimeoutMS > 600000 {
				in.TimeoutMS = 120000
			}
			if in.RunInBackground {
				if c.jobs == nil {
					return tool.Errorf("background jobs are unavailable"), nil
				}
				dir := "."
				if tc != nil && tc.WorkingDir != "" {
					dir = tc.WorkingDir
				}
				script := filepath.Join(dir, fmt.Sprintf(".python-job-%d.py", time.Now().UnixNano()))
				if err := os.WriteFile(script, []byte(in.Code), 0o600); err != nil {
					return tool.Errorf("background python: " + err.Error()), nil
				}
				job, err := c.jobs.Spawn(tool.SpawnSpec{
					Command: "python3 " + shellQuote(script), Description: "python background",
					Kind: tool.KindBash, WorkingDir: dir,
				})
				if err != nil {
					return tool.Errorf("background python: " + err.Error()), nil
				}
				return tool.Text(fmt.Sprintf("background job %s started: python3 %s\nUse job_output / job_kill to manage it.", job.ID, script)), nil
			}
			cctx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutMS)*time.Millisecond)
			defer cancel()
			cmd := exec.CommandContext(cctx, "python3", "-c", in.Code)
			if tc != nil && tc.WorkingDir != "" {
				cmd.Dir = tc.WorkingDir
			}
			out, err := cmd.CombinedOutput()
			text := string(out)
			if len(text) > 64<<10 {
				text = text[:64<<10] + "\n…(truncated)"
			}
			if err != nil {
				return tool.Errorf(fmt.Sprintf("%s\n[python error: %v]", text, err)), nil
			}
			return tool.Text(text), nil
		},
	})
}

func (c *chatToolSet) readImage() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "read_image",
		Description: "Inspect a local image: dimensions, format, size, and OCR text. Use it for screenshots, CAPTCHA, stego clues, and image-based challenges.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []any{"path"},
		},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			path := in.Path
			if !filepath.IsAbs(path) && tc != nil && tc.WorkingDir != "" {
				path = filepath.Join(tc.WorkingDir, path)
			}
			info, err := os.Stat(path)
			if err != nil {
				return tool.Errorf("read image: " + err.Error()), nil
			}
			if info.IsDir() {
				return tool.Errorf("not a file: " + path), nil
			}
			f, err := os.Open(path)
			if err != nil {
				return tool.Errorf("read image: " + err.Error()), nil
			}
			cfg, format, err := image.DecodeConfig(f)
			f.Close()
			var b strings.Builder
			fmt.Fprintf(&b, "path: %s\nsize: %d bytes\n", path, info.Size())
			if err == nil {
				fmt.Fprintf(&b, "format: %s\ndimensions: %dx%d\n", format, cfg.Width, cfg.Height)
			} else {
				b.WriteString("format: unknown\n")
			}
			ocrCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ocrCtx, "tesseract", path, "stdout", "-l", "eng+chi_sim")
			out, ocrErr := cmd.CombinedOutput()
			if len(out) > 0 {
				text := strings.TrimSpace(string(out))
				if len(text) > 32<<10 {
					text = text[:32<<10] + "\n…(truncated)"
				}
				b.WriteString("\nOCR:\n" + text + "\n")
			}
			if ocrErr != nil {
				fmt.Fprintf(&b, "\nOCR unavailable: %v\n", ocrErr)
			}
			return tool.Text(b.String()), nil
		},
	})
}

func (c *chatToolSet) todoWrite() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "todo_write",
		Description: "Replace the task/chat todo list. Each item has content and status: pending, in_progress, or completed.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string"},
							"status":  map[string]any{"type": "string", "enum": []any{"pending", "in_progress", "completed"}},
						},
						"required": []any{"content", "status"},
					},
				},
			},
			"required": []any{"todos"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Todos []domain.TodoItem `json:"todos"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			ownerType, ownerID := c.todoOwner()
			if err := c.st.SetChatTodos(ctx, ownerType, ownerID, in.Todos); err != nil {
				return tool.Errorf("save todos: " + err.Error()), nil
			}
			var b strings.Builder
			for i, t := range in.Todos {
				fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, t.Status, t.Content)
			}
			return tool.Text(strings.TrimSpace(b.String())), nil
		},
	})
}

func (c *chatToolSet) todoOwner() (string, int64) {
	if c.ownerType != "" {
		return c.ownerType, c.ownerID
	}
	if c.taskID > 0 {
		return "task", c.taskID
	}
	return "conversation", c.conversationID
}

func (c *chatToolSet) readAttachment() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "read_attachment",
		Description: "Read a text file uploaded to this task or chat by filename. Use it to inspect user-provided files.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []any{"name"},
		},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid input: " + err.Error()), nil
			}
			want := strings.TrimSpace(in.Name)
			if want == "" {
				return tool.Errorf("name is required"), nil
			}
			paths := map[string]string{}
			if c.taskID > 0 {
				if task, _ := c.st.GetTask(ctx, c.taskID); task != nil {
					if atts, ok := task.Budget["attachments"].([]any); ok {
						for _, raw := range atts {
							if m, ok := raw.(map[string]any); ok {
								name, _ := m["name"].(string)
								path, _ := m["path"].(string)
								if name != "" && path != "" {
									paths[name] = path
								}
							}
						}
					}
				}
			}
			msgs, _ := c.st.ListChatMessages(ctx, c.taskID, 1000)
			if c.conversationID > 0 {
				msgs, _ = c.st.ListConversationMessages(ctx, c.conversationID, 1000)
			}
			for _, msg := range msgs {
				for _, a := range msg.Attachments {
					paths[a.Name] = a.Path
				}
			}
			var path string
			for name, p := range paths {
				if strings.EqualFold(name, want) || strings.EqualFold(filepath.Base(name), filepath.Base(want)) {
					path = p
					break
				}
			}
			if path == "" {
				return tool.Errorf(fmt.Sprintf("attachment %q not found", want)), nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return tool.Errorf("invalid attachment path"), nil
			}
			base, _ := filepath.Abs(c.artifactDir)
			if base != "" && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
				return tool.Errorf("attachment is outside the artifact directory"), nil
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return tool.Errorf("read attachment: " + err.Error()), nil
			}
			ct := http.DetectContentType(data)
			if !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
				return tool.Text(fmt.Sprintf("attachment: %s\npath: %s\nsize: %d bytes\ncontent-type: %s\n(binary file; content not shown)", want, abs, len(data), ct)), nil
			}
			if len(data) > 64<<10 {
				data = data[:64<<10]
			}
			return tool.Text(fmt.Sprintf("attachment: %s\npath: %s\nsize: %d bytes\n\n%s", want, abs, len(data), string(data))), nil
		},
	})
}

var _ = domain.Activity{}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func toolTraceLine(name string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	detail := ""
	for _, key := range []string{"command", "code", "url", "path", "file_path", "query"} {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			detail = strings.TrimSpace(v)
			break
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 160 {
		detail = detail[:160] + "…"
	}
	if detail == "" {
		return "运行命令 " + name
	}
	return "运行命令 " + name + " · " + detail
}
