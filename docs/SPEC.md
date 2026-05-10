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
              │ TRUSTED_RPCS     │   │  Operator's BNB node   │
              │ (quorum of BSC   │   │  + cmd/prover (CLI)    │
              │  / opBNB ref     │   │  via signed proofs OR  │
              │  endpoints,      │   │  exposed JSON-RPC      │
              │  ADR-0009)       │   └────────────────────────┘
              └──────────────────┘
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
  scheduler/
    scheduler.go        Three-ticker bundle (heartbeat / challenge / uptime),
                        shared worker-pool semaphore, ctx-driven shutdown
    heartbeat.go        Heartbeat ticker (per ADR-0007)
    challenge.go        Per-tier challenge dispatcher
    uptime.go           Uptime reward ticker (wakes AwardUptimePoints)
    scheduler_test.go
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
2. Query the **trusted RPC quorum** ([ADR-0009](adr/0009-quorum-trusted-rpc.md)) and the node's RPC (claim)
3. Compare. Record latency.
4. Apply anti-cheat thresholds (see §7)

The quorum step fans out across every endpoint in `TRUSTED_RPCS` (default: 3 BSC dataseeds) in parallel. The majority answer is treated as truth. **No-majority abort case**: if no answer reaches `floor(N/2)+1`, the verifier produces a `VerificationResult` with `FailureReason="trusted RPC quorum disagreement (treated as abort, not cheating)"` and `Suspicious=false`. The store still records the result, but the operator's warning count is not bumped.

### 6.2 Local-prover

Operator runs `cmd/prover`. Loop:
1. `GET /challenges/request` → JSON challenge
2. Query the local node (via `NODE_RPC`, default `localhost:8545`)
3. Generate a fresh random `nonce` (e.g. UUID v4); sign the message
   ```
   Challenge Response
   ID: <challenge id>
   Answer: <answer>
   Timestamp: <unix-ms>
   Nonce: <nonce>
   ```
   with `PROVER_PRIVATE_KEY`
4. `POST /challenges/submit` with the signed response (body must include `nonce`)

Server verifies the signature corresponds to the registered wallet, **rejects replays** by recording `(wallet, nonce)` with a 10-minute TTL (returns 409 Conflict on replay), then compares the answer against the trusted RPC quorum.

Registration uses the same nonce-bound message shape:
```
Register node
Wallet: <address>
Type: <node type>
Timestamp: <unix-ms>
Nonce: <nonce>
```

> **Wire-format change (Phase 3)**: both signed messages above used to omit the `Nonce: …` line. Provers and front-end signers MUST be updated. Servers without the new field receive 400 (binding required); old clients without the field will receive 400 too. Document for downstream operators.

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
| `TRUSTED_RPCS` | 3 BSC dataseeds (`bsc-dataseed{1,2,3}.binance.org`) | server | **Preferred.** Comma-separated quorum endpoints per [ADR-0009](adr/0009-quorum-trusted-rpc.md). With N endpoints majority is `floor(N/2)+1`. If exactly one endpoint is configured we silently degrade to ADR-0005 single-source behavior and log a warning. opBNB tiers can use `https://opbnb-mainnet-rpc.bnbchain.org,…` if BSC dataseeds are wrong for the node type — heterogeneous endpoints work because the quorum lookup is per-call |
| `TRUSTED_RPC` | (empty) | server | **Deprecated.** Legacy ADR-0005 single endpoint. If `TRUSTED_RPCS` is unset and this is set we treat it as a one-element list and emit a deprecation warning on boot. Drop after one release cycle |
| `CORS_ALLOWED_ORIGINS` | (empty) | server | Comma-separated origins permitted to read the API cross-origin. **Empty / unset = no Access-Control-Allow-Origin header is ever returned**, which causes browsers to block all cross-origin reads — set this to your frontend's origin in prod |
| `ALLOW_PRIVATE_RPC` | `0` | server | When `1`, the SSRF guard on operator-supplied `RPCEndpoint` is disabled. Use only for local docker-compose / dev. In prod the guard rejects private IPs, loopback, link-local, and `.local` / `.internal` / `.corp` / `.home` / `.lan` suffixes |
| `ADMIN_API_KEY` | (empty) | server | **If empty, admin endpoints return 503** ("admin endpoints disabled"). Always set in prod — see gap #2 |
| `PROVER_PRIVATE_KEY` | (none) | prover | Operator's wallet key |
| `NODE_RPC` | `http://localhost:8545` | prover | Operator's local node RPC |
| `DEPIN_API` | `http://localhost:3000/api` | prover | Server URL |
| `NODE_TYPE` | `bsc-full` | prover | Self-declared node type |
| `INTERVAL` | `300000` | prover | Submit interval in ms (5 min) |
| `SCHEDULER_ENABLED` | `true` | server | Set to `false`/`0`/`no`/`off` to construct schedulers without starting their tickers (used by tests and local dev) — see §8a |
| `HEARTBEAT_INTERVAL` | `5m` | server | Cadence for the heartbeat ticker (Go duration syntax) |
| `CHALLENGE_CHECK_INTERVAL` | `1m` | server | Cadence for the challenge dispatcher's outer loop. Per-node challenge cadence is `NodeType.ChallengeFrequencyMinutes()` |
| `REWARD_INTERVAL` | `5m` | server | Cadence for the uptime-reward ticker (calls `AwardUptimePoints`) |
| `RPC_WORKERS` | `50` | server | Upper bound on concurrent outbound RPC operations across all three schedulers |

