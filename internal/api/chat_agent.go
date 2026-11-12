package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"

	"hornedviper/internal/domain"
)

const mainAgentPrompt = `你是 HornedViper 的主控 Agent（mainagent）。你正在与人类对话，讨论一个已经在系统中运行或完成的安全测试任务。

你可以看到任务状态、目标状态、发现、事实、资产、意图、Work 列表，以及每个 Work 的工具调用摘要。请：
- 直接回答人类的问题，简洁、具体。
- 只使用提供的任务事实，不要编造未观察到的结果。
- 不要执行工具，不要生成新的意图，不要输出大段 JSON。
- 如果任务已完成，指出关键证据；如果任务还没完成，说明当前进度和阻塞点。
- 如果人类询问某个 Work、某次工具调用或某个证据来源，直接使用上下文中的 Work/工具调用摘要回答，不要要求人类手动查询数据库。
- 如果人类提出了新的方向，说明它会作为 hint 注入 planner，由 planner 决定下一步。
- 需要某个 Work 的原始工具调用时，使用 get_work_trace；需要搜索关键字时使用 search_task；需要单次 LLM 调用时使用 get_llm_call。`

func truncateText(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (s *Server) chatProvider() (llm.Provider, string, string, error) {
	return s.chatProviderForLevel("")
}

func (s *Server) chatProviderForLevel(level string) (llm.Provider, string, string, error) {
	cfg := s.cfgStore.Get()
	p := cfg.Providers[cfg.DefaultProvider]
	if strings.TrimSpace(p.APIKey) == "" {
		return nil, "", "", fmt.Errorf("供应商 %s 未配置 API 密钥", cfg.DefaultProvider)
	}
	format := llm.FormatOpenAI
	switch p.Format {
	case "openai-responses":
		format = llm.FormatOpenAIResponses
	case "anthropic":
		format = llm.FormatAnthropic
	}
	lc := llm.Config{Format: format, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model}
	if strings.TrimSpace(level) != "" {
		switch normalizeReasoningLevel(level) {
		case "Off":
			lc.ThinkingType = "disabled"
		case "Low":
			lc.ReasoningEffort = "low"
		case "High":
			lc.ReasoningEffort = "high"
		case "Max":
			lc.ReasoningEffort = "max"
		}
	}
	prov, err := llm.NewProvider(lc)
	return prov, cfg.DefaultProvider, p.Model, err
}

// StartChat stores the human turn and an assistant placeholder, then answers
// asynchronously so the page can show a thinking state.
func (s *Server) StartChat(ctx context.Context, taskID int64, text string) error {
	return s.StartChatWithAttachments(ctx, taskID, text, nil)
}

func (s *Server) StartChatWithAttachments(ctx context.Context, taskID int64, text string, attachments []domain.Attachment) error {
	task, err := s.st.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return fmt.Errorf("任务不存在")
	}
	log.Printf("[chat] task %d human message (%d chars, %d files)", taskID, len(text), len(attachments))
	_ = s.SendHint(ctx, taskID, text)
	if _, err := s.st.CreateChatMessage(ctx, taskID, "user", text, "done", attachments...); err != nil {
		return err
	}
	placeholder, err := s.st.CreateChatMessage(ctx, taskID, "assistant", "", "thinking")
	if err != nil {
		return err
	}
	go s.answerChat(taskID, placeholder.ID, text)
	return nil
}

