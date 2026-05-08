// Package store defines the persistence contract used by the API and
// verification layers. Implementations live in sub-packages: store/memory
// (test/dev) and store/sqlite (prod).
package store

import "github.com/depinonbnb/depin/internal/types"

// Store is the persistence contract. Every reader and writer of node state
// goes through it. Implementations must be safe for concurrent use.
type Store interface {
	// RegisterNode persists a brand-new node, awards the registration bonus,
	// and returns the populated NodeRegistration. Generates a fresh UUID.
	// Indexes the node under its wallet.
	RegisterNode(walletAddress string, nodeType types.NodeType, method types.VerificationMethod, rpcEndpoint, authToken string) *types.NodeRegistration

	// GetNode returns the node with the given ID, or nil if not found.
	GetNode(nodeID string) *types.NodeRegistration

	// GetNodesByWallet returns every node owned by the wallet, in registration
	// order. Empty slice if none.
	GetNodesByWallet(walletAddress string) []*types.NodeRegistration

	// GetAllActiveNodes returns every node where IsActive = true. Used for
	// leaderboard and network stats.
	GetAllActiveNodes() []*types.NodeRegistration

	// UpdateNode applies the mutator to the node and persists the result
	// atomically. Returns the updated node, or nil if the node does not exist.
	UpdateNode(nodeID string, updates func(*types.NodeRegistration)) *types.NodeRegistration

	// RecordVerificationResult appends a result to the node's history, updates
	// pass/fail counters, sets LastVerifiedAt, and applies suspicious-event
	// escalation (warning -> flagged) per existing rules.
	RecordVerificationResult(result *types.VerificationResult)

	// GetVerificationHistory returns the most recent `limit` results for a
	// node, oldest-first.
	GetVerificationHistory(nodeID string, limit int) []*types.VerificationResult

	// RecordHeartbeat appends a heartbeat record. Caller is responsible for
	// setting Timestamp.
	RecordHeartbeat(heartbeat *types.HeartbeatRecord)

	// GetHeartbeats returns heartbeats for a node since the given
	// unix-millisecond timestamp. since == 0 means "all retained heartbeats".
	GetHeartbeats(nodeID string, since int64) []*types.HeartbeatRecord

	// GetNodeStats computes derived stats (pass rate, avg latency over last
	// 24h, uptime hours). Returns nil if the node does not exist.
	GetNodeStats(nodeID string) *types.NodeStats

	// GetWalletStats aggregates totals across every node owned by a wallet.
	// Returns nil if the wallet owns no nodes.
	GetWalletStats(walletAddress string) *types.WalletStats

	// AwardUptimePoints adds points proportional to NodeType.PointsPerHour()
	// and increments TotalUptimeMinutes. No-op for inactive, flagged, or
	// banned nodes.
	AwardUptimePoints(nodeID string, minutesOnline uint64)

	// AddSuspiciousEvent appends a structured event and applies the same
	// warning-escalation rules used by RecordVerificationResult.
	AddSuspiciousEvent(nodeID string, reason string)

	// GetFlaggedNodes returns every node whose CheatStatus is Warning or
	// Flagged.
	GetFlaggedNodes() []*types.NodeRegistration

	// SetNodeCheatStatus is an admin action. Returns false if the node was not
	// found. Side effects: cleared status resets WarningCount and
	// SuspiciousEvents; banned status sets IsActive=false.
	SetNodeCheatStatus(nodeID string, status types.CheatStatus, reason string) bool

	// Close releases any backing resources. The memory implementation is a
	// no-op; the SQLite implementation closes the DB handle and stops the
	// prune goroutine. Safe to call multiple times.
	Close() error
}
