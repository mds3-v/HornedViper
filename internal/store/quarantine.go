package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"hornedviper/internal/domain"
)

func quarantineHash(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateQuarantine(ctx context.Context, item domain.QuarantineItem) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	h := item.PayloadHash
	if h == "" {
		// Keep identical payloads from different lanes distinct: provenance is
		// part of the quarantine identity.
		h = quarantineHash(fmt.Sprintf("%s\x00%s\x00%d", item.PayloadJSON, item.LaneID, item.WorkID))
	}
	var existing int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM quarantine_items WHERE task_id=? AND payload_hash=? LIMIT 1`, item.TaskID, h).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	flags, _ := json.Marshal(item.CandidateFlags)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO quarantine_items
		 (task_id, intent_id, work_id, lane_id, source_kind, reason, summary, payload_json,
		  artifact_path, payload_hash, candidate_flags_json, status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
		item.TaskID, item.IntentID, item.WorkID, item.LaneID, item.SourceKind, item.Reason,
		item.Summary, item.PayloadJSON, item.ArtifactPath, h, string(flags), now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListQuarantine(ctx context.Context, taskID int64, limit int) ([]domain.QuarantineItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, intent_id, work_id, lane_id, source_kind, reason, summary,
		        payload_json, artifact_path, payload_hash, candidate_flags_json, status,
		        created_at, reviewed_at, reviewed_by
		 FROM quarantine_items WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.QuarantineItem
	for rows.Next() {
		item, err := scanQuarantine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) GetQuarantine(ctx context.Context, id int64) (*domain.QuarantineItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, intent_id, work_id, lane_id, source_kind, reason, summary,
		        payload_json, artifact_path, payload_hash, candidate_flags_json, status,
		        created_at, reviewed_at, reviewed_by
		 FROM quarantine_items WHERE id=?`, id)
	return scanQuarantine(row)
}

func scanQuarantine(row rowScanner) (*domain.QuarantineItem, error) {
	var item domain.QuarantineItem
	var flags string
	var created, reviewed int64
	if err := row.Scan(&item.ID, &item.TaskID, &item.IntentID, &item.WorkID, &item.LaneID,
		&item.SourceKind, &item.Reason, &item.Summary, &item.PayloadJSON, &item.ArtifactPath,
		&item.PayloadHash, &flags, &item.Status, &created, &reviewed, &item.ReviewedBy); err != nil {
		return nil, err
	}
	item.CreatedAt = msToTime(created)
	if reviewed > 0 {
		t := msToTime(reviewed)
		item.ReviewedAt = &t
	}
	_ = json.Unmarshal([]byte(flags), &item.CandidateFlags)
	return &item, nil
}

func (s *Store) SetQuarantineStatus(ctx context.Context, id int64, status, reviewedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`UPDATE quarantine_items SET status=?, updated_at=?, reviewed_at=?, reviewed_by=? WHERE id=?`,
		status, now, now, reviewedBy, id)
	return err
}

func (s *Store) QuarantineDigest(ctx context.Context, taskID int64, limit int) ([]domain.QuarantineDigest, error) {
	items, err := s.ListQuarantine(ctx, taskID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.QuarantineDigest, 0, len(items))
	for _, item := range items {
		if item.Status != "open" {
			continue
		}
		out = append(out, domain.QuarantineDigest{
			ID: item.ID, LaneID: item.LaneID, SourceKind: item.SourceKind,
			Reason: item.Reason, Summary: item.Summary, CandidateFlags: item.CandidateFlags,
			Status: item.Status,
		})
	}
	return out, nil
}

// PromoteQuarantine turns a quarantine item into a durable evidence artifact.
// It never creates a finding or submits a flag; promotion is an evidence-level
// action only.
func (s *Store) PromoteQuarantine(ctx context.Context, id int64, reviewedBy string) (*domain.Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var item domain.QuarantineItem
	var status string
	if err := tx.QueryRowContext(ctx,
		`SELECT id, task_id, work_id, lane_id, source_kind, reason, summary, payload_json, payload_hash, status
		 FROM quarantine_items WHERE id=?`, id).
		Scan(&item.ID, &item.TaskID, &item.WorkID, &item.LaneID, &item.SourceKind, &item.Reason,
			&item.Summary, &item.PayloadJSON, &item.PayloadHash, &status); err != nil {
		return nil, err
	}
	if status == "promoted" {
		return nil, fmt.Errorf("quarantine %d already promoted", id)
	}
	path := fmt.Sprintf("quarantine/%d", id)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO artifacts(task_id, work_id, type, path, hash, bytes, created_at)
		 VALUES(?, ?, 'quarantine', ?, ?, ?, ?)`,
		item.TaskID, item.WorkID, path, item.PayloadHash, len(item.PayloadJSON), now)
	if err != nil {
		return nil, err
	}
	artifactID, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx,
		`UPDATE quarantine_items SET status='promoted', reviewed_at=?, reviewed_by=?, updated_at=? WHERE id=?`,
		now, reviewedBy, now, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.Evidence{ID: artifactID, TaskID: item.TaskID, WorkID: item.WorkID,
		Type: "quarantine", Path: path, Hash: item.PayloadHash, Bytes: int64(len(item.PayloadJSON)),
		CreatedAt: msToTime(now)}, nil
}

func (s *Store) ListWorkArtifacts(ctx context.Context, taskID, workID int64, limit int) ([]domain.Evidence, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, work_id, type, path, hash, bytes, created_at
		 FROM artifacts WHERE task_id=? AND work_id=? ORDER BY id DESC LIMIT ?`, taskID, workID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Evidence
	for rows.Next() {
		var e domain.Evidence
		var hash sql.NullString
		var created int64
		if err := rows.Scan(&e.ID, &e.TaskID, &e.WorkID, &e.Type, &e.Path, &hash, &e.Bytes, &created); err != nil {
			return nil, err
		}
		e.Hash = hash.String
		e.CreatedAt = msToTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}
