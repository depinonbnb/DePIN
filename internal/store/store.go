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

func (s *Store) getRewardTier(nodeType types.NodeType) uint8 {
	switch nodeType {
	case types.BscArchive:
		return 4
	case types.BscFull:
		return 3
	case types.BscFast, types.OpbnbFull:
		return 2
	default:
		return 1
	}
}

// Register a new node
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
		RewardTier:         s.getRewardTier(nodeType),
		IsActive:           true,
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

// Record verification result
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
	heartbeats := s.heartbeats[nodeID]

	// Calculate uptime from last 24 hours
	last24h := time.Now().UnixMilli() - 24*60*60*1000
	recentHeartbeats := 0
	for _, h := range heartbeats {
		if h.Timestamp >= last24h {
			recentHeartbeats++
		}
	}
	expectedHeartbeats := float64(24 * 60 / 5) // Every 5 minutes
	uptimePercent := float64(recentHeartbeats) / expectedHeartbeats * 100
	if uptimePercent > 100 {
		uptimePercent = 100
	}

	// Challenge pass rate
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

	// Calculate streak
	streak := uint64(0)
	for i := len(verifications) - 1; i >= 0; i-- {
		if verifications[i].Passed {
			streak++
		} else {
			break
		}
	}

	return &types.NodeStats{
		NodeID:             node.ID,
		UptimePercent:      uptimePercent,
		ChallengePassRate:  passRate,
		AverageLatencyMs:   avgLatency,
		TotalRewardsEarned: "0",
		CurrentStreak:      streak,
	}
}
