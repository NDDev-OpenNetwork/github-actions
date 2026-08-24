# Changelog

All notable changes are documented here. The project follows Semantic
Versioning.

## [Unreleased]

### Added

- Added a typed provider rollout contract that requires observer restart and
  bounded inventory convergence after every provider identity change.
- Added a machine-enforced inventory for every shell/provider network bootstrap
  surface, classifying immutable image inputs, cache traffic, VPC-local runner
  bootstrap, reviewed source builds and non-promoting smoke probes.
- Added provider derivative `v0.1.5-nddev.73`, rebuilding the exact grouped AWS
  SDK and OpenTelemetry dependency update twice with identical bytes.
- Added GARM derivative `v0.2.1-nddev.76`: exact admitted jobs no longer
  inherit terminal provider circuits left by older work on the same scale set;
  generic retry state remains authoritative for shared capacity and legacy
  no-intent scale-up.

### Fixed

- Provision the standard runner cache directory with runner ownership before
  each job, preventing setup actions from exporting an unwritable home cache.
- Refresh the grouped provider dependency set to smithy-go 1.27.9 and
  testify 1.12.1 in provider derivative `v0.1.5-nddev.74`.
- Grouped minor/patch Go dependency updates so one reviewed provider derivative
  release covers each Dependabot wave instead of producing several mutually
  overlapping, structurally incomplete PRs.
- Added bounded retries for transient image-materialization downloads. HTTP
  408/429/5xx and transport/read failures receive at most three attempts with
  context-aware backoff; trust, size, digest and permanent HTTP failures remain
  terminal and are never retried.
- Made the entire baked Go workspace, including `GOPATH/bin`, runner-owned so
  setup actions and project tools can install executables without root.

- Added `actions/tool-cache`, a checksum-addressed immutable artifact cache for
  standalone CI tools. Private workers prefer their trust-scoped RustFS object;
  missing, unavailable, incomplete or corrupt cache state falls back to the
  exact HTTPS upstream with bounded retries and mandatory size/SHA validation.
  GitHub-hosted jobs use the same verified upstream path without fleet secrets.
- Added the actual `incus.member` placement identity to provider create and
  delete spans while retaining the services host as the provider process
  resource. Missing or malformed placement telemetry remains non-blocking for
  a healthy job.
- Extended the existing one-job start hook and cache claim with bounded GitHub
  repository, workflow run, attempt, job, workflow-ref and commit identities.
  The cache broker validates the all-or-nothing correlation envelope only after
  authenticating the one-use instance token and emits stable semantic fields
  for OpenObserve. Older hooks remain accepted, and missing correlation degrades
  observability without blocking the job or its optional cache fallback.
- Added a bidirectional repository contract proving that actionlint's runner
  labels exactly equal all ten published fleet classes; missing, extra and
  duplicate labels now fail together.
- Added real queue-phase duration spans emitted from durable journal
  transitions. Every GitHub job UUID receives a stable trace identity; spans
  carry repository, scale set, workflow run, runner request, numeric runner and
  exact provider instance correlation without creating synthetic work.
- Added queue journal schema v4 correlation fields for GitHub workflow run,
  runner request, job display name and numeric runner identity. JobAvailable and
  JobStarted enrich the original durable JobAssigned UUID in place; observer
  schema v13 reports every remaining bounded correlation gap.
- Raised the durable queue's slot ceiling from the historical 16 to the
  32-container fleet envelope. Measured CPU/memory budgets, placement and PSI
  remain the actual admission limits; a contended repository is capped at 24
  slots and an uncontended repository may use the full envelope.
- Added the `actions/package-cache` composite action for VPC-local,
  repository/trust-scoped Go, npm, pnpm, Yarn, Bun, uv, pip, Cargo, Maven and
  Gradle package caches. It reuses the one-job RustFS identity, stores no build
  outputs, degrades to uncached execution on transient cache outages, and emits
  structured transfer telemetry for real-job optimization.

### Changed

- Placement now chooses the least one-minute load per CPU core among members
  that pass disk, pressure and measured-memory gates. Best-fit memory remains a
  tie-break within a 0.05/core jitter window, so abundant memory can no longer
  concentrate work on the busiest host.
- Added GARM derivative `v0.2.1-nddev.75`: when upstream recovery omits both
  forge owner and name, pre-job retry identity derives the owner only from one
  unique active queue owner matching the exact scale-set ID and name. Multiple
  tenant owners remain fail-closed.
