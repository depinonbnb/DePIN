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

```bash
# Server
PORT=3000
TRUSTED_RPC=https://bsc-dataseed1.binance.org

# Prover
PROVER_PRIVATE_KEY=your_key
NODE_RPC=http://localhost:8545
DEPIN_API=http://localhost:3000/api
NODE_TYPE=bsc-full
```

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
