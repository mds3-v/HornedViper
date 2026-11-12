package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"hornedviper/internal/domain"
	"hornedviper/internal/runtime"
	"hornedviper/internal/store"
)

// Config controls the engine.
type Config struct {
	MaxWorkers           int
	Lease                time.Duration
	Scheduler            SchedulerConfig
	AutoApproveDangerous bool // demo/testing only; real pentest leaves it pending
	PacketFacts          int
	PacketFindings       int
	Submitter            domain.Submitter // optional: ctf-mode flag submission
	DrainWindow          time.Duration    // wait for running lanes after quorum
	MaxContainers        int              // optional container admission slots
	PriorityAging        time.Duration    // age one priority point every interval
	WorkerTimeout        time.Duration    // per-Work hard execution limit
	IdleReportAfter      time.Duration    // report idle workers after this duration
	CloseChallenge       func(ctx context.Context, task *domain.Task, challenge string) error
	ReconcileChallenges  func(ctx context.Context, task *domain.Task) ([]string, error)
}

func (c *Config) defaults() {
	if c.MaxWorkers <= 0 {
		// 3 container-capable workers + 1 reserved fast worker.
		c.MaxWorkers = 4
	}
	if c.PriorityAging <= 0 {
		c.PriorityAging = 5 * time.Minute
	}
	if c.WorkerTimeout <= 0 {
		c.WorkerTimeout = 20 * time.Minute
	}
	if c.IdleReportAfter <= 0 {
		c.IdleReportAfter = 5 * time.Minute
	}
	if c.Lease <= 0 {
		c.Lease = 10 * time.Minute
	}
	if c.PacketFacts <= 0 {
		c.PacketFacts = 20
	}
	if c.PacketFindings <= 0 {
		c.PacketFindings = 10
	}
}

type workOutcome struct {
	work   *domain.Work
	intent *domain.Intent
	result *domain.WorkResult
	err    error
	fast   bool
}

// Engine runs one task's event-driven loop.
type Engine struct {
	store *store.Store
	rt    runtime.Runtime
	cfg   Config
	sched *Scheduler

	workDone     chan workOutcome
	active       int
	fastActive   int
	containers   int
	cancel       context.CancelFunc
	workMu       sync.Mutex
	workCancels  map[int64]context.CancelFunc
	submissionMu sync.Mutex

	lastFactID       int64
	lastFindingID    int64
	planningFailures int
	idleSince        time.Time
	lastIdleReport   time.Time
	lastActive       int
	targetStates     map[string]*targetState
	targetEvidence   map[string]map[string]bool
	factSemantics    map[string]bool
	drainC           chan int64
	drainScheduled   map[int64]bool
}

func New(st *store.Store, rt runtime.Runtime, cfg Config) *Engine {
	cfg.defaults()
	return &Engine{
		store: st, rt: rt, cfg: cfg,
		sched:          NewScheduler(cfg.Scheduler),
		workDone:       make(chan workOutcome, cfg.MaxWorkers),
		drainC:         make(chan int64, 1),
		drainScheduled: map[int64]bool{},
		workCancels:    map[int64]context.CancelFunc{},
		targetStates:   map[string]*targetState{},
		targetEvidence: map[string]map[string]bool{},
		factSemantics:  map[string]bool{},
	}
}

