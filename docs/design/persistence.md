# Persistence Layer Design — SQLite MVP

Status: design complete, ready for implementation
Implements: [ADR-0006](../adr/0006-sqlite-mvp.md)
Author: system-architect agent
Date: 2026-05-08

This document specifies the persistence layer that replaces the current in-memory `*store.Store` (`internal/store/store.go`). It is a design only — no Go code is produced here. The next agent (`persist-coder`) implements against this spec.

Hygiene check: no unexpected file changes were reported by the upstream agent. Source tree at the time of design matches the structure documented in `docs/SPEC.md` §3.

---

## 1. Store Interface

### 1.1 Caller inventory

`grep -rn "depinonbnb/depin/internal/store"` confirms only three non-test files import the store package:

- `cmd/server/main.go` — constructor (`store.NewStore()`)
- `internal/api/router.go` — passes `*store.Store` into handlers
- `internal/api/handlers.go` — every method call

The `internal/verification/verifier.go` file does **not** import the store (the brief incorrectly stated it did). The verifier is store-agnostic; handlers persist `*types.VerificationResult` and `*types.HeartbeatRecord` returned by it. This is good — it means the interface change has a smaller blast radius.

### 1.2 Methods called by handlers (verbatim from `handlers.go`)

| # | Method (line) | Call site |
|---|---|---|
| 1 | `RegisterNode(wallet, nodeType, method, rpcEndpoint, authToken)` | `handlers.go:156`, `:531` |
| 2 | `GetNode(nodeID)` | `:174`, `:240`, `:276`, `:315`, `:340` |
| 3 | `GetNodesByWallet(wallet)` | `:190` |
| 4 | `GetWalletStats(wallet)` | `:205` |
| 5 | `GetNodeStats(nodeID)` | `:218`, `:388` |
| 6 | `RecordVerificationResult(result)` | `:299`, `:328` |
| 7 | `RecordHeartbeat(heartbeat)` | `:358` |
| 8 | `GetAllActiveNodes()` | `:368`, `:425` |
| 9 | `GetFlaggedNodes()` | `:448` |
| 10 | `SetNodeCheatStatus(nodeID, status, reason)` → `bool` | `:491` |

Inspection of `store.go` shows additional public methods that **are not currently called** by handlers but exist in the API:

- `UpdateNode(nodeID, updates func(*NodeRegistration)) *NodeRegistration` — exposed but unused. Keep it: scheduler work in [ADR-0007](../adr/0007-scheduler-driven-verification.md) is likely to need a generic node mutator.
- `GetVerificationHistory(nodeID, limit) []*VerificationResult` — exposed but unused. Keep it: needed by upcoming `/nodes/:id/history` endpoint and by snapshot tooling.
- `GetHeartbeats(nodeID, since) []*HeartbeatRecord` — exposed but unused. Keep it.
- `AwardUptimePoints(nodeID, minutesOnline)` — exposed but unused. Keep it: scheduler will call it from a tick goroutine.
- `AddSuspiciousEvent(nodeID, reason)` — exposed but unused. Keep it: anti-cheat extensions in SPEC gap #4 will use it.

The brief's expected list also mentioned `NewMemory()`. The current concrete constructor is `NewStore()`. To keep the interface idiomatic and the construction-site renames small, the new interface uses two factory functions in separate sub-packages (see §7), not interface methods.

### 1.3 Proposed Go interface (signatures, godoc-style comments)

The interface lives in `internal/store/store.go`. Names and signatures are preserved exactly so `handlers.go` and `router.go` compile against the interface with no changes other than `*store.Store` → `store.Store`.

