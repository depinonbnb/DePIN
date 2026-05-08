# 3. In-memory store for the first iteration

Date: 2026-05-07

## Status

Accepted (with explicit deprecation date — see Consequences)

## Context

The protocol is pre-launch. Schema for nodes, challenges, verification history, and anti-cheat state is still in flux. Adding migrations on top of a database we'll redesign within weeks slows iteration. Restart-on-crash losing state is acceptable while the platform has zero or near-zero real users.

## Decision

Implement the store as a Go in-memory map guarded by a mutex (`internal/store/store.go`). Same interface that a real DB-backed store would expose, so the swap later is mechanical.

## Consequences

- Fastest possible iteration on the data model — no migrations
- **Restart wipes state.** Operators have to re-register, point totals reset
- **Single-instance only.** Cannot horizontally scale the API without a real backing store
- The `// Replace with a real database in production` comment at the top of `store.go` is binding — production launch is gated on having a persistent store
- Recommended replacement: PostgreSQL (multi-instance friendly, mature ORMs in Go like `pgx`/`sqlc`). SQLite is acceptable if we commit to single-instance forever, which seems unlikely
- Tracked as gap #1 in [../SPEC.md](../SPEC.md)
