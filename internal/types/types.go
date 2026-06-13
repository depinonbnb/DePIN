package types

// What kind of node is the user running
type NodeType string

const (
	BscFull    NodeType = "bsc-full"
	BscFast    NodeType = "bsc-fast"
	BscArchive NodeType = "bsc-archive"
	OpbnbFull  NodeType = "opbnb-full"
	OpbnbFast  NodeType = "opbnb-fast"
)

// Points users get just for registering a synced node
func (n NodeType) RegistrationBonus() uint64 {
	switch n {
	case BscArchive:
		return 100 // Archive nodes are hardest to run
	case BscFull:
		return 50
	case BscFast:
		return 40
	case OpbnbFull:
		return 40
	case OpbnbFast:
		return 30
	default:
		return 0
	}
}

// Base points per hour of uptime (multiplied by bandwidth tier)
func (n NodeType) PointsPerHour() uint64 {
	switch n {
	case BscArchive:
		return 10
	case BscFull:
		return 6
	case BscFast:
		return 4
	case OpbnbFull:
		return 4
	case OpbnbFast:
		return 3
	default:
		return 0
	}
}

// Points awarded for each passed verification challenge. Tiered like uptime so
// harder-to-run nodes earn more per challenge.
func (n NodeType) PointsPerChallenge() uint64 {
	switch n {
	case BscArchive:
		return 5
	case BscFull:
		return 3
	case BscFast:
		return 2
	case OpbnbFull:
		return 2
	case OpbnbFast:
		return 1
	default:
		return 0
	}
}

func (n NodeType) MinUptimePercent() uint8 {
	switch n {
	case BscArchive, BscFull:
		return 95
	case BscFast, OpbnbFull:
		return 90
	case OpbnbFast:
		return 85
	default:
		return 90
	}
}

func (n NodeType) ChallengeFrequencyMinutes() uint64 {
	switch n {
	case BscArchive, BscFull:
		return 30
	default:
		return 60
	}
}

// How we verify the node
type VerificationMethod string

const (
	ExposedRPC  VerificationMethod = "exposed-rpc"
	LocalProver VerificationMethod = "local-prover"
)

// Types of challenges we send
type ChallengeType string

const (
	BlockHash    ChallengeType = "block-hash"
	BlockData    ChallengeType = "block-data"
	StateBalance ChallengeType = "state-balance"
	TxReceipt    ChallengeType = "tx-receipt"
	SyncStatus   ChallengeType = "sync-status"
)

// Anti-cheat status
type CheatStatus string

const (
	StatusClean   CheatStatus = "clean"   // No issues
	StatusWarning CheatStatus = "warning" // Suspicious activity detected
	StatusFlagged CheatStatus = "flagged" // Needs manual review by admin
	StatusBanned  CheatStatus = "banned"  // Confirmed cheating
)

// A registered node
type NodeRegistration struct {
	ID                    string             `json:"id"`
	WalletAddress         string             `json:"wallet_address"`
	NodeType              NodeType           `json:"node_type"`
	VerificationMethod    VerificationMethod `json:"verification_method"`
	RPCEndpoint           string             `json:"rpc_endpoint,omitempty"`
	AuthToken             string             `json:"auth_token,omitempty"`
	RegisteredAt          int64              `json:"registered_at"`
	LastVerifiedAt        int64              `json:"last_verified_at"`
	LastHeartbeatAt       int64              `json:"last_heartbeat_at"`
	TotalChallengesPassed uint64             `json:"total_challenges_passed"`
	TotalChallengesFailed uint64             `json:"total_challenges_failed"`
	TotalUptimeMinutes    uint64             `json:"total_uptime_minutes"`
	TotalPoints           uint64             `json:"total_points"`
	IsActive              bool               `json:"is_active"`
	IsSynced              bool               `json:"is_synced"`

	// Anti-cheat
	CheatStatus      CheatStatus `json:"cheat_status"`
	WarningCount     uint8       `json:"warning_count"`
	CheatReason      string      `json:"cheat_reason,omitempty"`
	SuspiciousEvents []string    `json:"suspicious_events,omitempty"`
}

