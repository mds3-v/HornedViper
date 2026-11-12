package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"hornedviper/internal/domain"
	"hornedviper/internal/loghub"
)

// ---------- 对话 / 人工介入 ----------

type chatData struct {
	Title          string
	Tab            string
	Tasks          []domain.Task
	Selected       *domain.Task
	Messages       []domain.ChatMessage
	Thinking       bool
	Model          string
	TaskPagination PageInfo
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	tasks, _ := s.st.ListTasks(r.Context(), 1000)
	var selected *domain.Task
	if id, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64); id > 0 {
		selected, _ = s.st.GetTask(r.Context(), id)
	}
	if selected == nil && len(tasks) > 0 {
		selected = &tasks[0]
	}
	var messages []domain.ChatMessage
	if selected != nil {
		messages, _ = s.st.ListChatMessages(r.Context(), selected.ID, 5000)
		if len(messages) == 0 {
			if hints, _ := s.st.ListHints(r.Context(), selected.ID, 5000); len(hints) > 0 {
				for _, h := range hints {
					messages = append(messages, domain.ChatMessage{
						ID: h.ID, TaskID: h.TaskID, Role: "user", Content: h.Text,
						Status: "done", CreatedAt: h.CreatedAt,
					})
				}
			}
		}
	}
	thinking := false
	for _, m := range messages {
		if m.Status == "thinking" {
			thinking = true
		}
	}
	taskPage := newPageInfoFor(r, len(tasks), "tpage")
	model := "主 Agent"
	if _, _, m, err := s.chatProvider(); err == nil && m != "" {
		model = m
	}
	s.render(w, "chat.html", chatData{
		Title: "对话", Tab: "chat",
		Tasks: paginateSlice(tasks, taskPage), Selected: selected,
		Messages: messages, Thinking: thinking, Model: model,
		TaskPagination: taskPage,
	})
}

func (s *Server) handleChatSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/chat", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/chat/"), "/"), 10, 64)
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "解析上传文件失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	var atts []domain.Attachment
	if r.MultipartForm != nil {
		var err error
		atts, err = s.saveUploads(r.Context(), id, "chat", r.MultipartForm.File["files"], nil, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if text == "" && len(atts) == 0 {
		http.Error(w, "消息不能为空", http.StatusBadRequest)
		return
	}
	if err := s.StartChatWithAttachments(r.Context(), id, text, atts); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/chat?task=%d", id), http.StatusSeeOther)
}

func (s *Server) SendHint(ctx context.Context, taskID int64, text string) error {
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return fmt.Errorf("任务不存在")
	}
	hint, err := s.st.AddHint(ctx, taskID, text)
	if err != nil {
		return err
	}
	_, _ = s.st.AppendEvent(ctx, taskID, domain.EventHintAdded, "human", "local", "hint", hint.ID,
		map[string]any{"text": text})
	s.mu.Lock()
	run := s.runs[taskID]
	s.mu.Unlock()
	if run != nil && run.engine != nil {
		run.engine.Notify(&domain.Event{
			TaskID: taskID, Type: domain.EventHintAdded,
			ActorType: "human", ActorID: "local", SubjectType: "hint", SubjectID: hint.ID,
		})
	}
	return nil
}

// ---------- Agent 管理 ----------

type agentsData struct {
	Title      string
	Tab        string
	Agents     []domain.Agent
	Pagination PageInfo
}

type agentToolOption struct {
	Name        string
	Display     string
	Description string
	Category    string
	Checked     bool
}

type agentToolGroup struct {
	Title string
	Tools []agentToolOption
	Open  bool
}

type agentData struct {
	Title      string
	Tab        string
	Agent      domain.Agent
	IsNew      bool
	ToolGroups []agentToolGroup
	ToolCount  int
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a := s.agentFromForm(r)
		if _, err := s.st.SaveAgent(r.Context(), a); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/agents", http.StatusSeeOther)
		return
	}
	agents, _ := s.st.ListAgents(r.Context())
	page := newPageInfo(r, len(agents))
	s.render(w, "agents.html", agentsData{Title: "Agent 管理", Tab: "agents", Agents: paginateSlice(agents, page), Pagination: page})
}

