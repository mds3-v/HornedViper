package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"hornedviper/internal/domain"
)

// GraphNode is one exploration node prepared for the task graph view.
type GraphNode struct {
	ID        int64
	Kind      string
	State     string
	Label     string
	Detail    string
	AssetIDs  []int64
	CreatedAt time.Time
}

type GraphEdge struct {
	SrcID int64
	DstID int64
	Rel   string
}

type GraphAnchor struct {
	NodeID  int64
	AssetID int64
}

type TaskGraph struct {
	Nodes   []GraphNode
	Edges   []GraphEdge
	Anchors []GraphAnchor
	Assets  []domain.Asset
}

// ListTaskGraph returns a bounded graph for one task. Newest nodes win when the
// graph is larger than limit; edges and anchors are restricted to those nodes.
func (s *Store) ListTaskGraph(ctx context.Context, taskID int64, limit int) (*TaskGraph, error) {
	if limit <= 0 {
		limit = 80
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, state, payload_json, created_at FROM exploration_nodes
		 WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	g := &TaskGraph{}
	ids := map[int64]bool{}
	for rows.Next() {
		var n GraphNode
		var payload string
		var created int64
		if err := rows.Scan(&n.ID, &n.Kind, &n.State, &payload, &created); err != nil {
			rows.Close()
			return nil, err
		}
		n.CreatedAt = msToTime(created)
		n.Label, n.Detail = graphNodeText(n.Kind, payload)
		ids[n.ID] = true
		g.Nodes = append(g.Nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	edges, err := s.db.QueryContext(ctx,
		`SELECT src_id, dst_id, rel FROM exploration_edges WHERE task_id=?`, taskID)
	if err != nil {
		return g, nil
	}
	for edges.Next() {
		var e GraphEdge
		if err := edges.Scan(&e.SrcID, &e.DstID, &e.Rel); err != nil {
			continue
		}
		if ids[e.SrcID] && ids[e.DstID] {
			g.Edges = append(g.Edges, e)
		}
	}
	edges.Close()
	assetIDs := map[int64]bool{}
	anchors, err := s.db.QueryContext(ctx,
		`SELECT a.node_id, a.asset_id FROM exploration_anchors a
		 JOIN exploration_nodes n ON n.id=a.node_id WHERE n.task_id=?`, taskID)
	if err != nil {
		return g, nil
	}
	for anchors.Next() {
		var a GraphAnchor
		if err := anchors.Scan(&a.NodeID, &a.AssetID); err != nil {
			continue
		}
		if ids[a.NodeID] {
			g.Anchors = append(g.Anchors, a)
			assetIDs[a.AssetID] = true
			for i := range g.Nodes {
				if g.Nodes[i].ID == a.NodeID {
					g.Nodes[i].AssetIDs = append(g.Nodes[i].AssetIDs, a.AssetID)
				}
			}
		}
	}
	anchors.Close()
	if len(assetIDs) == 0 {
		return g, nil
	}
	assets, err := s.db.QueryContext(ctx,
		`SELECT id, type, canonical_key, value, parent_id, scope_status, source, first_seen, last_seen FROM assets ORDER BY id`)
	if err != nil {
		return g, nil
	}
	for assets.Next() {
		var a domain.Asset
		var parent int64
		var first, last int64
		if err := assets.Scan(&a.ID, &a.Type, &a.CanonicalKey, &a.Value, &parent, &a.ScopeStatus, &a.Source, &first, &last); err != nil {
			continue
		}
		if assetIDs[a.ID] {
			a.ParentID = parent
			a.FirstSeen, a.LastSeen = msToTime(first), msToTime(last)
			g.Assets = append(g.Assets, a)
		}
	}
	assets.Close()
	return g, nil
}

func graphNodeText(kind, payload string) (string, string) {
	var p map[string]any
	_ = json.Unmarshal([]byte(payload), &p)
	str := func(k string) string {
		if v, ok := p[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	switch kind {
	case "goal":
		return truncateGraph(str("text"), 32), str("text")
	case "intent":
		detail := str("rationale")
		if detail == "" {
			detail = str("summary")
		}
		return truncateGraph(str("summary"), 32), detail
	case "fact":
		detail := str("detail")
		if detail == "" {
			detail = str("evidence")
		}
		return truncateGraph(str("summary"), 32), detail
	case "finding":
		return truncateGraph(str("title"), 32), str("evidence")
	default:
		return kind, ""
	}
}

func truncateGraph(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// CleanInvalidAnchors removes anchors that were never backed by an
// asset.recorded event for the node's task. This repairs older runs where a
// model guessed global asset ids from another task.
func (s *Store) CleanInvalidAnchors(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `
DELETE FROM exploration_anchors
WHERE EXISTS (
  SELECT 1 FROM exploration_nodes n
  WHERE n.id = exploration_anchors.node_id
    AND NOT EXISTS (
      SELECT 1 FROM workflow_events e
      WHERE e.task_id = n.task_id
        AND e.type = 'asset.recorded'
        AND e.subject_id = exploration_anchors.asset_id
    )
)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
