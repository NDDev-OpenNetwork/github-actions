# ADR 0034: Preemptible unregistered warm capacity

## Status

Accepted and deployed. The merge-bound v14 rollout, durable counter canary and
postconditions are recorded in `config/preemption-v14-rollout-audit.json`.

## Context

`server-example-legacy` exposes eight non-overcommitted CPU units and reserves
four for the host and retained services. One four-vCPU standard warm VM
therefore occupies all schedulable CI capacity. A real integration job cannot
start while that speculative VM exists, even though the warm VM is
unregistered, unclaimed and contains no job identity.

Deleting the warm VM before reserving the real job is racy. The periodic warm
controller can replenish the empty slot between deletion and cold admission.
Claiming the warm VM for an unrelated pool is invalid because the images,
profiles, Docker capability and trust metadata differ.

## Decision

Provider `v0.1.5-nddev.13` makes every unregistered `warm-ready` lease
speculative and preemptible by a real cold request from another pool:

- one fsync-backed journal update admits the cold request and binds the
  selected warm leases with `preempted_by=<cold lease>`;
- only exact `warm/*`, `warm-ready`, unclaimed leases are eligible;
- claimed, injected, cold, deleting, foreign and same-pool leases are never
  selected;
- selection is deterministic and prefers larger CPU/memory reservations, then
  the oldest instance and stable instance name;
- projected CPU, total-memory and live-available-memory reserves must all pass
  before the reservation is published;
- the provider captures diagnostics and destroys each reserved warm VM through
  the normal journal-aware teardown path;
- admission is evaluated again from fresh host and Incus state before cold VM
  creation;
- retries recover the durable reservation and cannot claim or replenish the
  victim; expiry or explicit release restores the exact observed lifecycle if
  teardown never happened.

Provider `v0.1.5-nddev.15` and later keep reservation ownership and observed lifecycle
orthogonal: a victim remains `created` or `warm-ready` while merely reserved,
then `MarkDeleting` changes it to `deleting` only when teardown starts. This
preserves `warm-preparing` state across release and expiry without adding a
second source of lifecycle truth. Release fails closed after teardown starts;
Incus reconciliation must then prove whether the VM still exists.

The original provider journal advanced to schema 3. Provider
`v0.1.5-nddev.14` advances it to schema 4 with a monotonic
`warm_preemptions_total` value. Schema 1, 2 and 3 documents are read and
migrated in memory without weakening validation. Observer `v0.3.0` schema 4
exports both the bounded `gha_fleet_provider_warm_preemptions` gauge and the
durable `gha_fleet_provider_warm_preemptions_total` counter. The counter is
incremented in the same journal transaction that binds each victim to the cold
request; idempotent retries do not increment it again.

This is capacity preemption, not repository fairness. GitHub Scale Set job
messages contain repository, workflow, event and queue-time metadata, but
GARM currently acquires every available request independently per Scale Set.
Weighted cross-repository ordering therefore belongs before `AcquireJobs`, not
in the Incus provider. It remains a separate scheduler change.

## Consequences

A queued integration/release/cold job no longer depends on an operator stopping
the warm timer or manually draining the standard VM. The warm latency
optimization remains best-effort and can never outrank assigned work.

The live v14 rollout retained one exact rollback set and proved automatic
preemption, successful job completion, diagnostic export, one-job teardown,
clean warm replenishment, zero orphan/missing resources, zero failed units,
twelve legacy listeners and healthy ExamplePlatform/Captcha. The preparing-warm retry
window captured by that evidence is the scheduler gap addressed by ADR 0024.
