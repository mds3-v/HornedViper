package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"hornedviper/internal/domain"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

// Store is the SQLite persistence layer.
//
// Phase 0 uses one writer connection (SetMaxOpenConns(1)) plus a mutex for
// write transactions. This is the simplest correct SQLite setup for a
// single-process engine; readers and writers are serialized by SQLite.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open opens (and migrates) a SQLite database.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("chat_messages", "attachments_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("traffic", "conversation_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrateTraffic(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_traffic_conversation ON traffic(conversation_id, id)"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("conversations", "reasoning_level", "TEXT NOT NULL DEFAULT 'High'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("conversations", "permission_mode", "TEXT NOT NULL DEFAULT 'workspace-write'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("chat_messages", "thinking_ms", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("conversation_messages", "thinking_ms", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("swarm_runs", "briefs_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("swarm_runs", "mode", "TEXT NOT NULL DEFAULT 'swarm'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("swarm_runs", "planned_by", "TEXT NOT NULL DEFAULT ''"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureColumn("swarm_runs", "lane_assets_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrateAssetTasks(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrateLLMCalls(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.SeedDefaults(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	for _, stmt := range strings.Split(string(raw), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// migrateTraffic removes the legacy task foreign key so conversation-scoped
// traffic can use task_id=0. Existing rows are preserved.
func (s *Store) migrateTraffic() error {
	rows, err := s.db.Query("PRAGMA foreign_key_list(traffic)")
	if err != nil {
		return err
	}
	hasFK := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		if from == "task_id" {
			hasFK = true
		}
	}
	rows.Close()
	if !hasFK {
		return nil
	}
	if _, err := s.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	_, err = s.db.Exec(`
CREATE TABLE traffic_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL DEFAULT 0,
    conversation_id INTEGER NOT NULL DEFAULT 0,
    work_id         INTEGER NOT NULL DEFAULT 0,
    intent_id       INTEGER NOT NULL DEFAULT 0,
    method          TEXT    NOT NULL DEFAULT '',
    host            TEXT    NOT NULL DEFAULT '',
    url             TEXT    NOT NULL DEFAULT '',
    status_code     INTEGER NOT NULL DEFAULT 0,
    content_type    TEXT    NOT NULL DEFAULT '',
    request_raw     TEXT    NOT NULL DEFAULT '',
    response_raw    TEXT    NOT NULL DEFAULT '',
    request_size    INTEGER NOT NULL DEFAULT 0,
    response_size   INTEGER NOT NULL DEFAULT 0,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    error           TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
)`)
	if err == nil {
		_, err = s.db.Exec(`INSERT INTO traffic_new
			(id, task_id, conversation_id, work_id, intent_id, method, host, url, status_code,
			 content_type, request_raw, response_raw, request_size, response_size, duration_ms, error, created_at)
			SELECT id, task_id, conversation_id, work_id, intent_id, method, host, url, status_code,
			 content_type, request_raw, response_raw, request_size, response_size, duration_ms, error, created_at
			FROM traffic`)
	}
	if err == nil {
		_, err = s.db.Exec("DROP TABLE traffic")
	}
	if err == nil {
		_, err = s.db.Exec("ALTER TABLE traffic_new RENAME TO traffic")
	}
	if err == nil {
		_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_traffic_task ON traffic(task_id, id)")
		_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_traffic_conversation ON traffic(conversation_id, id)")
	}
	_, _ = s.db.Exec("PRAGMA foreign_keys=ON")
	return err
}

