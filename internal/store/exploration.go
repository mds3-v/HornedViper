package store

import "context"

// GoalEvidenceValid verifies that every evidence id belongs to the task and is
// a fact/finding, and that the goal belongs to the task.
func (s *Store) GoalEvidenceValid(ctx context.Context, taskID, goalID int64, ids []int64) (bool, error) {
	var goalTask int64
	if err := s.db.QueryRowContext(ctx, `SELECT task_id FROM exploration_nodes WHERE id=? AND kind='goal'`, goalID).Scan(&goalTask); err != nil {
		return false, err
	}
	if goalTask != taskID || len(ids) == 0 {
		return false, nil
	}
	seen := map[int64]bool{}
	valid := 0
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM exploration_nodes WHERE id=? AND task_id=? AND kind IN ('fact','finding')`,
			id, taskID).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			valid++
		}
	}
	return valid > 0, nil
}
