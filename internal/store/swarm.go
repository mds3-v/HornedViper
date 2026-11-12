package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"hornedviper/internal/domain"
)

type SwarmRun struct {
	ID         int64
	TaskID     int64
	IntentID   int64
	Lanes      int
	Quorum     int
	Strategies []string
	Briefs     []string
	Mode       string
	PlannedBy  string
	LaneAssets [][]int64
	State      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type SwarmLaneResult struct {
	WorkID     int64
	IntentID   int64
	LaneID     string
	Status     string
	ResultJSON string
	CreatedAt  time.Time
}

type SwarmCandidate struct {
	IntentID     int64
	Lanes        int
	Quorum       int
	Strategies   []string
	Briefs       []string
	Mode         string
	LanePlanning string
	PlannedBy    string
	Hints        []string
	AssetIDs     []int64
	LaneAssets   [][]int64
}

func swarmBudget(intent *domain.Intent) SwarmCandidate {
	c := SwarmCandidate{IntentID: intent.ID, Lanes: 3, Quorum: 2,
		Mode: string(intent.ExecutionMode), AssetIDs: intent.AssetIDs}
	if c.Mode == "" {
		c.Mode = "swarm"
	}
	if c.Mode == "shard" {
		c.Quorum = 0
	}
	if intent.Budget != nil {
		if v, ok := intFromAny(intent.Budget["lanes"]); ok && v >= 2 {
			c.Lanes = v
		}
		if v, ok := intFromAny(intent.Budget["quorum"]); ok && v >= 1 {
			c.Quorum = v
		}
		if c.Mode == "shard" {
			if v, ok := intFromAny(intent.Budget["min_success"]); ok && v >= 1 {
				c.Quorum = v
			}
		}
		if raw, ok := intent.Budget["strategies"].([]any); ok {
			for _, v := range raw {
				switch item := v.(type) {
				case string:
					if item != "" {
						c.Strategies = append(c.Strategies, item)
						c.Briefs = append(c.Briefs, item)
						if c.Mode == "swarm" {
							c.Hints = append(c.Hints, item)
						}
					}
				case map[string]any:
					name, _ := item["name"].(string)
					brief, _ := item["brief"].(string)
					if brief == "" {
						brief, _ = item["description"].(string)
					}
					if name == "" {
						name = fmt.Sprintf("strategy-%d", len(c.Strategies)+1)
					}
					if brief == "" {
						brief = name
					}
					c.Strategies = append(c.Strategies, name)
					c.Briefs = append(c.Briefs, brief)
					if c.Mode == "swarm" {
						c.Hints = append(c.Hints, name+": "+brief)
					}
				}
			}
		}
		if v, ok := intent.Budget["lane_planning"].(string); ok {
			c.LanePlanning = strings.TrimSpace(v)
		}
		if raw, ok := intent.Budget["lane_briefs"].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && s != "" {
					c.Briefs = append(c.Briefs, s)
				}
			}
		}
	}
	if c.Lanes > 6 {
		c.Lanes = 6
	}
	if c.Quorum > c.Lanes {
		c.Quorum = c.Lanes
	}
	if c.Mode == "swarm" {
		// Planner owns the Intent; swarm_lead is the only lane planner. Planner
		// strategies survive only as advisory hints for the lead.
		if len(c.Hints) > 10 {
			c.Hints = c.Hints[:10]
		}
		c.Strategies = nil
		c.Briefs = nil
		c.LanePlanning = "lead"
	}
	return c
}

