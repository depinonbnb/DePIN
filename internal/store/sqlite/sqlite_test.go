// Package sqlite_test runs the cross-implementation conformance suite from
// internal/store against the SQLite-backed store, plus a smoke test that
// confirms migrations apply idempotently and survive a re-open.
package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/store/sqlite"

	// Pull modernc.org/sqlite into the dependency graph for the raw
	// sql.Open() call in TestSQLiteSmokeReopen below.
	_ "modernc.org/sqlite"
)

// testDSN constructs a per-test temp-file SQLite DSN with the same pragmas the
// production server uses (see cmd/server/main.go). _txlock=immediate is what
// gives BEGIN IMMEDIATE semantics on every transaction.
func testDSN(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	path := filepath.Join(dir, "test.db")
	return "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_txlock=immediate"
}

// TestSQLiteStoreConformance exercises every Store interface method against
// the SQLite implementation. Each subtest gets its own temp-dir database via
// testDSN so the schema is freshly migrated and there is no cross-talk.
func TestSQLiteStoreConformance(t *testing.T) {
	store.Suite(t, func(tb testing.TB) store.Store {
		dsn := testDSN(tb)
		s, err := sqlite.NewSQLite(dsn)
		if err != nil {
			tb.Fatalf("NewSQLite: %v", err)
		}
		// Some Suite subtests intentionally call Close() themselves; Close is
		// safe to call multiple times so this Cleanup is a belt-and-braces
		// guard against test panics leaving handles open.
		tb.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// TestSQLiteSmokeMigrationsApply verifies that opening a fresh DB applies
// migrations and records them in schema_migrations. The conformance suite
// covers the higher-level Store contract; this is the lower-level structural
// check that the bootstrap path actually ran.
func TestSQLiteSmokeMigrationsApply(t *testing.T) {
	dsn := testDSN(t)

	s, err := sqlite.NewSQLite(dsn)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Pop open a separate handle to inspect schema_migrations directly. Sharing
	// the file with the live Store is fine — WAL mode permits concurrent
	// readers.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()

	var migrationCount int
	if err := raw.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if migrationCount == 0 {
		t.Errorf("expected at least one applied migration, got %d", migrationCount)
	}

	// Validate that the canonical tables actually exist by issuing a trivial
	// SELECT against each. If any of these fail, the migration was a no-op.
	for _, table := range []string{
		"nodes",
		"verification_results",
		"heartbeats",
		"pending_challenges",
		"suspicious_events",
		"admin_actions",
		"nonces_seen",
		"snapshots",
		"snapshot_proofs",
	} {
		if _, err := raw.ExecContext(context.Background(), `SELECT COUNT(*) FROM `+table); err != nil {
			t.Errorf("table %q missing or unreadable: %v", table, err)
		}
	}
}

// TestSQLiteSmokeReopen verifies the migration runner is idempotent: opening a
// brand-new DB applies the migrations, and re-opening the same on-disk DB
// applies nothing further (no errors, schema_migrations count unchanged).
//
// We use a tempfile rather than ":memory:" because in-memory SQLite does not
// persist across opens — that would only ever exercise the fresh-bootstrap
// path. The point of this test is the second-open path.
func TestSQLiteSmokeReopen(t *testing.T) {
	dsn := testDSN(t)

	first, err := sqlite.NewSQLite(dsn)
	if err != nil {
		t.Fatalf("first NewSQLite: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Read the migration count after the first boot.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	var firstCount int
	if err := raw.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&firstCount); err != nil {
		_ = raw.Close()
		t.Fatalf("count after first open: %v", err)
	}
	_ = raw.Close()

	// Re-open: should not error, should not apply any new migrations.
	second, err := sqlite.NewSQLite(dsn)
	if err != nil {
		t.Fatalf("second NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	raw2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open(second): %v", err)
	}
	defer raw2.Close()

	var secondCount int
	if err := raw2.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&secondCount); err != nil {
		t.Fatalf("count after second open: %v", err)
	}
	if secondCount != firstCount {
		t.Errorf("migrations re-applied on re-open: first=%d second=%d", firstCount, secondCount)
	}
}