```go
// Package store defines the persistence contract used by the API and verification layers.
// Implementations live in sub-packages: store/memory (test/dev) and store/sqlite (prod).
package store

// Store is the persistence contract. Every reader and writer of node state goes through it.
type Store interface {
    // RegisterNode persists a brand-new node, awards the registration bonus, and returns the
    // populated NodeRegistration. Generates a fresh UUID. Indexes the node under its wallet.
    RegisterNode(walletAddress string, nodeType types.NodeType, method types.VerificationMethod, rpcEndpoint, authToken string) *types.NodeRegistration

    // GetNode returns the node with the given ID, or nil if not found.
    GetNode(nodeID string) *types.NodeRegistration

    // GetNodesByWallet returns every node owned by the wallet, in registration order. Empty slice if none.
    GetNodesByWallet(walletAddress string) []*types.NodeRegistration

    // GetAllActiveNodes returns every node where IsActive = true. Used for leaderboard and network stats.
    GetAllActiveNodes() []*types.NodeRegistration

    // UpdateNode applies the mutator to the node and persists the result atomically.
    // Returns the updated node, or nil if the node does not exist.
    UpdateNode(nodeID string, updates func(*types.NodeRegistration)) *types.NodeRegistration

    // RecordVerificationResult appends a result to the node's history, updates pass/fail counters,
    // sets LastVerifiedAt, and applies suspicious-event escalation (warning -> flagged) per existing rules.
    RecordVerificationResult(result *types.VerificationResult)

    // GetVerificationHistory returns the most recent `limit` results for a node, oldest-first.
    GetVerificationHistory(nodeID string, limit int) []*types.VerificationResult

    // RecordHeartbeat appends a heartbeat record. Caller is responsible for setting Timestamp.
    RecordHeartbeat(heartbeat *types.HeartbeatRecord)

    // GetHeartbeats returns heartbeats for a node since the given unix-millisecond timestamp.
    // since == 0 means "all retained heartbeats".
    GetHeartbeats(nodeID string, since int64) []*types.HeartbeatRecord

    // GetNodeStats computes derived stats (pass rate, avg latency over last 24h, uptime hours).
    // Returns nil if the node does not exist.
    GetNodeStats(nodeID string) *types.NodeStats

    // GetWalletStats aggregates totals across every node owned by a wallet.
    // Returns nil if the wallet owns no nodes.
    GetWalletStats(walletAddress string) *types.WalletStats

    // AwardUptimePoints adds points proportional to NodeType.PointsPerHour() and increments
    // TotalUptimeMinutes. No-op for inactive, flagged, or banned nodes.
    AwardUptimePoints(nodeID string, minutesOnline uint64)

    // AddSuspiciousEvent appends a structured event and applies the same warning-escalation rules
    // used by RecordVerificationResult.
    AddSuspiciousEvent(nodeID string, reason string)

    // GetFlaggedNodes returns every node whose CheatStatus is Warning or Flagged.
    GetFlaggedNodes() []*types.NodeRegistration

    // SetNodeCheatStatus is an admin action. Returns false if the node was not found.
    // Side effects: cleared status resets WarningCount and SuspiciousEvents; banned status sets IsActive=false.
    SetNodeCheatStatus(nodeID string, status types.CheatStatus, reason string) bool

    // Close releases any backing resources. Memory impl is a no-op; SQLite impl closes the DB handle
    // and stops the prune goroutine. Safe to call multiple times.
    Close() error
}
```

`Close() error` is the only addition vs. the current concrete struct. It is required for the SQLite impl and trivially implemented as `return nil` for the memory impl. Existing callers do not need to call it; `cmd/server/main.go` will gain a `defer nodeStore.Close()` (see §8).

### 1.4 Construction-site rename in handlers and router

Current:
```go
type Handlers struct { store *store.Store; ... }
func NewHandlers(store *store.Store, ...) *Handlers { ... }
func SetupRouter(store *store.Store, ...) *gin.Engine { ... }
```

After (one-character rename, no behavior change):
```go
type Handlers struct { store store.Store; ... }
func NewHandlers(store store.Store, ...) *Handlers { ... }
func SetupRouter(store store.Store, ...) *gin.Engine { ... }
```

Method-call sites (`h.store.GetNode(…)` etc.) compile unchanged.

---

## 2. SQLite Schema

All tables use `STRICT` mode (SQLite 3.37+) for type safety. All timestamps are `INTEGER` storing unix milliseconds (matches `time.Now().UnixMilli()` already used throughout). Boolean fields use `INTEGER` with a `CHECK (col IN (0,1))` constraint.

### 2.1 `nodes`

