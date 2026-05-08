package sqlite

import (
	"context"
	"fmt"
	"log/slog"
)

// Per-node retention caps. These match the in-memory store's behaviour so the
// SQLite backend is observationally equivalent for size-bounded reads.
const (
	verificationResultsPerNodeCap = 1000
	heartbeatsPerNodeCap          = 300
)

// prune deletes rows beyond the per-node retention caps and removes expired
// entries from the pending_challenges and nonces_seen tables. Tests may call
// this directly; the production schedule is driven by pruneLoop().
func (s *SQLiteStore) prune(ctx context.Context) error {
	nodeIDs, err := s.allNodeIDs(ctx)
	if err != nil {
		return err
	}

	logger := slog.Default()

	totalVerificationDeletes := int64(0)
	totalHeartbeatDeletes := int64(0)

	for _, id := range nodeIDs {
		n, err := s.pruneVerificationResults(ctx, id, verificationResultsPerNodeCap)
		if err != nil {
			return fmt.Errorf("prune verification_results for %s: %w", id, err)
		}
		totalVerificationDeletes += n

		m, err := s.pruneHeartbeats(ctx, id, heartbeatsPerNodeCap)
		if err != nil {
			return fmt.Errorf("prune heartbeats for %s: %w", id, err)
		}
		totalHeartbeatDeletes += m
	}

	expiredPending, err := s.pruneExpiredPendingChallenges(ctx)
	if err != nil {
		return fmt.Errorf("prune pending_challenges: %w", err)
	}

	expiredNonces, err := s.pruneExpiredNonces(ctx)
	if err != nil {
		return fmt.Errorf("prune nonces_seen: %w", err)
	}

	logger.Debug("sqlite: prune complete",
		"verification_deleted", totalVerificationDeletes,
		"heartbeats_deleted", totalHeartbeatDeletes,
		"expired_pending_deleted", expiredPending,
		"expired_nonces_deleted", expiredNonces,
	)

	return nil
}

func (s *SQLiteStore) allNodeIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("list node ids: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan node id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node ids: %w", err)
	}
	return ids, nil
}

func (s *SQLiteStore) pruneVerificationResults(ctx context.Context, nodeID string, cap int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM verification_results
		WHERE node_id = ?
		  AND id NOT IN (
		    SELECT id FROM verification_results
		    WHERE node_id = ?
		    ORDER BY ts DESC
		    LIMIT ?
		)`,
		nodeID, nodeID, cap,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (s *SQLiteStore) pruneHeartbeats(ctx context.Context, nodeID string, cap int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM heartbeats
		WHERE node_id = ?
		  AND id NOT IN (
		    SELECT id FROM heartbeats
		    WHERE node_id = ?
		    ORDER BY ts DESC
		    LIMIT ?
		)`,
		nodeID, nodeID, cap,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (s *SQLiteStore) pruneExpiredPendingChallenges(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pending_challenges WHERE expires_at < ?`,
		nowMillis(),
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (s *SQLiteStore) pruneExpiredNonces(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM nonces_seen WHERE expires_at < ?`,
		nowMillis(),
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
