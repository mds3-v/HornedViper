package store

import (
	"context"
	"database/sql"
	"strings"

	"hornedviper/internal/domain"
)

// SeedManagedTools inserts built-in catalog rows without overwriting edits or
// switches. Categories are created from the catalog labels on first start.
func (s *Store) SeedManagedTools(ctx context.Context, catalog []domain.ManagedTool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	for _, t := range catalog {
		catID := int64(0)
		if strings.TrimSpace(t.Category) != "" {
			if _, err := s.db.ExecContext(ctx,
				`INSERT OR IGNORE INTO tool_categories(name, sort_order, created_at) VALUES(?, 0, ?)`,
				t.Category, now); err != nil {
				return err
			}
			if err := s.db.QueryRowContext(ctx,
				`SELECT id FROM tool_categories WHERE name=?`, t.Category).Scan(&catID); err != nil {
				return err
			}
		}
		enabled := 1
		if t.Enabled {
			enabled = 1
		}
		// Migrate the old per-scope switch if it exists.
		var old int
		if err := s.db.QueryRowContext(ctx,
			`SELECT enabled FROM tool_settings WHERE scope=? AND name=?`, t.Scope, t.Name).Scan(&old); err == nil {
			enabled = old
		}
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO managed_tools
			 (scope, name, display, description, category_id, enabled, essential, builtin, deleted, kind, config_json, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, '{}', ?, ?)
			 ON CONFLICT(scope, name) DO NOTHING`,
			t.Scope, t.Name, t.Display, t.Description, catID, enabled, boolInt(t.Essential), boolInt(t.Builtin), t.Kind, now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) ListManagedTools(ctx context.Context, includeDeleted bool) ([]domain.ManagedTool, error) {
	q := `SELECT m.id, m.scope, m.name, m.display, m.description, m.category_id,
	             COALESCE(c.name, ''), m.enabled, m.essential, m.builtin, m.deleted,
	             m.kind, m.config_json, m.created_at, m.updated_at
	      FROM managed_tools m LEFT JOIN tool_categories c ON c.id=m.category_id`
	if !includeDeleted {
		q += ` WHERE m.deleted=0`
	}
	q += ` ORDER BY m.scope, m.id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ManagedTool
	for rows.Next() {
		var t domain.ManagedTool
		var enabled, essential, builtin, deleted int
		var created, updated int64
		if err := rows.Scan(&t.ID, &t.Scope, &t.Name, &t.Display, &t.Description, &t.CategoryID,
			&t.Category, &enabled, &essential, &builtin, &deleted, &t.Kind, &t.ConfigJSON,
			&created, &updated); err != nil {
			return nil, err
		}
		t.Enabled, t.Essential, t.Builtin, t.Deleted = enabled != 0, essential != 0, builtin != 0, deleted != 0
		t.CreatedAt, t.UpdatedAt = msToTime(created), msToTime(updated)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetManagedTool(ctx context.Context, id int64) (domain.ManagedTool, error) {
	var t domain.ManagedTool
	var enabled, essential, builtin, deleted int
	var created, updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT m.id, m.scope, m.name, m.display, m.description, m.category_id,
		        COALESCE(c.name, ''), m.enabled, m.essential, m.builtin, m.deleted,
		        m.kind, m.config_json, m.created_at, m.updated_at
		 FROM managed_tools m LEFT JOIN tool_categories c ON c.id=m.category_id WHERE m.id=?`, id).
		Scan(&t.ID, &t.Scope, &t.Name, &t.Display, &t.Description, &t.CategoryID,
			&t.Category, &enabled, &essential, &builtin, &deleted, &t.Kind, &t.ConfigJSON,
			&created, &updated)
	if err != nil {
		return t, err
	}
	t.Enabled, t.Essential, t.Builtin, t.Deleted = enabled != 0, essential != 0, builtin != 0, deleted != 0
	t.CreatedAt, t.UpdatedAt = msToTime(created), msToTime(updated)
	return t, nil
}

// SaveManagedTool updates metadata/config and optionally enabled state. Scope
// and name are immutable after creation to keep runtime routing stable.
func (s *Store) SaveManagedTool(ctx context.Context, t domain.ManagedTool) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	if t.ID > 0 {
		_, err := s.db.ExecContext(ctx,
			`UPDATE managed_tools SET display=?, description=?, category_id=?, enabled=?, deleted=0, config_json=?, updated_at=? WHERE id=?`,
			t.Display, t.Description, t.CategoryID, boolInt(t.Enabled), t.ConfigJSON, now, t.ID)
		return t.ID, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO managed_tools
		 (scope, name, display, description, category_id, enabled, essential, builtin, deleted, kind, config_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?)`,
		t.Scope, t.Name, t.Display, t.Description, t.CategoryID, boolInt(t.Enabled), t.Kind, t.ConfigJSON, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetManagedToolEnabled(ctx context.Context, id int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE managed_tools SET enabled=?, updated_at=? WHERE id=?`, boolInt(enabled), nowMS(), id)
	return err
}

func (s *Store) DeleteManagedTool(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE managed_tools SET deleted=1, enabled=0, updated_at=? WHERE id=?`, nowMS(), id)
	return err
}

func (s *Store) RestoreManagedTool(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE managed_tools SET deleted=0, updated_at=? WHERE id=?`, nowMS(), id)
	return err
}

func (s *Store) ListToolCategories(ctx context.Context) ([]domain.ToolCategory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, sort_order, created_at FROM tool_categories ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ToolCategory
	for rows.Next() {
		var c domain.ToolCategory
		var created int64
		if err := rows.Scan(&c.ID, &c.Name, &c.SortOrder, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = msToTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SaveToolCategory(ctx context.Context, id int64, name string, sortOrder int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id > 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE tool_categories SET name=?, sort_order=? WHERE id=?`, name, sortOrder, id)
		return id, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO tool_categories(name, sort_order, created_at) VALUES(?, ?, ?)`, name, sortOrder, nowMS())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteToolCategory(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_tools WHERE category_id=? AND deleted=0`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return sql.ErrNoRows
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM tool_categories WHERE id=?`, id)
	return err
}

// CustomToolsForScope returns enabled HTTP tools configured for one scope.
func (s *Store) CustomToolsForScope(ctx context.Context, scope string) ([]domain.ManagedTool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, scope, name, display, description, kind, config_json
		 FROM managed_tools
		 WHERE scope=? AND deleted=0 AND enabled=1 AND builtin=0 AND kind='http' ORDER BY id`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ManagedTool
	for rows.Next() {
		var t domain.ManagedTool
		if err := rows.Scan(&t.ID, &t.Scope, &t.Name, &t.Display, &t.Description, &t.Kind, &t.ConfigJSON); err != nil {
			return nil, err
		}
		t.Enabled = true
		out = append(out, t)
	}
	return out, rows.Err()
}

// ToolSettings now derives from managed_tools; the old tool_settings table is
// kept only as a migration input for existing installations.
func (s *Store) ToolSettings(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope, name, enabled, deleted FROM managed_tools`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var scope, name string
		var enabled, deleted int
		if err := rows.Scan(&scope, &name, &enabled, &deleted); err != nil {
			return nil, err
		}
		out[scope+"\x00"+name] = enabled != 0 && deleted == 0
	}
	return out, rows.Err()
}

// ToolEnabled is the runtime gate for built-in tools.
func (s *Store) ToolEnabled(ctx context.Context, scope, name string) bool {
	var enabled, deleted int
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled, deleted FROM managed_tools WHERE scope=? AND name=?`, scope, name).Scan(&enabled, &deleted)
	if err == nil {
		return enabled != 0 && deleted == 0
	}
	// Fallback for a tool inserted after the catalog seed.
	err = s.db.QueryRowContext(ctx,
		`SELECT enabled FROM tool_settings WHERE scope=? AND name=?`, scope, name).Scan(&enabled)
	if err == nil {
		return enabled != 0
	}
	return true
}
