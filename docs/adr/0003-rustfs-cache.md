# ADR 0003: RustFS for S3-compatible caches

- Status: accepted, pinned canary; production promotion blocked
- Date: 2026-08-08

## Context

GitHub `actions/cache` does not become host-local merely because a runner is
self-hosted. Compiler and content-addressed caches need a low-latency local
object service. The user selected RustFS for the S3-compatible plane.

## Decision

Use RustFS for sccache and content-addressed build objects. Keep OCI distribution
and BuildKit registry cache in a separate minimal Zot registry. Credentials are
runtime locators, never repository values. Namespaces include repository and
trust dimensions; release jobs cannot write shared cache entries.

The exact canary artifacts and their promotion state live in
`config/cache-artifacts.yaml`. RustFS `1.0.0-rc.1` is permitted only in an
isolated canary until a stable artifact passes the same supply-chain, IAM,
integrity and crash-recovery gates.

## Consequences

- RustFS crash recovery, upgrade, quota and integrity behavior must be tested on
  the actual storage topology before production.
- No floating `latest` artifact is permitted.
- Cache objects remain disposable performance aids, not authoritative artifacts.
- GC and low-disk admission are required from the first rollout.
- Root credentials never enter workers. Repository/trust-specific long-lived
  IAM users are used instead of STS or service accounts.