`.env.example` is the authoritative list — keep it in sync when env vars change.

## 8a. Schedulers (Phase 2, ADR-0007)

Three server-side tickers run as goroutines inside `cmd/server/main.go`, all driven by a single `context.Context` so SIGINT/SIGTERM cancels them cleanly. They share one bounded worker pool (a buffered semaphore channel of size `RPC_WORKERS`) so total outbound RPC concurrency is one number, not three.

| Ticker | Default interval | Env override | Job |
|---|---|---|---|
| heartbeat | 5m | `HEARTBEAT_INTERVAL` | For each active exposed-RPC node (skipping `banned`), call `verifier.CheckHeartbeat`. On success, persist a `HeartbeatRecord` |
| challenge | 1m loop | `CHALLENGE_CHECK_INTERVAL` | For each active exposed-RPC node whose `now - LastVerifiedAt > NodeType.ChallengeFrequencyMinutes() * 60_000`, run `verifier.VerifyExposedRPC` and persist the result. Local-prover nodes are NOT dispatched — they continue to pull via `GET /challenges/request` |
| uptime | 5m | `REWARD_INTERVAL` | For each active node with at least one heartbeat in the last `RewardInterval + buffer`, call `store.AwardUptimePoints(nodeID, RewardInterval/time.Minute)`. Banned/flagged nodes are pre-filtered to keep log counters accurate |

Lifecycle: `scheduler.New(store, verifier, cfg)` builds the bundle (no goroutines yet); `Start()` launches all three; `Close()` cancels their context and waits for in-flight workers to drain. `Close` is idempotent. When `SCHEDULER_ENABLED=false`, `main.go` constructs the scheduler but skips `Start()` so the same wiring works in tests that don't want background ticking.

Shutdown order (per ADR-0007): HTTP listener first (stop accepting), schedulers second (stop generating work), store last (so any in-flight reward writes still have a destination).

> **Phase 3 hardened (2026-05): the `*rand.Rand` is now mutex-protected,
> the trusted RPC is a quorum ([ADR-0009](adr/0009-quorum-trusted-rpc.md)),
> the EIP-191 messages bind a server-recorded nonce, the operator-supplied
> RPC endpoint is SSRF-checked, the wildcard CORS header is replaced with an
> allow-list, and per-IP + per-wallet rate limits are enforced.**

## 9. Known gaps

