package api

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/depinonbnb/depin/internal/rpc"
	"github.com/depinonbnb/depin/internal/store"
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
}

// NewHandlers constructs a Handlers value bound to the given store and verifier.
func NewHandlers(store store.Store, verifier *verification.Verifier) *Handlers {
	return &Handlers{
		store:    store,
		verifier: verifier,
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

	// Phase 3: anti-replay. After signature verification, consume the nonce.
	// 10-minute TTL matches the timestamp drift window times two so a slow
	// client can't legitimately submit twice within the window.
	if !h.store.ConsumeNonce(strings.ToLower(req.WalletAddress), req.Nonce, 10*time.Minute) {
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

	// Don't expose auth token
	safeCopy := *node
	safeCopy.AuthToken = ""
	c.JSON(http.StatusOK, safeCopy)
}

// GET /nodes/wallet/:walletAddress
func (h *Handlers) GetNodesByWallet(c *gin.Context) {
	wallet := strings.ToLower(c.Param("walletAddress"))
	nodes := h.store.GetNodesByWallet(wallet)

	// Don't expose auth tokens
	safeNodes := make([]types.NodeRegistration, len(nodes))
	for i, node := range nodes {
		safeNodes[i] = *node
		safeNodes[i].AuthToken = ""
	}

	c.JSON(http.StatusOK, safeNodes)
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
		Rank              int            `json:"rank"`
		NodeID            string         `json:"node_id"`
		WalletAddress     string         `json:"wallet_address"`
		NodeType          types.NodeType `json:"node_type"`
		TotalPoints       uint64         `json:"total_points"`
		TotalUptimeHours  float64        `json:"total_uptime_hours"`
		ChallengePassRate float64        `json:"challenge_pass_rate"`
		RegisteredAt      int64          `json:"registered_at"`
	}

	entries := make([]LeaderboardEntry, 0, len(nodes))
	for _, node := range nodes {
		// Don't show banned nodes on leaderboard
		if node.CheatStatus == types.StatusBanned {
			continue
		}

		stats := h.store.GetNodeStats(node.ID)
		entry := LeaderboardEntry{
			NodeID:           node.ID,
			WalletAddress:    node.WalletAddress,
			NodeType:         node.NodeType,
			TotalPoints:      node.TotalPoints,
			TotalUptimeHours: float64(node.TotalUptimeMinutes) / 60.0,
			RegisteredAt:     node.RegisteredAt,
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

	for _, node := range nodes {
		byType[string(node.NodeType)]++
		byMethod[string(node.VerificationMethod)]++
	}

	c.JSON(http.StatusOK, gin.H{
		"total_nodes": len(nodes),
		"by_type":     byType,
		"by_method":   byMethod,
	})
}
