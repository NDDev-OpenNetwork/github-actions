# Changelog

All notable changes are documented here. The project follows Semantic
Versioning.

## [Unreleased]

### Changed

- Published fleet contract v2 for the current one-job ephemeral Incus-container
  implementation. The rendered contract now carries each runner class's trust,
  credentials, network/cache policy, hard resources and cold-only warm support.
- Replaced the ambiguous `worker_kind: incus-instance`, `jobs_per_vm` and
  `allow_cpu_overcommit` platform fields with container- and scheduler-native
  semantics. Deployment overlays must declare weighted CPU overcommit and prove
  that hard memory excludes non-schedulable emergency swap.
- Cluster admission no longer adds total or free swap to worker memory
  capacity. A deployment overlay can be checked against the exact public
  contract with `gha-fleet fleet-contract --config <path>`.
- Cut provider derivative `v0.1.5-nddev.41` so the hard-memory admission change
  cannot deploy under the previous `.40` identity.
- Cut provider derivative `v0.1.5-nddev.42` after the `.41` canary proved the
  public provider-config validator incorrectly coupled private deployments to
  the synthetic example subnet. Private-unicast TLS cluster endpoints on port
  8443 are accepted; public, link-local and alternate-loopback endpoints remain
  rejected.
- Cut provider derivative `v0.1.5-nddev.43` after the `.42` read-only probe
  proved a public synthetic hostname allowlist still made private deployments
  impossible. Platform identity is now bound to the provider process's exact
  runtime hostname, with no private host inventory compiled into public code.
- Added typed CPU, memory and I/O PSI admission with bounded staleness,
  hysteresis and OOM-delta handling. A host publisher updates only its Incus
  member metadata, clustered admission excludes closed members, and every
  pressure/capacity refusal stays in the retry backpressure class.
- Cut GARM derivative `v0.2.1-nddev.46` so I/O pressure and host-unhealthy
  refusals cannot consume terminal provider circuit attempts.
- Cut provider derivative `v0.1.5-nddev.52` from the exact pressure-aware
  source commit with reproducible binary identity, while retaining `.51` as
  the bounded rollout fallback for already executing workers.
- Disabled implicit Go VCS metadata in provider builds. The explicit stamped
  source commit remains authoritative, so a standalone checkout and the same
  commit embedded as a submodule now produce identical bytes.
- Added GARM derivative `v0.2.1-nddev.47`: stale workflow-job reconciliation
  takes the oldest rows first, and GitHub job substitution atomically transfers
  an already-admitted same-scale-set capacity token to the job that actually
  started instead of losing exact lifecycle correlation. A uniquely verified
  still-queued DB job also rehydrates its expired provisional intent, so an
  acknowledged JobAssigned message cannot strand valid old work forever.
- Added GARM derivative `v0.2.1-nddev.48` after live `.47` acceptance proved
  the existing entity query excludes every scale-set row with
  `workflow_job_id=0`. A dedicated scale-set-only SQL query makes cleanup and
  rehydration reachable without exposing those rows to webhook pool consumers.
- Added GARM derivative `v0.2.1-nddev.49` after live `.48` made the authoritative
  query reachable and exposed two previously masked contracts. Dedicated fleet
  Apps now require exactly `actions:read` so queued workflow jobs can be checked
  without workflow mutation permission. Timed-out idle JIT runners are reaped
  only when GitHub proves them absent or offline, preventing a registered-but-
  unused ephemeral container from retaining capacity forever. Scale-up also
  emits one capacity probe per reconciliation edge instead of materializing an
  entire target-sized herd before the first saturation result can back it off.
  A denied or rate-limited authoritative read also establishes a manager-wide
  fifteen-minute fail-closed backoff instead of retrying once per stale row.
- Added GARM derivative `v0.2.1-nddev.50` and queue journal schema v2. JobStarted
  now binds the exact ephemeral runner name to the running intent; v1 journals
  upgrade in memory without inventing identity for already-running work.
  Observer metrics expose both running intents without runner identity and
  created provider leases without a matching running identity, closing the
  previous one-direction-only lifecycle blind spot.

## [0.1.1] - 2026-08-16

First release of the NDDev GitHub Actions fleet as an open-source control plane
under `github.com/NDDev-OpenNetwork/github-actions`. The version line starts
here: this is a new module path, so the numbering of the private predecessor it
grew out of resolves to releases no tag here can name. The repository ships
example host and tenant configuration; a real estate is supplied at deploy time
and is never vendored here.

### Added

- **Disposable full-VM workers** on Incus, one job per machine, created and
  destroyed around a single GitHub Actions job. A worker never outlives the job
  it was made for, so nothing a job leaves behind can reach the next one.
- **A GARM derivative** built from a pinned upstream release plus reviewed
  patches and overlays, each bound by digest in `config/garm-derivative.yaml`,
  so what runs can be reproduced from what is declared.
- **A central queue with admission control**: per-repository shares, priority
  aging, a weighted stride scheduler and a durable single-writer journal under
  `flock`, so capacity is allocated deliberately rather than by arrival order.
- **A restricted worker gateway**, the one component permitted to cross from the
  worker network to the queue. A worker may reach the public internet and its
  own bridge, nothing else; the gateway forwards a narrow, uncredentialed route
  and refuses anything that would make the hop ambiguous.
- **A per-host content cache** (RustFS, S3-compatible) with trust-scoped
  namespaces, so a job's cache writes cannot reach another trust class.
- **Multi-tenant service** from a closed tenant registry: several GitHub
  accounts share one queue, and the provider boundary is the account a scale set
  belongs to.
- **Telemetry and diagnostics** that stay on the private network by contract,
  with redaction applied before anything is written.

### Notes

The fleet is not a general-purpose scheduler or a container runtime. It answers
one question — how a GitHub Actions job gets a clean machine and gives it back —
and it is deliberately opinionated about isolation over reuse.
