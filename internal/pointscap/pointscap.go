// Package pointscap rate-limits how many points a single node can earn within a
// rolling time window, regardless of how often its prover submits. Without it, a
// local-prover node can farm points by submitting far faster than its tier's
// intended cadence. The per-window cap is passed per call so it can scale by
// node tier (see types.NodeType.PointsCapPerWindow).
//
// The package-level limiter is DISABLED by default (Allow returns the full
// amount), so tests that assert exact point accrual are unaffected. Production
// turns it on once at startup via Enable.
package pointscap

import (
	"sync"
	"time"
)

// Limiter caps points-per-window per node. Disabled until Enable is called.
type Limiter struct {
	mu      sync.Mutex
	enabled bool
	window  time.Duration
	state   map[string]windowState
	now     func() time.Time
}

type windowState struct {
	start time.Time
	used  uint64
}

// NewLimiter builds a disabled limiter with the given window.
func NewLimiter(window time.Duration) *Limiter {
	return &Limiter{
		window: window,
		state:  make(map[string]windowState),
		now:    time.Now,
	}
}

// Enable turns the cap on with the given window. Call once at startup.
func (l *Limiter) Enable(window time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled = true
	if window > 0 {
		l.window = window
	}
}

// Allow returns how many of `want` points may be credited to nodeID right now
// without exceeding cap in the current rolling window, and records that many as
// used. Returns want unchanged when the limiter is disabled or cap == 0.
func (l *Limiter) Allow(nodeID string, want, cap uint64) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.enabled || cap == 0 {
		return want
	}

	now := l.now()
	s := l.state[nodeID]
	if s.start.IsZero() || now.Sub(s.start) >= l.window {
		s = windowState{start: now}
	}

	remaining := uint64(0)
	if s.used < cap {
		remaining = cap - s.used
	}
	if want > remaining {
		want = remaining
	}
	s.used += want
	l.state[nodeID] = s
	return want
}

// def is the package-level limiter. Disabled until Enable is called, so it's a
// no-op in tests and an active cap in production.
var def = NewLimiter(10 * time.Minute)

// Allow delegates to the package-level limiter.
func Allow(nodeID string, want, cap uint64) uint64 { return def.Allow(nodeID, want, cap) }

// Enable turns on the package-level cap with the given window. Call once at
// startup before serving traffic.
func Enable(window time.Duration) { def.Enable(window) }
