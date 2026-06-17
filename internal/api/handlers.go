package api

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/rpc"
	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/token"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
)

// Handlers carries the dependencies every HTTP handler shares: the persistence
// store and the verifier. Methods on Handlers are split across several files
// in this package (admin_handlers.go, challenge_handlers.go, verify_handlers.go)
// to keep each file under the project's 500-LOC cap. The router (router.go)
// wires them all up.
type Handlers struct {
	store    store.Store
	verifier *verification.Verifier
	// holder gates point awards and supplies the holds_token display flag.
	// Defaults to token.Noop (gating disabled) unless the router injects an
	// on-chain checker via WithHolderChecker.
	holder token.Checker
}

// NewHandlers constructs a Handlers value bound to the given store and verifier.
// Token gating is disabled by default; the router opts in via WithHolderChecker.
func NewHandlers(store store.Store, verifier *verification.Verifier) *Handlers {
	return &Handlers{
		store:    store,
		verifier: verifier,
		holder:   token.Noop{},
	}
}

// RegisterRequest is the body of POST /nodes/register.
//
// Phase 3 (anti-replay): Nonce is now required. The signed message includes
// the nonce so a captured signature cannot be reused for a different request,
// and the server records (wallet, nonce) with a 10-minute TTL — replays
// inside that window are rejected with 409 Conflict.
type RegisterRequest struct {
	WalletAddress      string                   `json:"wallet_address" binding:"required"`
	NodeType           types.NodeType           `json:"node_type" binding:"required"`
	VerificationMethod types.VerificationMethod `json:"verification_method" binding:"required"`
	RPCEndpoint        string                   `json:"rpc_endpoint"`
	AuthToken          string                   `json:"auth_token"`
	Signature          string                   `json:"signature" binding:"required"`
	Timestamp          int64                    `json:"timestamp" binding:"required"`
	Nonce              string                   `json:"nonce" binding:"required"`
}

// RegisterResponse is returned from POST /nodes/register.
type RegisterResponse struct {
	Success bool   `json:"success"`
	NodeID  string `json:"node_id"`
	Message string `json:"message"`
}

// verifySignature checks that a hex-encoded EIP-191 secp256k1 signature was
// produced by the wallet at expectedAddress over the given message. Returns
// false on any decoding / recovery failure.
func (h *Handlers) verifySignature(message, signature, expectedAddress string) bool {
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil || len(sigBytes) != 65 {
		return false
	}

	// Ethereum signatures have v = 27 or 28, but we need 0 or 1
	if sigBytes[64] >= 27 {
		sigBytes[64] -= 27
	}

	// Hash the message with Ethereum prefix
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	hash := crypto.Keccak256Hash([]byte(prefixedMessage))

	// Recover public key
	pubKey, err := crypto.SigToPub(hash.Bytes(), sigBytes)
	if err != nil {
		return false
	}

	recoveredAddress := crypto.PubkeyToAddress(*pubKey).Hex()
	return strings.EqualFold(recoveredAddress, expectedAddress)
}

// ==================
// NODE REGISTRATION
// ==================

