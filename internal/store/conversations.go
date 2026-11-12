package store

import (
	"context"
	"encoding/json"

	"hornedviper/internal/domain"
)

func (s *Store) CreateConversation(ctx context.Context, title, reasoningLevel, permissionMode string) (*domain.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (title, reasoning_level, permission_mode, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, title, reasoningLevel, permissionMode, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.Conversation{ID: id, Title: title, ReasoningLevel: reasoningLevel, PermissionMode: permissionMode, CreatedAt: msToTime(now), UpdatedAt: msToTime(now)}, nil
}

func (s *Store) GetConversation(ctx context.Context, id int64) (*domain.Conversation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, title, reasoning_level, permission_mode, created_at, updated_at FROM conversations WHERE id=?`, id)
	var c domain.Conversation
	var created, updated int64
	if err := row.Scan(&c.ID, &c.Title, &c.ReasoningLevel, &c.PermissionMode, &created, &updated); err != nil {
		return nil, err
	}
	c.CreatedAt = msToTime(created)
	c.UpdatedAt = msToTime(updated)
	return &c, nil
}

func (s *Store) ListConversations(ctx context.Context, limit int) ([]domain.Conversation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, reasoning_level, permission_mode, created_at, updated_at FROM conversations ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Conversation
	for rows.Next() {
		var c domain.Conversation
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Title, &c.ReasoningLevel, &c.PermissionMode, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt = msToTime(created)
		c.UpdatedAt = msToTime(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SetConversationPermissionMode(ctx context.Context, id int64, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET permission_mode=?, updated_at=? WHERE id=?`, mode, nowMS(), id)
	return err
}

func (s *Store) SetConversationReasoningLevel(ctx context.Context, id int64, level string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET reasoning_level=?, updated_at=? WHERE id=?`, level, nowMS(), id)
	return err
}

func (s *Store) TouchConversation(ctx context.Context, id int64, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if title != "" {
		_, err := s.db.ExecContext(ctx, `UPDATE conversations SET title=?, updated_at=? WHERE id=?`, title, nowMS(), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET updated_at=? WHERE id=?`, nowMS(), id)
	return err
}

func (s *Store) CreateConversationMessage(ctx context.Context, convID int64, role, content, status string, attachments ...domain.Attachment) (*domain.ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	attJSON, _ := json.Marshal(attachments)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO conversation_messages (conversation_id, role, content, status, attachments_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		convID, role, content, status, string(attJSON), now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE conversations SET updated_at=? WHERE id=?`, now, convID)
	return &domain.ChatMessage{ID: id, TaskID: convID, Role: role, Content: content, Status: status, Attachments: attachments, CreatedAt: msToTime(now)}, nil
}

func (s *Store) ListConversationMessages(ctx context.Context, convID int64, limit int) ([]domain.ChatMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, role, content, thinking, status, error, attachments_json, thinking_ms, created_at
		 FROM conversation_messages WHERE conversation_id=? ORDER BY id ASC LIMIT ?`, convID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChatMessage
	for rows.Next() {
		var m domain.ChatMessage
		var created int64
		var attJSON string
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Thinking, &m.Status, &m.Error, &attJSON, &m.ThinkingMS, &created); err != nil {
			return nil, err
		}
		m.TaskID = convID
		_ = json.Unmarshal([]byte(attJSON), &m.Attachments)
		m.CreatedAt = msToTime(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) UpdateConversationMessage(ctx context.Context, id int64, content, thinking, status, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversation_messages
		 SET content=?, thinking=?, status=?, error=?,
		     thinking_ms=CASE WHEN ? IN ('done','error','cancelled') THEN ? - created_at ELSE thinking_ms END
		 WHERE id=?`,
		content, thinking, status, errText, status, now, id)
	return err
}

func (s *Store) DeleteConversation(ctx context.Context, conversationID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var title string
	_ = tx.QueryRowContext(ctx, `SELECT title FROM conversations WHERE id=?`, conversationID).Scan(&title)
	if title != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO deleted_entities(kind, entity_id, title, deleted_at) VALUES('conversation', ?, ?, ?)
			 ON CONFLICT(kind, entity_id) DO UPDATE SET title=excluded.title, deleted_at=excluded.deleted_at`,
			conversationID, title, nowMS()); err != nil {
			return err
		}
	}
	stmts := []string{
		`DELETE FROM conversation_messages WHERE conversation_id=?`,
		`DELETE FROM conversation_summaries WHERE conversation_id=?`,
		`DELETE FROM chat_todos WHERE owner_type='conversation' AND owner_id=?`,
		`DELETE FROM llm_calls WHERE conversation_id=?`,
		`DELETE FROM conversations WHERE id=?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, conversationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeletedTitles(ctx context.Context, kind string) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT entity_id, title FROM deleted_entities WHERE kind=?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

func (s *Store) ListLLMCallsForTask(ctx context.Context, taskID int64) ([]domain.LLMCall, error) {
	return s.listLLMCallsWhere(ctx, "task_id=?", taskID)
}

func (s *Store) ListLLMCallsForConversation(ctx context.Context, conversationID int64) ([]domain.LLMCall, error) {
	return s.listLLMCallsWhere(ctx, "conversation_id=?", conversationID)
}

func (s *Store) listLLMCallsWhere(ctx context.Context, where string, id int64) ([]domain.LLMCall, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, conversation_id, work_id, role, session_id, turn, provider, model,
		        request_json, response_json, thinking, tool_calls_json, usage_json,
		        latency_ms, error, request_hash, created_at
		 FROM llm_calls WHERE `+where+` ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LLMCall
	for rows.Next() {
		var c domain.LLMCall
		var created int64
		if err := rows.Scan(&c.ID, &c.TaskID, &c.ConversationID, &c.WorkID, &c.Role, &c.SessionID, &c.Turn,
			&c.Provider, &c.Model, &c.RequestJSON, &c.ResponseJSON, &c.Thinking,
			&c.ToolCallsJSON, &c.UsageJSON, &c.LatencyMS, &c.Error, &c.RequestHash, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = msToTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}
