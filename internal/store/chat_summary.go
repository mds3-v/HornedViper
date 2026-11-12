package store

import (
	"context"
	"encoding/json"

	"hornedviper/internal/domain"
)

func (s *Store) GetChatSummary(ctx context.Context, taskID int64) (string, int64, error) {
	var summary string
	var covered int64
	err := s.db.QueryRowContext(ctx, `SELECT summary, covered_id FROM chat_summaries WHERE task_id=?`, taskID).Scan(&summary, &covered)
	if err != nil {
		return "", 0, err
	}
	return summary, covered, nil
}

func (s *Store) SetChatSummary(ctx context.Context, taskID int64, summary string, coveredID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_summaries (task_id, summary, covered_id, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(task_id) DO UPDATE SET summary=excluded.summary, covered_id=excluded.covered_id, updated_at=excluded.updated_at`,
		taskID, summary, coveredID, nowMS())
	return err
}

// ChatHistory returns messages after the covered summary and the latest N turns.
func (s *Store) ChatHistory(ctx context.Context, taskID int64, afterID int64, limit int) ([]domain.ChatMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, role, content, thinking, status, error, attachments_json, created_at
		 FROM chat_messages WHERE task_id=? AND id>? ORDER BY id DESC LIMIT ?`, taskID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChatMessage
	for rows.Next() {
		var m domain.ChatMessage
		var created int64
		var attJSON string
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Role, &m.Content, &m.Thinking, &m.Status, &m.Error, &attJSON, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(attJSON), &m.Attachments)
		m.CreatedAt = msToTime(created)
		out = append(out, m)
	}
	// Reverse to chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
