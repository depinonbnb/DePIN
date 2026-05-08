# 2. Go + Gin for the API server

Date: 2026-05-07 (retroactive — captures choices already in `go.mod`)

## Status

Accepted

## Context

The service must:
- Verify EIP-191 wallet signatures (cheap CPU, no GC pauses on the hot path)
- Make many concurrent JSON-RPC calls to BNB nodes (TRUSTED_RPC plus per-operator nodes)
- Run as a single static binary on a small VM, with a separate static binary for the operator-side prover

Trade-offs we considered:
- **Node.js / TypeScript**: Same language as the frontend, but signature verification via ethers.js is slower under concurrent load and the runtime is heavier than a Go binary
- **Rust**: Better performance ceiling than Go, but no compelling need and slower iteration for early-stage protocol code
- **Go**: First-class crypto support via `go-ethereum`, simple goroutine model for fan-out RPC, single static binary

The README explicitly calls out Go as the chosen language ("Why Go was chosen for this project"). This ADR makes the decision searchable.

## Decision

- **Language**: Go 1.21
- **HTTP framework**: `github.com/gin-gonic/gin` 1.9 — tradition, familiar middleware model, good ergonomics
- **Crypto / signature verification**: `github.com/ethereum/go-ethereum` (the official Go Ethereum client, used as a library)
- **Env loading**: `github.com/joho/godotenv`
- **IDs**: `github.com/google/uuid`

Module: `github.com/depinonbnb/depin`. Two binaries shipped from the same module: `cmd/server` (the API) and `cmd/prover` (operator CLI).

## Consequences

- One small binary per role; trivial to deploy and reason about
- Signature verification is fast and matches what BNB Chain itself uses
- Concurrent RPC fan-out is a single goroutine + channel away
- Cost: every TypeScript/JS engineer on the project pays a context-switch tax to contribute. Mitigated by a small, well-documented codebase
- Gin is fine for now, but if we ever need streaming responses or HTTP/2 push the framework choice should be revisited
