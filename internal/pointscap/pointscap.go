// Package pointscap rate-limits how many points a single node can earn within a
// rolling time window, regardless of how often its prover submits. Without it, a
// local-prover node can farm challenge points by submitting far faster than its
// tier's intended cadence.
//
// The package-level limiter is DISABLED by default (Allow returns the full
// amount), so tests that assert exact point accrual are unaffected. Production
// turns it on once at startup via SetLimits.
package pointscap

import (
	"sync"
	"time"
)

// Limiter caps points-per-window per node. A cap of 0 means "disabled".
type Limiter struct {
	mu     sync.Mutex
	cap    uint64
	window time.Duration
	state  map[string]windowState
	now    func() time.Time
}

type windowState struct {
	start time.Time
	used  uint64
}

// NewLimiter builds a limiter. capPerWindow == 0 disables capping.
func NewLimiter(capPerWindow uint64, window time.Duration) *Limiter {
	return &Limiter{
		cap:    capPerWindow,
		window: window,
		state:  make(map[string]windowState),
		now:    time.Now,
	}
}

// Allow returns how many of `want` points may be credited to nodeID right now
// without exceeding the cap in the current rolling window, and records that
// many as used. When disabled (cap == 0) it returns want unchanged.
func (l *Limiter) Allow(nodeID string, want uint64) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cap == 0 {
		return want
	}

	now := l.now()
	s := l.state[nodeID]
	if s.start.IsZero() || now.Sub(s.start) >= l.window {
		s = windowState{start: now}
	}

	remaining := uint64(0)
	if s.used < l.cap {
		remaining = l.cap - s.used
	}
	if want > remaining {
		want = remaining
	}
	s.used += want
	l.state[nodeID] = s
	return want
}

// def is the package-level limiter. Disabled until SetLimits is called, so it's
// a no-op in tests and an active cap in production.
var def = NewLimiter(0, 0)

// Allow delegates to the package-level limiter.
func Allow(nodeID string, want uint64) uint64 { return def.Allow(nodeID, want) }

// SetLimits enables (or reconfigures) the package-level cap. Call once at
// startup before serving traffic. capPerWindow == 0 disables it.
func SetLimits(capPerWindow uint64, window time.Duration) {
	def = NewLimiter(capPerWindow, window)
}