func (s *Server) agentFromForm(r *http.Request) domain.Agent {
	toolsJSON := strings.TrimSpace(r.FormValue("tools_json"))
	if r.FormValue("tools_present") == "1" {
		seen := map[string]bool{}
		names := make([]string, 0, len(r.Form["tools"]))
		for _, name := range r.Form["tools"] {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		if names == nil {
			names = []string{}
		}
		if data, err := json.Marshal(names); err == nil {
			toolsJSON = string(data)
		}
	}
	return domain.Agent{
		Key: strings.TrimSpace(r.FormValue("key")), Name: strings.TrimSpace(r.FormValue("name")),
		Description:  strings.TrimSpace(r.FormValue("description")),
		SystemPrompt: strings.TrimSpace(r.FormValue("system_prompt")),
		ToolsJSON:    toolsJSON,
		Model:        strings.TrimSpace(r.FormValue("model")),
		Enabled:      r.FormValue("enabled") == "1",
	}
}

func (s *Server) agentToolGroups(ctx context.Context, toolsJSON string) ([]agentToolGroup, int) {
	selected := map[string]bool{}
	var raw []string
	if err := json.Unmarshal([]byte(toolsJSON), &raw); err == nil {
		for _, name := range raw {
			selected[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	items, _ := s.st.ListManagedTools(ctx, false)
	groups := make([]agentToolGroup, 0, 8)
	byCategory := map[string]int{}
	seen := map[string]bool{}
	total := 0
	for _, t := range items {
		if t.Deleted {
			continue
		}
		key := strings.ToLower(t.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		category := strings.TrimSpace(t.Category)
		if category == "" {
			category = "未分类"
		}
		idx, ok := byCategory[category]
		if !ok {
			idx = len(groups)
			byCategory[category] = idx
			groups = append(groups, agentToolGroup{Title: category})
		}
		checked := selected[key]
		if checked {
			total++
		}
		if checked {
			groups[idx].Open = true
		}
		groups[idx].Tools = append(groups[idx].Tools, agentToolOption{
			Name: t.Name, Display: t.Display, Description: t.Description, Category: category, Checked: checked,
		})
	}
	// Preserve legacy tool aliases (approve, merge, todo) that are not in the
	// managed catalog, so editing a built-in Agent does not silently drop them.
	other := agentToolGroup{Title: "其他"}
	for _, name := range raw {
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		other.Tools = append(other.Tools, agentToolOption{Name: name, Display: name, Description: "原有 Agent 工具，保留选中。", Category: "保留项", Checked: true})
		total++
	}
	if len(other.Tools) > 0 {
		groups = append(groups, other)
	}
	return groups, total
}

func (s *Server) handleAgentNew(w http.ResponseWriter, r *http.Request) {
	groups, total := s.agentToolGroups(r.Context(), "[]")
	s.render(w, "agent.html", agentData{Title: "新建 Agent", Tab: "agents", IsNew: true,
		Agent: domain.Agent{Enabled: true, ToolsJSON: "[]"}, ToolGroups: groups, ToolCount: total})
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/agents/"), "/")
	parts := strings.Split(rest, "/")
	key := parts[0]
	if key == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "delete" && r.Method == http.MethodPost {
		if err := s.st.DeleteAgent(r.Context(), key); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/agents", http.StatusSeeOther)
		return
	}
	agent, _ := s.st.GetAgent(r.Context(), key)
	if agent == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		updated := s.agentFromForm(r)
		updated.Key = key
		updated.Builtin = agent.Builtin
		if _, err := s.st.SaveAgent(r.Context(), updated); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/agents", http.StatusSeeOther)
		return
	}
	groups, total := s.agentToolGroups(r.Context(), agent.ToolsJSON)
	s.render(w, "agent.html", agentData{Title: agent.Name, Tab: "agents", Agent: *agent, ToolGroups: groups, ToolCount: total})
}

// ---------- 拦截规则 ----------

type rulesData struct {
	Title      string
	Tab        string
	Rules      []domain.InterceptRule
	Pagination PageInfo
}

type ruleData struct {
	Title string
	Tab   string
	Rule  domain.InterceptRule
	IsNew bool
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		rule := s.ruleFromForm(r)
		if _, err := s.st.SaveInterceptRule(r.Context(), rule); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/intercept-rules", http.StatusSeeOther)
		return
	}
	list, _ := s.st.ListInterceptRules(r.Context())
	page := newPageInfo(r, len(list))
	s.render(w, "rules.html", rulesData{Title: "拦截规则", Tab: "rules", Rules: paginateSlice(list, page), Pagination: page})
}

func (s *Server) ruleFromForm(r *http.Request) domain.InterceptRule {
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	if priority == 0 {
		priority = 100
	}
	return domain.InterceptRule{
		Name: strings.TrimSpace(r.FormValue("name")), Priority: priority,
		Target: strings.TrimSpace(r.FormValue("target")), MatchType: strings.TrimSpace(r.FormValue("match_type")),
		Pattern: strings.TrimSpace(r.FormValue("pattern")), Mode: strings.TrimSpace(r.FormValue("mode")),
		Enabled: r.FormValue("enabled") == "1",
	}
}

func (s *Server) handleRuleNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, "rule.html", ruleData{Title: "新建拦截规则", Tab: "rules", IsNew: true,
		Rule: domain.InterceptRule{Priority: 100, Target: "input", MatchType: "regex", Mode: "ask", Enabled: true}})
}

func (s *Server) handleRule(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/intercept-rules/"), "/")
	parts := strings.Split(rest, "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		switch parts[1] {
		case "delete":
			if err := s.st.DeleteInterceptRule(r.Context(), id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, "/intercept-rules", http.StatusSeeOther)
			return
		case "toggle":
			_ = s.st.SetInterceptRuleEnabled(r.Context(), id, r.FormValue("enabled") == "1")
			http.Redirect(w, r, "/intercept-rules", http.StatusSeeOther)
			return
		}
	}
	rule, _ := s.st.GetInterceptRule(r.Context(), id)
	if rule == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		updated := s.ruleFromForm(r)
		updated.ID = rule.ID
		updated.Builtin = rule.Builtin
		if _, err := s.st.SaveInterceptRule(r.Context(), updated); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/intercept-rules", http.StatusSeeOther)
		return
	}
	s.render(w, "rule.html", ruleData{Title: rule.Name, Tab: "rules", Rule: *rule})
}