| # | Where | Issue |
|---|---|---|
| 1 | `internal/store/store.go` | RESOLVED (Phase 1 persistence) — `internal/store/store.go` is now the `Store` interface; `internal/store/memory/` keeps the volatile implementation for tests/dev and `internal/store/sqlite/` adds the SQLite-backed production store per [ADR-0006](adr/0006-sqlite-mvp.md). Both implementations are exercised by the conformance suite at `internal/store/conformance.go` so they cannot drift; `internal/integration/` runs the full router end-to-end against the SQLite backend. Postgres swap path remains documented in ADR-0006 |
| 2 | `internal/api/middleware.go` | RESOLVED (Phase 0 hygiene) — `AdminAuthMiddleware` is now always applied. When `ADMIN_API_KEY` is empty it fails closed with 503 ("admin endpoints disabled"). Key comparison uses `crypto/subtle.ConstantTimeCompare` to defeat timing attacks |
| 3 | `cmd/server/main.go` | Default port 3000 conflicts with the frontend's expected 3001. Pick one and align both projects |
| 4 | `internal/verification/verifier.go` | Anti-cheat is latency-based only; no statistical anomaly detection (still open — Phase 3 punted; revisit alongside Merkle rewards in Phase 4) |
| 5 | `internal/scheduler/` | RESOLVED (Phase 2 — ADR-0007) — three server-side tickers now drive verification: heartbeat (every `HEARTBEAT_INTERVAL`, default 5m), challenge dispatcher (every `CHALLENGE_CHECK_INTERVAL`, default 1m, gated per-node by `NodeType.ChallengeFrequencyMinutes()`), and uptime reward (every `REWARD_INTERVAL`, default 5m, calling `AwardUptimePoints` and finally retiring that dead-code path). All three share a single semaphore-bounded worker pool (`RPC_WORKERS`, default 50). Set `SCHEDULER_ENABLED=false` to skip `Start()` for tests/dev. Local-prover nodes are intentionally NOT dispatched server-side — they continue to pull via `/challenges/request`. See `internal/scheduler/scheduler_test.go` for end-to-end coverage |
| 6 | `internal/reward/` + `internal/store/*` (snapshots) + `internal/api/snapshot_handlers.go` | RESOLVED (Phase 4 — ADR-0008) — Merkle snapshot rewards. The backend now aggregates lifetime points by wallet, builds a sorted-leaf / sorted-pair tree of `keccak256(abi.encodePacked(address, uint256))` leaves, persists root + per-wallet proofs in `snapshots` / `snapshot_proofs`, and exposes them via `GET /api/snapshots/latest`, `GET /api/snapshots/:cycleId`, `GET /api/wallet/:wallet/claim/:cycleId`, and `GET /api/wallet/:wallet/claim/latest`. Cycle publishing is manual via `POST /api/admin/snapshot/publish` for Phase 4; `SNAPSHOT_INTERVAL` env var is reserved as a no-op placeholder for the Phase 5 cron. The Solidity Distributor lives in a separate repo and is intentionally NOT in scope here. See §10 |
| 7 | `internal/api/ratelimit.go` | RESOLVED (Phase 3) — per-IP global limit (60 req/min), tighter per-IP cap on `/nodes/register` and `/challenges/submit` (10/min), and per-wallet caps (5 register/min, 30 submit/min). LRU map of `*rate.Limiter` capped at 10k keys. Implemented with `golang.org/x/time/rate`. Admin routes are not rate-limited (already auth-gated) |
| 8 | `.gitignore` | RESOLVED (Phase 0 hygiene) — `server.exe`, `server.exe~`, `info.md`, `rem.txt` untracked from git; `.gitignore` updated to cover those plus forward-looking `data.db*` / `data/` for the SQLite persistence work in [ADR-0006](adr/0006-sqlite-mvp.md) |
| 9 | `info.md` | RESOLVED — covered by gap #8 |
| 10 | `internal/api/handlers.go` (`GetLeaderboard`) | RESOLVED (Phase 0 hygiene) — replaced O(n²) bubble sort with `sort.Slice` descending by `TotalPoints` |
| 11 | `internal/verification/verifier.go` | RESOLVED (Phase 3) — replaced the single `*rpc.Client` trusted source with a quorum (`*rpc.QuorumClient`) per [ADR-0009](adr/0009-quorum-trusted-rpc.md). `TRUSTED_RPCS` is the new env var; `TRUSTED_RPC` (singular) is treated as a one-element list with a deprecation warning |
| 12 | `internal/api/handlers.go` + `internal/store/*` | RESOLVED (Phase 3) — anti-replay nonces enforced on `/nodes/register` and `/challenges/submit`. Signed message format now binds `Nonce: …`; server records `(wallet, nonce)` with 10-minute TTL via `store.ConsumeNonce`. Replays return 409 Conflict. Conformance suite covers both backends (`runConsumeNonce*`) |
| 13 | `internal/rpc/ssrf.go` + `internal/api/handlers.go` (`RegisterNode`) | RESOLVED (Phase 3) — operator-supplied `RPCEndpoint` is validated against an SSRF allow-list before persistence. Reject schemes other than http/https, RFC1918 / loopback / link-local / unique-local IPs, and `.local` / `.internal` / `.corp` / `.home` / `.lan` host suffixes. Override with `ALLOW_PRIVATE_RPC=1` for dev. DNS rebinding is not fully prevented — that's a known follow-up for the outbound `http.Client` |
| 14 | `internal/api/handlers.go` (`verifySignature`) | RESOLVED (Phase 0 hygiene) — replaced the hand-rolled hex byte loop and the `hexToByte` helper with `encoding/hex.DecodeString` and a length check (`len(sigBytes) == 65`). EIP-191 prefix and v-byte normalisation unchanged |
| 15 | `internal/api/router.go` | RESOLVED (Phase 3) — replaced wildcard `Access-Control-Allow-Origin: *` with an env-driven allow-list (`CORS_ALLOWED_ORIGINS`). Default is empty, which causes the middleware to never set the header — set the env var to your frontend origin |
| 16 | `internal/challenge/generator.go` | RESOLVED (Phase 3) — `*rand.Rand` is now mutex-protected; concurrent `GenerateChallenge` is safe under `-race`. Phase 2's workaround (`RPCWorkers=1` in tests) was removed |

