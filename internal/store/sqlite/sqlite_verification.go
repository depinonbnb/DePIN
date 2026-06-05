package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/depinonbnb/depin/internal/metrics"
	"github.com/depinonbnb/depin/internal/types"
)

// suspiciousEventPrefixLen is the length of "YYYY-MM-DD HH:MM: ", the prefix
// used by both stores when formatting suspicious events for the wire.
const suspiciousEventPrefixLen = len("2006-01-02 15:04: ")

// loadSuspiciousEventsTx mirrors loadSuspiciousEvents but runs inside an open
// transaction, which is necessary for UpdateNode's read-modify-write flow.
func (s *SQLiteStore) loadSuspiciousEventsTx(ctx context.Context, tx *sql.Tx, nodeID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
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
	out := make([]string, len(collected))
	for i, e := range collected {
		out[len(collected)-1-i] = time.UnixMilli(e.ts).Format("2006-01-02 15:04") + ": " + e.event
	}
	return out, nil
}

// replaceSuspiciousEventsTx wipes and re-inserts the suspicious_events for a
// node based on the supplied formatted slice. We strip the "YYYY-MM-DD HH:MM: "
// prefix added by callers and parse the timestamp back out so the table stays
// structured. Events that don't parse fall back to "now" with the full string
// preserved verbatim.
func replaceSuspiciousEventsTx(ctx context.Context, tx *sql.Tx, nodeID string, events []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM suspicious_events WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("clear suspicious events: %w", err)
	}
	if len(events) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO suspicious_events (node_id, event, ts) VALUES (?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare insert suspicious event: %w", err)
	}
	defer stmt.Close()

	now := nowMillis()
	for _, raw := range events {
		ts, body := splitFormattedSuspiciousEvent(raw, now)
		if _, err := stmt.ExecContext(ctx, nodeID, body, ts); err != nil {
			return fmt.Errorf("insert suspicious event: %w", err)
		}
	}
	return nil
}

// splitFormattedSuspiciousEvent parses the "YYYY-MM-DD HH:MM: <body>" wire
// format. If the prefix is missing or invalid, defaultTs is used and the full
// string is returned as the body.
func splitFormattedSuspiciousEvent(raw string, defaultTs int64) (int64, string) {
	if len(raw) < suspiciousEventPrefixLen {
		return defaultTs, raw
	}
	tsStr := raw[:len("2006-01-02 15:04")]
	t, err := time.ParseInLocation("2006-01-02 15:04", tsStr, time.Local)
	if err != nil {
		return defaultTs, raw
	}
	body := strings.TrimPrefix(raw[len("2006-01-02 15:04"):], ": ")
	return t.UnixMilli(), body
}

