package scheduler

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/types"
)

// runChallengeLoop is the goroutine entry point for the challenge dispatcher.
func (s *Scheduler) runChallengeLoop() {
	s.tickerLoop("challenge", s.cfg.ChallengeCheckInterval, s.runChallengeOnce)
}

// runChallengeOnce inspects every active node and dispatches a challenge
// against any exposed-RPC node whose last verification is older than its
// tier's NodeType.ChallengeFrequencyMinutes() interval.
//
// Local-prover nodes are intentionally NOT dispatched server-side — those
// nodes pull challenges via the existing GET /challenges/request endpoint.
// The scheduler trusts that flow to keep its rhythm.
func (s *Scheduler) runChallengeOnce() {
	nodes := s.store.GetAllActiveNodes()
	// Publish bucketed node-population gauges once per cycle. The challenge
	// loop runs at the highest cadence (typically every minute), so this is
	// the cheapest place to keep the gauges fresh without dedicating another
	// goroutine to it.
	s.publishNodeBuckets(nodes)

	now := time.Now().UnixMilli()

	var (
		dispatched atomic.Uint64
		passed     atomic.Uint64
		failed     atomic.Uint64
		wg         sync.WaitGroup
	)

	for _, node := range nodes {
		if node.CheatStatus == types.StatusBanned {
			continue
		}
		if node.VerificationMethod != types.ExposedRPC {
			// Local-prover nodes pull their own challenges; we don't dispatch
			// for them.
			continue
		}

		freqMinutes := node.NodeType.ChallengeFrequencyMinutes()
		if freqMinutes == 0 {
			// Unknown node type — skip rather than dispatch every tick.
			continue
		}

		dueAfter := node.LastVerifiedAt + int64(freqMinutes)*60*1000
		// LastVerifiedAt == 0 means "never verified" — that node is always due.
		if node.LastVerifiedAt != 0 && now < dueAfter {
			continue
		}

		if !s.acquire() {
			// Scheduler shutting down — stop dispatching new work, but let
			// already-spawned goroutines drain via wg.Wait below.
			break
		}
		wg.Add(1)
		go func(n *types.NodeRegistration) {
			defer s.release()
			defer wg.Done()

			select {
			case <-s.ctx.Done():
				return
			default:
			}

			result := s.verifier.VerifyExposedRPC(n)
			s.store.RecordVerificationResult(result)
			dispatched.Add(1)

			// Histogram: every challenge contributes its observed latency.
			metrics.VerificationLatencyMs.Observe(float64(result.ResponseTimeMs))

			// Counter: bucket by outcome × method × node type. "abort" =
			// quorum disagreement (the verifier surfaces this with a
			// recognisable failure reason — we treat it differently from
			// outright failure so dashboards can separate the two).
			outcome := "passed"
			if !result.Passed {
				if isQuorumAbort(result.FailureReason) {
					outcome = "abort"
				} else {
					outcome = "failed"
				}
			}
			metrics.ChallengesTotal.WithLabelValues(outcome, "exposed-rpc", string(n.NodeType)).Inc()

			if result.Passed {
				passed.Add(1)
			} else {
				failed.Add(1)
				s.logger.Warn("challenge: node failed verification",
					"node_id", n.ID,
					"reason", result.FailureReason,
				)
			}
		}(node)
	}

	wg.Wait()

	if dispatched.Load() == 0 {
		// Nothing dispatched this cycle — keep silent rather than spam.
		return
	}
	s.logger.Info("challenge cycle done",
		"dispatched", dispatched.Load(),
		"passed", passed.Load(),
		"failed", failed.Load(),
	)
}

// isQuorumAbort returns true when the verifier's failure reason looks like a
// quorum-disagreement abort (rpc.QuorumNoMajority or the verifier-side wrapped
// version of it). We deliberately match on substrings instead of importing
// internal/rpc here to avoid a coupling between scheduler and rpc just for
// one constant; the verifier already strings the constant into its reason.
func isQuorumAbort(reason string) bool {
	if reason == "" {
		return false
	}
	for _, needle := range []string{"no quorum", "quorum disagreement", "treated as abort"} {
		if containsCI(reason, needle) {
			return true
		}
	}
	return false
}

// containsCI is a small case-insensitive substring helper used by
// isQuorumAbort.
func containsCI(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	// Naive, ASCII-only — the verifier produces ASCII-only failure reasons so
	// pulling in unicode/utf8 normalisation would be overkill.
	for i := 0; i+len(substr) <= len(s); i++ {
		ok := true
		for j := 0; j < len(substr); j++ {
			a := s[i+j]
			b := substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
