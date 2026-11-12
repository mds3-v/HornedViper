package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/norma/tool"
)

func (s *Server) jobManagerFor(owner string, id int64) *tool.Manager {
	key := fmt.Sprintf("%s-%d-chat", owner, id)
	return s.chatJobManager(key)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner")
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if (owner != "conversation" && owner != "task") || id == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid owner or id"})
		return
	}
	m := s.jobManagerFor(owner, id)
	if m == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	switch {
	case r.URL.Path == "/api/jobs/output":
		job := r.URL.Query().Get("job")
		out, err := m.Output(job, 64<<10)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"output": out})
	case r.URL.Path == "/api/jobs/stop":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ok := m.Kill(r.URL.Query().Get("job"))
		writeJSON(w, http.StatusOK, map[string]any{"stopped": ok})
	default:
		list := m.List()
		out := make([]map[string]any, 0, len(list))
		for _, a := range s.activeTools(fmt.Sprintf("%s-%d", owner, id)) {
			out = append(out, map[string]any{
				"id": a.ID, "kind": "tool", "command": a.Command, "description": "前台工具",
				"status": "running", "exit_code": 0, "started_at": a.Started.Format(time.RFC3339),
				"ended_at": "", "backgrounded": false, "active": true,
			})
		}
		for _, t := range list {
			ended := ""
			if !t.EndTime.IsZero() {
				ended = t.EndTime.Format(time.RFC3339)
			}
			out = append(out, map[string]any{
				"id": t.ID, "kind": string(t.Kind), "command": t.Command,
				"description": t.Description, "status": string(t.Status),
				"exit_code": t.ExitCode, "started_at": t.StartTime.Format(time.RFC3339),
				"ended_at": ended, "backgrounded": t.Backgrounded,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) beginActiveTool(owner string, id int64, name string, input json.RawMessage) string {
	key := fmt.Sprintf("%s-%d", owner, id)
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.activeSeq++
	aid := fmt.Sprintf("live-%d", s.activeSeq)
	s.active[key] = append(s.active[key], &activeTool{ID: aid, Name: name, Command: toolTraceLine(name, input), Started: time.Now()})
	return aid
}

func (s *Server) endActiveTool(owner string, id int64, aid string) {
	key := fmt.Sprintf("%s-%d", owner, id)
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	list := s.active[key]
	for i, a := range list {
		if a.ID == aid {
			s.active[key] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(s.active[key]) == 0 {
		delete(s.active, key)
	}
}

func (s *Server) activeTools(key string) []*activeTool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	out := make([]*activeTool, len(s.active[key]))
	copy(out, s.active[key])
	return out
}
