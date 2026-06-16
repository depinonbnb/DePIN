// Package memory provides an in-memory implementation of the store.Store
// interface. It is intended for tests and local development; production
// deployments use store/sqlite. All methods are safe for concurrent use.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/pointscap"
	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/google/uuid"
)

// Compile-time assertion that *MemoryStore satisfies store.Store.
var _ store.Store = (*MemoryStore)(nil)

// MemoryStore is the volatile, process-local implementation of store.Store.
// It loses all state on restart; use store/sqlite for persistence.
type MemoryStore struct {
	nodes               map[string]*types.NodeRegistration
	nodesByWallet       map[string][]string
	verificationHistory map[string][]*types.VerificationResult
	heartbeats          map[string][]*types.HeartbeatRecord
	// nonces is keyed by wallet + ":" + nonce. Value is the unix-ms time the
	// row expires; reads after that are treated as missing so the same nonce
	// can be reused legitimately once its window has passed.
	nonces map[string]int64
	// snapshots is keyed by cycle_id. Each entry holds the published metadata
	// plus the per-wallet proof map. The body of those snapshot methods lives
	// in memory_snapshots.go to keep this file under the project's 500-LOC cap.
	snapshots map[string]*snapshotEntry
	mu        sync.RWMutex
}

// NewMemory constructs an empty MemoryStore. It never returns nil.
func NewMemory() *MemoryStore {
	return &MemoryStore{
		nodes:               make(map[string]*types.NodeRegistration),
		nodesByWallet:       make(map[string][]string),
		verificationHistory: make(map[string][]*types.VerificationResult),
		heartbeats:          make(map[string][]*types.HeartbeatRecord),
		nonces:              make(map[string]int64),
		snapshots:           make(map[string]*snapshotEntry),
	}
}

// RegisterNode persists a brand-new node, awards the registration bonus,
// and indexes the node under its wallet.
func (s *MemoryStore) RegisterNode(walletAddress string, nodeType types.NodeType, method types.VerificationMethod, rpcEndpoint, authToken string) *types.NodeRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()

	node := &types.NodeRegistration{
		ID:                 uuid.New().String(),
		WalletAddress:      walletAddress,
		NodeType:           nodeType,
		VerificationMethod: method,
		RPCEndpoint:        rpcEndpoint,
		AuthToken:          authToken,
		RegisteredAt:       time.Now().UnixMilli(),
		IsActive:           true,
		TotalPoints:        nodeType.RegistrationBonus(),
		TotalUptimeMinutes: 0,
		CheatStatus:        types.StatusClean,
		WarningCount:       0,
		SuspiciousEvents:   []string{},
	}

	s.nodes[node.ID] = node
	s.nodesByWallet[walletAddress] = append(s.nodesByWallet[walletAddress], node.ID)

	return node
}

// GetNode returns the node with the given ID, or nil if not found.
func (s *MemoryStore) GetNode(nodeID string) *types.NodeRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodes[nodeID]
}

// GetNodesByWallet returns every node owned by the wallet, in registration order.
func (s *MemoryStore) GetNodesByWallet(walletAddress string) []*types.NodeRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodeIDs := s.nodesByWallet[walletAddress]
	nodes := make([]*types.NodeRegistration, 0, len(nodeIDs))

	for _, id := range nodeIDs {
		if node, ok := s.nodes[id]; ok {
			nodes = append(nodes, node)
		}
	}

	return nodes
}

// GetAllActiveNodes returns every node where IsActive = true.
func (s *MemoryStore) GetAllActiveNodes() []*types.NodeRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes := make([]*types.NodeRegistration, 0)
	for _, node := range s.nodes {
		if node.IsActive {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// UpdateNode applies the mutator under the store's write lock.
func (s *MemoryStore) UpdateNode(nodeID string, updates func(*types.NodeRegistration)) *types.NodeRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return nil
	}

	updates(node)
	return node
}

