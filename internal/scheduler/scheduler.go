// Package scheduler runs the server-side verification tickers described in
// ADR-0007 (docs/adr/0007-scheduler-driven-verification.md). It owns three
// independent goroutines:
//
//   - heartbeat: every HeartbeatInterval, ping each active exposed-RPC node
//     and persist a HeartbeatRecord on success.
//   - challenge dispatcher: every ChallengeCheckInterval, look at every
//     active node and verify the ones whose last verification is older than
//     their tier's NodeType.ChallengeFrequencyMinutes() interval. Local-prover
//     nodes are NOT dispatched server-side; they continue to pull challenges
//     via /challenges/request.
//   - uptime reward: every RewardInterval, award uptime points to every node
//     that produced at least one heartbeat in the window. This finally wakes
//     the AwardUptimePoints code path described in ADR-0007's Context.
//
// All three tickers share a single bounded worker pool implemented as a
// semaphore channel. This caps total outbound RPC concurrency at one number
// (Config.RPCWorkers) instead of the sum of three independent pools.
//
// Lifecycle: New() builds the Scheduler but does not start any goroutines.
// Start() launches them. Close() cancels their context and waits for the
// workers to drain before returning. Calling Close() multiple times is safe.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/verification"
)

// Default cadences used when the corresponding Config fields are left zero.
const (
	defaultHeartbeatInterval      = 5 * time.Minute
	defaultChallengeCheckInterval = 1 * time.Minute
	defaultRewardInterval         = 5 * time.Minute
	defaultRPCWorkers             = 50
)

// Config controls the scheduler's behavior. Zero values are replaced with the
// defaults above.
type Config struct {
	// HeartbeatInterval — how often the heartbeat ticker fires.
	HeartbeatInterval time.Duration
	// ChallengeCheckInterval — how often the challenge dispatcher loops to
	// look for due challenges. The actual challenge cadence per node is
	// driven by NodeType.ChallengeFrequencyMinutes().
	ChallengeCheckInterval time.Duration
	// RewardInterval — how often the uptime-reward ticker fires.
	RewardInterval time.Duration
	// RPCWorkers — maximum concurrent outbound RPC operations across all
	// three tickers. Implemented as a semaphore.
	RPCWorkers int
	// Disabled — when true, Start() is a no-op. Used by tests and dev to
	// spin up the server without ticking.
	Disabled bool
}

// Scheduler is the heartbeat/challenge/uptime ticker bundle.
type Scheduler struct {
	store    store.Store
	verifier *verification.Verifier
	cfg      Config
	pool     chan struct{} // semaphore for RPC fanout (cap = RPCWorkers)
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	logger   *slog.Logger

	mu      sync.Mutex // guards started/closed
	started bool
	closed  bool
}

// New constructs a Scheduler. It does NOT start any goroutines; call Start.
// The returned Scheduler is safe to Close even if Start was never called.
func New(s store.Store, v *verification.Verifier, cfg Config) *Scheduler {
	cfg = applyDefaults(cfg)

	ctx, cancel := context.WithCancel(context.Background())

	return &Scheduler{
		store:    s,
		verifier: v,
		cfg:      cfg,
		pool:     make(chan struct{}, cfg.RPCWorkers),
		ctx:      ctx,
		cancel:   cancel,
		logger:   slog.Default(),
	}
}

// applyDefaults fills any zero field with the corresponding default.
func applyDefaults(cfg Config) Config {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}
	if cfg.ChallengeCheckInterval <= 0 {
		cfg.ChallengeCheckInterval = defaultChallengeCheckInterval
	}
	if cfg.RewardInterval <= 0 {
		cfg.RewardInterval = defaultRewardInterval
	}
	if cfg.RPCWorkers <= 0 {
		cfg.RPCWorkers = defaultRPCWorkers
	}
	return cfg
}

// Start launches the ticker goroutines. It is a no-op if cfg.Disabled is true
// or if Start has already been called. Calling Start after Close is also a
// no-op (the context is already cancelled).
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.cfg.Disabled || s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	s.logger.Info("scheduler starting",
		"heartbeat_interval", s.cfg.HeartbeatInterval,
		"challenge_check_interval", s.cfg.ChallengeCheckInterval,
		"reward_interval", s.cfg.RewardInterval,
		"rpc_workers", s.cfg.RPCWorkers,
	)

	s.wg.Add(3)
	go s.runHeartbeatLoop()
	go s.runChallengeLoop()
	go s.runUptimeRewardLoop()
}

// Close cancels the scheduler's context and waits for in-flight workers to
// drain. Safe to call multiple times. Returns nil.
func (s *Scheduler) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	s.wg.Wait()
	s.logger.Info("scheduler stopped")
	return nil
}

// acquire blocks until either a worker slot is available or the scheduler's
// context is cancelled. Returns true if a slot was acquired (and the caller
// MUST eventually call release), false if the context was cancelled first.
func (s *Scheduler) acquire() bool {
	select {
	case s.pool <- struct{}{}:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// release frees a worker slot acquired by acquire.
func (s *Scheduler) release() {
	select {
	case <-s.pool:
	default:
		// Defensive: this should never happen because every release is paired
		// with a successful acquire. If it does, log it loudly rather than
		// panicking — the scheduler should keep running.
		s.logger.Warn("scheduler.release called with empty pool")
	}
}

// tickerLoop drives a generic ticker: it fires `do` on every interval and
// returns when ctx is cancelled. The first tick fires after `interval`, not
// immediately — this matches Go's time.Ticker semantics and gives the server
// a clean startup phase before background work begins.
func (s *Scheduler) tickerLoop(name string, interval time.Duration, do func()) {
	defer s.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.logger.Debug("scheduler loop running", "loop", name, "interval", interval)
	for {
		select {
		case <-s.ctx.Done():
			s.logger.Debug("scheduler loop stopping", "loop", name)
			return
		case <-ticker.C:
			do()
		}
	}
}