// RecordVerificationResult appends a row to verification_results, updates the
// node's pass/fail counters and last_verified_at, and applies suspicious-event
// escalation. All inside a single BEGIN IMMEDIATE transaction.
func (s *SQLiteStore) RecordVerificationResult(result *types.VerificationResult) {
	if result == nil {
		return
	}
	ctx := s.ctx

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Default().Error("sqlite: RecordVerificationResult begin", "error", err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	passedInt := 0
	if result.Passed {
		passedInt = 1
	}
	suspiciousInt := 0
	if result.Suspicious {
		suspiciousInt = 1
	}

	// Verify the node exists so we can match the in-memory store's "skip
	// counter updates if the node is gone" semantics. Without this guard the
	// FK constraint on verification_results would surface as an error rather
	// than a silent no-op.
	var nodeTypeStr string
	if err := tx.QueryRowContext(ctx, `SELECT node_type FROM nodes WHERE id = ?`, result.NodeID).Scan(&nodeTypeStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No node — drop the row to mirror the in-memory store's
			// "if ok" guard (which silently skipped both history append and
			// counter updates). Commit a no-op transaction.
			if err := tx.Commit(); err != nil {
				slog.Default().Error("sqlite: commit no-op verification", "error", err)
			} else {
				committed = true
			}
			return
		}
		slog.Default().Error("sqlite: load node for verification", "node_id", result.NodeID, "error", err)
		return
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO verification_results (
			node_id, challenge_id, passed, response_time_ms,
			failure_reason, suspicious, suspicious_note, ts
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		result.NodeID, result.ChallengeID, passedInt, result.ResponseTimeMs,
		result.FailureReason, suspiciousInt, result.SuspiciousNote, result.Timestamp,
	); err != nil {
		slog.Default().Error("sqlite: insert verification_result", "node_id", result.NodeID, "error", err)
		return
	}

	if result.Passed {
		challengePoints := types.NodeType(nodeTypeStr).PointsPerChallenge()
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes SET
				total_challenges_passed = total_challenges_passed + 1,
				total_points = total_points + ?,
				last_verified_at = ?
			WHERE id = ?`,
			challengePoints, result.Timestamp, result.NodeID,
		); err != nil {
			slog.Default().Error("sqlite: bump pass counter", "node_id", result.NodeID, "error", err)
			return
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes SET
				total_challenges_failed = total_challenges_failed + 1,
				last_verified_at = ?
			WHERE id = ?`,
			result.Timestamp, result.NodeID,
		); err != nil {
			slog.Default().Error("sqlite: bump fail counter", "node_id", result.NodeID, "error", err)
			return
		}
	}

	if result.Suspicious {
		if err := applySuspiciousEscalationTx(ctx, tx, result.NodeID, result.SuspiciousNote, "Suspicious verification detected"); err != nil {
			slog.Default().Error("sqlite: escalate verification", "node_id", result.NodeID, "error", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Default().Error("sqlite: commit verification", "node_id", result.NodeID, "error", err)
		return
	}
	committed = true
}

// AddSuspiciousEvent records a structured event and re-applies escalation
// rules. Mirrors RecordVerificationResult's escalation path.
func (s *SQLiteStore) AddSuspiciousEvent(nodeID string, reason string) {
	// Counter is incremented up front so missing-node calls (which bail
	// silently below) are still observable as "missing_node".
	metrics.SuspiciousEventsTotal.WithLabelValues(metrics.BucketSuspiciousReason(reason)).Inc()

	ctx := s.ctx
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Default().Error("sqlite: AddSuspiciousEvent begin", "node_id", nodeID, "error", err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Skip if the node doesn't exist (matches the in-memory store's guard).
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ?`, nodeID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		slog.Default().Error("sqlite: AddSuspiciousEvent exists", "node_id", nodeID, "error", err)
		return
	}

	if err := applySuspiciousEscalationTx(ctx, tx, nodeID, reason, ""); err != nil {
		slog.Default().Error("sqlite: AddSuspiciousEvent escalate", "node_id", nodeID, "error", err)
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Default().Error("sqlite: AddSuspiciousEvent commit", "node_id", nodeID, "error", err)
		return
	}
	committed = true
}

// applySuspiciousEscalationTx inserts a suspicious_events row, increments the
// node's warning_count, prunes the per-node event window to 20, and updates
// cheat_status / cheat_reason per the same thresholds the in-memory store uses
// (warning at >=2, flagged at >=5).
//
// reason is the caller-supplied reason. fallback is used if reason is empty
// (matches the in-memory "Suspicious verification detected" default).
func applySuspiciousEscalationTx(ctx context.Context, tx *sql.Tx, nodeID, reason, fallback string) error {
	body := reason
	if body == "" {
		body = fallback
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO suspicious_events (node_id, event, ts) VALUES (?, ?, ?)`,
		nodeID, body, now,
	); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	// Trim oldest events to keep at most 20 per node.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM suspicious_events
		WHERE node_id = ?
		  AND id NOT IN (
		    SELECT id FROM suspicious_events
		    WHERE node_id = ?
		    ORDER BY ts DESC, id DESC
		    LIMIT 20
		)`,
		nodeID, nodeID,
	); err != nil {
		return fmt.Errorf("trim events: %w", err)
	}

	// Bump warning_count and re-fetch to apply thresholds.
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET warning_count = warning_count + 1 WHERE id = ?`,
		nodeID,
	); err != nil {
		return fmt.Errorf("bump warning: %w", err)
	}

	var warningCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT warning_count FROM nodes WHERE id = ?`,
		nodeID,
	).Scan(&warningCount); err != nil {
		return fmt.Errorf("read warning_count: %w", err)
	}

	switch {
	case warningCount >= 5:
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET cheat_status = ?, cheat_reason = ?
			WHERE id = ?`,
			string(types.StatusFlagged),
			"Multiple suspicious activities - needs manual review",
			nodeID,
		); err != nil {
			return fmt.Errorf("escalate to flagged: %w", err)
		}
	case warningCount >= 2:
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET cheat_status = ?, cheat_reason = ?
			WHERE id = ?`,
			string(types.StatusWarning),
			body,
			nodeID,
		); err != nil {
			return fmt.Errorf("escalate to warning: %w", err)
		}
	}

	return nil
}

// GetVerificationHistory returns up to `limit` most-recent verification
// results, oldest-first.
func (s *SQLiteStore) GetVerificationHistory(nodeID string, limit int) []*types.VerificationResult {
	if limit <= 0 {
		return []*types.VerificationResult{}
	}
	ctx := s.ctx
	rows, err := s.db.QueryContext(ctx, `
		SELECT challenge_id, node_id, passed, response_time_ms,
		       failure_reason, suspicious, suspicious_note, ts
		FROM verification_results
		WHERE node_id = ?
		ORDER BY ts DESC, id DESC
		LIMIT ?`,
		nodeID, limit,
	)
	if err != nil {
		slog.Default().Error("sqlite: GetVerificationHistory failed", "node_id", nodeID, "error", err)
		return []*types.VerificationResult{}
	}
	defer rows.Close()

	collected := make([]*types.VerificationResult, 0, limit)
	for rows.Next() {
		var (
			r                    types.VerificationResult
			passed, suspiciousIn int64
		)
		if err := rows.Scan(
			&r.ChallengeID, &r.NodeID, &passed, &r.ResponseTimeMs,
			&r.FailureReason, &suspiciousIn, &r.SuspiciousNote, &r.Timestamp,
		); err != nil {
			slog.Default().Error("sqlite: scan verification history", "node_id", nodeID, "error", err)
			return collected
		}
		r.Passed = passed != 0
		r.Suspicious = suspiciousIn != 0
		collected = append(collected, &r)
	}
	if err := rows.Err(); err != nil {
		slog.Default().Error("sqlite: iterate verification history", "node_id", nodeID, "error", err)
		return collected
	}

	// We pulled DESC for the LIMIT; reverse to oldest-first.
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}
	return collected
}