// migrateAssetTasks backfills task ownership for assets recorded before the
// asset_tasks table existed. Assets are globally deduplicated but listing is
// task-scoped.
func (s *Store) migrateAssetTasks() error {
	queries := []string{
		`INSERT OR IGNORE INTO asset_tasks(asset_id, task_id, first_seen, last_seen)
		 SELECT ea.asset_id, n.task_id, MIN(n.created_at), MAX(n.updated_at)
		 FROM exploration_anchors ea JOIN exploration_nodes n ON n.id=ea.node_id
		 WHERE n.task_id > 0 GROUP BY ea.asset_id, n.task_id`,
		`INSERT OR IGNORE INTO asset_tasks(asset_id, task_id, first_seen, last_seen)
		 SELECT subject_id, task_id, created_at, created_at
		 FROM workflow_events
		 WHERE subject_type='asset' AND subject_id > 0 AND task_id > 0`,
	}
	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateLLMCalls() error {
	rows, err := s.db.Query("PRAGMA table_info(llm_calls)")
	if err != nil {
		return err
	}
	hasConversation := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "conversation_id" {
			hasConversation = true
		}
	}
	rows.Close()
	if hasConversation {
		_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_llm_calls_conversation ON llm_calls(conversation_id, id)")
		return nil
	}
	// Legacy llm_calls had a foreign key to tasks, so task_id=0 could not
	// represent a conversation call. Rebuild without that constraint and add
	// conversation_id, preserving all existing task calls.
	if _, err := s.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	_, err = s.db.Exec(`
CREATE TABLE llm_calls_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL DEFAULT 0,
    conversation_id INTEGER NOT NULL DEFAULT 0,
    work_id         INTEGER NOT NULL DEFAULT 0,
    role            TEXT    NOT NULL,
    session_id      TEXT    NOT NULL DEFAULT '',
    turn            INTEGER NOT NULL DEFAULT 0,
    provider        TEXT    NOT NULL DEFAULT '',
    model           TEXT    NOT NULL DEFAULT '',
    request_json    TEXT    NOT NULL DEFAULT '',
    response_json   TEXT    NOT NULL DEFAULT '',
    thinking        TEXT    NOT NULL DEFAULT '',
    tool_calls_json TEXT    NOT NULL DEFAULT '',
    usage_json      TEXT    NOT NULL DEFAULT '',
    latency_ms      INTEGER NOT NULL DEFAULT 0,
    error           TEXT    NOT NULL DEFAULT '',
    request_hash    TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
);
INSERT INTO llm_calls_new (id, task_id, conversation_id, work_id, role, session_id, turn, provider, model,
    request_json, response_json, thinking, tool_calls_json, usage_json, latency_ms, error, request_hash, created_at)
SELECT id, task_id, 0, work_id, role, session_id, turn, provider, model,
    request_json, response_json, thinking, tool_calls_json, usage_json, latency_ms, error, request_hash, created_at
FROM llm_calls;
DROP TABLE llm_calls;
ALTER TABLE llm_calls_new RENAME TO llm_calls;
CREATE INDEX idx_llm_calls_task ON llm_calls(task_id, id);
CREATE INDEX idx_llm_calls_conversation ON llm_calls(conversation_id, id);
`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec("PRAGMA foreign_keys=ON")
	return nil
}

func (s *Store) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	_, err = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func nowMS() int64 { return time.Now().UnixMilli() }

func msToTime(ms int64) time.Time { return time.UnixMilli(ms) }

func timePtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := time.UnixMilli(ms.Int64)
	return &t
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// CreateTask creates a new task in the created state.
func (s *Store) CreateTask(ctx context.Context, title string, mode domain.TaskMode, budget map[string]any) (*domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tasks (title, mode, status, budget_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		title, string(mode), string(domain.TaskCreated), jsonText(budget), now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &domain.Task{
		ID: id, Title: title, Mode: mode, Status: domain.TaskCreated,
		CreatedAt: msToTime(now), Budget: budget,
	}, nil
}

// GetTask loads one task.
func (s *Store) GetTask(ctx context.Context, id int64) (*domain.Task, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, title, mode, status, goal_id, budget_json, created_at, started_at, ended_at
		 FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (*domain.Task, error) {
	var t domain.Task
	var mode, status, budget string
	var created int64
	var started, ended sql.NullInt64
	if err := row.Scan(&t.ID, &t.Title, &mode, &status, &t.GoalID, &budget, &created, &started, &ended); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	t.Mode = domain.TaskMode(mode)
	t.Status = domain.TaskStatus(status)
	t.CreatedAt = msToTime(created)
	t.StartedAt = timePtr(started)
	t.EndedAt = timePtr(ended)
	_ = json.Unmarshal([]byte(budget), &t.Budget)
	return &t, nil
}

// SetTaskStatus updates a task status with a state-machine guard.
func (s *Store) SetTaskStatus(ctx context.Context, id int64, to domain.TaskStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var current string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, id).Scan(&current); err != nil {
		return err
	}
	from := domain.TaskStatus(current)
	if !from.CanTransition(to) {
		return fmt.Errorf("task %d: illegal transition %s -> %s", id, from, to)
	}
	now := nowMS()
	var started, ended any
	switch to {
	case domain.TaskRunning:
		started = now
	case domain.TaskCompleted, domain.TaskFailed, domain.TaskCancelled:
		ended = now
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status=?,
		    started_at = COALESCE(started_at, ?),
		    ended_at = COALESCE(?, ended_at)
		 WHERE id=?`, string(to), started, ended, id)
	return err
}

// SetGoal creates the task's top-level goal node. One goal per task in Phase 0.
func (s *Store) SetGoal(ctx context.Context, taskID int64, text string, acceptance []string) (*domain.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	payload := mustJSON(map[string]any{"text": text, "acceptance": acceptance})
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO exploration_nodes
		 (task_id, kind, state, payload_json, priority, origin, created_by_role, created_at, updated_at)
		 VALUES (?, 'goal', 'open', ?, 0, 'mainagent', 'mainagent', ?, ?)`,
		taskID, payload, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET goal_id=? WHERE id=?`, id, taskID); err != nil {
		return nil, err
	}
	return &domain.Goal{
		ID: id, TaskID: taskID, Text: text, Acceptance: acceptance,
		State: domain.GoalOpen, CreatedAt: msToTime(now),
	}, nil
}

// GetGoal loads a goal by node id.
func (s *Store) GetGoal(ctx context.Context, id int64) (*domain.Goal, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, state, payload_json, created_at FROM exploration_nodes
		 WHERE id=? AND kind='goal'`, id)
	return scanGoal(row)
}

func scanGoal(row rowScanner) (*domain.Goal, error) {
	var g domain.Goal
	var state, payload string
	var created int64
	if err := row.Scan(&g.ID, &g.TaskID, &state, &payload, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	g.State = domain.GoalState(state)
	g.CreatedAt = msToTime(created)
	var p struct {
		Text       string   `json:"text"`
		Acceptance []string `json:"acceptance"`
	}
	_ = json.Unmarshal([]byte(payload), &p)
	g.Text, g.Acceptance = p.Text, p.Acceptance
	return &g, nil
}

// CurrentGoal loads the task's current goal.
func (s *Store) CurrentGoal(ctx context.Context, taskID int64) (*domain.Goal, error) {
	var goalID int64
	if err := s.db.QueryRowContext(ctx, `SELECT goal_id FROM tasks WHERE id=?`, taskID).Scan(&goalID); err != nil {
		return nil, err
	}
	if goalID == 0 {
		return nil, nil
	}
	return s.GetGoal(ctx, goalID)
}

// SetGoalState updates a goal state with a guard.
func (s *Store) SetGoalState(ctx context.Context, goalID int64, to domain.GoalState, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowMS()
	var current string
	if err := s.db.QueryRowContext(ctx,
		`SELECT state FROM exploration_nodes WHERE id=? AND kind='goal'`, goalID).Scan(&current); err != nil {
		return err
	}
	from := domain.GoalState(current)
	if !from.CanTransition(to) {
		return fmt.Errorf("goal %d: illegal transition %s -> %s", goalID, from, to)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE exploration_nodes SET state=?, updated_at=? WHERE id=?`, string(to), now, goalID)
	return err
}

// ListTasks lists tasks, newest first.
func (s *Store) ListTasks(ctx context.Context, limit int) ([]domain.Task, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, mode, status, goal_id, budget_json, created_at, started_at, ended_at
		 FROM tasks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out = append(out, *t)
		}
	}
	return out, rows.Err()
}

func (s *Store) UpdateTaskBudget(ctx context.Context, taskID int64, budget map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET budget_json=? WHERE id=?`, jsonText(budget), taskID)
	return err
}
