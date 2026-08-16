# ADR 0035: Cross-platform multi-tenant control plane

- Status: accepted
- Date: 2026-08-13
- Tracks: https://github.com/NDDev-OpenNetwork/github-actions/issues/208

## Context

The production implementation currently executes Linux jobs in disposable
Incus/KVM virtual machines. Two further requirements must not be expressed as
exceptions inside that Linux provider:

- `server-example-legacy` remains the operator and private-network entry point
  while execution moves entirely to fleet hosts;
- macOS and Windows jobs remain on GitHub-hosted runners until an independently
  reviewed backend exists, and future native macOS capacity must not inherit
  Linux image, lifecycle or host assumptions.

GitHub selects a runner from `runs-on` before this control plane sees a job.
There is no automatic spill from a busy or absent self-hosted label to a hosted
runner. A macOS or Windows job accidentally given a fleet label can therefore
wait indefinitely; admitting it locally and attempting a later fallback would
duplicate GitHub's queue ownership and still could not change `runs-on`.

The fleet is also multi-tenant. A scale set name is unique only within its forge
entity, while capacity, fairness and observability span all entities served by
one manager. Platform, tenant and capability identity must consequently remain
distinct throughout routing, admission and telemetry.

## Decision

The platform has four explicit roles:

| Role | Current placement | May execute workflow code |
| --- | --- | --- |
| operator ingress | `server-example-legacy` | no |
| management plane | GARM and NDDev controllers on fleet hosts | no |
| execution backend | disposable workers on dedicated fleet hosts | yes |
| observability backend | dedicated OpenObserve with RustFS storage | no |

Example legacy provides SSH/bastion access, private routing and operator entrypoints.
It holds no runner listener, warm worker, job credential, execution cache
credential or Scale Set capacity after migration. Losing Example legacy may prevent
new operator sessions; it must not stop an already-running job or erase queued,
lifecycle or telemetry state.

Runner routing happens before queue admission:

- private Linux work explicitly names an NDDev Linux class;
- public work uses standard GitHub-hosted runners;
- macOS names a GitHub-hosted `macos-*` label;
- Windows names a GitHub-hosted `windows-*` label;
- no self-hosted label claims to be a fallback for another operating system;
- the public reusable-workflow library describes requirements and keeps hosted
  defaults, while private callers map those requirements to estate labels.

Only Linux/amd64 on the Incus backend is enabled now. Configuration models a
backend as `(platform, architecture, implementation, failure_domain)` rather
than deriving platform from a pool name. A pool selects exactly one backend and
declares capabilities independently. An unknown platform, an unsupported
architecture or a capability/backend mismatch is rejected before a Scale Set
is reconciled.

A future macOS backend is a separate implementation and failure domain. It owes
the same one-job conformance contract—ephemeral identity, durable attempt
binding, cancellation, diagnostics, deletion and zero reuse—but may use a
different virtualization, image and bootstrap mechanism. Adding it requires a
separate ADR, threat model, capacity policy, provider version line, golden-image
contract and runtime evidence. Windows remains hosted until the same work is
approved for a Windows backend.

One process-wide admission coordinator orders every self-hosted backend and
tenant it owns. Its durable identity includes forge entity, tenant, repository,
scale set, platform, capability class and GitHub job identity. Global budgets
may be partitioned by backend and failure domain, while repository quotas,
priority aging and weighted stride fairness remain deterministic across manager
restart. Work that routes to GitHub-hosted capacity never enters this journal
and cannot consume or block a self-hosted slot.

GitHub remains the authoritative queue. An unselected request stays at GitHub;
local state is a durable intent and lifecycle projection, not a second job
queue. Every state has a bounded lease or terminal reconciliation path, and a
corrupt journal fails closed without acknowledging work that was not acquired.

OpenTelemetry is the collection boundary and OpenObserve is the durable store.
Every self-hosted job must be correlatable, without secrets or unbounded labels,
across queue intent, admission decision, provider claim, VM creation, runner
registration, execution, diagnostics, teardown and replacement. Required
dimensions are tenant, platform, architecture, backend, failure domain, pool
and bounded outcome/reason codes. Repository and job identifiers belong in
logs/traces, not Prometheus labels.

Collectors use a disk-backed sending queue. OpenObserve or network failure may
delay telemetry but must not fail admission, execution or teardown. Queue depth,
oldest buffered record, retries and dropped records are themselves exported and
alerted. OpenObserve storage follows ADR 0033 and uses the dedicated
`nddev-ci-telemetry` RustFS bucket rather than local-only mode.

## Consequences

- macOS and Windows continue immediately on hosted capacity instead of waiting
  for a Linux runner or participating in Linux fairness;
- adding native macOS does not add operating-system branches to the Incus
  provider or silently change existing labels;
- tenant fairness remains global where capacity is shared and partitioned where
  failure domains are independent;
- Example legacy can be maintained or replaced without becoming an execution host;
- every implementation change can be checked against one routing and telemetry
  contract;
- migration requires caller and GitHub-managed scanner settings to move before
  legacy listeners are removed, because labels do not provide fallback;
- completing this ADR requires typed backend configuration, negative routing
  tests, the `ci-workflows` capability mapping, complete OpenObserve delivery
  evidence and removal of execution listeners from Example legacy.
