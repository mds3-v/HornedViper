package kernel

import (
	"context"
	"sync"
	"time"

	"hornedviper/internal/domain"
)

// SchedulerConfig controls planner wake batching.
type SchedulerConfig struct {
	Debounce  time.Duration
	MaxDelay  time.Duration
	MinGap    time.Duration
	QueueSize int
}

// Wake is one planner wake with the events that triggered it.
type Wake struct {
	Events     []*domain.Event
	HighSignal bool
	At         time.Time
}

// Scheduler batches workflow events into planner wakes.
//
// Design:
//   - high-signal events (findings, work completion, hints, approvals) wake immediately;
//   - normal events (facts, assets) are debounced;
//   - MaxDelay is a hard cap measured from the first pending event, so a continuous
//     stream cannot postpone planning forever;
//   - only one planner runs at a time: the scheduler waits for Ack before emitting
//     the next wake.
type Scheduler struct {
	cfg SchedulerConfig

	in   chan *domain.Event
	out  chan Wake
	ack  chan struct{}
	stop chan struct{}

	mu     sync.Mutex
	closed bool
}

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	if cfg.Debounce <= 0 {
		cfg.Debounce = 30 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 200 * time.Millisecond
	}
	if cfg.MinGap < 0 {
		cfg.MinGap = 0
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 4096
	}
	return &Scheduler{
		cfg:  cfg,
		in:   make(chan *domain.Event, cfg.QueueSize),
		out:  make(chan Wake, 1),
		ack:  make(chan struct{}, 1),
		stop: make(chan struct{}),
	}
}

// Notify enqueues an event. It never blocks; a full queue drops the event and
// the next wake still sees the persisted graph state.
func (s *Scheduler) Notify(e *domain.Event) {
	if e == nil || !ShouldNotifyPlanner(e.Type) {
		return
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	select {
	case s.in <- e:
	default:
	}
}

// Wakes is the planner wake channel.
func (s *Scheduler) Wakes() <-chan Wake { return s.out }

// Ack marks the current planner run complete.
func (s *Scheduler) Ack() {
	select {
	case s.ack <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stop)
	s.mu.Unlock()
}

func (s *Scheduler) Run(ctx context.Context) {
	var pending []*domain.Event
	firstAt := time.Time{}
	busy := false

	var timer *time.Timer
	var timerC <-chan time.Time
	stopTimer := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer = nil
			timerC = nil
		}
	}
	arm := func(d time.Duration) {
		stopTimer()
		timer = time.NewTimer(d)
		timerC = timer.C
	}
	emit := func(high bool) {
		if busy || len(pending) == 0 {
			return
		}
		events := pending
		pending = nil
		firstAt = time.Time{}
		stopTimer()
		busy = true
		select {
		case s.out <- Wake{Events: events, HighSignal: high, At: time.Now()}:
		case <-ctx.Done():
		case <-s.stop:
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopTimer()
			return
		case <-s.stop:
			stopTimer()
			return
		case e := <-s.in:
			if e == nil {
				continue
			}
			high := IsHighSignal(e.Type)
			pending = append(pending, e)
			if firstAt.IsZero() {
				firstAt = time.Now()
			}
			if busy {
				continue
			}
			if high {
				emit(true)
				continue
			}
			if time.Since(firstAt) >= s.cfg.MaxDelay {
				emit(false)
				continue
			}
			arm(s.cfg.Debounce)
		case <-timerC:
			emit(false)
		case <-s.ack:
			busy = false
			if len(pending) == 0 {
				continue
			}
			high := false
			for _, e := range pending {
				if IsHighSignal(e.Type) {
					high = true
					break
				}
			}
			if high {
				emit(true)
				continue
			}
			if time.Since(firstAt) >= s.cfg.MaxDelay {
				emit(false)
				continue
			}
			d := s.cfg.Debounce
			if s.cfg.MinGap > d {
				d = s.cfg.MinGap
			}
			arm(d)
		}
	}
}

// ShouldNotifyPlanner filters out events produced by the planner itself.
// Otherwise a planner run would immediately schedule another planner run.
func ShouldNotifyPlanner(t domain.EventType) bool {
	switch t {
	case domain.EventIntentProposed,
		domain.EventIntentApproved,
		domain.EventIntentRejected,
		domain.EventWorkClaimed,
		domain.EventGoalMetProposed,
		domain.EventGoalAccepted,
		domain.EventGoalRejected,
		domain.EventPlannerNoIntent,
		domain.EventFlagSubmitted:
		return false
	}
	return true
}

// IsHighSignal reports whether an event should bypass debounce.
func IsHighSignal(t domain.EventType) bool {
	switch t {
	case domain.EventFindingReported,
		domain.EventWorkFinished,
		domain.EventWorkFailed,
		domain.EventWorkTimedOut,
		domain.EventGoalSet,
		domain.EventHintAdded,
		domain.EventConstraintAdded,
		domain.EventTaskStarted,
		domain.EventTaskResumed,
		domain.EventFlagAccepted,
		domain.EventFlagRejected,
		domain.EventWorkersIdle:
		return true
	}
	return false
}
