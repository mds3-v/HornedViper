package store

import (
	"context"
	"encoding/json"

	"hornedviper/internal/domain"
)

// AddHint stores a human intervention as a hint node.
func (s *Store) AddHint(ctx context.Context, taskID int64, text string) (*domain.Hint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	payload := mustJSON(map[string]any{"text": text})
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO exploration_nodes
		 (task_id, kind, state, payload_json, priority, origin, created_by_role, created_at, updated_at)
		 VALUES (?, 'hint', 'active', ?, 0, 'human', 'human', ?, ?)`,
		taskID, payload, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.Hint{ID: id, TaskID: taskID, Text: text, CreatedAt: msToTime(now)}, nil
}

// ListHints lists human hints for a task, oldest first.
func (s *Store) ListHints(ctx context.Context, taskID int64, limit int) ([]domain.Hint, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload_json, created_at FROM exploration_nodes
		 WHERE task_id=? AND kind='hint' ORDER BY id ASC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Hint
	for rows.Next() {
		var h domain.Hint
		var payload string
		var created int64
		if err := rows.Scan(&h.ID, &payload, &created); err != nil {
			return nil, err
		}
		h.TaskID, h.CreatedAt = taskID, msToTime(created)
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(payload), &p)
		h.Text = p.Text
		out = append(out, h)
	}
	return out, rows.Err()
}
