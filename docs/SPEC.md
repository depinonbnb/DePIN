# DePINonBNB — Specification

Status: **draft / generated from existing codebase 2026-05-07**. Refine as the implementation evolves.

The project README at [../README.md](../README.md) covers the *what* (operator-facing description, node types, quick-start). This SPEC covers the *how* — architecture, contracts, invariants, gaps. The two are complementary; the README is the source of truth for user-facing claims, the SPEC for internal design.

## 1. Purpose

DePINonBNB is the off-chain control plane for the DePIN-on-BNB protocol. It:

1. Receives node registrations (verifies an EIP-191 wallet signature)
2. Issues per-node API auth tokens
3. Generates challenges for node operators
4. Verifies challenge responses, either by directly probing an exposed RPC or by accepting a signed proof from a local prover
5. Tracks uptime, latency, and points
6. Detects and flags suspicious behavior (anti-cheat)
7. Exposes leaderboards and aggregate stats

The on-chain side (staking, points, node-registry contracts) lives elsewhere and is consumed by the frontend ([../../DePIN-Web/](../../DePIN-Web/)). This service does **not** call contracts; it relies on signed messages and trusted RPC comparisons.

## 2. Architecture

```
                    ┌────────────────────┐
                    │  DePIN-Web (UI)    │
                    └──────────┬─────────┘
                               │ HTTPS (REST + JSON)
                    ┌──────────▼─────────┐
                    │  DePINonBNB API    │   ← cmd/server
                    │  (Gin)             │
                    │                    │
                    │ ┌─api/handlers────┐│
                    │ ├─verification────┤│
                    │ ├─challenge───────┤│
                    │ ├─rpc client──────┤│
                    │ └─store (memory)──┘│
                    └────────┬─────────┬─┘
                             │         │
              ┌──────────────▼──┐   ┌──▼─────────────────────┐
              │ TRUSTED_RPC      │   │  Operator's BNB node   │
              │ (BSC mainnet     │   │  + cmd/prover (CLI)    │
              │  reference)      │   │  via signed proofs OR  │
              └──────────────────┘   │  exposed JSON-RPC      │
                                     └────────────────────────┘
```

Single-binary Go service (`cmd/server`). A separate binary (`cmd/prover`) is shipped to operators who don't want to expose their node's RPC publicly; it polls challenges from the API, queries the local node, signs the result, and submits.

## 3. Module layout

```
cmd/
  server/main.go        Boot, env load, signal handling, lifecycle
  prover/main.go        Operator-side CLI (separate binary, ~335 LOC)

internal/
  api/
    router.go           Gin router, CORS, route registration (base path /api)
    middleware.go       AdminAuthMiddleware (header-based API key)
    handlers.go         13 HTTP handlers
    handlers_test.go
  challenge/
    generator.go        Builds 5 challenge types per node-type difficulty
  rpc/
    client.go           JSON-RPC client for BSC / opBNB nodes
  store/
    store.go            In-memory thread-safe store for nodes, challenges,
                        heartbeats, verification history
    store_test.go
  types/
    types.go            All shared types + constants (node types, challenge
                        kinds, latency thresholds, point rates)
    types_test.go
  verification/
    verifier.go         Verifies responses, runs anti-cheat checks
    verifier_test.go
```

> **Test layout note**: tests live alongside implementation files (`*_test.go`) per Go convention. The workspace's "strict layout" rule about a top-level `tests/` directory does not apply to Go projects — moving tests would break package boundaries. SPEC mentions this explicitly so future agents don't try to "fix" it.

## 4. API surface

Base path: `/api`. Documented inline in `internal/api/router.go`.

### Public

| Method | Path | Purpose |
|---|---|---|
| POST | `/nodes/register` | Register a node. Body must include wallet, signature, node type, optional RPC URL |
| GET | `/nodes/:nodeId` | Public node info (auth token redacted) |
| GET | `/nodes/:nodeId/stats` | Uptime, points, pass-rate, avg latency |
| GET | `/nodes/wallet/:walletAddress` | All nodes for a wallet |
| GET | `/wallet/:walletAddress/stats` | Aggregated stats |
| GET | `/leaderboard` | Top-ranked nodes |
| GET | `/stats` | Network-wide stats |
| GET | `/health` | Liveness check |

