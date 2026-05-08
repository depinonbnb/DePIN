// Package sqlite provides a persistent implementation of the store.Store
// interface backed by SQLite (via the pure-Go modernc.org/sqlite driver).
//
// See docs/design/persistence.md for the full design and docs/adr/0006-sqlite-mvp.md
// for the rationale behind the technology choice. The migration runner and
// background pruner live in this file; per-domain SQL lives in sqlite_*.go.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/depinonbnb/depin/internal/store"

	// Register modernc.org/sqlite under the driver name "sqlite".
	_ "modernc.org/sqlite"
)

// Compile-time assertion that *SQLiteStore satisfies store.Store.
var _ store.Store = (*SQLiteStore)(nil)

//go:embed migrations/*.sql
var migrationFS embed.FS

// pruneInterval controls how often the retention pruner sweeps. Exposed as a
// package-level var (not a const) so tests can reduce it without exporting
// internals.
var pruneInterval = 10 * time.Minute

// SQLiteStore is the database/sql-backed implementation of store.Store.
// It is safe for concurrent use; multi-statement writes are wrapped in
// BEGIN IMMEDIATE transactions per design doc §4.
type SQLiteStore struct {
	db     *sql.DB
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

// NewSQLite opens (or creates) the SQLite database at dsn, applies any
// outstanding migrations, and starts the background pruner. The caller must
// invoke Close() to release the DB handle and stop the pruner.
//
// The dsn must use modernc.org/sqlite query syntax, e.g.:
//
//	file:./data/depin.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)
//
// If the dsn refers to a file under a directory that does not yet exist, the
// directory is created with mode 0o755.
func NewSQLite(dsn string) (*SQLiteStore, error) {
	if dir := dsnDirectory(dsn); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlite: create data directory %q: %w", dir, err)
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	// Pool sizing per design doc §4. WAL allows many readers; one writer.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &SQLiteStore{
		db:     db,
		ctx:    ctx,
		cancel: cancel,
	}

	if err := s.applyMigrations(ctx); err != nil {
		cancel()
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: migrations: %w", err)
	}

	s.wg.Add(1)
	go s.pruneLoop()

	return s, nil
}

// Close cancels the prune goroutine, waits for it to exit, and closes the
// underlying DB handle. Safe to call multiple times; only the first call
// performs work.
func (s *SQLiteStore) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// dsnDirectory returns the parent directory of the database file referenced by
// the dsn, or "" if no on-disk file is implied (e.g. ":memory:" databases).
func dsnDirectory(dsn string) string {
	// Strip the "file:" prefix if present.
	path := strings.TrimPrefix(dsn, "file:")
	// Strip any query string.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	// In-memory databases or empty paths have no parent directory.
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return ""
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return ""
	}
	return dir
}

// applyMigrations applies every embedded migration not yet recorded in
// schema_migrations, in lexical order. Each migration runs inside its own
// transaction; a failure rolls back and aborts startup.
func (s *SQLiteStore) applyMigrations(ctx context.Context) error {
	// Bootstrap the migrations table itself.
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		) STRICT
	`); err != nil {
		return fmt.Errorf("bootstrap schema_migrations: %w", err)
	}

	applied, err := s.loadAppliedMigrations(ctx)
	if err != nil {
		return err
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if _, ok := applied[version]; ok {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		if err := s.runMigration(ctx, version, name, string(body)); err != nil {
			return err
		}
		slog.Default().Info("sqlite: applied migration", "version", version, "file", name)
	}

	return nil
}

func (s *SQLiteStore) loadAppliedMigrations(ctx context.Context) (map[int]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("load applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func (s *SQLiteStore) runMigration(ctx context.Context, version int, name, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("exec migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		version, time.Now().UnixMilli(),
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

// migrationVersion parses the leading integer from a filename like
// "001_initial.sql" and returns it as an int.
func migrationVersion(name string) (int, error) {
	prefix := name
	if i := strings.IndexByte(name, '_'); i >= 0 {
		prefix = name[:i]
	} else if i := strings.IndexByte(name, '.'); i >= 0 {
		prefix = name[:i]
	}
	v := 0
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("migration filename %q does not start with a numeric version", name)
		}
		v = v*10 + int(r-'0')
	}
	return v, nil
}

// pruneLoop runs prune() every pruneInterval until the context is cancelled.
// Owned by NewSQLite; signalled to stop by Close().
func (s *SQLiteStore) pruneLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.prune(s.ctx); err != nil {
				slog.Default().Warn("sqlite: prune failed", "error", err)
			}
		}
	}
}
