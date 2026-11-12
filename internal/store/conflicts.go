package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"hornedviper/internal/domain"
)

func conflictHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateConflict(ctx context.Context, c domain.Conflict) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	if c.SubjectHash == "" {
		c.SubjectHash = conflictHash(c.Type + "\x00" + c.Subject)
	}
	lanes, _ := json.Marshal(c.LaneIDs)
	evidence, _ := json.Marshal(c.Evidence)
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM swarm_conflicts WHERE task_id=? AND intent_id=? AND subject_hash=? AND status IN ('open','quarantined') LIMIT 1`,
		c.TaskID, c.IntentID, c.SubjectHash).Scan(&id)
	if err == nil {
		return id, nil
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO swarm_conflicts(task_id, intent_id, type, subject, subject_hash, lane_ids_json, evidence_json,
		 status, decision, reason, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 'open', '', ?, ?, ?)`,
		c.TaskID, c.IntentID, c.Type, c.Subject, c.SubjectHash, string(lanes), string(evidence), "", now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ResolveConflict(ctx context.Context, id int64, decision, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := "quarantined"
	if decision == "keep_all" {
		status = "resolved"
	}
	if decision == "needs_human" {
		status = "needs_human"
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE swarm_conflicts SET status=?, decision=?, reason=?, updated_at=? WHERE id=?`,
		status, decision, reason, nowMS(), id)
	return err
}

func (s *Store) ListConflicts(ctx context.Context, taskID int64, limit int) ([]domain.Conflict, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, intent_id, type, subject, subject_hash, lane_ids_json, evidence_json, status, decision, reason
		 FROM swarm_conflicts WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Conflict
	for rows.Next() {
		var c domain.Conflict
		var lanes, evidence string
		if err := rows.Scan(&c.ID, &c.TaskID, &c.IntentID, &c.Type, &c.Subject, &c.SubjectHash, &lanes, &evidence,
			&c.Status, &c.Decision, &c.Reason); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(lanes), &c.LaneIDs)
		_ = json.Unmarshal([]byte(evidence), &c.Evidence)
		out = append(out, c)
	}
	return out, rows.Err()
}
