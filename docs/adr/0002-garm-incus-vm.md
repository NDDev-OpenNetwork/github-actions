# ADR 0002: GARM with Incus/KVM full VMs

- Status: accepted for pilot
- Date: 2026-08-08

## Context

The initial target is one managed runner host, not an existing Kubernetes
cluster. The current host is an 8-vCPU DigitalOcean KVM guest with nested KVM;
a larger dedicated host may replace it later. The platform needs disposable
workers, strong cross-job isolation and an extension point for local scheduling
without owning GitHub Actions semantics.

## Decision

Use GARM as the manager and its Incus provider with full KVM VMs. The pilot is
cold and binds one VM to one job attempt, then destroys it after diagnostics.
After the cold pilot, add a small pool of pre-booted but unregistered VMs through
an explicit activation extension rather than GARM's registered idle runners.

## Consequences

- Full VMs provide a stronger general boundary than ordinary containers/pods.
- GARM's Go provider model is the preferred extension seam.
- The pilot uses GitHub Runner Scale Sets, provider interface `v0.1.0`,
  `max-runners=1` and `min-idle-runners=0`.
- Stock GARM `min-idle-runners` creates registered JIT runners; it is not mapped
  to the desired unregistered warm pool. That optimization needs an explicit
  activation extension after the cold pilot.
- Kubernetes/CNI/CSI/Helm are not required for the first single-host deployment.
- The Incus provider must pass runtime parity and fault-injection gates.
- ARC becomes preferable if Kubernetes is already operated or multiple physical
  nodes make Kubernetes scheduling valuable.
- Firecracker remains a later provider option only after measured need.
