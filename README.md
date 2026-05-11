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

## What's in this repo

```
cmd/
├── server/         # Main API server
└── prover/         # Open-source prover script

internal/
├── api/            # HTTP handlers and routing
├── challenge/      # Challenge generation
├── rpc/            # RPC client for talking to nodes
├── store/          # Data storage
├── types/          # Type definitions
└── verification/   # Verification logic
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
