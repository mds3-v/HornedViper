package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/llm"

	"hornedviper/internal/domain"
)

type recordedToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type recordedResponse struct {
	Text       string             `json:"text,omitempty"`
	Thinking   string             `json:"thinking,omitempty"`
	ToolCalls  []recordedToolCall `json:"tool_calls,omitempty"`
	Usage      llm.Usage          `json:"usage"`
	StopReason string             `json:"stop_reason,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// recordingProvider wraps a provider and persists one llm_calls row per model
// invocation, including request, response, thinking, tool calls and usage.
type recordingProvider struct {
	inner    llm.Provider
	sink     func(domain.LLMCall)
	taskID   int64
	workID   int64
	role     string
	provider string
	model    string
	mu       sync.Mutex
	turn     int
}

func (p *recordingProvider) nextTurn() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turn++
	return p.turn
}

func (p *recordingProvider) record(req llm.CompletionRequest, resp recordedResponse, latency time.Duration) {
	if p.sink == nil {
		return
	}
	reqJSON, _ := json.Marshal(req)
	respJSON, _ := json.Marshal(resp)
	toolsJSON, _ := json.Marshal(resp.ToolCalls)
	usageJSON, _ := json.Marshal(resp.Usage)
	sum := sha256.Sum256(reqJSON)
	p.sink(domain.LLMCall{
		TaskID: p.taskID, WorkID: p.workID, Role: p.role,
		Turn: p.nextTurn(), Provider: p.provider, Model: p.model,
		RequestJSON: string(reqJSON), ResponseJSON: string(respJSON),
		Thinking: resp.Thinking, ToolCallsJSON: string(toolsJSON),
		UsageJSON: string(usageJSON), LatencyMS: latency.Milliseconds(),
		Error: resp.Error, RequestHash: hex.EncodeToString(sum[:]),
		CreatedAt: time.Now(),
	})
}

func (p *recordingProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		start := time.Now()
		var thinking, text strings.Builder
		var tools []recordedToolCall
		var usage llm.Usage
		var stopReason, callErr string
		for ev, err := range p.inner.Stream(ctx, req) {
			if err != nil {
				callErr = err.Error()
			}
			switch ev.Type {
			case llm.SEThinkingDelta:
				thinking.WriteString(ev.Text)
			case llm.SETextDelta:
				text.WriteString(ev.Text)
			case llm.SEToolUseStart:
				tools = append(tools, recordedToolCall{ID: ev.ToolID, Name: ev.ToolName})
			case llm.SEToolInputJSON:
				if len(tools) > 0 {
					tools[len(tools)-1].Input += ev.Text
				}
			case llm.SEMessageDelta:
				if ev.StopReason != "" {
					stopReason = ev.StopReason
				}
			}
			if ev.Usage.InputTokens != 0 || ev.Usage.OutputTokens != 0 || ev.Usage.CacheReadTokens != 0 || ev.Usage.CacheWriteTokens != 0 {
				usage = ev.Usage
			}
			if !yield(ev, err) {
				break
			}
		}
		resp := recordedResponse{
			Text: text.String(), Thinking: thinking.String(), ToolCalls: tools,
			Usage: usage, StopReason: stopReason, Error: callErr,
		}
		p.record(req, resp, time.Since(start))
	}
}

func (p *recordingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	start := time.Now()
	msg, raw, usage, err := p.inner.Complete(ctx, req)
	var thinking, text strings.Builder
	var tools []recordedToolCall
	for _, b := range msg.Content {
		switch b.Type {
		case llm.BlockThinking:
			thinking.WriteString(b.Thinking)
		case llm.BlockText:
			text.WriteString(b.Text)
		case llm.BlockToolUse:
			tools = append(tools, recordedToolCall{ID: b.ID, Name: b.Name, Input: string(b.Input)})
		}
	}
	resp := recordedResponse{Text: text.String(), Thinking: thinking.String(), ToolCalls: tools, Usage: usage}
	if err != nil {
		resp.Error = err.Error()
	}
	p.record(req, resp, time.Since(start))
	return msg, raw, usage, err
}
