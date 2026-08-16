# GARM and Incus integration contract

Status: managed cold pilot passed on 2026-08-08, on the host that carried
the fleet at the time. That host has since left the fleet; the contract
below is not host-specific.
Upstream source was reviewed statically; the NDDev golden-image pipeline, Incus
smoke VM, official-runner Scale Set jobs, cross-job isolation, cancellation,
diagnostics and complete teardown ran on the target host. Production rollout,
external telemetry, cache promotion and the 500-job reliability gate remain
open.

## Exact source snapshots

| Component | Tag | Source commit | Tag signature |
| --- | --- | --- | --- |
| GARM | `v0.2.1-nddev.11` from upstream `v0.2.1` | `154638445c3949c1958b01812f69d9a1e4d82684`; exact patch/overlay digests in `config/garm-derivative.yaml` | upstream tag annotated and unsigned; derivative reproducibly rebuilt |
| Incus provider | `v0.1.5` | `f3ae31910c6443c31d841de268a377985e7c60a5` | annotated, unsigned |

The GitHub release assets have platform-provided SHA-256 digests, recorded in
`docs/upstream-baseline.md`. Production provenance must independently verify the
download and bind it to the golden-image digest; an unsigned tag is not a trust
root.

## Selected GARM mode

Use GitHub Runner Scale Sets, not webhook-driven pools. In the pinned source:

- GARM creates the GitHub Scale Set with `Ephemeral: true`;
- the listener long-polls GitHub's Scale Set queue and explicitly acquires
  available `runnerRequestId` values;
- `TotalAssignedJobs` drives the desired count;
- target instances are `min(min_idle + desired, max_runners)`;
- each instance receives a GitHub-generated JIT configuration before provider
  creation;
- GARM reconciles GitHub runners, database instances and provider instances.

Evidence:

