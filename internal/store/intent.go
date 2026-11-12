package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"hornedviper/internal/domain"
)

type intentPayload struct {
	TargetKey        string         `json:"target_key,omitempty"`
	StrategyTag      string         `json:"strategy_tag,omitempty"`
	ExpectedFlags    int            `json:"expected_flags,omitempty"`
	CloseTarget      bool           `json:"close_target,omitempty"`
	GoalIDs          []int64        `json:"goal_ids"`
	Summary          string         `json:"summary"`
	Rationale        string         `json:"rationale"`
	ParentFactIDs    []int64        `json:"parent_fact_ids,omitempty"`
	AssetIDs         []int64        `json:"asset_ids,omitempty"`
	Priority         int            `json:"priority"`
	Risk             string         `json:"risk"`
	DangerousActions []string       `json:"dangerous_actions,omitempty"`
	ExecutionMode    string         `json:"execution_mode"`
	Budget           map[string]any `json:"budget,omitempty"`
	RequiredTools    []string       `json:"required_tools,omitempty"`
	ExpectedOutputs  []string       `json:"expected_outputs,omitempty"`
}

// CreateIntent persists a planner intent as an exploration node.
func (s *Store) CreateIntent(ctx context.Context, taskID int64, spec domain.IntentSpec, approvalID int64, state domain.IntentState) (*domain.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	payload := mustJSON(intentPayload{
		TargetKey: spec.TargetKey, StrategyTag: spec.StrategyTag, ExpectedFlags: spec.ExpectedFlags, CloseTarget: spec.CloseTarget,
		GoalIDs: spec.GoalIDs, Summary: spec.Summary, Rationale: spec.Rationale,
		ParentFactIDs: spec.ParentFactIDs, AssetIDs: spec.AssetIDs, Priority: spec.Priority,
		Risk: string(spec.Risk), DangerousActions: spec.DangerousActions,
		ExecutionMode: string(spec.ExecutionMode), Budget: spec.Budget,
		RequiredTools: spec.RequiredTools, ExpectedOutputs: spec.ExpectedOutputs,
	})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO exploration_nodes
		 (task_id, kind, state, payload_json, priority, origin, created_by_role, created_at, updated_at)
		 VALUES (?, 'intent', ?, ?, ?, 'planner', 'planner', ?, ?)`,
		taskID, string(state), payload, spec.Priority, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, goalID := range spec.GoalIDs {
		if goalID == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exploration_edges (task_id, src_id, dst_id, rel, created_at)
			 VALUES (?, ?, ?, 'spawns', ?)`, taskID, goalID, id, now); err != nil {
			return nil, err
		}
	}
	for _, parentID := range spec.ParentFactIDs {
		if parentID == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exploration_edges (task_id, src_id, dst_id, rel, created_at)
			 VALUES (?, ?, ?, 'derived_from', ?)`, taskID, parentID, id, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.Intent{
		ID: id, TaskID: taskID, GoalIDs: spec.GoalIDs, Summary: spec.Summary,
		Rationale: spec.Rationale, ParentFactIDs: spec.ParentFactIDs, AssetIDs: spec.AssetIDs,
		Priority: spec.Priority, Risk: spec.Risk, DangerousActions: spec.DangerousActions,
		ExecutionMode: spec.ExecutionMode, Budget: spec.Budget, RequiredTools: spec.RequiredTools,
		ExpectedOutputs: spec.ExpectedOutputs, TargetKey: spec.TargetKey,
		StrategyTag: spec.StrategyTag, ExpectedFlags: spec.ExpectedFlags, CloseTarget: spec.CloseTarget,
		State: state, ApprovalID: approvalID,
		CreatedAt: msToTime(now),
	}, nil
}

func scanIntent(row rowScanner) (*domain.Intent, error) {
	var in domain.Intent
	var state, payload string
	var priority int
	var created int64
	if err := row.Scan(&in.ID, &in.TaskID, &state, &payload, &priority, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	in.State = domain.IntentState(state)
	in.Priority = priority
	in.CreatedAt = msToTime(created)
	var p intentPayload
	_ = json.Unmarshal([]byte(payload), &p)
	in.GoalIDs, in.Summary, in.Rationale = p.GoalIDs, p.Summary, p.Rationale
	in.ParentFactIDs, in.AssetIDs = p.ParentFactIDs, p.AssetIDs
	in.Risk, in.DangerousActions = domain.Risk(p.Risk), p.DangerousActions
	in.ExecutionMode, in.Budget = domain.ExecutionMode(p.ExecutionMode), p.Budget
	in.RequiredTools, in.ExpectedOutputs = p.RequiredTools, p.ExpectedOutputs
	in.TargetKey, in.StrategyTag, in.ExpectedFlags = p.TargetKey, p.StrategyTag, p.ExpectedFlags
	in.CloseTarget = p.CloseTarget
	return &in, nil
}

// GetIntent loads one intent node.
func (s *Store) GetIntent(ctx context.Context, id int64) (*domain.Intent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, state, payload_json, priority, created_at
		 FROM exploration_nodes WHERE id=? AND kind='intent'`, id)
	return scanIntent(row)
}

