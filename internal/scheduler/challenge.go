package scheduler

import (
	"sync"
	"sync/atomic"
	"time"

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