func (s *Server) answerChat(taskID, assistantID int64, question string) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	s.chatMu.Lock()
	s.chatCancel[taskID] = cancel
	s.chatMu.Unlock()
	defer func() {
		cancel()
		s.chatMu.Lock()
		delete(s.chatCancel, taskID)
		s.chatMu.Unlock()
	}()

	prov, providerName, model, err := s.chatProvider()
	if err != nil {
		log.Printf("[chat] task %d provider error: %v", taskID, err)
		_ = s.st.UpdateChatMessage(ctx, assistantID, "模型调用失败："+err.Error(), "", "error", err.Error())
		return
	}
	task, _ := s.st.GetTask(ctx, taskID)
	if task == nil {
		_ = s.st.UpdateChatMessage(ctx, assistantID, "任务不存在", "", "error", "task not found")
		return
	}
	goal, _ := s.st.CurrentGoal(ctx, taskID)
	findings, _ := s.st.ListFindings(ctx, taskID, 100)
	facts, _ := s.st.ListFacts(ctx, taskID, 100)
	assets, _ := s.st.ListTaskAssets(ctx, taskID, 200)
	intents, _ := s.st.ListIntents(ctx, taskID)
	works, _ := s.st.ListWorks(ctx, taskID)
	activities, _ := s.st.ListActivities(ctx, taskID, 2000)

	var contextText strings.Builder
	fmt.Fprintf(&contextText, "任务 #%d：%s\n状态：%s\n模式：%s\n", task.ID, task.Title, statusLabel(task.Status), modeLabel(task.Mode))
	if goal != nil {
		fmt.Fprintf(&contextText, "目标状态：%s\n", statusLabel(goal.State))
	}
	if len(findings) > 0 {
		contextText.WriteString("\n发现：\n")
		for _, f := range findings {
			fmt.Fprintf(&contextText, "- [%s] %s；证据：%s\n", riskLabel(f.Severity), f.Title, f.Evidence)
		}
	}
	if len(facts) > 0 {
		contextText.WriteString("\n事实：\n")
		for _, f := range facts {
			fmt.Fprintf(&contextText, "- %s；证据：%s\n", f.Summary, f.Evidence)
		}
	}
	if len(assets) > 0 {
		contextText.WriteString("\n资产：\n")
		for _, a := range assets {
			fmt.Fprintf(&contextText, "- %s：%s\n", assetTypeLabel(a.Type), a.Value)
		}
	}
	if len(intents) > 0 {
		contextText.WriteString("\n意图：\n")
		for _, in := range intents {
			fmt.Fprintf(&contextText, "- #%d [%s] %s\n", in.ID, statusLabel(in.State), in.Summary)
		}
	}
	if len(works) > 0 {
		counts := map[int64]map[string]int{}
		for _, a := range activities {
			if a.Kind != "tool_use" {
				continue
			}
			if counts[a.WorkID] == nil {
				counts[a.WorkID] = map[string]int{}
			}
			counts[a.WorkID][a.Tool]++
		}
		contextText.WriteString("\nWork 索引（需要原始工具调用时用 get_work_trace）：\n")
		for _, w := range works {
			state := statusLabel(w.State)
			stop := w.StopReason
			if stop == "" {
				stop = "—"
			}
			var parts []string
			for name, n := range counts[w.ID] {
				parts = append(parts, fmt.Sprintf("%s×%d", name, n))
			}
			sort.Strings(parts)
			fmt.Fprintf(&contextText, "- Work #%d intent #%d worker=%s 状态=%s 停止原因=%s 工具：%s\n",
				w.ID, w.IntentID, w.WorkerID, state, stop, strings.Join(parts, ", "))
		}
	}

	summary, coveredID, _ := s.st.GetChatSummary(ctx, taskID)
	if summary != "" {
		contextText.WriteString("\n对话摘要：\n" + summary + "\n")
	}
	history, _ := s.st.ChatHistory(ctx, taskID, coveredID, 20)
	var attachmentLines []string
	for _, m := range history {
		for _, a := range m.Attachments {
			attachmentLines = append(attachmentLines, fmt.Sprintf("- %s：%s", a.Name, a.Path))
		}
	}
	if len(attachmentLines) > 0 {
		contextText.WriteString("\n本轮会话上传的附件（可用 read_attachment 读取）：\n" + strings.Join(attachmentLines, "\n") + "\n")
	}
	// Small text attachments are included directly so the main agent can answer
	// even when the model forgets to call read_attachment.
	seenPaths := map[string]bool{}
	totalAttachBytes := 0
	for _, m := range history {
		for _, a := range m.Attachments {
			if seenPaths[a.Path] || totalAttachBytes >= 48<<10 {
				continue
			}
			seenPaths[a.Path] = true
			data, err := os.ReadFile(a.Path)
			if err != nil || len(data) > 16<<10 {
				continue
			}
			ct := http.DetectContentType(data)
			if !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
				continue
			}
			totalAttachBytes += len(data)
			fmt.Fprintf(&contextText, "\n附件内容 %s：\n%s\n", a.Name, string(data))
		}
	}
	var messages []llm.Message
	for _, m := range history {
		if m.ID == assistantID || m.Status == "thinking" {
			continue
		}
		content := m.Content
		if len(m.Attachments) > 0 {
			var lines []string
			for _, a := range m.Attachments {
				lines = append(lines, fmt.Sprintf("- %s (%s)", a.Name, a.Path))
			}
			content += "\n\n[上传文件]\n" + strings.Join(lines, "\n")
		}
		switch m.Role {
		case "user":
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(content)}})
		case "assistant":
			if content != "" {
				messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(content)}})
			}
		}
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != llm.RoleUser {
		messages = append(messages, llm.UserText(question))
	}

	cfg := s.cfgStore.Get()
	workDir := filepath.Join(cfg.ArtifactDir, fmt.Sprintf("task-%d", taskID), "chat-workspace")
	_ = os.MkdirAll(workDir, 0o700)
	chatTools := &chatToolSet{st: s.st, taskID: taskID, artifactDir: cfg.ArtifactDir, workDir: workDir, jobs: s.chatJobManager(fmt.Sprintf("task-%d-chat", taskID))}
	toolList := chatTools.tools()
	byName := map[string]tool.CoreTool{}
	for _, t := range toolList {
		byName[t.Name()] = t
	}
	req := llm.CompletionRequest{
		System:    []string{mainAgentPrompt, contextText.String()},
		Messages:  messages,
		Tools:     chatTools.schemas(),
		MaxTokens: 256000,
	}

	collect := func(msg llm.Message) (string, string, []string) {
		var th, tx strings.Builder
		var calls []string
		for _, b := range msg.Content {
			switch b.Type {
			case llm.BlockThinking:
				th.WriteString(b.Thinking)
			case llm.BlockText:
				tx.WriteString(b.Text)
			case llm.BlockToolUse:
				calls = append(calls, b.Name)
			}
		}
		return th.String(), tx.String(), calls
	}
	recordCall := func(callReq llm.CompletionRequest, msg llm.Message, usage llm.Usage, latency time.Duration, callErr error) {
		reqJSON, _ := json.Marshal(callReq)
		usageJSON, _ := json.Marshal(usage)
		respJSON, _ := json.Marshal(msg)
		th, _, calls := collect(msg)
		toolsJSON, _ := json.Marshal(calls)
		errText := ""
		if callErr != nil {
			errText = callErr.Error()
		}
		sum := sha256.Sum256(reqJSON)
		_ = s.st.RecordLLMCall(ctx, domain.LLMCall{
			TaskID: taskID, Role: "mainagent", Provider: providerName, Model: model,
			RequestJSON: string(reqJSON), ResponseJSON: string(respJSON),
			Thinking: th, ToolCallsJSON: string(toolsJSON), UsageJSON: string(usageJSON),
			LatencyMS: latency.Milliseconds(), Error: errText,
			RequestHash: hex.EncodeToString(sum[:]), CreatedAt: time.Now(),
		})
	}

	var thinking, text strings.Builder
	var callErr error
	for {
		start := time.Now()
		msg, _, usage, err := prov.Complete(ctx, req)
		recordCall(req, msg, usage, time.Since(start), err)
		th, tx, _ := collect(msg)
		thinking.WriteString(th)
		if tx != "" {
			text.Reset()
			text.WriteString(tx)
		}
		if err != nil {
			callErr = err
			break
		}
		toolUses := msg.ToolUses()
		if len(toolUses) == 0 {
			break
		}
		req.Messages = append(req.Messages, msg)
		for _, tu := range toolUses {
			tl := byName[tu.Name]
			out := "unknown tool: " + tu.Name
			isErr := true
			if tl != nil {
				liveID := s.beginActiveTool("task", taskID, tu.Name, tu.Input)
				res, callErr := tl.Call(ctx, tu.Input, &tool.ToolContext{WorkingDir: chatTools.workDir, MaxOutputChars: 12000, Tasks: chatTools.jobs})
				s.endActiveTool("task", taskID, liveID)
				out = res.Flatten()
				isErr = callErr != nil || res.IsError
				if callErr != nil {
					out = "tool error: " + callErr.Error()
				}
				thinking.WriteString("\n\n" + toolTraceLine(tu.Name, tu.Input))
			}
			req.Messages = append(req.Messages, llm.Message{
				Role:    llm.RoleUser,
				Content: []llm.ContentBlock{llm.ToolResultText(tu.ID, out, isErr)},
			})
		}
	}

	// Reasoning models can spend the whole token budget on thinking and emit no
	// final text. Retry once with a clean, answer-only instruction.
	if callErr == nil && strings.TrimSpace(text.String()) == "" {
		retryReq := req
		retryReq.Tools = nil
		retryReq.Messages = append(append([]llm.Message{}, req.Messages...),
			llm.UserText("你刚才只输出了思考过程，没有给出最终回答。请基于任务事实直接给出最终回答，不要重复思考过程。"))
		retryReq.MaxTokens = 2048
		retryReq.Thinking = "disabled"
		start := time.Now()
		msg, _, usage, err := prov.Complete(ctx, retryReq)
		recordCall(retryReq, msg, usage, time.Since(start), err)
		th, tx, _ := collect(msg)
		thinking.WriteString(th)
		if tx != "" {
			text.Reset()
			text.WriteString(tx)
		}
		if err != nil {
			callErr = err
		}
	}

	if ctx.Err() != nil {
		return
	}
	if callErr != nil {
		log.Printf("[chat] task %d model error: %v", taskID, callErr)
		_ = s.st.UpdateChatMessage(ctx, assistantID, "模型调用失败："+callErr.Error(), thinking.String(), "error", callErr.Error())
		return
	}
	answer := strings.TrimSpace(text.String())
	if answer == "" {
		answer = "模型只返回了思考过程，没有生成最终回答。请重试或换个问法。"
	}
	_ = s.st.UpdateChatMessage(ctx, assistantID, answer, thinking.String(), "done", "")
	go s.updateChatSummary(taskID)
}