```sql
CREATE TABLE nodes (
    id                       TEXT    PRIMARY KEY,
    wallet_address           TEXT    NOT NULL,
    node_type                TEXT    NOT NULL,
    verification_method      TEXT    NOT NULL,
    rpc_endpoint             TEXT    NOT NULL DEFAULT '',
    auth_token               TEXT    NOT NULL DEFAULT '',
    registered_at            INTEGER NOT NULL,
    last_verified_at         INTEGER NOT NULL DEFAULT 0,
    last_heartbeat_at        INTEGER NOT NULL DEFAULT 0,
    total_challenges_passed  INTEGER NOT NULL DEFAULT 0,
    total_challenges_failed  INTEGER NOT NULL DEFAULT 0,
    total_uptime_minutes     INTEGER NOT NULL DEFAULT 0,
    total_points             INTEGER NOT NULL DEFAULT 0,
    is_active                INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0,1)),
    cheat_status             TEXT    NOT NULL DEFAULT 'clean',
    warning_count            INTEGER NOT NULL DEFAULT 0,
    cheat_reason             TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_nodes_wallet  ON nodes(wallet_address);
CREATE INDEX idx_nodes_flagged ON nodes(cheat_status) WHERE is_active = 1;
```

The `SuspiciousEvents []string` slice on `NodeRegistration` is **not** stored as a column. It's hydrated by joining the `suspicious_events` table at read time (see §2.6). When loading a node for `GetNode` the SQLite impl issues one extra `SELECT` against `suspicious_events` ordered by `ts DESC LIMIT 20` and assigns to the struct. This matches existing in-memory cap.

### 2.2 `verification_results`

```sql
CREATE TABLE verification_results (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id          TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    challenge_id     TEXT    NOT NULL,
    passed           INTEGER NOT NULL CHECK (passed IN (0,1)),
    response_time_ms INTEGER NOT NULL DEFAULT 0,
    failure_reason   TEXT    NOT NULL DEFAULT '',
    suspicious       INTEGER NOT NULL DEFAULT 0 CHECK (suspicious IN (0,1)),
    suspicious_note  TEXT    NOT NULL DEFAULT '',
    ts               INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_vresults_node_ts ON verification_results(node_id, ts DESC);
```

### 2.3 `heartbeats`

```sql
CREATE TABLE heartbeats (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id      TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    ts           INTEGER NOT NULL,
    block_number INTEGER NOT NULL DEFAULT 0,
    is_synced    INTEGER NOT NULL DEFAULT 0 CHECK (is_synced IN (0,1)),
    latency_ms   INTEGER NOT NULL DEFAULT 0,
    peers_count  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_heartbeats_node_ts ON heartbeats(node_id, ts DESC);
```

### 2.4 `pending_challenges`

Currently lives in `internal/verification/verifier.go` as an in-memory map. Persisting it lets pending challenges survive restart and lets the scheduler in ADR-0007 dispatch them across multiple goroutines safely. **Not used by the Phase 1 cut** — the verifier does not yet import the store. The table exists so a follow-up agent can wire the verifier to it without another migration.

```sql
CREATE TABLE pending_challenges (
    id               TEXT    PRIMARY KEY,
    node_id          TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    challenge_type   TEXT    NOT NULL,
    params_json      TEXT    NOT NULL DEFAULT '{}',
    expected_answer  TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_pending_expires ON pending_challenges(expires_at);
```

### 2.5 `suspicious_events`

Replaces the `SuspiciousEvents []string` slice with structured rows. The current code formats events as `"2006-01-02 15:04: <reason>"` strings; the new schema stores `(ts, event)` separately so admin UIs can sort and filter.

```sql
CREATE TABLE suspicious_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    event   TEXT    NOT NULL,
    ts      INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_suspicious_node_ts ON suspicious_events(node_id, ts DESC);
```

When hydrating `NodeRegistration.SuspiciousEvents` for handlers, format each row as `time.UnixMilli(ts).Format("2006-01-02 15:04") + ": " + event` to preserve the existing JSON wire format.

### 2.6 `admin_actions`

New audit trail. Every `SetNodeCheatStatus` call writes a row. `admin_subject` is the truncated public key or the literal string `"system"` if no admin context (e.g. automatic escalation).

```sql
CREATE TABLE admin_actions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id       TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    action        TEXT    NOT NULL,        -- "clear" | "warn" | "ban"
    reason        TEXT    NOT NULL DEFAULT '',
    ts            INTEGER NOT NULL,
    admin_subject TEXT    NOT NULL DEFAULT 'system'
) STRICT;

CREATE INDEX idx_admin_actions_node_ts ON admin_actions(node_id, ts DESC);
```

### 2.7 `nonces_seen` (Phase 3 placeholder)

Anti-replay table for signature verification. Created now so Phase 3 doesn't need a migration.

