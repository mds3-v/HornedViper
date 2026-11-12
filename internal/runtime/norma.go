package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"hornedviper/internal/domain"
)

// NormaConfig configures the Norma-backed runtime.
type NormaConfig struct {
	Format       string // anthropic | openai | openai-responses
	BaseURL      string
	APIKey       string
	Model        string
	ProviderName string
	ArtifactDir  string
	MaxTurns     int
	MaxDuration  time.Duration
	LLMTimeout   time.Duration
	// Container guard is internal state; no config needed.
	Provider                 llm.Provider // optional; for tests or a host-owned provider
	Trace                    TraceFunc    // optional activity sink
	Traffic                  func(domain.Traffic)
	LLMCall                  func(domain.LLMCall)
	Guard                    func(toolName string, input json.RawMessage) (mode, rule string)
	ToolEnabled              func(scope, name string) bool
	CustomTools              func(scope string) []tool.CoreTool
	QuarantineList           func(ctx context.Context, taskID int64, limit int) ([]domain.QuarantineDigest, error)
	QuarantineRead           func(ctx context.Context, id int64) (*domain.QuarantineItem, error)
	FlagAlreadySubmitted     func(ctx context.Context, taskID int64, flag string) (bool, error)
	FlagSubmissionRecorded   func(ctx context.Context, taskID int64, flag string, accepted bool, message string) error
	ChallengeCompleted       func(ctx context.Context, taskID int64, challenge string) (bool, error)
	ChallengeCompletedRecord func(ctx context.Context, taskID int64, challenge string) error
}

// TraceFunc receives one visible agent step. It is called synchronously from
// the streaming loop, so a slow sink slows the agent down.
type TraceFunc func(domain.Activity)

// NormaRuntime implements Runtime with Norma agentcore sessions.
type NormaRuntime struct {
	prov                     llm.Provider
	providerName             string
	model                    string
	artifactDir              string
	maxTurns                 int
	maxDuration              time.Duration
	llmTimeout               time.Duration
	containers               *containerGuard
	trace                    TraceFunc
	traffic                  func(domain.Traffic)
	llmCall                  func(domain.LLMCall)
	guard                    func(toolName string, input json.RawMessage) (mode, rule string)
	toolEnabled              func(scope, name string) bool
	customTools              func(scope string) []tool.CoreTool
	quarantineList           func(ctx context.Context, taskID int64, limit int) ([]domain.QuarantineDigest, error)
	quarantineRead           func(ctx context.Context, id int64) (*domain.QuarantineItem, error)
	flagAlreadySubmitted     func(ctx context.Context, taskID int64, flag string) (bool, error)
	flagSubmissionRecorded   func(ctx context.Context, taskID int64, flag string, accepted bool, message string) error
	challengeCompleted       func(ctx context.Context, taskID int64, challenge string) (bool, error)
	challengeCompletedRecord func(ctx context.Context, taskID int64, challenge string) error
}

func NewNormaRuntime(cfg NormaConfig) (*NormaRuntime, error) {
	prov := cfg.Provider
	if prov == nil {
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, fmt.Errorf("norma runtime: no API key (set HV_LLM_API_KEY or use the fake runtime)")
		}
		if strings.TrimSpace(cfg.Model) == "" {
			return nil, fmt.Errorf("norma runtime: no model (set HV_LLM_MODEL)")
		}
		format := llm.FormatOpenAI
		switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
		case "", "openai":
			format = llm.FormatOpenAI
		case "openai-responses", "responses":
			format = llm.FormatOpenAIResponses
		case "anthropic":
			format = llm.FormatAnthropic
		default:
			return nil, fmt.Errorf("norma runtime: unsupported format %q", cfg.Format)
		}
		p, err := llm.NewProvider(llm.Config{
			Format: format, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model,
		})
		if err != nil {
			return nil, fmt.Errorf("norma runtime: provider: %w", err)
		}
		prov = p
	}
	if cfg.ArtifactDir == "" {
		cfg.ArtifactDir = "artifacts"
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 30
	}
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = 20 * time.Minute
	}
	if cfg.LLMTimeout <= 0 {
		cfg.LLMTimeout = 2 * time.Minute
	}
	return &NormaRuntime{
		prov: prov, providerName: cfg.ProviderName, model: cfg.Model,
		artifactDir: cfg.ArtifactDir,
		maxTurns:    cfg.MaxTurns, maxDuration: cfg.MaxDuration, llmTimeout: cfg.LLMTimeout,
		containers: newContainerGuard(),
		trace:      cfg.Trace, traffic: cfg.Traffic, llmCall: cfg.LLMCall, guard: cfg.Guard,
		toolEnabled: cfg.ToolEnabled, customTools: cfg.CustomTools,
	}, nil
}