### Challenges (local-prover path)

| Method | Path | Purpose |
|---|---|---|
| GET | `/challenges/request?nodeId=…` | Issue a challenge to a node |
| POST | `/challenges/submit` | Submit a signed response |

### Verification (exposed-RPC path)

| Method | Path | Purpose |
|---|---|---|
| POST | `/verify/:nodeId` | Server probes node directly, compares against TRUSTED_RPC |
| GET | `/verify/:nodeId/heartbeat` | Lightweight uptime ping |

### Admin (require `ADMIN_API_KEY` header if env var is set)

| Method | Path | Purpose |
|---|---|---|
| GET | `/admin/flagged` | Nodes flagged by anti-cheat |
| POST | `/admin/review/:nodeId` | Approve / warn / ban |
| POST | `/admin/test/create-node` | Test fixture — create node without signature |

> **Hardening note**: if `ADMIN_API_KEY` is unset, admin routes are **unprotected**. Production deployments must always set this. Tracked as gap #2.

## 5. Data model

All types in `internal/types/types.go`. Key entities:

| Type | Fields (abridged) |
|---|---|
| `NodeRegistration` | NodeID, WalletAddress, NodeType, RPCURL, AuthToken, Status, RegisteredAt |
| `Challenge` | ID, NodeID, Type, Params, Difficulty, ExpectedAnswer, ExpiresAt |
| `VerificationResult` | NodeID, ChallengeID, Passed, Latency, FailureReason, At |
| `HeartbeatRecord` | NodeID, At, Source |
| `CheatStatus` | WarningCount, FlaggedAt, ReviewState |

Persisted in `internal/store/store.go` — an in-memory map protected by a mutex. Comments at the top mark it as "replace with a real database in production". Tracked as gap #1.

## 6. Verification protocol

Two interchangeable paths produce the same `VerificationResult`:

### 6.1 Exposed-RPC

Server holds a public RPC URL for the node. On a challenge cycle:
1. Pick a challenge type (block-hash, block-data, sync-status, balance, tx-receipt)
2. Query both `TRUSTED_RPC` (truth) and the node's RPC (claim)
3. Compare. Record latency.
4. Apply anti-cheat thresholds (see §7)

### 6.2 Local-prover

Operator runs `cmd/prover`. Loop:
1. `GET /challenges/request` → JSON challenge
2. Query the local node (via `NODE_RPC`, default `localhost:8545`)
3. Sign `(challengeID || answer || timestamp)` with `PROVER_PRIVATE_KEY`
4. `POST /challenges/submit` with the signed response

Server verifies the signature corresponds to the registered wallet, then compares the answer against `TRUSTED_RPC`.

Both paths use EIP-191 prefixed messages.

## 7. Anti-cheat

Latency thresholds (from `internal/types/types.go`):

| Bucket | Range | Verdict |
|---|---|---|
| Plausible local | <100 ms | Healthy |
| Suspicious | 100–150 ms | Warn |
| Public-RPC-like | 150–300 ms | Flag for review |
| Slow / proxied | 300–5000 ms | Warn |
| Timeout | >5000 ms | Fail |

Warnings accumulate; after a threshold the node is flagged for admin review. Decisions are recorded against `CheatStatus`.

## 8. Configuration

| Var | Default | Used by | Notes |
|---|---|---|---|
| `PORT` | `3000` | server | The frontend assumes `3001` — see gap #3 |
| `TRUSTED_RPC` | `https://bsc-dataseed1.binance.org` | server | Truth source for comparisons |
| `ADMIN_API_KEY` | (empty) | server | If empty, admin endpoints are open. **Set in prod.** |
| `PROVER_PRIVATE_KEY` | (none) | prover | Operator's wallet key |
| `NODE_RPC` | `http://localhost:8545` | prover | Operator's local node RPC |
| `DEPIN_API` | `http://localhost:3000/api` | prover | Server URL |
| `NODE_TYPE` | `bsc-full` | prover | Self-declared node type |
| `INTERVAL` | `300000` | prover | Submit interval in ms (5 min) |