```sql
CREATE TABLE nonces_seen (
    wallet     TEXT    NOT NULL,
    nonce      TEXT    NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (wallet, nonce)
) STRICT;

CREATE INDEX idx_nonces_expires ON nonces_seen(expires_at);
```

### 2.8 `snapshots` (Phase 4 placeholder)

Merkle snapshot publication record. Created now per [ADR-0006](../adr/0006-sqlite-mvp.md) reference to forthcoming ADR-0008.

```sql
CREATE TABLE snapshots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    cycle_id     INTEGER NOT NULL UNIQUE,
    root         TEXT    NOT NULL,
    total_amount INTEGER NOT NULL DEFAULT 0,
    published_at INTEGER NOT NULL,
    ipfs_cid     TEXT
) STRICT;
```

`ipfs_cid` is intentionally nullable (no `NOT NULL`) — pre-publish snapshots may not have a CID yet.

### 2.9 `snapshot_proofs` (Phase 4 placeholder)

```sql
CREATE TABLE snapshot_proofs (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    wallet      TEXT    NOT NULL,
    amount      INTEGER NOT NULL,
    proof_json  TEXT    NOT NULL,
    PRIMARY KEY (snapshot_id, wallet)
) STRICT;

CREATE INDEX idx_snapshot_proofs_wallet ON snapshot_proofs(wallet);
```

### 2.10 `schema_migrations`

```sql
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
) STRICT;
```

---

## 3. Migration Strategy

- Migrations live under `internal/store/sqlite/migrations/` as numbered SQL files. Naming: `NNN_short_description.sql`. Initial cut: `001_initial.sql` containing every `CREATE TABLE` and `CREATE INDEX` from §2 above.
- Embed via Go's standard `embed` package:
  - `//go:embed migrations/*.sql` on a `var migrationFS embed.FS` at the top of `internal/store/sqlite/sqlite.go`.
- On `NewSQLite(dsn)`:
  1. Open `sql.DB`, set pool sizing (§4).
  2. Apply boot pragmas (`journal_mode`, `synchronous`, `foreign_keys`, `busy_timeout`).
  3. `CREATE TABLE IF NOT EXISTS schema_migrations (...)` — bootstrap.
  4. `SELECT version FROM schema_migrations` into a set.
  5. List `migrations/*.sql` from the embedded FS, sorted lexicographically.
  6. For each unapplied file: `BEGIN; <sql>; INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?); COMMIT;`. Any error rolls back and aborts startup with a clear log line including the file name.
- Migrations are **forward-only and additive**. Per ADR-0006: rolling back a schema change is a new forward migration. Down-migrations are explicitly out of scope.
- Tests use a fresh `:memory:` DB per test or a temp file under `t.TempDir()`. The same migration runner is invoked.

---

## 4. Concurrency Model

**Decision: keep the default `database/sql` pool with `busy_timeout=5000ms`. Do not pin to a single connection.**

Rationale:
- WAL mode allows many concurrent readers and one writer. With a default pool, reads parallelize; writes naturally serialize because SQLite holds the write lock at the file level.
- `busy_timeout=5000` instructs SQLite to wait up to 5 seconds when a write lock is contended, instead of immediately returning `SQLITE_BUSY`. With our write rate (dozens/second per ADR-0006), 5 s is far more than enough.
- `SetMaxOpenConns(1)` would serialize **reads** as well, eliminating WAL's main benefit and making the leaderboard/stats endpoints scale linearly with handler count.
- Recommended pool tuning (set in `NewSQLite`):
  - `db.SetMaxOpenConns(8)` — small upper bound, keeps file descriptor count predictable.
  - `db.SetMaxIdleConns(4)`.
  - `db.SetConnMaxLifetime(0)` — connections live for the process lifetime.
- Every multi-statement write (e.g. `RecordVerificationResult` updating both `verification_results` and `nodes`) MUST run inside a `BEGIN IMMEDIATE` transaction so the writer lock is acquired up front and `busy_timeout` is applied to the lock acquisition itself, not to a later statement.

If profiling later reveals lock contention (unlikely at <100 nodes), the fallback is `SetMaxOpenConns(1)` for writes via a separate `*sql.DB` handle — but that's a follow-up, not Phase 1.

---

## 5. Retention

The current in-memory store caps `verification_history[node]` at 1000 entries and `heartbeats[node]` at 300 entries. The SQLite impl preserves this via a background pruner.