type plannerCapture struct {
	Called  bool
	Intents []domain.IntentSpec
	GoalMet *domain.GoalMetProposal
}

func (r *NormaRuntime) Plan(ctx context.Context, req *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	if req == nil || req.Packet == nil || req.Goal == nil {
		return nil, fmt.Errorf("norma planner: incomplete request")
	}
	cap := &plannerCapture{}
	plannerTools := []tool.CoreTool{proposeIntentsTool(cap), proposeGoalMetTool(cap)}
	if r.quarantineList != nil {
		plannerTools = append(plannerTools, listQuarantineTool(req.Task.ID, r.quarantineList))
	}
	if r.quarantineRead != nil {
		plannerTools = append(plannerTools, readQuarantineTool(req.Task.ID, r.quarantineRead))
	}
	plannerTools = append(plannerTools, r.loadCustomTools("planner")...)
	sess := agentcore.NewSession(agentcore.Options{
		Provider:       r.recordedProvider(req.Task.ID, 0, "planner"),
		SystemPrompt:   []string{plannerPrompt},
		Tools:          r.filterTools("planner", plannerTools),
		PermissionMode: permission.ModeBypass,
		MaxTurns:       0,
		MaxDuration:    5 * time.Minute,
		MaxTokens:      256000,
		Compaction: &compaction.Config{
			ContextWindow: 200000, MaxOutputTokens: 8000, KeepRecent: 8,
		},
	})
	defer sess.Close()

	packet, err := json.MarshalIndent(req.Packet, "", "  ")
	if err != nil {
		return nil, err
	}
	prompt := "Wake packet:\n" + string(packet) +
		"\n\nDecide the next intents. If there is no new direction, submit an empty array. " +
		"Call the propose_intents tool now."
	displayPrompt := prompt
	if req.Goal != nil && strings.TrimSpace(req.Goal.Text) != "" {
		displayPrompt = strings.ReplaceAll(displayPrompt, req.Goal.Text, "【目标内容已隐藏】")
	}
	r.emit(domain.Activity{
		TaskID: req.Task.ID, Role: "planner", Kind: "prompt",
		Summary: "planner wake packet", Detail: displayPrompt,
	})
	buf := newTraceBuffer(r, req.Task.ID, 0, 0, "planner")
	for ev, err := range sess.Prompt(ctx, prompt) {
		if err != nil {
			buf.flush()
			r.emit(domain.Activity{
				TaskID: req.Task.ID, Role: "planner", Kind: "error", Summary: err.Error(),
			})
			return nil, fmt.Errorf("norma planner: %w", err)
		}
		buf.add(ev)
	}
	buf.flush()
	return &domain.PlannerResponse{Intents: cap.Intents, GoalMet: cap.GoalMet}, nil
}

func listQuarantineTool(taskID int64, list func(context.Context, int64, int) ([]domain.QuarantineDigest, error)) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "list_quarantine",
		Description: "List compact digests of failed, late, or unconfirmed lane results for this task. Use read_quarantine for detail.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			items, err := list(ctx, taskID, 20)
			if err != nil {
				return tool.Errorf("list quarantine: " + err.Error()), nil
			}
			data, _ := json.MarshalIndent(items, "", "  ")
			return tool.Text(string(data)), nil
		},
	})
}