// ListIntents lists intents, optionally filtered by state.
func (s *Store) ListIntents(ctx context.Context, taskID int64, states ...domain.IntentState) ([]*domain.Intent, error) {
	q := `SELECT id, task_id, state, payload_json, priority, created_at
	      FROM exploration_nodes WHERE task_id=? AND kind='intent'`
	args := []any{taskID}
	if len(states) > 0 {
		ph := make([]string, 0, len(states))
		for _, st := range states {
			ph = append(ph, "?")
			args = append(args, string(st))
		}
		q += " AND state IN (" + strings.Join(ph, ",") + ")"
	}
	q += " ORDER BY priority DESC, id ASC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Intent
	for rows.Next() {
		in, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// ApproveIntent records an approval decision and moves the intent to approved/rejected.
func (s *Store) ApproveIntent(ctx context.Context, taskID, intentID int64, decision domain.ApprovalDecision, decidedBy, reason string, risk domain.Risk, dangerous []string) (*domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO approvals (task_id, intent_id, risk, dangerous_actions_json, decision, decided_by, reason, created_at, decided_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, intentID, string(risk), jsonText(dangerous), string(decision), decidedBy, reason, now, now)
	if err != nil {
		return nil, err
	}
	approvalID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	next := domain.IntentApproved
	if decision == domain.ApprovalRejected || decision == domain.ApprovalExpired {
		next = domain.IntentFailed
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE exploration_nodes SET state=?, updated_at=? WHERE id=? AND kind='intent'`,
		string(next), now, intentID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	t := msToTime(now)
	return &domain.Approval{
		ID: approvalID, TaskID: taskID, IntentID: intentID, Risk: risk,
		DangerousActions: dangerous, Decision: decision, DecidedBy: decidedBy,
		Reason: reason, CreatedAt: t, DecidedAt: &t,
	}, nil
}

// CreatePendingApproval records an approval that needs a human. The intent stays proposed.
func (s *Store) CreatePendingApproval(ctx context.Context, taskID, intentID int64, risk domain.Risk, dangerous []string) (*domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO approvals (task_id, intent_id, risk, dangerous_actions_json, decision, created_at)
		 VALUES (?, ?, ?, ?, 'pending', ?)`,
		taskID, intentID, string(risk), jsonText(dangerous), now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.Approval{
		ID: id, TaskID: taskID, IntentID: intentID, Risk: risk,
		DangerousActions: dangerous, Decision: domain.ApprovalPending,
		CreatedAt: msToTime(now),
	}, nil
}

// ClaimNextIntent atomically claims the highest-priority approved/queued intent.
func (s *Store) ClaimNextIntent(ctx context.Context, taskID int64, workerID string, lease time.Duration) (*domain.Work, *domain.Intent, error) {
	return s.claimNextIntent(ctx, taskID, workerID, lease, 0, nil)
}

// ClaimNextIntentFiltered claims the highest effective-priority intent that
// matches the fast/normal lane selector. Priority aging prevents a stream of
// new high-priority intents from starving an older submit/verify intent.
func (s *Store) ClaimNextIntentFiltered(ctx context.Context, taskID int64, workerID string, lease, aging time.Duration, wantFast *bool) (*domain.Work, *domain.Intent, error) {
	return s.claimNextIntent(ctx, taskID, workerID, lease, aging, wantFast)
}

func (s *Store) claimNextIntent(ctx context.Context, taskID int64, workerID string, lease, aging time.Duration, wantFast *bool) (*domain.Work, *domain.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	agingMS := aging.Milliseconds()
	if agingMS <= 0 {
		agingMS = 1
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload_json FROM exploration_nodes
		 WHERE task_id=? AND kind='intent' AND state IN ('approved','queued')
		   AND COALESCE(json_extract(payload_json,'$.execution_mode'),'single') NOT IN ('swarm','shard')
		 ORDER BY (priority + ((? - created_at) / ?)) DESC, id ASC`,
		taskID, now, agingMS)
	if err != nil {
		return nil, nil, err
	}
	var intentID int64
	for rows.Next() {
		var id int64
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if wantFast != nil {
			var p intentPayload
			if json.Unmarshal([]byte(payload), &p) != nil {
				continue
			}
			candidate := &domain.Intent{Summary: p.Summary, ExpectedOutputs: p.ExpectedOutputs, Budget: p.Budget}
			if domain.IsFastIntent(candidate) != *wantFast {
				continue
			}
		}
		intentID = id
		break
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if intentID == 0 {
		return nil, nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE exploration_nodes SET state='claimed', updated_at=?
		 WHERE id=? AND state IN ('approved','queued')`, now, intentID)
	if err != nil {
		return nil, nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil, nil
	}
	leaseAt := now + lease.Milliseconds()
	wres, err := tx.ExecContext(ctx,
		`INSERT INTO works (task_id, intent_id, worker_id, state, lease_expires_at, started_at)
		 VALUES (?, ?, ?, 'leased', ?, ?)`, taskID, intentID, workerID, leaseAt, now)
	if err != nil {
		return nil, nil, err
	}
	workID, err := wres.LastInsertId()
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE works SET state='running' WHERE id=?`, workID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	intent, err := s.getIntentNoLock(ctx, intentID)
	if err != nil {
		return nil, nil, err
	}
	return &domain.Work{
		ID: workID, TaskID: taskID, IntentID: intentID, WorkerID: workerID,
		State: domain.WorkRunning, StartedAt: ptrTime(msToTime(now)),
		LeaseExpiresAt: ptrTime(msToTime(leaseAt)),
	}, intent, nil
}

func (s *Store) getIntentNoLock(ctx context.Context, id int64) (*domain.Intent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, state, payload_json, priority, created_at
		 FROM exploration_nodes WHERE id=? AND kind='intent'`, id)
	return scanIntent(row)
}

func ptrTime(t time.Time) *time.Time { return &t }

// FinishWork finalizes a work and its intent.
func (s *Store) FinishWork(ctx context.Context, workID int64, state domain.WorkState, stopReason string, tokensIn, tokensOut int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var intentID int64
	if err := tx.QueryRowContext(ctx, `SELECT intent_id FROM works WHERE id=?`, workID).Scan(&intentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE works SET state=?, ended_at=?, stop_reason=?, tokens_in=?, tokens_out=? WHERE id=?`,
		string(state), now, stopReason, tokensIn, tokensOut, workID); err != nil {
		return err
	}
	intentState := domain.IntentSucceeded
	switch state {
	case domain.WorkFailed, domain.WorkTimedOut, domain.WorkLost:
		intentState = domain.IntentFailed
	case domain.WorkCancelled:
		intentState = domain.IntentCancelled
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE exploration_nodes SET state=?, updated_at=? WHERE id=? AND kind='intent'`,
		string(intentState), now, intentID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetWork loads one work.
func (s *Store) GetWork(ctx context.Context, id int64) (*domain.Work, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, intent_id, worker_id, lane_id, state, lease_expires_at,
		        started_at, ended_at, stop_reason, tokens_in, tokens_out
		 FROM works WHERE id=?`, id)
	return scanWork(row)
}

func scanWork(row rowScanner) (*domain.Work, error) {
	var w domain.Work
	var worker, state string
	var lane, stop sql.NullString
	var lease, started, ended sql.NullInt64
	var tin, tout int64
	if err := row.Scan(&w.ID, &w.TaskID, &w.IntentID, &worker, &lane, &state,
		&lease, &started, &ended, &stop, &tin, &tout); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	w.WorkerID = worker
	if lane.Valid {
		w.LaneID = lane.String
	}
	w.State = domain.WorkState(state)
	w.LeaseExpiresAt = timePtr(lease)
	w.StartedAt = timePtr(started)
	w.EndedAt = timePtr(ended)
	if stop.Valid {
		w.StopReason = stop.String
	}
	w.TokensIn, w.TokensOut = tin, tout
	return &w, nil
}

// ListWorks lists works for a task.
func (s *Store) ListWorks(ctx context.Context, taskID int64) ([]*domain.Work, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, intent_id, worker_id, lane_id, state, lease_expires_at,
		        started_at, ended_at, stop_reason, tokens_in, tokens_out
		 FROM works WHERE task_id=? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Work
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CountIntentsInState counts intents in a state (used for scheduling decisions).
func (s *Store) CountIntentsInState(ctx context.Context, taskID int64, states ...domain.IntentState) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	ph := make([]string, 0, len(states))
	args := []any{taskID}
	for _, st := range states {
		ph = append(ph, "?")
		args = append(args, string(st))
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM exploration_nodes WHERE task_id=? AND kind='intent' AND state IN (`+
			strings.Join(ph, ",")+`)`, args...).Scan(&n)
	return n, err
}