- Added GARM derivative `v0.2.1-nddev.74`: reconstructed instance managers
  recover the queue owner from the canonical forge entity name when upstream
  omits `ForgeEntity.Owner`. Pre-job reservations now match the same owner that
  the queue writer persisted for organization and repository scale sets.
- Added GARM derivative `v0.2.1-nddev.73`: a shared-capacity or retry
  preflight defer now releases its ephemeral pre-job reservation immediately.
  Accepted jobs remain available to replacement instances instead of waiting
  for a leaked two-minute lease after the deferred instance is discarded.
- Added GARM derivative `v0.2.1-nddev.72`: pre-JobAvailable instances now
  reserve one durable queue-intent retry identity before provider create.
  Fresh ephemeral instance names continue the same job attempt counter across
  capacity failure, deletion, restart and replay, so the original attempt plus
  two retries is enforced before numeric GitHub job metadata is available.
- Added GARM derivative `v0.2.1-nddev.71`: bounded each job-bound provider
  lifecycle to the original create plus at most
  two fresh infrastructure retries, including capacity refusals. Shared
  capacity remains a pool-level backpressure signal, while the affected job
  receives an explicit durable terminal circuit instead of silently reaching a
  fourth create attempt after restart or replay.
- Added GARM derivative `v0.2.1-nddev.70`: a successful shared-capacity
  probe now removes the saturated-domain record and restores bounded parallel
  creates. The next typed capacity refusal recreates single-probe mode, avoiding
  permanent one-at-a-time provisioning after capacity has returned.
- Added GARM derivative `v0.2.1-nddev.69`: every concrete shared-capacity
  owner is released after its two-minute lease expires, including a create
  interrupted by control-plane restart before it could record success or
  failure. Unexpired ownership remains exclusive.
- Added GARM derivative `v0.2.1-nddev.68`: an expired shared-capacity probe
  owned by a concrete create is released only when that owner's own durable
  retry record proves failure or has disappeared. A still-tracked owner without
  a failure remains protected, preventing duplicate in-flight probes while
  allowing real jobs to recover automatically from failed creates.
- Browser image qualification now accepts Chromium's bounded timeout only
  after the unprivileged process rendered the exact local smoke document and
  left no profile-bound process behind. Clean exits still pass; missing render
  artifacts, unexpected statuses and incomplete cleanup remain hard failures. The launch
  uses Playwright v1.62.1's pinned Chromium switches so qualification matches
  the consumer runtime instead of Chrome's network-active default startup.
- Advanced fleet contract v4: integration and priority-integration now declare
  Chromium OS compatibility. The b5 image bakes Playwright's Ubuntu 24.04
  Chromium dependencies, launch-tests SHA-pinned disposable Chrome bytes, and
  retains no browser so consumers continue to own their lockfile version.
  Provider derivative `v0.1.5-nddev.72` carries the exact capability schema.
- Advanced fleet contract v3: the trusted fast class now receives a one-use,
  repository-scoped cache claim. Untrusted work remains isolated, fast retains
  no Docker or deployment credential, and cache unavailability still degrades
  to verified upstream downloads.
- Added GARM derivative `v0.2.1-nddev.67` and provider derivative
  `v0.1.5-nddev.71`: shared-capacity wake ownership now
  intersects retry history with active durable queue intents by exact scale-set
  name. A completed worker can no longer grant its only wake to a historical
  domain with no waiting job; an empty active intersection remains unowned for
  the next real waiter instead of blocking current work.
- Added GARM derivative `v0.2.1-nddev.66` and provider derivative
  `v0.1.5-nddev.70`: once any scale set proves the
  measured fleet envelope saturated, a durable fleet-wide lease admits only
  the oldest waiting capacity domain. One completed worker deletion wakes one
  domain, successful replacement retains saturation, and unrelated in-flight
  successes cannot steal the probe. Observer metrics expose bounded saturation,
  waiter count, owner state, age and wake reason without tenant or runner labels.
- Added GARM derivative `v0.2.1-nddev.65` and provider derivative
  `v0.1.5-nddev.69`, reproducibly binding incomplete-metadata convergence to
  capacity retry semantics instead of provider-circuit semantics.
- Classified a bounded Incus inventory record with missing expanded flavor and
  no durable lease as capacity convergence rather than a provider defect. The
  inventory remains fail-closed and the accepted job retries, but the transient
  window cannot open a provider circuit or masquerade as permanent provider
  corruption.
