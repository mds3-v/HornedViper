package store

import (
	"context"
	"encoding/json"

	"hornedviper/internal/domain"
)

func (s *Store) CreateChatMessage(ctx context.Context, taskID int64, role, content, status string, attachments ...domain.Attachment) (*domain.ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	attJSON, _ := json.Marshal(attachments)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_messages (task_id, role, content, status, attachments_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, role, content, status, string(attJSON), now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.ChatMessage{ID: id, TaskID: taskID, Role: role, Content: content, Status: status, Attachments: attachments, CreatedAt: msToTime(now)}, nil
}

func (s *Store) UpdateChatMessage(ctx context.Context, id int64, content, thinking, status, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`UPDATE chat_messages
		 SET content=?, thinking=?, status=?, error=?,
		     thinking_ms=CASE WHEN ? IN ('done','error','cancelled') THEN ? - created_at ELSE thinking_ms END
		 WHERE id=?`,
		content, thinking, status, errText, status, now, id)
	return err
}

func (s *Store) ListChatMessages(ctx context.Context, taskID int64, limit int) ([]domain.ChatMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, role, content, thinking, status, error, attachments_json, thinking_ms, created_at
		 FROM chat_messages WHERE task_id=? ORDER BY id ASC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChatMessage
	for rows.Next() {
		var m domain.ChatMessage
		var created int64
		var attJSON string
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Role, &m.Content, &m.Thinking, &m.Status, &m.Error, &attJSON, &m.ThinkingMS, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(attJSON), &m.Attachments)
		m.CreatedAt = msToTime(created)
		out = append(out, m)
	}
	return out, rows.Err()
}
