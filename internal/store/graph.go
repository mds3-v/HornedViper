package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"hornedviper/internal/domain"
)

type factPayload struct {
	Summary    string  `json:"summary"`
	Detail     string  `json:"detail"`
	Evidence   string  `json:"evidence"`
	Confidence string  `json:"confidence"`
	AssetIDs   []int64 `json:"asset_ids,omitempty"`
	IntentID   int64   `json:"intent_id"`
}

type findingPayload struct {
	Title       string  `json:"title"`
	Severity    string  `json:"severity"`
	Evidence    string  `json:"evidence"`
	AssetIDs    []int64 `json:"asset_ids,omitempty"`
	IntentID    int64   `json:"intent_id"`
	FindingType string  `json:"finding_type,omitempty"`
	SubjectKey  string  `json:"subject_key,omitempty"`
	CaseKey     string  `json:"case_key,omitempty"`
	Observation string  `json:"observation,omitempty"`
}

// RecordFact writes a fact node, its provenance edges, and anchors.
func (s *Store) RecordFact(ctx context.Context, taskID, intentID, workID int64, spec domain.FactSpec) (*domain.Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	payload := mustJSON(factPayload{
		Summary: spec.Summary, Detail: spec.Detail, Evidence: spec.Evidence,
		Confidence: string(spec.Confidence), AssetIDs: spec.AssetIDs, IntentID: intentID,
	})
	res, err := tx.ExecContext(ctx,
		`INSERT INTO exploration_nodes
		 (task_id, kind, state, payload_json, priority, origin, created_by_role, created_by_work_id, created_at, updated_at)
		 VALUES (?, 'fact', 'confirmed', ?, 0, 'worker', 'worker', ?, ?, ?)`,
		taskID, payload, workID, now, now)
	if err != nil {
		return nil, err
	}
	factID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if intentID > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exploration_edges (task_id, src_id, dst_id, rel, created_at)
			 VALUES (?, ?, ?, 'yields', ?)`, taskID, intentID, factID, now); err != nil {
			return nil, err
		}
	}
	for _, assetID := range spec.AssetIDs {
		if assetID == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exploration_anchors (node_id, asset_id) VALUES (?, ?)`, factID, assetID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.Fact{
		ID: factID, TaskID: taskID, IntentID: intentID, Summary: spec.Summary,
		Detail: spec.Detail, Evidence: spec.Evidence, Confidence: spec.Confidence,
		AssetIDs: spec.AssetIDs, CreatedAt: msToTime(now),
	}, nil
}

// RecordFinding writes a finding node and anchors.
func (s *Store) RecordFinding(ctx context.Context, taskID, intentID, workID int64, spec domain.FindingSpec) (*domain.Finding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	payload := mustJSON(findingPayload{
		Title: spec.Title, Severity: spec.Severity, Evidence: spec.Evidence,
		AssetIDs: spec.AssetIDs, IntentID: intentID, SubjectKey: spec.SubjectKey,
		CaseKey: spec.CaseKey, Observation: spec.Observation,
	})
	res, err := tx.ExecContext(ctx,
		`INSERT INTO exploration_nodes
		 (task_id, kind, state, payload_json, priority, origin, created_by_role, created_by_work_id, created_at, updated_at)
		 VALUES (?, 'finding', 'confirmed', ?, 0, 'worker', 'worker', ?, ?, ?)`,
		taskID, payload, workID, now, now)
	if err != nil {
		return nil, err
	}
	findingID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if intentID > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exploration_edges (task_id, src_id, dst_id, rel, created_at)
			 VALUES (?, ?, ?, 'yields', ?)`, taskID, intentID, findingID, now); err != nil {
			return nil, err
		}
	}
	for _, assetID := range spec.AssetIDs {
		if assetID == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exploration_anchors (node_id, asset_id) VALUES (?, ?)`, findingID, assetID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.Finding{
		ID: findingID, TaskID: taskID, IntentID: intentID, Title: spec.Title,
		Severity: spec.Severity, Evidence: spec.Evidence, AssetIDs: spec.AssetIDs,
		CreatedAt: msToTime(now),
	}, nil
}

// RecordAsset upserts a raw asset and returns its canonical row.
func (s *Store) RecordAsset(ctx context.Context, spec domain.AssetSpec) (*domain.Asset, error) {
	return s.RecordAssetForTask(ctx, 0, spec)
}

