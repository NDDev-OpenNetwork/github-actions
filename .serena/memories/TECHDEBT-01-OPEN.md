# Open debt

Known defects with the issue that tracks each. Anything here is deliberate and
recorded, not forgotten.

## Capacity is arithmetic, not a pilot constant

The one-job ceiling is what the declared Incus project caps imply, for the heavy
classes. `gha-fleet capacity --config <host>` reports it and names the binding
cap: `project_max_cpu_units: 6` divided by a four-vCPU worker is one, and the
fast class at two vCPU fits three -- which is the `max_running: 3` gha-runner-3
already declares.

A second concurrent integration worker does not fit on one host: two want
20480 MiB and the host has 15991, so opening the CPU cap does not help. More
integration throughput needs a host, not a number (#257, #216).

## Enforcement not yet implemented

- **#240 / ADR 0036** -- the admission boundary is still the union of every
  registered tenant. The decision is to scope it to the entity a job arrived
  through; implementation waits on #220, because it makes the queue intent
  load-bearing for admission. **Do not implement the per-pool remedy the issue
  text still recommends**; a tripwire test explains why.
- **#236** -- every tenant shares one read-write cache namespace. The root is now
  one function, so making it per-tenant is one change; the live IAM and RustFS
  work is outside repository authority.
- **#216 / #253 / #257** -- one job per host, compiled into nine places. Consumer
  queues back up behind it.

## Live skew

- **#263** -- `gha-runner-1` and `gha-runner-2` run different provider binaries
  that both report `v0.1.5-nddev.30`. Fixed at source; the hosts have not been
  re-synchronized.
- **#259** -- `gha-runner-3` has a `.26` provider and no GARM. **#228** --
  `gha-runner-4` is Incus only; promoting it needs both a
  `config/server-gha-runner-4.yaml` and an entry in `supportedPlatformHosts`.

## Host contracts

- **#230** -- the compatibility probe expects the caller's own gid, so it fails
  as root and passes as `garm`.
- **#242** -- the cache units latch failed after ten seconds of trouble.
- **#220** -- observer and queue-journal schema compatibility during rollout.
- **#218** -- OpenObserve retention is declared and unverified.

## Consumers

The two Almaty blockers in #238 are both routing decisions rather than code. The
SSH destination is the jump host `82.200.134.102` on tcp/22 -- the staging box
itself is `172.20.99.19`, a private address the egress ACL rejects before any
allow is considered and always will. The per-pool `EgressAllowlist` is the right
mechanism, but it lives on `release-allowlist` and `nddev-linux-release` is
declared and not published, so opening a port there would be a hole in a pool
nothing routes to.

- **#232 / #233 / #235 / #237 / #238** -- what Almaty jobs need that the fleet
  does not publish yet: `node` on the standard PATH, a non-Docker secret-scan
  path, SSH egress, an organization-level integration scale set.

## Shape of the recurring defect

Almost everything above is one fact written down twice. When fixing anything
here, the question to ask is not "which copy is right" but "which one should be
derived, and what test holds it there".