func readQuarantineTool(taskID int64, get func(context.Context, int64) (*domain.QuarantineItem, error)) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "read_quarantine",
		Description: "Read one quarantine item (summary, candidate flags, and bounded raw payload) by id.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "integer"}},
			"required":   []any{"id"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid id: " + err.Error()), nil
			}
			item, err := get(ctx, in.ID)
			if err != nil || item == nil {
				return tool.Errorf("quarantine not found"), nil
			}
			if item.TaskID != taskID {
				return tool.Errorf("quarantine does not belong to this task"), nil
			}
			payload := []rune(item.PayloadJSON)
			if len(payload) > 16000 {
				item.PayloadJSON = string(payload[:16000]) + "\n…（已截断，完整内容请在任务隔离页查看）"
			}
			data, _ := json.MarshalIndent(item, "", "  ")
			return tool.Text(string(data)), nil
		},
	})
}

type workerCapture struct {
	Result   domain.WorkResult
	Finished bool
}

func (r *NormaRuntime) Work(ctx context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	if req == nil || req.Intent == nil || req.Work == nil {
		return nil, fmt.Errorf("norma worker: incomplete request")
	}
	runDir := filepath.Join(r.artifactDir, fmt.Sprintf("task-%d", req.Task.ID), fmt.Sprintf("work-%d", req.Work.ID))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("norma worker: run dir: %w", err)
	}

	cap := &workerCapture{}
	tools := append(tool.DefaultTools(),
		httpRequestTool(req.Task.ID, req.Work.ID, req.Intent.ID, r.traffic, r.flagAlreadySubmitted, r.flagSubmissionRecorded, r.containers.before, r.containers.after, intentAllowsNoContainer(req.Intent), r.challengeCompleted, r.challengeCompletedRecord, r.judgeContainer),
		recordAssetsTool(cap), recordFactsTool(cap), reportFindingsTool(cap),
		attachEvidenceTool(cap), finishTool(cap),
	)
	tools = append(r.filterTools("worker", tools), r.loadCustomTools("worker")...)
	sess := agentcore.NewSession(agentcore.Options{
		Provider:       r.recordedProvider(req.Task.ID, req.Work.ID, "worker"),
		SystemPrompt:   []string{workerPrompt},
		Tools:          tools,
		PermissionMode: permission.ModeDefault,
		CanUseTool:     r.canUseToolFor(req.Task.ID, runDir),
		EnableWebFetch: r.toolEnabledFor("worker", "WebFetch"),
		MaxTurns:       0,
		MaxDuration:    r.maxDuration,
		MaxTokens:      256000,
		WorkingDir:     runDir,
		ToolOutputDir:  filepath.Join(runDir, "cmd"),
		Compaction: &compaction.Config{
			ContextWindow: 200000, MaxOutputTokens: 8000, KeepRecent: 8,
		},
	})
	defer sess.Close()

	intentJSON, _ := json.MarshalIndent(req.Intent, "", "  ")
	factsJSON, _ := json.MarshalIndent(req.Facts, "", "  ")
	constraintsJSON, _ := json.MarshalIndent(req.Constraints, "", "  ")
	failuresJSON, _ := json.MarshalIndent(req.Failures, "", "  ")
	attachmentText := ""
	if atts, ok := req.Task.Budget["attachments"].([]any); ok && len(atts) > 0 {
		var lines []string
		for _, raw := range atts {
			if m, ok := raw.(map[string]any); ok {
				name, _ := m["name"].(string)
				path, _ := m["path"].(string)
				if path != "" {
					lines = append(lines, "- "+name+": "+path)
				}
			}
		}
		if len(lines) > 0 {
			attachmentText = "\n\nUploaded files:\n" + strings.Join(lines, "\n")
		}
	}
	prompt := "Intent:\n" + string(intentJSON) + "\n\nRelevant facts:\n" + string(factsJSON) +
		"\n\nPrevious failures for this target (do not repeat the same PoC without new evidence):\n" + string(failuresJSON) +
		"\n\nConstraints:\n" + string(constraintsJSON) + attachmentText +
		"\n\nWorking directory: " + runDir
	if req.Work.LaneID != "" {
		prompt += "\n\nSwarm lane: " + req.Work.LaneID
		if req.LaneStrategy != "" {
			prompt += "\nLane strategy: " + req.LaneStrategy
		}
		if req.LaneBrief != "" {
			prompt += "\nLane brief: " + req.LaneBrief
		}
		prompt += "\nStay within this lane's brief; do not plan other lanes."
	}
	prompt += "\nExecute this intent. Record assets/facts/findings/evidence and finish with finish(status)."
	r.emit(domain.Activity{
		TaskID: req.Task.ID, WorkID: req.Work.ID, IntentID: req.Intent.ID,
		Role: "worker", Kind: "prompt", Summary: "worker intent brief", Detail: prompt,
	})
	buf := newTraceBuffer(r, req.Task.ID, req.Work.ID, req.Intent.ID, "worker")
	for ev, err := range sess.Prompt(ctx, prompt) {
		if err != nil {
			buf.flush()
			r.emit(domain.Activity{
				TaskID: req.Task.ID, WorkID: req.Work.ID, IntentID: req.Intent.ID,
				Role: "worker", Kind: "error", Summary: err.Error(),
			})
			cap.Result.Status = "blocked"
			cap.Result.Error = err.Error()
			cap.Result.Partial = true
			return &cap.Result, nil
		}
		buf.add(ev)
	}
	buf.flush()
	if !cap.Finished {
		cap.Result.Status = "blocked"
		cap.Result.Error = "worker did not call finish"
		cap.Result.Partial = true
	}
	return &cap.Result, nil
}

