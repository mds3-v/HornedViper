-- HornedViper Phase 0 schema (SQLite)
-- Timestamps are Unix milliseconds (INTEGER).

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS tasks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    title         TEXT    NOT NULL,
    mode          TEXT    NOT NULL,
    status        TEXT    NOT NULL,
    goal_id       INTEGER NOT NULL DEFAULT 0,
    budget_json   TEXT    NOT NULL DEFAULT '{}',
    created_at    INTEGER NOT NULL,
    started_at    INTEGER,
    ended_at      INTEGER
);

CREATE TABLE IF NOT EXISTS exploration_nodes (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id               INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind                  TEXT    NOT NULL,
    state                 TEXT    NOT NULL,
    payload_json          TEXT    NOT NULL DEFAULT '{}',
    priority              INTEGER NOT NULL DEFAULT 0,
    origin                TEXT,
    created_by_role       TEXT,
    created_by_work_id    INTEGER,
    created_by_llm_call_id INTEGER,
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_nodes_task_kind_state
    ON exploration_nodes(task_id, kind, state);
CREATE INDEX IF NOT EXISTS idx_nodes_task_kind_priority
    ON exploration_nodes(task_id, kind, priority DESC);

CREATE TABLE IF NOT EXISTS exploration_edges (
    task_id    INTEGER NOT NULL,
    src_id     INTEGER NOT NULL,
    dst_id     INTEGER NOT NULL,
    rel        TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (task_id, src_id, rel, dst_id)
);
CREATE INDEX IF NOT EXISTS idx_edges_src ON exploration_edges(task_id, src_id, rel);
CREATE INDEX IF NOT EXISTS idx_edges_dst ON exploration_edges(task_id, dst_id, rel);

CREATE TABLE IF NOT EXISTS assets (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    type          TEXT    NOT NULL,
    canonical_key TEXT    NOT NULL,
    value         TEXT    NOT NULL,
    payload_json  TEXT    NOT NULL DEFAULT '{}',
    parent_id     INTEGER NOT NULL DEFAULT 0,
    scope_status  TEXT    NOT NULL DEFAULT 'unknown',
    source        TEXT    NOT NULL DEFAULT 'agent',
    first_seen    INTEGER NOT NULL,
    last_seen     INTEGER NOT NULL,
    UNIQUE(type, canonical_key)
);

CREATE TABLE IF NOT EXISTS asset_tasks (
    asset_id  INTEGER NOT NULL,
    task_id   INTEGER NOT NULL,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (asset_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_asset_tasks_task ON asset_tasks(task_id, asset_id);

CREATE TABLE IF NOT EXISTS deleted_entities (
    kind       TEXT    NOT NULL,
    entity_id  INTEGER NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    deleted_at INTEGER NOT NULL,
    PRIMARY KEY (kind, entity_id)
);

CREATE TABLE IF NOT EXISTS challenge_completions (
    task_id      INTEGER NOT NULL,
    challenge    TEXT NOT NULL,
    completed_at INTEGER NOT NULL,
    PRIMARY KEY (task_id, challenge)
);

CREATE TABLE IF NOT EXISTS asset_edges (
    src_id     INTEGER NOT NULL,
    dst_id     INTEGER NOT NULL,
    rel        TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (src_id, rel, dst_id)
);

CREATE TABLE IF NOT EXISTS exploration_anchors (
    node_id  INTEGER NOT NULL,
    asset_id INTEGER NOT NULL,
    PRIMARY KEY (node_id, asset_id)
);
CREATE INDEX IF NOT EXISTS idx_anchors_asset ON exploration_anchors(asset_id);

CREATE TABLE IF NOT EXISTS works (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id          INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    intent_id        INTEGER NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    worker_id        TEXT    NOT NULL,
    lane_id          TEXT,
    state            TEXT    NOT NULL,
    lease_expires_at INTEGER,
    started_at       INTEGER,
    ended_at         INTEGER,
    stop_reason      TEXT,
    tokens_in        INTEGER NOT NULL DEFAULT 0,
    tokens_out       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_works_task_state ON works(task_id, state);
CREATE INDEX IF NOT EXISTS idx_works_intent ON works(intent_id);

CREATE TABLE IF NOT EXISTS approvals (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id                INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    intent_id              INTEGER NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    risk                   TEXT    NOT NULL,
    dangerous_actions_json TEXT    NOT NULL DEFAULT '[]',
    decision               TEXT    NOT NULL,
    decided_by             TEXT,
    reason                 TEXT,
    created_at             INTEGER NOT NULL,
    decided_at             INTEGER
);
CREATE INDEX IF NOT EXISTS idx_approvals_task_decision ON approvals(task_id, decision);

CREATE TABLE IF NOT EXISTS workflow_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    type         TEXT    NOT NULL,
    actor_type   TEXT    NOT NULL,
    actor_id     TEXT    NOT NULL,
    subject_type TEXT,
    subject_id   INTEGER,
    payload_json TEXT    NOT NULL DEFAULT '{}',
    created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_task ON workflow_events(task_id, id);

CREATE TABLE IF NOT EXISTS activities (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    work_id      INTEGER NOT NULL DEFAULT 0,
    intent_id    INTEGER NOT NULL DEFAULT 0,
    role         TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    tool         TEXT,
    tool_use_id  TEXT,
    summary      TEXT,
    detail       TEXT,
    llm_call_id  INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activities_task ON activities(task_id, id);
CREATE INDEX IF NOT EXISTS idx_activities_work ON activities(work_id, id);

CREATE TABLE IF NOT EXISTS llm_calls (
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
CREATE INDEX IF NOT EXISTS idx_llm_calls_task ON llm_calls(task_id, id);

CREATE TABLE IF NOT EXISTS artifacts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    work_id    INTEGER NOT NULL DEFAULT 0,
    type       TEXT    NOT NULL,
    path       TEXT    NOT NULL,
    hash       TEXT    NOT NULL DEFAULT '',
    bytes      INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_artifacts_task ON artifacts(task_id, id);

CREATE TABLE IF NOT EXISTS task_constraints (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    type       TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS planner_todos (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    content      TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'pending',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS task_scope (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL,
    value      TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS flag_submissions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    finding_id INTEGER NOT NULL,
    flag       TEXT    NOT NULL,
    flag_hash  TEXT    NOT NULL,
    accepted   INTEGER NOT NULL DEFAULT 0,
    message    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE(task_id, flag_hash)
);

CREATE TABLE IF NOT EXISTS agents (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    key           TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    description   TEXT    NOT NULL DEFAULT '',
    system_prompt TEXT    NOT NULL DEFAULT '',
    tools_json    TEXT    NOT NULL DEFAULT '[]',
    model         TEXT    NOT NULL DEFAULT '',
    builtin       INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS intercept_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    priority   INTEGER NOT NULL DEFAULT 100,
    target     TEXT    NOT NULL DEFAULT 'input',
    match_type TEXT    NOT NULL DEFAULT 'regex',
    pattern    TEXT    NOT NULL,
    mode       TEXT    NOT NULL DEFAULT 'ask',
    enabled    INTEGER NOT NULL DEFAULT 1,
    builtin    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS traffic (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL DEFAULT 0,
    conversation_id INTEGER NOT NULL DEFAULT 0,
    work_id         INTEGER NOT NULL DEFAULT 0,
    intent_id       INTEGER NOT NULL DEFAULT 0,
    method        TEXT    NOT NULL DEFAULT '',
    host          TEXT    NOT NULL DEFAULT '',
    url           TEXT    NOT NULL DEFAULT '',
    status_code   INTEGER NOT NULL DEFAULT 0,
    content_type  TEXT    NOT NULL DEFAULT '',
    request_raw   TEXT    NOT NULL DEFAULT '',
    response_raw  TEXT    NOT NULL DEFAULT '',
    request_size  INTEGER NOT NULL DEFAULT 0,
    response_size INTEGER NOT NULL DEFAULT 0,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    error         TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_traffic_task ON traffic(task_id, id);

CREATE TABLE IF NOT EXISTS chat_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    role       TEXT    NOT NULL,
    content    TEXT    NOT NULL DEFAULT '',
    thinking   TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL DEFAULT 'done',
    error      TEXT    NOT NULL DEFAULT '',
    attachments_json TEXT NOT NULL DEFAULT '[]',
    thinking_ms INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chat_task ON chat_messages(task_id, id);

CREATE TABLE IF NOT EXISTS chat_summaries (
    task_id    INTEGER PRIMARY KEY,
    summary    TEXT    NOT NULL DEFAULT '',
    covered_id INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_logs_id ON logs(id);

CREATE TABLE IF NOT EXISTS external_tasks (
    platform    TEXT    NOT NULL,
    external_id TEXT    NOT NULL,
    task_id     INTEGER NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (platform, external_id)
);

CREATE TABLE IF NOT EXISTS conversations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    title           TEXT    NOT NULL DEFAULT '',
    reasoning_level TEXT    NOT NULL DEFAULT 'High',
    permission_mode TEXT    NOT NULL DEFAULT 'workspace-write',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversations_updated ON conversations(updated_at);

CREATE TABLE IF NOT EXISTS conversation_messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL,
    role            TEXT    NOT NULL,
    content         TEXT    NOT NULL DEFAULT '',
    thinking        TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'done',
    error           TEXT    NOT NULL DEFAULT '',
    attachments_json TEXT   NOT NULL DEFAULT '[]',
    thinking_ms     INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversation_messages ON conversation_messages(conversation_id, id);

CREATE TABLE IF NOT EXISTS chat_todos (
    owner_type TEXT    NOT NULL,
    owner_id   INTEGER NOT NULL,
    items_json TEXT    NOT NULL DEFAULT '[]',
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner_type, owner_id)
);

CREATE TABLE IF NOT EXISTS tool_settings (
    scope      TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (scope, name)
);

CREATE TABLE IF NOT EXISTS tool_categories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS managed_tools (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    display     TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    category_id INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    essential   INTEGER NOT NULL DEFAULT 0,
    builtin     INTEGER NOT NULL DEFAULT 1,
    deleted     INTEGER NOT NULL DEFAULT 0,
    kind        TEXT    NOT NULL DEFAULT 'builtin',
    config_json TEXT    NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE(scope, name)
);
CREATE INDEX IF NOT EXISTS idx_managed_tools_scope ON managed_tools(scope, deleted, name);

CREATE TABLE IF NOT EXISTS task_flags (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     INTEGER NOT NULL,
    challenge   TEXT    NOT NULL DEFAULT '',
    flag        TEXT    NOT NULL,
    flag_hash   TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'found',
    source      TEXT    NOT NULL DEFAULT 'manual',
    finding_id  INTEGER NOT NULL DEFAULT 0,
    deleted     INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE(task_id, flag_hash)
);
CREATE INDEX IF NOT EXISTS idx_task_flags_task ON task_flags(task_id, deleted, id);

CREATE TABLE IF NOT EXISTS conversation_summaries (
    conversation_id INTEGER PRIMARY KEY,
    summary         TEXT    NOT NULL DEFAULT '',
    covered_id      INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS swarm_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL,
    intent_id       INTEGER NOT NULL UNIQUE,
    lanes           INTEGER NOT NULL,
    quorum          INTEGER NOT NULL,
    strategies_json TEXT    NOT NULL DEFAULT '[]',
    briefs_json     TEXT    NOT NULL DEFAULT '[]',
    mode            TEXT    NOT NULL DEFAULT 'swarm',
    planned_by      TEXT    NOT NULL DEFAULT '',
    lane_assets_json TEXT   NOT NULL DEFAULT '[]',
    state           TEXT    NOT NULL DEFAULT 'running',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_swarm_runs_task ON swarm_runs(task_id, state);

CREATE TABLE IF NOT EXISTS swarm_lane_results (
    work_id     INTEGER PRIMARY KEY,
    intent_id   INTEGER NOT NULL,
    lane_id     TEXT    NOT NULL,
    status      TEXT    NOT NULL,
    result_json TEXT    NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_swarm_lane_results_intent ON swarm_lane_results(intent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_works_swarm_lane ON works(intent_id, lane_id) WHERE lane_id <> '';

CREATE TABLE IF NOT EXISTS quarantine_items (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id               INTEGER NOT NULL,
    intent_id             INTEGER NOT NULL DEFAULT 0,
    work_id               INTEGER NOT NULL DEFAULT 0,
    lane_id               TEXT    NOT NULL DEFAULT '',
    source_kind           TEXT    NOT NULL,
    reason                TEXT    NOT NULL DEFAULT '',
    summary               TEXT    NOT NULL DEFAULT '',
    payload_json          TEXT    NOT NULL DEFAULT '{}',
    artifact_path         TEXT    NOT NULL DEFAULT '',
    payload_hash          TEXT    NOT NULL DEFAULT '',
    candidate_flags_json  TEXT    NOT NULL DEFAULT '[]',
    status                TEXT    NOT NULL DEFAULT 'open',
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    reviewed_at           INTEGER NOT NULL DEFAULT 0,
    reviewed_by           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_quarantine_task ON quarantine_items(task_id, status, id);
CREATE INDEX IF NOT EXISTS idx_quarantine_hash ON quarantine_items(task_id, payload_hash);

CREATE TABLE IF NOT EXISTS swarm_conflicts (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id        INTEGER NOT NULL,
    intent_id      INTEGER NOT NULL,
    type           TEXT    NOT NULL,
    subject        TEXT    NOT NULL,
    subject_hash   TEXT    NOT NULL,
    lane_ids_json  TEXT    NOT NULL DEFAULT '[]',
    evidence_json  TEXT    NOT NULL DEFAULT '[]',
    status         TEXT    NOT NULL DEFAULT 'open',
    decision       TEXT    NOT NULL DEFAULT '',
    reason         TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_swarm_conflicts_task ON swarm_conflicts(task_id, status, id);
