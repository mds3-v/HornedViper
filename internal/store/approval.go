package store

import (
	"context"
	"encoding/json"

	"hornedviper/internal/domain"
)

// ListApprovals lists approval records for a task, newest first.
func (s *Store) ListApprovals(ctx context.Context, taskID int64, limit int) ([]domain.Approval, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, intent_id, risk, dangerous_actions_json, decision, decided_by, reason, created_at, decided_at
		 FROM approvals WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Approval
	for rows.Next() {
		var a domain.Approval
		var risk, dangerous, decision string
		var by, reason sqlNullString
		var created int64
		var decided sqlNullInt64
		if err := rows.Scan(&a.ID, &a.TaskID, &a.IntentID, &risk, &dangerous, &decision, &by, &reason, &created, &decided); err != nil {
			return nil, err
		}
		a.Risk, a.Decision = domain.Risk(risk), domain.ApprovalDecision(decision)
		if by.ok {
			a.DecidedBy = by.s
		}
		if reason.ok {
			a.Reason = reason.s
		}
		a.CreatedAt = msToTime(created)
		if decided.ok {
			t := msToTime(decided.v)
			a.DecidedAt = &t
		}
		_ = json.Unmarshal([]byte(dangerous), &a.DangerousActions)
		out = append(out, a)
	}
	return out, rows.Err()
}

type sqlNullString struct {
	s  string
	ok bool
}

func (n *sqlNullString) Scan(v any) error {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		n.s, n.ok = x, true
	case []byte:
		n.s, n.ok = string(x), true
	}
	return nil
}

type sqlNullInt64 struct {
	v  int64
	ok bool
}

func (n *sqlNullInt64) Scan(v any) error {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case int64:
		n.v, n.ok = x, true
	case int:
		n.v, n.ok = int64(x), true
	}
	return nil
}
