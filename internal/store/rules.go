package store

import (
	"context"
	"database/sql"

	"hornedviper/internal/domain"
)

func (s *Store) ListInterceptRules(ctx context.Context) ([]domain.InterceptRule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, priority, target, match_type, pattern, mode, enabled, builtin, created_at, updated_at
		 FROM intercept_rules ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.InterceptRule
	for rows.Next() {
		var r domain.InterceptRule
		var enabled, builtin int
		var created, updated int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Priority, &r.Target, &r.MatchType, &r.Pattern, &r.Mode, &enabled, &builtin, &created, &updated); err != nil {
			return nil, err
		}
		r.Enabled, r.Builtin = enabled == 1, builtin == 1
		r.CreatedAt, r.UpdatedAt = msToTime(created), msToTime(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetInterceptRule(ctx context.Context, id int64) (*domain.InterceptRule, error) {
	var r domain.InterceptRule
	var enabled, builtin int
	var created, updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, priority, target, match_type, pattern, mode, enabled, builtin, created_at, updated_at
		 FROM intercept_rules WHERE id=?`, id).Scan(
		&r.ID, &r.Name, &r.Priority, &r.Target, &r.MatchType, &r.Pattern, &r.Mode, &enabled, &builtin, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Enabled, r.Builtin = enabled == 1, builtin == 1
	r.CreatedAt, r.UpdatedAt = msToTime(created), msToTime(updated)
	return &r, nil
}

func (s *Store) SaveInterceptRule(ctx context.Context, r domain.InterceptRule) (*domain.InterceptRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	enabled, builtin := 0, 0
	if r.Enabled {
		enabled = 1
	}
	if r.Builtin {
		builtin = 1
	}
	var id int64
	if r.ID > 0 {
		_, err := s.db.ExecContext(ctx,
			`UPDATE intercept_rules SET name=?, priority=?, target=?, match_type=?, pattern=?, mode=?, enabled=?, updated_at=? WHERE id=?`,
			r.Name, r.Priority, r.Target, r.MatchType, r.Pattern, r.Mode, enabled, now, r.ID)
		if err != nil {
			return nil, err
		}
		id = r.ID
	} else {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO intercept_rules (name, priority, target, match_type, pattern, mode, enabled, builtin, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Name, r.Priority, r.Target, r.MatchType, r.Pattern, r.Mode, enabled, builtin, now, now)
		if err != nil {
			return nil, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return nil, err
		}
	}
	return s.getRuleNoLock(ctx, id)
}

func (s *Store) getRuleNoLock(ctx context.Context, id int64) (*domain.InterceptRule, error) {
	var r domain.InterceptRule
	var enabled, builtin int
	var created, updated int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, name, priority, target, match_type, pattern, mode, enabled, builtin, created_at, updated_at
		 FROM intercept_rules WHERE id=?`, id).Scan(
		&r.ID, &r.Name, &r.Priority, &r.Target, &r.MatchType, &r.Pattern, &r.Mode, &enabled, &builtin, &created, &updated); err != nil {
		return nil, err
	}
	r.Enabled, r.Builtin = enabled == 1, builtin == 1
	r.CreatedAt, r.UpdatedAt = msToTime(created), msToTime(updated)
	return &r, nil
}

func (s *Store) DeleteInterceptRule(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM intercept_rules WHERE id=? AND builtin=0`, id)
	return err
}

func (s *Store) SetInterceptRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE intercept_rules SET enabled=?, updated_at=? WHERE id=?`, v, nowMS(), id)
	return err
}
