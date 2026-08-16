# NDDev GitHub Actions platform

Control plane for secure, disposable, high-performance GitHub Actions workers:
one full virtual machine per job, created and destroyed around it, behind a
central admission queue that several accounts can share.

[**Current state**](docs/STATUS.md) records what is deployed and what is
blocking. This file describes what the platform is.

The platform deliberately keeps GitHub's official `actions/runner` as the job
execution engine. This repository owns the policy, admission, lifecycle,
reconciliation and integration code around GARM and Incus/KVM. Every production
job gets a full virtual machine, runs once, exports diagnostics and is destroyed.

## Target stack

- official `actions/runner` for GitHub compatibility;
- GARM plus the Incus provider for orchestration and provisioning;
- Incus/KVM full VMs as the security boundary;
- unregistered, pre-booted warm VMs for low queue-to-start latency;
- RustFS for S3-compatible compiler and content-addressed caches;
- minimal Zot as the separate local OCI registry and BuildKit registry cache;
- OpenTelemetry and external diagnostics before VM teardown.

This repository does **not** implement the GitHub Actions protocol, reuse a VM
after it has executed workflow code, expose the host Docker socket, or treat a
container as the general security boundary.

## Current implementation

`gha-fleet` is the initial Go control-plane core. It currently provides:

- strict, fail-closed platform configuration validation;
- a deterministic configuration fingerprint;
- conservative host resource admission decisions;
- read-only Linux host discovery and fail-closed cold-pilot readiness;
- deterministic, dry-run-by-default host-UFW and Incus
  project/storage/network/ACL/profile planning with idempotent reconcilers;
- an Incus 6.0 VM CPU policy that removes VMX/SVM from every worker rather
  than relying on that release's ineffective `security.nesting=false` path;
- digest-pinned Bun, Go, Rust and uv toolchains baked into both worker images,
  with Go seeded into the official runner tool cache so no job re-installs a
  toolchain it already has;
- a signed-input golden-image pipeline that verifies Canonical's OpenPGP
  signature, pinned source/runner SHA-256 digests, sanitation invariants,
  disposable smoke boot and current/previous rollback aliases;
- a typed one-job worker lifecycle with job-attempt idempotency keys;
- an in-tree `v0.1.5-nddev.30` Incus provider derivative with per-pool exact-image,
  official-runner, ownership, direct-JIT and VM-security enforcement;
- a reproducible `v0.2.1-nddev.11` GARM derivative whose coalescing watcher
  wake-ups remove two five-second provisioning polls, while startup states are
  protected from generic scale-down and retain the bounded timeout reaper;
- an opt-in, subprocess-only handoff of GitHub's official one-job JIT blob to
  already booted warm VMs, with byte-exact fallback for every other provider;
- durable pre-capacity `JobAssigned` admission and pre-`AcquireJobs`
  reconciliation with a global capacity budget, priority
  aging, per-repository quotas and weighted stride fairness;
- durable failed-acquisition cleanup that retains the JIT runner `AgentID`
  until GitHub confirms removal, preventing offline registration orphans;
- an atomic, fsync-backed, cross-process admission journal with leases and
  observed-Incus reconciliation, including durable warm-VM claims;
- an unregistered warm-pool reconciler with guest readiness attestation,
  one-way JIT activation over the Incus Agent, automatic replenishment and
  structured no-op backpressure for expected admission refusal;
- a pre-teardown, allowlisted and redacted diagnostic exporter with strict
  time, file, bundle and private-spool retention bounds;
- a teardown-independent, content-addressed RustFS canary exporter with
  prefix-only IAM, a credential-bound config, a read-only spool view and
  HEAD-after-PUT durability confirmation;
- a loopback-only observer that reconciles exact Incus/journal inventory and
  exports bounded host, pool, service and diagnostic-spool metrics, including
  an explicit 90-second asynchronous exporter convergence state;
- a TLS worker gateway that exposes only enumerated GARM callback/metadata
  routes while keeping its administrative API on loopback;
- reviewed systemd, sysusers, tmpfiles and secret-free deployment contracts for
  every fleet host;
- a strict cache-artifact manifest, isolated RustFS/Zot service contracts and
  real CRUD/multipart/OCI restart and crash-recovery smoke tests;
- repository/trust-scoped Zot identities with digest-bound destructive-storage
  and live disposable-VM authorization evidence;
- a fail-closed RustFS cache-IAM reconciler with exact per-trust prefixes,
  bounded quota/lifecycle policy, root:garm credential files and effective
  positive/negative S3 authorization probes;
- one-job RustFS credential delivery over the Incus Agent, with exact
  trust-to-role selection, no secret-bearing instance metadata, official
  runner pre-job masking/env propagation and pre-step staging deletion;
- a loopback-only, least-privilege GitHub App manifest flow that verifies the
  owning organization, its installation, the repository selection and the
  permission set before persisting a one-time GARM credential bundle;
- a fail-closed GARM API reconciler that creates the pilot disabled, preserves
  both runner and guest update locks, and separates inspection from enablement;
- a manual, least-privilege five-stack benchmark harness with exact toolchains,
  committed lockfiles and isolated cold/warm cache conditions;
- a read-only Go evidence collector that verifies workflow/job identity,
  phase timestamps, artifact digests and disposable runner uniqueness;
- executable contracts and unit tests for the most important invariants.

Which of these are live, which are canary-only and what is currently blocking
are recorded in [current state](docs/STATUS.md) rather than here, because that
answer changes and this list does not. GARM's own registered idle-runner
setting is deliberately not treated as the warm pool.

## Quick start

```bash
go run ./cmd/gha-fleet validate --config config/server-gha-runner-1.yaml
sudo go run ./cmd/gha-fleet preflight --config config/server-gha-runner-1.yaml
go run ./cmd/gha-fleet reconcile-incus --config config/server-gha-runner-1.yaml
go run ./cmd/gha-fleet reconcile-image \
  --config config/server-gha-runner-1.yaml \
  --manifest config/golden-image.yaml
go run ./cmd/gha-fleet validate-cache \
  --manifest config/cache-artifacts.yaml
go run ./cmd/gha-fleet validate-rustfs-cache \
  --config config/rustfs-cache-identities.yaml
GH_TOKEN="$(gh auth token)" go run ./cmd/gha-benchmark collect \
  --run-id RUN_ID
go test ./...
go vet ./...
go build ./...
make build-benchmark build-provider build-gateway build-observer
```

Render the normalized, fingerprintable policy as JSON:

```bash
go run ./cmd/gha-fleet render --config config/server-gha-runner-1.yaml
```

## Documentation

- [Current state](docs/STATUS.md)
- [Architecture](docs/architecture.md)
- [Threat model](docs/threat-model.md)
- [Lifecycle contract](docs/contracts/lifecycle.md)
- [GARM/Incus integration contract](docs/garm-integration.md)
- [Dedicated GitHub App bootstrap](docs/github-app-bootstrap.md)
- [Local cache plane](docs/cache-plane.md)
- [Representative benchmark protocol](benchmark/README.md)
- [Phase 0 pilot evidence](docs/benchmark-phase0.md)
- [Operations](docs/operations.md)
- [Example legacy host baseline](docs/host-baseline.md)
- [Roadmap and acceptance gates](docs/roadmap.md)
- [Upstream baseline](docs/upstream-baseline.md)
- [Architecture decisions](docs/adr/)

Host-specific deployment state and the final destructive cleanup manifest live
outside this repository: credentials, PKI, environment files and the addresses
a host was provisioned with. This repository remains the portable
implementation, the policy source and the deployment contract every fleet host
runs.
