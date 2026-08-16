# ADR 0024: Central admission before GitHub job acquisition

## Status

Accepted for merge-bound canary deployment. Code, deterministic scheduler
tests and deployment contracts are complete; live acquisition, cancellation,
restart and saturation evidence remains required.

## Context

GARM upstream `v0.2.1` runs one listener per GitHub runner Scale Set. Each
listener receives repository, workflow, event, queue-time and runner-request
metadata, then independently passes every available request to `AcquireJobs`.
The Incus provider sees only the later aggregate runner create request.

That boundary caused a measured race on `server-example-legacy`: an
integration job arrived while the periodic controller was preparing standard
warm capacity. The provider correctly refused unsafe overcommit seven times,
then preempted the VM after it became `warm-ready`. Capacity remained safe, but
the queue intent arrived too late to suppress speculative work or authorize
preemption of the still-preparing VM.

Repository fairness cannot be reconstructed in the provider because its
request omits the queue metadata. It therefore belongs in GARM at the first
`JobAssigned` message, with a second reconciliation boundary before
`AcquireJobs`.

The first merge-bound nddev.3 live canary on 2026-08-09 exposed an additional
protocol boundary: GitHub delivered `JobAssigned` with UUID `jobId`
`5c3077ba-3664-5824-b2cf-e22a31b25f43` and `runnerRequestId=0`. The fail-closed
coordinator retained the message, provisioned no VM and was rolled back. The
nddev.4 state machine moved admission to the UUID, but a second merge-bound
canary exposed that the same message also omits repository, workflow, event and
queue-time metadata. It likewise failed closed without assigning its warm VM
and was rolled back. The nddev.5 state machine therefore derives repository
identity exclusively from GARM's canonical repository-scoped `ForgeEntity`,
admits the UUID before desired capacity, and enriches workflow metadata plus the
numeric ID only from `JobAvailable`. A regression fixture preserves the exact
sparse observed shape; successful replacement live evidence remains a promotion
gate.

The nddev.5 live canary then completed the full official-runner job in 12
seconds, including immutable-boundary checks, RustFS cache delivery, composite
actions, command files, artifacts and post-actions. Its terminal Scale Set
message contained both `JobStarted` and `JobCompleted`. Processing completion
first removed the intent before the started-state check, so the fsync
transaction failed closed and the message was retained. nddev.6 treats a
completed identity as terminal for the whole batch: matching assigned/started
records are ignored, deletion happens last, and terminal redelivery converges
even when the intent is already absent.

The merge-bound nddev.6 canary, all three fail-closed protocol discoveries,
the bounded terminal reconciliation and the final zero-intent/zero-claim warm
replacement postconditions are recorded in
`config/central-queue-admission-rollout-audit.json` and enforced by a strict
deploy-contract test. This completes the single-sample functional gate, not the
500-job production reliability gate.

## Decision

GARM derivative `v0.2.1-nddev.6` adds one process-wide queue coordinator used
by every Scale Set listener:

- every repository-scoped `JobAssigned` message is joined with the canonical
  GARM entity, published to a private fsync+rename journal and globally admitted
  before GARM updates desired runner capacity; organization and enterprise
  Scale Sets fail closed because the sparse event cannot identify a repository;
- the stable key is `(scale_set_id, job_id)` because GitHub omits the numeric
  `runnerRequestId` until the later `JobAvailable` stage; duplicate delivery
  cannot downgrade an acquired or executing state;
- an exclusive `flock` transaction chooses at most one global in-flight job on
  the current one-worker host;
- release pools are priority 0 and other sparse assigned work is priority 2;
  main/merge/scheduled classification is retained after `JobAvailable` for audit
  but is not claimed as a pre-capacity scheduling signal because GitHub does not
  expose it at `JobAssigned`;
- non-release work ages toward priority 1 every five minutes so an older job
  cannot be starved by an indefinite ordinary stream;
- repositories use durable stride scheduling with configurable integer weights
  and a fail-closed per-repository in-flight limit;
- the chosen assignment is durably `assigned` before VM provisioning; the
  later available message binds its numeric request ID and advances it to
  `acquiring` before `AcquireJobs`; API failure restores it to `assigned`, a
  returned subset becomes `acquired`, start advances it, and completion removes
  it;
- after a process crash, the same GitHub message retries a durable `acquiring`
  ID without charging repository stride twice; a successful API response that
  omits the ID leaves only the 120-second acquiring uncertainty lease and the
  message is acknowledged so later lifecycle events are not hidden behind it;
- a Scale Set message whose assigned or available job was not selected remains
  unacknowledged at GitHub and is retried with a context-aware one-second
  backoff; lifecycle events in a mixed message are recorded before selection;
- the one-worker pilot requires `max_runners=1` on every Scale Set and rejects
  a batched available-job message instead of silently losing work;
- queued, acquiring, acquired and execution states have distinct bounded TTLs;
  crash recovery removes an expired reservation in the next writer transaction;
- configuration and state reject unknown fields, symlinks, public modes,
  unbounded paths, trailing JSON and unsupported pilot values.

GARM is the sole queue-journal writer. The provider and observer use a strict
read-only implementation of the same schema. This deliberately keeps the
provider lifecycle journal single-purpose and prevents a second process from
mutating scheduler state.

Provider `v0.1.5-nddev.16` requires an admitted, non-queued same-Scale-Set intent before a
cold claim or admission. Any active job intent suppresses speculative warm
replenishment. A queue-authorized cold request may atomically reserve and
destroy either `warm-ready` or `warm-preparing` capacity; without that earlier
intent, preparing capacity is not preemptible. The existing provider journal
still owns the cold lease and victim reservation in one fsync transaction.
Reservation records `preempted_by` without overwriting the observed lifecycle;
the victim becomes `deleting` only when teardown actually begins. Release or
expiry therefore restores `created` versus `warm-ready` exactly, while release
fails closed if teardown is already active and awaits Incus reconciliation.

Observer `v0.5.0` snapshot schema 5 reports bounded aggregate queue generation,
stored/active/expired/in-flight counts, oldest age, states, priorities and the
four configured Scale Sets. Repository names are intentionally excluded from
Prometheus labels.

## Consequences

- speculative warm capacity can never outrank already visible GitHub work;
- concurrent repositories cannot independently acquire beyond host capacity;
- repository weights and quotas are durable and deterministic across manager
  restart;
- a corrupt or unreadable queue journal stops acquisition and warm creation
  rather than falling back to uncontrolled upstream behavior;
- an unselected available-job notification stays owned by GitHub rather than
  becoming a local-only queue entry;
- `AcquireJobs` and official Actions runner semantics remain GitHub-owned;
- the current global ceiling of one is a measured Example legacy policy, not a
  portable constant; raising it requires saturation evidence and a reviewed
  configuration change;
- runtime promotion requires an exact rollback set, empty pre-rollout queue,
  repeated same-pool and cross-pool jobs, acquisition failure injection,
  GARM restart with a queued intent, cancellation, zero orphan/missing
  resources, complete RustFS diagnostics, twelve legacy listeners and healthy
  ExamplePlatform/Captcha.
