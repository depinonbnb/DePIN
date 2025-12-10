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
src/
├── api/            # Backend API endpoints
├── verification/   # Challenge generation and verification logic
├── prover/         # Open-source script users run locally
├── store/          # Data storage
├── types/          # TypeScript types
└── index.ts        # Server entry point
```

## Setup

```bash
npm install
npm run dev
```

## Run the local prover

```bash
npm run prover -- --private-key YOUR_KEY --node-rpc http://localhost:8545
```

## Website

The web interface will be available at [bnb-depin.site](http://bnb-depin.site/)

Source code is open source: [github.com/depinonbnb/DePIN-Web](https://github.com/depinonbnb/DePIN-Web)

## Links

- [Website](http://bnb-depin.site/)
- [Website Source Code](https://github.com/depinonbnb/DePIN-Web)
- [BNB Chain Node Docs](https://docs.bnbchain.org/bnb-smart-chain/developers/node_operators/full_node/)
- [opBNB Node Docs](https://docs.bnbchain.org/bnb-opbnb/advanced/local-node/)
