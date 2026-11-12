package store

import (
	"context"
	"strings"
)

// CancelActiveWork cancels live works and (optionally) proposed intents.
// It is used when a task is paused or stopped so the UI does not keep showing
// phantom running work. Pending approvals are expired only on stop.
func (s *Store) CancelActiveWork(ctx context.Context, taskID int64, reason string, includeProposed, expireApprovals bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE works SET state='cancelled', stop_reason=?, ended_at=?
		 WHERE task_id=? AND state IN ('leased','running','settling')`,
		reason, now, taskID); err != nil {
		return err
	}

	states := []string{"approved", "queued", "claimed", "running"}
	if includeProposed {
		states = append(states, "proposed")
	}
	ph := make([]string, len(states))
	args := []any{now, taskID}
	for i, st := range states {
		ph[i] = "?"
		args = append(args, st)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE exploration_nodes SET state='cancelled', updated_at=?
		 WHERE task_id=? AND kind='intent' AND state IN (`+strings.Join(ph, ",")+`)`,
		args...); err != nil {
		return err
	}

	if expireApprovals {
		if _, err := tx.ExecContext(ctx,
			`UPDATE approvals SET decision='expired', decided_by='system', reason=?, decided_at=?
			 WHERE task_id=? AND decision='pending'`,
			reason, now, taskID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AbandonGoal marks the task's goal abandoned unless it is already met/abandoned.
// It returns the goal id and whether a state change was applied.
func (s *Store) AbandonGoal(ctx context.Context, taskID int64) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var goalID int64
	if err := s.db.QueryRowContext(ctx, `SELECT goal_id FROM tasks WHERE id=?`, taskID).Scan(&goalID); err != nil {
		return 0, false, err
	}
	if goalID == 0 {
		return 0, false, nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE exploration_nodes SET state='abandoned', updated_at=?
		 WHERE id=? AND kind='goal' AND state IN ('open','in_progress','paused')`,
		nowMS(), goalID)
	if err != nil {
		return goalID, false, err
	}
	n, _ := res.RowsAffected()
	return goalID, n > 0, nil
}

func (s *Store) RenameTask(ctx context.Context, taskID int64, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET title=? WHERE id=?`, title, taskID)
	return err
}

// ReapExpiredWork turns expired lane leases back into queued intents. It is
// safe to call periodically; only works whose lease has expired are touched.
func (s *Store) ReapExpiredWork(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, intent_id FROM works
		 WHERE state IN ('leased','running') AND lease_expires_at IS NOT NULL AND lease_expires_at < ?
		 ORDER BY id LIMIT 50`, now)
	if err != nil {
		return 0, err
	}
	type expired struct{ workID, intentID int64 }
	var items []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.workID, &e.intentID); err != nil {
			continue
		}
		items = append(items, e)
	}
	rows.Close()
	reaped := 0
	for _, e := range items {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			continue
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE works SET state='timed_out', stop_reason='lease expired', ended_at=? WHERE id=? AND state IN ('leased','running')`,
			now, e.workID)
		if err == nil {
			var active int
			_ = tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM works WHERE intent_id=? AND state IN ('leased','running')`, e.intentID).Scan(&active)
			if active == 0 {
				_, _ = tx.ExecContext(ctx,
					`UPDATE exploration_nodes SET state='queued', updated_at=? WHERE id=? AND kind='intent' AND state IN ('claimed','running')`,
					now, e.intentID)
			}
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err == nil {
			reaped++
		}
	}
	return reaped, nil
}
