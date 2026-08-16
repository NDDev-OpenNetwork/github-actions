# ADR 0025: Direct official-JIT handoff for prebooted warm VMs

## Status

Accepted for merge-bound canary deployment, with the first live direct-path
attempt rolled back and corrected before promotion. Static contracts, provider
tests, the pinned GARM race suite and reproducible derivative build are required
before merge. Live latency, lifecycle, diagnostics and rollback evidence remain
promotion gates.

## Context

The event-driven warm pool removed image cloning and boot from the hot path, but
the measured GitHub `created` to `started` interval remained 10–11 seconds. A
2026-08-09 canary separated that interval into approximately 3.2 seconds from
GARM assignment to the first guest-visible activation, roughly 1.5 seconds of
sequential metadata credential/install-script requests, and the official
runner's session handshake. Running the official
`Runner.Listener warmup` before readiness improved a single sample by about one
second; it did not remove the metadata round trips.

GARM already calls GitHub's official generate-JIT endpoint and seals the decoded
three-file map in its instance record. Its default Linux bootstrap reconstructs
those files through authenticated metadata requests, renders a systemd runner
unit and starts it. That work is necessary for a cold generic provider, but is
duplicative for an immutable, already booted NDDev VM whose one-job assignment
channel is root-owned and Incus-Agent mediated.

The compatibility boundary must not move. The official `actions/runner`
supports the single-use `--jitconfig` entrypoint and remains responsible for all
Actions semantics. A replacement executor or interpretation of workflow YAML is
out of scope.

The first merge-bound direct-path attempt on 2026-08-09 also established an
important format invariant. GitHub's decoded document uses the literal file
names `.runner`, `.credentials` and `.credentials_rsaparams`. Provider nddev.17
incorrectly validated their undotted spellings, failed closed before VM
activation and was immediately migrated to the already tested metadata mode.
The same canary then completed through metadata in 14 seconds. Provider
nddev.18 corrects only that validator contract and adds a regression test that
rejects the undotted shape; direct mode remains unpromoted until its next live
canary passes every gate below.

The corrected direct transport then ran the complete official-runner canary
without any metadata install or credential request, but exposed the next
lifecycle invariant. Because the direct path intentionally skips GARM's
metadata installer callbacks, the database runner status remained `pending`.
GitHub's authoritative `JobStarted` message could not apply the normal
`idle -> active` transition, and repeated `pending -> active` failures prevented
the completed job from reaching teardown. Activation was rolled back again,
the completed one-job VM was force-removed through GARM's authenticated API,
and restart reconciliation returned the metadata fleet to zero claims and
orphans. GARM nddev.9 bridges only direct-JIT `pending -> installing -> idle ->
active` (or `installing -> idle -> active`) after an authoritative
`JobStarted`; non-direct pools retain byte-identical transition behavior.

## Decision

GARM derivative `v0.2.1-nddev.9` and provider
`v0.1.5-nddev.18` add an opt-in dynamic handoff:

- the stored Scale Set extra specs contain only
  `nddev_direct_jit=true`; they never contain a JIT blob;
- after GARM has generated and unsealed the official JIT map for the one
  instance, a narrow provider patch JSON-encodes and base64-encodes that map
  into the subprocess-only `nddev_encoded_jit_config` request field;
- the field is rejected when supplied statically, when the opt-in is absent,
  when the decoded document exceeds 64 KiB, or when it is not exactly the
  non-empty base64 `.runner`, `.credentials` and `.credentials_rsaparams` map;
- unrelated providers and disabled pools receive byte-identical extra specs;
- a warm claim is still journaled, bound to exact image/pool/repository/job
  metadata and injected through the existing root-owned mode-0700 assignment;
- the guest warm agent opens that assignment as root and executes it as the
  unprivileged `runner` account; the assignment verifies an unchanged official
  runner tree with no registration files, then executes the official
  `/home/runner/actions-runner/run.sh --jitconfig` entrypoint;
- cache credentials retain their separate one-job, trust-scoped delivery and
  are merged before runner activation;
- cold provisioning deliberately discards the dynamic field and retains the
  current metadata/bootstrap path;
- the reconciler accepts only the exact `metadata` and `direct-jit` states; an
  explicit migration disables the Scale Set, changes only `extra_specs`,
  verifies the disabled result and optionally re-enables it;
- selecting `--activation-mode metadata --migrate-activation` restores the
  complete metadata path as the immediate rollback without database edits.
- for an opted-in direct scale set only, an authoritative GitHub `JobStarted`
  event advances the callback-free runner through GARM's existing legal
  lifecycle states before applying `active`; retries from `active` remain
  idempotent and every non-direct scale set keeps the upstream transition path;

Golden image revisions standard `b8` and integration `b4` execute the official
`Runner.Listener warmup` as `runner` before publishing warm readiness, then
repeat the registration-state absence check. A VM that has executed any job is
destroyed and can never re-enter the warm pool.

## Security and consistency invariants

- GitHub remains the JIT issuer; GARM remains the only generate-JIT caller and
  stores the map sealed exactly as before.
- The official runner binary, scripts and workflow execution semantics are
  unchanged.
- The ephemeral blob is never persisted in static Scale Set configuration,
  image metadata, golden images or the provider lifecycle journal.
- Neither the callback token nor the direct JIT blob appears in provider error
  text, Incus instance configuration or diagnostics produced before teardown.
- Exactly one active VM may own a job attempt; duplicate provider invocation
  adopts the durable injected claim without reinjection.
- Failure before successful activation converges through existing failed-runner
  registration removal, claim release, diagnostics and VM destruction.
- Legacy runner services, ExamplePlatform and Captcha are outside this rollout and must
  remain healthy across every canary and rollback.

## Promotion gates

The direct path is not production-proven until a merge-bound canary demonstrates:

1. exact nddev.9/nddev.19 versions and standard-b8 image provenance;
2. a successful official-runner job including outputs, composite action,
   artifact and post-action behavior;
3. no metadata install-script or credential-file requests on the direct path;
4. one-job VM destruction, GitHub runner removal, zero queue intents/claims and
   clean warm replacement;
5. complete redacted diagnostics exported to RustFS with no JIT-shaped secret
   match;
6. a tested metadata-mode rollback with retained nddev.7/nddev.16 binaries and
   standard-b7 image aliases available for full artifact rollback;
7. at least 20 nominal warm samples before any p95 claim.

The first post-nddev.19 nominal series completed on 2026-08-10 and is recorded
in `config/direct-jit-nddev19-latency-audit.json`. All 20 GitHub jobs succeeded,
all 20 one-job VMs were distinct and destroyed, every replacement formed one
continuous clean chain, diagnostics advanced exactly once per sample and the
final fleet converged with zero queue gap, claim, registration or Incus orphan.
The performance target did not pass: nearest-rank median was 5.320 seconds and
p95 was 6.700 seconds against the exclusive below-five-second gate. This closes
the sample-count requirement but not the latency promotion. Instrument and
optimize the assignment-to-runner path, then collect a new immutable series;
never relabel this baseline as passing.

## Consequences

This removes avoidable guest bootstrap RPCs without creating a second Actions
engine or a persistent runner. It adds a small, explicitly NDDev-owned transport
surface to GARM and the provider; therefore exact patch digests, fail-closed
schema checks, reproducible builds and live fault evidence are mandatory. If
the latency gate is still missed, optimization must target measured GARM/GitHub
and session-handshake time rather than weakening one-job isolation.
