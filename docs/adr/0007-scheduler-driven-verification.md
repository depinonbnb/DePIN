# 7. Scheduler-driven verification

Date: 2026-05-08

## Status

Accepted

## Context

Today verification is **request-driven**. The operator's prover polls `/challenges/request` on its own schedule, or an exposed-RPC node is only probed when something external hits `POST /verify/:nodeId`. Two consequences fall out of this:

1. **`AwardUptimePoints` is dead code.** It exists in `internal/types` and is wired through `store`, but nothing in the running server ever calls it. Operators accrue verification results but no uptime points unless an external trigger fires.
2. **A node can register, never verify, and never be marked offline.** There is no server-side heartbeat in the spec sense — the `/verify/:nodeId/heartbeat` endpoint is an HTTP probe, not a scheduled check.

Both the README and the internal `rem.txt` design notes describe automated, server-side cycles: heartbeat every 5 minutes, challenges per-tier, periodic uptime accrual. The implementation never caught up. SPEC gap #5 explicitly tracks "no background scheduler issuing challenges automatically".

The fields are already in `NodeType`:

- `ChallengeFrequencyMinutes` — how often each tier is challenged (30 / 60 / etc.)
- `MinUptimePercent` — the threshold for a healthy node

These fields exist but are not consulted by anything that runs.

## Decision

Verification is driven by **server-side tickers**, not by inbound HTTP. Three independent schedulers run inside `cmd/server`, each on its own goroutine, each with a `context.Context` for graceful shutdown:

| Scheduler | Cadence | Job |
|---|---|---|
| Heartbeat | every 5 min | For each `active` node, record a `HeartbeatRecord{Source: scheduler}`. For exposed-RPC nodes, do a lightweight `eth_blockNumber` ping. |
| Challenge | every minute (dispatcher); per-tier interval consulted from `NodeType.ChallengeFrequencyMinutes` | For each node whose last challenge is older than its tier's interval, generate and dispatch a challenge. |
| Uptime reward | every 5 min | For each active node, compute uptime over the last window and call `AwardUptimePoints`. Wakes the dead code path. |

Execution model:

- A **worker pool** of ~50 goroutines processes RPC fan-out (challenge probing + heartbeat pings). Bounded so we don't accidentally DoS a public RPC or our own egress NIC. Pool size is configurable via `RPC_WORKERS` env var.
- Each outbound RPC has a per-call timeout (5 s, matches existing latency thresholds in `internal/types/types.go`).
- **Retry policy**: exponential backoff (1s, 2s, 4s) up to 3 attempts per challenge. After 3 consecutive failures across 3 separate cycles, the node enters a **dead-letter** state — challenges still queue but at a 10x reduced rate to avoid wasting RPC budget on a clearly-down node.
- HTTP request-driven verification (`POST /verify/:nodeId`) is **kept** as an admin / debugging tool, but is no longer the primary path.
- Schedulers respect the on/off env switch `SCHEDULER_ENABLED=true|false` so tests and local dev can disable them.

Lifecycle: `cmd/server/main.go` constructs the schedulers after the store, registers their stop functions with the existing signal handler, and starts them after the HTTP listener is up.

## Consequences

- **`ChallengeFrequencyMinutes` and `MinUptimePercent` become live** — they finally do what their names suggest.
- **`AwardUptimePoints` is reachable**, so the points story end-to-end works for the first time. This is the precondition for the Merkle snapshot in [0008](0008-merkle-snapshot-rewards.md).
- **A node can no longer hide.** Registration alone doesn't earn points; the node has to keep responding to scheduler-issued challenges.
- **Outbound RPC traffic increases.** Roughly: `(active nodes) / ChallengeFrequencyMinutes` calls/minute, plus `2 * active_nodes / 5` heartbeat pings per minute (one to trusted, one to operator). With 100 nodes on a 30-min cadence: ~43 RPC calls/minute steady-state. Manageable.
- **Worker pool back-pressure** matters. If the pool saturates, jobs queue up; we monitor queue depth and emit a metric. If the queue grows unbounded, that's the canary that the pool is undersized.
- **Dead-letter handling** prevents one offline node from burning the entire RPC budget. The reduced cadence still gives the node a chance to recover without manual admin action.
- **Graceful shutdown**: schedulers cancel via context, drain in-flight workers up to a 30-second deadline, then force-close. Stop order: HTTP listener first (stop accepting), schedulers second (stop generating work), worker pool third (drain), store last.
- **Testability**: schedulers take an injectable `Clock` so tests can advance time without `time.Sleep`. The HTTP-driven verification path doubles as a mock for tests that don't want to spin up tickers.
- **SQLite write contention** ([0006](0006-sqlite-mvp.md)) is the main resource constraint. Worker pool size of 50 is chosen so contention is rare; raising it past ~100 will start noticeably hurting tail latency.
- Closes SPEC gap #5.

## Notes for downstream agents

- Schedulers go in a new `internal/scheduler/` package. Do not put them in `internal/api/`.
- The worker pool is one shared instance, not per-scheduler, so total outbound RPC concurrency is capped at one number, not three.
- Use `context.Context` everywhere — no `select { case <-time.After }` without a `case <-ctx.Done()` branch.
