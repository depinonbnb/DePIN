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

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/depinonbnb/depin/internal/verification"
)

// Default cadences used when the corresponding Config fields are left zero.
const (
	defaultHeartbeatInterval      = 5 * time.Minute
	defaultChallengeCheckInterval = 1 * time.Minute
	defaultRewardInterval         = 5 * time.Minute
	defaultSnapshotInterval       = 7 * 24 * time.Hour // weekly per ADR-0008
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
	// SnapshotInterval — how often the snapshot cron ticker fires. Default
	// is weekly (7*24h, per ADR-0008). Set to a negative value to disable
	// the snapshot loop entirely without disabling the rest of the scheduler.
	// Zero is treated as "use default" (weekly).
	SnapshotInterval time.Duration
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
//
// SnapshotInterval has a tri-state contract:
//   - 0  => use defaultSnapshotInterval (weekly).
//   - <0 => caller has explicitly disabled the snapshot loop.
//   - >0 => caller-supplied cadence.
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
	if cfg.SnapshotInterval == 0 {
		cfg.SnapshotInterval = defaultSnapshotInterval
	}
	if cfg.RPCWorkers <= 0 {
		cfg.RPCWorkers = defaultRPCWorkers
	}
	return cfg
}

// Start launches the ticker goroutines. It is a no-op if cfg.Disabled is true
// or if Start has already been called. Calling Start after Close is also a
// no-op (the context is already cancelled).
//
// The snapshot loop is started only when cfg.SnapshotInterval > 0; a negative
// value explicitly disables it (handy for tests and operators who only want
// manual `POST /api/admin/snapshot/publish`).
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
		"snapshot_interval", s.cfg.SnapshotInterval,
		"rpc_workers", s.cfg.RPCWorkers,
	)

	// Publish the worker pool capacity gauge once at startup. The inflight
	// gauge is updated inside acquire/release.
	metrics.RPCWorkerCapacity.Set(float64(s.cfg.RPCWorkers))
	metrics.RPCWorkerInflight.Set(0)

	s.wg.Add(3)
	go s.runHeartbeatLoop()
	go s.runChallengeLoop()
	go s.runUptimeRewardLoop()

	if s.cfg.SnapshotInterval > 0 {
		s.wg.Add(1)
		go s.runSnapshotLoop()
	}
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
//
// On a successful acquire we publish the new pool occupancy via the inflight
// gauge; the gauge is paired with the release call's decrement.
func (s *Scheduler) acquire() bool {
	select {
	case s.pool <- struct{}{}:
		metrics.RPCWorkerInflight.Set(float64(len(s.pool)))
		return true
	case <-s.ctx.Done():
		return false
	}
}

// release frees a worker slot acquired by acquire.
func (s *Scheduler) release() {
	select {
	case <-s.pool:
		metrics.RPCWorkerInflight.Set(float64(len(s.pool)))
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
//
// Every successful tick stamps depin_scheduler_last_tick_seconds so /ready
// can answer "when was the last tick?". The gauge is set AFTER `do` returns
// so the timestamp reflects the completion of the tick, not its kickoff.
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
			metrics.SchedulerLastTick.WithLabelValues(name).SetToCurrentTime()
		}
	}
}

// publishNodeBuckets snapshots the current active-node population into the
// gauge family so /metrics and /ready can both see the same picture. Called
// at the top of every challenge cycle (the highest-frequency loop) so the
// labels stay near-fresh without an extra goroutine.
func (s *Scheduler) publishNodeBuckets(nodes []*types.NodeRegistration) {
	metrics.NodesActive.Set(float64(len(nodes)))

	byStatus := map[string]int{
		"clean":   0,
		"warning": 0,
		"flagged": 0,
		"banned":  0,
	}
	byType := make(map[string]int)
	for _, n := range nodes {
		switch n.CheatStatus {
		case types.StatusClean:
			byStatus["clean"]++
		case types.StatusWarning:
			byStatus["warning"]++
		case types.StatusFlagged:
			byStatus["flagged"]++
		case types.StatusBanned:
			byStatus["banned"]++
		}
		byType[string(n.NodeType)]++
	}
	for k, v := range byStatus {
		metrics.NodesByStatus.WithLabelValues(k).Set(float64(v))
	}
	for k, v := range byType {
		metrics.NodesByType.WithLabelValues(k).Set(float64(v))
	}
}
