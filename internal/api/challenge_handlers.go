package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/gin-gonic/gin"
)

// ==================
// CHALLENGES (local-prover path)
// ==================

// ChallengeRequestResponse is returned from GET /challenges/request.
type ChallengeRequestResponse struct {
	Challenge  ChallengePublic `json:"challenge"`
	ServerTime int64           `json:"server_time"`
}

// ChallengePublic is the externally-visible projection of a Challenge — it
// intentionally omits the expected answer the verifier holds internally.
type ChallengePublic struct {
	ID            string                `json:"id"`
	ChallengeType types.ChallengeType   `json:"challenge_type"`
	Params        types.ChallengeParams `json:"params"`
	ExpiresAt     int64                 `json:"expires_at"`
}

// SubmitChallengeRequest is the body of POST /challenges/submit.
//
// Phase 3 (anti-replay): Nonce is required and forms part of the signed
// message. The server consumes (wallet, nonce) with a 10-minute TTL after
// signature verification — replays in-window return 409 Conflict.
type SubmitChallengeRequest struct {
	ChallengeID    string `json:"challenge_id" binding:"required"`
	NodeID         string `json:"node_id" binding:"required"`
	Answer         string `json:"answer" binding:"required"`
	Signature      string `json:"signature" binding:"required"`
	ResponseTimeMs uint64 `json:"response_time_ms"`
	Timestamp      int64  `json:"timestamp" binding:"required"`
	Nonce          string `json:"nonce" binding:"required"`
}

// GET /challenges/request - issue a challenge to a node (local-prover path).
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

// POST /challenges/submit - submit a signed response to a challenge.
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

	// Verify signature. Phase 3: Nonce is bound into the message (SPEC §6).
	message := "Challenge Response\nID: " + req.ChallengeID +
		"\nAnswer: " + req.Answer +
		"\nTimestamp: " + fmt.Sprintf("%d", req.Timestamp) +
		"\nNonce: " + req.Nonce
	if !h.verifySignature(message, req.Signature, node.WalletAddress) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	// Phase 3: anti-replay nonce consumption.
	if !h.store.ConsumeNonce(strings.ToLower(node.WalletAddress), req.Nonce, 10*time.Minute) {
		metrics.NonceReplaysTotal.Inc()
		c.JSON(http.StatusConflict, gin.H{"error": "replayed nonce"})
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

	// Token gate: a local prover cannot earn challenge points without holding
	// the minimum balance. Resolve the wallet (cache-first; lazy fetch on miss)
	// and withhold the payout if it falls short. The verification is still
	// recorded — only the points are suppressed. No-op when gating is disabled.
	if h.holder.Enabled() {
		h.holder.Refresh(c.Request.Context(), []string{node.WalletAddress})
		result.SkipPointsAward = !h.holder.Allow(node.WalletAddress)
	}

	h.store.RecordVerificationResult(result)

	// Record a heartbeat so local-prover nodes accrue uptime points. The
	// heartbeat ticker only probes exposed-RPC nodes, so without this a prover
	// node would never be credited for uptime by the reward ticker.
	h.store.RecordHeartbeat(&types.HeartbeatRecord{
		NodeID:    req.NodeID,
		Timestamp: time.Now().UnixMilli(),
		IsSynced:  result.Passed,
		LatencyMs: result.ResponseTimeMs,
	})

	// Record verification observation (mirrors the scheduler-side path so
	// local-prover submissions show up in the same histogram + counter).
	metrics.VerificationLatencyMs.Observe(float64(result.ResponseTimeMs))
	outcome := "passed"
	if !result.Passed {
		outcome = "failed"
	}
	metrics.ChallengesTotal.WithLabelValues(outcome, "local-prover", string(node.NodeType)).Inc()

	c.JSON(http.StatusOK, VerifyResponse{
		Passed:         result.Passed,
		FailureReason:  result.FailureReason,
		ResponseTimeMs: result.ResponseTimeMs,
	})
}