- Split genuinely incomplete runner-request correlation from running direct-JIT
  jobs, for which GitHub never emits an AcquireJobs request ID. The observer
  retains a strict pre-execution gap metric and exposes the authoritative
  direct-JIT count separately instead of reporting every healthy running job as
  missing identity.
- Added provider derivative `v0.1.5-nddev.68`, reproducibly bound to Incus
  member placement telemetry and its exact binary digest.
- Added provider derivative `v0.1.5-nddev.67`, reproducibly bound to the
  job-start correlation hook and its exact binary digest.
- Added observer schema v14 with a stateful first-observed horizon for created
  leases temporarily absent from Incus during terminal teardown. One
  cross-source sample no longer flaps platform health; absence beyond 30
  seconds remains a strict missing-instance blocker, with separate count and
  oldest-age metrics.
- Added GARM derivative `v0.2.1-nddev.64`: when GitHub has already moved a
  direct-JIT job to `in_progress`, authoritative reconciliation now binds its
  repository before removing the stale queued DB duplicate. A binding failure
  retains the row for retry; running intent ownership is never released.
- Added GARM derivative `v0.2.1-nddev.63`: authoritative reconciliation now
  binds an existing organization-scoped JobAssigned intent to the exact
  repository from GARM's verified queued-job row. Direct-JIT jobs no longer
  remain account-only when GitHub skips JobAvailable; UUID, state, lease,
  runner identity and capacity ownership remain unchanged, and any attempted
  repository rebinding fails closed.
- Cluster admission now compares measured reservations with physical memory
  instead of treating `free_ram + buffers` as Linux MemAvailable. Placement
  preserves host reserves, PSI remains the live stop, and page cache no longer
  makes an otherwise idle fleet appear full.
- Split measured scheduling capacity from Incus aggregate hard-limit quotas.
  Queue, provider and placement continue to admit against measured reservations
  and PSI, while the project memory and disk quotas now bound the absolute
  32-container hard-limit envelope instead of rejecting physically safe work.
  Provider `v0.1.5-nddev.64` carries the typed platform fields.
- Replaced worst-case queue/provider/placement reservations with p95-derived
  measured envelopes while retaining hard Incus limits and PSI as safety
  authorities. Queue schema v5 also caps one repository at 75 percent of slots,
  measured CPU and measured memory only while another repository is waiting;
  uncontended work remains able to use the full fleet. The exact artifacts are
  GARM `v0.2.1-nddev.61` and provider `v0.1.5-nddev.63`. The provider migrates
  matching live leases from hard-limit accounting to measured reservations
  atomically, while immutable instance identity mismatches still fail closed.
- Added observer schema v12 with a 30-second deleting-visibility convergence
  counter. A runner already absent from Incus while its lease is two seconds
  into normal teardown no longer causes a transient platform outage; the same
  missing ownership after the bounded grace remains a strict blocker.
- Added provider derivative `v0.1.5-nddev.61` and the bounded
  `reconcile-maintenance` command. It plans by default and, under `--apply`,
  removes only expired-plus-grace, absent, unclaimed exact image-builder/smoke
  leases under the journal lock; ordinary runner ownership is never eligible.
- Separated exact visible image-builder/smoke inventory from orphan GitHub job
  runners in observer schema v11. Maintenance remains measurable without
  making the production platform unhealthy, while malformed lookalike names
  remain blockers. Image smoke evidence now reports `startup_mode: cold-only`
  instead of the retired warm-agent label.
- Removed dormant warm-runner services from the cold-only Incus container
  images. Cold bootstrap now verifies and reuses the already materialized clean
  runner tree instead of copying and recursively chowning it while a second
  `Runner.Listener warmup` process starts in parallel. Standard and integration
  container aliases advance to b10 and b4; b9 and b3 remain rollback targets.
- Cut provider derivative `v0.1.5-nddev.60` from the exact cold-bootstrap
  source commit so the no-copy runner verification cannot deploy under the
  previous `.59` identity.
- Made the reproducible provider lane reject binary-affecting source drift
  after the manifest's exact release commit. Changing provider code while
  rebuilding an older declared source can no longer produce a green artifact.
