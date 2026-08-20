# GitHub Actions fleet engine

Open-source control-plane components for secure, disposable GitHub Actions
runners on GARM and Incus. The engine provides durable queue admission,
provider leases, one-job container lifecycle, exact image identity, diagnostic
WAL export, pressure-aware placement and observable reconciliation.

This repository contains only product code and synthetic examples. Real
tenants, repositories, hosts, networks, credentials, deployment values and
runtime evidence belong in the consuming private estate.

## Properties

- GitHub `JobAssigned` is observation-only; complete `JobAvailable` identity is
  required before capacity reservation.
- Ambiguous GitHub/provider evidence fails closed.
- Each worker executes one job and is destroyed.
- Diagnostic data is retained locally until remote size, digest and schema are
  confirmed.
- Host-local CPU, memory and I/O PSI is converted into a hysteretic, expiring
  Incus member signal; missing, stale or closed signals remove capacity rather
  than becoming provider failures or hidden overcommit.
- Public repository CI runs only on standard GitHub-hosted runners.
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
