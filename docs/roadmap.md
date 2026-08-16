# Roadmap and acceptance gates

The rollout is evidence-driven. A phase is complete only when its gate passes;
merging code or observing one successful job is not completion.

## Phase 0 — discovery and baseline

- inventory CPU, sockets, NUMA, RAM, NVMe, network and Incus/KVM capability;
- inventory current runner services and retained ExamplePlatform/Captcha boundaries;
- select representative Go, Rust, Python/uv, Bun/Node and Docker workflows;
- run 20 cold and 20 warm GitHub-hosted baselines per workload;
- record median/p95 queue, setup, dependency, build, test and upload time;
- record bytes downloaded, cache hits and infrastructure failures.

Gate: hardware facts and the top three workflow bottlenecks are measured. Pool
concurrency is not finalized before this gate.

## Phase 1 — secure one-VM parity pilot

- install and harden Incus/KVM without exposing its API to workers;
- build one immutable Linux full-VM image;
- install the pinned official runner without registration state;
- use one-job JIT/ephemeral registration;
- verify shell, JavaScript, composite and Docker actions;
- verify job/service containers, cancellation, timeouts, artifacts and logs;
- prove no host Docker socket, private route, prior workspace or image credential;
- export diagnostics and fully destroy every VM.

Gate: representative parity suite passes, cross-job persistence is zero and all
resources disappear after twice the cleanup lease.

## Phase 2 — GARM and durable orchestration

- integrate pinned GARM and Incus provider versions;
- implement a durable lifecycle journal and atomic event transitions;
- implement idempotent webhook admission and bounded leases;
- reconcile GitHub registrations, journal state, Incus VM/disk/network state;
- implement explainable reason codes and low-disk circuit breaker;
- add image canary selection and operator drain/quarantine controls;
- fault-inject duplicate events, API timeouts and manager restarts at every state.

Gate over at least 500 jobs:

- infrastructure-attributable failure rate below 0.5 percent;
- orphan runners, VMs, disks and networks equal zero;
- manager restart recovery succeeds in every injected transition;
- cancellation produces bounded teardown;
- every job has a complete audit correlation chain.

## Phase 3 — warm pools and local caches

- keep one unregistered warm VM for fast and standard pools;
- deploy RustFS with bucket/prefix-scoped credentials, quotas and GC;
- connect sccache and content-addressed build objects;
- deploy a separate OCI registry and BuildKit registry cache;
- add uv, Bun/Node and Go native cache integration where measurements justify it;
- pre-pull common images and bake stable toolchains;
- enforce trusted, isolated/untrusted and read-only promoted namespaces.

Gate:

- cold provision-to-online p95 below 30 seconds as the initial target;
- steady-state free disk above 20 percent and zero OOM events;
- the two gates below, which replace three that measurement retired.

**Warm queue-to-online.** The original gate was a p95 below five seconds. ADR
0031 decomposed the measured series per clock domain and proved it unreachable
while the unregistered-warm invariant holds: subtracting every segment this
repository can change leaves a 5241 ms floor, because an unregistered warm VM
performs runner registration and broker connect on the critical path while a
GitHub-hosted runner has already completed both. The gate is recorded as failed
and is not reopened as written. It is replaced by three separate measurements,
because one number hid which of them was moving:

- control-plane latency, from queue intent to provider claim;
- registration and broker latency, which is the floor above and is only
  reducible by changing the threat model;
- execution and setup latency inside the VM.

A new target for any of the three requires a statistical series against a
comparable baseline, not a number chosen to be beatable.

**Cache value.** The original gates were a 3x median speedup, a 2x p95 speedup
and a hit rate above 70 percent. ADR 0032 records why the hit rate cannot carry
them: the fixture is a 55-line crate with 29 locked packages where all 57
compile requests can be served from cache and the end-to-end ratio still stays
near 1.0, because cargo orchestration, build scripts and linking dominate.
Cache success is therefore measured as phase time saved and end-to-end
improvement on a fixture that exhibits the property, and enlarging the fixtures
precedes any further cache tuning.

## Phase 4 — repository rollout

- standardize labels through reusable workflows;
- add fast, full and nightly lanes;
- enable branch concurrency with stale-run cancellation;
- add path-aware tests and matrix caps;
- implement per-repository quotas and weighted fair queuing;
- isolate release runner group, image, OIDC and egress policy;
- document explicit GitHub-hosted fallback selectors;
- remove repository-specific snowflakes or record reviewed exceptions.

