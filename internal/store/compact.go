package store

import (
	"context"
	"strings"
)

// CompactStreamingActivities collapses token-by-token thinking/text activities
// produced by older runtime versions into one row per contiguous stream.
func (s *Store) CompactStreamingActivities(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, work_id, role, kind, detail, created_at
		 FROM activities ORDER BY task_id, work_id, id`)
	if err != nil {
		return err
	}
	type row struct {
		id, taskID, workID int64
		role, kind, detail string
		created            int64
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.taskID, &r.workID, &r.role, &r.kind, &r.detail, &r.created); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var merge *row
	flush := func() error {
		if merge == nil {
			return nil
		}
		summary := truncateSummary(merge.detail, 180)
		_, err := s.db.ExecContext(ctx, `UPDATE activities SET summary=?, detail=? WHERE id=?`,
			summary, merge.detail, merge.id)
		merge = nil
		return err
	}
	for i := range all {
		r := &all[i]
		if (r.kind != "thinking" && r.kind != "assistant_text") || r.detail == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if merge != nil && merge.kind == r.kind && merge.taskID == r.taskID &&
			merge.workID == r.workID && merge.role == r.role && r.created-merge.created <= 3000 {
			merge.detail += r.detail
			if _, err := s.db.ExecContext(ctx, `DELETE FROM activities WHERE id=?`, r.id); err != nil {
				return err
			}
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		merge = &all[i]
	}
	return flush()
}

func truncateSummary(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