// RecordAssetForTask records a globally deduplicated asset and associates it
// with a task. The same asset may belong to multiple tasks.
func (s *Store) RecordAssetForTask(ctx context.Context, taskID int64, spec domain.AssetSpec) (*domain.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	typ := strings.ToLower(strings.TrimSpace(spec.Type))
	key := canonicalAssetKey(typ, spec.Value)
	payload := jsonText(spec.Metadata)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO assets (type, canonical_key, value, payload_json, scope_status, source, first_seen, last_seen)
		 VALUES (?, ?, ?, ?, 'unknown', 'agent', ?, ?)
		 ON CONFLICT(type, canonical_key) DO UPDATE SET
		   value=excluded.value, payload_json=excluded.payload_json, last_seen=excluded.last_seen`,
		typ, key, strings.TrimSpace(spec.Value), payload, now, now); err != nil {
		return nil, err
	}
	var a domain.Asset
	var parentID sql.NullInt64
	var first, last int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id, type, canonical_key, value, parent_id, scope_status, source, first_seen, last_seen
		 FROM assets WHERE type=? AND canonical_key=?`, typ, key).Scan(
		&a.ID, &a.Type, &a.CanonicalKey, &a.Value, &parentID, &a.ScopeStatus, &a.Source, &first, &last); err != nil {
		return nil, err
	}
	if parentID.Valid {
		a.ParentID = parentID.Int64
	}
	a.FirstSeen, a.LastSeen = msToTime(first), msToTime(last)
	if taskID > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO asset_tasks(asset_id, task_id, first_seen, last_seen)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(asset_id, task_id) DO UPDATE SET last_seen=excluded.last_seen`,
			a.ID, taskID, first, last); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &a, nil
}

func canonicalAssetKey(typ, value string) string {
	v := strings.TrimSpace(value)
	switch typ {
	case "root_domain", "subdomain", "service", "app", "endpoint":
		v = strings.ToLower(strings.TrimSuffix(v, "/"))
	default:
		v = strings.ToLower(v)
	}
	return v
}

// AttachAnchor links a graph node to an asset.
func (s *Store) AttachAnchor(ctx context.Context, nodeID, assetID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO exploration_anchors (node_id, asset_id) VALUES (?, ?)`, nodeID, assetID)
	return err
}

