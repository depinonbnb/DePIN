package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
)

type Handlers struct {
	store    *store.Store
	verifier *verification.Verifier
}

func NewHandlers(store *store.Store, verifier *verification.Verifier) *Handlers {
	return &Handlers{
		store:    store,
		verifier: verifier,
	}
}

// Request/Response types
type RegisterRequest struct {
	WalletAddress      string                   `json:"wallet_address" binding:"required"`
	NodeType           types.NodeType           `json:"node_type" binding:"required"`
	VerificationMethod types.VerificationMethod `json:"verification_method" binding:"required"`
	RPCEndpoint        string                   `json:"rpc_endpoint"`
	AuthToken          string                   `json:"auth_token"`
	Signature          string                   `json:"signature" binding:"required"`
	Timestamp          int64                    `json:"timestamp" binding:"required"`
}

type RegisterResponse struct {
	Success bool   `json:"success"`
	NodeID  string `json:"node_id"`
	Message string `json:"message"`
}

type ChallengeRequestResponse struct {
	Challenge  ChallengePublic `json:"challenge"`
	ServerTime int64           `json:"server_time"`
}

type ChallengePublic struct {
	ID            string                `json:"id"`
	ChallengeType types.ChallengeType   `json:"challenge_type"`
	Params        types.ChallengeParams `json:"params"`
	ExpiresAt     int64                 `json:"expires_at"`
}

type SubmitChallengeRequest struct {
	ChallengeID    string `json:"challenge_id" binding:"required"`
	NodeID         string `json:"node_id" binding:"required"`
	Answer         string `json:"answer" binding:"required"`
	Signature      string `json:"signature" binding:"required"`
	ResponseTimeMs uint64 `json:"response_time_ms"`
	Timestamp      int64  `json:"timestamp" binding:"required"`
}

type VerifyResponse struct {
	Passed         bool   `json:"passed"`
	FailureReason  string `json:"failure_reason,omitempty"`
	ResponseTimeMs uint64 `json:"response_time_ms"`
}

// Verify wallet signature
func (h *Handlers) verifySignature(message, signature, expectedAddress string) bool {
	// Remove 0x prefix if present
	sig := strings.TrimPrefix(signature, "0x")

	sigBytes := make([]byte, 65)
	for i := 0; i < 65; i++ {
		var b byte
		_, err := hexToByte(sig[i*2:i*2+2], &b)
		if err != nil {
			return false
		}
		sigBytes[i] = b
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

func hexToByte(s string, b *byte) (int, error) {
	n := 0
	for _, c := range s {
		n *= 16
		switch {
		case '0' <= c && c <= '9':
			n += int(c - '0')
		case 'a' <= c && c <= 'f':
			n += int(c - 'a' + 10)
		case 'A' <= c && c <= 'F':
			n += int(c - 'A' + 10)
		}
	}
	*b = byte(n)
	return 2, nil
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

	// Check timestamp is recent (within 5 minutes)
	now := time.Now().UnixMilli()
	if abs(now-req.Timestamp) > 5*60*1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "timestamp too old"})
		return
	}

	// Verify signature
	message := "Register node\nWallet: " + req.WalletAddress + "\nType: " + string(req.NodeType) + "\nTimestamp: " + fmt.Sprintf("%d", req.Timestamp)
	if !h.verifySignature(message, req.Signature, req.WalletAddress) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
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
// CHALLENGES
// ==================

// GET /challenges/request
func (h *Handlers) RequestChallenge(c *gin.Context) {
	nodeID := c.Query("nodeId")
	if nodeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nodeId required"})
		return
	}

	node := h.store.GetNode(nodeID)
	if node == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	if !node.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node is not active"})
		return
	}

	challenge, err := h.verifier.CreateChallenge(node)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create challenge"})
		return
	}

	c.JSON(http.StatusOK, ChallengeRequestResponse{
		Challenge: ChallengePublic{
			ID:            challenge.ID,
			ChallengeType: challenge.ChallengeType,
			Params:        challenge.Params,
			ExpiresAt:     challenge.ExpiresAt,
		},
		ServerTime: time.Now().UnixMilli(),
	})
}

// POST /challenges/submit
func (h *Handlers) SubmitChallenge(c *gin.Context) {
	var req SubmitChallengeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required fields"})
		return
	}

	node := h.store.GetNode(req.NodeID)
	if node == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	// Verify signature
	message := "Challenge Response\nID: " + req.ChallengeID + "\nAnswer: " + req.Answer + "\nTimestamp: " + fmt.Sprintf("%d", req.Timestamp)
	if !h.verifySignature(message, req.Signature, node.WalletAddress) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	// Verify the response
	result := h.verifier.VerifyResponse(&types.ChallengeResponse{
		ChallengeID:    req.ChallengeID,
		NodeID:         req.NodeID,
		Answer:         req.Answer,
		Signature:      req.Signature,
		ResponseTimeMs: req.ResponseTimeMs,
		Timestamp:      req.Timestamp,
	})

	h.store.RecordVerificationResult(result)

	c.JSON(http.StatusOK, VerifyResponse{
		Passed:         result.Passed,
		FailureReason:  result.FailureReason,
		ResponseTimeMs: result.ResponseTimeMs,
	})
}

// ==================
// DIRECT VERIFICATION
// ==================

