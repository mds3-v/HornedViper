package domain

import (
	"context"
	"time"
)

// TaskMode selects approval policy and domain adapters.
type TaskMode string

const (
	ModeCTF     TaskMode = "ctf"
	ModePentest TaskMode = "pentest"
)

type TaskStatus string

const (
	TaskCreated   TaskStatus = "created"
	TaskPlanning  TaskStatus = "planning"
	TaskRunning   TaskStatus = "running"
	TaskSettling  TaskStatus = "settling"
	TaskCompleted TaskStatus = "completed"
	TaskPaused    TaskStatus = "paused"
	TaskCancelled TaskStatus = "cancelled"
	TaskFailed    TaskStatus = "failed"
)

type GoalState string

const (
	GoalOpen       GoalState = "open"
	GoalInProgress GoalState = "in_progress"
	GoalPaused     GoalState = "paused"
	GoalMet        GoalState = "met"
	GoalAbandoned  GoalState = "abandoned"
)

type IntentState string

const (
	IntentProposed  IntentState = "proposed"
	IntentApproved  IntentState = "approved"
	IntentQueued    IntentState = "queued"
	IntentDeferred  IntentState = "deferred"
	IntentClaimed   IntentState = "claimed"
	IntentRunning   IntentState = "running"
	IntentSucceeded IntentState = "succeeded"
	IntentFailed    IntentState = "failed"
	IntentBlocked   IntentState = "blocked"
	IntentCancelled IntentState = "cancelled"
)

type WorkState string

const (
	WorkQueued    WorkState = "queued"
	WorkLeased    WorkState = "leased"
	WorkRunning   WorkState = "running"
	WorkSettling  WorkState = "settling"
	WorkSucceeded WorkState = "succeeded"
	WorkFailed    WorkState = "failed"
	WorkTimedOut  WorkState = "timed_out"
	WorkCancelled WorkState = "cancelled"
	WorkLost      WorkState = "lost"
)

type Risk string

