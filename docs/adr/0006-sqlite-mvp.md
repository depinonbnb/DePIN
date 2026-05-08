# 6. SQLite + WAL for MVP persistence

Date: 2026-05-08

## Status

Accepted (supersedes the in-memory portion of [0003](0003-in-memory-store.md) — that ADR's deprecation clock now starts)

## Context

[ADR-0003](0003-in-memory-store.md) committed to an in-memory store with the explicit deprecation note that "production launch is gated on having a persistent store". We are now starting Phase 1 of the production upgrade and the store has to survive a restart.

The SPEC's open question §10 framed the choice as "Postgres vs. SQLite vs. embedded KV". Re-evaluated against where the project actually is:

- Single API instance, single VM, single operator pool of <100 nodes for the foreseeable future
- Schema is still moving (anti-cheat state, snapshot tables for [0008](0008-merkle-snapshot-rewards.md), scheduler state for [0007](0007-scheduler-driven-verification.md)) — managed Postgres makes every change a migration on a remote DB
- The team has one Go engineer; standing up Postgres adds connection-pool config, a managed-DB account, an additional env var (`DATABASE_URL`), and a backup process
- We already have `internal/store/store.go` exposing a `Store` interface — swapping the backend later is a constructor change, not a rewrite

Postgres is the right answer when we need horizontal scale, multi-instance API, or shared state with another service. None of those are true today.

## Decision

- **Backend**: SQLite, single file at `data/depin.db` (path configurable via `DB_PATH`).
- **Driver**: `modernc.org/sqlite` — pure-Go translation of SQLite, no CGO required. Keeps the build a single static binary, keeps `go build` working on the dev laptops without extra toolchain.
  - Explicitly **not** `github.com/mattn/go-sqlite3` even though it's more popular. That driver requires CGO, which breaks cross-compilation, slows CI, and adds glibc dependency at runtime.
- **Mode**: WAL (`PRAGMA journal_mode=WAL`) enabled at boot. Allows concurrent readers while one writer is active, which matches our access pattern (many heartbeat / verification reads, few writes).
- **Other PRAGMAs at boot**:
  - `synchronous=NORMAL` — durability good enough for this data, faster than `FULL`
  - `foreign_keys=ON`
  - `busy_timeout=5000` — five-second wait before returning `SQLITE_BUSY`
- **Migrations**: a `schema/` directory with numbered `.sql` files, applied in order at boot. No external migration tool — a 30-line Go runner is sufficient at this scale.
- **Interface preserved**: the existing `store.Store` interface in `internal/store/store.go` does not change. A new `internal/store/sqlite/` package implements it. The in-memory store stays as a test fixture.
- **Production swap to Postgres** is a config decision, not a rewrite: when we cross the single-instance threshold, we add `internal/store/postgres/` implementing the same interface and switch via a `STORE_BACKEND` env var. Schema differences are bridged by keeping migrations dialect-light (no SQLite-only or Postgres-only features in the canonical schema).

## Consequences

- **Restart no longer wipes state.** Resolves SPEC gap #1.
- **Single binary, single file** — deployment is still `scp` and `systemctl restart`.
- **No new env vars** required to run locally — `DB_PATH` defaults to `./data/depin.db`.
- **Writes serialize.** SQLite allows one writer at a time. Heartbeat and verification writes can contend; the worker pool in [0007](0007-scheduler-driven-verification.md) must respect this (mostly fine — write rate is dozens/second at most).
- **`data/depin.db` becomes operationally critical.** It must be in the backup set. Document this in the deploy README; add it to `.gitignore`.
- **Single-instance only.** Cannot run two API replicas pointing at the same file. Acceptable until we outgrow it. The Postgres swap path is documented above.
- **No CGO** — verified by keeping `CGO_ENABLED=0` in the Docker build and CI.
- **Test isolation**: tests use `:memory:` SQLite or a temp file per test, not the in-memory map. Slower but realistic.
- The "Replace with a real database in production" comment at the top of `store.go` can be removed once the SQLite implementation lands.

## Notes for downstream agents

- Do **not** add `github.com/mattn/go-sqlite3` to `go.mod`. If you see it, it's a regression.
- The `Store` interface is the contract. Anything reading or writing data goes through it; no handler should `import database/sql` directly.
- Schema changes go in `schema/NNN-description.sql`, additive only. Rolling back a schema change is a new forward migration.
