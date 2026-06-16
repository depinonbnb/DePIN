package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/gin-gonic/gin"
)

// ==================
// ADMIN ENDPOINTS
// ==================

// ReviewRequest is the body of POST /admin/review/:nodeId.
type ReviewRequest struct {
	Action string `json:"action" binding:"required"` // "clear", "warn", "ban"
	Reason string `json:"reason"`
}

// TestCreateNodeRequest is the body of POST /admin/test/create-node.
// It is intentionally smaller than RegisterRequest — no signature is required.
type TestCreateNodeRequest struct {
	WalletAddress      string                   `json:"wallet_address" binding:"required"`
	NodeType           types.NodeType           `json:"node_type" binding:"required"`
	VerificationMethod types.VerificationMethod `json:"verification_method" binding:"required"`
	RPCEndpoint        string                   `json:"rpc_endpoint"`
	// Optional seed state for demo/test nodes.
	TotalPoints   uint64 `json:"total_points"`
	Verifications uint64 `json:"verifications"`
	UptimeMinutes uint64 `json:"uptime_minutes"`
	Synced        bool   `json:"synced"`
}

// GET /admin/flagged - Get all nodes that need review.
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

// POST /admin/review/:nodeId - Admin reviews a flagged node.
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

	metrics.AdminActionsTotal.WithLabelValues(req.Action).Inc()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"node_id": nodeID,
		"status":  status,
		"message": fmt.Sprintf("Node status set to %s", status),
	})
}

// POST /admin/test/create-node - Create test node without signature (for testing).
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

	// Optionally seed demo state (points / synced). Cheat status stays Clean
	// from registration.
	if req.TotalPoints > 0 || req.Verifications > 0 || req.UptimeMinutes > 0 || req.Synced {
		h.store.SeedNode(node.ID, req.TotalPoints, req.Verifications, req.UptimeMinutes, req.Synced)
		node = h.store.GetNode(node.ID)
	}

	c.JSON(http.StatusOK, gin.H{
		"success":      true,
		"node_id":      node.ID,
		"wallet":       node.WalletAddress,
		"node_type":    node.NodeType,
		"total_points": node.TotalPoints,
		"is_synced":    node.IsSynced,
		"message":      "test node created successfully",
	})
}

// abs returns the absolute value of x. Used for timestamp drift comparison in
// signature-validating handlers. Lives here because admin_handlers.go pulls in
// no extra packages and any handler file could host a tiny helper, but keeping
// it next to the admin code (the only file that already imports nothing else
// from stdlib beyond fmt/net/http/strings) avoids unused-import churn in the
// other split files.
func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