// judgeContainer asks the model whether an ambiguous start response describes a
// usable answering scene. It is intentionally not a string-matching rule: the
// platform's error messages and semantics can change.
func (r *NormaRuntime) judgeContainer(ctx context.Context, taskID int64, challenge, action string, status int, body string) (bool, string) {
	if r == nil || r.prov == nil {
		return false, "no judge provider"
	}
	if len(body) > 4096 {
		body = body[:4096]
	}
	prompt := fmt.Sprintf(`A CTF platform returned an ambiguous challenge-instance response.
Challenge: %s
Action: %s
HTTP status: %d
Response body:
%s

Decide whether this response means the challenge is currently in a valid answering scene: a container/instance is usable for solving and submitting flags.
Do not infer success from the HTTP status alone. Return ONLY JSON:
{"usable":true|false,"reason":"short explanation"}`, challenge, action, status, body)
	judgeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	msg, _, _, err := r.recordedProvider(taskID, 0, "container_judge").Complete(judgeCtx, llm.CompletionRequest{
		System:   []string{"You are a CTF platform state judge. Decide only from the supplied response. Return compact JSON."},
		Messages: []llm.Message{llm.UserText(prompt)}, MaxTokens: 300,
	})
	if err != nil {
		return false, err.Error()
	}
	text := strings.TrimSpace(msg.Text())
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var out struct {
		Usable bool   `json:"usable"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return false, "invalid judge JSON"
	}
	return out.Usable, out.Reason
}

func (r *NormaRuntime) recordedProvider(taskID, workID int64, role string) llm.Provider {
	var p llm.Provider = r.prov
	if r.llmCall != nil {
		p = &recordingProvider{
			inner: r.prov, sink: r.llmCall, taskID: taskID, workID: workID, role: role,
			provider: r.providerName, model: r.model,
		}
	}
	if r.llmTimeout > 0 {
		p = &timeoutProvider{inner: p, timeout: r.llmTimeout}
	}
	return p
}

func (r *NormaRuntime) canUseTool(_ context.Context, toolName string, input json.RawMessage, _ permission.Context) permission.Decision {
	if toolName == "WebFetch" {
		return permission.Denied("HTTP 请求必须使用 HTTPRequest 工具，以便记录原始请求包和响应包")
	}
	if toolName == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(input, &in)
		if strings.Contains(in.Command, "curl") || strings.Contains(in.Command, "wget") {
			return permission.Denied("HTTP 请求必须使用 HTTPRequest 工具，以便记录原始请求包和响应包")
		}
	}
	if r.guard == nil || !isGuardedTool(toolName) {
		return permission.Allowed()
	}
	mode, rule := r.guard(toolName, input)
	switch mode {
	case "deny":
		return permission.Denied("已拦截：规则「" + rule + "」禁止该操作")
	case "ask":
		return permission.Denied("已拦截：规则「" + rule + "」需要人工审批，已阻止本次工具调用")
	default:
		return permission.Allowed()
	}
}

func isGuardedTool(name string) bool {
	switch name {
	case "Bash", "WebFetch", "HTTPRequest", "Write", "Edit", "MultiEdit":
		return true
	}
	return strings.HasPrefix(name, "mcp__")
}

func (r *NormaRuntime) toolEnabledFor(scope, name string) bool {
	if r.toolEnabled == nil {
		return true
	}
	return r.toolEnabled(scope, name)
}

func (r *NormaRuntime) loadCustomTools(scope string) []tool.CoreTool {
	if r.customTools == nil {
		return nil
	}
	return r.customTools(scope)
}

func (r *NormaRuntime) filterTools(scope string, tools []tool.CoreTool) []tool.CoreTool {
	if r.toolEnabled == nil {
		return tools
	}
	out := make([]tool.CoreTool, 0, len(tools))
	for _, t := range tools {
		if r.toolEnabled(scope, t.Name()) {
			out = append(out, t)
		}
	}
	return out
}

func (r *NormaRuntime) emit(a domain.Activity) {
	if r.trace != nil {
		r.trace(a)
	}
}

// traceBuffer coalesces streaming thinking/text deltas into one trace row per
// agent turn. Without this, providers that stream token chunks produce one
// activity per token instead of a readable trajectory.
type traceBuffer struct {
	rt       *NormaRuntime
	taskID   int64
	workID   int64
	intentID int64
	role     string
	thinking strings.Builder
	text     strings.Builder
}

func newTraceBuffer(rt *NormaRuntime, taskID, workID, intentID int64, role string) *traceBuffer {
	return &traceBuffer{rt: rt, taskID: taskID, workID: workID, intentID: intentID, role: role}
}

func (b *traceBuffer) add(ev harness.Event) {
	switch ev.Kind {
	case harness.KindThinking:
		b.thinking.WriteString(ev.Text)
	case harness.KindText:
		b.text.WriteString(ev.Text)
	case harness.KindToolUse:
		b.flush()
		if ev.ToolUse == nil {
			return
		}
		input := string(ev.ToolUse.Input)
		b.rt.emit(domain.Activity{
			TaskID: b.taskID, WorkID: b.workID, IntentID: b.intentID, Role: b.role,
			Kind: "tool_use", Tool: ev.ToolUse.Name, ToolUseID: ev.ToolUse.ID,
			Summary: ev.ToolUse.Name + " " + truncate(input, 140), Detail: input,
		})
	case harness.KindToolResult:
		b.flush()
		if ev.ToolResult == nil {
			return
		}
		detail, _ := json.Marshal(ev.ToolResult)
		b.rt.emit(domain.Activity{
			TaskID: b.taskID, WorkID: b.workID, IntentID: b.intentID, Role: b.role,
			Kind: "tool_result", ToolUseID: ev.ToolResult.ToolUseID,
			Summary: truncate(string(detail), 180), Detail: string(detail),
		})
	case harness.KindUsage:
		b.flush()
		if ev.Usage == nil {
			return
		}
		detail, _ := json.Marshal(ev.Usage)
		b.rt.emit(domain.Activity{
			TaskID: b.taskID, WorkID: b.workID, IntentID: b.intentID, Role: b.role,
			Kind: "usage", Summary: string(detail), Detail: string(detail),
		})
	case harness.KindResult:
		b.flush()
		summary := "completed"
		if ev.Terminal != nil {
			summary = string(ev.Terminal.Reason)
			if ev.Terminal.Err != nil {
				summary += ": " + ev.Terminal.Err.Error()
			}
		}
		b.rt.emit(domain.Activity{
			TaskID: b.taskID, WorkID: b.workID, IntentID: b.intentID, Role: b.role,
			Kind: "result", Summary: summary,
		})
	}
}

func (b *traceBuffer) flush() {
	if b.thinking.Len() > 0 {
		text := b.thinking.String()
		b.rt.emit(domain.Activity{
			TaskID: b.taskID, WorkID: b.workID, IntentID: b.intentID, Role: b.role,
			Kind: "thinking", Summary: truncate(text, 180), Detail: text,
		})
		b.thinking.Reset()
	}
	if b.text.Len() > 0 {
		text := b.text.String()
		b.rt.emit(domain.Activity{
			TaskID: b.taskID, WorkID: b.workID, IntentID: b.intentID, Role: b.role,
			Kind: "assistant_text", Summary: truncate(text, 180), Detail: text,
		})
		b.text.Reset()
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func proposeIntentsTool(cap *plannerCapture) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "propose_intents",
		Description: plannerToolDescription,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"intents": map[string]any{
					"type":  "array",
					"items": intentItemSchema(),
				},
			},
			"required": []any{"intents"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Intents []domain.IntentSpec `json:"intents"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid intents: " + err.Error()), nil
			}
			cap.Called = true
			for i := range in.Intents {
				normalizeIntentSpec(&in.Intents[i])
			}
			cap.Intents = in.Intents
			return tool.Text(fmt.Sprintf("accepted %d intents", len(in.Intents))), nil
		},
	})
}

