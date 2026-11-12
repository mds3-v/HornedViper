package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	rep "hornedviper/internal/report"
	"hornedviper/internal/store"
)

const reportAgentPrompt = `你是 HornedViper 的报告导出 Agent。
你唯一允许的行为是“导出报告”。你只能调用 export_report 工具；不得执行命令、扫描、访问网络、修改文件、解题或生成新的任务。
用户通过新会话命令 /导出报告 触发你。用户可以用 /导出报告 <task_id> 指定任务；没有指定时导出最新任务。
调用 export_report 后，把下载链接原样返回给用户，并简要说明报告包含的内容。不要编造没有导出的文件。`

type reportExporter struct {
	st             *store.Store
	artifactDir    string
	conversationID int64
	last           *exportedReport
}

type exportedReport struct {
	TaskID   int64
	Filename string
	Path     string
	URL      string
}

func (e *reportExporter) export(ctx context.Context, taskID int64) (*exportedReport, error) {
	if taskID <= 0 {
		tasks, err := e.st.ListTasks(ctx, 1)
		if err != nil {
			return nil, err
		}
		if len(tasks) == 0 {
			return nil, fmt.Errorf("没有可导出的任务")
		}
		taskID = tasks[0].ID
	}
	doc, err := rep.BuildTask(ctx, e.st, taskID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(e.artifactDir, fmt.Sprintf("task-%d", taskID), "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, doc.Filename)
	if err := os.WriteFile(path, []byte(doc.Content), 0o644); err != nil {
		return nil, err
	}
	out := &exportedReport{
		TaskID: taskID, Filename: doc.Filename, Path: path,
		URL: "/reports?task=" + strconv.FormatInt(taskID, 10) + "&name=" + url.QueryEscape(doc.Filename),
	}
	e.last = out
	return out, nil
}

func (e *reportExporter) tool() tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "export_report",
		Description: "导出指定任务的 Markdown 报告并返回下载链接。task_id 省略时导出最新任务。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "integer", "description": "任务 ID；省略则导出最新任务"},
			},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				TaskID int64 `json:"task_id"`
			}
			if len(input) > 0 {
				if err := json.Unmarshal(input, &in); err != nil {
					return tool.Errorf("invalid input: " + err.Error()), nil
				}
			}
			out, err := e.export(ctx, in.TaskID)
			if err != nil {
				return tool.Errorf(err.Error()), nil
			}
			return tool.Text(fmt.Sprintf("报告已导出。\n任务：#%d\n文件：%s\n[下载报告](%s)", out.TaskID, out.Filename, out.URL)), nil
		},
	})
}

func (s *Server) handleReportDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	taskID, err := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	if err != nil || taskID <= 0 {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(r.URL.Query().Get("name"))
	if name == "" || name == "." || name == ".." || name != r.URL.Query().Get("name") || strings.ContainsAny(name, `/\`) {
		http.NotFound(w, r)
		return
	}
	cfg := s.cfgStore.Get()
	path := filepath.Join(cfg.ArtifactDir, fmt.Sprintf("task-%d", taskID), "reports", name)
	abs, err := filepath.Abs(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	base, _ := filepath.Abs(filepath.Join(cfg.ArtifactDir, fmt.Sprintf("task-%d", taskID), "reports"))
	if !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	http.ServeFile(w, r, abs)
}

func (s *Server) exportTaskReport(w http.ResponseWriter, r *http.Request, taskID int64) {
	cfg := s.cfgStore.Get()
	exporter := &reportExporter{st: s.st, artifactDir: cfg.ArtifactDir}
	out, err := exporter.export(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+out.Filename+`"`)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	http.ServeFile(w, r, out.Path)
}

func isReportCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/导出报告")
}

func reportTaskIDFromText(text string) int64 {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 2 {
		return 0
	}
	id, _ := strconv.ParseInt(fields[1], 10, 64)
	return id
}

func (s *Server) reportTaskIDForConversation(ctx context.Context, convID int64) int64 {
	msgs, _ := s.st.ListConversationMessages(ctx, convID, 20)
	for _, m := range msgs {
		if m.Role == "user" {
			return reportTaskIDFromText(m.Content)
		}
	}
	return 0
}

func (s *Server) isReportConversation(ctx context.Context, convID int64) bool {
	conv, err := s.st.GetConversation(ctx, convID)
	if err != nil || conv == nil {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(conv.Title), "报告导出") {
		return true
	}
	msgs, _ := s.st.ListConversationMessages(ctx, convID, 20)
	for _, m := range msgs {
		if m.Role == "user" {
			return isReportCommand(m.Content)
		}
	}
	return false
}