- [ephemeral Scale Set creation](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/workers/scaleset/scaleset.go#L122-L134)
- [message acquisition and desired count](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/workers/scaleset/scaleset_listener.go#L184-L237)
- [JIT configuration before durable instance creation](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/workers/scaleset/scaleset.go#L820-L855)
- [target runner formula](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/workers/scaleset/scaleset.go#L952-L959)
- [provider reconciliation path](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/workers/scaleset/scaleset.go#L378-L616)

The Scale Set name is the primary `runs-on` selector. Custom labels are fixed at
Scale Set creation and are not treated as a mutable routing abstraction.

Generic scale-down removes only runners outside protected startup and execution
states. `pending` and `installing` runners remain owned until they register or
the existing Scale Set runner timeout reaper removes them. This prevents a slow
cold VM from being deleted merely because a later statistics message
temporarily reports zero desired runners; `active` and `terminated` remain
protected as in upstream.

## Pilot mapping

| NDDev policy | GARM/Incus value |
| --- | --- |
| scheduling mode | Scale Set |
| Scale Set name | `pool.scale_set_name` |
| maximum instances | `max-runners=1` |
| GARM idle runners | `min-idle-runners=0` |
| worker kind | Incus `instance_type="virtual-machine"` |
| resource class | Incus profile named by GARM `flavor` |
| image | local golden-image alias verified against expected fingerprint |
| lifecycle | GitHub JIT ephemeral runner, one job, full VM deletion |
| provider transport | restricted HTTPS client certificate, never Unix socket |
| provider interface | exactly `v0.1.0` |

The pinned provider imports the `v0.1.0` external-provider interface. GARM can
select `v0.1.0` or `v0.1.1`; selecting `v0.1.1` for this binary would fail the
interface assertion. The value is therefore explicit rather than relying on
GARM's empty-string fallback.

Evidence:

- [GARM interface selection](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/providers/external/external.go#L27-L35)
- [provider v0.1.0 assertion](https://github.com/cloudbase/garm-provider-incus/blob/f3ae31910c6443c31d841de268a377985e7c60a5/provider/incus.go#L25-L38)

## Warm-pool boundary

GARM `min-idle-runners > 0` creates a JIT runner record and provider VM ahead of
job assignment. That VM becomes an idle runner registered with GitHub. It is
still ephemeral and is destroyed after one job, but it is not the unregistered
pre-booted VM required by the target warm-pool contract.

The production Scale Sets therefore keep `min-idle-runners=0`. The NDDev
provider owns a separate unregistered warm pool: it pre-boots an exact-image VM,
requires root-owned guest readiness evidence, and persists an exclusive claim
before injecting the one-job assignment through the Incus Agent. GARM receives
the warm VM's real name as the provider ID, while its requested runner name
remains the durable retry key. Duplicate creates converge on the same claim.

Activation is one-way. The guest creates an irreversible started marker before
running the assignment, and a claimed VM is never counted as reusable or
returned to ready state. Completion, cancellation, injection ambiguity or claim
expiry all converge toward deletion; replenishment always creates a fresh VM.
The official runner remains the execution engine and is pre-baked unregistered
in the golden image to remove its download from the activation hot path.

The implementation accepts `target_ready > 0`, but the committed production
target stays zero until the new immutable image passes build, readiness smoke,
one-job activation, teardown and replacement canaries on the target host.

## Incus provider behavior

The pinned provider:

- selects an existing Incus project and an existing profile named by `flavor`;
- embeds generated cloud-init as `user.user-data`;
- explicitly supports `virtual-machine` and secure-boot configuration;
- tags instances with controller ID and a pool/pseudo-pool ID;
- creates, starts and then waits for a global IPv4 address;
- treats delete of an absent instance as success;
- lists instances by controller/pool tags for reconciliation;
- resolves only a local alias pinned by fingerprint for the requested flavor.

On Incus 6.0.0 the stock binary cannot enforce the required VMX/SVM removal:
`security.nesting=false` is accepted but has no effect on generated QEMU CPU
arguments. The candidate binary is therefore the in-tree `v0.1.5-nddev.21`
derivative of this exact source snapshot. It adds a closed VM-only
`raw.qemu=-cpu host,-vmx,-svm` value and does not accept arbitrary QEMU input.
The reconciled VM profile owns `security.nesting=false`; the provider does not
duplicate that key in an individual VM create request because the host's Incus
6.0 API rejects it there as an unknown configuration key. Provider adoption and
reconciliation still require the value in expanded configuration, proving that
the managed profile remains attached.

Warm replenishment distinguishes an admission decision from an operational
error. Every non-admitted capacity decision is returned as a structured
`deferred` result with exit status zero, so an expected reserve or saturation
condition cannot poison systemd health and cascade into `host-unhealthy`.
Failures to evaluate admission or reconcile Incus/journal state remain hard
errors. The merge-bound v12 rollout, both live deferred reasons, successful
one-job regression and clean replacement are recorded in
`config/warm-backpressure-v12-rollout-audit.json`.

Provider v13 also makes every unregistered warm-ready lease explicitly
preemptible by a real cold request in another pool. Cold admission and warm
teardown ownership are published in one journal generation before any VM is
deleted; the provider then uses normal diagnostic teardown and rechecks live
capacity before launching the cold VM. A preempted lease cannot be claimed or
replenished, while request release/expiry restores an observed victim if the
delete never happened. The exact boundary and remaining queue-fairness gap are
recorded in `docs/adr/0034-preemptible-unregistered-warm-capacity.md`.
Provider v14 retains that lifecycle and adds a monotonic, fsync-backed
preemption total so a hand-off shorter than the observer sample interval is
still externally visible after both VMs have converged. The merge-bound v14
rollout, corrected three-VM parity run, exact diagnostic digests, live `0 -> 1`
counter transition and converged replacement are recorded in
`config/preemption-v14-rollout-audit.json`. That evidence deliberately keeps
the separate queue-intent gap open: speculative warm preparation caused seven
bounded `insufficient-cpu` cold retries before safe preemption could proceed.

GARM derivative `v0.2.1-nddev.9` publishes the opaque `jobId` identity at
`JobAssigned`, derives its repository from GARM's canonical repository-scoped
`ForgeEntity`, and selects it through one fsync-backed global budget before
desired capacity is changed. The sparse assigned message has no reliable
workflow/event/queue metadata; those fields and the numeric `runnerRequestId`
are bound from the later `JobAvailable` message before `AcquireJobs`. Provider
v16 requires the admitted, non-queued intent before
cold admission, blocks speculative replenishment
while any intent is active and permits the exact cold request to reserve either
ready or still-preparing warm capacity. Durable weighted stride state,
repository limits, priority aging and schema-5 aggregate metrics share this
single boundary; the design and open live gates are in
`docs/adr/0024-central-queue-admission.md`.

If provider acquisition fails after GitHub has generated a JIT registration,
the derivative keeps the failed instance and its `AgentID` durable. It removes
that exact registration first, retries transient GitHub failures without
advancing teardown, and only then permits provider and database deletion. This
prevents an acquisition failure from becoming an uncorrelatable offline runner.

For an explicitly opted-in warm claim, nddev.9 reconstructs GitHub's official
encoded JIT blob only in the dynamic external-provider request. Provider
nddev.19 retains nddev.18's validation of the exact dotted `.runner`, `.credentials` and
`.credentials_rsaparams` three-file shape and injects a root-owned one-job
assignment that executes the unchanged official runner `--jitconfig`
entrypoint as user `runner`. Static Scale Set configuration never contains the
blob, cold workers retain the metadata installer, and pools without the opt-in
retain byte-identical nddev.7 behavior. The boundary and rollout gates are in
`docs/adr/0025-direct-jit-warm-activation.md`.

For a claimed warm VM, provider reconciliation exposes the logical GARM runner
name in `ProviderInstance.Name` and retains the physical Incus VM name only in
`ProviderID`. The projection is accepted only when the durable claim resolves
that logical name back to the exact physical VM. This prevents Scale Set
inventory reconciliation from interpreting a single claimed VM as an orphaned
physical instance plus a missing logical runner. The failure and correction
contract are in `docs/adr/0026-warm-provider-identity-projection.md`. The
merge-bound nddev.19 rollout and a 45-second reconciliation canary passed with
the exact claim intact, zero uncovered running work and clean one-job teardown;
`config/warm-identity-nddev19-rollout-audit.json` is the machine-readable proof.
The subsequent 20-sample nominal series preserved those lifecycle invariants in
every run but measured median 5.320 seconds and p95 6.700 seconds from GARM
assignment to official runner session, so the below-five-second performance
gate remains open. Its immutable evidence is
`config/direct-jit-nddev19-latency-audit.json`.

That first series passed every lifecycle invariant but measured median 5.320
seconds and p95 6.700 seconds against the p95-below-5-seconds performance gate.
GARM `v0.2.1-nddev.10` therefore adds only the phase telemetry specified by
ADR 0027. The host journal now closes the previously unmeasured boundaries
around `AcquireJobs` and provider creation without logging the JIT request or
changing the hot-path state machine. No optimization is accepted until a
telemetry canary attributes the delay to a concrete phase.

The derivative also fails closed on unknown configuration, non-loopback Incus
transport, Unix-socket use, non-VM workers, non-JIT bootstrap, wrong image
fingerprint, runner version/URL drift, foreign ownership and instance security
drift. One provider process owns all admitted pools and one journal; its
`worker_images` map selects the exact standard or integration alias, fingerprint,
image variant and numeric runner UID/GID by flavor. Cold cache-delivery recovery
accepts guest files only with that image-bound identity rather than assuming a
distribution-default account number. A mapping to an unknown pool, a Docker capability
to a standard image, or a non-Docker capability to an integration image is
rejected before provider startup. The resolved image fingerprint and mutable
variant property are rechecked for every create request. Stored GARM Scale Set
`extra_specs` must be exactly
`disable_updates=true`. Immediately before invoking an external provider,
pinned GARM `v0.2.1`
[adds its Linux install wrapper at runtime](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/util/util.go#L134-L161).
The provider accepts only the byte-exact rendering of the pinned
[`linux_wrapper.tmpl`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/internal/templates/userdata/linux_wrapper.tmpl),
bound to the request's expected metadata endpoint and opaque instance token.
It rejects any other package, debug, root-script, context or template input.
After verification it deliberately discards the wrapper: the upstream wrapper
uses shell tracing around bearer-token setup, while the provider-owned path
generates the normal `garm-provider-common` JIT bootstrap without that wrapper.
A provider-owned pre-install script materializes
`/opt/cache/actions-runner/latest` into the runner home first, so a golden-image
hit avoids runner download and dependency bootstrap without changing GitHub's
execution engine. A second provider-owned script replaces upstream broad
supplementary groups: standard runners receive only `sudo`, integration runners
receive only `sudo,docker`, and `lxd` membership is forbidden.

The same create boundary requires the exact repository, `Default` runner group
and TLS worker-gateway callback/metadata base URLs. A missing or whitespace-
modified instance token fails before Incus lookup or mutation. Errors never
include the wrapper or token.

Each short-lived provider process obtains a cross-process file lock and updates
an atomic JSON admission journal. Pending leases bound crash recovery; observed
Incus VMs are reconciled into durable state; duplicate creates adopt only an
exact matching VM; successful or already-absent deletes release the lease.
Capacity, host health, disk pressure and per-pool limits are checked immediately
before Incus mutation.

## Scale Set creation boundary

Two similarly named update controls are required and independently verified:

- GARM's `CreateScaleSetParams.disable_update` becomes GitHub's official runner
  setting and prevents the runner binary from self-updating inside a job VM;
- provider `extra_specs.disable_updates=true` prevents guest package updates
  from drifting the immutable image during cloud-init.

Pinned GARM contains the first API field in
[`CreateScaleSetParams`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/params/requests.go#L602-L628),
but its
[`garm-cli scaleset add`](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/cmd/garm-cli/cmd/scalesets.go#L221-L237)
does not populate it. `gha-fleet reconcile-garm` therefore owns initial
creation through GARM's loopback API. It records a public-key fingerprint in
the non-secret credential description and verifies it on every later call
against `config/garm-credential-anchor.json`. The private bundle is accepted
only to create a missing credential. Once imported, Scale Set operations use
only the public anchor. The reconciler creates capacity `1/0` disabled, uses
repository group `Default`, explicitly pins GARM's `roundrobin` repository pool
balancer, and rejects any drift rather than updating it.
Enablement is a distinct call permitted only after the returned resource has
both locks, exact image/provider/flavor, no extra labels and no remote shell.

## Worker API boundary

GARM binds only to `127.0.0.1:9997`. The separate Go TLS gateway on
`192.0.2.1:9443` proxies an enumerated set of instance-authenticated callback
and metadata routes. It rejects GARM login/admin/controller APIs, webhooks,
metrics, pprof, WebSocket shell and agent mode. It also rejects encoded or
non-canonical paths, unexpected query parameters, wrong methods and request
bodies above 1 MiB. The worker CA is injected through GARM controller metadata;
the gateway never receives GitHub or Incus credentials.

The deployment contract is in
`deploy/fleet-host/`. GARM and the gateway have distinct service
identities and hardened systemd sandboxes. Only GARM gets `/dev/kvm`, solely so
its host-admission probe can verify availability. Neither service gets a host
Docker or Incus Unix socket.

Evidence:

- [project/profile and VM creation](https://github.com/cloudbase/garm-provider-incus/blob/f3ae31910c6443c31d841de268a377985e7c60a5/provider/incus.go#L136-L253)
- [create and IP wait](https://github.com/cloudbase/garm-provider-incus/blob/f3ae31910c6443c31d841de268a377985e7c60a5/provider/incus.go#L294-L315)
- [idempotent-not-found delete and timeouts](https://github.com/cloudbase/garm-provider-incus/blob/f3ae31910c6443c31d841de268a377985e7c60a5/provider/incus.go#L334-L389)
- [controller/pool reconciliation tags](https://github.com/cloudbase/garm-provider-incus/blob/f3ae31910c6443c31d841de268a377985e7c60a5/provider/incus.go#L396-L444)
- [local alias resolution](https://github.com/cloudbase/garm-provider-incus/blob/f3ae31910c6443c31d841de268a377985e7c60a5/provider/images.go#L49-L88)
- [HTTPS certificate configuration](https://github.com/cloudbase/garm-provider-incus/blob/f3ae31910c6443c31d841de268a377985e7c60a5/config/config.go#L132-L177)

## Pilot constraints

The stock versions are sufficient for the bounded standard and integration
Scale Sets, each limited to one cold VM, only when all of these constraints
hold:

1. the target host has completed its update/reboot window, host nested
   KVM remains proven, and disposable worker probes prove VMX/SVM plus
   `/dev/kvm` are absent.
2. Incus uses a dedicated project, bridge, storage budget and explicit VM
   profile; `include_default_profile=false`.
3. GARM connects through a project-restricted TLS certificate. The Incus Unix
   socket is absent from the GARM service namespace.
4. The Scale Set has `max-runners=1`, `min-idle-runners=0` and a dedicated
   runner group/repository allowlist.
5. The local image alias resolves to the expected immutable fingerprint before
   the Scale Set is enabled.
6. Workers can reach only the restricted GARM callback/metadata gateway; they
   cannot reach Incus, GARM administration, SSH, cloud metadata or tenant
   networks.
7. Provider create/delete timeouts and manager restart are fault-injected.

## Gaps before production

GitHub now documents the official Go-based [Actions Runner Scale Set
Client](https://docs.github.com/en/actions/reference/runners/self-hosted-runners#github-actions-runner-scale-set-client)
for custom non-Kubernetes autoscalers. It is the preferred future protocol
client to evaluate if the GARM control plane is replaced. It does not change
the current pilot boundary: the official runner remains the execution engine,
the proven GARM/Incus path is not replaced during parity testing, and any
migration requires a separate lifecycle/reconciliation conformance gate.

Remaining P0/P1 evidence is deliberately operational rather than a rewrite of
the runner engine. The least-privilege GitHub App, final systemd services,
global IPv4, callback/metadata TLS, JIT bootstrap, successful teardown and
in-job cancellation gates are complete. The merge-bound
[`config/garm-restart-recovery-audit.json`](../config/garm-restart-recovery-audit.json)
also proves one idle restart and one restart during an assigned job: the exact
warm VM and durable claim survived, GARM recovered the running job, cancellation
completed its post-action, the VM was destroyed, diagnostics reached RustFS and
the pool converged to one different unregistered worker with no orphan. Mocked
Incus operation-boundary tests separately prove that retries after ambiguous
create/delete timeouts do not repeat the external mutation; this is not claimed
as live external timeout injection. Remaining work is to:

- fault-inject the external provider timeout and host reboot paths;
- export stable reason metrics, traces and teardown diagnostics externally;
- complete the 500-job gate before enabling more than one pool or any idle
  capacity;
- add weighted cross-repository fairness before concurrency exceeds one.

Provider interface `v0.1.0` remains intentional. Moving to `v0.1.1` requires a
separate compatibility review and is not an automatic version bump.

The event-driven derivative runtime canary is recorded in
[`config/garm-event-driven-canary-audit.json`](../config/garm-event-driven-canary-audit.json).
The exact derivative restarted with no systemd restart or warning, preserved
the previous upstream binary for rollback, reduced one nominal-load
created-to-start sample from 19 to 12 seconds, destroyed the used VM and
replenished a clean unregistered worker. This is a directional measurement,
not the required p95 sample: the remaining measured interval is guest
activation between assignment and the official runner session.

P1 adds the unregistered warm-instance activator. It does not replace the
official runner, GitHub Scale Set queue or GARM's durable instance records.
The activator also redirects inherited Bash xtrace to `/dev/null` before it
executes GARM's generated installer. This is a secret boundary: a future
upstream wrapper may enable tracing, but callback credentials must never reach
the guest journal or teardown diagnostics. The merge-bound runtime proof in
[`config/warm-bootstrap-xtrace-audit.json`](../config/warm-bootstrap-xtrace-audit.json)
executed one official-runner job on provider `v0.1.5-nddev.5`, observed zero
JWT-shaped matches in the live guest journal and all 44 local diagnostic
bundles, destroyed the used VM, and durably synchronized 44/44 bundles to
RustFS. That run occurred under retained-runner CPU pressure and is therefore a
security proof, not a warm-latency sample.

Provider `v0.1.5-nddev.6` additionally removes `update-ca-certificates` from the
one-job critical path. It embeds the validated public callback CA in the
root-owned assignment, combines it at runtime with the image's system bundle in
a mode-`0400` temporary file, and exposes only that path to installer curl
processes through `CURL_CA_BUNDLE`. The file is deleted on exit, TLS validation
remains mandatory and the global guest trust store is unchanged.

Provider `v0.1.5-nddev.7` makes host-reboot recovery an explicit warm-pool
invariant. Newly prepared warm VMs have `boot.autostart=true`, and both pool
reconciliation and job claim reject a stopped or non-autostart instance. An
existing warm VM from an older provider must be drained and replaced before
this version is admitted; the controller never silently upgrades or treats it
as ready.

Provider `v0.1.5-nddev.8` refuses privileged `warm-drain` execution. This keeps
atomic journal replacement and diagnostic capture owned by the dedicated GARM
service identity even during an operator-driven release transition.

Provider `v0.1.5-nddev.11` adds the cache credential boundary without changing
the execution engine. A clean cold VM waits for a root-owned assignment file
and a separate zero-byte readiness marker; a claimed warm VM receives the same
payload inside its already one-shot Incus-Agent assignment. The payload is
never stored in Incus configuration, GARM state, cloud-init or the golden
image. Before the official runner starts, a non-secret setup script installs
the RustFS CA and registers an administrator-controlled
`ACTIONS_RUNNER_HOOK_JOB_STARTED` path. The synchronous hook masks the two S3
values, appends the exact role-scoped settings to the runner-created
`GITHUB_ENV`, records non-secret consumption evidence and deletes the staging
file before the first workflow step. Existing-instance retries accept only an
exact job-bound ready or consumed marker, preventing a second injection race.

The merge-bound canary in `config/process-scoped-ca-canary-audit.json` reduced
assignment-to-first-metadata from 3.201 to 1.509 seconds and
assignment-to-listening from 7.766 to 5.528 seconds. It also proved one-job
destruction, clean replacement, zero token-shaped diagnostic matches and 47/47
RustFS synchronization. This is one improved sample, not a completed p95 gate;
the remaining 5.528-second interval and 500-job reliability gate stay open.

## Job identity mapping

Scale Set `JobAssigned` messages reliably expose only `jobId`; the later
`JobAvailable` message adds `runnerRequestId`, repository and workflow metadata.
The repository is taken from the repository-scoped GARM `ForgeEntity`, never
guessed from an absent job field. The opaque `jobId` persists through assignment,
acquisition, start and completion, so the primary idempotency key is:

```text
github-scale-set-job:v2:<scale_set_id>:<job_id>
```

Webhook-driven fallback pools retain:

```text
github-webhook-job:v1:<repository_id>:<workflow_job_id>:<run_attempt>
```

Both are domain-separated before SHA-256 hashing. Audit records also preserve
the un-hashed source fields so operators can correlate GitHub, GARM and Incus.
