// Package sqlite (white-box test): verifies the retention pruner deletes the
// right rows and keeps the most recent ones, plus the expiry cleanup paths
// for pending_challenges and nonces_seen.
//
// White-box gives us direct access to the unexported prune(ctx) method so we
// can drive it synchronously instead of waiting for the 10-minute ticker.
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// pruneTestDSN mirrors the helper in sqlite_test.go but lives here so the
// white-box prune test does not depend on the external test package.
func pruneTestDSN(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	path := filepath.Join(dir, "prune.db")
	return "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_txlock=immediate"
}

// newTestStore opens a SQLiteStore for use inside this package's tests.
// Because we are in package sqlite (not sqlite_test) we touch the unexported
// type directly.
func newPruneTestStore(tb testing.TB) *SQLiteStore {
	tb.Helper()
	s, err := NewSQLite(pruneTestDSN(tb))
	if err != nil {
		tb.Fatalf("NewSQLite: %v", err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	return s
}

// insertVerificationRow is a focused helper that bypasses
// RecordVerificationResult so we can plant exactly the rows we need without
// triggering the side-effects (counter bumps, escalation) that
// RecordVerificationResult performs.
func insertVerificationRow(t *testing.T, s *SQLiteStore, nodeID, challengeID string, ts int64) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO verification_results (node_id, challenge_id, passed, response_time_ms, failure_reason, suspicious, suspicious_note, ts)
		VALUES (?, ?, 1, 0, '', 0, '', ?)`,
		nodeID, challengeID, ts,
	); err != nil {
		t.Fatalf("insert verification_results: %v", err)
	}
}

func insertHeartbeatRow(t *testing.T, s *SQLiteStore, nodeID string, ts int64) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO heartbeats (node_id, ts, block_number, is_synced, latency_ms, peers_count)
		VALUES (?, ?, 0, 0, 0, 0)`,
		nodeID, ts,
	); err != nil {
		t.Fatalf("insert heartbeats: %v", err)
	}
}

// TestPruneVerificationResultsCap inserts > cap rows for one node, runs prune,
// and verifies that exactly verificationResultsPerNodeCap rows remain — the
// most recent ones (highest ts).
func TestPruneVerificationResultsCap(t *testing.T) {
	s := newPruneTestStore(t)
	ctx := context.Background()

	// Register a node so the FK constraint is satisfied. Bypass the public
	// RegisterNode by using a known UUID — same effect, fewer moving parts.
	const nodeID = "prune-node-1"
	if _, err := s.db.Exec(`
		INSERT INTO nodes (id, wallet_address, node_type, verification_method, registered_at)
		VALUES (?, '0xtest', 'bsc-full', 'local-prover', 0)`,
		nodeID,
	); err != nil {
		t.Fatalf("insert node: %v", err)
	}

	// Insert cap+50 rows with monotonically increasing timestamps. After prune,
	// only the rows with the highest cap timestamps should survive.
	const insertCount = verificationResultsPerNodeCap + 50
	for i := 0; i < insertCount; i++ {
		insertVerificationRow(t, s, nodeID, "challenge", int64(i))
	}

	if err := s.prune(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM verification_results WHERE node_id = ?`, nodeID).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != verificationResultsPerNodeCap {
		t.Errorf("expected %d rows after prune, got %d", verificationResultsPerNodeCap, remaining)
	}

	// The retained set must be the most-recent timestamps. Smallest retained
	// ts should be insertCount - cap.
	var minTs int64
	if err := s.db.QueryRow(`SELECT MIN(ts) FROM verification_results WHERE node_id = ?`, nodeID).Scan(&minTs); err != nil {
		t.Fatalf("min ts: %v", err)
	}
	wantMin := int64(insertCount - verificationResultsPerNodeCap)
	if minTs != wantMin {
		t.Errorf("expected min retained ts=%d, got %d (oldest rows were not pruned)", wantMin, minTs)
	}
}

// TestPruneHeartbeatsCap mirrors the verification_results test for heartbeats
// (per-node cap is heartbeatsPerNodeCap=300).
func TestPruneHeartbeatsCap(t *testing.T) {
	s := newPruneTestStore(t)
	ctx := context.Background()

	const nodeID = "prune-node-2"
	if _, err := s.db.Exec(`
		INSERT INTO nodes (id, wallet_address, node_type, verification_method, registered_at)
		VALUES (?, '0xtest', 'bsc-full', 'local-prover', 0)`,
		nodeID,
	); err != nil {
		t.Fatalf("insert node: %v", err)
	}

	const insertCount = heartbeatsPerNodeCap + 50
	for i := 0; i < insertCount; i++ {
		insertHeartbeatRow(t, s, nodeID, int64(i))
	}

	if err := s.prune(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM heartbeats WHERE node_id = ?`, nodeID).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != heartbeatsPerNodeCap {
		t.Errorf("expected %d rows after prune, got %d", heartbeatsPerNodeCap, remaining)
	}

	var minTs int64
	if err := s.db.QueryRow(`SELECT MIN(ts) FROM heartbeats WHERE node_id = ?`, nodeID).Scan(&minTs); err != nil {
		t.Fatalf("min ts: %v", err)
	}
	wantMin := int64(insertCount - heartbeatsPerNodeCap)
	if minTs != wantMin {
		t.Errorf("expected min retained ts=%d, got %d", wantMin, minTs)
	}
}

