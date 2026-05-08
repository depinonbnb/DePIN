# 1. Record architecture decisions

Date: 2026-05-07

## Status

Accepted

## Context

We need to capture the reasoning behind significant architectural choices so future contributors (and agents) can understand *why* the code looks the way it does, not just what it does. Comments in code answer "what"; ADRs answer "why".

## Decision

Use Architecture Decision Records (ADRs) as described in [Documenting Architecture Decisions](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions). Records live under `docs/adr/`, are numbered sequentially, and are append-only.

## Consequences

- A grep-able history of design choices lives in the repo
- Decisions are visible during code review (PRs touching a major design get an accompanying ADR)
- Slight up-front cost: every non-trivial design choice deserves a short ADR
