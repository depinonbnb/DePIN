// Package sqlite: sqlite_nonces.go implements the ConsumeNonce store method
// for the SQLite backend. The nonces_seen table is created in
// migrations/001_initial.sql; expired rows are removed by sqlite_prune.go's
// pruneExpiredNonces.
//
// The contract (see store.Store.ConsumeNonce):
//
//	ConsumeNonce(wallet, nonce, ttl) bool
//	  - returns true if (wallet, nonce) is fresh (just inserted)
//	  - returns false if a non-expired row already exists
//	  - expired rows behave as missing (so a nonce can rotate naturally)
//
// SQLite doesn't have a single statement that combines "INSERT if missing,
// otherwise UPSERT only if expired" while reporting which path ran, so we
// use BEGIN IMMEDIATE + (DELETE expired)+(INSERT OR IGNORE) + (rows affected).
// _txlock=immediate is set on the production DSN so BEGIN takes the writer
// lock up front and we don't have to retry on SQLITE_BUSY.
package sqlite

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ConsumeNonce attempts to record (wallet, nonce) with a TTL. Returns true if
// the row was newly inserted, false if a non-expired row was already present.
func (s *SQLiteStore) ConsumeNonce(wallet string, nonce string, ttl time.Duration) bool {
	ctx := s.ctx
	now := nowMillis()
	expiresAt := now + ttl.Milliseconds()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Default().Error("sqlite: ConsumeNonce begin", "wallet", wallet, "error", err)
		return false
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Step 1: evict the existing row IFF it has already expired. This lets a
	// fresh INSERT below succeed for a legitimately-rotated nonce while still
	// blocking live replays.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM nonces_seen WHERE wallet = ? AND nonce = ? AND expires_at <= ?`,
		wallet, nonce, now,
	); err != nil {
		slog.Default().Error("sqlite: ConsumeNonce evict expired", "wallet", wallet, "error", err)
		return false
	}

	// Step 2: INSERT OR IGNORE — succeeds only if no row exists for this
	// (wallet, nonce). After step 1 that's exactly the "fresh" condition.
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO nonces_seen (wallet, nonce, expires_at) VALUES (?, ?, ?)`,
		wallet, nonce, expiresAt,
	)
	if err != nil {
		slog.Default().Error("sqlite: ConsumeNonce insert", "wallet", wallet, "error", err)
		return false
	}
	rows, err := res.RowsAffected()
	if err != nil {
		// modernc.org/sqlite reports affected rows reliably; treat any error
		// here as a system fault and fail closed.
		slog.Default().Error("sqlite: ConsumeNonce rows affected", "wallet", wallet, "error", err)
		if errors.Is(err, context.Canceled) {
			return false
		}
		return false
	}

	if err := tx.Commit(); err != nil {
		slog.Default().Error("sqlite: ConsumeNonce commit", "wallet", wallet, "error", err)
		return false
	}
	committed = true

	return rows == 1
}
