package store

import "context"

func (s *Store) GetConversationSummary(ctx context.Context, convID int64) (string, int64, error) {
	var summary string
	var covered int64
	err := s.db.QueryRowContext(ctx,
		`SELECT summary, covered_id FROM conversation_summaries WHERE conversation_id=?`, convID).
		Scan(&summary, &covered)
	if err != nil {
		return "", 0, nil
	}
	return summary, covered, nil
}

func (s *Store) SetConversationSummary(ctx context.Context, convID int64, summary string, coveredID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversation_summaries(conversation_id, summary, covered_id, updated_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(conversation_id) DO UPDATE SET summary=excluded.summary, covered_id=excluded.covered_id, updated_at=excluded.updated_at`,
		convID, summary, coveredID, nowMS())
	return err
}
