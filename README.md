# GitHub Actions fleet engine

Open-source control-plane components for secure, disposable GitHub Actions
runners on GARM and Incus. The engine provides durable queue admission,
provider leases, one-job container lifecycle, exact image identity, diagnostic
WAL export, pressure-aware placement and observable reconciliation.

This repository contains only product code and synthetic examples. Real
tenants, repositories, hosts, networks, credentials, deployment values and
runtime evidence belong in the consuming private estate.

The [Drakkars product contract](docs/drakkars-product-contract.md) defines the
portable outcome, three-level priority model, technology compatibility and
whole-pipeline performance standard implemented by this engine.

## Properties

- GitHub `JobAssigned` is observation-only; complete `JobAvailable` identity is
  required before capacity reservation.
- Ambiguous GitHub/provider evidence fails closed.
- Each worker executes one job and is destroyed.
- Diagnostic data is retained locally until remote size, digest and schema are
  confirmed.
- Scale-set workers receive no repository credential at create time, because
  GitHub may assign a ready runner a different repository from the queue intent
  that caused its creation. A one-time, 15-minute claim lets the synchronous job-start hook bind
  the server-provided `GITHUB_REPOSITORY` to an estate allowlist and receive
  only that repository/pool trust role. Claim retries are idempotent; another
  repository, runner, role, expired token or replay after cleanup fails closed.
  The same authenticated job-start claim binds the exact workflow run to the
  runner's authoritative running queue intent, so short jobs remain visible in
  run-scoped latency telemetry even when their lifecycle webhook is sparse.
  After token expiry, only the already-bound repository name remains available
  for 24 hours so provider teardown can file runner diagnostics under the exact
  repository instead of an organization-wide account bucket; it cannot deliver
  cache credentials or bind a second repository.
- `reconcile-diagnostic-storage` plans, applies and reads back the remote hard
  quota and prefix lifecycle as one source-controlled durability contract. A
  consuming estate sizes the quota from measured retention, burst and outage
  demand; reaching the hard quota is never treated as normal backpressure.
- Host-local CPU, memory and I/O PSI is converted into a hysteretic, expiring
  Incus member signal; missing, stale or closed signals remove capacity rather
  than becoming provider failures or hidden overcommit.
- OpenTelemetry is the only collection standard, OTLP/HTTP is the transport,
  and OpenObserve is the only telemetry store. PromQL appears only as
  OpenObserve's metric-query language; it does not imply a Prometheus server.
  High-volume kernel/LVM notices are exported as bounded cumulative metrics,
  while package, reboot, kernel and SRSO state remains visible for every host.
- Raw missing correlation remains visible during queued and assigned capacity
  phases. Persistent identity alerts begin only for a running intent after its
  own convergence grace, so capacity delay is not mislabeled as identity loss.
- Public repository CI runs only on standard GitHub-hosted runners.
- Private Linux jobs can use the [trust-scoped package cache](docs/package-cache.md)
  over their existing one-job RustFS identity; compiler and package caches
  remain separate, measurable layers.
- Checksum-pinned standalone tools can use the [immutable tool cache](docs/tool-cache.md),
  which prefers the VPC-local object and falls back to the exact upstream URL.
- Configuration examples use documentation identities and address ranges.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
go build -trimpath ./...
```

`config/example-*.yaml` demonstrates the typed contract. It is not deployment
state. Consumers supply their own exact values from a separate private estate.
