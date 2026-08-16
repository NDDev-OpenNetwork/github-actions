# ADR 0004: Go for the initial control plane

- Status: accepted
- Date: 2026-08-08

## Context

The platform needs close integration with GARM and its providers, which are Go
projects. Correctness depends more on durable state, APIs, retries and operations
than on a custom high-throughput execution runtime.

## Decision

Implement policy, admission, lifecycle, reconciliation and GARM/Incus adapters
in Go. Keep packages narrow and dependency-light. Rust remains appropriate for
future isolated agents or a measured microVM backend, not as a reason to split
the initial control plane across languages.

## Consequences

- Upstream types and operational patterns are easier to integrate and review.
- One language reduces bootstrap and maintenance complexity.
- Performance work focuses on VM/image/cache/workflow paths where minutes are
  actually spent.
- A Rust component requires a measured boundary and independent justification.
