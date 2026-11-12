package store

import (
	"context"
	"strings"

	"hornedviper/internal/domain"
)

func (s *Store) RecordLLMCall(ctx context.Context, c domain.LLMCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	if !c.CreatedAt.IsZero() {
		now = c.CreatedAt.UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO llm_calls
		 (task_id, conversation_id, work_id, role, session_id, turn, provider, model,
		  request_json, response_json, thinking, tool_calls_json, usage_json,
		  latency_ms, error, request_hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.TaskID, c.ConversationID, c.WorkID, c.Role, c.SessionID, c.Turn, c.Provider, c.Model,
		c.RequestJSON, c.ResponseJSON, c.Thinking, c.ToolCallsJSON, c.UsageJSON,
		c.LatencyMS, c.Error, c.RequestHash, now)
	return err
}

func (s *Store) ListLLMCalls(ctx context.Context, taskID int64, limit int) ([]domain.LLMCall, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, task_id, conversation_id, work_id, role, session_id, turn, provider, model,
	             request_json, response_json, thinking, tool_calls_json, usage_json,
	             latency_ms, error, request_hash, created_at
	      FROM llm_calls`
	args := []any{}
	if taskID > 0 {
		q += ` WHERE task_id=?`
		args = append(args, taskID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
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

func (s *Store) GetLLMCall(ctx context.Context, id int64) (*domain.LLMCall, error) {
	var c domain.LLMCall
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, conversation_id, work_id, role, session_id, turn, provider, model,
		        request_json, response_json, thinking, tool_calls_json, usage_json,
		        latency_ms, error, request_hash, created_at
		 FROM llm_calls WHERE id=?`, id).
		Scan(&c.ID, &c.TaskID, &c.ConversationID, &c.WorkID, &c.Role, &c.SessionID, &c.Turn,
			&c.Provider, &c.Model, &c.RequestJSON, &c.ResponseJSON, &c.Thinking,
			&c.ToolCallsJSON, &c.UsageJSON, &c.LatencyMS, &c.Error, &c.RequestHash, &created)
	if err != nil {
		return nil, err
	}
	c.CreatedAt = msToTime(created)
	return &c, nil
}

func llmFilter(taskID, conversationID int64, search string) (string, []any) {
	var clauses []string
	var args []any
	switch {
	case taskID > 0:
		clauses = append(clauses, "task_id=?")
		args = append(args, taskID)
	case conversationID > 0:
		clauses = append(clauses, "conversation_id=?")
		args = append(args, conversationID)
	}
	if q := strings.TrimSpace(search); q != "" {
		like := "%" + q + "%"
		clauses = append(clauses, "(role LIKE ? OR model LIKE ? OR provider LIKE ? OR request_json LIKE ? OR response_json LIKE ? OR thinking LIKE ? OR tool_calls_json LIKE ? OR error LIKE ?)")
		args = append(args, like, like, like, like, like, like, like, like)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *Store) LLMCallGroups(ctx context.Context) ([]domain.LLMGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, conversation_id, COUNT(*), MAX(created_at) FROM llm_calls
		 WHERE task_id > 0 OR conversation_id > 0
		 GROUP BY task_id, conversation_id ORDER BY MAX(created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LLMGroup
	for rows.Next() {
		var g domain.LLMGroup
		var last int64
		if err := rows.Scan(&g.TaskID, &g.ConversationID, &g.Count, &last); err != nil {
			return nil, err
		}
		g.LastAt = msToTime(last)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) CountLLMCallsSearch(ctx context.Context, taskID, conversationID int64, search string) (int, error) {
	where, args := llmFilter(taskID, conversationID, search)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_calls`+where, args...).Scan(&n)
	return n, err
}

func (s *Store) ListLLMCallsPageSearch(ctx context.Context, taskID, conversationID int64, search string, limit, offset int) ([]domain.LLMCall, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	where, args := llmFilter(taskID, conversationID, search)
	query := `SELECT id, task_id, conversation_id, work_id, role, session_id, turn, provider, model,
	             request_json, response_json, thinking, tool_calls_json, usage_json,
	             latency_ms, error, request_hash, created_at
	      FROM llm_calls` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
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
