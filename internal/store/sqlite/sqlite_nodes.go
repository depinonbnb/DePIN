package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/depinonbnb/depin/internal/types"
	"github.com/google/uuid"
)

// nodeColumns lists every column read by scanNode, in the SELECT order. Keep
// it in lock-step with scanNode and the INSERT in RegisterNode.
const nodeColumns = `
	id, wallet_address, node_type, verification_method,
	rpc_endpoint, auth_token,
	registered_at, last_verified_at, last_heartbeat_at,
	total_challenges_passed, total_challenges_failed,
	total_uptime_minutes, total_points,
	is_active, cheat_status, warning_count, cheat_reason
`

// nowMillis returns time.Now().UnixMilli(). Centralized so tests could swap it
// later if we ever needed to fake time.
func nowMillis() int64 { return time.Now().UnixMilli() }

// scanNode reads the column set described by nodeColumns from a single row.
// Suspicious events are NOT included in this scan; callers should hydrate them
// separately via loadSuspiciousEvents.
func scanNode(row interface {
	Scan(dest ...any) error
}) (*types.NodeRegistration, error) {
	var (
		n        types.NodeRegistration
		isActive int64
	)
	if err := row.Scan(
		&n.ID, &n.WalletAddress, &n.NodeType, &n.VerificationMethod,
		&n.RPCEndpoint, &n.AuthToken,
		&n.RegisteredAt, &n.LastVerifiedAt, &n.LastHeartbeatAt,
		&n.TotalChallengesPassed, &n.TotalChallengesFailed,
		&n.TotalUptimeMinutes, &n.TotalPoints,
		&isActive, &n.CheatStatus, &n.WarningCount, &n.CheatReason,
	); err != nil {
		return nil, err
	}
	n.IsActive = isActive != 0
	return &n, nil
}

// loadSuspiciousEvents pulls the most recent 20 events for a node and formats
// them to match the in-memory store's "YYYY-MM-DD HH:MM: <event>" wire shape.
func (s *SQLiteStore) loadSuspiciousEvents(ctx context.Context, nodeID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT event, ts
		FROM suspicious_events
		WHERE node_id = ?
		ORDER BY ts DESC
		LIMIT 20`,
		nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("load suspicious events: %w", err)
	}
	defer rows.Close()

	type entry struct {
		event string
		ts    int64
	}
	collected := make([]entry, 0, 20)
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.event, &e.ts); err != nil {
			return nil, fmt.Errorf("scan suspicious event: %w", err)
		}
		collected = append(collected, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate suspicious events: %w", err)
	}

	// We pulled DESC for the LIMIT to grab the newest 20; reverse so the
	// returned slice is oldest-first, matching the in-memory implementation.
	out := make([]string, len(collected))
	for i, e := range collected {
		out[len(collected)-1-i] = time.UnixMilli(e.ts).Format("2006-01-02 15:04") + ": " + e.event
	}
	return out, nil
}

// RegisterNode inserts a new node row and returns the populated
// NodeRegistration. SuspiciousEvents is initialized to a non-nil empty slice.
func (s *SQLiteStore) RegisterNode(walletAddress string, nodeType types.NodeType, method types.VerificationMethod, rpcEndpoint, authToken string) *types.NodeRegistration {
	ctx := s.ctx
	now := nowMillis()
	bonus := nodeType.RegistrationBonus()

	node := &types.NodeRegistration{
		ID:                 uuid.New().String(),
		WalletAddress:      walletAddress,
		NodeType:           nodeType,
		VerificationMethod: method,
		RPCEndpoint:        rpcEndpoint,
		AuthToken:          authToken,
		RegisteredAt:       now,
		IsActive:           true,
		TotalPoints:        bonus,
		TotalUptimeMinutes: 0,
		CheatStatus:        types.StatusClean,
		WarningCount:       0,
		SuspiciousEvents:   []string{},
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO nodes (
			id, wallet_address, node_type, verification_method,
			rpc_endpoint, auth_token,
			registered_at, last_verified_at, last_heartbeat_at,
			total_challenges_passed, total_challenges_failed,
			total_uptime_minutes, total_points,
			is_active, cheat_status, warning_count, cheat_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0, ?, 1, ?, 0, '')`,
		node.ID, node.WalletAddress, string(node.NodeType), string(node.VerificationMethod),
		node.RPCEndpoint, node.AuthToken,
		node.RegisteredAt,
		node.TotalPoints,
		string(node.CheatStatus),
	)
	if err != nil {
		// The interface signature is non-erroring (matches the in-memory
		// store). Log loudly so operators see the failure; return nil so the
		// caller's nil check fires.
		slog.Default().Error("sqlite: RegisterNode failed", "wallet", walletAddress, "error", err)
		return nil
	}

	return node
}

