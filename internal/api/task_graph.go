package api

import (
	"strings"

	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

type graphNodeView struct {
	ID       int64
	Kind     string
	State    string
	Label    string
	Short    string
	Detail   string
	CX, CY   int
	R        int
	Class    string
	AssetIDs []int64
}

type graphEdgeView struct {
	X1, Y1, X2, Y2 int
	Rel            string
}

type taskGraphView struct {
	Nodes      []graphNodeView
	Edges      []graphEdgeView
	SVGWidth   int
	SVGHeight  int
	TotalNodes int
	ShownNodes int
	Assets     []domain.Asset
	Anchors    []store.GraphAnchor
}

func buildTaskGraph(g *store.TaskGraph, total int) *taskGraphView {
	if g == nil {
		return nil
	}
	view := &taskGraphView{TotalNodes: total, SVGWidth: 1240}
	// The graph is an overview, not an exhaustive dump. Keep the highest-signal
	// nodes: goals and findings first, then supporting facts and intents.
	caps := map[string]int{"goal": 2, "finding": 12, "fact": 10, "intent": 8}
	columns := map[string]int{"goal": 0, "intent": 1, "fact": 2, "finding": 3}
	colX := []int{110, 420, 730, 1040}
	perCol := map[int]int{}
	pos := map[int64][2]int{}
	for _, n := range g.Nodes {
		col, ok := columns[n.Kind]
		if !ok {
			continue
		}
		if perCol[col] >= caps[n.Kind] {
			continue
		}
		row := perCol[col]
		perCol[col]++
		y := 90 + row*88
		pos[n.ID] = [2]int{colX[col], y}
		class := "graph-" + n.Kind
		if n.State != "confirmed" && n.State != "succeeded" && n.State != "met" {
			class += " graph-muted"
		}
		view.Nodes = append(view.Nodes, graphNodeView{
			ID: n.ID, Kind: n.Kind, State: n.State, Label: n.Label,
			Short: truncateUIRunes(n.Label, 11), Detail: n.Detail,
			CX: colX[col], CY: y, R: 34, Class: class, AssetIDs: n.AssetIDs,
		})
		view.ShownNodes++
	}
	maxY := 0
	for _, n := range view.Nodes {
		if n.CY+50 > maxY {
			maxY = n.CY + 50
		}
	}
	view.SVGHeight = maxY + 30
	if view.SVGHeight < 220 {
		view.SVGHeight = 220
	}
	colOrder := map[string]int{"goal": 0, "intent": 1, "fact": 2, "finding": 3}
	for _, e := range g.Edges {
		a, okA := pos[e.SrcID]
		b, okB := pos[e.DstID]
		if !okA || !okB {
			continue
		}
		// Point the line at the circle boundary rather than its center.
		ax, ay := a[0], a[1]
		bx, by := b[0], b[1]
		ra, rb := 34, 34
		if colOrder[graphKindOf(g, e.SrcID)] <= colOrder[graphKindOf(g, e.DstID)] {
			ax += ra
			bx -= rb
		} else {
			ax -= ra
			bx += rb
		}
		view.Edges = append(view.Edges, graphEdgeView{X1: ax, Y1: ay, X2: bx, Y2: by, Rel: e.Rel})
	}
	view.Assets = g.Assets
	view.Anchors = g.Anchors
	return view
}

func graphKindOf(g *store.TaskGraph, id int64) string {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n.Kind
		}
	}
	return ""
}

func truncateUIRunes(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func graphKindLabel(kind string) string {
	switch kind {
	case "goal":
		return "目标"
	case "intent":
		return "意图"
	case "fact":
		return "事实"
	case "finding":
		return "发现"
	default:
		return kind
	}
}

func graphStateLabel(state string) string {
	if state == "" {
		return "—"
	}
	return state
}
