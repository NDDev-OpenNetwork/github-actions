# ADR 0015: Event-driven GARM reconciliation

## Status

Accepted for canary deployment. Production latency and reliability gates remain
open until the runtime evidence in the roadmap is complete.

## Context

Upstream GARM `v0.2.1` waits on two independent five-second tickers after a
Scale Set queue message: the Scale Set worker waits before creating its durable
instance record, then the provider instance manager waits before invoking the
provider. A separate one-second delay checks an already-populated GitHub tools
cache. With an unregistered warm VM these waits dominate orchestration latency;
they do not add isolation or compatibility.

The latest upstream release remains `v0.2.1`, and upstream `main` at
`afda4e76f1808e8b41d72edd0c17f99a51d84758` still contains both five-second
tickers. Replacing the official Actions runner or the Scale Set protocol is out
of scope.

## Decision

Maintain a minimal derivative `v0.2.1-nddev.1`:

- a buffered, coalescing wake channel reacts to durable Scale Set watcher
  updates;
- a second buffered wake channel starts provider instance reconciliation on
  manager start and instance updates;
- the existing five-second tickers remain as convergence safety nets;
- the GitHub tools cache is checked immediately and retried once per second
  only while unavailable;
- no Actions YAML, runner protocol, runner binary, provider interface, database
  schema or persisted state changes.

The patch is applied only to exact upstream commit
`154638445c3949c1958b01812f69d9a1e4d82684`. Its build has no network access,
uses vendored modules and a digest-pinned Go container, runs the full upstream
race suite, and must produce two byte-identical binaries. The resulting binary
requires at most `GLIBC_2.34`, below the server's glibc `2.39`.

## Consequences

Watcher delivery becomes the normal fast path, while missed or coalesced
signals still converge through periodic reconciliation. The derivative adds a
small patch-maintenance obligation, isolated to provisioning and scheduling as
recommended by the architecture review.

Deployment is canary-first. Keep the previous upstream binary by digest for
rollback, restart only `garm.service`, prove database migration is absent, and
verify existing instances, all twelve retained listeners, ExamplePlatform and Captcha
before and after the restart. The change is not considered successful until
nominal-load samples demonstrate the warm-path target without new orphan or
failure behavior.
