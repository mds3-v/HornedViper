package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"hornedviper/internal/domain"
)

type llmRecordRow struct {
	Call              domain.LLMCall
	TaskTitle         string
	ConversationTitle string
	InputTokens       int
	OutputTokens      int
}

type llmRecordsData struct {
	Title                  string
	Tab                    string
	Calls                  []llmRecordRow
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

func (s *Server) handleLLMRecords(w http.ResponseWriter, r *http.Request) {
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
	convs, _ := s.st.ListConversations(ctx, 5000)
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
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(search)) > 200 {
		search = string([]rune(search)[:200])
	}
	if qTask > 0 || qConv > 0 {
		total, _ := s.st.CountLLMCallsSearch(ctx, qTask, qConv, search)
		page := newPageInfo(r, total)
		calls, _ := s.st.ListLLMCallsPageSearch(ctx, qTask, qConv, search, page.PerPage, (page.Page-1)*page.PerPage)
		data := llmRecordsData{Title: "LLM 记录", Tab: "llm-records", Pagination: page,
			FilterTaskID: qTask, FilterConversationID: qConv, SearchQuery: search}
		for _, c := range calls {
			data.Calls = append(data.Calls, llmRow(c, titles, convTitles))
		}
		if qTask > 0 {
			data.Mode = "task"
			data.GroupTitle = fmt.Sprintf("任务 #%d %s", qTask, titles[qTask])
			data.SearchClearURL = fmt.Sprintf("/llm-records?task=%d", qTask)
		} else {
			data.Mode = "conversation"
			data.GroupTitle = fmt.Sprintf("新会话 #%d %s", qConv, convTitles[qConv])
			data.SearchClearURL = fmt.Sprintf("/llm-records?conversation=%d", qConv)
		}
		data.BackURL = "/llm-records"
		s.render(w, "llm_records.html", data)
		return
	}

	groups, _ := s.st.LLMCallGroups(ctx)
	taskGroups := make([]trafficGroup, 0)
	conversationGroups := make([]trafficGroup, 0)
	for _, g := range groups {
		if g.TaskID > 0 {
			taskGroups = append(taskGroups, trafficGroup{Kind: "任务", TaskID: g.TaskID, Title: titles[g.TaskID],
				Count: g.Count, LastAt: g.LastAt, URL: fmt.Sprintf("/llm-records?task=%d", g.TaskID)})
		} else if g.ConversationID > 0 {
			conversationGroups = append(conversationGroups, trafficGroup{Kind: "新会话", ConversationID: g.ConversationID, Title: convTitles[g.ConversationID],
				Count: g.Count, LastAt: g.LastAt, URL: fmt.Sprintf("/llm-records?conversation=%d", g.ConversationID)})
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
	s.render(w, "llm_records.html", llmRecordsData{
		Title: "LLM 记录", Tab: "llm-records",
		TaskGroups: paginateSlice(taskGroups, taskPage), TaskPagination: taskPage,
		ConversationGroups: paginateSlice(conversationGroups, convPage), ConversationPagination: convPage,
	})
}

func llmRow(c domain.LLMCall, titles, convTitles map[int64]string) llmRecordRow {
	var usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	_ = json.Unmarshal([]byte(c.UsageJSON), &usage)
	return llmRecordRow{
		Call: c, TaskTitle: titles[c.TaskID], ConversationTitle: convTitles[c.ConversationID],
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
	}
}