// updateChatSummary compresses older chat turns into a rolling summary. The
// most recent 10 turns remain verbatim; the summary only affects conversation
// memory, never evidence or findings.
func (s *Server) updateChatSummary(taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	summary, coveredID, _ := s.st.GetChatSummary(ctx, taskID)
	msgs, _ := s.st.ListChatMessages(ctx, taskID, 1000)
	var pending []domain.ChatMessage
	for _, m := range msgs {
		if m.ID > coveredID && m.Status == "done" {
			pending = append(pending, m)
		}
	}
	if len(pending) <= 22 {
		return
	}
	old := pending[:len(pending)-10]
	var transcript strings.Builder
	for _, m := range old {
		if m.Role == "user" {
			fmt.Fprintf(&transcript, "用户：%s\n", m.Content)
		} else if m.Content != "" {
			fmt.Fprintf(&transcript, "主 Agent：%s\n", m.Content)
		}
	}
	prov, providerName, model, err := s.chatProvider()
	if err != nil {
		return
	}
	req := llm.CompletionRequest{
		System:    []string{"你是对话摘要器。把历史对话压缩成简洁摘要，保留任务结论、关键证据、用户偏好和未解决问题。不要编造。"},
		Messages:  []llm.Message{llm.UserText(transcript.String())},
		MaxTokens: 700,
	}
	start := time.Now()
	msg, _, usage, callErr := prov.Complete(ctx, req)
	latency := time.Since(start)
	var text strings.Builder
	for _, b := range msg.Content {
		if b.Type == llm.BlockText {
			text.WriteString(b.Text)
		}
	}
	if callErr != nil || strings.TrimSpace(text.String()) == "" {
		return
	}
	next := summary
	if next != "" {
		next += "\n"
	}
	next += strings.TrimSpace(text.String())
	_ = s.st.SetChatSummary(ctx, taskID, next, old[len(old)-1].ID)

	reqJSON, _ := json.Marshal(req)
	respJSON, _ := json.Marshal(msg)
	usageJSON, _ := json.Marshal(usage)
	sum := sha256.Sum256(reqJSON)
	_ = s.st.RecordLLMCall(ctx, domain.LLMCall{
		TaskID: taskID, Role: "chat_summary", Provider: providerName, Model: model,
		RequestJSON: string(reqJSON), ResponseJSON: string(respJSON),
		Thinking: "", UsageJSON: string(usageJSON), LatencyMS: latency.Milliseconds(),
		RequestHash: hex.EncodeToString(sum[:]), CreatedAt: time.Now(),
	})
}

func (s *Server) stopTaskChat(ctx context.Context, taskID int64) bool {
	s.chatMu.Lock()
	cancel := s.chatCancel[taskID]
	delete(s.chatCancel, taskID)
	s.chatMu.Unlock()
	if cancel != nil {
		cancel()
	}
	msgs, _ := s.st.ListChatMessages(ctx, taskID, 20)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && msgs[i].Status == "thinking" {
			_ = s.st.UpdateChatMessage(ctx, msgs[i].ID, "已停止生成。", msgs[i].Thinking, "cancelled", "")
			return true
		}
	}
	return cancel != nil
}