- Expanded every worker image compatibility contract with pinned pnpm and Yarn
  distributions, conventional Python/pip entry points, and default Go/gofmt
  links. Standard and integration variants now share Python, Java, Maven,
  CMake and Ninja prerequisites, and image smoke tests execute each command so
  missing package-manager support fails at build time instead of in project CI.
  Every changed recipe advances its immutable image alias; existing `b11`,
  `b10`, `b7` and `b2` artifacts remain untouched rollback targets.
- Made the cluster-wide Incus placement scriptlet typed public desired state.
  Eligible workers now use hard-memory best-fit packing after pressure and disk
  admission, preserving contiguous capacity for larger jobs instead of
  spreading small containers by transient host load. Reconciliation updates
  placement only after immutable Incus resource checks pass.
- Added GARM derivative `v0.2.1-nddev.58` and queue-admission schema v4.
  Background work now has a separately bounded concurrency envelope. Aging
  chooses fairly within that envelope but cannot let a long soak occupy the
  production fleet; already-running overage remains authoritative and drains
  without preemption.
- Added GARM derivative `v0.2.1-nddev.56`: authoritative cleanup of an
  in-progress job now removes only its stale queued database duplicate and
  preserves the running central admission intent.
- Added GARM derivative `v0.2.1-nddev.55`: persistent capacity saturation now
  backs off to five minutes instead of probing every minute, while every proven
  provider deletion still wakes one oldest waiting domain immediately.
- Added GARM derivative `v0.2.1-nddev.54`: after a scale set has proved
  capacity-bound, only one concrete worker may consume the next domain retry
  lease. Initial creates remain parallel and each completed deletion still
  wakes one oldest domain, eliminating repeated provider-call herds without
  reducing usable capacity.
- Added an idempotent OpenObserve plan/apply/read-back reconciler. It blocks on
  a missing destination or metric stream, updates only the declared rules,
  deletes only obsolete `managed-by:gds` alerts, and preserves operator-owned
  alerts.
- Upgraded the OpenObserve rules contract to keep the metric stream, PromQL
  expression, comparison operator and threshold as separate typed fields, and
  added deterministic v0.92 alert payload rendering with explicit disabled or
  reviewed-enabled modes.
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
- Cut provider derivative `v0.1.5-nddev.53` from the same queue-schema-v2
  source as GARM `.50` and observer v0.8.2. This is a required paired rollout:
  the previous `.52` strict reader correctly rejects the new journal and must
  not be mixed with a schema-v2 writer.
- Cut provider derivative `v0.1.5-nddev.54` for job-start cache claims. An
  organization bootstrap receives only a random short-lived claim; no cache
  credential is opened until the synchronous hook binds the exact GitHub
  repository to the server-owned pool role and private estate allowlist.
- Cut provider derivative `v0.1.5-nddev.55` after the first broker canary
  proved a valid claim for an unconfigured repository was incorrectly treated
  as a job failure. Such claims now bind the exact repository and return an
  explicit secret-free cache miss; invalid and cross-repository claims still
  fail closed.
- Cut provider derivative `v0.1.5-nddev.56` for the blue-green distributed
  cache endpoint. Cache TLS may move from standalone port 9002 to the exact
  gateway port 9003 while diagnostic storage remains untouched on 9002.
- Cut provider derivative `v0.1.5-nddev.57` after read-only Incus planning
  proved `rustfs_port` also owned the services diagnostic route. The platform
  now carries a separate `cache_gateway_port`; both ports receive exact scoped
  rules without redirecting diagnostics.
- Added GARM derivative `v0.2.1-nddev.51` and queue-admission schema v2. The
  central durable scheduler now reserves declared CPU units and hard memory
  before JIT/DB/provider creation, backfills smaller work into temporary holes,
  and stops backfilling once an aged or release candidate reaches priority zero.
  Provider admission remains the per-member/PSI authority and final safety net.
- Added GARM derivative `v0.2.1-nddev.52`. Provisional JobAssigned ownership
  now uses the measured ten-minute pre-start horizon instead of expiring during
  a valid 150-second cold registration. If an authoritative JobStarted still
  arrives without an intent, its exact running identity is rehydrated so real
  CPU/memory cannot disappear from central accounting; an observed running
  overage is retained as truth and prevents further admission rather than
  poisoning lifecycle delivery.
- Restored the generic representative benchmark harness omitted by the public
  export: frozen workload fixtures now have their metrics/toolchain/sccache
  scripts and a workflow-dispatch-only private-estate template. The public
  repository still executes only GitHub-hosted CI.

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