// GetNode returns the node with the given ID, or nil if not found.
func (s *SQLiteStore) GetNode(nodeID string) *types.NodeRegistration {
	ctx := s.ctx
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, nodeID)

	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		slog.Default().Error("sqlite: GetNode failed", "node_id", nodeID, "error", err)
		return nil
	}

	events, err := s.loadSuspiciousEvents(ctx, nodeID)
	if err != nil {
		slog.Default().Error("sqlite: GetNode events failed", "node_id", nodeID, "error", err)
		return nil
	}
	node.SuspiciousEvents = events
	return node
}

// GetNodesByWallet returns every node owned by the wallet, in registration order.
func (s *SQLiteStore) GetNodesByWallet(walletAddress string) []*types.NodeRegistration {
	ctx := s.ctx
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeColumns+`
		FROM nodes
		WHERE wallet_address = ?
		ORDER BY registered_at ASC, id ASC`,
		walletAddress,
	)
	if err != nil {
		slog.Default().Error("sqlite: GetNodesByWallet failed", "wallet", walletAddress, "error", err)
		return []*types.NodeRegistration{}
	}
	defer rows.Close()

	out := make([]*types.NodeRegistration, 0)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			slog.Default().Error("sqlite: scan in GetNodesByWallet", "error", err)
			return out
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		slog.Default().Error("sqlite: iterate in GetNodesByWallet", "error", err)
		return out
	}
	for _, n := range out {
		events, err := s.loadSuspiciousEvents(ctx, n.ID)
		if err != nil {
			slog.Default().Error("sqlite: events in GetNodesByWallet", "node_id", n.ID, "error", err)
			continue
		}
		n.SuspiciousEvents = events
	}
	return out
}

// GetAllActiveNodes returns every node where IsActive = true.
func (s *SQLiteStore) GetAllActiveNodes() []*types.NodeRegistration {
	ctx := s.ctx
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeColumns+`
		FROM nodes
		WHERE is_active = 1
		ORDER BY registered_at ASC, id ASC`,
	)
	if err != nil {
		slog.Default().Error("sqlite: GetAllActiveNodes failed", "error", err)
		return []*types.NodeRegistration{}
	}
	defer rows.Close()

	out := make([]*types.NodeRegistration, 0)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			slog.Default().Error("sqlite: scan in GetAllActiveNodes", "error", err)
			return out
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		slog.Default().Error("sqlite: iterate in GetAllActiveNodes", "error", err)
		return out
	}
	for _, n := range out {
		events, err := s.loadSuspiciousEvents(ctx, n.ID)
		if err != nil {
			slog.Default().Error("sqlite: events in GetAllActiveNodes", "node_id", n.ID, "error", err)
			continue
		}
		n.SuspiciousEvents = events
	}
	return out
}

// GetFlaggedNodes returns every node whose CheatStatus is Warning or Flagged.
func (s *SQLiteStore) GetFlaggedNodes() []*types.NodeRegistration {
	ctx := s.ctx
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeColumns+`
		FROM nodes
		WHERE cheat_status IN ('warning', 'flagged')
		ORDER BY registered_at ASC, id ASC`,
	)
	if err != nil {
		slog.Default().Error("sqlite: GetFlaggedNodes failed", "error", err)
		return []*types.NodeRegistration{}
	}
	defer rows.Close()

	out := make([]*types.NodeRegistration, 0)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			slog.Default().Error("sqlite: scan in GetFlaggedNodes", "error", err)
			return out
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		slog.Default().Error("sqlite: iterate in GetFlaggedNodes", "error", err)
		return out
	}
	for _, n := range out {
		events, err := s.loadSuspiciousEvents(ctx, n.ID)
		if err != nil {
			slog.Default().Error("sqlite: events in GetFlaggedNodes", "node_id", n.ID, "error", err)
			continue
		}
		n.SuspiciousEvents = events
	}
	return out
}

