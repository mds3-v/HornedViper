package store

import "context"

// DeleteTask permanently removes a task and its task-scoped graph/execution
// rows. Global assets and task traffic are retained; the API archives LLM
// records before this call. Task artifacts are removed by the API layer.
func (s *Store) DeleteTask(ctx context.Context, taskID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var title string
	_ = tx.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id=?`, taskID).Scan(&title)
	if title != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO deleted_entities(kind, entity_id, title, deleted_at) VALUES('task', ?, ?, ?)
			 ON CONFLICT(kind, entity_id) DO UPDATE SET title=excluded.title, deleted_at=excluded.deleted_at`,
			taskID, title, nowMS()); err != nil {
			return err
		}
	}
	stmts := []string{
		`DELETE FROM exploration_edges WHERE task_id=?`,
		`DELETE FROM exploration_anchors WHERE node_id IN (SELECT id FROM exploration_nodes WHERE task_id=?)`,
		`DELETE FROM swarm_lane_results WHERE intent_id IN (SELECT id FROM exploration_nodes WHERE task_id=? AND kind='intent')`,
		`DELETE FROM swarm_runs WHERE task_id=?`,
		`DELETE FROM swarm_conflicts WHERE task_id=?`,
		`DELETE FROM quarantine_items WHERE task_id=?`,
		`DELETE FROM challenge_completions WHERE task_id=?`,
		`DELETE FROM asset_tasks WHERE task_id=?`,
		`DELETE FROM works WHERE task_id=?`,
		`DELETE FROM approvals WHERE task_id=?`,
		`DELETE FROM workflow_events WHERE task_id=?`,
		`DELETE FROM activities WHERE task_id=?`,
		`DELETE FROM llm_calls WHERE task_id=?`,
		`DELETE FROM artifacts WHERE task_id=?`,
		`DELETE FROM task_constraints WHERE task_id=?`,
		`DELETE FROM planner_todos WHERE task_id=?`,
		`DELETE FROM task_scope WHERE task_id=?`,
		`DELETE FROM flag_submissions WHERE task_id=?`,
		`DELETE FROM chat_messages WHERE task_id=?`,
		`DELETE FROM chat_summaries WHERE task_id=?`,
		`DELETE FROM chat_todos WHERE owner_type='task' AND owner_id=?`,
		`DELETE FROM task_flags WHERE task_id=?`,
		`DELETE FROM external_tasks WHERE task_id=?`,
		`DELETE FROM exploration_nodes WHERE task_id=?`,
		`DELETE FROM tasks WHERE id=?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, taskID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