// POST /nodes/register
func (h *Handlers) RegisterNode(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required fields"})
		return
	}

	// If exposed-rpc, need endpoint
	if req.VerificationMethod == types.ExposedRPC && req.RPCEndpoint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rpc endpoint required for exposed-rpc method"})
		return
	}

	// Phase 3: SSRF guard — only validate when an endpoint was provided.
	// Reject RFC1918 / loopback / link-local / well-known internal suffixes.
	// ALLOW_PRIVATE_RPC=1 disables for local docker-compose dev.
	if req.RPCEndpoint != "" {
		if err := rpc.IsAllowedURL(req.RPCEndpoint); err != nil {
			metrics.SSRFRejectionsTotal.Inc()
			c.JSON(http.StatusBadRequest, gin.H{"error": "rpc endpoint not allowed (must be public)"})
			return
		}
	}

	// Check timestamp is recent (within 5 minutes)
	now := time.Now().UnixMilli()
	if abs(now-req.Timestamp) > 5*60*1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "timestamp too old"})
		return
	}

	// Verify signature. Phase 3: the signed message now includes Nonce so a
	// captured signature cannot be replayed against a different request.
	// SPEC §6 documents this wire-format change.
	message := "Register node\nWallet: " + req.WalletAddress +
		"\nType: " + string(req.NodeType) +
		"\nTimestamp: " + fmt.Sprintf("%d", req.Timestamp) +
		"\nNonce: " + req.Nonce
	if !h.verifySignature(message, req.Signature, req.WalletAddress) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	// One node per network address (the "one node per location, for now" rule).
	// Checked before consuming the nonce so a rejected attempt doesn't burn it.
	clientIP := c.ClientIP()
	if h.store.HasActiveNodeFromIP(clientIP) {
		c.JSON(http.StatusConflict, gin.H{"error": "a node is already registered from this network address (one node per address)"})
		return
	}

	// Phase 3: anti-replay. After signature verification, consume the nonce.
	// 10-minute TTL matches the timestamp drift window times two so a slow
	// client can't legitimately submit twice within the window.
	if !h.store.ConsumeNonce(strings.ToLower(req.WalletAddress), req.Nonce, 10*time.Minute) {
		metrics.NonceReplaysTotal.Inc()
		c.JSON(http.StatusConflict, gin.H{"error": "replayed nonce"})
		return
	}

	// Register the node
	node := h.store.RegisterNode(
		strings.ToLower(req.WalletAddress),
		req.NodeType,
		req.VerificationMethod,
		req.RPCEndpoint,
		req.AuthToken,
	)
	if clientIP != "" {
		h.store.SetNodeIP(node.ID, clientIP)
	}

	c.JSON(http.StatusOK, RegisterResponse{
		Success: true,
		NodeID:  node.ID,
		Message: "node registered successfully",
	})
}

// GET /nodes/:nodeId
func (h *Handlers) GetNode(c *gin.Context) {
	nodeID := c.Param("nodeId")
	node := h.store.GetNode(nodeID)

	if node == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	// Don't expose auth token. holds_token mirrors the token gate (true for
	// everyone when gating is disabled).
	safeCopy := *node
	safeCopy.AuthToken = ""
	holds, _ := h.holder.IsHolder(safeCopy.WalletAddress)
	c.JSON(http.StatusOK, nodeWithHolder{NodeRegistration: safeCopy, HoldsToken: holds})
}

// nodeWithHolder embeds a node and adds the holds_token display flag. The
// embedded struct is flattened by encoding/json, so the response shape is the
// node's fields plus holds_token.
type nodeWithHolder struct {
	types.NodeRegistration
	HoldsToken bool `json:"holds_token"`
}

// GET /nodes/wallet/:walletAddress
func (h *Handlers) GetNodesByWallet(c *gin.Context) {
	wallet := strings.ToLower(c.Param("walletAddress"))
	nodes := h.store.GetNodesByWallet(wallet)

	// Don't expose auth tokens; annotate each node with its holds_token flag.
	out := make([]nodeWithHolder, len(nodes))
	for i, node := range nodes {
		n := *node
		n.AuthToken = ""
		holds, _ := h.holder.IsHolder(n.WalletAddress)
		out[i] = nodeWithHolder{NodeRegistration: n, HoldsToken: holds}
	}

	c.JSON(http.StatusOK, out)
}

