package store

import (
	"context"
	"encoding/json"

	"hornedviper/internal/domain"
)

// TokenUsage sums usage_json across all model calls.
func (s *Store) TokenUsage(ctx context.Context) (domain.TokenUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT usage_json FROM llm_calls`)
	if err != nil {
		return domain.TokenUsage{}, err
	}
	defer rows.Close()
	var out domain.TokenUsage
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		var u struct {
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_tokens"`
			CacheWrite int `json:"cache_write_tokens"`
		}
		if json.Unmarshal([]byte(raw), &u) == nil {
			out.Calls++
			out.Input += u.Input
			out.Output += u.Output
			out.CacheRead += u.CacheRead
			out.CacheWrite += u.CacheWrite
		}
	}
	return out, rows.Err()
}
