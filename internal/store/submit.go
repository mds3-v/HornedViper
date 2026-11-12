package store

import (
	"context"
	"database/sql"
)

// FlagAlreadySubmitted reports whether this task already recorded this flag hash.
func (s *Store) FlagAlreadySubmitted(ctx context.Context, taskID int64, hash string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM flag_submissions WHERE task_id=? AND flag_hash=?`, taskID, hash).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n > 0, err
}

// RecordFlagSubmission stores the submission outcome. Duplicate (task, flag) is ignored.
func (s *Store) RecordFlagSubmission(ctx context.Context, taskID, findingID int64, flag, hash string, accepted bool, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc := 0
	if accepted {
		acc = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO flag_submissions (task_id, finding_id, flag, flag_hash, accepted, message, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`, taskID, findingID, flag, hash, acc, message, nowMS())
	return err
}

// FlagAccepted reports whether this task has an accepted submission for hash.
// It deliberately ignores rejected/duplicate rows so progress accounting does
// not treat a failed attempt as a solved target.
func (s *Store) FlagAccepted(ctx context.Context, taskID int64, hash string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM flag_submissions WHERE task_id=? AND flag_hash=? AND accepted=1`, taskID, hash).Scan(&n)
	return n > 0, err
}