// ---------- 审批记录 ----------

type approvalRecordRow struct {
	TaskTitle     string
	IntentSummary string
	Approval      domain.Approval
}

type approvalRecordsData struct {
	Title          string
	Tab            string
	Approvals      []approvalRecordRow
	Pagination     PageInfo
	TaskGroups     []trafficGroup
	TaskPagination PageInfo
	Mode           string
	GroupTitle     string
	BackURL        string
	FilterTaskID   int64
	SearchQuery    string
	SearchClearURL string
}

func (s *Server) handleApprovalRecords(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tasks, _ := s.st.ListTasks(ctx, 5000)
	titles := map[int64]string{}
	for _, t := range tasks {
		titles[t.ID] = t.Title
	}
	if deleted, _ := s.st.DeletedTitles(ctx, "task"); len(deleted) > 0 {
		for id, title := range deleted {
			if titles[id] == "" {
				titles[id] = title
			}
		}
	}
	var rows []approvalRecordRow
	for _, t := range tasks {
		list, _ := s.st.ListApprovals(ctx, t.ID, 5000)
		for _, a := range list {
			summary := ""
			if in, _ := s.st.GetIntent(ctx, a.IntentID); in != nil {
				summary = in.Summary
			}
			rows = append(rows, approvalRecordRow{TaskTitle: t.Title, IntentSummary: summary, Approval: a})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Approval.CreatedAt.After(rows[j].Approval.CreatedAt) })
	qTask, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if qTask > 0 {
		filtered := rows[:0]
		for _, row := range rows {
			if row.Approval.TaskID != qTask {
				continue
			}
			text := strings.ToLower(row.IntentSummary + " " + row.TaskTitle + " " + row.Approval.Reason + " " + row.Approval.DecidedBy + " " + string(row.Approval.Decision) + " " + string(row.Approval.Risk))
			if search != "" && !strings.Contains(text, search) {
				continue
			}
			filtered = append(filtered, row)
		}
		page := newPageInfo(r, len(filtered))
		s.render(w, "approval_records.html", approvalRecordsData{
			Title: "审批记录", Tab: "approval-records", Approvals: paginateSlice(filtered, page), Pagination: page,
			Mode: "task", GroupTitle: fmt.Sprintf("任务 #%d %s", qTask, titles[qTask]), BackURL: "/approval-records",
			FilterTaskID: qTask, SearchQuery: search, SearchClearURL: fmt.Sprintf("/approval-records?task=%d", qTask),
		})
		return
	}
	counts := map[int64]int{}
	last := map[int64]time.Time{}
	for _, row := range rows {
		counts[row.Approval.TaskID]++
		if row.Approval.CreatedAt.After(last[row.Approval.TaskID]) {
			last[row.Approval.TaskID] = row.Approval.CreatedAt
		}
	}
	groups := make([]trafficGroup, 0, len(counts))
	for id, n := range counts {
		groups = append(groups, trafficGroup{Kind: "任务", TaskID: id, Title: titles[id], Count: n, LastAt: last[id], URL: fmt.Sprintf("/approval-records?task=%d", id)})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].LastAt.After(groups[j].LastAt) })
	page := newPageInfoSize(r, len(groups), "tpage", 5)
	s.render(w, "approval_records.html", approvalRecordsData{Title: "审批记录", Tab: "approval-records", TaskGroups: paginateSlice(groups, page), TaskPagination: page})
}

