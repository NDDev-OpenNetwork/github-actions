# Drakkars product contract

Status: accepted product direction  
Private deployment owner: the consuming GDS estate

## Outcome

Drakkars is an always-available Linux CI/CD execution engine. A consuming
estate uses it to minimize the complete path from a ready pull request through
required checks, merge, deployment and post-deployment verification without
dropping coverage or hiding failure.

This public repository owns portable scheduling, lifecycle, worker,
compatibility and observability behavior. It contains no real organization,
repository, tenant, host, network, credential or runtime-evidence identity.
Those facts belong to the private consuming estate.

## Execution routing

The portable contract supports this policy:

- public Linux jobs use GitHub-hosted Linux runners;
- private and internal Linux jobs use Drakkars;
- macOS and Windows jobs use GitHub-hosted runners until an estate declares
  dedicated capacity for them;
- private Linux jobs never silently fall back to hosted Linux capacity.

The estate compiles exact routing from repository visibility and platform
requirements. The public engine does not embed an account or repository name.

## Complete pipeline speed

The primary measure is the complete useful result, not one isolated job:

```text
ready pull request
-> required CI
-> merge
-> deployment
-> health verification
```

Each phase is measured separately so the critical path can be improved.
Optimization may combine tiny checks, shard independent long work, prepare
toolchains, use safe caches or improve scheduling. It may not remove a required
obligation, weaken isolation or convert a real failure into success.

## Shared capacity

All compute members form one work-conserving CPU, memory and storage pool.
Labels describe capabilities and trust; they do not own hosts or permanent
capacity. Every safe resource may be used when eligible work exists.

Admission reserves declared resources before GitHub job acquisition. Provider
and placement checks remain physical safety authorities. Capacity pressure is
bounded backpressure, not a provider-failure storm.

Cluster placement packs eligible workers by the smallest safe remaining hard
memory after the request. Pressure, disk and host reserve checks run first.
This best-fit policy prevents small jobs from fragmenting every member and
preserves contiguous capacity for larger capability classes without dedicating
a host or leaving safe capacity idle.

## Three priority levels

The engine supports exactly three owner-configured levels:

0. high;
1. ordinary;
2. background.

The private estate decides which exact scale sets receive each level. A high
job moves ahead of queued ordinary work and may use the entire safe fleet, but
never interrupts a running job. Scheduled work is background unless it uses a
high scale set. Background work ages into ordinary service so it cannot starve
forever.

Background work has a separate bounded concurrency envelope. Aging selects
which background job receives those slots; it never lets a long maintenance or
soak workload occupy the whole production fleet. A reduced limit does not
preempt already-running work: the observed overage drains naturally.

Sparse lifecycle events do not carry reliable repository or workflow metadata.
Therefore priority is part of the scale-set policy available before admission,
not a hard-coded tenant name or a guess from a job title.

## Lossless lifecycle

Once accepted, a job has one durable intent, at most one reservation, at most
one active worker and exactly one terminal outcome. Duplicate events, process
restart, provider restart, one member loss and temporary external failure do
not silently lose it.

Running jobs are not preempted. When a pull-request commit is superseded,
already-running work completes while not-yet-started work for the obsolete
commit can be cancelled durably. Main, release and deployment work is never
superseded implicitly.

Infrastructure failures use bounded classified retry. Product and test
failures remain visible. A flaky retry remains observable and cannot silently
satisfy a strict release gate.

## Worker model

The target Linux backend is one disposable unprivileged Incus container per
job. An executed worker is destroyed and never returned to a pool. Virtual
machines are not a permanent compatibility layer; an estate retains one only
while a declared workload lacks a proven safe container path.

Trust, credentials, filesystem identity, devices, networking, cache and OIDC
are explicit capabilities. They affect policy, not physical ownership.

## Technology compatibility

The engine and its images support every technology declared by consuming
projects, including multiple valid tools for one language. A project is not
forced from one package manager to another merely to fit a runner image.

The inventory can include:

- Node.js with npm, pnpm, Yarn and Bun;
- Python with uv, pip and declared build/test tools;
- Go modules, race, fuzz and vulnerability tooling;
- Rust, Cargo, rustfmt, Clippy and compiler caches;
- browsers and Playwright system dependencies;
- Docker-compatible builds, BuildKit, Buildx, Compose and service containers;
- shell, structured-data, documentation and infrastructure tools;
- GitHub CLI, SSH, OIDC, signing, SBOM, provenance and deployment utilities.

Support covers deterministic version selection, lockfiles, dependency
resolution, cache, build, tests, services, artifacts, networking and failure
diagnostics. An unsupported tool is a compatibility defect, not a product test
failure or an infinite retry condition.

## Cache behavior

Ordinary CI degrades to uncached execution when an optional cache is
unavailable. Trust violations deny the cache operation and remain visible.
Namespaces include tenant, repository, trust, purpose, toolchain and content
identity. Untrusted work cannot write trusted cache, and release work consumes
only explicitly promoted inputs.

## Observability

One trace covers GitHub event, queue, admission, reservation, placement,
worker creation, bootstrap, registration, checkout, toolchain setup, cache,
dependencies, build, tests, security, artifacts, merge, deployment,
verification, diagnostics and teardown.

Signals share repository, commit, workflow, run, job, attempt, priority,
technology, package manager, intent, reservation, worker, host, image and
artifact identities where their trust boundary permits them.

The system must explain queue delay, critical path, retry reason, constrained
resource, cache benefit, missing technology, failure ownership and telemetry
coverage. Raw output is bounded and redacted. A telemetry-store outage delays
evidence but does not lose acknowledged signals or change job outcome.

## Completion

Merged code is not completion. Source, schemas, tests, GitHub checks, immutable
artifacts, private deployment policy, runtime, telemetry and documentation must
identify the same contract and versions.

Final acceptance requires zero lost accepted jobs, duplicate active workers,
host OOM, silent fallback and missing required verification obligations under
representative burst, soak, restart, member-loss and dependency-outage tests.
