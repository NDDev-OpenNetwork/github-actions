# ADR 0026: Preserve logical GARM identity for claimed warm VMs

## Status

Accepted and deployed. The production failure, merge-bound reproducible
artifacts, transactional rollout and deliberately long reconciliation canary
are recorded in `config/warm-identity-nddev19-rollout-audit.json`. The resumed
20-sample latency series remains a separate performance gate.

## Context

An unregistered warm VM has a physical Incus name such as
`warm-standard-7cf6ba0130ef`. When GARM assigns a job, it creates a distinct
logical runner identity such as `nddev-qofu6jxasefm`. The provider durably binds
that job name to the physical VM and returns the physical name as `ProviderID`,
which lets `GetInstance` and `DeleteInstance` resolve later operations safely.

Provider nddev.18 also returned the physical name as `ProviderInstance.Name`
from `ListInstances`. GARM's Scale Set reconciler compares that field with its
logical database names. During direct-JIT canary run `31341001674`, the job ran
long enough for periodic reconciliation to observe the mismatch. At
`2026-08-09T23:05:14.856Z`, it classified the physical warm name as absent from
the database and deleted the VM while the official `Runner.Worker` was still in
`upload-artifact`. It then classified the logical name as absent from the
provider. GitHub correctly refused registration deletion because the runner was
executing a job, leaving the job and central queue intent running without an
execution VM.

The earlier short canaries completed before that reconciliation window. They
proved the happy path but did not cover identity consistency over a job longer
than the reconciliation interval.

## Decision

Provider `v0.1.5-nddev.19` maintains two explicit identities:

- `ProviderInstance.Name` is the logical GARM runner name;
- `ProviderInstance.ProviderID` is the physical Incus VM name.

The projection applies to warm activation, `GetInstance` and `ListInstances`.
It is not trusted from mutable instance metadata alone. When metadata contains a
GARM job name, the provider asks its durable admission controller to resolve
that name and requires the result to equal the exact observed Incus instance.
A missing admission controller, failed journal read or mismatched binding fails
the whole inventory call closed instead of exposing an ambiguous identity.
Cold instances remain unchanged because their logical and physical names are
identical. Unregistered warm capacity has no job name and remains visible under
its physical name only in non-Scale-Set inventory.

Observer `v0.6.0` schema 6 adds `queue.uncovered_running` and the corresponding
`gha_fleet_queue_uncovered_running` metric. A running queue intent must have a
durable `created` or `warm-claimed` execution lease. Any positive coverage gap
makes platform health false while leaving collection health independently
observable.

## Invariants

- one claimed VM has exactly one logical name in GARM reconciliation;
- physical identity remains available for Incus operations and diagnostics;
- a claim cannot rewrite the identity of another VM;
- inventory ambiguity fails closed and never triggers best-effort deletion;
- no VM that is still executing official runner code may be removed merely
  because logical and physical names differ;
- a running queue intent without an execution lease cannot report healthy;
- legacy runners, ExamplePlatform and Captcha remain outside the rollout boundary.

## Promotion gates

1. provider and observer unit/race tests cover valid and invalid identity
   projection plus uncovered running work;
2. nddev.19 and observer v0.6.0 binaries are reproducible and bound to the
   ordinary repository merge commit;
3. rollout has exact pre-state, rollback artifacts and automatic rollback on
   any failed postcondition;
4. a direct-JIT job runs longer than two reconciliation periods and completes
   all steps, artifact upload and post-actions;
5. the same logical runner remains present in every provider inventory sample,
   the physical VM remains present until authoritative completion, and both are
   then removed exactly once;
6. diagnostics reach RustFS, the replacement warm VM is distinct, queue and
   claim counts return to zero, and no runner/VM/disk orphan remains;
7. all 12 legacy listeners and retained application health remain unchanged.

Gates 1 through 7 passed on 2026-08-09. Canary run `31342261697` kept the same
logical runner and physical VM alive across a delayed `workflow_job` transition
and a 45-second workload, then completed artifact upload and post-actions. The
queue, claim and instance planes converged to zero active work with one distinct
replacement warm VM, zero orphan or missing instances and synchronized RustFS
diagnostics. This correctness promotion does not make a latency claim; the
20-sample p95 gate remains open.

## Consequences

The provider API now represents the identity split explicitly instead of
leaking an Incus implementation detail into GARM's logical reconciliation key.
The change does not alter the official runner, GitHub protocol, cache identity,
VM security boundary or one-job teardown policy. Future provider backends must
apply the same contract whenever a pre-created physical worker is adopted under
a new logical runner name.