`.env.example` is the authoritative list — keep it in sync when env vars change.

## 9. Known gaps

| # | Where | Issue |
|---|---|---|
| 1 | `internal/store/store.go` | RESOLVED (Phase 1 persistence) — `internal/store/store.go` is now the `Store` interface; `internal/store/memory/` keeps the volatile implementation for tests/dev and `internal/store/sqlite/` adds the SQLite-backed production store per [ADR-0006](adr/0006-sqlite-mvp.md). Both implementations are exercised by the conformance suite at `internal/store/conformance.go` so they cannot drift; `internal/integration/` runs the full router end-to-end against the SQLite backend. Postgres swap path remains documented in ADR-0006 |
| 2 | `internal/api/middleware.go` | RESOLVED (Phase 0 hygiene) — `AdminAuthMiddleware` is now always applied. When `ADMIN_API_KEY` is empty it fails closed with 503 ("admin endpoints disabled"). Key comparison uses `crypto/subtle.ConstantTimeCompare` to defeat timing attacks |
| 3 | `cmd/server/main.go` | Default port 3000 conflicts with the frontend's expected 3001. Pick one and align both projects |
| 4 | `internal/verification/verifier.go` | Anti-cheat is latency-based only; no statistical anomaly detection |
| 5 | (none) | No background scheduler issuing challenges automatically — currently challenge cycle is request-driven by the prover. May need cron loop for exposed-RPC nodes |
| 6 | (none) | No actual reward distribution. Points are tracked but not claimable on-chain |
| 7 | (none) | No rate limiting per node or per wallet |
| 8 | `.gitignore` | RESOLVED (Phase 0 hygiene) — `server.exe`, `server.exe~`, `info.md`, `rem.txt` untracked from git; `.gitignore` updated to cover those plus forward-looking `data.db*` / `data/` for the SQLite persistence work in [ADR-0006](adr/0006-sqlite-mvp.md) |
| 9 | `info.md` | RESOLVED — covered by gap #8 |
| 10 | `internal/api/handlers.go` (`GetLeaderboard`) | RESOLVED (Phase 0 hygiene) — replaced O(n²) bubble sort with `sort.Slice` descending by `TotalPoints` |
| 14 | `internal/api/handlers.go` (`verifySignature`) | RESOLVED (Phase 0 hygiene) — replaced the hand-rolled hex byte loop and the `hexToByte` helper with `encoding/hex.DecodeString` and a length check (`len(sigBytes) == 65`). EIP-191 prefix and v-byte normalisation unchanged |

> **Logger**: `log/slog` (stdlib, Go 1.21+). Honors `LOG_LEVEL` (`debug|info|warn|error`, case-insensitive, default `info`) and `LOG_FORMAT` (`json` selects the JSON handler, anything else / unset selects text). Configured once at boot in `cmd/server/main.go`; all packages call `slog.Default()` (e.g. `slog.Warn(...)` in `internal/verification/verifier.go`). The `log` package is no longer imported anywhere in the server tree.

> **Tests**: a single conformance suite at `internal/store/conformance.go` is run against both `MemoryStore` and `SQLiteStore` from their respective `*_test.go` entry points so the two implementations cannot drift. End-to-end flow is exercised in `internal/integration/integration_test.go`. Run via `go test ./... -race -count=1` (the `-race` flag must stay clean across the full run).

## 10. Open questions

- Persistent store: Postgres vs. SQLite vs. embedded KV (Bolt/Badger)? Postgres if we expect multi-instance; SQLite if single-instance is fine for now
- Should `TRUSTED_RPC` be plural (a quorum of trusted RPCs) so a single bad reference can't poison verification?
- How do we rotate `AuthToken` if leaked?
- How are challenges expired and garbage-collected from the in-memory store at scale?