// Run drives a task until its goal is accepted or the context is cancelled.
func (e *Engine) Run(ctx context.Context, taskID int64) error {
	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	if task.Status == domain.TaskCreated {
		if err := e.store.SetTaskStatus(ctx, taskID, domain.TaskPlanning); err != nil {
			return err
		}
		if _, err := e.emit(ctx, taskID, domain.EventTaskStarted, "system", "engine", "task", taskID, nil); err != nil {
			return err
		}
		if err := e.store.SetTaskStatus(ctx, taskID, domain.TaskRunning); err != nil {
			return err
		}
	} else {
		if _, err := e.emit(ctx, taskID, domain.EventTaskResumed, "system", "engine", "task", taskID, nil); err != nil {
			return err
		}
	}

	goal, err := e.store.CurrentGoal(ctx, taskID)
	if err != nil {
		return err
	}
	if goal == nil {
		return fmt.Errorf("task %d has no goal", taskID)
	}
	e.reconcileChallenges(ctx, taskID, task)

	// Workers inherit this context. Cancelling it on any Run exit (goal met,
	// failure, pause/stop) prevents abandoned workers from staying "running".
	baseCtx := ctx
	if minutes := hardTaskMinutes(task); minutes > 0 {
		var timeoutCancel context.CancelFunc
		baseCtx, timeoutCancel = context.WithTimeout(ctx, time.Duration(minutes)*time.Minute)
		defer timeoutCancel()
	}
	runCtx, cancel := context.WithCancel(baseCtx)
	e.cancel = cancel
	defer func() {
		e.cancel = nil
		cancel()
	}()

	go e.sched.Run(runCtx)
	defer e.sched.Stop()
	// Re-dispatch periodically. A canceled/timed-out worker may have already
	// released its DB state without producing a workDone event in this engine
	// instance; without a reconciliation tick queued Intents can sit forever
	// while workers are idle.
	dispatchTick := time.NewTicker(5 * time.Second)
	defer dispatchTick.Stop()
	reconcileTick := time.NewTicker(30 * time.Second)
	defer reconcileTick.Stop()

	for {
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-dispatchTick.C:
			if task, _ := e.store.GetTask(runCtx, taskID); task != nil && task.Status == domain.TaskRunning {
				e.dispatch(runCtx, taskID)
				e.checkIdle(runCtx, taskID)
			}
		case <-reconcileTick.C:
			if task, _ := e.store.GetTask(runCtx, taskID); task != nil && task.Status == domain.TaskRunning {
				e.reconcileChallenges(runCtx, taskID, task)
				e.dispatch(runCtx, taskID)
			}
		case wake := <-e.sched.Wakes():
			done, err := e.onWake(runCtx, taskID, wake)
			e.sched.Ack()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case out := <-e.workDone:
			done, err := e.onWorkDone(runCtx, taskID, out)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case intentID := <-e.drainC:
			delete(e.drainScheduled, intentID)
			done, err := e.maybeMergeSwarm(runCtx, taskID, intentID, true)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}

func (e *Engine) onWake(ctx context.Context, taskID int64, wake Wake) (bool, error) {
	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	goal, err := e.store.CurrentGoal(ctx, taskID)
	if err != nil {
		return false, err
	}
	packet, maxFact, maxFinding, err := e.buildPacket(ctx, taskID, task, goal, wake)
	if err != nil {
		return false, err
	}
	resp, err := e.rt.Plan(ctx, &domain.PlannerRequest{Task: task, Goal: goal, Packet: packet})
	if err != nil {
		return false, fmt.Errorf("planner: %w", err)
	}
	e.lastFactID, e.lastFindingID = maxFact, maxFinding
	log.Printf("[engine] task %d planner wake events=%d high_signal=%v", taskID, len(wake.Events), wake.HighSignal)

	if resp.GoalMet != nil {
		return e.handleGoalMet(ctx, taskID, goal, resp.GoalMet)
	}
	if len(resp.Intents) == 0 && len(packet.OpenIntents) == 0 {
		if _, err := e.emit(ctx, taskID, domain.EventPlannerNoIntent, "planner", "planner", "goal", goal.ID, nil); err != nil {
			return false, err
		}
	}
	for _, spec := range resp.Intents {
		if err := e.createIntent(ctx, taskID, task, goal, spec); err != nil {
			return false, err
		}
	}
	e.dispatch(ctx, taskID)
	return false, nil
}

func (e *Engine) buildPacket(ctx context.Context, taskID int64, task *domain.Task, goal *domain.Goal, wake Wake) (*domain.PlannerPacket, int64, int64, error) {
	constraints, err := e.store.ListConstraints(ctx, taskID)
	if err != nil {
		return nil, 0, 0, err
	}
	todos, err := e.store.ListTodos(ctx, taskID)
	if err != nil {
		return nil, 0, 0, err
	}
	hints, err := e.store.ListHints(ctx, taskID, 50)
	if err != nil {
		return nil, 0, 0, err
	}
	allFacts, err := e.store.ListFacts(ctx, taskID, e.cfg.PacketFacts)
	if err != nil {
		return nil, 0, 0, err
	}
	allFindings, err := e.store.ListFindings(ctx, taskID, e.cfg.PacketFindings)
	if err != nil {
		return nil, 0, 0, err
	}
	var newFacts []domain.Fact
	var maxFact int64
	for _, f := range allFacts {
		if f.ID > maxFact {
			maxFact = f.ID
		}
		if f.ID > e.lastFactID {
			newFacts = append(newFacts, f)
		}
	}
	var newFindings []domain.Finding
	var maxFinding int64
	for _, f := range allFindings {
		if f.ID > maxFinding {
			maxFinding = f.ID
		}
		if f.ID > e.lastFindingID {
			newFindings = append(newFindings, f)
		}
	}
	works, err := e.store.ListWorks(ctx, taskID)
	if err != nil {
		return nil, 0, 0, err
	}
	var running []domain.Work
	for _, w := range works {
		if w.State == domain.WorkRunning || w.State == domain.WorkLeased {
			running = append(running, *w)
		}
	}
	allIntents, _ := e.store.ListIntents(ctx, taskID)
	openIntents, err := e.store.ListIntents(ctx, taskID,
		domain.IntentProposed, domain.IntentApproved, domain.IntentQueued, domain.IntentDeferred,
		domain.IntentClaimed, domain.IntentRunning)
	if err != nil {
		return nil, 0, 0, err
	}
	var open []domain.Intent
	for _, in := range openIntents {
		open = append(open, *in)
	}
	var trigger *domain.Event
	if len(wake.Events) > 0 {
		trigger = wake.Events[0]
	}
	safeTask := domain.PublicTask(task)
	quarantine, _ := e.store.QuarantineDigest(ctx, taskID, 5)
	return &domain.PlannerPacket{
		Task: safeTask, Goal: goal, Constraints: constraints,
		NewFacts: newFacts, NewFindings: newFindings, Hints: hints, OpenIntents: open, RunningWorks: running,
		Quarantine: quarantine, Todo: todos, Trigger: trigger, Budget: safeTask.Budget,
		TargetStates:   e.targetSnapshots(),
		RecentFailures: e.recentFailures(ctx, taskID, works, allIntents),
		ActiveWorkers:  e.active, IdleWorkers: maxInt(0, e.cfg.MaxWorkers-e.active), MaxWorkers: e.cfg.MaxWorkers,
	}, maxFact, maxFinding, nil
}

func (e *Engine) createIntent(ctx context.Context, taskID int64, task *domain.Task, goal *domain.Goal, spec domain.IntentSpec) error {
	if spec.ExecutionMode == "" {
		spec.ExecutionMode = domain.ExecutionSingle
	}
	if spec.TargetKey == "" {
		spec.TargetKey = intentTargetKey(spec.Summary)
	}
	if spec.StrategyTag == "" {
		spec.StrategyTag = strategyTag(spec)
	}
	if spec.CloseTarget {
		e.executeCloseIntent(ctx, taskID, task, goal, spec)
		return nil
	}
	internalClose := spec.Budget != nil && spec.Budget["internal_close"] == true
	if spec.TargetKey != "" && !internalClose {
		if done, _ := e.store.IsChallengeCompleted(ctx, taskID, spec.TargetKey); done {
			_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "completion-policy", "goal", goal.ID,
				map[string]any{"reason": "challenge already completed; no further work or flag submission", "target": spec.TargetKey})
			return nil
		}
	}
	if key := spec.TargetKey; key != "" {
		state := e.targetState(key)
		fastIntent := domain.IsFastIntentSpec(spec)
		manualRecover := false
		if spec.Budget != nil {
			manualRecover, _ = spec.Budget["manual_recover"].(bool)
		}
		if manualRecover {
			state.Attempts = 0
			state.Exhausted = false
			state.CooldownUntil = time.Time{}
			state.ExhaustedReason = ""
			state.LastFailureReason = ""
			log.Printf("[engine] target %s manually recovered", key)
		}
		if state.Exhausted && !fastIntent {
			_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "retry-policy", "goal", goal.ID,
				map[string]any{"reason": "target exhausted; manual recovery required", "target": key,
					"exhausted_reason": state.ExhaustedReason})
			return nil
		}
		if state.Solved && !fastIntent {
			_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "retry-policy", "goal", goal.ID,
				map[string]any{"reason": "target already solved", "target": key})
			return nil
		}
		if !fastIntent && state.Attempts >= 2 && spec.ExecutionMode == domain.ExecutionSingle {
			spec.ExecutionMode = domain.ExecutionSwarm
			if spec.Budget == nil {
				spec.Budget = map[string]any{}
			}
			spec.Budget["auto_escalated"] = true
			log.Printf("[engine] escalating target %s to swarm after %d no-progress failures", key, state.Attempts)
		}
		if !fastIntent && state.Attempts >= 3 {
			// Cooldown: no automatic retry until it expires. After it expires,
			// only a new strategy tag may retry; new evidence already reset the
			// attempt counter in recordTargetOutcome.
			if time.Now().Before(state.CooldownUntil) {
				_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "retry-policy", "goal", goal.ID,
					map[string]any{"reason": "target cooldown", "target": key, "cooldown_until": state.CooldownUntil})
				return nil
			}
			if strategyTag(spec) == "" || strategyTag(spec) == state.LastStrategy {
				state.CooldownUntil = time.Now().Add(10 * time.Minute)
				_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "retry-policy", "goal", goal.ID,
					map[string]any{"reason": "target requires a new strategy", "target": key})
				return nil
			}
			// Deprioritize the target while it remains difficult.
			if spec.Priority > 1 {
				spec.Priority -= 2
			}
		}
		if spec.Budget != nil {
			if tag := strategyTag(spec); tag != "" {
				state.LastStrategy = tag
			}
		}
		state.LastMode = spec.ExecutionMode
		if spec.ExpectedFlags > 0 {
			state.ExpectedFlags = spec.ExpectedFlags
		}
	}
	if len(spec.GoalIDs) == 0 {
		spec.GoalIDs = []int64{goal.ID}
	}
	if err := domain.ValidateIntentSpec(spec); err != nil {
		_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "validator", "goal", goal.ID,
			map[string]any{"summary": spec.Summary, "error": err.Error()})
		return nil
	}
	existing, err := e.store.ListIntents(ctx, taskID)
	if err != nil {
		return err
	}
	for _, old := range existing {
		if old.Summary == spec.Summary &&
			old.State != domain.IntentFailed && old.State != domain.IntentCancelled {
			log.Printf("[engine] skip duplicate intent %q (existing #%d, %s)", spec.Summary, old.ID, old.State)
			return nil
		}
	}
	intent, err := e.store.CreateIntent(ctx, taskID, spec, 0, domain.IntentProposed)
	if err != nil {
		return err
	}
	log.Printf("[engine] task %d intent #%d proposed %q risk=%s", taskID, intent.ID, intent.Summary, intent.Risk)
	if _, err := e.emit(ctx, taskID, domain.EventIntentProposed, "planner", "planner", "intent", intent.ID,
		map[string]any{"summary": intent.Summary, "risk": intent.Risk}); err != nil {
		return err
	}

	decision, by, reason := e.approval(task, intent)
	switch decision {
	case domain.ApprovalApproved:
		if _, err := e.store.ApproveIntent(ctx, taskID, intent.ID, decision, by, reason, intent.Risk, intent.DangerousActions); err != nil {
			return err
		}
		if _, err := e.emit(ctx, taskID, domain.EventIntentApproved, "approver", by, "intent", intent.ID,
			map[string]any{"reason": reason}); err != nil {
			return err
		}
	case domain.ApprovalRejected:
		if _, err := e.store.ApproveIntent(ctx, taskID, intent.ID, decision, by, reason, intent.Risk, intent.DangerousActions); err != nil {
			return err
		}
		if _, err := e.emit(ctx, taskID, domain.EventIntentRejected, "approver", by, "intent", intent.ID,
			map[string]any{"reason": reason}); err != nil {
			return err
		}
	default:
		if _, err := e.store.CreatePendingApproval(ctx, taskID, intent.ID, intent.Risk, intent.DangerousActions); err != nil {
			return err
		}
		log.Printf("[engine] intent %d waiting for human approval (risk=%s)", intent.ID, intent.Risk)
	}
	return nil
}

func (e *Engine) approval(task *domain.Task, intent *domain.Intent) (domain.ApprovalDecision, string, string) {
	if task.Mode == domain.ModeCTF {
		return domain.ApprovalApproved, "policy:ctf", "ctf mode: all intents approved"
	}
	if !domain.IsDangerousIntent(intent) {
		return domain.ApprovalApproved, "policy:auto", "low/medium risk, no dangerous action"
	}
	if e.cfg.AutoApproveDangerous {
		return domain.ApprovalApproved, "human:demo", "demo auto-approval"
	}
	return domain.ApprovalPending, "", "dangerous intent requires human approval"
}

