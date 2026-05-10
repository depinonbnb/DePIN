// Package api: snapshot_handlers.go implements the Phase 4 reward endpoints
// described in docs/adr/0008-merkle-snapshot-rewards.md §API.
//
// Endpoints:
//
//	POST /api/admin/snapshot/publish        — admin-only; runs the cycle build
//	                                          and persists the result.
//	GET  /api/snapshots/latest              — public; latest cycle metadata.
//	GET  /api/snapshots/:cycleId            — public; specific cycle metadata.
//	GET  /api/wallet/:walletAddress/claim/:cycleId — public; per-wallet proof.
//	GET  /api/wallet/:walletAddress/claim/latest   — public; convenience.
//
// The Solidity Distributor (separate repo, see ADR-0008 §Decision) consumes
// the (root, amount, proof) tuple. This service does not call contracts.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/reward"
	"github.com/gin-gonic/gin"
)

// PublishSnapshotRequest is the body of POST /admin/snapshot/publish.
type PublishSnapshotRequest struct {
	CycleID string `json:"cycle_id" binding:"required"`
	IPFSCID string `json:"ipfs_cid"`
}

// PublishSnapshot builds and persists a reward cycle. Admin-only.
func (h *Handlers) PublishSnapshot(c *gin.Context) {
	var req PublishSnapshotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required field: cycle_id"})
		return
	}
	cycleID := strings.TrimSpace(req.CycleID)
	if cycleID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cycle_id must be non-empty"})
		return
	}

	buildStart := time.Now()
	cycle, err := reward.BuildCycle(h.store, cycleID)
	metrics.SnapshotBuildSeconds.Observe(time.Since(buildStart).Seconds())
	if err != nil {
		if errors.Is(err, reward.ErrNoEarners) {
			// ADR-0008 §Decision: zero-earner cycles are valid; we just don't
			// publish a tree. Surface a 200 with a clear message rather than
			// a 4xx so the snapshot job can log + skip.
			c.JSON(http.StatusOK, gin.H{
				"success":     true,
				"published":   false,
				"cycle_id":    cycleID,
				"node_count":  0,
				"message":     "cycle has no earners; nothing published",
			})
			return
		}
		metrics.SnapshotBuildFailures.Inc()
		slog.Default().Error("PublishSnapshot: BuildCycle failed",
			"cycle_id", cycleID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build cycle"})
		return
	}

	publishedAt := time.Now().UnixMilli()
	if err := cycle.Persist(h.store, publishedAt, req.IPFSCID); err != nil {
		metrics.SnapshotBuildFailures.Inc()
		slog.Default().Error("PublishSnapshot: persist failed",
			"cycle_id", cycleID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist snapshot"})
		return
	}

	metrics.SnapshotsPublished.Inc()
	metrics.LastSnapshotPublished.SetToCurrentTime()

	c.JSON(http.StatusOK, gin.H{
		"success":      true,
		"published":    true,
		"cycle_id":     cycleID,
		"root":         "0x" + hexEncodeBytes(cycle.Root),
		"node_count":   cycle.NodeCount,
		"total_amount": "0x" + cycle.TotalAmount.Text(16),
		"published_at": publishedAt,
	})
}

// GetLatestSnapshot returns the most recently published cycle's metadata.
// 404 when no snapshots have been published yet.
func (h *Handlers) GetLatestSnapshot(c *gin.Context) {
	snap, err := h.store.GetLatestSnapshot()
	if err != nil {
		slog.Default().Error("GetLatestSnapshot: store error", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store error"})
		return
	}
	if snap == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no snapshots published yet"})
		return
	}
	c.JSON(http.StatusOK, snap)
}

// GetSnapshotByCycle returns a specific cycle's metadata. 404 if missing.
func (h *Handlers) GetSnapshotByCycle(c *gin.Context) {
	cycleID := strings.TrimSpace(c.Param("cycleId"))
	if cycleID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cycleId required"})
		return
	}
	snap, err := h.store.GetSnapshotByCycle(cycleID)
	if err != nil {
		slog.Default().Error("GetSnapshotByCycle: store error", "cycle_id", cycleID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store error"})
		return
	}
	if snap == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "cycle not found"})
		return
	}
	c.JSON(http.StatusOK, snap)
}

// GetWalletClaim returns the (cycle, amount, proof) tuple for a wallet in a
// specific cycle so the operator can submit it on chain.
func (h *Handlers) GetWalletClaim(c *gin.Context) {
	wallet := strings.ToLower(strings.TrimSpace(c.Param("walletAddress")))
	cycleID := strings.TrimSpace(c.Param("cycleId"))
	if wallet == "" || cycleID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "walletAddress and cycleId required"})
		return
	}

	snap, proof, amount, err := h.store.GetWalletProof(cycleID, wallet)
	if err != nil {
		slog.Default().Error("GetWalletClaim: store error",
			"cycle_id", cycleID, "wallet", wallet, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store error"})
		return
	}
	if snap == nil {
		metrics.ClaimLookupsTotal.WithLabelValues("not_found").Inc()
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet has no claim in this cycle"})
		return
	}

	metrics.ClaimLookupsTotal.WithLabelValues("found").Inc()
	c.JSON(http.StatusOK, gin.H{
		"wallet":   wallet,
		"cycle_id": snap.CycleID,
		"root":     snap.Root,
		"amount":   "0x" + amount.Text(16),
		"proof":    reward.HexProof(proof),
	})
}

// GetWalletClaimLatest is a convenience: latest cycle + the wallet's proof in
// it. Returns 404 if no snapshots exist OR if the wallet has no proof in the
// latest cycle.
func (h *Handlers) GetWalletClaimLatest(c *gin.Context) {
	wallet := strings.ToLower(strings.TrimSpace(c.Param("walletAddress")))
	if wallet == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "walletAddress required"})
		return
	}
	latest, err := h.store.GetLatestSnapshot()
	if err != nil {
		slog.Default().Error("GetWalletClaimLatest: latest store error",
			"wallet", wallet, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store error"})
		return
	}
	if latest == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no snapshots published yet"})
		return
	}
	snap, proof, amount, err := h.store.GetWalletProof(latest.CycleID, wallet)
	if err != nil {
		slog.Default().Error("GetWalletClaimLatest: proof store error",
			"cycle_id", latest.CycleID, "wallet", wallet, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store error"})
		return
	}
	if snap == nil {
		metrics.ClaimLookupsTotal.WithLabelValues("not_found").Inc()
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet has no claim in latest cycle"})
		return
	}
	metrics.ClaimLookupsTotal.WithLabelValues("found").Inc()
	c.JSON(http.StatusOK, gin.H{
		"wallet":   wallet,
		"cycle_id": snap.CycleID,
		"root":     snap.Root,
		"amount":   "0x" + amount.Text(16),
		"proof":    reward.HexProof(proof),
	})
}

// hexEncodeBytes is a tiny helper that lower-cases bytes into hex without
// pulling encoding/hex into this file's import list — kept local because the
// API package already has plenty of imports.
func hexEncodeBytes(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}
