package store

import "context"

// FailStaleThinking marks generation placeholders left by a previous process as
// cancelled, so the UI does not poll forever after a restart.
func (s *Store) FailStaleThinking(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE chat_messages SET status='cancelled', error='server restarted',
		 content=CASE WHEN content='' THEN '已中断生成。' ELSE content END
		 WHERE status='thinking'`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversation_messages SET status='cancelled', error='server restarted',
		 content=CASE WHEN content='' THEN '已中断生成。' ELSE content END
		 WHERE status='thinking'`)
	return err
}
