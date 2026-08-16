# ADR 0009: loopback fleet observer

Status: accepted for implementation

## Context

The runner manager, provider, Incus host and cache services already emit useful
state, but no single stable surface proves that admission remains safe after a
restart or under disk pressure. Exporting the GARM administrative API, Incus
socket or service credentials to a general monitoring network would create a
larger security boundary than the metrics justify.

## Decision

Run a small NDDev-owned Go observer as the existing unprivileged `garm` user.
It samples only read-only, secret-free sources:

- the validated platform policy and provider admission journal;
- the same Linux host probe used by runtime admission;
- the provider's read-only, project-restricted Incus compatibility inventory;
- metadata and sizes of matching private diagnostic bundles;
- active state for the four reviewed fleet systemd units.

The observer binds exactly `127.0.0.1:9464` and exposes three GET-only routes:

- `/metrics`: deterministic Prometheus text exposition without dynamic job or
  instance labels;
- `/healthz`: freshness and collector-health status;
- `/snapshot`: the current secret-free JSON observation for incident review.

It does not accept configuration mutations, proxy GARM endpoints, read runner
credentials or inspect workflow workspaces. Incus access remains mTLS over its
loopback endpoint with the existing project-restricted certificate; the Unix
socket stays inaccessible. The systemd unit has no writable path, host socket,
network route outside loopback or ambient capability.

Sampling is every 15 seconds with a 10-second collection timeout. Health fails
closed when the latest sample is older than 45 seconds or any essential source
failed. Metrics retain the failed sample's timestamp and error count so absence
cannot look healthy.

Exact Incus instance names are compared in memory with created/deleting journal
leases. Only aggregate orphan and missing-lease counts are exported; names are
not metric labels. Pool, service and lifecycle labels come exclusively from the
validated bounded policy.

## Consequences

- Operators and a future OpenTelemetry Collector receive one stable local
  source without credentials in scrape configuration.
- Loopback Prometheus output is an integration boundary, not an Internet or
  worker-facing endpoint.
- GARM queue, GitHub API and job-phase latency still require authenticated GARM
  or GitHub event instrumentation in a later increment.
- Runtime acceptance must prove bind scope, stale/failure health behavior,
  exact inventory reconciliation, service sandboxing and unchanged runner/app
  health.
