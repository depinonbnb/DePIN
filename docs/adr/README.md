# Architecture Decision Records

Records of architecturally significant decisions made on this project.

Format: [Michael Nygard's template](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions). One file per decision, numbered sequentially: `NNNN-kebab-title.md`.

Each ADR has:
- **Status** — proposed / accepted / superseded by NNNN
- **Context** — what's the situation that forces a decision
- **Decision** — what we chose
- **Consequences** — what follows from this, both good and bad

ADRs are append-only. To revisit, write a new ADR that supersedes the old one rather than editing history.

## Index

- [0001](0001-record-architecture-decisions.md) — Record architecture decisions
- [0002](0002-go-gin-stack.md) — Go + Gin for the API server
- [0003](0003-in-memory-store.md) — In-memory store for the first iteration
- [0004](0004-dual-verification-modes.md) — Dual verification modes (exposed-RPC and local-prover)
- [0005](0005-trusted-rpc-as-truth.md) — Trusted RPC as the source of truth *(superseded by 0009)*
- [0006](0006-sqlite-mvp.md) — SQLite + WAL for MVP persistence
- [0007](0007-scheduler-driven-verification.md) — Scheduler-driven verification
- [0008](0008-merkle-snapshot-rewards.md) — Merkle snapshot rewards
- [0009](0009-quorum-trusted-rpc.md) — Quorum of trusted RPCs