// ---------- 流量 ----------

type trafficRow struct {
	TaskID         int64
	ConversationID int64
	TaskTitle      string
	WorkID         int64
	Tool           string
	ToolUseID      string
	Method         string
	Host           string
	URL            string
	Request        string
	Response       string
	StatusCode     int
	ResponseSize   int
	IsError        bool
	Time           time.Time
}

type trafficGroup struct {
	Kind           string
	TaskID         int64
	ConversationID int64
	Title          string
	Count          int
	LastAt         time.Time
	URL            string
}

type trafficData struct {
	Title                  string
	Tab                    string
	Rows                   []trafficRow
	Pagination             PageInfo
	TaskGroups             []trafficGroup
	ConversationGroups     []trafficGroup
	TaskPagination         PageInfo
	ConversationPagination PageInfo
	Mode                   string
	GroupTitle             string
	BackURL                string
	FilterTaskID           int64
	FilterConversationID   int64
	SearchQuery            string
	SearchClearURL         string
}

func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tasks, _ := s.st.ListTasks(ctx, 2000)
	taskTitles := map[int64]string{}
	for _, t := range tasks {
		taskTitles[t.ID] = t.Title
	}
	if deleted, _ := s.st.DeletedTitles(ctx, "task"); len(deleted) > 0 {
		for id, title := range deleted {
			if taskTitles[id] == "" {
				taskTitles[id] = title
			}
		}
	}
	convs, _ := s.st.ListConversations(ctx, 2000)
	convTitles := map[int64]string{}
	for _, c := range convs {
		convTitles[c.ID] = c.Title
	}
	if deleted, _ := s.st.DeletedTitles(ctx, "conversation"); len(deleted) > 0 {
		for id, title := range deleted {
			if convTitles[id] == "" {
				convTitles[id] = title
			}
		}
	}
	qTask, _ := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	qConv, _ := strconv.ParseInt(r.URL.Query().Get("conversation"), 10, 64)
	if qTask > 0 || qConv > 0 {
		search := strings.TrimSpace(r.URL.Query().Get("q"))
		if len([]rune(search)) > 200 {
			search = string([]rune(search)[:200])
		}
		total, _ := s.st.CountTrafficSearch(ctx, qTask, qConv, search)
		page := newPageInfo(r, total)
		offset := (page.Page - 1) * page.PerPage
		records, _ := s.st.ListTrafficPageSearch(ctx, qTask, qConv, search, page.PerPage, offset)
		rows := make([]trafficRow, 0, len(records))
		for _, t := range records {
			rows = append(rows, trafficRow{
				TaskID: t.TaskID, ConversationID: t.ConversationID, TaskTitle: taskTitles[t.TaskID], WorkID: t.WorkID,
				Tool: "HTTPRequest", Method: t.Method, Host: t.Host, URL: t.URL,
				Request: t.RequestRaw, Response: t.ResponseRaw,
				StatusCode: t.StatusCode, ResponseSize: t.ResponseSize,
				IsError: t.Error != "", Time: t.CreatedAt,
			})
		}
		data := trafficData{Title: "流量", Tab: "traffic", Rows: rows, Pagination: page,
			FilterTaskID: qTask, FilterConversationID: qConv, SearchQuery: search}
		if qTask > 0 {
			data.Mode = "task"
			data.GroupTitle = fmt.Sprintf("任务 #%d %s", qTask, taskTitles[qTask])
			data.SearchClearURL = fmt.Sprintf("/traffic?task=%d", qTask)
		} else {
			data.Mode = "conversation"
			data.GroupTitle = fmt.Sprintf("新会话 #%d %s", qConv, convTitles[qConv])
			data.SearchClearURL = fmt.Sprintf("/traffic?conversation=%d", qConv)
		}
		data.BackURL = "/traffic"
		s.render(w, "traffic.html", data)
		return
	}

	groups, _ := s.st.TrafficGroups(ctx)
	taskGroups := make([]trafficGroup, 0)
	conversationGroups := make([]trafficGroup, 0)
	for _, g := range groups {
		if g.TaskID > 0 {
			taskGroups = append(taskGroups, trafficGroup{Kind: "任务", TaskID: g.TaskID, Title: taskTitles[g.TaskID],
				Count: g.Count, LastAt: g.LastAt, URL: fmt.Sprintf("/traffic?task=%d", g.TaskID)})
		} else if g.ConversationID > 0 {
			conversationGroups = append(conversationGroups, trafficGroup{Kind: "新会话", ConversationID: g.ConversationID, Title: convTitles[g.ConversationID],
				Count: g.Count, LastAt: g.LastAt, URL: fmt.Sprintf("/traffic?conversation=%d", g.ConversationID)})
		}
	}
	sortGroups := func(groups []trafficGroup) {
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].LastAt.Equal(groups[j].LastAt) {
				return groups[i].Count > groups[j].Count
			}
			return groups[i].LastAt.After(groups[j].LastAt)
		})
	}
	sortGroups(taskGroups)
	sortGroups(conversationGroups)
	taskPage := newPageInfoSize(r, len(taskGroups), "tpage", 5)
	convPage := newPageInfoSize(r, len(conversationGroups), "cpage", 5)
	s.render(w, "traffic.html", trafficData{
		Title: "流量", Tab: "traffic",
		TaskGroups: paginateSlice(taskGroups, taskPage), TaskPagination: taskPage,
		ConversationGroups: paginateSlice(conversationGroups, convPage), ConversationPagination: convPage,
	})
}

