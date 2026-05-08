# 8. Merkle snapshot rewards

Date: 2026-05-08

## Status

Accepted

## Context

The README promises operators that uptime translates into on-chain BNB rewards. The current code accrues a `Points` integer per node, but there is no path from that integer to anyone's wallet. SPEC gap #6 records this: *"No actual reward distribution. Points are tracked but not claimable on-chain."*

Three obvious approaches:

1. **Per-event on-chain accounting.** Every heartbeat, challenge result, or point grant is a transaction. Trustless but cost-prohibitive — at 5-minute heartbeats with 100 nodes, that's 28,800 txs/day on BNB Chain. Even at low BNB gas, the operational cost dwarfs the rewards being distributed.
2. **Centralized payouts.** The backend keeps an internal ledger and triggers manual or scheduled BNB transfers. Cheap but contradicts the trustless DePIN story; operators have to trust us not to lie about their points.
3. **Off-chain points + periodic Merkle snapshot + on-chain claim.** The same pattern as Uniswap, Optimism, and most token airdrops. Backend computes who-earned-what, builds a Merkle tree of (wallet, amount), publishes the root on-chain, operators submit a Merkle proof to a Distributor contract to claim. Constant gas per claim, no per-event tx cost, anyone can re-derive the tree from public data and verify.

Approach 3 wins on cost, decentralization story, and prior art.

## Decision

Off-chain accumulation with periodic Merkle snapshots:

- **Cycle length**: weekly. A `RewardCycle` row records `cycle_id`, `start_at`, `end_at`, `merkle_root`, `total_points`, `published_at`.
- **Snapshot job**: runs once per cycle (cron-like ticker, separate from the schedulers in [0007](0007-scheduler-driven-verification.md)). Steps:
  1. Freeze the cycle's points (`SELECT wallet, SUM(points) WHERE earned_at BETWEEN start AND end GROUP BY wallet`).
  2. Convert points to BNB amounts using the cycle's reward pool size (env-configurable per-cycle until governance exists).
  3. Build a sorted Merkle tree of `keccak256(abi.encodePacked(wallet, amount))` leaves. Sorted-leaf, sorted-pair convention so proofs match OpenZeppelin's `MerkleProof.verify`.
  4. Store the root, every leaf, and every operator's proof in two new tables: `reward_snapshots` (one row per cycle) and `reward_claims` (one row per wallet per cycle).
  5. Optionally publish the leaf set and tree to IPFS for public verification (gated by `IPFS_PUBLISH=true`).
- **API**:
  - `GET /rewards/:wallet/cycles` — list cycles the wallet earned in.
  - `GET /rewards/:wallet/proof/:cycleId` — return `{amount, proof: [bytes32...], root}` so the operator can submit it on-chain.
  - `GET /rewards/cycles/:cycleId` — public cycle metadata (root, total amount, IPFS CID if published).
- **On-chain Distributor**: a separate Solidity project, **not in this repo**. Standard pattern: `claim(uint256 cycleId, address account, uint256 amount, bytes32[] proof)` with `MerkleProof.verifyCalldata`. The repo for that contract is tracked separately; this ADR only commits the backend's side of the protocol.
- **Idempotency**: snapshot job is idempotent on `cycle_id`. Re-running on the same cycle re-derives the same root (deterministic leaf ordering by wallet ascending).
- **Failure mode**: if the cycle has zero earners, the snapshot job records `merkle_root = 0x00...00` and skips publishing. The on-chain Distributor must reject zero-root cycles.

## Consequences

- **O(1) gas per claim**, regardless of how many cycles or how many earners. Standard Merkle airdrop economics.
- **Operators control timing.** They can claim a single cycle or batch many cycles in one tx (Distributor accepts multiple proofs).
- **Re-derivable.** Anyone with the leaf set (published to IPFS or queryable from the API) can rebuild the tree and verify the published root. This is the trustless property approach 2 lacked.
- **Per-cycle reward pool sizing.** Until governance exists, this is an env var (`CYCLE_REWARD_BNB`). Document that this is centralized for now and replaceable later.
- **New work for downstream agents**:
  - A Merkle tree builder in `internal/rewards/merkle.go`. Existing Go libs (e.g. `github.com/wealdtech/go-merkletree`) are acceptable; sorted-pair OpenZeppelin compatibility is mandatory.
  - Two new SQLite tables ([0006](0006-sqlite-mvp.md) handles persistence) + migrations.
  - Three new HTTP handlers + tests.
  - The cycle scheduler (similar shape to [0007](0007-scheduler-driven-verification.md) but with a much longer interval).
- **Out of scope here**: the Solidity Distributor, the deploy keys, the funding of the reward pool wallet. Those belong to the contracts repo and the operations runbook respectively.
- **Privacy**: the leaf set leaks a public mapping of wallets to weekly BNB amounts. This is the same privacy posture as every public airdrop and is acceptable for a transparent rewards protocol. Document it on the operator-facing README.
- Closes SPEC gap #6.

## Notes for downstream agents

- Leaf encoding **must** be `keccak256(abi.encodePacked(address, uint256))`, NOT `abi.encode`. The Distributor will use `MerkleProof.verifyCalldata` with the same encoding; mismatched encodings produce silently-failing proofs.
- Sort leaves by wallet address ascending before building the tree. Sort pairs by byte order at every internal node. Deterministic ordering is non-negotiable for reproducibility.
- Do not roll your own Keccak — use `golang.org/x/crypto/sha3` or `go-ethereum`'s `crypto.Keccak256`.
- Snapshot tables are append-only. Never `UPDATE` a published cycle.
