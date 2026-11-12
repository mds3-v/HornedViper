package loghub

import (
	"strings"
	"sync"
	"time"
)

type Line struct {
	Time string `json:"time"`
	Text string `json:"text"`
}

// Hub is a bounded in-memory log buffer with SSE subscribers.
type Hub struct {
	mu    sync.Mutex
	max   int
	lines []Line
	subs  map[chan Line]struct{}
	sink  func(Line)
}

func New(max int) *Hub {
	if max <= 0 {
		max = 500
	}
	return &Hub{max: max, subs: map[chan Line]struct{}{}}
}

func (h *Hub) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\n")
	if text == "" {
		return len(p), nil
	}
	line := Line{Time: time.Now().Format("01-02 15:04:05"), Text: text}
	h.mu.Lock()
	h.lines = append(h.lines, line)
	if len(h.lines) > h.max {
		h.lines = h.lines[len(h.lines)-h.max:]
	}
	for ch := range h.subs {
		select {
		case ch <- line:
		default:
		}
	}
	sink := h.sink
	h.mu.Unlock()
	if sink != nil {
		sink(line)
	}
	return len(p), nil
}

// SetSink installs a persistent sink for log lines.
func (h *Hub) SetSink(sink func(Line)) {
	h.mu.Lock()
	h.sink = sink
	h.mu.Unlock()
}

func (h *Hub) Snapshot() []Line {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Line(nil), h.lines...)
}

func (h *Hub) Subscribe() (<-chan Line, func()) {
	ch := make(chan Line, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}
