# Admission boundaries

Who may run work here, and where each refusal happens.

## The tenant registry is compiled in and closed

`internal/tenant` names every account the fleet serves. Onboarding is a reviewed
code change; there is no runtime allowlist. Three rows today: `nddev`
(`NDDev-OpenNetwork`), `guild` (`example-guild`), `example-media`
(`example-media`). `ServesWholeAccount` widens a row from one repository to
every repository that account holds.

## Where a job can be refused

| Check | Where | Refuses |
| --- | --- | --- |
| repository is registered | `provider/incus.go` `isRegisteredRepositoryURL` | an account the fleet never heard of |
| repository is within the pool's tenant | `provider/incus.go` `repositoryWithinTenant` | another tenant's repository, **only if the pool declared a tenant** |
| provider version matches the policy | `provider/admission.go` | a binary that is not the one the host config pins |
| host is supported | `provider/admission.go` `supportedPlatformHosts` | a platform policy for an unknown host |
| worker image mapping | `provider/admission.go` `validateWorkerImageMappings` | a pool with no pinned worker image |

## What is decided and not yet implemented

**ADR 0036: tenancy is scoped to the entity, not the pool.** The per-pool check
above is inert in production -- no pool declares a tenant, and none should: a
pool serves every entity whose scale set is registered against it, so declaring
one refuses the others at create time. `internal/deploycontract` has a tripwire
test that fails if a pool declares a tenant.

Implementation waits on #220, because moving the boundary onto the entity makes
the queue intent load-bearing for admission rather than only for capacity.

## Repository identity has two live forms

Both appear in the queue-intent journals on both serving hosts:

- `owner/name` -- from a repository entity, or from an organization entity after
  `JobAvailable` binds one;
- bare `owner` -- from an organization entity whose `JobAssigned` names only the
  job.

The bare form is a state of the intent, not an identity a worker may run under.
It is not a trailing-slash artefact; it follows the entity kind.

## Two ways the queue used to stall, both fixed

- **A batched message was refused whole.** How many jobs GitHub puts in one
  protocol message is GitHub's decision; how many run at once is ours, taken in
  `SelectForAcquire`, which selects exactly one per call. Conflating them refused
  a message the system could record, and the refusal is not durable -- 28502
  redeliveries in an hour, placing nothing. Fixed in `v0.2.1-nddev.12`.
- **An assigned intent held the slot for a day.** `assigned` shared `running`'s
  execution TTL. It means "won the slot, will be acquired next" and the gap is
  seconds, so a job GitHub had already cancelled froze a one-wide class for
  twenty-four hours. Fixed in `v0.2.1-nddev.13`; it now expires on the acquired
  TTL.

Stored expiries are not recomputed, so a stale intent from before the fix has to
be dropped by hand.

## Capacity is a compiled-in ceiling, not a config value

One job per host is enforced in nine places across two components -- seven in
`third_party/garm/overlay/workers/scaleset/queue_intent.go` and two in
`internal/garmbootstrap/reconcile.go`. Raising it is a derivative rebuild and a
scale-set recreation on GitHub's side, not a config edit. Tracked by #216.