func proposeGoalMetTool(cap *plannerCapture) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "propose_goal_met",
		Description: "Propose that the goal is achieved. Provide evidence node ids (fact/finding ids). Only call when the evidence is concrete.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id":      map[string]any{"type": "integer"},
				"evidence_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"reason":       map[string]any{"type": "string"},
			},
			"required": []any{"evidence_ids", "reason"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				GoalID      int64   `json:"goal_id"`
				EvidenceIDs []int64 `json:"evidence_ids"`
				Reason      string  `json:"reason"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid goal met proposal: " + err.Error()), nil
			}
			cap.GoalMet = &domain.GoalMetProposal{GoalID: in.GoalID, EvidenceIDs: in.EvidenceIDs, Reason: in.Reason}
			return tool.Text(fmt.Sprintf("accepted goal met proposal with %d evidence ids", len(in.EvidenceIDs))), nil
		},
	})
}

func normalizeIntentSpec(spec *domain.IntentSpec) {
	switch strings.ToLower(strings.TrimSpace(string(spec.ExecutionMode))) {
	case "shard":
		spec.ExecutionMode = domain.ExecutionShard
	case "swarm":
		spec.ExecutionMode = domain.ExecutionSwarm
		if spec.Budget == nil {
			spec.Budget = map[string]any{}
		}
		// Lane planning belongs exclusively to swarm_lead. Never accept
		// planner-authored strategies, and never let a model-inserted
		// lane_planning value bypass the lead call.
		delete(spec.Budget, "strategies")
		delete(spec.Budget, "lane_briefs")
		spec.Budget["lane_planning"] = "lead"
	default:
		spec.ExecutionMode = domain.ExecutionSingle
	}
	switch strings.ToLower(strings.TrimSpace(string(spec.Risk))) {
	case "medium", "moderate":
		spec.Risk = domain.RiskMedium
	case "high":
		spec.Risk = domain.RiskHigh
	case "critical", "severe":
		spec.Risk = domain.RiskCritical
	default:
		spec.Risk = domain.RiskLow
	}
	if spec.Priority < 0 {
		spec.Priority = 0
	}
	if spec.Priority > 10 {
		spec.Priority = 10
	}
	var actions []string
	for _, a := range spec.DangerousActions {
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "delete", "modify", "credential_access":
			actions = append(actions, strings.ToLower(strings.TrimSpace(a)))
		}
	}
	spec.DangerousActions = actions
}

func intentItemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":           map[string]any{"type": "string"},
			"target_key":        map[string]any{"type": "string"},
			"strategy_tag":      map[string]any{"type": "string"},
			"expected_flags":    map[string]any{"type": "integer"},
			"close_target":      map[string]any{"type": "boolean"},
			"rationale":         map[string]any{"type": "string"},
			"goal_ids":          map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"parent_fact_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"asset_ids":         map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"priority":          map[string]any{"type": "integer"},
			"risk":              map[string]any{"type": "string", "enum": []any{"low", "medium", "high", "critical"}},
			"dangerous_actions": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"delete", "modify", "credential_access"}}},
			"execution_mode":    map[string]any{"type": "string", "enum": []any{"single", "shard", "swarm"}},
			"budget":            map[string]any{"type": "object"},
			"required_tools":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"expected_outputs":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []any{"summary", "rationale", "priority", "risk", "execution_mode"},
	}
}

func recordAssetsTool(cap *workerCapture) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "record_assets",
		Description: "Record raw assets. The host canonicalizes and deduplicates them.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"assets": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type":     map[string]any{"type": "string"},
							"value":    map[string]any{"type": "string"},
							"metadata": map[string]any{"type": "object"},
						},
						"required": []any{"type", "value"},
					},
				},
			},
			"required": []any{"assets"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Assets []domain.AssetSpec `json:"assets"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid assets: " + err.Error()), nil
			}
			cap.Result.Assets = append(cap.Result.Assets, in.Assets...)
			return tool.Text(fmt.Sprintf("recorded %d assets", len(in.Assets))), nil
		},
	})
}

