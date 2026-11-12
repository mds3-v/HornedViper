package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"hornedviper/internal/domain"
)

type llmCallData struct {
	Title  string
	Tab    string
	Call   *domain.LLMCall
	System []string
}

func (s *Server) handleLLMCall(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/llm-calls/"), "/"), 10, 64)
	call, err := s.st.GetLLMCall(r.Context(), id)
	if err != nil || call == nil {
		http.NotFound(w, r)
		return
	}
	var req struct{ System []string }
	_ = json.Unmarshal([]byte(call.RequestJSON), &req)
	s.render(w, "llm_call.html", llmCallData{Title: "LLM 调用", Tab: "llm-records", Call: call, System: req.System})
}