var (
	urlPattern    = regexp.MustCompile(`https?://[^\s"'<>]+`)
	methodPattern = regexp.MustCompile(`(?i)(?:-X|--request)\s+([a-z]+)`)
)

// parseHTTPCall returns true only for actual HTTP(S) traffic initiated by a
// worker. Other tool calls (read/write/record_*) are intentionally excluded.
func parseHTTPCall(tool, request string) (method, host, target string, ok bool) {
	var payload struct {
		URL     string `json:"url"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal([]byte(request), &payload)

	switch tool {
	case "WebFetch":
		target = strings.TrimSpace(payload.URL)
		method = http.MethodGet
	case "Bash":
		cmd := payload.Command
		if !strings.Contains(cmd, "curl") && !strings.Contains(cmd, "wget") {
			return "", "", "", false
		}
		m := urlPattern.FindString(cmd)
		if m == "" {
			return "", "", "", false
		}
		target = strings.TrimRight(m, ".,;)\\")
		method = http.MethodGet
		if mm := methodPattern.FindStringSubmatch(cmd); len(mm) == 2 {
			method = strings.ToUpper(mm[1])
		} else if strings.Contains(cmd, " -d ") || strings.Contains(cmd, "--data") || strings.Contains(cmd, "-F ") {
			method = http.MethodPost
		}
	default:
		return "", "", "", false
	}
	if target == "" {
		return "", "", "", false
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", "", false
	}
	return method, u.Host, target, true
}

// ---------- 后端日志 ----------

type logsData struct {
	Title string
	Tab   string
	Lines []loghub.Line
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	var lines []loghub.Line
	if entries, err := s.st.ListLogs(r.Context(), 500); err == nil && len(entries) > 0 {
		for _, e := range entries {
			lines = append(lines, loghub.Line{Time: e.CreatedAt.Format("01-02 15:04:05"), Text: e.Text})
		}
	} else if s.logs != nil {
		lines = s.logs.Snapshot()
		if len(lines) > 500 {
			lines = lines[len(lines)-500:]
		}
	}
	s.render(w, "logs.html", logsData{Title: "后端日志", Tab: "logs", Lines: lines})
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		http.Error(w, "log hub unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, cancel := s.logs.Subscribe()
	defer cancel()
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(line)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