// UpdateNode loads the node, applies the mutator, and writes the result back
// inside a single BEGIN IMMEDIATE transaction. SuspiciousEvents is hydrated
// before the mutator runs and re-persisted (full replace) afterwards so admin
// flows that clear the slice behave the same as the in-memory store.
func (s *SQLiteStore) UpdateNode(nodeID string, updates func(*types.NodeRegistration)) *types.NodeRegistration {
	if updates == nil {
		return s.GetNode(nodeID)
	}
	ctx := s.ctx

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Default().Error("sqlite: UpdateNode begin", "node_id", nodeID, "error", err)
		return nil
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, nodeID)
	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		slog.Default().Error("sqlite: UpdateNode scan", "node_id", nodeID, "error", err)
		return nil
	}

	beforeEvents, err := s.loadSuspiciousEventsTx(ctx, tx, nodeID)
	if err != nil {
		slog.Default().Error("sqlite: UpdateNode events", "node_id", nodeID, "error", err)
		return nil
	}
	node.SuspiciousEvents = beforeEvents

	updates(node)

	if err := persistNodeTx(ctx, tx, node); err != nil {
		slog.Default().Error("sqlite: UpdateNode persist", "node_id", nodeID, "error", err)
		return nil
	}
	if err := replaceSuspiciousEventsTx(ctx, tx, nodeID, node.SuspiciousEvents); err != nil {
		slog.Default().Error("sqlite: UpdateNode events persist", "node_id", nodeID, "error", err)
		return nil
	}
	if err := tx.Commit(); err != nil {
		slog.Default().Error("sqlite: UpdateNode commit", "node_id", nodeID, "error", err)
		return nil
	}
	return node
}

// SetNodeCheatStatus is an admin action. Returns false if the node does not
// exist. Cleared status resets warning_count to 0 and removes all
// suspicious_events; banned status sets is_active = 0.
func (s *SQLiteStore) SetNodeCheatStatus(nodeID string, status types.CheatStatus, reason string) bool {
	ctx := s.ctx
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Default().Error("sqlite: SetNodeCheatStatus begin", "node_id", nodeID, "error", err)
		return false
	}
	defer func() { _ = tx.Rollback() }()

	// Verify the node exists; matches the bool-returning contract.
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ?`, nodeID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		slog.Default().Error("sqlite: SetNodeCheatStatus exists check", "node_id", nodeID, "error", err)
		return false
	}

	switch status {
	case types.StatusClean:
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET cheat_status = ?, cheat_reason = ?, warning_count = 0
			WHERE id = ?`,
			string(status), reason, nodeID,
		); err != nil {
			slog.Default().Error("sqlite: SetNodeCheatStatus clear", "node_id", nodeID, "error", err)
			return false
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM suspicious_events WHERE node_id = ?`, nodeID); err != nil {
			slog.Default().Error("sqlite: SetNodeCheatStatus clear events", "node_id", nodeID, "error", err)
			return false
		}
	case types.StatusBanned:
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET cheat_status = ?, cheat_reason = ?, is_active = 0
			WHERE id = ?`,
			string(status), reason, nodeID,
		); err != nil {
			slog.Default().Error("sqlite: SetNodeCheatStatus ban", "node_id", nodeID, "error", err)
			return false
		}
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET cheat_status = ?, cheat_reason = ?
			WHERE id = ?`,
			string(status), reason, nodeID,
		); err != nil {
			slog.Default().Error("sqlite: SetNodeCheatStatus update", "node_id", nodeID, "error", err)
			return false
		}
	}

	// Audit trail
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO admin_actions (node_id, action, reason, ts, admin_subject)
		VALUES (?, ?, ?, ?, 'system')`,
		nodeID, string(status), reason, nowMillis(),
	); err != nil {
		slog.Default().Error("sqlite: SetNodeCheatStatus audit", "node_id", nodeID, "error", err)
		return false
	}

	if err := tx.Commit(); err != nil {
		slog.Default().Error("sqlite: SetNodeCheatStatus commit", "node_id", nodeID, "error", err)
		return false
	}
	return true
}

// persistNodeTx writes every mutable column of a NodeRegistration back to the
// nodes table inside the supplied transaction. It does NOT touch
// suspicious_events; callers should use replaceSuspiciousEventsTx for that.
func persistNodeTx(ctx context.Context, tx *sql.Tx, n *types.NodeRegistration) error {
	isActive := 0
	if n.IsActive {
		isActive = 1
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes SET
			wallet_address          = ?,
			node_type               = ?,
			verification_method     = ?,
			rpc_endpoint            = ?,
			auth_token              = ?,
			registered_at           = ?,
			last_verified_at        = ?,
			last_heartbeat_at       = ?,
			total_challenges_passed = ?,
			total_challenges_failed = ?,
			total_uptime_minutes    = ?,
			total_points            = ?,
			is_active               = ?,
			cheat_status            = ?,
			warning_count           = ?,
			cheat_reason            = ?
		WHERE id = ?`,
		n.WalletAddress, string(n.NodeType), string(n.VerificationMethod),
		n.RPCEndpoint, n.AuthToken,
		n.RegisteredAt, n.LastVerifiedAt, n.LastHeartbeatAt,
		n.TotalChallengesPassed, n.TotalChallengesFailed,
		n.TotalUptimeMinutes, n.TotalPoints,
		isActive, string(n.CheatStatus), n.WarningCount, n.CheatReason,
		n.ID,
	); err != nil {
		return fmt.Errorf("persist node: %w", err)
	}
	return nil
}