// Challenge we send to nodes
type Challenge struct {
	ID            string          `json:"id"`
	NodeID        string          `json:"node_id"`
	ChallengeType ChallengeType   `json:"challenge_type"`
	CreatedAt     int64           `json:"created_at"`
	ExpiresAt     int64           `json:"expires_at"`
	Params        ChallengeParams `json:"params"`
}

type ChallengeParams struct {
	BlockNumber *uint64 `json:"block_number,omitempty"`
	Address     string  `json:"address,omitempty"`
	TxHash      string  `json:"tx_hash,omitempty"`
}

// Response from user's prover
type ChallengeResponse struct {
	ChallengeID    string `json:"challenge_id"`
	NodeID         string `json:"node_id"`
	Answer         string `json:"answer"`
	Signature      string `json:"signature"`
	ResponseTimeMs uint64 `json:"response_time_ms"`
	Timestamp      int64  `json:"timestamp"`
}

// Result of verification
type VerificationResult struct {
	ChallengeID    string `json:"challenge_id"`
	NodeID         string `json:"node_id"`
	Passed         bool   `json:"passed"`
	ResponseTimeMs uint64 `json:"response_time_ms"`
	FailureReason  string `json:"failure_reason,omitempty"`
	Suspicious     bool   `json:"suspicious"`
	SuspiciousNote string `json:"suspicious_note,omitempty"`
	Timestamp      int64  `json:"timestamp"`
	// SkipPointsAward, when true, tells the store to record the verification
	// (history, counters, last-verified) but withhold challenge points. The
	// server sets it from the token gate; it is never accepted from clients
	// (json:"-") so a local prover cannot self-exempt by forging the field.
	SkipPointsAward bool `json:"-"`
}

// Heartbeat for uptime tracking
type HeartbeatRecord struct {
	NodeID      string `json:"node_id"`
	Timestamp   int64  `json:"timestamp"`
	BlockNumber uint64 `json:"block_number"`
	IsSynced    bool   `json:"is_synced"`
	LatencyMs   uint64 `json:"latency_ms"`
	PeersCount  uint64 `json:"peers_count"`
}

// Stats for a node
type NodeStats struct {
	NodeID             string      `json:"node_id"`
	TotalPoints        uint64      `json:"total_points"`
	TotalUptimeMinutes uint64      `json:"total_uptime_minutes"`
	TotalUptimeHours   float64     `json:"total_uptime_hours"`
	ChallengePassRate  float64     `json:"challenge_pass_rate"`
	AverageLatencyMs   float64     `json:"average_latency_ms"`
	CheatStatus        CheatStatus `json:"cheat_status"`
	WarningCount       uint8       `json:"warning_count"`
}

// Wallet-level stats (user can have multiple nodes)
type WalletStats struct {
	WalletAddress string `json:"wallet_address"`
	TotalPoints   uint64 `json:"total_points"`
	TotalNodes    int    `json:"total_nodes"`
	ActiveNodes   int    `json:"active_nodes"`
	FlaggedNodes  int    `json:"flagged_nodes"`
}

// Latency limits for anti-cheat
const (
	LatencyLocalNode     uint64 = 100  // Local nodes respond in under 100ms
	LatencySuspiciousMin uint64 = 150  // Anything over this is suspicious
	LatencyPublicRPC     uint64 = 300  // Public RPCs typically take 300ms+
	LatencyMaxAllowed    uint64 = 5000 // Timeout after this
)

// Snapshot is one published Merkle reward cycle (ADR-0008). The on-chain
// Distributor consumes Root + per-wallet (amount, proof) tuples; this struct
// is the off-chain shadow the API hands out for operator claim flows.
//
// Encoding notes:
//   - Root and TotalAmount are hex-encoded (with the "0x" prefix) so amounts
//     beyond int64 round-trip cleanly through SQLite TEXT columns.
//   - CycleID is a caller-supplied opaque string; the snapshot job decides
//     the format (e.g. "cycle-1", "2026-W18"). Idempotent on (cycle_id).
type Snapshot struct {
	CycleID     string `json:"cycle_id"`
	Root        string `json:"root"`         // hex-encoded keccak256 root, "0x..."
	TotalAmount string `json:"total_amount"` // hex-encoded big.Int sum, "0x..."
	NodeCount   int    `json:"node_count"`
	PublishedAt int64  `json:"published_at"` // unix-ms
	IPFSCID     string `json:"ipfs_cid,omitempty"`
}