- A goroutine started in `NewSQLite` runs every **10 minutes**.
- For each node, delete rows beyond the cap:
  ```sql
  DELETE FROM verification_results
  WHERE node_id = ?
    AND id NOT IN (
      SELECT id FROM verification_results
      WHERE node_id = ?
      ORDER BY ts DESC LIMIT 1000
    );
  ```
  And the same shape for `heartbeats` with `LIMIT 300`.
- Also prune `pending_challenges WHERE expires_at < ?` and `nonces_seen WHERE expires_at < ?`.
- The goroutine is owned by the store and receives a stop signal on `Close()`. Implementation uses `context.WithCancel` in the SQLite struct; `Close()` cancels the context and waits on a `sync.WaitGroup`.
- Triggers are explicitly avoided per the task brief: triggers run inside the writer's transaction and inflate write latency, plus they're harder to test than a goroutine you can call directly. Tests can call the unexported `prune()` method synchronously.

---

## 6. Driver Choice

- Driver: **`modernc.org/sqlite`** (pure Go, no CGO). Already mandated by ADR-0006.
- Registered driver name: `"sqlite"`. **Not** `"sqlite3"` — that name belongs to `mattn/go-sqlite3` and using it here will fail with a confusing "unknown driver" error if both packages are ever imported.
- DSN format:
  ```
  file:./data/depin.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)
  ```
  The `_pragma=` query syntax is `modernc.org/sqlite`'s; it differs from `mattn/go-sqlite3`'s `_journal=WAL` style. The brief's example uses the mattn syntax — the implementation must use the modernc syntax.
- For tests / `:memory:` DBs:
  ```
  file::memory:?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)
  ```
- The DB file path comes from the `DB_PATH` env var per ADR-0006, defaulting to `./data/depin.db`. The store creates the parent directory with `os.MkdirAll(dir, 0o755)` if missing.
- Add to `go.mod`: `modernc.org/sqlite` only. Do **not** add `mattn/go-sqlite3` or any migration tool (`golang-migrate`, `goose`, etc.) — the embedded runner in §3 is sufficient.

---

## 7. Package Layout

```
internal/store/
├── store.go                       # Interface only (Store), plus shared error vars
├── memory/
│   └── memory.go                  # MemoryStore — current implementation, renamed
└── sqlite/
    ├── sqlite.go                  # SQLiteStore struct, constructor, Close, prune loop, migration runner
    ├── sqlite_nodes.go            # RegisterNode, GetNode, GetNodesByWallet, GetAllActiveNodes, UpdateNode, SetNodeCheatStatus, GetFlaggedNodes
    ├── sqlite_verification.go     # RecordVerificationResult, GetVerificationHistory, AddSuspiciousEvent
    ├── sqlite_heartbeats.go       # RecordHeartbeat, GetHeartbeats
    ├── sqlite_stats.go            # GetNodeStats, GetWalletStats, AwardUptimePoints
    └── migrations/
        └── 001_initial.sql        # All CREATE TABLE / CREATE INDEX from §2
```

Constructors:
- `memory.New() store.Store` — replaces today's `store.NewStore()`.
- `sqlite.New(dsn string) (store.Store, error)` — creates the file, runs migrations, starts the pruner. Returns the store (assignable to the interface) and any error.

The split into multiple files in `sqlite/` keeps each file under the 500-line CLAUDE.md limit. The memory impl stays in a single file because it's already only ~380 lines.

---

## 8. Handler Wiring

### 8.1 `cmd/server/main.go` diff (illustrative)

Add an env var `DATABASE_URL`. Empty or `"memory"` selects the in-memory backend (preserves current behavior for tests and dev). Anything else is treated as a SQLite DSN/path.

```diff
 import (
     ...
-    "github.com/depinonbnb/depin/internal/store"
+    "github.com/depinonbnb/depin/internal/store"
+    "github.com/depinonbnb/depin/internal/store/memory"
+    "github.com/depinonbnb/depin/internal/store/sqlite"
     "github.com/depinonbnb/depin/internal/verification"
 )

 func main() {
     godotenv.Load()
     ...
+    dbURL := os.Getenv("DATABASE_URL")
+    if dbURL == "" {
+        dbURL = os.Getenv("DB_PATH") // legacy name from ADR-0006; honor it
+    }
+
+    var nodeStore store.Store
+    switch {
+    case dbURL == "" || dbURL == "memory":
+        nodeStore = memory.New()
+        fmt.Println("Store backend: memory (volatile)")
+    default:
+        s, err := sqlite.New(dbURL)
+        if err != nil {
+            log.Fatalf("failed to open sqlite store at %q: %v", dbURL, err)
+        }
+        nodeStore = s
+        fmt.Printf("Store backend: sqlite (%s)\n", dbURL)
+    }
+    defer nodeStore.Close()
-    nodeStore := store.NewStore()
     verifier := verification.NewVerifier(trustedRPC)
     ...
 }
```