// POST /verify/:nodeId
func (h *Handlers) VerifyNode(c *gin.Context) {
	nodeID := c.Param("nodeId")
	node := h.store.GetNode(nodeID)

	if node == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	if node.VerificationMethod != types.ExposedRPC {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node is not using exposed-rpc method"})
		return
	}

	result := h.verifier.VerifyExposedRPC(node)
	h.store.RecordVerificationResult(result)

	c.JSON(http.StatusOK, VerifyResponse{
		Passed:         result.Passed,
		FailureReason:  result.FailureReason,
		ResponseTimeMs: result.ResponseTimeMs,
	})
}

// GET /verify/:nodeId/heartbeat
func (h *Handlers) CheckHeartbeat(c *gin.Context) {
	nodeID := c.Param("nodeId")
	node := h.store.GetNode(nodeID)

	if node == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	if node.VerificationMethod != types.ExposedRPC {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node is not using exposed-rpc method"})
		return
	}

	heartbeat := h.verifier.CheckHeartbeat(node)
	if heartbeat == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "node unreachable"})
		return
	}

	h.store.RecordHeartbeat(heartbeat)
	c.JSON(http.StatusOK, heartbeat)
}

// ==================
// PUBLIC DATA
// ==================

// GET /leaderboard
func (h *Handlers) GetLeaderboard(c *gin.Context) {
	nodes := h.store.GetAllActiveNodes()

	type LeaderboardEntry struct {
		Rank               int              `json:"rank"`
		NodeID             string           `json:"node_id"`
		WalletAddress      string           `json:"wallet_address"`
		NodeType           types.NodeType   `json:"node_type"`
		TotalPoints        uint64           `json:"total_points"`
		TotalUptimeHours   float64          `json:"total_uptime_hours"`
		ChallengePassRate  float64          `json:"challenge_pass_rate"`
		RegisteredAt       int64            `json:"registered_at"`
	}

	entries := make([]LeaderboardEntry, 0, len(nodes))
	for _, node := range nodes {
		// Don't show banned nodes on leaderboard
		if node.CheatStatus == types.StatusBanned {
			continue
		}

		stats := h.store.GetNodeStats(node.ID)
		entry := LeaderboardEntry{
			NodeID:             node.ID,
			WalletAddress:      node.WalletAddress,
			NodeType:           node.NodeType,
			TotalPoints:        node.TotalPoints,
			TotalUptimeHours:   float64(node.TotalUptimeMinutes) / 60.0,
			RegisteredAt:       node.RegisteredAt,
		}
		if stats != nil {
			entry.ChallengePassRate = stats.ChallengePassRate
		}
		entries = append(entries, entry)
	}

	// Sort by total points (highest first)
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].TotalPoints > entries[i].TotalPoints {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

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

// ==================
// ADMIN ENDPOINTS
// ==================

// GET /admin/flagged - Get all nodes that need review
func (h *Handlers) GetFlaggedNodes(c *gin.Context) {
	flagged := h.store.GetFlaggedNodes()

	// Don't expose auth tokens
	safeNodes := make([]types.NodeRegistration, len(flagged))
	for i, node := range flagged {
		safeNodes[i] = *node
		safeNodes[i].AuthToken = ""
	}

	c.JSON(http.StatusOK, gin.H{
		"count": len(safeNodes),
		"nodes": safeNodes,
	})
}

// POST /admin/review/:nodeId - Admin reviews a flagged node
type ReviewRequest struct {
	Action string `json:"action" binding:"required"` // "clear", "warn", "ban"
	Reason string `json:"reason"`
}

func (h *Handlers) ReviewNode(c *gin.Context) {
	nodeID := c.Param("nodeId")

	var req ReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action required (clear, warn, or ban)"})
		return
	}

	var status types.CheatStatus
	switch req.Action {
	case "clear":
		status = types.StatusClean
	case "warn":
		status = types.StatusWarning
	case "ban":
		status = types.StatusBanned
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid action - use clear, warn, or ban"})
		return
	}

	if !h.store.SetNodeCheatStatus(nodeID, status, req.Reason) {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"node_id": nodeID,
		"status":  status,
		"message": fmt.Sprintf("Node status set to %s", status),
	})
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// ==================
// TEST ENDPOINTS (Admin only)
// ==================

// POST /admin/test/create-node - Create test node without signature (for testing)
type TestCreateNodeRequest struct {
	WalletAddress      string                   `json:"wallet_address" binding:"required"`
	NodeType           types.NodeType           `json:"node_type" binding:"required"`
	VerificationMethod types.VerificationMethod `json:"verification_method" binding:"required"`
	RPCEndpoint        string                   `json:"rpc_endpoint"`
}

func (h *Handlers) TestCreateNode(c *gin.Context) {
	var req TestCreateNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required fields: wallet_address, node_type, verification_method"})
		return
	}

	// Register the node without signature verification
	node := h.store.RegisterNode(
		strings.ToLower(req.WalletAddress),
		req.NodeType,
		req.VerificationMethod,
		req.RPCEndpoint,
		"",
	)

	c.JSON(http.StatusOK, gin.H{
		"success":      true,
		"node_id":      node.ID,
		"wallet":       node.WalletAddress,
		"node_type":    node.NodeType,
		"total_points": node.TotalPoints,
		"message":      "test node created successfully",
	})
}
