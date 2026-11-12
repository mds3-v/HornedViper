package domain

import "time"

// ToolCategory groups tools in the management UI. Categories are user-owned;
// the built-in set is only seeded on first start.
type ToolCategory struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// ManagedTool is one tool in the management catalog. Built-in tools are
// compiled into the agent; custom tools currently use the HTTP executor.
type ManagedTool struct {
	ID          int64     `json:"id"`
	Scope       string    `json:"scope"`
	Name        string    `json:"name"`
	Display     string    `json:"display"`
	Description string    `json:"description"`
	CategoryID  int64     `json:"category_id"`
	Category    string    `json:"category"`
	Enabled     bool      `json:"enabled"`
	Essential   bool      `json:"essential"`
	Builtin     bool      `json:"builtin"`
	Deleted     bool      `json:"deleted"`
	Kind        string    `json:"kind"`
	ConfigJSON  string    `json:"config_json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