func (e *Engine) handleGoalMet(ctx context.Context, taskID int64, goal *domain.Goal, proposal *domain.GoalMetProposal) (bool, error) {
	if proposal == nil || len(proposal.EvidenceIDs) == 0 {
		_, _ = e.emit(ctx, taskID, domain.EventGoalRejected, "mainagent", "mainagent", "goal", goal.ID,
			map[string]any{"reason": "no evidence"})
		return false, nil
	}
	validEvidence, _ := e.store.GoalEvidenceValid(ctx, taskID, goal.ID, proposal.EvidenceIDs)
	if !validEvidence {
		_, _ = e.emit(ctx, taskID, domain.EventGoalRejected, "mainagent", "mainagent", "goal", goal.ID,
			map[string]any{"reason": "evidence ids do not belong to this task/goal"})
		return false, nil
	}
	if _, err := e.emit(ctx, taskID, domain.EventGoalMetProposed, "planner", "planner", "goal", goal.ID,
		map[string]any{"evidence_ids": proposal.EvidenceIDs, "reason": proposal.Reason}); err != nil {
		return false, err
	}
	if err := e.store.SetGoalState(ctx, goal.ID, domain.GoalMet, "mainagent"); err != nil {
		return false, err
	}
	if _, err := e.emit(ctx, taskID, domain.EventGoalAccepted, "mainagent", "mainagent", "goal", goal.ID,
		map[string]any{"reason": proposal.Reason}); err != nil {
		return false, err
	}
	log.Printf("[engine] task %d goal accepted; cancelling active work", taskID)
	_ = e.store.CancelActiveWork(ctx, taskID, "goal met", true, true)
	if err := e.store.SetTaskStatus(ctx, taskID, domain.TaskSettling); err != nil {
		return false, err
	}
	if err := e.store.SetTaskStatus(ctx, taskID, domain.TaskCompleted); err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) onWorkDone(ctx context.Context, taskID int64, out workOutcome) (bool, error) {
	e.active--
	if out.fast && e.fastActive > 0 {
		e.fastActive--
	}
	if out.work.LaneID != "" && e.containers > 0 {
		e.containers--
	}
	if task, _ := e.store.GetTask(ctx, taskID); task != nil {
		switch task.Status {
		case domain.TaskCancelled, domain.TaskFailed, domain.TaskPaused:
			return false, nil
		}
	}
	if out.work.LaneID != "" {
		e.finishSwarmLane(ctx, taskID, out)
		e.dispatch(ctx, taskID)
		return false, nil
	}
	if out.err != nil {
		e.recordTargetOutcome(ctx, taskID, out.intent, nil, out.err.Error())
		if err := e.store.FinishWork(ctx, out.work.ID, domain.WorkFailed, out.err.Error(), 0, 0); err != nil {
			return false, err
		}
		if _, err := e.emit(ctx, taskID, domain.EventWorkFailed, "worker", out.work.WorkerID, "work", out.work.ID,
			map[string]any{"error": out.err.Error()}); err != nil {
			return false, err
		}
		e.dispatch(ctx, taskID)
		return false, nil
	}
	if out.result == nil {
		e.recordTargetOutcome(ctx, taskID, out.intent, nil, "worker returned nil result")
		return false, fmt.Errorf("worker returned nil result")
	}
	if err := domain.ValidateWorkResult(*out.result); err != nil {
		e.recordTargetOutcome(ctx, taskID, out.intent, nil, err.Error())
		if ferr := e.store.FinishWork(ctx, out.work.ID, domain.WorkFailed, err.Error(), 0, 0); ferr != nil {
			return false, ferr
		}
		_, _ = e.emit(ctx, taskID, domain.EventWorkFailed, "worker", out.work.WorkerID, "work", out.work.ID,
			map[string]any{"error": err.Error()})
		e.dispatch(ctx, taskID)
		return false, nil
	}
	var assetIDs []int64
	validAssets := map[int64]bool{}
	for _, spec := range out.result.Assets {
		asset, err := e.store.RecordAssetForTask(ctx, taskID, spec)
		if err != nil {
			return false, err
		}
		assetIDs = append(assetIDs, asset.ID)
		validAssets[asset.ID] = true
		if _, err := e.emit(ctx, taskID, domain.EventAssetRecorded, "worker", out.work.WorkerID, "asset", asset.ID,
			map[string]any{"type": asset.Type, "value": asset.Value}); err != nil {
			return false, err
		}
	}
	filterAssets := func(ids []int64) []int64 {
		if len(ids) == 0 {
			return assetIDs
		}
		kept := make([]int64, 0, len(ids))
		for _, id := range ids {
			if validAssets[id] {
				kept = append(kept, id)
			}
		}
		if len(kept) == 0 {
			return assetIDs
		}
		return kept
	}
	for _, spec := range out.result.Facts {
		if crossTaskReference(spec.Summary+" "+spec.Detail+" "+spec.Evidence, taskID) {
			log.Printf("[engine] task %d blocked cross-task fact", taskID)
			continue
		}
		spec.AssetIDs = filterAssets(spec.AssetIDs)
		semantic := semanticFactFingerprint(spec)
		if e.factSemantics[semantic] {
			continue
		}
		e.factSemantics[semantic] = true
		fact, err := e.store.RecordFact(ctx, taskID, out.intent.ID, out.work.ID, spec)
		if err != nil {
			return false, err
		}
		if _, err := e.emit(ctx, taskID, domain.EventFactRecorded, "worker", out.work.WorkerID, "fact", fact.ID,
			map[string]any{"summary": fact.Summary}); err != nil {
			return false, err
		}
		_ = e.store.RecordActivity(ctx, domain.Activity{
			TaskID: taskID, WorkID: out.work.ID, IntentID: out.intent.ID,
			Role: "worker", Kind: "fact", Summary: fact.Summary,
		})
	}
	for _, spec := range out.result.Findings {
		if crossTaskReference(spec.Title+" "+spec.Evidence, taskID) {
			log.Printf("[engine] task %d blocked cross-task finding", taskID)
			continue
		}
		spec.AssetIDs = filterAssets(spec.AssetIDs)
		finding, err := e.store.RecordFinding(ctx, taskID, out.intent.ID, out.work.ID, spec)
		if err != nil {
			return false, err
		}
		if _, err := e.emit(ctx, taskID, domain.EventFindingReported, "worker", out.work.WorkerID, "finding", finding.ID,
			map[string]any{"title": finding.Title, "severity": finding.Severity}); err != nil {
			return false, err
		}
		_ = e.store.RecordActivity(ctx, domain.Activity{
			TaskID: taskID, WorkID: out.work.ID, IntentID: out.intent.ID,
			Role: "worker", Kind: "finding", Summary: finding.Title,
		})
		if !out.result.Partial {
			task, _ := e.store.GetTask(ctx, taskID)
			e.submitFlag(ctx, taskID, task, out.intent.TargetKey, finding)
		}
	}
	for _, spec := range out.result.Evidence {
		if _, err := e.store.RecordEvidence(ctx, taskID, out.work.ID, spec); err != nil {
			return false, err
		}
	}
	workState := domain.WorkSucceeded
	if out.result.Status != "done" {
		workState = domain.WorkFailed
	}
	if err := e.store.FinishWork(ctx, out.work.ID, workState, out.result.Error, 0, 0); err != nil {
		return false, err
	}
	reason := out.result.Error
	if reason == "" && out.result.Status != "done" {
		reason = out.result.Status
	}
	e.recordTargetOutcome(ctx, taskID, out.intent, out.result, reason)
	if _, err := e.emit(ctx, taskID, domain.EventWorkFinished, "worker", out.work.WorkerID, "work", out.work.ID,
		map[string]any{"status": out.result.Status}); err != nil {
		return false, err
	}
	_ = e.store.RecordActivity(ctx, domain.Activity{
		TaskID: taskID, WorkID: out.work.ID, IntentID: out.intent.ID,
		Role: "worker", Kind: "work_finished", Summary: out.result.Status,
	})
	e.dispatch(ctx, taskID)
	return false, nil
}

