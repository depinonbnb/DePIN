// Package store defines the persistence contract used by the API and
// verification layers. Implementations live in sub-packages: store/memory
// (test/dev) and store/sqlite (prod).
package store

import (
	"context"
	"math/big"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

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

	// SeedNode sets a node's total points, passed-verification count, and synced
	// flag directly. Admin/test seam for demo nodes; not part of normal
	// operation. Returns false if the node was not found.
	SeedNode(nodeID string, totalPoints, verifications uint64, synced bool) bool

	// ConsumeNonce records (wallet, nonce) with a TTL and returns true if the
	// pair is fresh (just recorded). It returns false if the same (wallet,
	// nonce) is still present and unexpired — that is the replay-detection
	// signal. Expired rows are ignored / overwritten so a long-lived nonce
	// can rotate naturally as it falls outside the TTL window.
	ConsumeNonce(wallet string, nonce string, ttl time.Duration) bool

	// SaveSnapshot persists a published reward cycle (ADR-0008). The call is
	// idempotent on snapshot.CycleID — re-publishing the same cycle replaces
	// the prior root/proofs atomically so re-runs of the snapshot job converge
	// to the same canonical row. Amounts in proofs are hex-encoded big.Int
	// strings to round-trip through SQLite's TEXT columns without truncation.
	// Wallets are normalised to lower-case before persistence.
	SaveSnapshot(snapshot *types.Snapshot, proofs map[string]SnapshotProof) error

	// GetLatestSnapshot returns the most recently published cycle, or nil if
	// no snapshots have been published. Does NOT load per-wallet proofs.
	GetLatestSnapshot() (*types.Snapshot, error)

	// GetSnapshotByCycle returns the snapshot for a specific cycle id, or nil
	// if not found. Does NOT load per-wallet proofs.
	GetSnapshotByCycle(cycleID string) (*types.Snapshot, error)

	// GetWalletProof fetches the (snapshot, proof, amount) tuple for a wallet
	// in a given cycle. Returns (nil, nil, nil, nil) if the cycle exists but
	// the wallet has no proof in that cycle, or if the cycle does not exist.
	// Returns a non-nil error only on backend failure.
	GetWalletProof(cycleID string, wallet string) (snapshot *types.Snapshot, proof [][]byte, amount *big.Int, err error)

	// Ping is a lightweight liveness probe used by /ready. The memory
	// implementation always returns nil; the SQLite implementation calls
	// db.PingContext so a deep readiness check fails fast when the underlying
	// connection pool can't reach the database file. Implementations should
	// honor the context's deadline.
	Ping(ctx context.Context) error

	// Close releases any backing resources. The memory implementation is a
	// no-op; the SQLite implementation closes the DB handle and stops the
	// prune goroutine. Safe to call multiple times.
	Close() error
}

// SnapshotProof is the persisted view of one wallet's claim row inside a
// cycle. Amount is the raw big.Int (hex-encoded on the wire / in storage);
// Proof is the sibling-hash path produced by reward.BuildTree.
type SnapshotProof struct {
	Wallet string
	Amount *big.Int
	Proof  [][]byte
}
