package api

import (
	"net/http"

	"github.com/depinonbnb/depin/internal/types"
	"github.com/gin-gonic/gin"
)

// ==================
// DIRECT VERIFICATION (exposed-RPC path)
// ==================

// VerifyResponse is returned from /verify/:nodeId and /challenges/submit.
// It deliberately surfaces only the fields a caller needs to decide if a
// node is healthy without leaking internal verification result detail.
type VerifyResponse struct {
	Passed         bool   `json:"passed"`
	FailureReason  string `json:"failure_reason,omitempty"`
	ResponseTimeMs uint64 `json:"response_time_ms"`
}

// POST /verify/:nodeId - server probes the node's RPC, compares against
// TRUSTED_RPC, records a VerificationResult.
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

// GET /verify/:nodeId/heartbeat - lightweight uptime ping; calls eth_blockNumber
// against the node and records a HeartbeatRecord on success.
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