> **Logger**: `log/slog` (stdlib, Go 1.21+). Honors `LOG_LEVEL` (`debug|info|warn|error`, case-insensitive, default `info`) and `LOG_FORMAT` (`json` selects the JSON handler, anything else / unset selects text). Configured once at boot in `cmd/server/main.go`; all packages call `slog.Default()` (e.g. `slog.Warn(...)` in `internal/verification/verifier.go`). The `log` package is no longer imported anywhere in the server tree.

> **Tests**: a single conformance suite at `internal/store/conformance.go` is run against both `MemoryStore` and `SQLiteStore` from their respective `*_test.go` entry points so the two implementations cannot drift. End-to-end flow is exercised in `internal/integration/integration_test.go`. Run via `go test ./... -race -count=1` (the `-race` flag must stay clean across the full run).

## 10. Rewards (Phase 4, ADR-0008)

Phase 4 lands the Merkle snapshot reward flow described in [ADR-0008](adr/0008-merkle-snapshot-rewards.md). The off-chain backend periodically (manually for now) aggregates points per wallet and publishes a Merkle root; operators submit a Merkle proof to a Solidity Distributor on-chain to claim BNB. Constant gas per claim, no per-event tx cost, anyone can re-derive the tree from the published proofs and verify the root.

### 10.1 Encoding contract (CRITICAL)

The Distributor uses OpenZeppelin's `MerkleProof.verify`. Mismatched encoding produces silently-failing proofs. The implementation therefore commits to:

- **Leaf encoding**: `keccak256(abi.encodePacked(address, uint256))` — that is, `20 bytes (raw address) || 32 bytes (uint256, big-endian, zero-padded)`. *Not* `abi.encode` (which would 32-pad the address).
- **Sorted leaves**: ascending by raw address bytes (NOT lexicographic hex strings).
- **Sorted pairs**: at every internal node, `hashPair(a, b) = keccak256(min(a,b) || max(a,b))`. The proof is therefore commutative — OpenZeppelin's `MerkleProof.verify` does not carry left/right bits.

`internal/reward/merkle.go` is the single source of truth for these rules. Tests in `internal/reward/merkle_test.go` pin every invariant down (including a regression test that fails if someone "fixes" the encoder to use `abi.encode`).

