package api

import (
	"html/template"
	"net/http"
	"time"

	"hornedviper/internal/markdown"
)

func (s *Server) handleTaskChatMessages(w http.ResponseWriter, r *http.Request, taskID int64) {
	task, err := s.st.GetTask(r.Context(), taskID)
	if err != nil || task == nil {
		http.NotFound(w, r)
		return
	}
	messages, _ := s.st.ListChatMessages(r.Context(), taskID, 1000)
	thinking := false
	for _, m := range messages {
		if m.Status == "thinking" {
			thinking = true
			break
		}
	}
	tmpl, err := template.New("chat_messages").Funcs(template.FuncMap{
		"markdown":      markdown.Render,
		"shortTime":     func(t time.Time) string { return t.Format("01-02 15:04:05") },
		"attachmentURL": attachmentURL,
		"thinkingSteps": thinkingSteps,
		"thinkStepKind": thinkStepKind,
		"thinkDuration": thinkDuration,
	}).ParseFS(webFS, "web/templates/thinking.html", "web/templates/chat_messages.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.ExecuteTemplate(w, "chat_messages", chatData{
		Selected: task, Messages: messages, Thinking: thinking,
	})
}