func (e *Engine) reconcileChallenges(ctx context.Context, taskID int64, task *domain.Task) {
	if e.cfg.ReconcileChallenges == nil || task == nil {
		return
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	codes, err := e.cfg.ReconcileChallenges(reconcileCtx, task)
	if err != nil {
		log.Printf("[engine] task %d completion reconcile: %v", taskID, err)
		return
	}
	for _, code := range codes {
		key := "challenge:" + strings.TrimPrefix(strings.ToLower(code), "challenge:")
		state := e.targetState(key)
		if !state.Solved || !state.PlatformCompleted {
			state.Solved = true
			state.PlatformCompleted = true
			state.Attempts = 0
			state.CooldownUntil = time.Time{}
			state.Exhausted = false
			e.cancelTargetIntents(ctx, taskID, key, 0)
			log.Printf("[engine] task %d reconciled completed challenge %s", taskID, code)
		}
	}
}

func (e *Engine) reconcileActive(ctx context.Context, taskID int64) {
	works, err := e.store.ListWorks(ctx, taskID)
	if err != nil {
		return
	}
	intents, _ := e.store.ListIntents(ctx, taskID)
	fast := map[int64]bool{}
	for _, in := range intents {
		fast[in.ID] = domain.IsFastIntent(in)
	}
	active, fastActive, containers := 0, 0, 0
	for _, w := range works {
		if w.State != domain.WorkRunning && w.State != domain.WorkLeased {
			continue
		}
		active++
		if w.LaneID != "" {
			containers++
		}
		if fast[w.IntentID] {
			fastActive++
		}
	}
	if active != e.lastActive {
		e.lastActive = active
		e.idleSince = time.Time{}
	}
	e.active = active
	e.fastActive = fastActive
	e.containers = containers
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// checkIdle reports continuously idle worker capacity. The event wakes the
// Planner, which decides whether to open a new direction.
func (e *Engine) checkIdle(ctx context.Context, taskID int64) {
	if e.cfg.IdleReportAfter <= 0 || e.active >= e.cfg.MaxWorkers {
		e.idleSince = time.Time{}
		return
	}
	now := time.Now()
	if e.idleSince.IsZero() {
		e.idleSince = now
		return
	}
	if now.Sub(e.idleSince) < e.cfg.IdleReportAfter {
		return
	}
	if !e.lastIdleReport.IsZero() && now.Sub(e.lastIdleReport) < e.cfg.IdleReportAfter {
		return
	}
	e.lastIdleReport = now
	idle := e.cfg.MaxWorkers - e.active
	_, _ = e.emit(ctx, taskID, domain.EventWorkersIdle, "engine", "scheduler", "task", taskID,
		map[string]any{"idle_workers": idle, "active_workers": e.active, "max_workers": e.cfg.MaxWorkers,
			"containers": e.containers, "max_containers": e.cfg.MaxContainers})
	log.Printf("[engine] task %d idle workers=%d active=%d", taskID, idle, e.active)
}

func (e *Engine) dispatch(ctx context.Context, taskID int64) {
	e.reconcileActive(ctx, taskID)
	e.ensureSwarms(ctx, taskID)
	normalCap := e.cfg.MaxWorkers
	fastCap := 0
	if e.cfg.MaxWorkers >= 3 {
		// Reserve one slot for submit / verify / finalize work. Long solve
		// lanes can occupy the other slots but cannot starve this one.
		normalCap = e.cfg.MaxWorkers - 1
		fastCap = 1
	}
	for {
		progressed := false
		if fastCap > 0 && e.fastActive < fastCap && e.active < e.cfg.MaxWorkers {
			wantFast := true
			work, intent, err := e.store.ClaimNextIntentFiltered(ctx, taskID,
				fmt.Sprintf("worker-%d", e.active+1), e.cfg.Lease, e.cfg.PriorityAging, &wantFast)
			if err != nil {
				log.Printf("[engine] claim fast intent: %v", err)
				return
			}
			if work != nil && intent != nil {
				e.startWork(ctx, taskID, work, intent, true)
				progressed = true
				continue
			}
		}
		if e.active-e.fastActive < normalCap && e.active < e.cfg.MaxWorkers {
			if e.cfg.MaxContainers > 0 && e.containers >= e.cfg.MaxContainers {
				break
			}
			work, intent, err := e.store.ClaimSwarmLane(ctx, taskID,
				fmt.Sprintf("worker-%d", e.active+1), e.cfg.Lease)
			if err != nil {
				log.Printf("[engine] claim swarm lane: %v", err)
				return
			}
			if work == nil || intent == nil {
				wantFast := false
				work, intent, err = e.store.ClaimNextIntentFiltered(ctx, taskID,
					fmt.Sprintf("worker-%d", e.active+1), e.cfg.Lease, e.cfg.PriorityAging, &wantFast)
				if err != nil {
					log.Printf("[engine] claim: %v", err)
					return
				}
			}
			if work != nil && intent != nil {
				e.startWork(ctx, taskID, work, intent, false)
				progressed = true
				continue
			}
		}
		if !progressed {
			return
		}
	}
}

func (e *Engine) startWork(ctx context.Context, taskID int64, work *domain.Work, intent *domain.Intent, fast bool) {
	e.active++
	if fast {
		e.fastActive++
	}
	if work.LaneID != "" {
		e.containers++
	}
	if _, err := e.emit(ctx, taskID, domain.EventWorkClaimed, "worker", work.WorkerID, "work", work.ID,
		map[string]any{"intent_id": intent.ID, "lane_id": work.LaneID, "fast": fast}); err != nil {
		log.Printf("[engine] emit claimed: %v", err)
	}
	_ = e.store.RecordActivity(ctx, domain.Activity{
		TaskID: taskID, WorkID: work.ID, IntentID: intent.ID,
		Role: "engine", Kind: "work_claimed", Summary: intent.Summary,
	})
	go e.runWork(ctx, taskID, work, intent, fast)
}

func (e *Engine) ensureSwarms(ctx context.Context, taskID int64) {
	cands, err := e.store.ListSwarmCandidates(ctx, taskID)
	if err != nil {
		return
	}
	for _, c := range cands {
		if c.Mode == "swarm" {
			// Planner owns the Intent; every swarm lane plan is authored by
			// swarm_lead. Planner-authored strategies are ignored by design.
			if !e.leadSwarm(ctx, taskID, &c, nil) {
				continue
			}
		}
		run, err := e.store.StartSwarm(ctx, taskID, c)
		if err != nil {
			log.Printf("[engine] start swarm intent #%d: %v", c.IntentID, err)
			e.blockSwarm(ctx, taskID, c.IntentID, "start swarm: "+err.Error())
			continue
		}
		e.planningFailures = 0
		log.Printf("[engine] %s intent #%d prepared lanes=%d quorum=%d", run.Mode, run.IntentID, run.Lanes, run.Quorum)
	}
}

func completeLanePlan(c store.SwarmCandidate) bool {
	if len(c.Strategies) != c.Lanes || len(c.Briefs) != c.Lanes {
		return false
	}
	seen := map[string]bool{}
	for i := range c.Strategies {
		name, brief := strings.TrimSpace(c.Strategies[i]), strings.TrimSpace(c.Briefs[i])
		if name == "" || brief == "" || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func (e *Engine) leadSwarm(ctx context.Context, taskID int64, c *store.SwarmCandidate, existing []domain.LanePlan) bool {
	lead, ok := e.rt.(runtime.LeadRuntime)
	if !ok {
		e.blockSwarm(ctx, taskID, c.IntentID, "swarm_lead runtime unavailable")
		return false
	}
	task, _ := e.store.GetTask(ctx, taskID)
	goal, _ := e.store.CurrentGoal(ctx, taskID)
	facts, _ := e.store.ListFacts(ctx, taskID, 10)
	constraints, _ := e.store.ListConstraints(ctx, taskID)
	intent, _ := e.store.GetIntent(ctx, c.IntentID)
	if task == nil || goal == nil || intent == nil {
		e.blockSwarm(ctx, taskID, c.IntentID, "incomplete swarm lead request")
		return false
	}
	plan, err := lead.Lead(ctx, &domain.LeadRequest{
		Task: task, Goal: goal, Intent: intent, Lanes: c.Lanes,
		Facts: facts, Constraints: constraints, Existing: existing, Hints: c.Hints,
	})
	if err != nil || plan == nil || len(plan.Lanes) < c.Lanes {
		reason := "swarm_lead returned no plan"
		if err != nil {
			reason = "swarm_lead: " + err.Error()
		}
		e.blockSwarm(ctx, taskID, c.IntentID, reason)
		return false
	}
	c.Strategies = make([]string, c.Lanes)
	c.Briefs = make([]string, c.Lanes)
	for i := 0; i < c.Lanes; i++ {
		if i < len(existing) && existing[i].Name != "" && existing[i].Brief != "" {
			c.Strategies[i], c.Briefs[i] = existing[i].Name, existing[i].Brief
			continue
		}
		c.Strategies[i] = plan.Lanes[i].Name
		c.Briefs[i] = plan.Lanes[i].Brief
	}
	if !completeLanePlan(*c) {
		e.blockSwarm(ctx, taskID, c.IntentID, "swarm_lead returned an incomplete or duplicate lane plan")
		return false
	}
	if len(existing) > 0 {
		c.PlannedBy = "swarm_lead_repair"
	} else {
		c.PlannedBy = "swarm_lead"
	}
	log.Printf("[engine] swarm_lead %s %d lanes for intent #%d", map[bool]string{true: "repaired", false: "planned"}[len(existing) > 0], c.Lanes, c.IntentID)
	return true
}

func (e *Engine) blockSwarm(ctx context.Context, taskID, intentID int64, reason string) {
	_ = e.store.SetIntentState(ctx, intentID, domain.IntentBlocked)
	_, _ = e.emit(ctx, taskID, domain.EventSwarmFailed, "engine", "coordinator", "intent", intentID,
		map[string]any{"reason": reason})
	e.planningFailures++
	log.Printf("[engine] swarm intent #%d blocked: %s", intentID, reason)
	if e.planningFailures >= 3 {
		if task, _ := e.store.GetTask(ctx, taskID); task != nil && task.Status == domain.TaskRunning {
			_ = e.store.SetTaskStatus(ctx, taskID, domain.TaskPaused)
			_, _ = e.emit(ctx, taskID, domain.EventTaskPaused, "engine", "coordinator", "task", taskID,
				map[string]any{"reason": "swarm planning stalled", "failures": e.planningFailures})
		}
		if e.cancel != nil {
			e.cancel()
		}
	}
}

func (e *Engine) runWork(ctx context.Context, taskID int64, work *domain.Work, intent *domain.Intent, fast bool) {
	workCtx, workCancel := context.WithCancel(ctx)
	if e.cfg.WorkerTimeout > 0 {
		deadlineCtx, deadlineCancel := context.WithTimeout(workCtx, e.cfg.WorkerTimeout)
		priorCancel := workCancel
		workCtx = deadlineCtx
		workCancel = func() {
			deadlineCancel()
			priorCancel()
		}
	}
	e.workMu.Lock()
	e.workCancels[work.ID] = workCancel
	e.workMu.Unlock()
	defer func() {
		workCancel()
		e.workMu.Lock()
		delete(e.workCancels, work.ID)
		e.workMu.Unlock()
	}()
	task, _ := e.store.GetTask(ctx, taskID)
	goal, _ := e.store.CurrentGoal(ctx, taskID)
	facts, _ := e.store.ListFacts(ctx, taskID, 10)
	constraints, _ := e.store.ListConstraints(ctx, taskID)
	failures := e.failuresForTarget(ctx, taskID, intent.TargetKey)
	strategy, brief := "", ""
	if work.LaneID != "" {
		if run, err := e.store.GetSwarmRun(ctx, intent.ID); err == nil {
			for i, lane := range swarmLaneIDs(run.Lanes) {
				if lane == work.LaneID {
					if i < len(run.Strategies) {
						strategy = run.Strategies[i]
					}
					if i < len(run.Briefs) {
						brief = run.Briefs[i]
					}
				}
			}
		}
	}
	log.Printf("[engine] task %d work #%d start intent #%d lane=%s", taskID, work.ID, intent.ID, work.LaneID)
	result, err := e.rt.Work(workCtx, &domain.WorkerRequest{
		Task: task, Goal: goal, Intent: intent, Work: work, Facts: facts, Constraints: constraints,
		Failures: failures, LaneStrategy: strategy, LaneBrief: brief,
	})
	if err != nil {
		log.Printf("[engine] task %d work #%d failed: %v", taskID, work.ID, err)
	} else if result != nil {
		log.Printf("[engine] task %d work #%d done status=%s", taskID, work.ID, result.Status)
	}
	select {
	case e.workDone <- workOutcome{work: work, intent: intent, result: result, err: err, fast: fast}:
	case <-ctx.Done():
	}
}

func (e *Engine) emit(ctx context.Context, taskID int64, typ domain.EventType, actorType, actorID, subjectType string, subjectID int64, payload map[string]any) (*domain.Event, error) {
	ev, err := e.store.AppendEvent(ctx, taskID, typ, actorType, actorID, subjectType, subjectID, payload)
	if err != nil {
		return nil, err
	}
	e.sched.Notify(ev)
	return ev, nil
}

var intentTargetPattern = regexp.MustCompile(`(?i)\b([a-z]{1,3}-[0-9]{1,3})\b`)
var crossTaskArtifactPattern = regexp.MustCompile(`(?i)artifacts[\\/]+task-([0-9]+)`)
var expectedFlagPattern = regexp.MustCompile(`(?i)\b(?:total_)?flag_count"?\s*[:=]\s*"?([0-9]+)`)
var platformCompletedPattern = regexp.MustCompile(`(?i)\bis_completed"?\s*[:=]\s*"?true\b`)

func expectedFlagsFromText(text string) int {
	m := expectedFlagPattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// crossTaskReference rejects evidence that embeds another task's artifact path
// or the host database. The runtime sandbox is the primary defense; this is a
// second line of defense at ingestion.
func crossTaskReference(text string, taskID int64) bool {
	if strings.Contains(strings.ToLower(text), "hornedviper.db") {
		return true
	}
	for _, m := range crossTaskArtifactPattern.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 && m[1] != strconv.FormatInt(taskID, 10) {
			return true
		}
	}
	return false
}

func intentTargetKey(summary string) string {
	m := intentTargetPattern.FindStringSubmatch(summary)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

func strategyTag(spec domain.IntentSpec) string {
	if strings.TrimSpace(spec.StrategyTag) != "" {
		return strings.ToLower(strings.TrimSpace(spec.StrategyTag))
	}
	if spec.Budget == nil {
		return ""
	}
	v, _ := spec.Budget["strategy_tag"].(string)
	return strings.ToLower(strings.TrimSpace(v))
}

type targetState struct {
	Attempts          int
	ProgressCount     int
	LastProgressAt    time.Time
	LastFailureAt     time.Time
	CooldownUntil     time.Time
	LastStrategy      string
	LastMode          domain.ExecutionMode
	ExpectedFlags     int
	AcceptedFlags     int
	AcceptedHashes    map[string]bool
	PlatformCompleted bool
	LastFailureReason string
	ExhaustedReason   string
	Exhausted         bool
	Solved            bool
	CloseRequested    bool
}

// RecoverTarget clears cooldown/exhaustion for a target after a human review.
func (e *Engine) RecoverTarget(ctx context.Context, taskID int64, key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	state := e.targetState(key)
	state.Attempts = 0
	state.CooldownUntil = time.Time{}
	state.Exhausted = false
	state.ExhaustedReason = ""
	state.LastFailureReason = ""
	e.releaseDeferredTargetIntents(ctx, taskID, key)
	log.Printf("[engine] target %s recovered by human", key)
	return true
}

func (e *Engine) targetState(key string) *targetState {
	if e.targetStates == nil {
		e.targetStates = map[string]*targetState{}
	}
	state := e.targetStates[key]
	if state == nil {
		state = &targetState{}
		e.targetStates[key] = state
	}
	return state
}

func (e *Engine) newTargetOutput(key string, result *domain.WorkResult) bool {
	if result == nil {
		return false
	}
	hashes := map[string]bool{}
	add := func(parts ...string) {
		sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		hashes[hex.EncodeToString(sum[:])] = true
	}
	for _, a := range result.Assets {
		add("asset", strings.TrimSpace(a.Type), strings.TrimSpace(a.Value))
	}
	for _, f := range result.Facts {
		// Semantic fact identity, not raw evidence: "port 80 open" and
		// "80/tcp is open" describe the same observation.
		add("fact-semantic", semanticFactFingerprint(f))
	}
	for _, f := range result.Findings {
		add("finding", strings.TrimSpace(f.Title), strings.TrimSpace(f.Evidence), strings.TrimSpace(f.SubjectKey), strings.TrimSpace(f.CaseKey), strings.TrimSpace(f.Observation))
	}
	for _, ev := range result.Evidence {
		add("evidence", strings.TrimSpace(ev.Type), strings.TrimSpace(ev.Path), strings.TrimSpace(ev.Hash))
	}
	if len(hashes) == 0 {
		return false
	}
	if e.targetEvidence == nil {
		e.targetEvidence = map[string]map[string]bool{}
	}
	seen := e.targetEvidence[key]
	if seen == nil {
		seen = map[string]bool{}
		e.targetEvidence[key] = seen
	}
	fresh := false
	for h := range hashes {
		if !seen[h] {
			fresh = true
		}
		seen[h] = true
	}
	return fresh
}

func semanticFactFingerprint(f domain.FactSpec) string {
	text := strings.ToLower(f.Summary + " " + f.Detail)
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	stop := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true, "was": true, "were": true,
		"of": true, "to": true, "in": true, "on": true, "at": true, "with": true, "and": true,
		"or": true, "for": true, "from": true, "port": true, "tcp": true, "udp": true,
	}
	synonym := map[string]string{
		"opened": "open", "listening": "open", "reachable": "open",
		"found": "found", "discovered": "found", "detected": "found",
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if stop[token] {
			continue
		}
		if v, ok := synonym[token]; ok {
			token = v
		}
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func (e *Engine) failuresForTarget(ctx context.Context, taskID int64, targetKey string) []domain.WorkFailureView {
	targetKey = strings.ToLower(strings.TrimSpace(targetKey))
	if targetKey == "" {
		return nil
	}
	works, _ := e.store.ListWorks(ctx, taskID)
	intents, _ := e.store.ListIntents(ctx, taskID)
	targets := map[int64]string{}
	for _, in := range intents {
		k := strings.ToLower(in.TargetKey)
		if k == "" {
			k = strings.ToLower(intentTargetKey(in.Summary))
		}
		targets[in.ID] = k
	}
	out := make([]domain.WorkFailureView, 0, 8)
	for i := len(works) - 1; i >= 0 && len(out) < 8; i-- {
		w := works[i]
		if w.State != domain.WorkFailed && w.State != domain.WorkCancelled {
			continue
		}
		if targets[w.IntentID] != targetKey {
			continue
		}
		out = append(out, domain.WorkFailureView{WorkID: w.ID, IntentID: w.IntentID, Target: targetKey,
			Status: string(w.State), Reason: w.StopReason})
	}
	return out
}

func (e *Engine) recentFailures(ctx context.Context, taskID int64, works []*domain.Work, intents []*domain.Intent) []domain.WorkFailureView {
	targets := map[int64]string{}
	for _, in := range intents {
		key := in.TargetKey
		if key == "" {
			key = intentTargetKey(in.Summary)
		}
		targets[in.ID] = key
	}
	out := make([]domain.WorkFailureView, 0, 8)
	for i := len(works) - 1; i >= 0 && len(out) < 8; i-- {
		w := works[i]
		if w.State != domain.WorkFailed && w.State != domain.WorkCancelled {
			continue
		}
		out = append(out, domain.WorkFailureView{
			WorkID: w.ID, IntentID: w.IntentID, Target: targets[w.IntentID],
			Status: string(w.State), Reason: w.StopReason,
		})
	}
	return out
}

// TargetStates exposes the current retry/cooldown state for the plan page.
func (e *Engine) TargetStates() []domain.TargetStateView {
	return e.targetSnapshots()
}

func (e *Engine) executeCloseIntent(ctx context.Context, taskID int64, task *domain.Task, goal *domain.Goal, spec domain.IntentSpec) {
	key := strings.TrimSpace(spec.TargetKey)
	if key == "" {
		_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "close-policy", "goal", goal.ID,
			map[string]any{"reason": "close_target requires target_key"})
		return
	}
	state := e.targetState(key)
	if state.AcceptedFlags == 0 && !state.PlatformCompleted {
		_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "close-policy", "goal", goal.ID,
			map[string]any{"reason": "close_target requires accepted evidence", "target": key})
		return
	}
	if !state.PlatformCompleted && state.ExpectedFlags > 0 && state.AcceptedFlags < state.ExpectedFlags {
		_, _ = e.emit(ctx, taskID, domain.EventIntentRejected, "system", "close-policy", "goal", goal.ID,
			map[string]any{"reason": "close_target incomplete", "target": key,
				"accepted": state.AcceptedFlags, "expected": state.ExpectedFlags})
		return
	}
	e.requestTargetClose(ctx, taskID, key)
}

// requestTargetClose is invoked only after the Planner explicitly decides that
// a target should be closed. It executes the platform close directly, with a
// fast close Intent as fallback.
func (e *Engine) requestTargetClose(ctx context.Context, taskID int64, key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	state := e.targetState(key)
	if state.CloseRequested {
		return
	}
	task, _ := e.store.GetTask(ctx, taskID)
	goal, _ := e.store.CurrentGoal(ctx, taskID)
	if task == nil || goal == nil {
		return
	}
	if e.cfg.CloseChallenge != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := e.cfg.CloseChallenge(closeCtx, task, key)
		cancel()
		if err == nil {
			state.CloseRequested = true
			log.Printf("[engine] target %s closed directly", key)
			return
		}
		log.Printf("[engine] target %s direct close failed, queueing close Intent: %v", key, err)
	}
	state.CloseRequested = true
	spec := domain.IntentSpec{
		Summary:   "Close completed challenge " + key + ": it is already solved; use the platform close endpoint to release the container. Do not submit or exploit again.",
		TargetKey: key, StrategyTag: "close", Rationale: "target solved; release container slot",
		GoalIDs: []int64{goal.ID}, Priority: 10, Risk: domain.RiskLow, ExecutionMode: domain.ExecutionSingle,
		Budget: map[string]any{"fast_lane": true, "container_required": true, "internal_close": true},
	}
	if err := e.createIntent(ctx, taskID, task, goal, spec); err != nil {
		log.Printf("[engine] target %s close intent: %v", key, err)
	}
}

func (e *Engine) targetSnapshots() []domain.TargetStateView {
	if len(e.targetStates) == 0 {
		return nil
	}
	keys := make([]string, 0, len(e.targetStates))
	for k := range e.targetStates {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]domain.TargetStateView, 0, len(keys))
	for _, k := range keys {
		s := e.targetStates[k]
		v := domain.TargetStateView{
			Target: k, Attempts: s.Attempts, ProgressCount: s.ProgressCount,
			Solved: s.Solved, LastStrategy: s.LastStrategy,
			ExpectedFlags: s.ExpectedFlags, AcceptedFlags: s.AcceptedFlags,
			LastFailureReason: s.LastFailureReason, ExhaustedReason: s.ExhaustedReason,
		}
		if !s.CooldownUntil.IsZero() {
			t := s.CooldownUntil
			v.CooldownUntil = &t
		}
		v.Exhausted = s.Exhausted
		out = append(out, v)
	}
	return out
}

func resultFacts(result *domain.WorkResult) []domain.FactSpec {
	if result == nil {
		return nil
	}
	return result.Facts
}

// recordAcceptedFlags adds newly accepted flag hashes to the target state and
// returns true when at least one accepted flag is present.
func (e *Engine) recordAcceptedFlags(ctx context.Context, taskID int64, state *targetState, result *domain.WorkResult) bool {
	if result == nil {
		return false
	}
	if state.AcceptedHashes == nil {
		state.AcceptedHashes = map[string]bool{}
	}
	found := false
	for _, finding := range result.Findings {
		for _, flag := range flagLike.FindAllString(finding.Title+" "+finding.Evidence, -1) {
			sum := sha256.Sum256([]byte(flag))
			hash := hex.EncodeToString(sum[:])
			ok, _ := e.store.FlagAccepted(ctx, taskID, hash)
			if !ok {
				continue
			}
			found = true
			if !state.AcceptedHashes[hash] {
				state.AcceptedHashes[hash] = true
				state.AcceptedFlags++
			}
		}
	}
	return found
}

// recordTargetOutcome is the first-layer accounting rule:
//
//	solved / new evidence -> clear attempts
//	partial progress      -> clear attempts (progress is not a failure)
//	no output failure     -> increment attempts
func (e *Engine) recordTargetOutcome(ctx context.Context, taskID int64, intent *domain.Intent, result *domain.WorkResult, reason string) {
	if intent == nil {
		return
	}
	key := intent.TargetKey
	if key == "" {
		key = intentTargetKey(intent.Summary)
	}
	if key == "" {
		return
	}
	state := e.targetState(key)
	for _, f := range resultFacts(result) {
		text := f.Summary + " " + f.Detail + " " + f.Evidence
		if n := expectedFlagsFromText(text); n > 0 {
			state.ExpectedFlags = n
		}
		if platformCompletedPattern.MatchString(text) {
			state.PlatformCompleted = true
			state.Solved = true
			state.CooldownUntil = time.Time{}
			state.Exhausted = false
			_ = e.store.MarkChallengeCompleted(ctx, taskID, key)
		}
	}
	if state.PlatformCompleted {
		e.cancelTargetIntents(ctx, taskID, key, intent.ID)
	}
	if e.recordAcceptedFlags(ctx, taskID, state, result) {
		state.Solved = state.ExpectedFlags > 0 && state.AcceptedFlags >= state.ExpectedFlags
		state.Attempts = 0
		state.CooldownUntil = time.Time{}
		state.Exhausted = false
		state.ProgressCount++
		state.LastProgressAt = time.Now()
		if state.Solved {
			_ = e.store.MarkChallengeCompleted(ctx, taskID, key)
			e.cancelTargetIntents(ctx, taskID, key, intent.ID)
		}
		// Closing is a Planner decision; this only marks completion state.
		log.Printf("[engine] target %s accepted flags=%d/%d solved=%v", key, state.AcceptedFlags, state.ExpectedFlags, state.Solved)
		return
	}
	if e.newTargetOutput(key, result) {
		state.Attempts = 0
		state.CooldownUntil = time.Time{}
		state.Exhausted = false
		state.ExhaustedReason = ""
		state.ProgressCount++
		state.LastProgressAt = time.Now()
		e.releaseDeferredTargetIntents(ctx, taskID, key)
		log.Printf("[engine] target %s progress; attempts cleared", key)
		return
	}
	if result == nil || result.Status != "done" || result.Partial {
		state.Attempts++
		state.LastFailureAt = time.Now()
		if reason != "" {
			state.LastFailureReason = reason
		}
		log.Printf("[engine] target %s no-progress failure count=%d reason=%s", key, state.Attempts, reason)
		if state.Attempts == 3 {
			state.CooldownUntil = time.Now().Add(10 * time.Minute)
			e.deferTargetIntents(ctx, taskID, key, intentStrategy(intent), false, "target cooldown")
			log.Printf("[engine] target %s cooldown until %s", key, state.CooldownUntil.Format(time.RFC3339))
		}
		if state.Attempts >= 5 {
			state.Exhausted = true
			state.ExhaustedReason = reason
			e.deferTargetIntents(ctx, taskID, key, "", true, reason)
			log.Printf("[engine] target %s exhausted after %d no-progress failures: %s", key, state.Attempts, reason)
		}
	}
}

func hardTaskMinutes(task *domain.Task) float64 {
	if task == nil || task.Budget == nil {
		return 0
	}
	return minutesFromAny(task.Budget["hard_max_minutes"])
}

func minutesFromAny(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// Notify injects an external event (for example a human approval) into the scheduler.
func (e *Engine) Notify(ev *domain.Event) {
	e.sched.Notify(ev)
}

var flagLike = regexp.MustCompile(`(?i)[a-z0-9_]{2,}\{[^}\s]+\}`)

func (e *Engine) submitFlag(ctx context.Context, taskID int64, task *domain.Task, targetKey string, finding *domain.Finding) {
	e.submitFindingFlags(ctx, taskID, task, targetKey, finding, nil)
}

// submitFindingFlags submits each flag independently. allowed==nil means all
// flags in the finding; otherwise only flags whose key is present are submitted.
func (e *Engine) submitFindingFlags(ctx context.Context, taskID int64, task *domain.Task, targetKey string, finding *domain.Finding, allowed map[string]bool) {
	if e.cfg.Submitter == nil || !e.cfg.Submitter.Enabled() || task == nil || task.Mode != domain.ModeCTF {
		return
	}
	if finding == nil || !flagLike.MatchString(finding.Evidence+" "+finding.Title) {
		return
	}
	seen := map[string]bool{}
	for _, flag := range flagPattern.FindAllString(finding.Evidence+" "+finding.Title, -1) {
		flag = strings.TrimSpace(flag)
		key := strings.ToLower(flag)
		if flag == "" || seen[key] {
			continue
		}
		seen[key] = true
		if allowed != nil && !allowed[key] {
			continue
		}
		e.submitOneFlag(ctx, taskID, task, targetKey, finding, flag)
	}
}

func (e *Engine) submitOneFlag(ctx context.Context, taskID int64, task *domain.Task, targetKey string, finding *domain.Finding, flag string) {
	// Serialize check+submit across lanes so two lanes cannot both send the
	// same new flag to the platform.
	e.submissionMu.Lock()
	defer e.submissionMu.Unlock()
	if done, _ := e.store.IsChallengeCompleted(ctx, taskID, targetKey); done {
		return
	}
	copyFinding := *finding
	// The submitter extracts one flag from a finding. Give it exactly the flag
	// selected by the merger so a multi-flag finding cannot submit unconfirmed
	// siblings.
	copyFinding.Evidence = flag
	copyFinding.Title = flag
	sum := sha256.Sum256([]byte(flag))
	hash := hex.EncodeToString(sum[:])
	// Check before invoking the submitter: a previously accepted flag must not
	// be sent to the platform again.
	if already, err := e.store.FlagAccepted(ctx, taskID, hash); err != nil {
		log.Printf("[engine] flag dedupe: %v", err)
		return
	} else if already {
		return
	}
	res, err := e.cfg.Submitter.Submit(ctx, task, copyFinding)
	if err != nil {
		log.Printf("[engine] submit finding %d flag %s: %v", finding.ID, flag, err)
		return
	}
	submitted := res.Flag
	if submitted == "" {
		submitted = flag
	}
	sum = sha256.Sum256([]byte(submitted))
	hash = hex.EncodeToString(sum[:])
	if err := e.store.RecordFlagSubmission(ctx, taskID, finding.ID, submitted, hash, res.Accepted, res.Message); err != nil {
		log.Printf("[engine] record submission: %v", err)
		return
	}
	_, _ = e.emit(ctx, taskID, domain.EventFlagSubmitted, "submitter", "command", "finding", finding.ID,
		map[string]any{"flag": submitted, "accepted": res.Accepted, "message": res.Message})
	if res.Accepted {
		_, _ = e.emit(ctx, taskID, domain.EventFlagAccepted, "submitter", "command", "finding", finding.ID,
			map[string]any{"flag": submitted, "message": res.Message})
	} else {
		_, _ = e.emit(ctx, taskID, domain.EventFlagRejected, "submitter", "command", "finding", finding.ID,
			map[string]any{"flag": submitted, "message": res.Message})
	}
}

func intentStrategy(intent *domain.Intent) string {
	if intent == nil {
		return ""
	}
	if strings.TrimSpace(intent.StrategyTag) != "" {
		return strings.ToLower(strings.TrimSpace(intent.StrategyTag))
	}
	if intent.Budget != nil {
		if v, ok := intent.Budget["strategy_tag"].(string); ok {
			return strings.ToLower(strings.TrimSpace(v))
		}
	}
	return ""
}

func (e *Engine) deferTargetIntents(ctx context.Context, taskID int64, targetKey, strategy string, all bool, reason string) {
	targetKey = strings.ToLower(strings.TrimSpace(targetKey))
	if targetKey == "" {
		return
	}
	intents, err := e.store.ListIntents(ctx, taskID, domain.IntentApproved, domain.IntentQueued)
	if err != nil {
		return
	}
	for _, in := range intents {
		k := in.TargetKey
		if k == "" {
			k = intentTargetKey(in.Summary)
		}
		if strings.ToLower(k) != targetKey || domain.IsFastIntent(in) {
			continue
		}
		if !all && strategy != "" && intentStrategy(in) != strategy {
			continue
		}
		if err := e.store.SetIntentState(ctx, in.ID, domain.IntentDeferred); err != nil {
			continue
		}
		_, _ = e.emit(ctx, taskID, domain.EventIntentDeferred, "engine", "retry-policy", "intent", in.ID,
			map[string]any{"target": targetKey, "strategy": intentStrategy(in), "reason": reason})
	}
}

func (e *Engine) releaseDeferredTargetIntents(ctx context.Context, taskID int64, targetKey string) {
	targetKey = strings.ToLower(strings.TrimSpace(targetKey))
	if targetKey == "" {
		return
	}
	intents, err := e.store.ListIntents(ctx, taskID, domain.IntentDeferred)
	if err != nil {
		return
	}
	for _, in := range intents {
		k := in.TargetKey
		if k == "" {
			k = intentTargetKey(in.Summary)
		}
		if strings.ToLower(k) == targetKey {
			_ = e.store.SetIntentState(ctx, in.ID, domain.IntentApproved)
		}
	}
}

// cancelTargetIntents ends other solving attempts after a single-flag target
// (or a known complete multi-flag target) has been accepted.
func (e *Engine) cancelTargetIntents(ctx context.Context, taskID int64, targetKey string, skipIntentID int64) {
	key := strings.ToLower(strings.TrimSpace(targetKey))
	if key == "" {
		return
	}
	intents, err := e.store.ListIntents(ctx, taskID,
		domain.IntentProposed, domain.IntentApproved, domain.IntentQueued, domain.IntentDeferred,
		domain.IntentClaimed, domain.IntentRunning)
	if err != nil {
		return
	}
	for _, in := range intents {
		if in.ID == skipIntentID {
			continue
		}
		k := in.TargetKey
		if k == "" {
			k = intentTargetKey(in.Summary)
		}
		if strings.ToLower(k) != key {
			continue
		}
		if in.State == domain.IntentRunning || in.State == domain.IntentClaimed {
			works, _ := e.store.ListWorks(ctx, taskID)
			for _, w := range works {
				if w.IntentID != in.ID {
					continue
				}
				e.workMu.Lock()
				cancel := e.workCancels[w.ID]
				e.workMu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
		}
		_ = e.store.SetIntentState(ctx, in.ID, domain.IntentCancelled)
	}
}

func swarmLaneIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("lane-%d", i+1)
	}
	return out
}

func (e *Engine) finishSwarmLane(ctx context.Context, taskID int64, out workOutcome) {
	intentID := out.work.IntentID
	status := "failed"
	payload := "{}"
	if out.err == nil && out.result != nil {
		status = out.result.Status
		if data, err := json.Marshal(out.result); err == nil {
			payload = string(data)
		}
	}
	state := domain.WorkFailed
	if status == "done" {
		state = domain.WorkSucceeded
	}
	_ = e.store.FinishLaneWork(ctx, out.work.ID, state, status)
	_ = e.store.RecordLaneResult(ctx, out.work.ID, intentID, out.work.LaneID, status, payload)
	e.submitLaneFlags(ctx, taskID, out)
	_, _ = e.emit(ctx, taskID, domain.EventSwarmLaneDone, "worker", out.work.WorkerID, "work", out.work.ID,
		map[string]any{"intent_id": intentID, "lane_id": out.work.LaneID, "status": status})
	run, _ := e.store.GetSwarmRun(ctx, intentID)
	late := run != nil && run.State != "running"
	if status != "done" || late {
		reason := status
		if out.err != nil {
			reason = out.err.Error()
		} else if late {
			reason = "late_result_after_merge"
		}
		kind := "failed_lane"
		if late {
			kind = "late_result"
		}
		flags := flagPattern.FindAllString(payload, -1)
		sum := "lane " + out.work.LaneID + " " + reason
		_, _ = e.store.CreateQuarantine(ctx, domain.QuarantineItem{
			TaskID: taskID, IntentID: intentID, WorkID: out.work.ID, LaneID: out.work.LaneID,
			SourceKind: kind, Reason: reason, Summary: sum, PayloadJSON: payload,
			CandidateFlags: flags,
		})
	}
	_, _ = e.maybeMergeSwarm(ctx, taskID, intentID, false)
}

func (e *Engine) cancelRunningLanes(ctx context.Context, taskID, intentID int64) int {
	works, err := e.store.ListWorks(ctx, taskID)
	if err != nil {
		return 0
	}
	n := 0
	for _, w := range works {
		if w.IntentID != intentID || w.LaneID == "" || (w.State != domain.WorkRunning && w.State != domain.WorkLeased) {
			continue
		}
		e.workMu.Lock()
		cancel := e.workCancels[w.ID]
		e.workMu.Unlock()
		if cancel != nil {
			cancel()
			n++
		}
	}
	return n
}

func (e *Engine) submitLaneFlags(ctx context.Context, taskID int64, out workOutcome) {
	if out.result == nil || out.result.Partial || len(out.result.Findings) == 0 {
		return
	}
	task, _ := e.store.GetTask(ctx, taskID)
	intent, _ := e.store.GetIntent(ctx, out.work.IntentID)
	if task == nil || intent == nil {
		return
	}
	for _, spec := range out.result.Findings {
		finding := &domain.Finding{
			TaskID: taskID, IntentID: out.work.IntentID, Title: spec.Title,
			Severity: spec.Severity, Evidence: spec.Evidence, AssetIDs: spec.AssetIDs,
		}
		e.submitFindingFlags(ctx, taskID, task, intent.TargetKey, finding, nil)
	}
}

func (e *Engine) maybeMergeSwarm(ctx context.Context, taskID, intentID int64, force bool) (bool, error) {
	run, err := e.store.GetSwarmRun(ctx, intentID)
	if err != nil || run == nil || run.State != "running" {
		return false, nil
	}
	total, terminal, err := e.store.SwarmLaneTerminal(ctx, intentID)
	if err != nil || total == 0 {
		return false, nil
	}
	results, err := e.store.ListLaneResults(ctx, intentID)
	if err != nil {
		return false, nil
	}
	done := 0
	for _, r := range results {
		if r.Status == "done" {
			done++
		}
	}
	if done < run.Quorum {
		if terminal < total && !force {
			return false, nil
		}
		_ = e.store.FinalizeSwarm(ctx, intentID, "failed", domain.IntentFailed)
		if intent, err := e.store.GetIntent(ctx, intentID); err == nil {
			e.recordTargetOutcome(ctx, taskID, intent, nil, "swarm quorum not reached")
		}
		_, _ = e.emit(ctx, taskID, domain.EventSwarmFailed, "engine", "merger", "intent", intentID,
			map[string]any{"mode": run.Mode, "lanes": total, "done": done, "quorum": run.Quorum, "reason": "quorum not reached"})
		return false, nil
	}
	// Quorum/min_success is already enough. Give running lanes a bounded drain
	// window before freezing the merge; queued lanes can be cancelled now.
	if terminal < total && !force && e.cfg.DrainWindow > 0 {
		if !e.drainScheduled[intentID] {
			e.drainScheduled[intentID] = true
			go func(id int64) {
				time.Sleep(e.cfg.DrainWindow)
				select {
				case e.drainC <- id:
				default:
				}
			}(intentID)
			log.Printf("[engine] %s intent #%d draining running lanes for %s", run.Mode, intentID, e.cfg.DrainWindow)
		}
		return false, nil
	}
	if terminal < total {
		if n, _ := e.store.CancelQueuedLanes(ctx, intentID, "not needed after quorum"); n > 0 {
			log.Printf("[engine] %s intent #%d cancelled %d queued lanes after quorum", run.Mode, intentID, n)
		}
		if force {
			if n := e.cancelRunningLanes(ctx, taskID, intentID); n > 0 {
				log.Printf("[engine] %s intent #%d soft-cancelled %d running lanes at drain deadline", run.Mode, intentID, n)
			}
		}
	}
	task, _ := e.store.GetTask(ctx, taskID)
	policy := lanePolicyForTask(task)
	e.mergeSwarm(ctx, taskID, intentID, run, results, policy)
	return false, nil
}

func (e *Engine) mergeSwarm(ctx context.Context, taskID, intentID int64, run *store.SwarmRun, results []store.SwarmLaneResult, policy LanePolicy) {
	type merged struct {
		assets   []domain.AssetSpec
		facts    []domain.FactSpec
		findings []domain.FindingSpec
		evidence []domain.EvidenceSpec
	}
	m := merged{}
	assetSeen := map[string]bool{}
	factSeen := map[string]bool{}
	findingSeen := map[string]bool{}
	flagLane := map[string]map[string]bool{}
	for _, r := range results {
		if r.Status != "done" {
			continue
		}
		var wr domain.WorkResult
		if json.Unmarshal([]byte(r.ResultJSON), &wr) != nil {
			continue
		}
		for _, a := range wr.Assets {
			key := strings.ToLower(a.Type) + "\x00" + strings.ToLower(strings.TrimSpace(a.Value))
			if assetSeen[key] {
				continue
			}
			assetSeen[key] = true
			m.assets = append(m.assets, a)
		}
		for _, f := range wr.Facts {
			if crossTaskReference(f.Summary+" "+f.Detail+" "+f.Evidence, taskID) {
				continue
			}
			key := semanticFactFingerprint(f)
			if factSeen[key] {
				continue
			}
			factSeen[key] = true
			m.facts = append(m.facts, f)
		}
		for _, f := range wr.Findings {
			if crossTaskReference(f.Title+" "+f.Evidence, taskID) {
				continue
			}
			for _, flag := range flagPattern.FindAllString(f.Title+" "+f.Evidence, -1) {
				if flagLane[flag] == nil {
					flagLane[flag] = map[string]bool{}
				}
				flagLane[flag][r.LaneID] = true
			}
			key := strings.TrimSpace(f.Title) + "\x00" + strings.TrimSpace(f.Evidence) + "\x00" +
				strings.TrimSpace(f.SubjectKey) + "\x00" + strings.TrimSpace(f.CaseKey) + "\x00" + strings.TrimSpace(f.Observation)
			if findingSeen[key] {
				continue
			}
			findingSeen[key] = true
			m.findings = append(m.findings, f)
		}
		m.evidence = append(m.evidence, wr.Evidence...)
	}
	task, _ := e.store.GetTask(ctx, taskID)
	intent, _ := e.store.GetIntent(ctx, intentID)
	// Detect only true contradictions. Different flags, and one flag found by
	// multiple lanes for the same challenge, are valid corroboration.
	flagChallenges := map[string]map[string]bool{}
	flagEvidence := map[string][]string{}
	for _, f := range m.findings {
		challenge := challengeFromText(f.Title)
		if challenge == "" {
			continue
		}
		for _, flag := range flagPattern.FindAllString(f.Title+" "+f.Evidence, -1) {
			key := strings.ToLower(flag)
			if flagChallenges[key] == nil {
				flagChallenges[key] = map[string]bool{}
			}
			flagChallenges[key][challenge] = true
			flagEvidence[key] = append(flagEvidence[key], f.Title)
		}
	}
	for _, f := range m.facts {
		challenge := challengeFromText(f.Summary)
		if challenge == "" {
			continue
		}
		for _, flag := range flagPattern.FindAllString(f.Summary+" "+f.Detail+" "+f.Evidence, -1) {
			key := strings.ToLower(flag)
			if flagChallenges[key] == nil {
				flagChallenges[key] = map[string]bool{}
			}
			flagChallenges[key][challenge] = true
			flagEvidence[key] = append(flagEvidence[key], f.Summary)
		}
	}
	type conflictRecord struct {
		id          int64
		subjectHash string
		flagKey     string
		flag        string
	}
	var conflicts []conflictRecord
	for key, challenges := range flagChallenges {
		if len(challenges) < 2 {
			continue
		}
		flag := key
		for _, f := range m.findings {
			for _, candidate := range flagPattern.FindAllString(f.Title+" "+f.Evidence, -1) {
				if strings.ToLower(candidate) == key {
					flag = candidate
				}
			}
		}
		lanes := []string{}
		for lane := range flagLane[flag] {
			lanes = append(lanes, lane)
		}
		id, err := e.store.CreateConflict(ctx, domain.Conflict{
			TaskID: taskID, IntentID: intentID, Type: "flag_challenge_mismatch",
			Subject: flag, SubjectHash: conflictHashKey(key), LaneIDs: lanes, Evidence: flagEvidence[key],
		})
		if err != nil {
			log.Printf("[engine] create conflict: %v", err)
		}
		conflicts = append(conflicts, conflictRecord{id: id, subjectHash: conflictHashKey(key), flagKey: key, flag: flag})
	}
	// Finding inconsistency requires the same subject AND same case. Two
	// different strategies/cases are evidence diversity, not a conflict.
	findingObservations := map[string]map[string]bool{}
	findingFlag := map[string]string{}
	findingTitles := map[string][]string{}
	for _, f := range m.findings {
		if strings.TrimSpace(f.SubjectKey) == "" || strings.TrimSpace(f.CaseKey) == "" {
			continue
		}
		obs := strings.ToLower(strings.TrimSpace(f.Observation))
		if obs != "observed" && obs != "not_observed" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(f.SubjectKey)) + "\x00" + strings.ToLower(strings.TrimSpace(f.CaseKey))
		if findingObservations[key] == nil {
			findingObservations[key] = map[string]bool{}
		}
		findingObservations[key][obs] = true
		findingTitles[key] = append(findingTitles[key], f.Title)
		if flags := flagPattern.FindAllString(f.Title+" "+f.Evidence, -1); len(flags) > 0 {
			findingFlag[key] = strings.ToLower(flags[0])
		}
	}
	for key, obs := range findingObservations {
		if !obs["observed"] || !obs["not_observed"] {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		subject := parts[0]
		if len(parts) > 1 {
			subject += " / " + parts[1]
		}
		hash := conflictHashKey("finding\x00" + key)
		id, err := e.store.CreateConflict(ctx, domain.Conflict{
			TaskID: taskID, IntentID: intentID, Type: "inconsistent_observation",
			Subject: subject, SubjectHash: hash, Evidence: findingTitles[key],
		})
		if err != nil {
			log.Printf("[engine] create finding conflict: %v", err)
		}
		conflicts = append(conflicts, conflictRecord{id: id, subjectHash: hash, flagKey: findingFlag[key], flag: findingFlag[key]})
	}
	conflictDecisions := map[string]string{}
	if len(conflicts) > 0 {
		if reviewer, ok := e.rt.(runtime.ConflictRuntime); ok {
			conflictItems, _ := e.store.ListConflicts(ctx, taskID, 20)
			resp, err := reviewer.ReviewConflicts(ctx, &domain.ConflictReviewRequest{Task: task, Intent: intent, Conflicts: conflictItems})
			if err == nil {
				for _, d := range resp.Decisions {
					conflictDecisions[strings.ToLower(d.SubjectHash)] = d.Decision
				}
			}
		}
	}
	// Quorum is evaluated per flag, not per finding. Multiple different flags
	// are valid; a finding may contain confirmed and unconfirmed flags together.
	confirmedFlags := map[string]bool{}
	for flag, lanes := range flagLane {
		if len(lanes) >= run.Quorum {
			confirmedFlags[strings.ToLower(flag)] = true
		}
	}
	for _, c := range conflicts {
		decision := conflictDecisions[strings.ToLower(c.subjectHash)]
		if decision == "" {
			decision = "quarantine_all"
		}
		if decision == "keep_all" {
			_ = e.store.ResolveConflict(ctx, c.id, decision, "swarm_lead: keep all")
			continue
		}
		if decision == "needs_human" && task != nil && task.Mode != domain.ModeCTF {
			_ = e.store.ResolveConflict(ctx, c.id, decision, "swarm_lead: needs human")
			delete(confirmedFlags, c.flagKey)
			continue
		}
		// CTF: no human gate. A needs_human advice is downgraded to quarantine.
		_ = e.store.ResolveConflict(ctx, c.id, "quarantine_all", "conflicted flag quarantined; CTF continues")
		delete(confirmedFlags, c.flagKey)
	}
	// Flag submission happens at lane completion, not at merge. Merge only
	// combines information; per-flag quorum no longer suppresses a valid flag.
	keptFindings := m.findings
	if len(keptFindings) == 0 && len(m.facts) == 0 {
		_ = e.store.FinalizeSwarm(ctx, intentID, "failed", domain.IntentFailed)
		if intent, err := e.store.GetIntent(ctx, intentID); err == nil {
			e.recordTargetOutcome(ctx, taskID, intent, nil, "swarm merged no findings or facts")
		}
		_, _ = e.emit(ctx, taskID, domain.EventSwarmFailed, "engine", "merger", "intent", intentID,
			map[string]any{"mode": run.Mode, "lanes": run.Lanes, "quorum": run.Quorum, "reason": "no merged findings or facts"})
		return
	}
	for _, a := range m.assets {
		_, _ = e.store.RecordAssetForTask(ctx, taskID, a)
	}
	for _, f := range m.facts {
		_, _ = e.store.RecordFact(ctx, taskID, intentID, 0, f)
	}
	for _, f := range keptFindings {
		finding, err := e.store.RecordFinding(ctx, taskID, intentID, 0, f)
		if err == nil && policy.SubmitFlags() {
			// Lane flags are submitted immediately. The merger may still see a
			// newly assembled flag (for example fragments stitched into one
			// finding), so submit merged findings too. FlagAccepted and the
			// submission mutex keep this idempotent.
			e.submitFindingFlags(ctx, taskID, task, intent.TargetKey, finding, nil)
		}
	}
	for _, ev := range m.evidence {
		_, _ = e.store.RecordEvidence(ctx, taskID, 0, ev)
	}
	_ = e.store.FinalizeSwarm(ctx, intentID, "done", domain.IntentSucceeded)
	_, _ = e.emit(ctx, taskID, domain.EventSwarmMerged, "engine", "merger", "intent", intentID,
		map[string]any{
			"mode": run.Mode, "policy": policy.Name(), "lanes": run.Lanes, "quorum": run.Quorum,
			"facts": len(m.facts), "findings": len(keptFindings),
			"flags_seen": len(flagLane), "confirmed_flags": len(confirmedFlags), "conflicts": len(conflicts),
		})
	e.recordTargetOutcome(ctx, taskID, intent, &domain.WorkResult{
		Status: "done", Assets: m.assets, Facts: m.facts, Findings: keptFindings, Evidence: m.evidence,
	}, "")
	log.Printf("[engine] task %d %s intent #%d merged lanes=%d facts=%d findings=%d", taskID, run.Mode, intentID, len(results), len(m.facts), len(keptFindings))
}

var challengePattern = regexp.MustCompile(`(?i)\b([a-z]{1,4}-?\d{1,4})\b`)

func challengeFromText(text string) string {
	m := challengePattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

func conflictHashKey(s string) string {
	sum := sha256.Sum256([]byte("flag_challenge_mismatch\x00" + s))
	return hex.EncodeToString(sum[:])
}

var flagPattern = regexp.MustCompile(`(?i)flag\{[^}]{1,1000}\}`)
