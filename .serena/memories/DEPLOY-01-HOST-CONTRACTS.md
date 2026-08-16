# Host contracts

What a fleet host must look like, and what is deployed where.

## Topology

Five hosts, reachable through `server-example-legacy` (also aliased
`nddev-bastion`) as a ProxyJump. All are 8 CPU / 15991 MiB / 309 GiB root.

| Host | Role |
| --- | --- |
| `gha-runner-1` | serving; NDDev and Example Media scale sets |
| `gha-runner-2` | serving; NDDev and guild scale sets |
| `gha-runner-3` | Incus and otelcol only; provider binary is stale |
| `gha-runner-4` | declared as a fleet host with every warm target zero; nothing fleet-specific provisioned (#228) |
| `gha-services` | services/collector role |

A serving host runs `garm`, `gha-fleet-gateway`, `gha-rustfs`, `gha-zot`,
`gha-warm-pool.timer`, `gha-diagnostic-exporter.timer`, `otelcol-fleet` and
`gha-fleet-observer`.

## Unit contracts

`deploy/fleet-host/` holds systemd, sysusers and tmpfiles. Two things that have
caused outages:

- **`/etc/garm/cache` must exist as `root:garm` mode 0750.** It is in the
  tmpfiles declaration. The provider's compatibility probe expects the group to
  be *the caller's own effective gid*, so it passes as the `garm` user and fails
  as root -- which is how the runbook runs it (#230).
- **`gha-zot` and `gha-rustfs` latch failed after ten seconds of trouble.**
  `Restart=always` with `RestartUSec=2s` and `StartLimitBurst=5` in a 5-minute
  interval means five attempts at two seconds, then systemd gives up until
  something resets it. `Requires=incus.service` is correct and should stay -- the
  reboot audit shows Zot binding six seconds before the bridge existed -- the
  start limit is what should go (#242).

## Routing: pool is not label

A pool is provider-side policy; the GitHub scale-set **name** is the label a
consumer writes, and they differ. Observed:

| label | entity | pool | host |
| --- | --- | --- | --- |
| `nddev-linux-standard` | `NDDev-OpenNetwork` | standard | runner-1 |
| `example-media` | `example-media` | standard | runner-1 |
| `nddev-linux-fast` | `NDDev-OpenNetwork` | fast | runner-1 |
| `guild-ai-stp-ci` | `example-guild/ai_stp` | integration | runner-2 |
| `nddev-linux-integration` | `NDDev-OpenNetwork` | integration | runner-2 |

runner-1 therefore carries three scale sets against 6 CPU units, so the two
standard scale sets share one slot.

`nddev-linux-fast` carries **no job credential** -- it suits jobs that check out
nothing private, and cannot check out a private repository.

## Coordinated changes

A provider binary and the host's `/etc/gha-fleet/platform.yaml` move together.
The namespace-template correction makes a new provider refuse the config on a
host that still carries the old eight-segment template -- verified by running the
built binary on gha-runner-1. Deploying one without the other fails closed on a
correctly deployed host.

## Rollout

Units are digest-bound to audit records under `config/`. Changing a unit means
re-recording the audit that asserts its content, which is why some fixes need
fresh host evidence rather than only a code change.
