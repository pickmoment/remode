// Package orchestrator provides multi-session orchestration primitives.
//
// The TurnTracker observes agent events to derive per-session turn state
// (BLOCKED / ACTIVE / IDLE) without polling or adding Capture logic.
package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

// TurnState represents the current activity state of a session's turn.
type TurnState int

const (
	TurnUnknown TurnState = iota // no events seen yet
	TurnActive                   // receiving events; not done
	TurnBlocked                  // waiting for human input (approval/plan/ask)
	TurnIdle                     // no events for >= TurnIdleMS; turn complete
)

// Clock abstracts time.Now() for testability.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// sessionTrack holds mutable turn state for one session.
type sessionTrack struct {
	state     TurnState
	lastEvent time.Time
}

// TurnTracker observes AgentEvents from the session manager and maintains
// per-session BLOCKED/ACTIVE/IDLE state. Waiters can block on WaitIdle.
type TurnTracker struct {
	mu      sync.RWMutex
	states  map[string]*sessionTrack   // session name → state
	waiters map[string][]chan struct{}  // session name → notify channels
	idleMS  int
	clock   Clock
}

// NewTurnTracker creates a TurnTracker.
// idleMS is how many milliseconds of silence after the last ACTIVE event
// promotes the session to IDLE (recommended ≥ 2000 ms).
// clock may be nil (uses the real clock).
func NewTurnTracker(idleMS int, clock Clock) *TurnTracker {
	if clock == nil {
		clock = realClock{}
	}
	return &TurnTracker{
		states:  make(map[string]*sessionTrack),
		waiters: make(map[string][]chan struct{}),
		idleMS:  idleMS,
		clock:   clock,
	}
}

// OnEvent is the observer callback registered with session.Manager.RegisterObserver.
func (t *TurnTracker) OnEvent(name string, ev core.AgentEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	st, ok := t.states[name]
	if !ok {
		st = &sessionTrack{}
		t.states[name] = st
	}

	switch ev.Type {
	case core.EventApprovalPrompt, core.EventPlanPrompt, core.EventAskUserQuestion:
		// Session needs human input — never fire chain/DAG downstream.
		st.state = TurnBlocked
	case core.EventText, core.EventToolUse, core.EventInfoPanel:
		// Active events: reset idle timer.
		if st.state != TurnBlocked {
			st.state = TurnActive
		}
		st.lastEvent = t.clock.Now()
	}
}

// Run starts the background ticker that promotes ACTIVE→IDLE after silence.
// Blocks until ctx is cancelled. Call in a goroutine.
func (t *TurnTracker) Run(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			t.tick(now)
		}
	}
}

func (t *TurnTracker) tick(now time.Time) {
	idleDur := time.Duration(t.idleMS) * time.Millisecond
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, st := range t.states {
		if st.state == TurnActive && !st.lastEvent.IsZero() &&
			now.Sub(st.lastEvent) >= idleDur {
			st.state = TurnIdle
			t.notifyWaiters(name)
		}
	}
}

// notifyWaiters closes/signals all channels waiting on this session.
// Must be called with mu held.
func (t *TurnTracker) notifyWaiters(name string) {
	for _, ch := range t.waiters[name] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	delete(t.waiters, name)
}

// WaitIdle blocks until the named session reaches IDLE state or ctx is cancelled.
// Returns immediately if the session is already IDLE.
func (t *TurnTracker) WaitIdle(ctx context.Context, name string) error {
	t.mu.Lock()
	if st, ok := t.states[name]; ok && st.state == TurnIdle {
		t.mu.Unlock()
		return nil
	}
	ch := make(chan struct{}, 1)
	t.waiters[name] = append(t.waiters[name], ch)
	t.mu.Unlock()

	select {
	case <-ctx.Done():
		// Clean up the waiter channel to avoid goroutine leak.
		t.mu.Lock()
		t.removeWaiter(name, ch)
		t.mu.Unlock()
		return ctx.Err()
	case <-ch:
		return nil
	}
}

// State returns the current TurnState for the named session.
func (t *TurnTracker) State(name string) TurnState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if st, ok := t.states[name]; ok {
		return st.state
	}
	return TurnUnknown
}

// ForceIdle transitions a session to IDLE and notifies waiters.
// Used by tests to simulate turn completion without real timing.
func (t *TurnTracker) ForceIdle(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.states[name]; !ok {
		t.states[name] = &sessionTrack{}
	}
	t.states[name].state = TurnIdle
	t.notifyWaiters(name)
}

// removeWaiter removes ch from the waiters list for name.
// Must be called with mu held.
func (t *TurnTracker) removeWaiter(name string, ch chan struct{}) {
	list := t.waiters[name]
	for i, c := range list {
		if c == ch {
			t.waiters[name] = append(list[:i], list[i+1:]...)
			return
		}
	}
}
