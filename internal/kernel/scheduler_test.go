package kernel

import (
	"context"
	"testing"
	"time"

	"hornedviper/internal/domain"
)

func startScheduler(t *testing.T, cfg SchedulerConfig) (*Scheduler, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := NewScheduler(cfg)
	go s.Run(ctx)
	t.Cleanup(func() {
		s.Stop()
		cancel()
	})
	return s, cancel
}

func TestSchedulerHighSignalImmediate(t *testing.T) {
	s, _ := startScheduler(t, SchedulerConfig{Debounce: 500 * time.Millisecond, MaxDelay: 2 * time.Second})
	start := time.Now()
	s.Notify(&domain.Event{Type: domain.EventFindingReported})
	select {
	case wake := <-s.Wakes():
		if time.Since(start) > 200*time.Millisecond {
			t.Fatalf("high-signal wake was too slow: %v", time.Since(start))
		}
		if len(wake.Events) != 1 || !wake.HighSignal {
			t.Fatalf("unexpected wake: %+v", wake)
		}
	case <-time.After(time.Second):
		t.Fatal("no high-signal wake")
	}
	s.Ack()
}

func TestSchedulerDebouncesNormalEvents(t *testing.T) {
	s, _ := startScheduler(t, SchedulerConfig{Debounce: 40 * time.Millisecond, MaxDelay: time.Second})
	s.Notify(&domain.Event{Type: domain.EventFactRecorded})
	s.Notify(&domain.Event{Type: domain.EventFactRecorded})
	select {
	case wake := <-s.Wakes():
		if len(wake.Events) != 2 {
			t.Fatalf("expected both events in one wake, got %d", len(wake.Events))
		}
	case <-time.After(time.Second):
		t.Fatal("no debounced wake")
	}
	s.Ack()
}

func TestSchedulerIgnoresPlannerInternalEvents(t *testing.T) {
	s, _ := startScheduler(t, SchedulerConfig{Debounce: 20 * time.Millisecond, MaxDelay: 200 * time.Millisecond})
	s.Notify(&domain.Event{Type: domain.EventIntentProposed})
	select {
	case wake := <-s.Wakes():
		t.Fatalf("planner-internal event should not wake planner: %+v", wake)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestSchedulerMaxDelayCapsContinuousStream(t *testing.T) {
	s, _ := startScheduler(t, SchedulerConfig{Debounce: 80 * time.Millisecond, MaxDelay: 150 * time.Millisecond})
	start := time.Now()
	for i := 0; i < 4; i++ {
		s.Notify(&domain.Event{Type: domain.EventFactRecorded})
		time.Sleep(40 * time.Millisecond)
	}
	select {
	case wake := <-s.Wakes():
		if time.Since(start) > 500*time.Millisecond {
			t.Fatalf("maxDelay did not cap the stream: %v", time.Since(start))
		}
		if len(wake.Events) == 0 {
			t.Fatal("wake carried no events")
		}
	case <-time.After(time.Second):
		t.Fatal("no wake despite maxDelay")
	}
	s.Ack()
}
