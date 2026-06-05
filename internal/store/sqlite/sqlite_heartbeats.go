package sqlite

import (
	"database/sql"
	"log/slog"

	"github.com/depinonbnb/depin/internal/types"
)

// RecordHeartbeat appends a heartbeat row. Bounded retention is handled by the
// background pruner; this writer always inserts.
func (s *SQLiteStore) RecordHeartbeat(heartbeat *types.HeartbeatRecord) {
	if heartbeat == nil {
		return
	}
	ctx := s.ctx

	syncedInt := 0
	if heartbeat.IsSynced {
		syncedInt = 1
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO heartbeats (
			node_id, ts, block_number, is_synced, latency_ms, peers_count
		) VALUES (?, ?, ?, ?, ?, ?)`,
		heartbeat.NodeID, heartbeat.Timestamp, heartbeat.BlockNumber,
		syncedInt, heartbeat.LatencyMs, heartbeat.PeersCount,
	); err != nil {
		slog.Default().Error("sqlite: RecordHeartbeat failed", "node_id", heartbeat.NodeID, "error", err)
		return
	}

	// Reflect the latest sync state on the node so the API can surface it.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET last_heartbeat_at = ?, is_synced = ? WHERE id = ?`,
		heartbeat.Timestamp, syncedInt, heartbeat.NodeID,
	); err != nil {
		slog.Default().Error("sqlite: RecordHeartbeat node update failed", "node_id", heartbeat.NodeID, "error", err)
	}
}

// GetHeartbeats returns retained heartbeats since the given unix-ms timestamp,
// in ascending time order. since == 0 means "all retained heartbeats".
func (s *SQLiteStore) GetHeartbeats(nodeID string, since int64) []*types.HeartbeatRecord {
	ctx := s.ctx

	var (
		rows *sql.Rows
		err  error
	)
	if since == 0 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT node_id, ts, block_number, is_synced, latency_ms, peers_count
			FROM heartbeats
			WHERE node_id = ?
			ORDER BY ts ASC, id ASC`,
			nodeID,
		)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT node_id, ts, block_number, is_synced, latency_ms, peers_count
			FROM heartbeats
			WHERE node_id = ? AND ts >= ?
			ORDER BY ts ASC, id ASC`,
			nodeID, since,
		)
	}
	if err != nil {
		slog.Default().Error("sqlite: GetHeartbeats failed", "node_id", nodeID, "error", err)
		return []*types.HeartbeatRecord{}
	}
	defer rows.Close()

	out := make([]*types.HeartbeatRecord, 0)
	for rows.Next() {
		var (
			h        types.HeartbeatRecord
			syncedIn int64
		)
		if err := rows.Scan(
			&h.NodeID, &h.Timestamp, &h.BlockNumber, &syncedIn, &h.LatencyMs, &h.PeersCount,
		); err != nil {
			slog.Default().Error("sqlite: scan heartbeat", "node_id", nodeID, "error", err)
			return out
		}
		h.IsSynced = syncedIn != 0
		out = append(out, &h)
	}
	if err := rows.Err(); err != nil {
		slog.Default().Error("sqlite: iterate heartbeats", "node_id", nodeID, "error", err)
		return out
	}
	return out
}
