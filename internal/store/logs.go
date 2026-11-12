package store

import (
	"context"

	"hornedviper/internal/domain"
)

func (s *Store) RecordLog(ctx context.Context, text string, createdAt int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if createdAt == 0 {
		createdAt = nowMS()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO logs (text, created_at) VALUES (?, ?)`, text, createdAt)
	return err
}

func (s *Store) ListLogs(ctx context.Context, limit int) ([]domain.LogEntry, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, text, created_at FROM logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LogEntry
	for rows.Next() {
		var e domain.LogEntry
		var created int64
		if err := rows.Scan(&e.ID, &e.Text, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = msToTime(created)
		out = append(out, e)
	}
	// Chronological for the viewer.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Store) PruneLogs(ctx context.Context, keep int) error {
	if keep <= 0 {
		keep = 50000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM logs WHERE id NOT IN (SELECT id FROM logs ORDER BY id DESC LIMIT ?)`, keep)
	return err
}
