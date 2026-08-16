# ADR 0036: Tenancy is scoped to the entity, not the pool

- Status: accepted
- Date: 2026-08-15
- Tracks: https://github.com/NDDev-OpenNetwork/github-actions/issues/240

## Context

The provider's admission boundary was the union of every tenant the compiled
registry names. `isRegisteredRepositoryURL` answers "is this any account we
serve", which was the right question while the fleet served one account and the
wrong one once it served three: a job from any registered tenant was admissible
on any pool, and a pool carries a trust class and a cache write scope.

Issue #240 proposed narrowing the boundary to the pool: `Pool.Tenant` is already
in the schema, so admission would compare the requesting account against the
tenant its pool declares. `cae2d18` implemented exactly that, guarded by
whether the pool declared a tenant at all, because reading `Pool.TenantID`'s
default as a declaration had refused every non-default tenant on every host.

Reading the fleet on 2026-08-15 shows why the guard was necessary and why the
proposal cannot be completed as stated.

**No pool declares a tenant.** There is no `tenant:` key in any pool of any of
the three host configurations, so `declared` is false everywhere and the
per-pool check never runs in production. The narrowing landed and is inert.

**A pool serves several tenants at once.** Each tenant reaches the fleet through
its own GitHub scale set, and those scale sets share a pool:

| host | scale-set worker | entity |
| --- | --- | --- |
| `gha-runner-1` | `scaleset-worker-nddev-linux-standard-2` | `NDDev-OpenNetwork` |
| `gha-runner-1` | `scaleset-worker-nddev-linux-standard-3` | `example-media` |
| `gha-runner-2` | `scaleset-worker-nddev-linux-integration-3` | `example-guild/ai_stp` |

Two entities, one pool name, on the same host. Declaring `tenant:` on
`nddev-linux-standard` would have to name one of them, and would refuse the
other's jobs at create time — after the App is installed, the scale set is
registered and the job is assigned, which is the same late failure the
registry-wide boundary was introduced to avoid.

That is not a configuration mistake. It is the topology working as designed: a
pool is provider-side policy — image, resources, trust class, cache scope — and
a scale set is GitHub-side routing for one entity. One image serves many
entities; that is the point of publishing a class.

**The identity a job carries depends on the entity kind.** The queue-intent
journals carry both forms simultaneously:

```text
gha-runner-1: NDDev-OpenNetwork/github-actions, NDDev-OpenNetwork, example-media
gha-runner-2: NDDev-OpenNetwork/github-actions, example-guild/ai_stp, NDDev-OpenNetwork
```

An organization entity's `JobAssigned` message names only the job, so the intent
is admitted against the account and binds a repository when `JobAvailable`
arrives. A repository entity names its repository from the start. The bare
account form is therefore a legitimate intermediate state, not the trailing-slash
accident it was previously taken for.

## Decision

**The tenancy boundary is scoped to the entity a job arrived through, not to the
pool it is placed on.**

1. `Pool.Tenant` stops being the enforcement axis. A pool may serve any entity
   whose scale set is registered against it; what a pool decides is capability
   and privilege — image, resources, trust class, cache write scope — and those
   are properties of the class, not of the customer.

2. Admission compares the requesting repository against **the tenant that owns
   the entity the job arrived through**, resolved from the queue intent, rather
   than against the union of the registry or against a pool's declaration.

3. The account form of a repository identity is accepted only while an intent is
   unbound. A worker is never launched carrying an account as its repository
   identity: binding happens before create, or create is refused. This removes
   the contradiction between `narrowBootstrapRepository` and
   `TestWholeAccountBootstrapRetainsAccountIdentity` by choosing the first —
   the account form is a state of the intent, not an identity a job may run
   under.

4. `internal/tenant` stays compiled-in and closed. Onboarding remains a reviewed
   code change; nothing here introduces a runtime allowlist.

## Consequences

- The registry-wide check stays as the cheap outer refusal. It is not the
  boundary any more, but it still rejects an account the fleet never heard of
  before any further work is done.
- `Pool.Tenant` becomes advisory or is removed. It must not be left in the
  schema as an enforcement axis nobody enforces, which is what it is today.
- Per-repository attribution stops depending on entity kind. Diagnostics, cache
  namespacing and fairness all key on a repository, and today an
  organization-served job can reach them with an account instead.
- The queue intent becomes load-bearing for admission, not only for capacity.
  Its schema compatibility rules matter more, and #220 has to be settled before
  this is deployed rather than alongside it.
- Fairness in organization mode remains open: the scheduler keys policy by
  repository while an unbound intent carries an account, and the admission config
  cannot express an account-level policy. That is #253's neighbourhood and is not
  resolved here.

## Alternatives considered

**Declare a tenant per pool, as #240 proposed.** Rejected on evidence: the
deployed topology puts two entities on one pool, so this refuses real work. It
could be made to fit by giving every tenant its own pool, which multiplies images
and warm capacity by the number of tenants for no isolation gain — the isolation
that matters is already per-VM.

**Keep the registry-wide boundary and rely on GitHub's runner groups.** Rejected:
it makes the provider's refusal depend on a setting outside this repository, and
the provider is the last place that can refuse before a VM is created with a
tenant's credential.

**Accept the account identity and attribute elsewhere.** Rejected: every
downstream consumer — cache prefix, diagnostics scope, fairness — is keyed by
repository. Accepting an account means each of them needs its own fallback, and
three fallbacks are three places to get it wrong.