// TestPruneExpiredPendingChallenges verifies that prune deletes
// pending_challenges with expires_at < now and keeps the rest.
func TestPruneExpiredPendingChallenges(t *testing.T) {
	s := newPruneTestStore(t)
	ctx := context.Background()

	const nodeID = "prune-node-3"
	if _, err := s.db.Exec(`
		INSERT INTO nodes (id, wallet_address, node_type, verification_method, registered_at)
		VALUES (?, '0xtest', 'bsc-full', 'local-prover', 0)`,
		nodeID,
	); err != nil {
		t.Fatalf("insert node: %v", err)
	}

	now := nowMillis()

	// Two expired challenges (well in the past), one live (1 hour in the
	// future).
	expired := []string{"chal-old-1", "chal-old-2"}
	for _, id := range expired {
		if _, err := s.db.Exec(`
			INSERT INTO pending_challenges (id, node_id, challenge_type, params_json, expected_answer, created_at, expires_at)
			VALUES (?, ?, 'block-hash', '{}', 'ans', ?, ?)`,
			id, nodeID, now-3600_000, now-1000,
		); err != nil {
			t.Fatalf("insert expired pending_challenge: %v", err)
		}
	}
	if _, err := s.db.Exec(`
		INSERT INTO pending_challenges (id, node_id, challenge_type, params_json, expected_answer, created_at, expires_at)
		VALUES (?, ?, 'block-hash', '{}', 'ans', ?, ?)`,
		"chal-live", nodeID, now, now+3600_000,
	); err != nil {
		t.Fatalf("insert live pending_challenge: %v", err)
	}

	if err := s.prune(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pending_challenges`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 1 {
		t.Errorf("expected 1 live pending_challenge, got %d", remaining)
	}

	var liveID string
	if err := s.db.QueryRow(`SELECT id FROM pending_challenges`).Scan(&liveID); err != nil {
		t.Fatalf("read remaining id: %v", err)
	}
	if liveID != "chal-live" {
		t.Errorf("expected chal-live to survive, got %q", liveID)
	}
}

// TestPruneExpiredNonces verifies the same shape for the nonces_seen table.
func TestPruneExpiredNonces(t *testing.T) {
	s := newPruneTestStore(t)
	ctx := context.Background()

	now := nowMillis()

	// Two expired nonces, one live.
	if _, err := s.db.Exec(`INSERT INTO nonces_seen (wallet, nonce, expires_at) VALUES (?, ?, ?)`, "0xa", "n1", now-1000); err != nil {
		t.Fatalf("insert expired nonce 1: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO nonces_seen (wallet, nonce, expires_at) VALUES (?, ?, ?)`, "0xa", "n2", now-2000); err != nil {
		t.Fatalf("insert expired nonce 2: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO nonces_seen (wallet, nonce, expires_at) VALUES (?, ?, ?)`, "0xa", "n3", now+3600_000); err != nil {
		t.Fatalf("insert live nonce: %v", err)
	}

	if err := s.prune(ctx); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nonces_seen`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining nonces: %v", err)
	}
	if remaining != 1 {
		t.Errorf("expected 1 live nonce, got %d", remaining)
	}

	var liveNonce string
	if err := s.db.QueryRow(`SELECT nonce FROM nonces_seen`).Scan(&liveNonce); err != nil {
		t.Fatalf("read remaining nonce: %v", err)
	}
	if liveNonce != "n3" {
		t.Errorf("expected n3 to survive, got %q", liveNonce)
	}
}