const (
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

type ExecutionMode string

const (
	ExecutionSingle ExecutionMode = "single"
	ExecutionShard  ExecutionMode = "shard"
	ExecutionSwarm  ExecutionMode = "swarm"
)

type Confidence string

const (
	ConfidenceObserved Confidence = "observed"
	ConfidenceInferred Confidence = "inferred"
)

type ApprovalDecision string

const (
	ApprovalPending  ApprovalDecision = "pending"
	ApprovalApproved ApprovalDecision = "approved"
	ApprovalRejected ApprovalDecision = "rejected"
	ApprovalExpired  ApprovalDecision = "expired"
)

type EventType string

const (
	EventTaskStarted     EventType = "task.started"
	EventGoalSet         EventType = "goal.set"
	EventHintAdded       EventType = "hint.added"
	EventConstraintAdded EventType = "constraint.added"
	EventIntentProposed  EventType = "intent.proposed"
	EventIntentApproved  EventType = "intent.approved"
	EventIntentRejected  EventType = "intent.rejected"
	EventIntentDeferred  EventType = "intent.deferred"
	EventWorkClaimed     EventType = "work.claimed"
	EventWorkFinished    EventType = "work.finished"
	EventWorkFailed      EventType = "work.failed"
	EventWorkTimedOut    EventType = "work.timed_out"
	EventFactRecorded    EventType = "fact.recorded"
	EventFindingReported EventType = "finding.reported"
	EventAssetRecorded   EventType = "asset.recorded"
	EventGoalMetProposed EventType = "goal.met_proposed"
	EventGoalAccepted    EventType = "goal.accepted"
	EventGoalRejected    EventType = "goal.rejected"
	EventGoalAbandoned   EventType = "goal.abandoned"
	EventTaskResumed     EventType = "task.resumed"
	EventTaskPaused      EventType = "task.paused"
	EventTaskStopped     EventType = "task.stopped"
	EventWorkersIdle     EventType = "workers.idle"
	EventPlannerNoIntent EventType = "planner.no_intent"
	EventFlagSubmitted   EventType = "flag.submitted"
	EventFlagAccepted    EventType = "flag.accepted"
	EventFlagRejected    EventType = "flag.rejected"
	EventSwarmLaneDone   EventType = "swarm.lane_done"
	EventSwarmMerged     EventType = "swarm.merged"
	EventSwarmFailed     EventType = "swarm.failed"
)

// Task is one engagement (a CTF, a pentest, a research run).
type Task struct {
	ID        int64          `json:"id"`
	Title     string         `json:"title"`
	Mode      TaskMode       `json:"mode"`
	Status    TaskStatus     `json:"status"`
	GoalID    int64          `json:"goal_id"`
	CreatedAt time.Time      `json:"created_at"`
	StartedAt *time.Time     `json:"started_at,omitempty"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
	Budget    map[string]any `json:"budget,omitempty"`
}

type Goal struct {
	ID         int64      `json:"id"`
	TaskID     int64      `json:"task_id"`
	Text       string     `json:"text"`
	Acceptance []string   `json:"acceptance,omitempty"`
	State      GoalState  `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	MetAt      *time.Time `json:"met_at,omitempty"`
	MetBy      string     `json:"met_by,omitempty"`
}

type Intent struct {
	ID               int64          `json:"id"`
	TargetKey        string         `json:"target_key,omitempty"`
	StrategyTag      string         `json:"strategy_tag,omitempty"`
	ExpectedFlags    int            `json:"expected_flags,omitempty"`
	CloseTarget      bool           `json:"close_target,omitempty"`
	TaskID           int64          `json:"task_id"`
	GoalIDs          []int64        `json:"goal_ids"`
	Summary          string         `json:"summary"`
	Rationale        string         `json:"rationale"`
	ParentFactIDs    []int64        `json:"parent_fact_ids,omitempty"`
	AssetIDs         []int64        `json:"asset_ids,omitempty"`
	Priority         int            `json:"priority"`
	Risk             Risk           `json:"risk"`
	DangerousActions []string       `json:"dangerous_actions,omitempty"`
	ExecutionMode    ExecutionMode  `json:"execution_mode"`
	Budget           map[string]any `json:"budget,omitempty"`
	RequiredTools    []string       `json:"required_tools,omitempty"`
	ExpectedOutputs  []string       `json:"expected_outputs,omitempty"`
	State            IntentState    `json:"state"`
	ApprovalID       int64          `json:"approval_id,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

type Work struct {
	ID             int64      `json:"id"`
	TaskID         int64      `json:"task_id"`
	IntentID       int64      `json:"intent_id"`
	WorkerID       string     `json:"worker_id"`
	LaneID         string     `json:"lane_id,omitempty"`
	State          WorkState  `json:"state"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	StopReason     string     `json:"stop_reason,omitempty"`
	TokensIn       int64      `json:"tokens_in,omitempty"`
	TokensOut      int64      `json:"tokens_out,omitempty"`
}

type Fact struct {
	ID         int64      `json:"id"`
	TaskID     int64      `json:"task_id"`
	IntentID   int64      `json:"intent_id"`
	Summary    string     `json:"summary"`
	Detail     string     `json:"detail"`
	Evidence   string     `json:"evidence"`
	Confidence Confidence `json:"confidence"`
	AssetIDs   []int64    `json:"asset_ids,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type FindingGroup struct {
	TaskID int64     `json:"task_id"`
	Count  int       `json:"count"`
	LastAt time.Time `json:"last_at"`
}

type Finding struct {
	ID          int64     `json:"id"`
	TaskID      int64     `json:"task_id"`
	IntentID    int64     `json:"intent_id"`
	Title       string    `json:"title"`
	Severity    string    `json:"severity"`
	Evidence    string    `json:"evidence"`
	AssetIDs    []int64   `json:"asset_ids,omitempty"`
	SubjectKey  string    `json:"subject_key,omitempty"`
	CaseKey     string    `json:"case_key,omitempty"`
	Observation string    `json:"observation,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type Asset struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type"`
	CanonicalKey string    `json:"canonical_key"`
	Value        string    `json:"value"`
	ParentID     int64     `json:"parent_id,omitempty"`
	ScopeStatus  string    `json:"scope_status"`
	Source       string    `json:"source"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

// LogEntry is one persisted backend log line.
type LogEntry struct {
	ID        int64     `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// TokenUsage aggregates model token accounting for the dashboard.
type TokenUsage struct {
	Calls      int `json:"calls"`
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// Traffic is one raw HTTP request/response pair captured from a worker.
type TrafficGroup struct {
	TaskID         int64     `json:"task_id"`
	ConversationID int64     `json:"conversation_id"`
	Count          int       `json:"count"`
	LastAt         time.Time `json:"last_at"`
}

type Traffic struct {
	ID             int64     `json:"id"`
	TaskID         int64     `json:"task_id"`
	ConversationID int64     `json:"conversation_id"`
	WorkID         int64     `json:"work_id"`
	IntentID       int64     `json:"intent_id"`
	Method         string    `json:"method"`
	Host           string    `json:"host"`
	URL            string    `json:"url"`
	StatusCode     int       `json:"status_code"`
	ContentType    string    `json:"content_type"`
	RequestRaw     string    `json:"request_raw"`
	ResponseRaw    string    `json:"response_raw"`
	RequestSize    int       `json:"request_size"`
	ResponseSize   int       `json:"response_size"`
	DurationMS     int64     `json:"duration_ms"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Evidence struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	WorkID    int64     `json:"work_id"`
	Type      string    `json:"type"`
	Path      string    `json:"path"`
	Hash      string    `json:"hash,omitempty"`
	Bytes     int64     `json:"bytes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Event struct {
	ID          int64          `json:"id"`
	TaskID      int64          `json:"task_id"`
	Type        EventType      `json:"type"`
	ActorType   string         `json:"actor_type"`
	ActorID     string         `json:"actor_id"`
	SubjectType string         `json:"subject_type,omitempty"`
	SubjectID   int64          `json:"subject_id,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type Approval struct {
	ID               int64            `json:"id"`
	TaskID           int64            `json:"task_id"`
	IntentID         int64            `json:"intent_id"`
	Risk             Risk             `json:"risk"`
	DangerousActions []string         `json:"dangerous_actions,omitempty"`
	Decision         ApprovalDecision `json:"decision"`
	DecidedBy        string           `json:"decided_by,omitempty"`
	Reason           string           `json:"reason,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	DecidedAt        *time.Time       `json:"decided_at,omitempty"`
}

type Activity struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	WorkID    int64     `json:"work_id,omitempty"`
	IntentID  int64     `json:"intent_id,omitempty"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Tool      string    `json:"tool,omitempty"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	LLMCallID int64     `json:"llm_call_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type LLMGroup struct {
	TaskID         int64     `json:"task_id"`
	ConversationID int64     `json:"conversation_id"`
	Count          int       `json:"count"`
	LastAt         time.Time `json:"last_at"`
}

type LLMCall struct {
	ID             int64     `json:"id"`
	TaskID         int64     `json:"task_id"`
	ConversationID int64     `json:"conversation_id,omitempty"`
	WorkID         int64     `json:"work_id,omitempty"`
	Role           string    `json:"role"`
	SessionID      string    `json:"session_id"`
	Turn           int       `json:"turn"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	RequestJSON    string    `json:"request_json"`
	ResponseJSON   string    `json:"response_json"`
	Thinking       string    `json:"thinking,omitempty"`
	ToolCallsJSON  string    `json:"tool_calls_json,omitempty"`
	UsageJSON      string    `json:"usage_json,omitempty"`
	LatencyMS      int64     `json:"latency_ms"`
	Error          string    `json:"error,omitempty"`
	RequestHash    string    `json:"request_hash,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Constraint is an allow/deny operation boundary for a task.
// Hint is a human intervention injected into the planner.
type Hint struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Attachment is one uploaded file linked to a task or chat message.
type Attachment struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Kind        string `json:"kind,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// Conversation is a standalone direct-chat session, independent of tasks.
type Conversation struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	ReasoningLevel string    `json:"reasoning_level"`
	PermissionMode string    `json:"permission_mode"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ChatMessage is one human/mainagent conversation turn.
type ChatMessage struct {
	ID          int64        `json:"id"`
	TaskID      int64        `json:"task_id"`
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	Thinking    string       `json:"thinking,omitempty"`
	Status      string       `json:"status"`
	Error       string       `json:"error,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ThinkingMS  int64        `json:"thinking_ms,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

// Agent is a configurable role definition.
type Agent struct {
	ID           int64     `json:"id"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	SystemPrompt string    `json:"system_prompt"`
	ToolsJSON    string    `json:"tools_json"`
	Model        string    `json:"model"`
	Builtin      bool      `json:"builtin"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// InterceptRule is a tool-call guard rule.
type InterceptRule struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Priority  int       `json:"priority"`
	Target    string    `json:"target"`
	MatchType string    `json:"match_type"`
	Pattern   string    `json:"pattern"`
	Mode      string    `json:"mode"`
	Enabled   bool      `json:"enabled"`
	Builtin   bool      `json:"builtin"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Constraint struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// TodoItem is a planner scratchpad entry that survives wakes.
type TodoItem struct {
	ID      int64  `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// IntentSpec is what the planner emits. It is validated before persistence.
type IntentSpec struct {
	Summary          string         `json:"summary"`
	TargetKey        string         `json:"target_key,omitempty"`
	StrategyTag      string         `json:"strategy_tag,omitempty"`
	ExpectedFlags    int            `json:"expected_flags,omitempty"`
	CloseTarget      bool           `json:"close_target,omitempty"`
	Rationale        string         `json:"rationale"`
	GoalIDs          []int64        `json:"goal_ids"`
	ParentFactIDs    []int64        `json:"parent_fact_ids,omitempty"`
	AssetIDs         []int64        `json:"asset_ids,omitempty"`
	Priority         int            `json:"priority"`
	Risk             Risk           `json:"risk"`
	DangerousActions []string       `json:"dangerous_actions,omitempty"`
	ExecutionMode    ExecutionMode  `json:"execution_mode"`
	Budget           map[string]any `json:"budget,omitempty"`
	RequiredTools    []string       `json:"required_tools,omitempty"`
	ExpectedOutputs  []string       `json:"expected_outputs,omitempty"`
}

type GoalMetProposal struct {
	GoalID      int64   `json:"goal_id"`
	EvidenceIDs []int64 `json:"evidence_ids"`
	Reason      string  `json:"reason"`
}

type PlannerResponse struct {
	Intents []IntentSpec     `json:"intents"`
	GoalMet *GoalMetProposal `json:"goal_met,omitempty"`
}

// AssetSpec is a raw asset submitted by a worker. The store canonicalizes it.
type AssetSpec struct {
	Type     string         `json:"type"`
	Value    string         `json:"value"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type FactSpec struct {
	Summary    string     `json:"summary"`
	Detail     string     `json:"detail"`
	Evidence   string     `json:"evidence"`
	Confidence Confidence `json:"confidence"`
	AssetIDs   []int64    `json:"asset_ids,omitempty"`
}

type FindingSpec struct {
	Title       string  `json:"title"`
	Severity    string  `json:"severity"`
	Evidence    string  `json:"evidence"`
	AssetIDs    []int64 `json:"asset_ids,omitempty"`
	SubjectKey  string  `json:"subject_key,omitempty"`
	CaseKey     string  `json:"case_key,omitempty"`
	Observation string  `json:"observation,omitempty"` // observed / not_observed / unknown
}

type EvidenceSpec struct {
	Type  string `json:"type"`
	Path  string `json:"path"`
	Hash  string `json:"hash,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
}

// WorkResult is the only output a worker returns: assets, facts, findings, evidence.
type WorkResult struct {
	Partial  bool           `json:"partial,omitempty"`
	Assets   []AssetSpec    `json:"assets,omitempty"`
	Facts    []FactSpec     `json:"facts,omitempty"`
	Findings []FindingSpec  `json:"findings,omitempty"`
	Evidence []EvidenceSpec `json:"evidence,omitempty"`
	Status   string         `json:"status"`
	Error    string         `json:"error,omitempty"`
}

type Conflict struct {
	ID          int64    `json:"id"`
	TaskID      int64    `json:"task_id"`
	IntentID    int64    `json:"intent_id"`
	Type        string   `json:"type"`
	Subject     string   `json:"subject"`
	SubjectHash string   `json:"subject_hash"`
	LaneIDs     []string `json:"lane_ids,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Status      string   `json:"status"`
	Decision    string   `json:"decision"`
	Reason      string   `json:"reason"`
}

type ConflictReviewRequest struct {
	Task      *Task
	Intent    *Intent
	Conflicts []Conflict
}

type ConflictDecision struct {
	SubjectHash string `json:"subject_hash"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason"`
}

type ConflictReviewResponse struct {
	Decisions []ConflictDecision `json:"decisions"`
}

type QuarantineItem struct {
	ID             int64      `json:"id"`
	TaskID         int64      `json:"task_id"`
	IntentID       int64      `json:"intent_id"`
	WorkID         int64      `json:"work_id"`
	LaneID         string     `json:"lane_id"`
	SourceKind     string     `json:"source_kind"`
	Reason         string     `json:"reason"`
	Summary        string     `json:"summary"`
	PayloadJSON    string     `json:"payload_json"`
	ArtifactPath   string     `json:"artifact_path,omitempty"`
	PayloadHash    string     `json:"payload_hash"`
	CandidateFlags []string   `json:"candidate_flags,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
	ReviewedBy     string     `json:"reviewed_by,omitempty"`
}

type QuarantineDigest struct {
	ID             int64    `json:"id"`
	LaneID         string   `json:"lane_id"`
	SourceKind     string   `json:"source_kind"`
	Reason         string   `json:"reason"`
	Summary        string   `json:"summary"`
	CandidateFlags []string `json:"candidate_flags,omitempty"`
	Status         string   `json:"status"`
}

type PlannerPacket struct {
	Task           *Task              `json:"task"`
	Goal           *Goal              `json:"goal"`
	Constraints    []Constraint       `json:"constraints,omitempty"`
	NewFacts       []Fact             `json:"new_facts,omitempty"`
	NewFindings    []Finding          `json:"new_findings,omitempty"`
	Hints          []Hint             `json:"hints,omitempty"`
	OpenIntents    []Intent           `json:"open_intents,omitempty"`
	RunningWorks   []Work             `json:"running_works,omitempty"`
	Todo           []TodoItem         `json:"todo,omitempty"`
	Quarantine     []QuarantineDigest `json:"quarantine,omitempty"`
	Trigger        *Event             `json:"trigger,omitempty"`
	Budget         map[string]any     `json:"budget,omitempty"`
	TargetStates   []TargetStateView  `json:"target_states,omitempty"`
	RecentFailures []WorkFailureView  `json:"recent_failures,omitempty"`
	ActiveWorkers  int                `json:"active_workers"`
	IdleWorkers    int                `json:"idle_workers"`
	MaxWorkers     int                `json:"max_workers"`
}

type WorkFailureView struct {
	WorkID   int64  `json:"work_id"`
	IntentID int64  `json:"intent_id"`
	Target   string `json:"target,omitempty"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// TargetStateView is the Planner-visible retry state for one target. It is
// intentionally compact: the Planner must see cooldown/exhaustion and the
// failure reason before proposing another strategy for the same target.
type TargetStateView struct {
	Target            string     `json:"target"`
	Attempts          int        `json:"attempts"`
	ProgressCount     int        `json:"progress_count,omitempty"`
	CooldownUntil     *time.Time `json:"cooldown_until,omitempty"`
	Exhausted         bool       `json:"exhausted,omitempty"`
	Solved            bool       `json:"solved,omitempty"`
	CloseRequested    bool       `json:"close_requested,omitempty"`
	LastStrategy      string     `json:"last_strategy,omitempty"`
	ExpectedFlags     int        `json:"expected_flags,omitempty"`
	AcceptedFlags     int        `json:"accepted_flags,omitempty"`
	LastFailureReason string     `json:"last_failure_reason,omitempty"`
	ExhaustedReason   string     `json:"exhausted_reason,omitempty"`
}

type LanePlan struct {
	Name  string `json:"name"`
	Brief string `json:"brief"`
}

type LeadRequest struct {
	Task        *Task
	Goal        *Goal
	Intent      *Intent
	Lanes       int
	Facts       []Fact
	Constraints []Constraint
	Existing    []LanePlan
	Hints       []string
}

type LeadResponse struct {
	Lanes []LanePlan `json:"lanes"`
	Notes string     `json:"notes,omitempty"`
}

type PlannerRequest struct {
	Task   *Task
	Goal   *Goal
	Packet *PlannerPacket
}

type WorkerRequest struct {
	Task         *Task
	Goal         *Goal
	Intent       *Intent
	Work         *Work
	Facts        []Fact
	Constraints  []Constraint
	Failures     []WorkFailureView
	LaneStrategy string
	LaneBrief    string
}

// SubmitResult is the outcome of one flag submission.
type SubmitResult struct {
	Accepted bool   `json:"accepted"`
	Flag     string `json:"flag,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Submitter is a user-provided flag submission path. It is optional and only
// used by ctf-mode tasks.
type Submitter interface {
	Enabled() bool
	Submit(ctx context.Context, task *Task, finding Finding) (SubmitResult, error)
}

// PublicTask returns a shallow task copy with secret material removed from the
// budget. Use it for API responses and model packets; the submitter still loads
// the raw task from the store.
func PublicTask(t *Task) *Task {
	if t == nil {
		return nil
	}
	cp := *t
	cp.Budget = RedactBudget(t.Budget)
	return &cp
}

func RedactBudget(b map[string]any) map[string]any {
	if b == nil {
		return nil
	}
	out := make(map[string]any, len(b))
	for k, v := range b {
		if k == "ctf" {
			if m, ok := v.(map[string]any); ok {
				cm := make(map[string]any, len(m))
				for mk, mv := range m {
					if mk == "token" {
						cm[mk] = "***"
					} else {
						cm[mk] = mv
					}
				}
				out[k] = cm
				continue
			}
		}
		out[k] = v
	}
	return out
}

// TaskFlag records one flag recovered for a task and the challenge it belongs
// to. Source is manual, finding or submission.
type TaskFlag struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	Challenge string    `json:"challenge"`
	Flag      string    `json:"flag"`
	FlagHash  string    `json:"flag_hash"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	FindingID int64     `json:"finding_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