// GET /wallet/:walletAddress/stats
func (h *Handlers) GetWalletStats(c *gin.Context) {
	wallet := strings.ToLower(c.Param("walletAddress"))
	stats := h.store.GetWalletStats(wallet)

	if stats == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// GET /nodes/:nodeId/stats
func (h *Handlers) GetNodeStats(c *gin.Context) {
	nodeID := c.Param("nodeId")
	stats := h.store.GetNodeStats(nodeID)

	if stats == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// ==================
// PUBLIC DATA
// ==================

// GET /leaderboard
func (h *Handlers) GetLeaderboard(c *gin.Context) {
	nodes := h.store.GetAllActiveNodes()

	type LeaderboardEntry struct {
		Rank                  int               `json:"rank"`
		NodeID                string            `json:"node_id"`
		WalletAddress         string            `json:"wallet_address"`
		NodeType              types.NodeType    `json:"node_type"`
		TotalPoints           uint64            `json:"total_points"`
		TotalUptimeHours      float64           `json:"total_uptime_hours"`
		TotalChallengesPassed uint64            `json:"total_challenges_passed"`
		ChallengePassRate     float64           `json:"challenge_pass_rate"`
		CheatStatus           types.CheatStatus `json:"cheat_status"`
		CheatReason           string            `json:"cheat_reason,omitempty"`
		IsSynced              bool              `json:"is_synced"`
		LastHeartbeatAt       int64             `json:"last_heartbeat_at"`
		RegisteredAt          int64             `json:"registered_at"`
		// HoldsToken mirrors the token gate's verdict for this wallet. When
		// gating is disabled it is true for everyone; when enabled it is true
		// only for wallets resolved to hold at least the minimum balance.
		HoldsToken bool `json:"holds_token"`
	}

	entries := make([]LeaderboardEntry, 0, len(nodes))
	for _, node := range nodes {
		stats := h.store.GetNodeStats(node.ID)
		holds, _ := h.holder.IsHolder(node.WalletAddress)
		entry := LeaderboardEntry{
			NodeID:                node.ID,
			WalletAddress:         node.WalletAddress,
			NodeType:              node.NodeType,
			TotalPoints:           node.TotalPoints,
			TotalUptimeHours:      float64(node.TotalUptimeMinutes) / 60.0,
			TotalChallengesPassed: node.TotalChallengesPassed,
			CheatStatus:           node.CheatStatus,
			CheatReason:           node.CheatReason,
			IsSynced:              node.IsSynced,
			LastHeartbeatAt:       node.LastHeartbeatAt,
			RegisteredAt:          node.RegisteredAt,
			HoldsToken:            holds,
		}
		if stats != nil {
			entry.ChallengePassRate = stats.ChallengePassRate
		}
		entries = append(entries, entry)
	}

	// Sort by total points (highest first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TotalPoints > entries[j].TotalPoints
	})

	// Add ranks and limit to 100
	if len(entries) > 100 {
		entries = entries[:100]
	}
	for i := range entries {
		entries[i].Rank = i + 1
	}

	c.JSON(http.StatusOK, entries)
}

// GET /stats
func (h *Handlers) GetNetworkStats(c *gin.Context) {
	nodes := h.store.GetAllActiveNodes()

	byType := make(map[string]int)
	byMethod := make(map[string]int)

	var activeNodes int
	var totalVerifications uint64
	var totalPassed uint64
	var totalPoints uint64

	for _, node := range nodes {
		byType[string(node.NodeType)]++
		byMethod[string(node.VerificationMethod)]++
		if node.IsActive {
			activeNodes++
		}
		totalPassed += node.TotalChallengesPassed
		totalVerifications += node.TotalChallengesPassed + node.TotalChallengesFailed
		totalPoints += node.TotalPoints
	}

	var successRate float64
	if totalVerifications > 0 {
		successRate = float64(totalPassed) / float64(totalVerifications) * 100
	}

	c.JSON(http.StatusOK, gin.H{
		"total_nodes":               len(nodes),
		"active_nodes":              activeNodes,
		"total_verifications":       totalVerifications,
		"verification_success_rate": successRate,
		"total_points":              totalPoints,
		"by_type":                   byType,
		"by_method":                 byMethod,
	})
}
