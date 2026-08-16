# ADR 0037: One queue per class, many hosts behind it

- Status: proposed
- Date: 2026-08-16
- Tracks: https://github.com/NDDev-OpenNetwork/github-actions/issues/259, https://github.com/NDDev-OpenNetwork/github-actions/issues/228

## Context

Adding a host to this fleet is disproportionately hard, and the reason is
structural rather than operational.

A GitHub runner scale set name is unique per entity. Each host runs its own GARM,
and each GARM creates its own scale sets, so two hosts cannot both serve
`nddev-linux-fast` for `NDDev-OpenNetwork`. One host therefore owns one class, and the
configuration says so in as many words: *"This host exists for this pool… one
host serves one name: gha-runner-1 keeps standard warm, gha-runner-2 keeps
integration, and this one keeps fast."*

The consequence is that a new host cannot add capacity to a class. It can only
take a class over. Bringing `gha-runner-3` up as the fast host is a **migration**
— delete the scale set on one host, create it on another — and bringing
`gha-runner-4` up has no defined role at all, because every name is taken.

Measured on 2026-08-16, that is what the estate looks like:

| host | GARM | serves | slots |
| --- | --- | --- | --- |
| gha-runner-1 | yes | `standard` ×3 entities, `fast` ×1 entity | 1 |
| gha-runner-2 | yes | `integration` ×3 entities | 1 |
| gha-runner-3 | no | — | 0 |
| gha-runner-4 | no | — | 0 |

Two of four hosts contribute nothing. Thirty-two vCPU and sixty gibibytes sit
idle behind a naming constraint.

The cost is not only idle hardware. Each new GARM needs its own copy of every
secret the fleet holds — RustFS keys, registry credentials, a JWT secret, an
Incus client certificate — **and a GitHub App private key per tenant**. GitHub
never re-issues a private key, so a fourth host means generating new keys, which
changes the fingerprint every credential anchor pins. Provisioning a host is
therefore a credential event, not a capacity event.

## Decision

**The scale set stays one per class per entity. The provider becomes
multi-host.**

`garm-provider-incus-nddev` today holds one Incus endpoint, and asserts it:
`ExpectedIncusURL = "https://127.0.0.1:8443"`. It will instead hold an ordered
set of endpoints, one per host, and choose between them at admission time.

Nothing above the provider changes. GARM keeps one scale set, one
`provider_name` and one `max_runners` — now the sum across hosts rather than the
capacity of one. GitHub sees exactly what it sees today: one queue per class per
entity, with runners joining it. Tenancy attaches to the queue, as it already
does, and the provider distributes.

Adding a host becomes: provision Incus and a client certificate, append an
endpoint, raise `max_runners`. No scale set is created, moved or deleted. No
GitHub App key is issued. No credential anchor changes.

### Host state comes from the observer, not from a local probe

The provider is single-host today because `hostprobe.Collect` reads `/proc` and
the root filesystem of the machine it runs on. Admission needs that state for
*each* candidate host, and re-deriving it through the Incus API would produce a
second, weaker source: Incus reports resources, not one-minute load, and not the
free-inode percentage the disk-pressure rule already uses.

`gha-fleet-observer` already collects exactly the right shape on every host and
exposes it as `gha_fleet_host_*`. It runs on all four hosts today —
`gha-runner-3` is already shipping telemetry with no GARM at all. The provider
reads each endpoint's host state from that endpoint's observer.

This requires the observer to listen on the private interface rather than only
on loopback, and a firewall rule for it. Both are narrow and reviewable, and the
alternative — a second host-probe implementation over the Incus API — is not.

### Placement is the existing admission function, run per endpoint

`admission.Evaluate` already takes a `HostSnapshot` and a `Request` and answers
whether this host admits this allocation, with a typed reason. It already models
everything placement needs: health, CPU and memory totals, allocations, free
disk, and the per-pool running count.

Multi-host placement is that function run over the candidate endpoints, taking
the first that admits. The rules do not change, the reserve does not change, and
a fleet-wide refusal reports the same typed reason it reports today, per host.

## Consequences

**A host stops being a role.** `config/server-gha-runner-N.yaml` currently
declares which class a host keeps warm. That becomes a property of the class,
not of the host: warm targets are held per class across the fleet, and the
provider decides where.

**`max_running` becomes fleet-wide.** Today it is the one-wide contract that the
queue admission depends on, and the queue intent journal is per host. A
fleet-wide ceiling needs one authority, which the central queue journal already
is — it is described as "GARM's only queue-journal writer".

**The Incus project quota stays per host** and remains the real ceiling on any
one machine: three instances, six CPU units, twelve gibibytes. Placement must
respect it per endpoint, which is what running admission per endpoint does.

**Rollback is an endpoint list of one.** A single-endpoint configuration must
keep working unchanged, so the migration is reversible by removing endpoints,
and the first host to adopt it proves the path before any other moves.

## Alternatives rejected

**One GARM per host, more scale sets.** Give each host its own class name —
`nddev-linux-fast-3`, `nddev-linux-fast-4`. This works today with no code
change, and it pushes the problem onto every consumer: each repository would
choose a host by label, capacity would not pool, and a host going down would
strand the jobs that named it.

**One GARM with four providers.** GARM supports multiple providers, but a scale
set binds to exactly one `provider_name`. Four providers means four scale sets,
which is the naming problem again.

**Re-deriving host state through Incus.** Rejected above: it would be a second
source of a fact the observer already publishes, and weaker in exactly the
dimensions the admission rules use.
