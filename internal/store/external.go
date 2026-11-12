package store

import (
	"context"
	"database/sql"
)

func (s *Store) LinkExternalTask(ctx context.Context, platform, externalID string, taskID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO external_tasks (platform, external_id, task_id, created_at) VALUES (?, ?, ?, ?)`,
		platform, externalID, taskID, nowMS())
	return err
}

func (s *Store) TaskIDForExternal(ctx context.Context, platform, externalID string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT task_id FROM external_tasks WHERE platform=? AND external_id=?`, platform, externalID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (s *Store) ExternalForTask(ctx context.Context, platform string, taskID int64) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT external_id FROM external_tasks WHERE platform=? AND task_id=?`, platform, taskID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}