Gate: multiple repositories can saturate the fleet without one repository taking
more than its configured share, and release policy tests fail closed.

## Phase 5 — availability and advanced optimization

- add a second independent CI host/failure domain with the same labels;
- exercise complete primary-host loss;
- evaluate multi-node GARM placement or ARC if Kubernetes becomes standard;
- profile Incus provisioning as a fraction of end-to-end duration;
- consider a Firecracker/Cloud Hypervisor provider only if provisioning remains
  a measured bottleneck after warm pools and cache optimization.

Gate: critical workflow SLO survives loss of either host. A microVM
backend requires a separate conformance, networking, image and reconciliation
test program.

## Current implementation milestones

- [x] private repository and GDS identity;
- [x] architecture, threat and lifecycle contracts;
- [x] strict versioned policy configuration;
- [x] job-attempt idempotency key;
- [x] typed disposable-worker state machine;
- [x] conservative capacity admission and stable reason codes;
- [x] exact GARM/provider source snapshot and integration-gap analysis;
- [x] executable host preflight and initial Example legacy discovery record;
- [x] dry-run Incus foundation plan and idempotent local-socket reconciler;
- [x] pinned Canonical/runner golden-image plan, verified artifact pipeline,
  sanitation contract, smoke boot and rollback-alias implementation;
- [x] runtime golden-image build, nested-KVM/network isolation smoke,
  storage-footprint optimization and idempotent second apply;
- [x] hardened in-tree Incus provider with pinned image/runner/bootstrap policy;
- [x] fsync-backed cross-process admission journal and lease reconciliation;
- [x] unregistered warm-pool controller, durable exclusive claims, root-owned
  guest readiness attestation and one-way Incus-Agent JIT activation;
- [x] restricted worker TLS gateway and loopback-only GARM deployment contract;
- [x] pinned RustFS/Zot cache artifact contract and isolated disposable-VM
  CRUD, multipart, OCI, restart and crash-recovery tests;
- [x] live exact-repository GitHub App import, GARM standard/integration Scale
  Sets and managed one-job runtime canaries;
- [x] runtime GARM/provider/gateway canary on the target host;
- [x] controlled Incus loop-pool headroom grow to 200 GiB, with unchanged image
  inventory, empty reconcile, ready preflight and retained-service postchecks;
- [x] loopback-only fleet observer with exact Incus/journal reconciliation,
  bounded host/pool/service/diagnostic metrics and runtime-verified sandbox;
- [ ] complete cache rollout (Zot artifact, IAM, TLS, GC, storage, disposable
  VM authorization and automatic reboot gates passed and the component is
  production-ready; the exact RustFS repository/trust IAM reconciler, live
  effective-policy apply and merge-bound one-job cold/warm credential delivery
  canaries are complete; the independent RustFS stable-release gate,
  tool-native cache integration and statistical speedup/hit-rate gates remain);
- [ ] pinned sccache client and namespace adapter (repository implementation,
  immutable standard/integration image smoke, exact provider activation and a
  live RustFS prime/hit canary are complete; the hit run served all 57 compile
  requests from RustFS with zero cache errors; statistical cache gates and the
  independently blocked RustFS production gate remain);
- [ ] make the representative fixtures exhibit the property the cache gate
  measures. Per-step durations of the two strict-series runs show the earlier
  attribution was half wrong: toolchain installation is 20 to 21 seconds in
  **both** cold and warm, so it inflates absolute time but cannot move the
  ratio, and dependency resolution is only 1 to 2 seconds rather than a second
  major cost. The single cacheable phase went from 17 seconds to 16 with all 57
  rustc invocations served from cache, because the fixture is a 55-line crate
  with 29 locked packages where cargo orchestration, build scripts and linking
  dominate. ADR 0030's baked toolchains halve the absolute job and leave the
  ratio at roughly 1.0; they are now built, smoked, promoted and proven at
  runtime, with `Install toolchain` measured falling from 19 to 2 seconds on Go
  and 21 to 1 second on Rust. ADR 0032 records that the remaining defect is the
  fixture, not the gate and not the cache plane, and that enlarging the fixtures
  now precedes any further cache tuning;
