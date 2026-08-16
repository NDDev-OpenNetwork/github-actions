# Current state

What is deployed and what is blocking, as observed on 2026-08-13. This is the
only file that describes the present. Architecture describes what is stable,
the roadmap describes what is next, and `docs/host-baseline.md` and the audit
records under `config/` describe what happened, none of which this file
repeats.

Every line below was read from the fleet or from GitHub on the date above.
Where something was not observed, it says so.

## Hosts

| Host | Role | Reserve mode | Fleet services | systemd |
| --- | --- | --- | --- | --- |
| `server-gha-runner-1` | standard pool warm, and fast since 2026-08-14 | `dedicated` | schema-2 execution services, observer and diagnostic exporter active | `running` |
| `server-gha-runner-2` | integration pool warm | `dedicated` | all fleet services active, control plane merge-bound since 2026-08-14 | `running` |
| `server-gha-runner-3` | future fast pool | `dedicated` | Incus, image and collector foundation only; not yet serving | `running` |
| `server-gha-runner-4` | mandatory rollout target | declared, no warm targets | Incus foundation only; not yet serving. Now has `config/server-gha-runner-4.yaml` and is in `supportedPlatformHosts`, so a provider on it would start; nothing fleet-specific is provisioned and every warm target is zero | `running` |
| `server-gha-services` | telemetry backend | not a fleet host | OpenObserve and collector active | `running` |

A GitHub runner scale set name is unique per entity, so one host serves one
name per class. `gha-runner-1` keeps `nddev-linux-standard` and, since
2026-08-14, `nddev-linux-fast`; `gha-runner-2` keeps `nddev-linux-integration`.

The fast class was architecturally assigned to `gha-runner-3`, which is where it
belongs: that host declares `max_running: 3` and `target_ready: 3` for it, where
runner-1 declares one and holds no warm fast VM. It runs on runner-1 because
runner-3 has no control plane, and moving it is the point of bringing runner-3
up rather than a reason to leave the class unreachable in the meantime. What was
proven on runner-1 on 2026-08-14: the worker image pinned, the Incus profile
created by `reconcile-incus`, an organization-level scale set enabled, and a
canary run to `success` on a `nddev-linux-fast` worker.

`server-example-legacy` left the fleet on 2026-08-12. Every fleet component
was removed from it: the storage pool, the bridge, the ACL, the project, its
eight images, RustFS, Zot, the collector, GARM, the gateway, the observer, the
warm-pool and diagnostic timers, the binaries, the configuration, the PKI, the
diagnostic spool, five service accounts, eight firewall rules and the Incus
packages. Two hundred and four gibibytes were returned to the host. Its ExamplePlatform
and Captcha tenants were untouched and are running. What remains on it is the
organization-scoped runner listeners, which are not fleet components and which
still serve the repositories named below.

Neither serving host has a pending reboot. Both report `running` with no failed
unit. On `server-gha-runner-1`, diagnostic exporter `v0.1.3` exported all 36
observed bundles and bytes with zero pending bundles after schema v3 admitted
the reviewed Example Media and NDDev account scopes separately. Its timer
remains active and the schema-v2 config plus previous binary are retained for
rollback. `server-gha-runner-1` now runs the synchronized schema-2 bundle from
merge `ad8efaa`: provider `v0.1.5-nddev.30`, observer `v0.6.2`, controller and
platform policy share that commit, its fresh observer returns HTTP 200, and a
different warm VM replaced the safely drained canary. The integration host had that
window on 2026-08-14 and took the same bundle from merge `b2cbdcf`, with the
provider at `cae2d18`: claims and leases were proven zero first, a full rollback
set is retained on the host, and its observer -- which had failed closed 41 127
times on a config its old validator rejected -- now reports healthy with zero
collection errors. Both GARM binaries report `v0.2.1-nddev.11`, which is the
reviewed derivative digest, so neither host needed one rebuilt.