Note: ADR-0006 uses `DB_PATH`, the brief uses `DATABASE_URL`. Honor both — `DATABASE_URL` wins if set, fallback to `DB_PATH`. Update `.env.example` and SPEC §8 in a follow-up commit (not in this design's scope).

### 8.2 No changes required in `internal/verification/`

The verifier never touched the store directly; only handlers did. Phase 1 is store-only.

### 8.3 Test wiring

- `internal/api/handlers_test.go`: replace `store.NewStore()` with `memory.New()`.
- New `internal/store/sqlite/sqlite_test.go`: every interface method gets a round-trip test using `sqlite.New("file::memory:?…")`.
- Existing `internal/store/store_test.go` moves to `internal/store/memory/memory_test.go`.
- A shared `internal/store/storetest/` package (optional, recommended) defines a `RunSuite(t *testing.T, factory func() store.Store)` so both backends are tested against the same scenarios. This catches behavioral drift.

---

## 9. Open Questions

1. **`is_active` column vs derived flag.** Today `IsActive` is a stored boolean toggled only by `SetNodeCheatStatus(StatusBanned)`. Should it be derived at read time from heartbeat freshness (e.g., `last_heartbeat_at > now - 15min`)? Pros: no stale "active" nodes that haven't pinged in days. Cons: changes the meaning of leaderboard/stats numbers and breaks the existing test where a freshly registered node is `IsActive=true` before any heartbeat. **Recommend: keep as a stored column for Phase 1, file a separate ADR if/when we want to redefine activity.**

2. **Soft-delete vs row deletion for banned nodes.** The current impl flips `IsActive=false` and keeps the row. Should we ever actually `DELETE FROM nodes WHERE id = ?`? The audit trail in `admin_actions` has `ON DELETE CASCADE` against `nodes(id)`, which would lose the audit history if we ever hard-delete. **Recommend: never hard-delete. Drop `ON DELETE CASCADE` on `admin_actions.node_id` and keep it as a `REFERENCES` constraint only — but then orphaned audit rows are possible if a node is deleted by hand. The cleanest answer is "we never delete nodes" + keep CASCADE on the `verification_results`, `heartbeats`, `suspicious_events`, `pending_challenges` tables (which are large and disposable) and remove CASCADE on `admin_actions` (which is small and an audit record).** Persist-coder should follow this recommendation; if it conflicts with anything in the implementation, raise it back.

3. **Lower-casing wallet addresses.** Handlers call `strings.ToLower(wallet)` before passing to the store. Should the store enforce this invariant itself (via `LOWER(wallet_address)` in WHERE clauses, or a CHECK constraint)? Today it relies on the caller. **Recommend: keep caller responsibility for Phase 1 (matches existing behavior); consider adding a `CHECK (wallet_address = LOWER(wallet_address))` in a later migration once we're sure no caller is sneaking mixed-case addresses through.**

---

## Elevator Summary (for downstream agent)

1. The `Store` interface is the existing concrete `*store.Store` API verbatim, plus a new `Close() error`. Handlers and router compile with a one-character change (`*store.Store` → `store.Store`).
2. Schema is in §2 — ten tables, all `STRICT`, FK with `ON DELETE CASCADE` everywhere except `admin_actions`. Indexes specified.
3. Driver is `modernc.org/sqlite` (pure Go, no CGO). Driver name `"sqlite"`. DSN uses `_pragma=...(...)` query syntax.
4. Concurrency: default `database/sql` pool + `busy_timeout=5000`, NOT `SetMaxOpenConns(1)`. WAL mode. Multi-statement writes use `BEGIN IMMEDIATE`.
5. Retention via background goroutine every 10 min (1000 verification rows + 300 heartbeats per node), no triggers. Migrations forward-only, embedded `*.sql`, applied transactionally.