func intFromAny(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

// ListSwarmCandidates returns approved/queued parallel intents that do not yet
// have a coordinator run.
func (s *Store) ListSwarmCandidates(ctx context.Context, taskID int64) ([]SwarmCandidate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload_json FROM exploration_nodes
		 WHERE task_id=? AND kind='intent' AND state IN ('approved','queued')
		   AND json_extract(payload_json,'$.execution_mode') IN ('swarm','shard')
		   AND id NOT IN (SELECT intent_id FROM swarm_runs)`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SwarmCandidate
	for rows.Next() {
		var id int64
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, err
		}
		var p struct {
			Budget        map[string]any `json:"budget"`
			ExecutionMode string         `json:"execution_mode"`
			AssetIDs      []int64        `json:"asset_ids"`
		}
		_ = json.Unmarshal([]byte(payload), &p)
		c := swarmBudget(&domain.Intent{ID: id, Budget: p.Budget, ExecutionMode: domain.ExecutionMode(p.ExecutionMode), AssetIDs: p.AssetIDs})
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) StartSwarm(ctx context.Context, taskID int64, c SwarmCandidate) (*SwarmRun, error) {
	if c.Mode == "shard" {
		if err := s.prepareShard(ctx, taskID, &c); err != nil {
			return nil, err
		}
	}
	if c.Mode != "shard" {
		if c.Lanes < 2 || c.Lanes > 6 {
			return nil, fmt.Errorf("swarm lanes must be 2..6")
		}
		if len(c.Strategies) != c.Lanes || len(c.Briefs) != c.Lanes {
			return nil, fmt.Errorf("swarm lane plan is incomplete: lanes=%d strategies=%d briefs=%d", c.Lanes, len(c.Strategies), len(c.Briefs))
		}
		seen := map[string]bool{}
		for i := range c.Strategies {
			name := strings.TrimSpace(c.Strategies[i])
			brief := strings.TrimSpace(c.Briefs[i])
			if name == "" || brief == "" || seen[name] {
				return nil, fmt.Errorf("swarm lane %d requires unique name and non-empty brief", i+1)
			}
			seen[name] = true
			c.Strategies[i], c.Briefs[i] = name, brief
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM exploration_nodes WHERE id=? AND task_id=? AND kind='intent'`, c.IntentID, taskID).Scan(&current); err != nil {
		return nil, err
	}
	if current != "approved" && current != "queued" {
		return nil, fmt.Errorf("intent %d is %s", c.IntentID, current)
	}
	var existing int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM swarm_runs WHERE intent_id=?`, c.IntentID).Scan(&existing); err == nil {
		return nil, fmt.Errorf("swarm for intent %d already exists", c.IntentID)
	}
	mode := c.Mode
	if mode == "" {
		mode = "swarm"
	}
	strategies, _ := json.Marshal(c.Strategies)
	briefs, _ := json.Marshal(c.Briefs)
	laneAssets, _ := json.Marshal(c.LaneAssets)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO swarm_runs(task_id, intent_id, lanes, quorum, strategies_json, briefs_json, mode, planned_by, lane_assets_json, state, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?)`,
		taskID, c.IntentID, c.Lanes, c.Quorum, string(strategies), string(briefs), mode, c.PlannedBy, string(laneAssets), now, now)
	if err != nil {
		return nil, err
	}
	for i := 0; i < c.Lanes; i++ {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO works(task_id, intent_id, worker_id, lane_id, state, started_at)
			 VALUES(?, ?, '', ?, 'queued', ?)`,
			taskID, c.IntentID, fmt.Sprintf("lane-%d", i+1), now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE exploration_nodes SET state='running', updated_at=? WHERE id=?`, now, c.IntentID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getSwarmNoLock(ctx, c.IntentID)
}