func recordFactsTool(cap *workerCapture) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "record_facts",
		Description: "Record facts with evidence and observed/inferred confidence.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"facts": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"summary":    map[string]any{"type": "string"},
							"detail":     map[string]any{"type": "string"},
							"evidence":   map[string]any{"type": "string"},
							"confidence": map[string]any{"type": "string"},
							"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
						},
						"required": []any{"summary", "evidence", "confidence"},
					},
				},
			},
			"required": []any{"facts"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Facts []domain.FactSpec `json:"facts"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid facts: " + err.Error()), nil
			}
			cap.Result.Facts = append(cap.Result.Facts, in.Facts...)
			return tool.Text(fmt.Sprintf("recorded %d facts", len(in.Facts))), nil
		},
	})
}

func reportFindingsTool(cap *workerCapture) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "report_findings",
		Description: "Report confirmed findings with evidence.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"findings": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":       map[string]any{"type": "string"},
							"severity":    map[string]any{"type": "string"},
							"evidence":    map[string]any{"type": "string"},
							"asset_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
							"subject_key": map[string]any{"type": "string"},
							"case_key":    map[string]any{"type": "string"},
							"observation": map[string]any{"type": "string", "enum": []any{"observed", "not_observed", "unknown"}},
						},
						"required": []any{"title", "severity", "evidence"},
					},
				},
			},
			"required": []any{"findings"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Findings []domain.FindingSpec `json:"findings"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid findings: " + err.Error()), nil
			}
			cap.Result.Findings = append(cap.Result.Findings, in.Findings...)
			return tool.Text(fmt.Sprintf("recorded %d findings", len(in.Findings))), nil
		},
	})
}

