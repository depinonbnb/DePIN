package scheduler

import (
	"sync"
	"sync/atomic"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/types"
)

// runHeartbeatLoop is the goroutine entry point for the heartbeat ticker.
func (s *Scheduler) runHeartbeatLoop() {
	s.tickerLoop("heartbeat", s.cfg.HeartbeatInterval, s.runHeartbeatOnce)
}

// runHeartbeatOnce executes one heartbeat sweep over every active node that
// uses the exposed-RPC verification method. Banned nodes are skipped. Each
// per-node ping runs through the shared worker pool so we do not exceed
// Config.RPCWorkers concurrent outbound RPC calls.
func (s *Scheduler) runHeartbeatOnce() {
	nodes := s.store.GetAllActiveNodes()

	var (
		total   int
		online  atomic.Uint64
		offline atomic.Uint64
		wg      sync.WaitGroup
	)

	for _, node := range nodes {
		if node.VerificationMethod != types.ExposedRPC {
			continue
		}
		if node.CheatStatus == types.StatusBanned {
			continue
		}
		total++

		// Bound outbound concurrency through the shared semaphore. If the
		// scheduler is being shut down, stop dispatching new work — but do
		// NOT return before wg.Wait, otherwise already-running goroutines
		// would be leaked.
		if !s.acquire() {
			break
		}

		wg.Add(1)
		go func(n *types.NodeRegistration) {
			defer s.release()
			defer wg.Done()

			// Re-check ctx before doing work; the node list snapshot may be
			// stale if shutdown happened between acquire and goroutine start.
			select {
			case <-s.ctx.Done():
				return
			default:
			}

			heartbeat := s.verifier.CheckHeartbeat(n)
			if heartbeat == nil {
				offline.Add(1)
				s.logger.Warn("heartbeat: node unreachable",
					"node_id", n.ID,
					"rpc_endpoint", n.RPCEndpoint,
				)
				return
			}
			// Heartbeat latency is part of the same anti-cheat threshold
			// distribution, so it shares the verification_latency histogram.
			metrics.VerificationLatencyMs.Observe(float64(heartbeat.LatencyMs))
			s.store.RecordHeartbeat(heartbeat)
			online.Add(1)
		}(node)
	}

	wg.Wait()

	if total == 0 {
		// Nothing to log at info level when there are no exposed-rpc nodes.
		return
	}
	s.logger.Info("heartbeat cycle done",
		"total", total,
		"online", online.Load(),
		"offline", offline.Load(),
	)
}
