package sqlite

import (
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/depinonbnb/depin/internal/pointscap"
	"github.com/depinonbnb/depin/internal/types"
)

// GetNodeStats computes derived stats (24h pass rate, 24h average latency, and
// total uptime hours). Returns nil if the node does not exist.
func (s *SQLiteStore) GetNodeStats(nodeID string) *types.NodeStats {
	ctx := s.ctx

	var (
		totalPoints        uint64
		totalUptimeMinutes uint64
		cheatStatus        string
		warningCount       uint8
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT total_points, total_uptime_minutes, cheat_status, warning_count
		FROM nodes WHERE id = ?`,
		nodeID,
	).Scan(&totalPoints, &totalUptimeMinutes, &cheatStatus, &warningCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		slog.Default().Error("sqlite: GetNodeStats node row", "node_id", nodeID, "error", err)
		return nil
	}

	last24h := time.Now().UnixMilli() - 24*60*60*1000

	var (
		recentVerifications int64
		recentPassed        int64
		totalLatency        sql.NullInt64
	)
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(passed), 0),
			COALESCE(SUM(response_time_ms), 0)
		FROM verification_results
		WHERE node_id = ? AND ts >= ?`,
		nodeID, last24h,
	).Scan(&recentVerifications, &recentPassed, &totalLatency); err != nil {
		slog.Default().Error("sqlite: GetNodeStats aggregate", "node_id", nodeID, "error", err)
		return nil
	}

	passRate := float64(0)
	avgLatency := float64(0)
	if recentVerifications > 0 {
		passRate = float64(recentPassed) / float64(recentVerifications) * 100
		if totalLatency.Valid {
			avgLatency = float64(totalLatency.Int64) / float64(recentVerifications)
		}
	}

	return &types.NodeStats{
		NodeID:             nodeID,
		TotalPoints:        totalPoints,
		TotalUptimeMinutes: totalUptimeMinutes,
		TotalUptimeHours:   float64(totalUptimeMinutes) / 60.0,
		ChallengePassRate:  passRate,
		AverageLatencyMs:   avgLatency,
		CheatStatus:        types.CheatStatus(cheatStatus),
		WarningCount:       warningCount,
	}
}

// GetWalletStats aggregates totals across every node owned by a wallet.
// Returns nil if the wallet owns no nodes.
func (s *SQLiteStore) GetWalletStats(walletAddress string) *types.WalletStats {
	ctx := s.ctx
	var (
		totalNodes   sql.NullInt64
		totalPoints  sql.NullInt64
		activeNodes  sql.NullInt64
		flaggedNodes sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(total_points), 0),
			COALESCE(SUM(CASE WHEN is_active = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN cheat_status IN ('warning','flagged') THEN 1 ELSE 0 END), 0)
		FROM nodes
		WHERE wallet_address = ?`,
		walletAddress,
	).Scan(&totalNodes, &totalPoints, &activeNodes, &flaggedNodes)
	if err != nil {
		slog.Default().Error("sqlite: GetWalletStats", "wallet", walletAddress, "error", err)
		return nil
	}

	if !totalNodes.Valid || totalNodes.Int64 == 0 {
		return nil
	}

	return &types.WalletStats{
		WalletAddress: walletAddress,
		TotalPoints:   uint64(totalPoints.Int64),
		TotalNodes:    int(totalNodes.Int64),
		ActiveNodes:   int(activeNodes.Int64),
		FlaggedNodes:  int(flaggedNodes.Int64),
	}
}

// AwardUptimePoints adds points proportional to NodeType.PointsPerHour() and
// increments TotalUptimeMinutes. No-op for inactive, flagged, or banned nodes.
func (s *SQLiteStore) AwardUptimePoints(nodeID string, minutesOnline uint64) {
	ctx := s.ctx

	// Read the current node state to make the same is-active/cheat-status
	// gating decisions as the in-memory store.
	var (
		nodeType    string
		isActive    int64
		cheatStatus string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT node_type, is_active, cheat_status
		FROM nodes WHERE id = ?`,
		nodeID,
	).Scan(&nodeType, &isActive, &cheatStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Default().Error("sqlite: AwardUptimePoints lookup", "node_id", nodeID, "error", err)
		return
	}
	if isActive == 0 {
		return
	}
	switch types.CheatStatus(cheatStatus) {
	case types.StatusFlagged, types.StatusBanned:
		return
	}

	pointsPerInterval := types.NodeType(nodeType).PointsPerHour() / 12
	if pointsPerInterval < 1 {
		pointsPerInterval = 1
	}
	// Rate-cap points per node per window (no-op unless enabled at startup).
	pointsPerInterval = pointscap.Allow(nodeID, pointsPerInterval)

	if _, err := s.db.ExecContext(ctx, `
		UPDATE nodes
		SET total_uptime_minutes = total_uptime_minutes + ?,
		    last_heartbeat_at = ?,
		    total_points = total_points + ?
		WHERE id = ?`,
		minutesOnline, nowMillis(), pointsPerInterval, nodeID,
	); err != nil {
		slog.Default().Error("sqlite: AwardUptimePoints update", "node_id", nodeID, "error", err)
	}
}

// SeedNode sets a node's total points and synced flag directly (admin/test seam
// for demo nodes). Returns false if the node was not found.
func (s *SQLiteStore) SeedNode(nodeID string, totalPoints, verifications, uptimeMinutes uint64, synced bool) bool {
	syncedInt := 0
	if synced {
		syncedInt = 1
	}
	res, err := s.db.ExecContext(s.ctx, `
		UPDATE nodes SET total_points = ?, total_challenges_passed = ?, total_uptime_minutes = ?, is_synced = ? WHERE id = ?`,
		totalPoints, verifications, uptimeMinutes, syncedInt, nodeID,
	)
	if err != nil {
		slog.Default().Error("sqlite: SeedNode", "node_id", nodeID, "error", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}