- [x] external RustFS diagnostic exporter and loopback fleet observer;
- [ ] OpenTelemetry collector and durable external metrics/log backend. The gap
  is measured: nothing scrapes the observer's 53 metrics, no collection
  component is installed, and `journald` sits at its 200 MB cap with roughly two
  days of history rather than the seven its unit file asks for, so phase
  telemetry and warm-pool refusal reasons evaporate. ADR 0033 pins the plane:
  an OpenTelemetry Collector on the CI host as a digest-pinned tarball with its
  checksum, SBOM and Sigstore bundle, exporting OTLP over the private network
  into a dedicated OpenObserve instance on a second host with its own RustFS
  bucket, credentials and retention. The contract and its validator are
  complete; deployment, runtime evidence and promotion out of canary remain;
- [x] production warm-pool image build and one-job claim,
  destruction/replenishment canary;
- [x] expected warm admission backpressure as a structured exit-zero no-op,
  live-verified for pool saturation and memory-driven host unhealthiness without
  a failed systemd unit or delayed one-job teardown;
- [x] automatic cross-pool warm-capacity preemption with durable reservation,
  fault tests, corrected three-VM parity, schema-4 monotonic accounting,
  merge-bound v14 rollout and live increment/convergence proof;
- [x] central queue-intent admission ahead of speculative warm replenishment;
  the durable pre-`AcquireJobs` coordinator, global budget, priority aging,
  weighted stride fairness, repository quota, preparing-warm authorization and
  schema-5 observability are implemented, merge-bound and live fault-tested;
- [x] current-version active manager restart and provider-acquisition failure
  recovery, including durable JIT `AgentID` retention, removal before teardown,
  same-job convergence, cancellation, diagnostics export and zero orphans;
- [x] GARM-managed warm-worker cancellation canary with post-action completion,
  diagnostic export, bounded destruction and clean replacement replenishment;
- [x] inherited-xtrace secret-boundary canary with zero JWT-shaped matches in
  the live guest journal and 44 diagnostic bundles, one-job VM destruction and
  complete RustFS export;
- [x] event-driven GARM derivative canary with exact rollback artifact, healthy
  restart recovery, one-job destruction/replenishment and a single-sample
  created-to-start reduction from 19 to 12 seconds;
- [ ] direct official-JIT warm activation (narrow GARM/provider implementation,
  fail-closed tests, ordinary implementation merge, reproducible nddev.9 build,
  stage-only b8/b4 image smoke, transactional rollout/rollback and one clean
  merge-bound direct-JIT lifecycle canary are complete; sample 13 of the first
  statistical series exposed a physical/logical provider identity mismatch;
  provider nddev.19 and observer schema 6 correct it, and their merge-bound
  rollout plus longer-than-reconcile-interval canary passed with zero identity
  coverage gap or orphan; the fresh 20-sample baseline then completed with
  median 5.320 seconds and p95 6.700 seconds, so precise activation
  instrumentation, optimization and a new passing series remain required);
- [x] GARM startup-state scale-down guard: reproducible derivative, fault test,
  merge-bound rollout and repeated three-VM integration canary complete;
- [~] warm queue-to-online: retired as written, replaced by the three
  measurements in the Phase 3 gate. The nddev.21
  20-sample series measured median and p95 of 7 seconds. ADR 0031 then
  decomposed it per clock domain and proved the gate unreachable as written:
  subtracting every segment this repository can change leaves a 5241 ms floor
  against the 5000 ms exclusive target, because an unregistered warm VM performs
  official runner registration and broker connect on the critical path while a
  GitHub-hosted runner has already completed both. The hosted reference on the
  same workflow is a 3-second median and a 5-second p95. Closing this gate
  requires changing the unregistered-warm invariant, which is a threat-model
  decision and not taken here; the gate is recorded as failed, never as passed;
- [x] manual five-stack representative benchmark harness with locked fixtures,
  cold/warm isolation and Jobs API-compatible phase names;
- [x] six-run GitHub-hosted/NDDev cold, warm-prime and cache-hit protocol pilot
  with strict artifact evidence and post-run convergence;
- [ ] representative workflow statistical baseline measurements (20 cold plus
  20 cache-hit runs per environment; pilot and prime runs excluded);
- [x] managed disposable-VM parity for JavaScript/composite/Docker actions,
  job and service containers, outputs/post-actions, network denial, timeout and
  explicit cancellation.

## Deferred by design

- custom GitHub Actions execution engine;
- persistent general-purpose runners;
- Docker-only isolation or host Docker socket passthrough;
- Kubernetes/ARC on one host without an existing Kubernetes operating model;
- Firecracker before profiling proves Incus startup is material;
- destructive removal of active legacy runners before the replacement gate.
