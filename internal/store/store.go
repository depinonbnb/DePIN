package store

import (
	"sync"
	"time"

	"github.com/depinonbnb/depin/internal/types"
	"github.com/google/uuid"
)

// In-memory store for nodes and verification data
// Replace with a real database in production
type Store struct {
	nodes               map[string]*types.NodeRegistration
	nodesByWallet       map[string][]string
	verificationHistory map[string][]*types.VerificationResult
	heartbeats          map[string][]*types.HeartbeatRecord
	mu                  sync.RWMutex
}

func NewStore() *Store {
	return &Store{
		nodes:               make(map[string]*types.NodeRegistration),
		nodesByWallet:       make(map[string][]string),
		verificationHistory: make(map[string][]*types.VerificationResult),
		heartbeats:          make(map[string][]*types.HeartbeatRecord),
	}
}

// Register a new node - gives registration bonus points
func (s *Store) RegisterNode(walletAddress string, nodeType types.NodeType, method types.VerificationMethod, rpcEndpoint, authToken string) *types.NodeRegistration {
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
		TotalPoints:        nodeType.RegistrationBonus(), // Bonus for registering!
		TotalUptimeMinutes: 0,
		CheatStatus:        types.StatusClean,
		WarningCount:       0,
		SuspiciousEvents:   []string{},
	}

	s.nodes[node.ID] = node

	// Track by wallet
	s.nodesByWallet[walletAddress] = append(s.nodesByWallet[walletAddress], node.ID)

	return node
}

func (s *Store) GetNode(nodeID string) *types.NodeRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodes[nodeID]
}

func (s *Store) GetNodesByWallet(walletAddress string) []*types.NodeRegistration {
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

func (s *Store) GetAllActiveNodes() []*types.NodeRegistration {
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

func (s *Store) UpdateNode(nodeID string, updates func(*types.NodeRegistration)) *types.NodeRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return nil
	}

	updates(node)
	return node
}

// Record verification result and award points
func (s *Store) RecordVerificationResult(result *types.VerificationResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.verificationHistory[result.NodeID]
	history = append(history, result)

	// Keep last 1000 results
	if len(history) > 1000 {
		history = history[1:]
	}

	s.verificationHistory[result.NodeID] = history

	// Update node stats
	if node, ok := s.nodes[result.NodeID]; ok {
		if result.Passed {
			node.TotalChallengesPassed++
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
		}
	}
}

func (s *Store) GetVerificationHistory(nodeID string, limit int) []*types.VerificationResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.verificationHistory[nodeID]
	if len(history) <= limit {
		return history
	}
	return history[len(history)-limit:]
}

// Record heartbeat
func (s *Store) RecordHeartbeat(heartbeat *types.HeartbeatRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.heartbeats[heartbeat.NodeID]
	history = append(history, heartbeat)

	// Keep last 300 (about 24 hours at 5 min intervals)
	if len(history) > 300 {
		history = history[1:]
	}

	s.heartbeats[heartbeat.NodeID] = history
}

func (s *Store) GetHeartbeats(nodeID string, since int64) []*types.HeartbeatRecord {
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

// Get node stats
func (s *Store) GetNodeStats(nodeID string) *types.NodeStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[nodeID]
	if !ok {
		return nil
	}

	verifications := s.verificationHistory[nodeID]

	// Challenge pass rate
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

// Get total points for a wallet (across all their nodes)
func (s *Store) GetWalletStats(walletAddress string) *types.WalletStats {
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

// Award points for uptime - call this periodically (every 5 minutes)
// Also tracks uptime minutes
func (s *Store) AwardUptimePoints(nodeID string, minutesOnline uint64) {
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
	// So if PointsPerHour is 6, they get 0.5 points per 5 minutes
	pointsPerInterval := node.NodeType.PointsPerHour() / 12
	if pointsPerInterval < 1 {
		pointsPerInterval = 1
	}
	node.TotalPoints += pointsPerInterval
}

// Add a suspicious event to a node
func (s *Store) AddSuspiciousEvent(nodeID string, reason string) {
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

// Get all nodes that need admin review
func (s *Store) GetFlaggedNodes() []*types.NodeRegistration {
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

// Admin action: clear warnings or ban a node
func (s *Store) SetNodeCheatStatus(nodeID string, status types.CheatStatus, reason string) bool {
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