// RecordVerificationResult appends a result and applies escalation rules.
func (s *MemoryStore) RecordVerificationResult(result *types.VerificationResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[result.NodeID]
	if !ok {
		// Silent no-op for missing nodes; mirrors SQLiteStore which would
		// otherwise hit a foreign-key error. The API handlers always look
		// the node up before recording, so this only fires on stale calls.
		return
	}

	history := s.verificationHistory[result.NodeID]
	history = append(history, result)

	// Keep last 1000 results
	if len(history) > 1000 {
		history = history[1:]
	}

	s.verificationHistory[result.NodeID] = history

	// Update node stats
	{
		if result.Passed {
			node.TotalChallengesPassed++
			// Award challenge points so passed challenges actually pay out.
			// Routes through here for both the exposed-RPC scheduler path and
			// local-prover submissions. The token gate may withhold the points
			// (SkipPointsAward) for wallets below the minimum balance — the pass
			// still counts, only the payout is suppressed.
			if !result.SkipPointsAward {
				node.TotalPoints += pointscap.Allow(node.ID, node.NodeType.PointsPerChallenge())
			}
		} else {
			node.TotalChallengesFailed++
		}
		node.LastVerifiedAt = result.Timestamp

		// Track suspicious activity
		if result.Suspicious {
			event := result.SuspiciousNote
			if event == "" {
				event = "Suspicious verification detected"
			}
			node.SuspiciousEvents = append(node.SuspiciousEvents,
				time.Now().Format("2006-01-02 15:04")+": "+event)

			// Keep only last 20 events
			if len(node.SuspiciousEvents) > 20 {
				node.SuspiciousEvents = node.SuspiciousEvents[1:]
			}

			node.WarningCount++

			// Escalate based on warning count
			if node.WarningCount >= 5 {
				node.CheatStatus = types.StatusFlagged
				node.CheatReason = "Multiple suspicious activities - needs manual review"
			} else if node.WarningCount >= 2 {
				node.CheatStatus = types.StatusWarning
				node.CheatReason = event
			}
		} else if result.Passed && node.CheatStatus != types.StatusBanned {
			// Clean, fast pass: recover one step so occasional slow spikes don't
			// permanently flag an otherwise-healthy node. Banned stays banned.
			if node.WarningCount > 0 {
				node.WarningCount--
			}
			switch {
			case node.WarningCount >= 5:
				node.CheatStatus = types.StatusFlagged
			case node.WarningCount >= 2:
				node.CheatStatus = types.StatusWarning
			default:
				node.CheatStatus = types.StatusClean
				node.CheatReason = ""
			}
		}
	}
}

// GetVerificationHistory returns the most recent `limit` results, oldest-first.
func (s *MemoryStore) GetVerificationHistory(nodeID string, limit int) []*types.VerificationResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.verificationHistory[nodeID]
	if len(history) <= limit {
		return history
	}
	return history[len(history)-limit:]
}

// RecordHeartbeat appends a heartbeat record (capped at 300 retained per node).
func (s *MemoryStore) RecordHeartbeat(heartbeat *types.HeartbeatRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.heartbeats[heartbeat.NodeID]
	history = append(history, heartbeat)

	// Keep last 300 (about 24 hours at 5 min intervals)
	if len(history) > 300 {
		history = history[1:]
	}

	s.heartbeats[heartbeat.NodeID] = history

	// Reflect the latest sync state on the node so the API can surface it.
	if node, ok := s.nodes[heartbeat.NodeID]; ok {
		node.LastHeartbeatAt = heartbeat.Timestamp
		node.IsSynced = heartbeat.IsSynced
	}
}

// GetHeartbeats returns retained heartbeats since the given unix-ms timestamp.
func (s *MemoryStore) GetHeartbeats(nodeID string, since int64) []*types.HeartbeatRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.heartbeats[nodeID]
	if since == 0 {
		return history
	}

	filtered := make([]*types.HeartbeatRecord, 0)
	for _, h := range history {
		if h.Timestamp >= since {
			filtered = append(filtered, h)
		}
	}
	return filtered
}

// GetNodeStats computes derived stats for the given node.
func (s *MemoryStore) GetNodeStats(nodeID string) *types.NodeStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return nil
	}

	verifications := s.verificationHistory[nodeID]

	// Challenge pass rate over the last 24 hours
	last24h := time.Now().UnixMilli() - 24*60*60*1000
	recentVerifications := 0
	recentPassed := 0
	var totalLatency uint64
	for _, v := range verifications {
		if v.Timestamp >= last24h {
			recentVerifications++
			if v.Passed {
				recentPassed++
			}
			totalLatency += v.ResponseTimeMs
		}
	}
	passRate := float64(0)
	avgLatency := float64(0)
	if recentVerifications > 0 {
		passRate = float64(recentPassed) / float64(recentVerifications) * 100
		avgLatency = float64(totalLatency) / float64(recentVerifications)
	}

	return &types.NodeStats{
		NodeID:             node.ID,
		TotalPoints:        node.TotalPoints,
		TotalUptimeMinutes: node.TotalUptimeMinutes,
		TotalUptimeHours:   float64(node.TotalUptimeMinutes) / 60.0,
		ChallengePassRate:  passRate,
		AverageLatencyMs:   avgLatency,
		CheatStatus:        node.CheatStatus,
		WarningCount:       node.WarningCount,
	}
}

