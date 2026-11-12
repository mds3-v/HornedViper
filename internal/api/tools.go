package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"hornedviper/internal/domain"
)

type toolRow struct {
	Tool domain.ManagedTool
}

type toolGroup struct {
	Scope       string
	Title       string
	Description string
	AgentKey    string
	Rows        []toolRow
	Active      int
	Total       int
}

type toolsData struct {
	Title        string
	Tab          string
	Groups       []toolGroup
	AgentGroups  []toolGroup
	Archived     []domain.ManagedTool
	Categories   []domain.ToolCategory
	Total        int
	Active       int
	ShowArchived bool
	Error        string
}

var toolScopeOrder = []struct {
	Scope       string
	Title       string
	Description string
}{
	{toolScopeSession, "会话 Agent", "新会话（/new）可调用的工具。修改后对新消息生效。"},
	{toolScopeTaskChat, "任务主 Agent", "任务对话的只读查询工具。主 Agent 不执行命令。"},
	{toolScopeWorker, "Worker", "任务执行者用于分析、利用和产出结果的工具。"},
	{toolScopePlanner, "Planner", "Planner 的编排工具；核心工具不可关闭。"},
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	all, err := s.st.ListManagedTools(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	categories, _ := s.st.ListToolCategories(r.Context())
	includeArchived := r.URL.Query().Get("archived") == "1"
	groups := make([]toolGroup, 0, len(toolScopeOrder))
	agentGroups := make([]toolGroup, 0)
	total, active := 0, 0
	for _, meta := range toolScopeOrder {
		g := toolGroup{Scope: meta.Scope, Title: meta.Title, Description: meta.Description}
		for _, t := range all {
			if t.Scope != meta.Scope || t.Deleted {
				continue
			}
			g.Rows = append(g.Rows, toolRow{Tool: t})
			g.Total++
			if t.Enabled {
				g.Active++
			}
			total++
			if t.Enabled {
				active++
			}
		}
		groups = append(groups, g)
	}
	// Agent categories are derived from the saved Agent tool lists. This keeps
	// the management page in sync when a new Agent is created or edited.
	agents, _ := s.st.ListAgents(r.Context())
	for _, agent := range agents {
		if agent.Builtin {
			continue
		}
		var names []string
		_ = json.Unmarshal([]byte(agent.ToolsJSON), &names)
		seen := map[string]bool{}
		g := toolGroup{
			Scope:       "agent:" + agent.Key,
			Title:       agent.Name,
			Description: "Agent " + agent.Key + " 的工具清单；在 Agent 页面修改。",
			AgentKey:    agent.Key,
		}
		for _, name := range names {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			for _, t := range all {
				if t.Deleted || strings.ToLower(t.Name) != key {
					continue
				}
				g.Rows = append(g.Rows, toolRow{Tool: t})
				g.Total++
				if t.Enabled {
					g.Active++
				}
				break
			}
		}
		if g.Total > 0 {
			agentGroups = append(agentGroups, g)
		}
	}

	var archived []domain.ManagedTool
	for _, t := range all {
		if t.Deleted {
			archived = append(archived, t)
		}
	}
	if !includeArchived {
		archived = nil
	}
	s.render(w, "tools.html", toolsData{
		Title: "工具管理", Tab: "tools", Groups: groups, AgentGroups: agentGroups, Archived: archived,
		Categories: categories, Total: total, Active: active,
		ShowArchived: includeArchived, Error: r.URL.Query().Get("error"),
	})
}

func (s *Server) handleToolToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	t, err := s.st.GetManagedTool(r.Context(), id)
	if err != nil {
		redirectToolError(w, r, "工具不存在")
		return
	}
	if t.Essential {
		redirectToolError(w, r, "核心工具不可关闭")
		return
	}
	if err := s.st.SetManagedToolEnabled(r.Context(), id, !t.Enabled); err != nil {
		redirectToolError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/tools#"+t.Scope, http.StatusSeeOther)
}

var toolNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func (s *Server) handleToolSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	scope := strings.TrimSpace(r.FormValue("scope"))
	name := strings.TrimSpace(r.FormValue("name"))
	display := strings.TrimSpace(r.FormValue("display"))
	description := strings.TrimSpace(r.FormValue("description"))
	categoryID, _ := strconv.ParseInt(r.FormValue("category_id"), 10, 64)
	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind == "" {
		kind = "http"
	}
	enabled := r.FormValue("enabled") == "1"

	if id > 0 {
		existing, err := s.st.GetManagedTool(r.Context(), id)
		if err != nil {
			redirectToolError(w, r, "工具不存在")
			return
		}
		existing.Display = display
		existing.Description = description
		existing.CategoryID = categoryID
		existing.Enabled = enabled
		if !existing.Builtin {
			cfg, err := httpConfigFromForm(r)
			if err != nil {
				redirectToolError(w, r, err.Error())
				return
			}
			existing.ConfigJSON = cfg
		}
		if _, err := s.st.SaveManagedTool(r.Context(), existing); err != nil {
			redirectToolError(w, r, err.Error())
			return
		}
		http.Redirect(w, r, "/tools#"+existing.Scope, http.StatusSeeOther)
		return
	}

	if !toolNamePattern.MatchString(name) {
		redirectToolError(w, r, "内部名称只能使用字母、数字、下划线，且不能以数字开头")
		return
	}
	if scope != toolScopeSession && scope != toolScopeWorker && scope != toolScopePlanner {
		redirectToolError(w, r, "自定义工具只支持会话 Agent、Worker 或 Planner")
		return
	}
	if kind != "http" {
		redirectToolError(w, r, "目前仅支持 HTTP 自定义工具")
		return
	}
	cfg, err := httpConfigFromForm(r)
	if err != nil {
		redirectToolError(w, r, err.Error())
		return
	}
	display = strings.TrimSpace(display)
	if display == "" {
		display = name
	}
	t := domain.ManagedTool{
		Scope: scope, Name: name, Display: display, Description: description,
		CategoryID: categoryID, Enabled: enabled, Kind: "http", ConfigJSON: cfg,
	}
	if _, err := s.st.SaveManagedTool(r.Context(), t); err != nil {
		redirectToolError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/tools#"+scope, http.StatusSeeOther)
}

func httpConfigFromForm(r *http.Request) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(r.FormValue("http_method")))
	if method == "" {
		method = http.MethodGet
	}
	u := strings.TrimSpace(r.FormValue("http_url"))
	if u == "" {
		return "", fmt.Errorf("HTTP URL 不能为空")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", fmt.Errorf("HTTP URL 必须以 http:// 或 https:// 开头")
	}
	headers := map[string]string{}
	for _, line := range strings.Split(r.FormValue("http_headers"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return "", fmt.Errorf("请求头格式应为 Key: Value")
		}
		headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	cfg, _ := json.Marshal(map[string]any{
		"method": method, "url": u, "headers": headers, "body": r.FormValue("http_body"),
	})
	return string(cfg), nil
}

