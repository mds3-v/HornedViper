package store

import (
	"context"
	"strings"

	"hornedviper/internal/domain"
)

func (s *Store) RecordTraffic(ctx context.Context, t domain.Traffic) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	if !t.CreatedAt.IsZero() {
		now = t.CreatedAt.UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO traffic
		 (task_id, conversation_id, work_id, intent_id, method, host, url, status_code, content_type,
		  request_raw, response_raw, request_size, response_size, duration_ms, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TaskID, t.ConversationID, t.WorkID, t.IntentID, t.Method, t.Host, t.URL, t.StatusCode, t.ContentType,
		t.RequestRaw, t.ResponseRaw, t.RequestSize, t.ResponseSize, t.DurationMS, t.Error, now)
	return err
}

func (s *Store) ListTraffic(ctx context.Context, limit int) ([]domain.Traffic, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, conversation_id, work_id, intent_id, method, host, url, status_code, content_type,
		        request_raw, response_raw, request_size, response_size, duration_ms, error, created_at
		 FROM traffic ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Traffic
	for rows.Next() {
		var t domain.Traffic
		var created int64
		if err := rows.Scan(&t.ID, &t.TaskID, &t.ConversationID, &t.WorkID, &t.IntentID, &t.Method, &t.Host, &t.URL,
			&t.StatusCode, &t.ContentType, &t.RequestRaw, &t.ResponseRaw, &t.RequestSize,
			&t.ResponseSize, &t.DurationMS, &t.Error, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = msToTime(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TrafficGroups(ctx context.Context) ([]domain.TrafficGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, conversation_id, COUNT(*), MAX(created_at) FROM traffic
		 WHERE task_id > 0 OR conversation_id > 0
		 GROUP BY task_id, conversation_id ORDER BY MAX(created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TrafficGroup
	for rows.Next() {
		var g domain.TrafficGroup
		var last int64
		if err := rows.Scan(&g.TaskID, &g.ConversationID, &g.Count, &last); err != nil {
			return nil, err
		}
		g.LastAt = msToTime(last)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) CountTraffic(ctx context.Context, taskID, conversationID int64) (int, error) {
	return s.CountTrafficSearch(ctx, taskID, conversationID, "")
}

func trafficFilter(taskID, conversationID int64, search string) (string, []any) {
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
		clauses = append(clauses, "(url LIKE ? OR host LIKE ? OR method LIKE ? OR request_raw LIKE ? OR response_raw LIKE ?)")
		args = append(args, like, like, like, like, like)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *Store) CountTrafficSearch(ctx context.Context, taskID, conversationID int64, search string) (int, error) {
	where, args := trafficFilter(taskID, conversationID, search)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic`+where, args...).Scan(&n)
	return n, err
}

func (s *Store) ListTrafficPage(ctx context.Context, taskID, conversationID int64, limit, offset int) ([]domain.Traffic, error) {
	return s.ListTrafficPageSearch(ctx, taskID, conversationID, "", limit, offset)
}

func (s *Store) ListTrafficPageSearch(ctx context.Context, taskID, conversationID int64, search string, limit, offset int) ([]domain.Traffic, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	where, args := trafficFilter(taskID, conversationID, search)
	query := `SELECT id, task_id, conversation_id, work_id, intent_id, method, host, url, status_code, content_type,
		request_raw, response_raw, request_size, response_size, duration_ms, error, created_at FROM traffic` + where
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Traffic
	for rows.Next() {
		var t domain.Traffic
		var created int64
		if err := rows.Scan(&t.ID, &t.TaskID, &t.ConversationID, &t.WorkID, &t.IntentID, &t.Method, &t.Host, &t.URL,
			&t.StatusCode, &t.ContentType, &t.RequestRaw, &t.ResponseRaw, &t.RequestSize,
			&t.ResponseSize, &t.DurationMS, &t.Error, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = msToTime(created)
		out = append(out, t)
	}
	return out, rows.Err()
}
