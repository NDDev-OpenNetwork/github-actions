# ADR 0029: Clock-safe direct-JIT latency evidence

- Status: accepted
- Date: 2026-08-10

## Context

The original latency harness subtracted a GARM host-journal timestamp from an
official runner diagnostic timestamp inside a VM restored from an immutable
image. A telemetry canary proved that the guest clock could lag the host: the
guest logged `ExecuteCommand` before the host had completed assignment
injection. The resulting 20-sample median and p95 remain useful historical
observations, but they do not prove a queue-to-online duration.

## Decision

Schema 2 latency evidence uses GitHub workflow-job `created_at` and `started_at`
as the only endpoints of the promotion metric. They are emitted by one
authoritative control-plane clock and directly represent the externally visible
queue-to-start outcome.

Internal attribution is recorded without subtracting clocks:

- GARM host timestamps measure assignment to provider start/completion;
- provider `duration_ms` uses Go's host monotonic clock;
- the direct-JIT assignment writes `assignment-script-started` and
  `runner-exec` nanoseconds to a secret-free `.log` in the official runner
  diagnostic directory; their difference is guest-local setup time;
- the official runner's session timestamp is retained only as supporting
  evidence and is never subtracted from a host timestamp.

Provider `v0.1.5-nddev.21` waits at most five seconds for the first guest marker
and validates its schema, owner, group, mode and maximum size. This closes the
host-to-guest delivery boundary before provider success. It does not wait for
the GitHub broker session and does not change the one-job execution engine.

No JIT configuration, token, credential, environment value or CA content is
written to the phase file. The diagnostic exporter already bounds and hashes
`.log` files from `_diag`, so no second telemetry store is introduced.

## Consequences

The old schema-1 audit cannot satisfy the performance gate and is not combined
with schema 2. A fresh 20-sample series is required. The GitHub timestamps have
one-second display resolution, so results are integer-second observations;
this limitation is explicit and consistent across samples. Phase optimization
is accepted only when its same-clock component changes and the external GitHub
metric improves.