// RecordEvidence stores an artifact reference.
func (s *Store) RecordEvidence(ctx context.Context, taskID, workID int64, spec domain.EvidenceSpec) (*domain.Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO artifacts (task_id, work_id, type, path, hash, bytes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		taskID, workID, spec.Type, spec.Path, spec.Hash, spec.Bytes, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.Evidence{
		ID: id, TaskID: taskID, WorkID: workID, Type: spec.Type,
		Path: spec.Path, Hash: spec.Hash, Bytes: spec.Bytes, CreatedAt: msToTime(now),
	}, nil
}

// RecordActivity appends one execution step.
func (s *Store) RecordActivity(ctx context.Context, a domain.Activity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO activities (task_id, work_id, intent_id, role, kind, tool, tool_use_id, summary, detail, llm_call_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.TaskID, a.WorkID, a.IntentID, a.Role, a.Kind, a.Tool, a.ToolUseID,
		a.Summary, a.Detail, a.LLMCallID, a.CreatedAt.UnixMilli())
	return err
}

// ListFacts lists facts for a task, newest first.
func (s *Store) ListFacts(ctx context.Context, taskID int64, limit int) ([]domain.Fact, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload_json, created_at FROM exploration_nodes
		 WHERE task_id=? AND kind='fact' ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Fact
	for rows.Next() {
		var f domain.Fact
		var payload string
		var created int64
		if err := rows.Scan(&f.ID, &payload, &created); err != nil {
			return nil, err
		}
		f.TaskID, f.CreatedAt = taskID, msToTime(created)
		var p factPayload
		_ = json.Unmarshal([]byte(payload), &p)
		f.IntentID, f.Summary, f.Detail, f.Evidence = p.IntentID, p.Summary, p.Detail, p.Evidence
		f.Confidence, f.AssetIDs = domain.Confidence(p.Confidence), p.AssetIDs
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListFindings lists findings for a task, newest first.
func (s *Store) ListFindings(ctx context.Context, taskID int64, limit int) ([]domain.Finding, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload_json, created_at FROM exploration_nodes
		 WHERE task_id=? AND kind='finding' ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Finding
	for rows.Next() {
		var f domain.Finding
		var payload string
		var created int64
		if err := rows.Scan(&f.ID, &payload, &created); err != nil {
			return nil, err
		}
		f.TaskID, f.CreatedAt = taskID, msToTime(created)
		var p findingPayload
		_ = json.Unmarshal([]byte(payload), &p)
		f.IntentID, f.Title, f.Severity, f.Evidence = p.IntentID, p.Title, p.Severity, p.Evidence
		f.AssetIDs, f.SubjectKey, f.CaseKey, f.Observation = p.AssetIDs, p.SubjectKey, p.CaseKey, p.Observation
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListTaskAssets lists assets associated with one task, newest first.
func (s *Store) ListTaskAssets(ctx context.Context, taskID int64, limit int) ([]domain.Asset, error) {
	if taskID <= 0 {
		return s.ListAssets(ctx, limit)
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.type, a.canonical_key, a.value, a.parent_id, a.scope_status, a.source, a.first_seen, a.last_seen
		 FROM assets a JOIN asset_tasks at ON at.asset_id=a.id
		 WHERE at.task_id=? ORDER BY a.id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Asset
	for rows.Next() {
		var a domain.Asset
		var parent sql.NullInt64
		var first, last int64
		if err := rows.Scan(&a.ID, &a.Type, &a.CanonicalKey, &a.Value, &parent, &a.ScopeStatus, &a.Source, &first, &last); err != nil {
			return nil, err
		}
		if parent.Valid {
			a.ParentID = parent.Int64
		}
		a.FirstSeen, a.LastSeen = msToTime(first), msToTime(last)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAssets lists assets, newest first.
func (s *Store) ListAssets(ctx context.Context, limit int) ([]domain.Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, canonical_key, value, parent_id, scope_status, source, first_seen, last_seen
		 FROM assets ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Asset
	for rows.Next() {
		var a domain.Asset
		var parent sql.NullInt64
		var first, last int64
		if err := rows.Scan(&a.ID, &a.Type, &a.CanonicalKey, &a.Value, &parent, &a.ScopeStatus, &a.Source, &first, &last); err != nil {
			return nil, err
		}
		if parent.Valid {
			a.ParentID = parent.Int64
		}
		a.FirstSeen, a.LastSeen = msToTime(first), msToTime(last)
		out = append(out, a)
	}
	return out, rows.Err()
}

// FindingGroups aggregates findings by task for the findings index.
func (s *Store) FindingGroups(ctx context.Context) ([]domain.FindingGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, COUNT(*), MAX(created_at) FROM exploration_nodes
		 WHERE kind='finding' GROUP BY task_id ORDER BY MAX(created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FindingGroup
	for rows.Next() {
		var g domain.FindingGroup
		var last int64
		if err := rows.Scan(&g.TaskID, &g.Count, &last); err != nil {
			return nil, err
		}
		g.LastAt = msToTime(last)
		out = append(out, g)
	}
	return out, rows.Err()
}

func findingFilter(taskID int64, search string) (string, []any) {
	where := ` WHERE kind='finding'`
	args := []any{}
	if taskID > 0 {
		where += ` AND task_id=?`
		args = append(args, taskID)
	}
	if q := strings.TrimSpace(search); q != "" {
		where += ` AND payload_json LIKE ?`
		args = append(args, "%"+q+"%")
	}
	return where, args
}

func (s *Store) CountFindingsSearch(ctx context.Context, taskID int64, search string) (int, error) {
	where, args := findingFilter(taskID, search)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exploration_nodes`+where, args...).Scan(&n)
	return n, err
}

func (s *Store) ListFindingsPageSearch(ctx context.Context, taskID int64, search string, limit, offset int) ([]domain.Finding, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	where, args := findingFilter(taskID, search)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload_json, created_at FROM exploration_nodes`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Finding
	for rows.Next() {
		var f domain.Finding
		var payload string
		var created int64
		if err := rows.Scan(&f.ID, &payload, &created); err != nil {
			return nil, err
		}
		f.TaskID, f.CreatedAt = taskID, msToTime(created)
		var p findingPayload
		_ = json.Unmarshal([]byte(payload), &p)
		f.IntentID, f.Title, f.Severity, f.Evidence = p.IntentID, p.Title, p.Severity, p.Evidence
		f.AssetIDs, f.SubjectKey, f.CaseKey, f.Observation = p.AssetIDs, p.SubjectKey, p.CaseKey, p.Observation
		out = append(out, f)
	}
	return out, rows.Err()
}
