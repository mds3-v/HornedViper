package store

import (
	"context"
	"strings"
)

func normalizeChallenge(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.TrimPrefix(code, "challenge:")
	code = strings.TrimPrefix(code, "challenge/")
	return code
}

func (s *Store) MarkChallengeCompleted(ctx context.Context, taskID int64, challenge string) error {
	challenge = normalizeChallenge(challenge)
	if taskID <= 0 || challenge == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO challenge_completions(task_id, challenge, completed_at) VALUES(?, ?, ?)
		 ON CONFLICT(task_id, challenge) DO UPDATE SET completed_at=excluded.completed_at`,
		taskID, challenge, nowMS())
	return err
}

func (s *Store) IsChallengeCompleted(ctx context.Context, taskID int64, challenge string) (bool, error) {
	challenge = normalizeChallenge(challenge)
	if taskID <= 0 || challenge == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM challenge_completions WHERE task_id=? AND challenge=?`,
		taskID, challenge).Scan(&n)
	return n > 0, err
}