func (s *Store) getSwarmNoLock(ctx context.Context, intentID int64) (*SwarmRun, error) {
	var r SwarmRun
	var strategies, briefs, laneAssets string
	var created, updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, intent_id, lanes, quorum, strategies_json, briefs_json, mode, planned_by, lane_assets_json, state, created_at, updated_at
		 FROM swarm_runs WHERE intent_id=?`, intentID).
		Scan(&r.ID, &r.TaskID, &r.IntentID, &r.Lanes, &r.Quorum, &strategies, &briefs, &r.Mode, &r.PlannedBy, &laneAssets, &r.State, &created, &updated)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(strategies), &r.Strategies)
	_ = json.Unmarshal([]byte(briefs), &r.Briefs)
	_ = json.Unmarshal([]byte(laneAssets), &r.LaneAssets)
	r.CreatedAt, r.UpdatedAt = msToTime(created), msToTime(updated)
	return &r, nil
}

func (s *Store) GetSwarmRun(ctx context.Context, intentID int64) (*SwarmRun, error) {
	return s.getSwarmNoLock(ctx, intentID)
}

// ClaimSwarmLane atomically claims one queued lane work.
func (s *Store) ClaimSwarmLane(ctx context.Context, taskID int64, workerID string, lease time.Duration) (*domain.Work, *domain.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var workID, intentID int64
	var lane string
	err = tx.QueryRowContext(ctx,
		`SELECT w.id, w.intent_id, w.lane_id FROM works w
		 JOIN exploration_nodes n ON n.id=w.intent_id
		 WHERE w.task_id=? AND w.state='queued' AND w.lane_id<>'' AND n.state='running'
		 ORDER BY w.id LIMIT 1`, taskID).Scan(&workID, &intentID, &lane)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	leaseAt := now + lease.Milliseconds()
	res, err := tx.ExecContext(ctx,
		`UPDATE works SET state='running', worker_id=?, lease_expires_at=?, started_at=? WHERE id=? AND state='queued'`,
		workerID, leaseAt, now, workID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	intent, err := s.getIntentNoLock(ctx, intentID)
	if err != nil {
		return nil, nil, err
	}
	return &domain.Work{ID: workID, TaskID: taskID, IntentID: intentID, WorkerID: workerID, LaneID: lane,
		State: domain.WorkRunning, StartedAt: ptrTime(msToTime(now)), LeaseExpiresAt: ptrTime(msToTime(leaseAt))}, intent, nil
}

func (s *Store) FinishLaneWork(ctx context.Context, workID int64, state domain.WorkState, stopReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`UPDATE works SET state=?, ended_at=?, stop_reason=? WHERE id=?`, string(state), now, stopReason, workID)
	return err
}

func (s *Store) RecordLaneResult(ctx context.Context, workID, intentID int64, laneID, status, resultJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO swarm_lane_results(work_id, intent_id, lane_id, status, result_json, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(work_id) DO UPDATE SET status=excluded.status, result_json=excluded.result_json, updated_at=excluded.updated_at`,
		workID, intentID, laneID, status, resultJSON, now, now)
	return err
}

func (s *Store) ListLaneResults(ctx context.Context, intentID int64) ([]SwarmLaneResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT work_id, intent_id, lane_id, status, result_json, created_at
		 FROM swarm_lane_results WHERE intent_id=? ORDER BY work_id`, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SwarmLaneResult
	for rows.Next() {
		var r SwarmLaneResult
		var created int64
		if err := rows.Scan(&r.WorkID, &r.IntentID, &r.LaneID, &r.Status, &r.ResultJSON, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = msToTime(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SwarmLaneTerminal(ctx context.Context, intentID int64) (total, terminal int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN state IN ('succeeded','failed','timed_out','cancelled','lost') THEN 1 ELSE 0 END),0)
		 FROM works WHERE intent_id=? AND lane_id<>''`, intentID).Scan(&total, &terminal)
	return
}

func (s *Store) FinalizeSwarm(ctx context.Context, intentID int64, swarmState string, intentState domain.IntentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE swarm_runs SET state=?, updated_at=? WHERE intent_id=?`, swarmState, now, intentID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE exploration_nodes SET state=?, updated_at=? WHERE id=?`, string(intentState), now, intentID)
	return err
}

