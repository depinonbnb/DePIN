# 5. Trusted RPC as the source of truth

Date: 2026-05-07

## Status

Superseded by [0009](0009-quorum-trusted-rpc.md)

## Context

To know whether an operator's answer to a challenge is correct, we need a reference. Options:
- **Run our own full node.** Most authoritative; expensive (16+ vCPU, 32GB RAM, multi-TB SSD per the README). Operationally heavy.
- **Trust a single public RPC** (e.g. `bsc-dataseed1.binance.org`). Simple, free, but a single point of failure / influence.
- **Quorum of public RPCs.** Compare answers across N reputable endpoints; tolerate disagreement up to threshold. More complex but resistant to a bad actor at any single provider.

For a pre-launch testnet system with low traffic and no real money on the line, a single trusted RPC is good enough.

## Decision

Configure a single trusted endpoint via `TRUSTED_RPC` (default `https://bsc-dataseed1.binance.org`). All challenge answers are compared against this endpoint.

## Consequences

- Simple. Zero operational overhead.
- **If `TRUSTED_RPC` lies, every honest operator gets marked as cheating.** This is fine while we're the only people running this; not fine when real rewards are on the line
- Before mainnet launch this should be replaced with a quorum approach (e.g. 2-of-3 across `bsc-dataseed{1,2,3}.binance.org`) — supersede this ADR at that point
- Tracked as open question in [../SPEC.md](../SPEC.md) §10
