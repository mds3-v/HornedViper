package api

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"hornedviper/internal/domain"
)

// recordConversationFetch records session web_fetch calls in the same traffic
// ledger as task requests, tagged by conversation id and task_id=0.
func (s *Server) recordConversationFetch(ctx context.Context, convID int64, input, output string, isErr bool) {
	var in struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal([]byte(input), &in)
	target := strings.TrimSpace(in.URL)
	if target == "" {
		return
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return
	}
	errText := ""
	if isErr {
		errText = "tool_error"
	}
	_ = s.st.RecordTraffic(ctx, domain.Traffic{
		TaskID: 0, ConversationID: convID,
		Method: "GET", Host: u.Host, URL: u.String(),
		RequestRaw: "GET " + target, ResponseRaw: output,
		RequestSize: len(input), ResponseSize: len(output),
		Error: errText,
	})
}