`server-gha-runner-1` is the host now behind: it still runs merge `ad8efaa`.
Verified artifacts from `2b3500c` are staged on it in
`/var/backups/gha-fleet/staged-main-2b3500c` and deliberately not installed,
because stopping `garm.service` also stops `gha-fleet-gateway`, which is the
callback endpoint of any running worker, and that host was executing
back-to-back jobs.

The two non-serving hosts also report `running` with no failed unit or pending
reboot. `server-gha-runner-3` has Incus, the promoted standard golden image,
the provider `v0.1.5-nddev.26`, and the collector, but no GARM, gateway, Zot,
RustFS or warm-pool unit. `server-gha-runner-4` has Incus `6.0.0-1ubuntu0.3`
and no fleet control-plane unit or image.

Three things had accumulated behind one another, and the order they were found
in is worth keeping. A package upgrade on 2026-08-11 restarted `incus.service`
several times within a few minutes; `Requires=incus.service` stopped Zot with
it each time; and `StartLimitBurst=5` inside five minutes then held Zot failed
for thirteen hours after Incus was healthy again. That left both hosts
`degraded`, which the admission logic of the day treated as a blocker.

The same upgrade left `reboot-required` for a kernel and libc update, which is
its own admission blocker. It was invisible while it mattered least: the
reconciler only requests admission when it must create a VM, and both pools
were already at target. The first host to drain its warm VM as part of the
version rollout exposed it immediately.

The unit contract still makes the Zot outcome reachable from routine
maintenance, since restarting Incus five times in five minutes is an ordinary
thing to do. A cache whose outage falls back to an uncached build should
survive its dependency being cycled rather than latch failed. Worth fixing
separately.

## Credential

The fleet holds one GitHub App, `nddev-gha-fleet`, app id `100001`.
It is private, owned by the `NDDev-OpenNetwork` organization, and installed on that
same organization as installation `200001` with `repository_selection: all`.
It carries `administration: write` and `metadata: read` and subscribes to no
webhook events. `config/garm-credential-anchor.json` records the non-secret
identity; `docs/github-app-bootstrap.md` is the procedure that recreates it.

## Pinned versions

