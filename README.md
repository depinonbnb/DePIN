# DePIN on BNB

A decentralized physical infrastructure network (DePIN) that rewards users for running BNB Chain nodes.

## What is this?

This project incentivizes people to run BNB Chain nodes by rewarding them with BNB. More nodes = stronger, more decentralized network.

The BNB token charges transaction fees. These fees go into a vault which is used to pay users who host nodes.

Users download the official BNB node from [bnb-chain/bsc](https://github.com/bnb-chain/bsc), sync it, and earn rewards for keeping it online.

## Supported Nodes

| Node Type | Chain | Reward Tier |
|-----------|-------|-------------|
| BSC Full Node | BNB Smart Chain | High |
| BSC Fast Node | BNB Smart Chain | Medium |
| BSC Archive Node | BNB Smart Chain | Highest |
| opBNB Full Node | opBNB (L2) | Medium |
| opBNB Fast Node | opBNB (L2) | Standard |

*Reward tiers may change in the future based on network needs.*

## How Verification Works

We verify nodes are real and synced using a challenge-response system:

1. Server sends a random challenge (e.g., "what's the hash of block #38291847?")
2. Only a real synced node can answer correctly
3. Pass challenges = earn rewards

Two verification methods:

- **Exposed RPC (Recommended)** - You expose an RPC endpoint so we can query your node directly. This is the easiest option.
- **Local Prover** - You download and run an open-source script that submits proofs on your behalf. You can review all the code before running it.

## Operator quickstart

If you just want to run a node and earn:

1. **Sync a BNB Chain node.** Pick a type from the table above and follow the official [BSC](https://docs.bnbchain.org/bnb-smart-chain/developers/node_operators/full_node/) or [opBNB](https://docs.bnbchain.org/bnb-opbnb/advanced/local-node/) docs.
2. **Register your node.** Sign a registration message with your wallet and `POST /api/nodes/register` (or use the web frontend). You'll get back a node ID and auth token.
3. **Pick a verification path.**
   - **Exposed RPC** — make your node's JSON-RPC reachable from the server, set `rpc_endpoint` during registration, and you're done. The server probes you every few minutes.
   - **Local Prover** — install [`cmd/prover`](cmd/prover/), set `PROVER_PRIVATE_KEY` to your wallet key, and let it poll `/api/challenges/request` on your behalf.
4. **Stay online.** Points accrue from heartbeats + passed challenges. See "Anti-cheat" in [docs/SPEC.md §7](docs/SPEC.md) for the latency thresholds — proxying via public RPCs gets you flagged.
5. **Claim rewards** — see below.

## Earning and claiming rewards

Points accrue continuously per [ADR-0008](docs/adr/0008-merkle-snapshot-rewards.md). On a fixed cadence (default weekly, set by `SNAPSHOT_INTERVAL`), the server publishes a **Merkle snapshot** of lifetime points per wallet. Operators claim BNB by submitting a Merkle proof to the on-chain Distributor contract.

To fetch your proof for the current cycle:

```bash
curl http://localhost:3000/api/wallet/<YOUR_WALLET>/claim/latest
```

Response gives you `{root, amount, proof}` — pass these to the Distributor's `claim()` call. The on-chain contract lives in a separate repo (per ADR-0008 §10.5).

See [SPEC §10](docs/SPEC.md) for the leaf encoding contract (`keccak256(abi.encodePacked(address, uint256))`, sorted leaves, sorted pairs) — don't reimplement without reading it.

## What's in this repo

```
cmd/
├── server/         # Main API server
└── prover/         # Open-source prover script

internal/
├── api/            # HTTP handlers, routing, middleware, rate limiting
├── challenge/      # Challenge generation (block hash, balance, sync, etc.)
├── rpc/            # JSON-RPC client + trusted-RPC quorum + SSRF guard
├── scheduler/      # Heartbeat, challenge, uptime, and snapshot tickers
├── store/          # Store interface + memory and SQLite backends
├── reward/         # Merkle snapshot builder (ADR-0008)
├── metrics/        # Prometheus metrics
├── verification/   # Verification + anti-cheat
├── types/          # Shared types and constants
└── integration/    # End-to-end tests (router + sqlite + fake RPC)
```

## Setup

```bash
# Install dependencies
go mod download

# Run the server
go run cmd/server/main.go

# Or build it
go build -o server cmd/server/main.go
./server
```

## Run the local prover

```bash
go run cmd/prover/main.go --private-key YOUR_KEY --node-rpc http://localhost:8545

# Or build it
go build -o prover cmd/prover/main.go
./prover --private-key YOUR_KEY
```

## Testing

```bash
go test ./... -race -count=1
```

The `-race` flag is required — the project guarantees a race-clean test suite. A single conformance suite (`internal/store/conformance.go`) runs against both the memory and SQLite backends so they cannot drift, and `internal/integration/` runs the full router end-to-end.

## Environment Variables

Authoritative list lives in [`.env.example`](.env.example) and `docs/SPEC.md` §8. The summary below groups vars by what they control. Defaults are used when the var is unset; **bold** vars must be set in production.

### Server — networking and access control

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `3000` | HTTP listener port |
| **`CORS_ALLOWED_ORIGINS`** | (empty) | Comma-separated list of allowed browser origins. **Empty = no `Access-Control-Allow-Origin` header is ever sent, which blocks the web frontend.** Set to e.g. `http://localhost:5173,https://yourdomain.com` |
| **`ADMIN_API_KEY`** | (empty) | API key for admin endpoints. **Empty = admin routes return 503.** Use `openssl rand -hex 32` to generate. Never commit |
| `ALLOW_PRIVATE_RPC` | `0` | When `1`, disables the SSRF guard on operator-supplied `rpc_endpoint`. Use only for local docker-compose / dev |

### Server — verification (trusted RPC quorum)

| Var | Default | Purpose |
|---|---|---|
| **`TRUSTED_RPCS`** | (none) | Comma-separated quorum of trusted RPC endpoints (ADR-0009). Majority answer is treated as truth. Recommended: 3 BSC dataseeds, e.g. `https://bsc-dataseed1.binance.org,https://bsc-dataseed2.binance.org,https://bsc-dataseed3.binance.org` |
| `TRUSTED_RPC` | (none) | **Deprecated.** Legacy single endpoint (ADR-0005). Treated as a one-element quorum with a deprecation warning on boot. Drop after one release cycle |

### Server — storage

| Var | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | SQLite at default path | Preferred. SQLite DSN. `"memory"` selects the in-memory store (tests/dev). Anything else is a SQLite DSN |
| `DB_PATH` | — | Legacy alternative to `DATABASE_URL` |

### Server — scheduler intervals (Phase 2, ADR-0007)

All three tickers share one bounded worker pool sized by `RPC_WORKERS`. Set `SCHEDULER_ENABLED=false` to construct without ticking (used by tests/dev).

| Var | Default | Purpose |
|---|---|---|
| `SCHEDULER_ENABLED` | `true` | `false`/`0`/`no`/`off` skips ticker start |
| `HEARTBEAT_INTERVAL` | `5m` | Cadence for the heartbeat probe |
| `CHALLENGE_CHECK_INTERVAL` | `1m` | Outer loop for the challenge dispatcher (per-node gating uses `NodeType.ChallengeFrequencyMinutes()`) |
| `REWARD_INTERVAL` | `5m` | Cadence for the uptime-reward ticker |
| `RPC_WORKERS` | `50` | Upper bound on concurrent outbound RPC operations |

### Server — rewards (Phase 4–5, ADR-0008)

| Var | Default | Purpose |
|---|---|---|
| `SNAPSHOT_INTERVAL` | `168h` (weekly) | Cycle cadence for the snapshot cron. `0`/`off`/`false`/`disabled`/`no` disables. Manual publish via `POST /api/admin/snapshot/publish` still works |

### Server — observability (Phase 5)

| Var | Default | Purpose |
|---|---|---|
| `METRICS_ENABLED` | `true` | Reserved — currently always on |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`, case-insensitive |
| `LOG_FORMAT` | text | `json` selects the JSON handler; anything else / unset selects text |

### Prover (operator-side CLI)

| Var | Default | Purpose |
|---|---|---|
| **`PROVER_PRIVATE_KEY`** | (none) | Operator's wallet private key. **Never commit** |
| `NODE_RPC` | `http://localhost:8545` | Operator's local node RPC |
| `DEPIN_API` | `http://localhost:3000/api` | Server URL |
| `NODE_TYPE` | `bsc-full` | Self-declared node type. One of: `bsc-full`, `bsc-fast`, `bsc-archive`, `opbnb-full`, `opbnb-fast` |
| `INTERVAL` | `300000` | Submit interval in ms (5 min) |

## Architecture and design docs

- [docs/SPEC.md](docs/SPEC.md) — full system specification: API surface, data model, verification protocol, anti-cheat rules, schedulers, rewards, observability
- [docs/adr/](docs/adr/) — architecture decision records (ADR-0001 through ADR-0009 covering Go/Gin, in-memory then SQLite store, dual verification modes, scheduler-driven verification, Merkle snapshot rewards, trusted-RPC quorum)
- [docs/design/persistence.md](docs/design/persistence.md) — store interface and migration design

The SPEC is the source of truth for behavior. The README is the operator-facing front door; if the two disagree, the SPEC wins and the README is the bug.

## Website

The web interface will be available at [bnb-depin.site](http://bnb-depin.site/)

Source code is open source: [github.com/depinonbnb/DePIN-Web](https://github.com/depinonbnb/DePIN-Web)

## Links

- [Website](http://bnb-depin.site/)
- [Website Source Code](https://github.com/depinonbnb/DePIN-Web)
- [BNB Chain Node Docs](https://docs.bnbchain.org/bnb-smart-chain/developers/node_operators/full_node/)
- [opBNB Node Docs](https://docs.bnbchain.org/bnb-opbnb/advanced/local-node/)

## Why Go?

We originally started with TypeScript and considered Rust, but ended up going with Go. Here's why:

- **Simpler code** - Go is easy to read and write. No complex ownership rules or async headaches.
- **Fast builds** - Compiles in seconds, not minutes.
- **Single binary** - Just build and run. No node_modules, no runtime dependencies.
- **Great for networking** - Go was built for this kind of stuff. Goroutines make concurrent RPC calls easy.
- **go-ethereum** - The official Ethereum/BNB client is written in Go, so the ecosystem is solid.

For a verification system that talks to nodes over RPC, Go hits the sweet spot between simplicity and performance.