### 10.2 Cycle aggregation (Option A)

Phase 4 ships **Option A**: the leaf amount is the wallet's *lifetime* point total (`SUM(TotalPoints)` across all of the wallet's nodes, excluding banned ones). The Solidity Distributor is responsible for "amount already claimed per wallet per cycle" state — re-publishing the same wallet with a higher lifetime total just lets them claim the difference.

Option B (per-cycle deltas: `points-since-last-snapshot` per wallet) is a future refinement. Revisit when governance lands.

### 10.3 Persistence

Two SQLite tables (in `001_initial.sql`):

- `snapshots` — one row per published cycle. `cycle_id` is opaque text (caller's choice; the snapshot job picks the format, e.g. `cycle-7` or `2026-W18`). `root` and `total_amount` are stored as `0x`-prefixed hex strings so they round-trip cleanly through TEXT columns regardless of size.
- `snapshot_proofs` — one row per wallet per cycle, holding `(amount TEXT, proof_json TEXT)`. The proof is a JSON array of `0x`-prefixed sibling hashes.

`SaveSnapshot` is idempotent on `cycle_id`: re-publishing the same cycle replaces the prior row + every proof inside one BEGIN IMMEDIATE transaction.

The conformance suite (`internal/store/conformance_snapshots.go`) exercises both backends so the in-memory and SQLite implementations cannot drift.

### 10.4 API

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/admin/snapshot/publish` | Build + persist a cycle. Body: `{"cycle_id": "...", "ipfs_cid": "..."}`. Admin-only |
| GET | `/api/snapshots/latest` | Most recently published cycle's metadata (no proofs) |
| GET | `/api/snapshots/:cycleId` | Specific cycle's metadata |
| GET | `/api/wallet/:walletAddress/claim/:cycleId` | `{wallet, cycle_id, root, amount, proof}` for an on-chain claim |
| GET | `/api/wallet/:walletAddress/claim/latest` | Convenience: latest cycle + this wallet's proof |

Wallet lookups are case-insensitive — the store normalises to lower-case before persistence.

### 10.5 Out of scope (still)

- The Solidity Distributor contract. ADR-0008 §Decision pins this to a separate repo. This service does not call contracts.
- The funding wallet for the reward pool. That belongs to the operations runbook.
- The cron job that ticks `SNAPSHOT_INTERVAL`. Phase 4 keeps publishing manual; `cmd/server/main.go` recognises the env var and logs "inactive in Phase 4" so operators know it's reserved for Phase 5.
- IPFS publishing of the leaf set. The `ipfs_cid` field exists on the snapshot row + the publish request body, but no actual IPFS client is wired up — operators who want the trustless re-derive-and-verify story have to publish the JSON externally and pass the resulting CID into the publish call.

## 12. Observability (Phase 5)

Phase 5 lands the operator-facing observability surface: Prometheus metrics, deep-readiness probe, and the snapshot cron that finally wires `SNAPSHOT_INTERVAL` from §10's reserved placeholder into a live ticker.

### 12.1 Endpoints

| Method | Path | Purpose | Middleware |
|---|---|---|---|
| GET | `/metrics` | Prometheus scrape (text exposition) | **none** — no rate limit, no CORS |
| GET | `/health` | Shallow liveness (`{"status":"ok"}`) | global rate limit only |
| GET | `/ready` | Deep readiness with per-check breakdown | global rate limit only |

`/metrics` is mounted on the API listener (port `PORT`, default 3000) rather than a sidecar :9090 listener. Single-port keeps the binary trivial to deploy; if a deployment grows past one operator team it's a one-line change to split into two `*http.Server` instances. `internal/api/router.go` registers `/metrics` BEFORE the global middleware stack so scrapes are unrestricted.

### 12.2 Metrics families

All metrics are prefixed `depin_`. Counters end in `_total`. Histograms end in `_seconds` for time-based observations and `_ms` for explicit millisecond observations (verifier already operates in milliseconds; we don't want to pay float conversions on every observation).

| Family | Type | Labels |
|---|---|---|
| `depin_challenges_total` | counter | `result` (passed/failed/abort), `method` (exposed-rpc/local-prover), `node_type` |
| `depin_quorum_failures_total` | counter | — |
| `depin_rate_limit_429_total` | counter | `route` (global/strict/wallet_*) |
| `depin_nonce_replays_total` | counter | — |
| `depin_ssrf_rejections_total` | counter | — |
| `depin_snapshots_published_total` | counter | — |
| `depin_snapshot_build_failures_total` | counter | — |
| `depin_claim_lookups_total` | counter | `result` (found/not_found) |
| `depin_suspicious_events_total` | counter | `kind` (latency/answer_mismatch/missing_node/other) |
| `depin_admin_actions_total` | counter | `action` (clear/warn/ban) |
| `depin_nodes_active` | gauge | — |
| `depin_nodes_by_status` | gauge | `status` (clean/warning/flagged/banned) |
| `depin_nodes_by_type` | gauge | `type` |
| `depin_last_snapshot_published_seconds` | gauge | — |
| `depin_scheduler_last_tick_seconds` | gauge | `ticker` (heartbeat/challenge/uptime/snapshot) |
| `depin_rpc_worker_inflight` | gauge | — |
| `depin_rpc_worker_capacity` | gauge | — |
| `depin_verification_latency_ms` | histogram | — |
| `depin_snapshot_build_seconds` | histogram | — |
| `depin_quorum_latency_ms` | histogram | — |
| `depin_http_request_seconds` | histogram | `route`, `method`, `status` (status_class) |

Bucket boundaries for `depin_verification_latency_ms` are aligned with the anti-cheat thresholds in §7 so a single histogram view can spot threshold drift.

### 12.3 Readiness checks

`/ready` returns 200 when all mandatory checks pass and 503 when any do not. The `degraded` rollup (snapshot stale, or trusted RPC reachable-but-not-all) returns 200 so the load balancer doesn't drain a pod that is only partially impaired.

| Check | Source | Failure semantics |
|---|---|---|
| `store` | `Store.Ping(ctx)` (1s deadline) | mandatory |
| `scheduler` | `depin_scheduler_last_tick_seconds{ticker=…}` | informational (zero ticks => zero timestamps; not a fail) |
| `trusted_rpc` | `QuorumClient.HealthCheck` (1s per endpoint) | mandatory; degraded when any endpoint reachable, fail when none |
| `snapshot` | `Store.GetLatestSnapshot()` | informational; `stale` when older than 14 days, `never` if no publish yet |

### 12.4 Snapshot cron

Phase 5 retires the §10.5 reservation: `SNAPSHOT_INTERVAL` is now live. Configuration:

| Var | Default | Behaviour |
|---|---|---|
| `SNAPSHOT_INTERVAL` | `168h` (weekly per ADR-0008) | parsed via `time.ParseDuration`; `0` / `off` / `false` / `disabled` / `no` disables the cron without disabling the rest of the scheduler |

Cycle id format: `cycle-<RFC3339 UTC>` (e.g. `cycle-2026-05-09T15:00:00Z`). On `BuildCycle` returning `ErrNoEarners` the cron logs at info level and continues; on any other error it bumps `depin_snapshot_build_failures_total` and continues to the next tick. Manual publish via `POST /api/admin/snapshot/publish` remains supported.

### 12.5 New env vars

| Var | Default | Notes |
|---|---|---|
| `SNAPSHOT_INTERVAL` | `168h` | See §12.4 |
| `METRICS_ENABLED` | `true` | Reserved — currently always on. Operators who don't want `/metrics` exposed should put the API behind a path-allowlisting reverse proxy |

## 11. Open questions

- Persistent store: Postgres vs. SQLite vs. embedded KV (Bolt/Badger)? Postgres if we expect multi-instance; SQLite if single-instance is fine for now
- Should `TRUSTED_RPC` be plural (a quorum of trusted RPCs) so a single bad reference can't poison verification?
- How do we rotate `AuthToken` if leaked?
- How are challenges expired and garbage-collected from the in-memory store at scale?