func attachEvidenceTool(cap *workerCapture) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "attach_evidence",
		Description: "Attach scripts, screenshots, traffic or output files.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"evidence": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type":  map[string]any{"type": "string"},
							"path":  map[string]any{"type": "string"},
							"hash":  map[string]any{"type": "string"},
							"bytes": map[string]any{"type": "integer"},
						},
						"required": []any{"type", "path"},
					},
				},
			},
			"required": []any{"evidence"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Evidence []domain.EvidenceSpec `json:"evidence"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid evidence: " + err.Error()), nil
			}
			cap.Result.Evidence = append(cap.Result.Evidence, in.Evidence...)
			return tool.Text(fmt.Sprintf("recorded %d evidence refs", len(in.Evidence))), nil
		},
	})
}

func finishTool(cap *workerCapture) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "finish",
		Description: workerFinishDescription,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string"},
				"error":  map[string]any{"type": "string"},
			},
			"required": []any{"status"},
		},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, input json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var in struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return tool.Errorf("invalid finish: " + err.Error()), nil
			}
			if in.Status == "" {
				in.Status = "done"
			}
			cap.Result.Status = in.Status
			cap.Result.Error = in.Error
			cap.Finished = true
			return tool.Text("finished"), nil
		},
	})
}

// Lead asks the swarm_lead role for concrete, distinct lane briefs. It is used
// only when the planner did not provide explicit strategies.
func (r *NormaRuntime) Lead(ctx context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error) {
	if req == nil || req.Intent == nil || req.Task == nil || req.Goal == nil {
		return nil, fmt.Errorf("norma lead: incomplete request")
	}
	lanes := req.Lanes
	if lanes < 2 {
		lanes = 2
	}
	facts, _ := json.MarshalIndent(req.Facts, "", "  ")
	constraints, _ := json.MarshalIndent(req.Constraints, "", "  ")
	existing, _ := json.MarshalIndent(req.Existing, "", "  ")
	hints, _ := json.MarshalIndent(req.Hints, "", "  ")
	prompt := fmt.Sprintf(`Intent:
summary: %s
rationale: %s
lanes: %d

Goal: %s

Facts:
%s

Constraints:
%s

Planner strategy hints (advisory only; you decide the final lane plan):
%s

Existing valid lane plans (empty entries must be filled; preserve valid entries):
%s

Produce exactly %d distinct lane plans for this same intent. Each lane must use a genuinely different method, target focus, and success signal. Return ONLY JSON:
{"lanes":[{"name":"short-name","brief":"concrete method and success signal"}],"notes":"optional"}`,
		req.Intent.Summary, req.Intent.Rationale, lanes, req.Goal.Text, facts, constraints, hints, existing, lanes)
	reqLLM := llm.CompletionRequest{
		System:    []string{"You are swarm_lead. You only plan independent lanes for one Intent; you never execute tools or generate new Intents."},
		Messages:  []llm.Message{llm.UserText(prompt)},
		MaxTokens: 3000,
	}
	msg, _, _, err := r.recordedProvider(req.Task.ID, 0, "swarm_lead").Complete(ctx, reqLLM)
	if err != nil {
		return nil, fmt.Errorf("norma lead: %w", err)
	}
	text := strings.TrimSpace(msg.Text())
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var out domain.LeadResponse
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("norma lead: parse JSON: %w", err)
	}
	for i := range out.Lanes {
		out.Lanes[i].Name = strings.TrimSpace(out.Lanes[i].Name)
		out.Lanes[i].Brief = strings.TrimSpace(out.Lanes[i].Brief)
		if out.Lanes[i].Name == "" {
			out.Lanes[i].Name = fmt.Sprintf("lead-%d", i+1)
		}
		if out.Lanes[i].Brief == "" {
			out.Lanes[i].Brief = out.Lanes[i].Name
		}
	}
	if len(out.Lanes) < lanes {
		return nil, fmt.Errorf("norma lead: got %d lanes, want %d", len(out.Lanes), lanes)
	}
	out.Lanes = out.Lanes[:lanes]
	r.emit(domain.Activity{TaskID: req.Task.ID, Role: "swarm_lead", Kind: "lane_plan", Summary: fmt.Sprintf("planned %d lanes", len(out.Lanes))})
	return &out, nil
}

func (r *NormaRuntime) ReviewConflicts(ctx context.Context, req *domain.ConflictReviewRequest) (*domain.ConflictReviewResponse, error) {
	if req == nil || req.Task == nil || req.Intent == nil || len(req.Conflicts) == 0 {
		return &domain.ConflictReviewResponse{}, nil
	}
	conflicts, _ := json.MarshalIndent(req.Conflicts, "", "  ")
	prompt := fmt.Sprintf(`Intent: %s
Rationale: %s

Conflicts:
%s

For each conflict choose exactly one:
- keep_all: different flags/claims are legitimate; keep them
- quarantine_all: unresolved; keep as candidate but do not submit
- needs_human: pentest/action or genuinely ambiguous
Never invent facts, never change quorum, never submit.
Return ONLY JSON:
{"decisions":[{"subject_hash":"...","decision":"keep_all|quarantine_all|needs_human","reason":"..."}]}`,
		req.Intent.Summary, req.Intent.Rationale, string(conflicts))
	msg, _, _, err := r.recordedProvider(req.Task.ID, 0, "swarm_lead_conflict").Complete(ctx, llm.CompletionRequest{
		System:    []string{"You are swarm_lead conflict reviewer. You only advise on conflicts; you cannot confirm flags or execute actions."},
		Messages:  []llm.Message{llm.UserText(prompt)},
		MaxTokens: 2000,
	})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(msg.Text())
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var out domain.ConflictReviewResponse
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, err
	}
	for _, d := range out.Decisions {
		switch d.Decision {
		case "keep_all", "quarantine_all", "needs_human":
		default:
			return nil, fmt.Errorf("swarm_lead conflict: invalid decision %q", d.Decision)
		}
	}
	return &out, nil
}

func intentAllowsNoContainer(intent *domain.Intent) bool {
	if intent == nil || intent.Budget == nil {
		return false
	}
	v, ok := intent.Budget["container_required"].(bool)
	return ok && !v
}
