package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"hornedviper/internal/domain"
)

// AppendEvent appends a workflow event and returns it.
func (s *Store) AppendEvent(ctx context.Context, taskID int64, typ domain.EventType, actorType, actorID, subjectType string, subjectID int64, payload map[string]any) (*domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO workflow_events
		 (task_id, type, actor_type, actor_id, subject_type, subject_id, payload_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, string(typ), actorType, actorID, subjectType, subjectID, jsonText(payload), now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.Event{
		ID: id, TaskID: taskID, Type: typ, ActorType: actorType, ActorID: actorID,
		SubjectType: subjectType, SubjectID: subjectID, Payload: payload,
		CreatedAt: msToTime(now),
	}, nil
}

// ListEvents returns events oldest-first (last `limit`).
func (s *Store) ListEvents(ctx context.Context, taskID int64, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, actor_type, actor_id, subject_type, subject_id, payload_json, created_at
		 FROM workflow_events WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var typ, payload string
		var subjectID *int64
		var created int64
		if err := rows.Scan(&e.ID, &typ, &e.ActorType, &e.ActorID, &e.SubjectType, &subjectID, &payload, &created); err != nil {
			return nil, err
		}
		e.TaskID, e.Type, e.CreatedAt = taskID, domain.EventType(typ), msToTime(created)
		if subjectID != nil {
			e.SubjectID = *subjectID
		}
		_ = json.Unmarshal([]byte(payload), &e.Payload)
		out = append(out, e)
	}
	// reverse to oldest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// AddConstraint adds an allow/deny operation constraint.
func (s *Store) AddConstraint(ctx context.Context, taskID int64, typ, text string) (*domain.Constraint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if typ != "allow" && typ != "deny" {
		return nil, fmt.Errorf("constraint type must be allow or deny")
	}
	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO task_constraints (task_id, type, text, created_at) VALUES (?, ?, ?, ?)`,
		taskID, typ, text, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.Constraint{ID: id, Type: typ, Text: text}, nil
}

// ListConstraints lists constraints for a task.
func (s *Store) ListConstraints(ctx context.Context, taskID int64) ([]domain.Constraint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, text FROM task_constraints WHERE task_id=? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Constraint
	for rows.Next() {
		var c domain.Constraint
		if err := rows.Scan(&c.ID, &c.Type, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddTodo adds a planner todo item.
func (s *Store) AddTodo(ctx context.Context, taskID int64, content string) (*domain.TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO planner_todos (task_id, content, status, created_at, updated_at)
		 VALUES (?, ?, 'pending', ?, ?)`, taskID, content, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.TodoItem{ID: id, Content: content, Status: "pending"}, nil
}

// ListTodos lists planner todo items.
func (s *Store) ListTodos(ctx context.Context, taskID int64) ([]domain.TodoItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, content, status FROM planner_todos WHERE task_id=? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TodoItem
	for rows.Next() {
		var t domain.TodoItem
		if err := rows.Scan(&t.ID, &t.Content, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTodo updates a todo status.
func (s *Store) UpdateTodo(ctx context.Context, id int64, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE planner_todos SET status=?, updated_at=? WHERE id=?`, status, nowMS(), id)
	return err
}

// PendingApprovals lists intent approvals still waiting for a human.
func (s *Store) PendingApprovals(ctx context.Context, taskID int64) ([]domain.Approval, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, intent_id, risk, dangerous_actions_json, decision, decided_by, reason, created_at
		 FROM approvals WHERE task_id=? AND decision='pending' ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Approval
	for rows.Next() {
		var a domain.Approval
		var risk, dangerous, decision string
		var by, reason sql.NullString
		var created int64
		if err := rows.Scan(&a.ID, &a.IntentID, &risk, &dangerous, &decision, &by, &reason, &created); err != nil {
			return nil, err
		}
		a.TaskID, a.Risk, a.Decision = taskID, domain.Risk(risk), domain.ApprovalDecision(decision)
		if by.Valid {
			a.DecidedBy = by.String
		}
		if reason.Valid {
			a.Reason = reason.String
		}
		a.CreatedAt = msToTime(created)
		_ = json.Unmarshal([]byte(dangerous), &a.DangerousActions)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GraphCounts returns node counts by kind.
func (s *Store) GraphCounts(ctx context.Context, taskID int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, COUNT(*) FROM exploration_nodes WHERE task_id=? GROUP BY kind`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// LatestEventTime returns the last event timestamp for debounce bookkeeping.
func (s *Store) LatestEventTime(ctx context.Context, taskID int64) (time.Time, error) {
	var ms int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(created_at), 0) FROM workflow_events WHERE task_id=?`, taskID).Scan(&ms)
	if err != nil {
		return time.Time{}, err
	}
	if ms == 0 {
		return time.Time{}, nil
	}
	return msToTime(ms), nil
}

// ListActivities lists activities, oldest-first (last `limit`).
func (s *Store) ListActivities(ctx context.Context, taskID int64, limit int) ([]domain.Activity, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, work_id, intent_id, role, kind, tool, tool_use_id, summary, detail, llm_call_id, created_at
		 FROM activities WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Activity
	for rows.Next() {
		var a domain.Activity
		var tool, toolUseID, summary, detail sql.NullString
		var created int64
		if err := rows.Scan(&a.ID, &a.WorkID, &a.IntentID, &a.Role, &a.Kind, &tool, &toolUseID, &summary, &detail, &a.LLMCallID, &created); err != nil {
			return nil, err
		}
		a.TaskID, a.CreatedAt = taskID, msToTime(created)
		if tool.Valid {
			a.Tool = tool.String
		}
		if toolUseID.Valid {
			a.ToolUseID = toolUseID.String
		}
		if summary.Valid {
			a.Summary = summary.String
		}
		if detail.Valid {
			a.Detail = detail.String
		}
		out = append(out, a)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// ResolveApproval applies a human decision to a pending intent approval.
func (s *Store) ResolveApproval(ctx context.Context, approvalID int64, decision domain.ApprovalDecision, decidedBy, reason string) (*domain.Approval, error) {
	if decision != domain.ApprovalApproved && decision != domain.ApprovalRejected {
		return nil, fmt.Errorf("decision must be approved or rejected")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var a domain.Approval
	var intentID, taskID int64
	var risk, dangerous, current string
	if err := tx.QueryRowContext(ctx,
		`SELECT task_id, intent_id, risk, dangerous_actions_json, decision
		 FROM approvals WHERE id=?`, approvalID).Scan(&taskID, &intentID, &risk, &dangerous, &current); err != nil {
		return nil, err
	}
	if current != string(domain.ApprovalPending) {
		return nil, fmt.Errorf("approval %d is not pending", approvalID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE approvals SET decision=?, decided_by=?, reason=?, decided_at=? WHERE id=?`,
		string(decision), decidedBy, reason, now, approvalID); err != nil {
		return nil, err
	}
	next := domain.IntentApproved
	if decision == domain.ApprovalRejected {
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
	a.ID, a.TaskID, a.IntentID = approvalID, taskID, intentID
	a.Risk, a.Decision, a.DecidedBy, a.Reason = domain.Risk(risk), decision, decidedBy, reason
	a.CreatedAt, a.DecidedAt = msToTime(now), ptrTime(msToTime(now))
	_ = json.Unmarshal([]byte(dangerous), &a.DangerousActions)
	return &a, nil
}

// ListRecentEvents lists events across tasks, newest first.
func (s *Store) ListRecentEvents(ctx context.Context, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, type, actor_type, actor_id, subject_type, subject_id, payload_json, created_at
		 FROM workflow_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var typ, payload string
		var subjectID sql.NullInt64
		var created int64
		if err := rows.Scan(&e.ID, &e.TaskID, &typ, &e.ActorType, &e.ActorID, &e.SubjectType, &subjectID, &payload, &created); err != nil {
			return nil, err
		}
		e.Type, e.CreatedAt = domain.EventType(typ), msToTime(created)
		if subjectID.Valid {
			e.SubjectID = subjectID.Int64
		}
		_ = json.Unmarshal([]byte(payload), &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}
