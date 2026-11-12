package loghub

import "testing"

func TestHubSnapshotAndSubscribe(t *testing.T) {
	h := New(2)
	ch, cancel := h.Subscribe()
	defer cancel()
	_, _ = h.Write([]byte("one\n"))
	select {
	case line := <-ch:
		if line.Text != "one" {
			t.Fatalf("line = %+v", line)
		}
	default:
		t.Fatal("expected a delivered line")
	}
	_, _ = h.Write([]byte("two\n"))
	_, _ = h.Write([]byte("three\n"))
	snap := h.Snapshot()
	if len(snap) != 2 || snap[0].Text != "two" || snap[1].Text != "three" {
		t.Fatalf("snapshot = %+v", snap)
	}
}