func (s *Store) ListSwarmRuns(ctx context.Context, taskID int64) ([]SwarmRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, intent_id, lanes, quorum, strategies_json, briefs_json, mode, planned_by, lane_assets_json, state, created_at, updated_at
		 FROM swarm_runs WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SwarmRun
	for rows.Next() {
		var r SwarmRun
		var strategies, briefs, laneAssets string
		var created, updated int64
		if err := rows.Scan(&r.ID, &r.TaskID, &r.IntentID, &r.Lanes, &r.Quorum, &strategies, &briefs, &r.Mode, &r.PlannedBy, &laneAssets, &r.State, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(strategies), &r.Strategies)
		_ = json.Unmarshal([]byte(briefs), &r.Briefs)
		_ = json.Unmarshal([]byte(laneAssets), &r.LaneAssets)
		r.CreatedAt, r.UpdatedAt = msToTime(created), msToTime(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// TaskAssets resolves explicit intent assets, or assets anchored into the task
// graph. It is used by the shard coordinator to build disjoint lane sets.
func (s *Store) TaskAssets(ctx context.Context, taskID int64, explicit []int64) ([]domain.Asset, error) {
	if len(explicit) > 0 {
		ph := make([]string, 0, len(explicit))
		args := make([]any, 0, len(explicit)+1)
		for _, id := range explicit {
			ph = append(ph, "?")
			args = append(args, id)
		}
		args = append(args, taskID)
		rows, err := s.db.QueryContext(ctx,
			`SELECT a.id, a.type, a.canonical_key, a.value, a.parent_id, a.scope_status, a.source, a.first_seen, a.last_seen
			 FROM assets a
			 WHERE a.id IN (`+strings.Join(ph, ",")+`) AND a.scope_status <> 'out_of_scope'
			   AND EXISTS (
			     SELECT 1 FROM exploration_anchors an
			     JOIN exploration_nodes n ON n.id=an.node_id
			     WHERE an.asset_id=a.id AND n.task_id=?
			   )
			 ORDER BY a.id`, args...)
		if err != nil {
			return nil, err
		}
		return scanAssets(rows)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT a.id, a.type, a.canonical_key, a.value, a.parent_id, a.scope_status, a.source, a.first_seen, a.last_seen
		 FROM assets a
		 JOIN exploration_anchors an ON an.asset_id=a.id
		 JOIN exploration_nodes n ON n.id=an.node_id
		 WHERE n.task_id=? AND a.scope_status <> 'out_of_scope'
		 ORDER BY a.id LIMIT 500`, taskID)
	if err != nil {
		return nil, err
	}
	return scanAssets(rows)
}

func scanAssets(rows *sql.Rows) ([]domain.Asset, error) {
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

func (s *Store) prepareShard(ctx context.Context, taskID int64, c *SwarmCandidate) error {
	assets, err := s.TaskAssets(ctx, taskID, c.AssetIDs)
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return fmt.Errorf("shard intent %d has no assets", c.IntentID)
	}
	// Keep assets from the same parent together so one host is not split
	// across unrelated workers.
	groups := map[int64][]domain.Asset{}
	var order []int64
	for _, a := range assets {
		key := a.ParentID
		if key == 0 {
			key = a.ID
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], a)
	}
	lanes := c.Lanes
	if lanes < 1 {
		lanes = 3
	}
	if lanes > 6 {
		lanes = 6
	}
	if lanes > len(order) {
		lanes = len(order)
	}
	if lanes > len(assets) {
		lanes = len(assets)
	}
	if lanes < 1 {
		lanes = 1
	}
	assigned := make([][]domain.Asset, lanes)
	for i, key := range order {
		assigned[i%lanes] = append(assigned[i%lanes], groups[key]...)
	}
	if c.Quorum < 1 || c.Quorum > lanes {
		c.Quorum = lanes
	}
	c.Lanes = lanes
	c.LaneAssets = make([][]int64, lanes)
	c.Strategies = make([]string, lanes)
	c.Briefs = make([]string, lanes)
	for i, list := range assigned {
		ids := make([]int64, 0, len(list))
		values := make([]string, 0, len(list))
		for _, a := range list {
			ids = append(ids, a.ID)
			values = append(values, a.Value)
		}
		c.LaneAssets[i] = ids
		c.Strategies[i] = fmt.Sprintf("shard-%d", i+1)
		preview := strings.Join(values, ", ")
		if len([]rune(preview)) > 300 {
			preview = string([]rune(preview)[:300]) + "…"
		}
		c.Briefs[i] = fmt.Sprintf("Shard %d/%d · %d assets: %s. Only operate on these assets; do not scan or report assets outside this shard.", i+1, lanes, len(list), preview)
	}
	return nil
}

// CancelQueuedLanes marks still-queued lanes cancelled once quorum/min_success
// has been reached. Running lanes are allowed to finish and are ignored by the
// merger after finalization.
func (s *Store) CancelQueuedLanes(ctx context.Context, intentID int64, reason string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`UPDATE works SET state='cancelled', ended_at=?, stop_reason=?
		 WHERE intent_id=? AND lane_id<>'' AND state='queued'`, now, reason, intentID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) SetIntentState(ctx context.Context, intentID int64, state domain.IntentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE exploration_nodes SET state=?, updated_at=? WHERE id=? AND kind='intent'`,
		string(state), nowMS(), intentID)
	return err
}