func (s *Server) handleToolDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	t, err := s.st.GetManagedTool(r.Context(), id)
	if err != nil {
		redirectToolError(w, r, "工具不存在")
		return
	}
	if t.Essential {
		redirectToolError(w, r, "核心工具不可删除")
		return
	}
	if err := s.st.DeleteManagedTool(r.Context(), id); err != nil {
		redirectToolError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/tools#"+t.Scope, http.StatusSeeOther)
}

func (s *Server) handleToolRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := s.st.RestoreManagedTool(r.Context(), id); err != nil {
		redirectToolError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/tools?archived=1", http.StatusSeeOther)
}

func (s *Server) handleToolCategorySave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len([]rune(name)) > 40 {
		redirectToolError(w, r, "分类名称不能为空且不能超过 40 个字符")
		return
	}
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	if _, err := s.st.SaveToolCategory(r.Context(), id, name, sortOrder); err != nil {
		redirectToolError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/tools", http.StatusSeeOther)
}

func (s *Server) handleToolCategoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := s.st.DeleteToolCategory(r.Context(), id); err != nil {
		redirectToolError(w, r, "分类仍被工具使用，不能删除")
		return
	}
	http.Redirect(w, r, "/tools", http.StatusSeeOther)
}

func redirectToolError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/tools?error="+url.QueryEscape(msg), http.StatusSeeOther)
}

func toolCatalogSeeds() []domain.ManagedTool {
	out := make([]domain.ManagedTool, 0, len(toolCatalog))
	for _, t := range toolCatalog {
		out = append(out, domain.ManagedTool{
			Scope: t.Scope, Name: t.Name, Display: t.Display, Description: t.Description,
			Category: t.Category, Enabled: true, Essential: t.Essential, Builtin: true, Kind: "builtin",
		})
	}
	return out
}

func (s *Server) seedToolCatalog(ctx context.Context) {
	_ = s.st.SeedManagedTools(ctx, toolCatalogSeeds())
}