// GetWalletStats aggregates totals across every node owned by a wallet.
func (s *MemoryStore) GetWalletStats(walletAddress string) *types.WalletStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodeIDs := s.nodesByWallet[walletAddress]
	if len(nodeIDs) == 0 {
		return nil
	}

	var totalPoints uint64
	activeNodes := 0
	flaggedNodes := 0

	for _, nodeID := range nodeIDs {
		if node, ok := s.nodes[nodeID]; ok {
			totalPoints += node.TotalPoints
			if node.IsActive {
				activeNodes++
			}
			if node.CheatStatus == types.StatusFlagged || node.CheatStatus == types.StatusWarning {
				flaggedNodes++
			}
		}
	}

	return &types.WalletStats{
		WalletAddress: walletAddress,
		TotalPoints:   totalPoints,
		TotalNodes:    len(nodeIDs),
		ActiveNodes:   activeNodes,
		FlaggedNodes:  flaggedNodes,
	}
}

// AwardUptimePoints adds points proportional to NodeType.PointsPerHour().
// No-op for inactive, flagged, or banned nodes.
func (s *MemoryStore) AwardUptimePoints(nodeID string, minutesOnline uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[nodeID]
	if !ok || !node.IsActive {
		return
	}

	// Don't award points to flagged/banned nodes
	if node.CheatStatus == types.StatusFlagged || node.CheatStatus == types.StatusBanned {
		return
	}

	node.TotalUptimeMinutes += minutesOnline
	node.LastHeartbeatAt = time.Now().UnixMilli()

	// Award points based on uptime (per hour rate, divided by 12 for 5-min intervals)
	pointsPerInterval := node.NodeType.PointsPerHour() / 12
	if pointsPerInterval < 1 {
		pointsPerInterval = 1
	}
	node.TotalPoints += pointscap.Allow(node.ID, pointsPerInterval)
}

// AddSuspiciousEvent appends a structured event and applies escalation rules.
func (s *MemoryStore) AddSuspiciousEvent(nodeID string, reason string) {
	// Counter is incremented unconditionally (even if the node lookup fails
	// below). The "missing_node" reason bucket exists precisely so missing
	// targets are observable rather than silently dropped.
	metrics.SuspiciousEventsTotal.WithLabelValues(metrics.BucketSuspiciousReason(reason)).Inc()

	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return
	}

	// Add to suspicious events list
	event := time.Now().Format("2006-01-02 15:04") + ": " + reason
	node.SuspiciousEvents = append(node.SuspiciousEvents, event)

	// Keep only last 20 events
	if len(node.SuspiciousEvents) > 20 {
		node.SuspiciousEvents = node.SuspiciousEvents[1:]
	}

	node.WarningCount++

	// Escalate status based on warning count
	if node.WarningCount >= 5 {
		node.CheatStatus = types.StatusFlagged
		node.CheatReason = "Multiple suspicious activities detected - needs manual review"
	} else if node.WarningCount >= 2 {
		node.CheatStatus = types.StatusWarning
		node.CheatReason = reason
	}
}

// GetFlaggedNodes returns every node whose CheatStatus is Warning or Flagged.
func (s *MemoryStore) GetFlaggedNodes() []*types.NodeRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()

	flagged := make([]*types.NodeRegistration, 0)
	for _, node := range s.nodes {
		if node.CheatStatus == types.StatusFlagged || node.CheatStatus == types.StatusWarning {
			flagged = append(flagged, node)
		}
	}
	return flagged
}

// SetNodeCheatStatus is an admin action. Returns false if the node was not found.
func (s *MemoryStore) SetNodeCheatStatus(nodeID string, status types.CheatStatus, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return false
	}

	node.CheatStatus = status
	node.CheatReason = reason

	// If cleared, reset warning count
	if status == types.StatusClean {
		node.WarningCount = 0
		node.SuspiciousEvents = []string{}
	}

	// If banned, deactivate
	if status == types.StatusBanned {
		node.IsActive = false
	}

	return true
}

// ConsumeNonce records (wallet, nonce) with a TTL. Returns true if the pair
// was recorded fresh, false if the same pair is still present and unexpired.
// Expired rows are evicted on the way through so memory doesn't grow without
// bound; this keeps us honest without a separate goroutine.
func (s *MemoryStore) ConsumeNonce(wallet string, nonce string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	key := wallet + ":" + nonce

	if exp, exists := s.nonces[key]; exists && exp > now {
		// Live replay.
		return false
	}

	// Opportunistic prune so the map doesn't grow unboundedly across long
	// runs. Cap the per-call work so a request with a degenerate map doesn't
	// stall on this; the SQLite store has its own pruner for the persistent
	// case.
	const maxScan = 64
	scanned := 0
	for k, exp := range s.nonces {
		if exp <= now {
			delete(s.nonces, k)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}

	s.nonces[key] = now + ttl.Milliseconds()
	return true
}

// Ping is the liveness probe used by /ready. For MemoryStore this is always
// nil — the underlying maps don't have a "down" state. Honors ctx.Err so a
// pre-cancelled context still surfaces correctly.
func (s *MemoryStore) Ping(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Close releases backing resources. For MemoryStore this is a no-op.
func (s *MemoryStore) Close() error {
	return nil
}
