# 4. Dual verification modes (exposed-RPC and local-prover)

Date: 2026-05-07

## Status

Accepted

## Context

We need to verify that an operator is actually running a real BNB node. Two reasonable approaches exist, and neither alone is enough:

- **Exposed-RPC**: operator gives us a public JSON-RPC URL; we probe it. Easy, but plenty of operators won't expose their node publicly (DDoS risk, infra cost) so this excludes them.
- **Local-prover**: ship a CLI that runs on the same box as the node, signs answers, and submits them. Solves privacy, but harder UX and requires the operator to run another process.

Picking only one halves the addressable operator pool. Picking both doubles the verification surface but unlocks more operators.

## Decision

Support both modes interchangeably:

- Each `NodeRegistration` records its mode (presence of `RPCURL` => exposed-RPC).
- Both modes produce the same `VerificationResult` shape, so downstream (points, leaderboard, anti-cheat) is mode-agnostic.
- Exposed-RPC: server makes the JSON-RPC call directly and compares to `TRUSTED_RPC`.
- Local-prover: operator runs `cmd/prover`, polls `/challenges/request`, signs and POSTs to `/challenges/submit`. Server still compares answer to `TRUSTED_RPC`.

Both paths use EIP-191 message prefixes for any signed payload.

## Consequences

- Wider operator pool — privacy-conscious operators can participate
- Two attack surfaces to harden, not one
- Anti-cheat tuning is harder: latency profile of an exposed-RPC node and a local-prover node differ. Thresholds in `internal/types/types.go` need to consider both
- Local-prover requires the operator's wallet private key on the prover box (`PROVER_PRIVATE_KEY`). Documented in [../SPEC.md](../SPEC.md) §8 and the operator-facing README
