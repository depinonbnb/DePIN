// Package scheduler: snapshot.go is the Phase 5 cron loop that builds and
// publishes Merkle reward snapshots on a fixed cadence.
//
// Per ADR-0008 §Decision the cycle length is weekly. The default cfg gives
// SnapshotInterval=168h; operators can override with the SNAPSHOT_INTERVAL
// env var (parsed in cmd/server/main.go via time.ParseDuration). A negative
// value disables this loop entirely without disabling the rest of the
// scheduler — useful for tests that don't want to exercise BuildCycle.
//
// Cycle ID format: "cycle-" + RFC3339 UTC timestamp at the moment the cron
// fires. This makes the cycle id sortable, human-readable, and trivially
// derivable from the published_at metadata.
//
// Failure mode: BuildCycle returning ErrNoEarners is logged at info level and
// the loop continues — that is the documented zero-earner branch in ADR-0008
// §Decision. Any other error increments the snapshot_build_failures_total
// counter and is logged at error level; the loop continues to the next tick
// rather than crashing the process.
package scheduler

import (
	"errors"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/reward"
)

// runSnapshotLoop is the goroutine entry point for the snapshot cron.
// Mirrors the shape of the other ticker goroutines so Close()'s wg.Wait
// behaviour is identical.
func (s *Scheduler) runSnapshotLoop() {
	s.tickerLoop("snapshot", s.cfg.SnapshotInterval, s.runSnapshotOnce)
}

// runSnapshotOnce executes one snapshot cycle: BuildCycle → Persist. It is
// idempotent on cycle id, so re-running on the same minute is safe (the
// store's SaveSnapshot replaces the prior row). Tests rely on this when they
// drive the loop at a sub-second cadence.
func (s *Scheduler) runSnapshotOnce() {
	cycleID := buildCycleID(time.Now())

	start := time.Now()
	cycle, err := reward.BuildCycle(s.store, cycleID)
	buildSeconds := time.Since(start).Seconds()
	metrics.SnapshotBuildSeconds.Observe(buildSeconds)

	if err != nil {
		if errors.Is(err, reward.ErrNoEarners) {
			s.logger.Info("snapshot cycle skipped: no earners",
				"cycle_id", cycleID,
				"build_seconds", buildSeconds,
			)
			return
		}
		metrics.SnapshotBuildFailures.Inc()
		s.logger.Error("snapshot cycle: build failed",
			"cycle_id", cycleID,
			"error", err,
		)
		return
	}

	publishedAt := time.Now().UnixMilli()
	if err := cycle.Persist(s.store, publishedAt, ""); err != nil {
		metrics.SnapshotBuildFailures.Inc()
		s.logger.Error("snapshot cycle: persist failed",
			"cycle_id", cycleID,
			"error", err,
		)
		return
	}

	metrics.SnapshotsPublished.Inc()
	metrics.LastSnapshotPublished.SetToCurrentTime()

	// total_amount is a *big.Int; render hex for log readability — the same
	// shape we already use in the API response.
	totalHex := "0x" + cycle.TotalAmount.Text(16)
	rootHex := "0x" + hexEncodeBytes(cycle.Root)
	s.logger.Info("snapshot cycle published",
		"cycle_id", cycleID,
		"root", rootHex,
		"node_count", cycle.NodeCount,
		"total_amount", totalHex,
		"published_at", publishedAt,
		"build_seconds", buildSeconds,
	)
}

// buildCycleID renders the cycle id format documented in this file's package
// comment: "cycle-" + RFC3339 UTC. Extracted so tests can pin the format.
func buildCycleID(now time.Time) string {
	return "cycle-" + now.UTC().Format("2006-01-02T15:04:05Z")
}

// hexEncodeBytes lowercases bytes into hex without dragging encoding/hex into
// this file. Mirrors snapshot_handlers.go's helper of the same name; both are
// trivial and keeping them local means the shared package's import surface
// stays minimal.
func hexEncodeBytes(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}
