package types

// What kind of node is the user running
type NodeType string

const (
	BscFull     NodeType = "bsc-full"
	BscFast     NodeType = "bsc-fast"
	BscArchive  NodeType = "bsc-archive"
	OpbnbFull   NodeType = "opbnb-full"
	OpbnbFast   NodeType = "opbnb-fast"
)

func (n NodeType) BaseRewardPerDay() uint64 {
	switch n {
	case BscArchive:
		return 150
	case BscFull:
		return 100
	case BscFast:
		return 70
	case OpbnbFull:
		return 60
	case OpbnbFast:
		return 40
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
	ExposedRPC   VerificationMethod = "exposed-rpc"
	LocalProver  VerificationMethod = "local-prover"
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
	TotalUptimeSeconds    uint64             `json:"total_uptime_seconds"`
	TotalChallengesPassed uint64             `json:"total_challenges_passed"`
	TotalChallengesFailed uint64             `json:"total_challenges_failed"`
	RewardTier            uint8              `json:"reward_tier"`
	IsActive              bool               `json:"is_active"`
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
	Timestamp      int64  `json:"timestamp"`
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
	NodeID             string  `json:"node_id"`
	UptimePercent      float64 `json:"uptime_percent"`
	ChallengePassRate  float64 `json:"challenge_pass_rate"`
	AverageLatencyMs   float64 `json:"average_latency_ms"`
	TotalRewardsEarned string  `json:"total_rewards_earned"`
	CurrentStreak      uint64  `json:"current_streak"`
}

// Latency limits for anti-cheat
const (
	LatencyLocalNode      uint64 = 100   // Local nodes respond in under 100ms
	LatencySuspiciousMin  uint64 = 150   // Anything over this is suspicious
	LatencyPublicRPC      uint64 = 300   // Public RPCs typically take 300ms+
	LatencyMaxAllowed     uint64 = 5000  // Timeout after this
)