| Component | Version | Status |
| --- | --- | --- |
| `actions/runner` | `v2.336.0` | official, unmodified |
| GARM derivative | `v0.2.1-nddev.11` | deployed |
| Incus provider derivative | `v0.1.5-nddev.30` | deployed on runner-1 and runner-2 -- from merges `ad8efaa` and `cae2d18` respectively, so the two binaries differ while both report this version (#263); runner-3 remains `.26` |
| Fleet observer | `v0.6.2` | deployed and healthy on runner-1 and runner-2 |
| Diagnostic exporter | `v0.1.3` | deployed on runner-1 and runner-2, both exporting with zero pending bundles |
| Incus | `6.0.0` (Ubuntu LTS package) | deployed |
| Zot | `v2.1.20` | passed its production evidence contract |
| RustFS | `1.0.0-rc.1` | canary only; production promotion blocked |
| sccache | `v0.17.0` | baked into both worker images |

## Repository settings

Merge commits only, with squash and rebase disabled and auto-merge enabled.
`policies/repositories/github-actions.yaml` in the control plane now encodes
that as desired state, so GDS and the live repository agree; before it existed
the compiled policy declared the inverse on three fields. The default branch
requires one status context, `Go verify`, plus the organization signature rule.
`Gate` exists in CI to become that context once branch protection is moved onto
it.

## Blocking work

**The `example-legacy` label cannot be retired yet.** Four organization-scoped
runner listeners on `server-example-legacy` serve roughly twenty-five
workflows across ten repositories, including `nddev-harnesses` security and
validation lanes and the ExamplePlatform deployment. Ten further repositories route GitHub
Code Quality onto the same label, which no workflow file shows because that
analysis runs outside them; `config/code-quality-routing.yaml` records which.
Retiring the listeners requires proven capacity, then migration of both
consumer kinds off the label, then a drain.

The blocker is not capacity. It was, until 2026-08-12, that
`internal/garmbootstrap` bound the control plane to one account through
constants, so the fleet could serve exactly one repository.

**That binding is gone from source.** `internal/tenant` now holds a closed,
compiled-in set of accounts the fleet may serve, and every identity check in
the reconciler and the App bootstrapper compares against the selected tenant
rather than a constant. An unknown tenant is refused rather than defaulted, so
the fail-closed property is unchanged; what changed is that there is more than
one row it can be checked against. `nddev` remains the default, byte-identical
to the deployed constants and covered by a test that says so.

What that does not do is deploy anything. Serving a second account still needs
its own private GitHub App, registered by that account through
`bootstrap-github-app --tenant <id>`, its own GARM credential, its own entity
and its scale sets on a host. Until those exist the fleet still serves one
account in practice.

The capability is deployed. `--entity-kind organization` reconciles the
`NDDev-OpenNetwork` organization as the forge entity and hangs the scale sets from
it, which serves every repository the account holds. It is opt-in, the default
remains the repository entity, and validation fails closed on a kind mismatch in
either direction.

As of 2026-08-14 the organization entity exists on both serving hosts and
carries three enabled scale sets: `nddev-linux-standard` and `nddev-linux-fast`
on `gha-runner-1`, and `nddev-linux-integration` on `gha-runner-2`. The last of
those is what `server-almaty-libraries` needs for its `attest` job; that
repository already runs its contract and admission jobs on the organization
standard set, so the route is proven rather than assumed.

The remainder of the rollout is unchanged: migrate the consumers of the
`example-legacy` label onto them, then drain and remove the listeners.

One caution learned on 2026-08-14: a scale-set message is acknowledged only once
a create succeeds, so a job cancelled while the pool is saturated is redelivered
to its listener indefinitely -- observed at roughly thirty per second. Deleting
and recreating the scale set drops its queue with it; enabling a set at a moment
when the pool is free avoids the situation entirely.

Choosing an organization entity widens who can reach these pools, since GARM
expresses no repository filter for one. A repository allowlist, if it is
wanted, belongs on the GitHub runner group the scale sets are created in.

Capacity, measured on the same date, is a smaller problem than it looked. Each
runner host is eight cores and 16 GiB with an Incus project capped at three
instances, six CPU units and 12 GiB. A standard or integration worker is four
vCPU and 10 GiB, so one of those fills a host; but three `fast` workers at two
vCPU and 4 GiB fit exactly within all three caps, which is the size that pool
was given for that reason. `max_running: 1` on that pool is a config choice
rather than a hardware limit, and `server-gha-runner-3` already sets it to
three. `server-example-legacy` has 32 GiB, twice either runner host, and runs
no fleet VM at all. Its four listeners cost 402 MiB between them, and its 16
GiB reserve is sized for the ExamplePlatform and Captcha stacks rather than for them,
so retiring them frees a label rather than memory.

Also observed on 2026-08-11: free disk 86, 72 and 30 percent, all above the 20
percent floor, and one-minute load 0.08, 0.21 and 0.12. On 2026-08-13 there
was one warm VM on each serving runner host and none on runner-3 or runner-4.

**Warm queue-to-online below five seconds is unreachable as written.** ADR 0031
measured a floor of 5241 ms while the unregistered-warm invariant holds,
against a 5000 ms exclusive target. The gate is recorded as failed and must be
restated before it is worked on again; closing it as written would require
changing the threat model, which is not a performance decision.

**Cache speedup gates are measured against an unrepresentative fixture.** ADR
0032 records that a 55-line crate with 29 locked packages cannot show a cache
ratio even when every compile request hits, because orchestration and linking
dominate. Fixtures must exhibit the property before the gate means anything.

**RustFS production promotion is blocked** on the upstream release being a
release candidate, independently of the repository's own evidence, which
passed.

## Not observed in this pass

Live GARM entity and scale set inventory, Incus instance inventory, warm-VM
claims, the diagnostic spool and OpenObserve retention were not read. Nothing
here asserts their state.
