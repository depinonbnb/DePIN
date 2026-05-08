# 9. Quorum of trusted RPCs

Date: 2026-05-08

## Status

Accepted (supersedes [0005](0005-trusted-rpc-as-truth.md))

## Context

[ADR-0005](0005-trusted-rpc-as-truth.md) accepted a single `TRUSTED_RPC` endpoint as the source of truth for verification. It also flagged its own expiry: *"Before mainnet launch this should be replaced with a quorum approach (e.g. 2-of-3 across `bsc-dataseed{1,2,3}.binance.org`) — supersede this ADR at that point."*

We are now approaching that point. With [0007](0007-scheduler-driven-verification.md) automating verification and [0008](0008-merkle-snapshot-rewards.md) turning verification results into on-chain rewards, the cost of a wrong answer from the trusted source goes from "embarrassing log line" to "operators lose real BNB they earned." Concrete failure modes a single source exposes us to:

- The single endpoint serves a stale block height during reorg / catchup, marking honest synced nodes as cheating.
- The single endpoint goes down for an hour; verification halts globally.
- A provider serves intentionally bad data to a subset of clients (unlikely with Binance dataseeds, very real on third-party RPCs).

The fix is well-understood: take the majority answer from a small set of independent reference endpoints.

## Decision

Replace `TRUSTED_RPC` (single string) with `TRUSTED_RPCS` (comma-separated list).

- **Default value**:
  - For BSC: `https://bsc-dataseed1.binance.org,https://bsc-dataseed2.binance.org,https://bsc-dataseed3.binance.org`
  - For opBNB (used when verifying opBNB-tier nodes): `https://opbnb-mainnet-rpc.bnbchain.org,https://opbnb-rpc.publicnode.com,https://opbnb.publicnode.com`
  - Selected per-challenge based on the node's `NodeType` (BSC tiers query BSC quorum, opBNB tiers query opBNB quorum).
- **Quorum rule**: query all configured endpoints in parallel, with the same per-call timeout used elsewhere (5 s). Group answers; the **majority** answer is the truth. With 3 endpoints, that's 2-of-3.
- **No-majority case**: if no answer reaches a strict majority (e.g. all 3 disagree, or only 1 of 3 responded), **abort the verification** for that challenge. Record the disagreement in a new `quorum_failures` table and skip — do **not** mark the node as cheating, do **not** mark it as healthy. This is a reference-side problem, not an operator-side problem; punishing operators for our reference flakiness was the failure mode that motivated this ADR.
- **Disagreement logging**: when a minority endpoint disagrees with the majority, log it at `WARN` with endpoint URL, block height, and divergent answers. Repeated disagreement from one endpoint is a signal to drop it from the quorum.
- **Backwards compatibility**: if `TRUSTED_RPC` (singular) is set and `TRUSTED_RPCS` is not, treat it as a one-element list and emit a deprecation warning on boot. Drop support after one release cycle.
- **Configurability of quorum size**: implicit in the list length. We do not add a `QUORUM_THRESHOLD` env var; majority of N is always `floor(N/2) + 1`. If we need weighted quorum later, it's a follow-up ADR.
- **Update [ADR-0005](0005-trusted-rpc-as-truth.md)**: change its Status line to `Superseded by [0009](0009-quorum-trusted-rpc.md)`. Don't delete or rewrite — append-only history.

## Consequences

- **No single point of failure / influence.** Two of three endpoints have to agree before we accept an answer. A single rogue or stale endpoint cannot poison verification.
- **Outbound RPC traffic 3x.** Each verification call now fans out to three endpoints. Combined with [0007](0007-scheduler-driven-verification.md)'s ~43 calls/min steady state, that's ~130 calls/min to the BSC quorum. Public dataseeds rate-limit at hundreds of req/sec; we're nowhere near that.
- **Latency = max of the three calls** (we wait for majority, which usually means waiting for the slowest of the first two agreeing). Slightly worse than single-source p50; better worst-case because one slow endpoint doesn't stall us if the other two agree.
- **Verification can now correctly say "I don't know."** The no-majority abort case is a new, third outcome alongside pass/fail. The scheduler handles it as "skip and re-queue at next interval", not as a cheat signal. This is a correctness improvement worth the extra complexity.
- **Operators stop being punished for our reference's bad days.** The original failure mode in [ADR-0005](0005-trusted-rpc-as-truth.md) is closed.
- **Disagreement metrics become a useful signal.** A long-tail of disagreements from one endpoint tells us to swap providers. We get this for free from the new `quorum_failures` table.
- **The `rpc.Client` in `internal/rpc/client.go` needs a fan-out wrapper.** New type, e.g. `internal/rpc/quorum.go`, that takes `[]Client` and exposes the same per-method surface but returns `(answer, agreement_ratio, error)`.
- **Tests**: the quorum logic must have unit tests for every edge case — all-agree, 2-of-3 agree, all-disagree, one timeout + two agree, two timeouts, etc. Easy to get this wrong.
- Addresses SPEC §10 open question #2.

## Notes for downstream agents

- Do not assume `len(TRUSTED_RPCS) >= 3`. The quorum logic must work for 1, 2, 3, 5, etc. With 1 endpoint there's no quorum and we silently degrade to ADR-0005 behavior (acceptable for local dev, dangerous in prod — log a warning).
- Comparing answers must be type-aware. `eth_blockNumber` returns hex strings; normalize before comparing. Block hashes are case-insensitive hex; lowercase before comparing. Reorg-prone calls (head block) need a small acceptance window — if endpoints disagree by 1 block, treat as agreement on the lower height.
- Do not parallelize the quorum across goroutines without bounding it — reuse the worker pool from [0007](0007-scheduler-driven-verification.md).
