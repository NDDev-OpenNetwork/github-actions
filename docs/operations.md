# Operations

This is the initial runbook contract. Exact commands and systemd units are added
after host discovery and the single-VM pilot. Host-specific secrets and paths do
not belong in this repository.

## Coexistence during migration

The current self-hosted runner implementation remains active until the new fleet
passes its pilot gates. ExamplePlatform and Captcha remain running and are outside CI
cleanup scope. Existing runner listener services are drained, not abruptly
stopped while jobs are active.

No old runner data is removed until all of the following are true:

1. every legacy listener reports idle;
2. the new pilot has completed representative parity jobs;
3. the exact deletion manifest is revalidated on the server;
4. retained ExamplePlatform/Captcha resources are proven not to intersect the manifest;
5. the user-authorized no-backup cleanup window is active.

The cleanup is intentionally irreversible and produces only an audit ledger of
what was deleted, not a backup of deleted runner state.

## Service order

Target startup order:

1. host storage and networking;
2. Incus/KVM and image storage;
3. RustFS and local OCI registry;
4. lifecycle journal;
5. GARM provider and GARM;
6. NDDev admission/reconciliation service;
7. telemetry exporters;
8. warm-pool replenishment.

Admission remains closed until health checks prove the journal, Incus, GitHub
API and required image digest are usable. Cache failure may permit a documented
uncached mode, but must never weaken trust separation.

## Provisioning a new fleet host

Ubuntu splits what an Incus full VM needs across packages that
`--no-install-recommends` does not pull in, and each absence surfaces later and
less clearly than the last. This is the set, found by provisioning
`gha-runner-3` and hitting them one at a time:

```bash
sudo apt-get install -y --no-install-recommends \
  incus incus-client incus-agent \
  qemu-system-x86 qemu-utils qemu-system-modules-spice ovmf \
  dnsmasq-base lvm2 jq curl gpg ca-certificates xz-utils zstd unzip
```

`gha-fleet preflight` and the dependency check now name all three of the
easily-missed ones, so a fresh host refuses with the package name rather than
with a QEMU feature-check failure or a bridge that will not create.

Two host settings are not packages and are verified before the Incus API is
reached:

- **IP forwarding.** The bridge NATs worker egress, so the host forwards. Incus
  turns it on when it creates the bridge, but the firewall contract is checked
  first and UFW reports `disabled (routed)` while forwarding is off. Set it
  explicitly in `/etc/sysctl.d/` rather than relying on that ordering.
- **UFW.** It must be active with `deny (incoming), allow (outgoing), deny
  (routed)` before the reconciler runs, and the operator's own SSH rule must
  exist before it is enabled. The reconciler adds only its own commented rules
  and never adopts or removes an unrelated one, which is why the access rule is
  yours to place:

  ```bash
  sudo ufw allow from 172.16.0.0/20 to any port 22 proto tcp
  ```

Then reconcile the foundation, naming the pool the host will serve:

```bash
gha-fleet reconcile-incus --config /etc/gha-fleet/platform.yaml \
  --pool nddev-linux-standard
gha-fleet reconcile-incus --config /etc/gha-fleet/platform.yaml \
  --pool nddev-linux-standard --apply
```

Every step above is fail-closed and idempotent: a second apply is empty, and a
missing prerequisite refuses by name rather than leaving a partial host.

## Preflight

Before enabling a pool, record:

- exposed CPU topology, provider guarantees, sockets/cores/SMT and CPU model;
- total RAM and NUMA placement;
- NVMe devices, filesystem, RAID/redundancy and measured latency/IOPS;
- link speed, routes, DNS and GitHub endpoint reachability;
- Incus version, storage pool/driver and KVM availability;
- current free disk by images, VMs, OCI registry, RustFS and logs;
- exact runner, GARM, provider and golden-image digests;
- current ExamplePlatform/Captcha resource and network reservations.

`gha-fleet validate` must succeed against the proposed policy. A configuration
fingerprint is recorded with every deployment and worker audit record.

The executable host preflight applies those reserves to an observed Linux host
and fails closed if KVM, capacity, disk, maintenance state or Incus is not ready:

```bash
sudo gha-fleet preflight \
  --config /etc/gha-fleet/platform.yaml \
  --pool nddev-linux-fast
```

The command is read-only and emits a secret-free JSON snapshot plus stable
findings. Exit code `3` is a policy rejection, not an internal error. Legacy
listener and worker counts are recorded as coexistence evidence and never
trigger automatic service mutation.

## Cache plane rollout

Validate the exact artifact contract before any download or install:

```bash
gha-fleet validate-cache \
  --manifest /etc/gha-fleet/cache-artifacts.yaml
```

Read the component fields independently. Zot may report `production` and
promotion-allowed while RustFS reports `canary-only`; the aggregate flag stays
false until every component is production-ready. Never reinterpret that
aggregate as a reason to downgrade an already accepted component or to bypass
a blocked one.

Install RustFS and Zot only at the SHA-256 values emitted by the deployment
ledger. The non-secret units and Zot policy are under
`deploy/fleet-host/`; root-owned material is limited to:

| Path | Purpose |
| --- | --- |
| `/etc/gha-fleet/garm-credential-anchor.json` | non-secret exact App/installation/public-key identity for GARM reconciliation |
| `/etc/gha-fleet/secrets/rustfs-{access,secret}-key` | server root identity; never a job credential |
| `/etc/gha-fleet/secrets/rustfs-rpc-secret` | independent internode RPC authentication secret; never reuse the S3 root secret |
| `/etc/gha-fleet/secrets/rustfs-diagnostics-{access,secret}-key` | prefix-only diagnostic exporter identity |
| `/etc/gha-fleet/rustfs-cache-identities.yaml` | reviewed non-secret RustFS repository/trust identity contract |
| `/etc/garm/cache/rustfs-{trusted-writer,untrusted-writer,promoter,release-reader}-{access-key,secret-key}` | reconciled non-root cache identities; `root:garm`, `0640` |
| `/etc/gha-fleet/trust/rustfs-ca.pem` | public RustFS CA; singly linked `root:root`, `0644`, with mode-`0755` parents |
| `/etc/gha-fleet/diagnostic-exporter.yaml` | non-secret exporter config loaded as a private systemd credential |
| `/etc/gha-fleet/pki/cache/rustfs-{cert,key}.pem` | RustFS TLS identity |
| `/etc/gha-fleet/pki/cache/zot-{cert,key}.pem` | Zot TLS identity |
| `/etc/gha-fleet/secrets/zot.htpasswd` | exact repository/trust-scoped bcrypt hashes, no broad or admin identity |
| `/etc/gha-fleet/secrets/zot-github-actions-{trusted,untrusted,promoter,release}-{username,password}` | root-only machine credential pairs; distribute only the role required by a disposable worker |

Cache service files are loaded with systemd credentials; the non-secret GARM
anchor is read directly by the root-invoked reconciler. RustFS reads its root
key from credential files, while the reviewed launcher exports an independently
generated RPC secret only into the RustFS process before `exec`. Zot uses TLS,
htpasswd, three exact `cache/example-user/github-actions/{trusted,untrusted,promoted}/**`
paths and a default-deny catch-all, with no extension configuration. Do not
enable either unit if
its binary digest, cache-manifest fingerprint, certificate SAN or native config
validation differs from the deployment ledger.

RustFS's local rolling log is not an unlimited diagnostic archive. Keep the
reviewed `warn` level, 512 MiB total ceiling, 64 MiB per-file ceiling and
15-minute cleanup interval until external telemetry is operational. Raising the
level for diagnosis requires a time-bounded override plus an immediate disk
check; never leave `info` or `debug` enabled for normal CI traffic.

Before a runner can use the cache, execute both smoke scripts from a disposable
VM with its installed cache CA. Enable their restart and crash-recovery modes,
then run negative probes for anonymous, wrong-trust and cross-repository access.
The RustFS IAM reconciler must use long-lived repository/trust-scoped users;
STS and service accounts remain forbidden. See `docs/cache-plane.md` for the
complete promotion gate.

Run `gha-fleet reconcile-zot-credentials` first as a read-only plan. On the
initial migration it accepts only the two known bootstrap usernames; any mixed,
partial or unknown htpasswd state fails closed. `--apply` requires root,
generates four independent 384-bit passwords, writes mode-0600 credential files
atomically and replaces htpasswd last with bcrypt cost 12. Its JSON output is
redacted. Restart Zot only after the applied result reports `state_after` as
`managed`, then execute positive and cross-namespace negative tests for every
role.

Validate the RustFS identity contract without reading credentials or touching
the service:

```bash
gha-fleet validate-rustfs-cache \
  --config /etc/gha-fleet/rustfs-cache-identities.yaml
```

Create `/etc/garm/cache` as a real `root:garm` mode-`0750` directory. Then run
the root-invoked reconciler first without `--apply`; even the plan reads the
mode-`0600`, `root:root` RustFS root pair in order to distinguish absent,
managed and unsafe remote-only identities:

```bash
sudo gha-fleet reconcile-rustfs-cache \
  --config /etc/gha-fleet/rustfs-cache-identities.yaml
sudo gha-fleet reconcile-rustfs-cache \
  --config /etc/gha-fleet/rustfs-cache-identities.yaml \
  --apply
sudo gha-fleet reconcile-rustfs-cache \
  --config /etc/gha-fleet/rustfs-cache-identities.yaml
```

Accept the apply only when it reports `fresh -> managed`, all four identities,
the exact 64 GiB quota and successful effective-policy probes. The final plan
must report `managed -> managed` with an empty action list. Output contains
deterministic access-key identifiers and paths but never secret keys or raw
RustFS response bodies. Applying this contract does not restart RustFS and
does not promote RC.1 or inject a credential into a runner; those remain
separate reviewed gates.

The accepted 2026-08-09 live identity rollout is bound to controller merge
commit `4d3ebc2670d78271961d6809ba12455b09194c16` and recorded in
`config/rustfs-cache-identity-rollout-audit.json`. It proves all four effective
trust boundaries, zero remaining probe objects, a subsequent empty plan and no
credential matches in the host journal. It does not waive the independent
RustFS stable-release gate or authorize workflow credential distribution.

After restarting Zot, poll the authenticated endpoint for up to 60 seconds and
accept readiness only when an anonymous `/v2/` request returns 401. An initial
connection refusal is a startup state, not proof of a bad config; crossing the
deadline must restore both the previous config and htpasswd before restart.

Install `scripts/cache-network-ready.sh` as
`/usr/local/libexec/gha-fleet/cache-network-ready`, owned by `root:root` and
mode `0755`, before installing either cache unit. RustFS and Zot require and
start after `incus.service`; each waits up to 120 seconds for
`/sys/class/net/gha0` under a 150-second systemd startup budget. The probe reads
only sysfs and does not need Netlink, a host socket or a credential. Both
long-running daemons use `Restart=always`. This is mandatory for Zot v2.1.20
because a bind attempt made before the bridge address exists logs an error but
exits with status zero, which `Restart=on-failure` cannot recover. A manual
restart after boot is a repair, not reboot-gate evidence; repeat the controlled
reboot and require an automatic active state before promotion.

When an Incus create/launch command is invoked inside `ssh ... bash -s`, redirect
that Incus command's stdin from `/dev/null`. Incus accepts instance YAML on
stdin in non-interactive mode; without the redirect it can consume the rest of
the maintenance script as configuration. Always export secret-free evidence
before destroying an audit VM, then prove the VM, image and storage volume are
all absent.

For Zot supply-chain verification, use two clean `v2.1.20` checkouts and
different absolute module/build cache paths with
`scripts/zot-reproducible-build.sh`. Both emitted JSON records must bind the
GitHub-verified commit, report a clean VCS tree and match the pinned release
binary SHA-256. A shared source checkout or cache is not two independent
builds.

Run `scripts/zot-storage-audit.sh` only as root inside a disposable full VM.
It refuses any storage path except its dedicated loopback ext4 boundary. The
script performs real orphan GC and filesystem exhaustion, so never point it at
`/var/lib/gha-cache/zot`, a retained application path or a host filesystem.
Capture its JSON result outside the VM before destroying the instance.

The diagnostic exporter is a canary exception with a dedicated built-in IAM
user rather than a worker credential. Provision it only through
`rustfs-diagnostic-exporter-bootstrap.sh`. Its replacement policy mapping
permits exactly GET/PUT (and therefore HEAD) under `diagnostics/v1/` in
`gha-diagnostics-canary`; LIST, bucket-location, DELETE, cross-prefix,
cross-bucket and anonymous probes must all return `403`. Install
and start the timer only after the bootstrap, an explicit one-shot exporter run
and exact status reconciliation succeed. A failed exporter must leave the
mode-`0600` source bundle in `/var/lib/gha-fleet/diagnostics`. The service must
consume the configuration from its systemd credential mount, see the spool only
through `/run/gha-diagnostic-exporter-source`, and be unable to traverse
`/etc`, `/var/lib/gha-fleet`, `/var/lib/garm` or `/var/lib/gha-cache`.
Configuration schema `v3` has exact sorted repository, account and pool
allowlists. The verified manifest identity selects a repository, account or
unassigned-warm namespace; an unknown repository or account fails closed. A
host deployment must narrow the pool allowlist to only the Scale Sets that host
serves, even when the repository example contains both reviewed Linux pools.

`rustfs-scoped-smoke.sh` is the worker-side probe. Its inputs are a temporary
scoped identity plus pre-created allowed and denied buckets. Root credentials
stay on the manager host; the probe verifies integrity and both sides of the
namespace boundary, deletes its object and fails if cleanup does not converge.

`rustfs-quota-smoke.sh` creates a private audit bucket, applies a bounded hard
quota, proves excess writes are rejected and deleted capacity becomes writable,
then removes the quota and bucket. It waits up to 180 seconds for the first
authoritative usage snapshot because the default scanner cycle is 60 seconds;
`RUSTFS_QUOTA_READY_TIMEOUT_SECONDS` may bound that wait from 60 through 900
seconds. `rustfs-lifecycle-smoke.sh` is more destructive and refuses to run unless
`RUSTFS_LIFECYCLE_AUDIT_ACCELERATED=1`; use it only against a disposable server
started with the reviewed time-compression and one-second scanner settings. It
proves lifecycle round-trip, matching-prefix expiry and negative-control
survival without waiting a real day. Neither audit authorizes those debug
settings in the live service.

## Incus foundation reconciliation

Render the complete desired foundation without contacting Incus:

```bash
gha-fleet reconcile-incus \
  --config /etc/gha-fleet/platform.yaml \
  --pool nddev-linux-standard
```

The only mutating form is explicit and must run as root on the Linux host:

```bash
sudo gha-fleet reconcile-incus \
  --config /etc/gha-fleet/platform.yaml \
  --pool nddev-linux-standard \
  --apply
```

Before the first mutation the command verifies the trusted local Unix socket,
exact Incus 6.0.0 baseline, QEMU, LVM, required API extensions and the installed
`qemu-system-modules-spice` host package required by the Ubuntu QEMU split. It
also requires active UFW with `deny incoming`, `allow outgoing`, and `deny
routed` defaults. It adds only `gha-fleet-*` rules declared in the rendered
plan, preserves every unrelated host rule, and fails closed if it discovers a
stale managed rule. It then binds the TLS listener to loopback only and
reconciles the bounded LVM pool, ACL, managed bridge, restricted project and
explicitly selected profiles.
It does not create an instance, register a runner, restart a service or issue
DELETE.
Incus 6.0 does not expose a project-level VM nesting restriction. Runtime
probing and the exact `v6.0.0` source show that this release also accepts but
does not enforce `security.nesting=false` for VMs: its QEMU command retains
`hv_passthrough`, the guest CPU exposes VMX/SVM and `/dev/kvm` appears. The
managed builder, smoke VM and pinned provider therefore set the single closed
instance value `raw.qemu=-cpu host,-vmx,-svm`. The project permits VM low-level
configuration only for that manager operation; containers remain disabled and
their low-level setting remains blocked. Workers never receive Incus API or
socket access. Smoke acceptance requires both CPU flags and `/dev/kvm` to be
absent. The same LTS does not expose the newer storage-pool access restriction:
the aggregate 100-GiB project disk limit remains enforced and every managed
profile names the dedicated `gha-lvm` pool explicitly.

Run the same apply command a second time. Acceptance requires an empty
`changes` array. Existing storage drift and unmanaged network-name collisions
fail closed rather than being repaired destructively.

### One-time loop-pool headroom grow

ADR 0005 permits exactly one grow-only mutation from 120 to 200 GiB before the
managed Scale Set is enabled. It is not part of routine reconciliation. Stop if
any precondition differs:

```bash
test "$(incus storage get gha-lvm size)" = 120GiB
test "$(incus storage get gha-lvm source)" = /var/lib/incus/disks/gha-lvm.img
test -z "$(incus list --project gha-fleet --format csv -c n)"
incus query /1.0 | jq -e \
  '.api_extensions | index("storage_pool_loop_resize") != null'
test "$(stat -c %s /var/lib/incus/disks/gha-lvm.img)" = 128849018880
```

Record root free bytes, LVM thin data/metadata percentages, image fingerprints,
service restart counters, legacy listener count and retained-app health. Then
use Incus' own online grow operation:

```bash
incus storage set gha-lvm size=200GiB
```

Acceptance requires all of the following before continuing:

- `incus storage get gha-lvm size` is `200GiB` and the loop file apparent size
  is exactly `214748364800` bytes;
- `lvs` reports a larger `gha-thin` data LV with healthy data and metadata
  percentages;
- all three pinned image fingerprints and aliases are unchanged;
- `gha-fleet reconcile-incus --apply` against the merged 200-GiB policy returns
  an empty `changes` array;
- the Incus fleet remains empty, all four fleet services are active, all twelve
  legacy listeners remain, and ExamplePlatform/Captcha still return HTTP 200.

The operation is grow-only and has no automated rollback or delete path. A
failed postcondition blocks Scale Set enablement and requires investigation;
do not attempt to shrink the pool.

The bridge receives default-deny ACL policy for host and external traffic.
Incus 6.0 does not accept ACL properties on a bridged NIC, so every managed VM
NIC uses port isolation to block intra-bridge traffic and additionally enables
IPv4 and MAC anti-spoofing. Only TCP 80/443 to public destinations, managed DNS
and the explicit local cache ports are allowed by the initial standard-pool
policy. Incus supplies its managed bridge's baseline DHCP/DNS service rules,
which its custom ACL cannot override. Because an accept in Incus' nftables base
chain cannot override UFW's separate drop chain, the host-firewall plan permits
only DHCP and DNS to the bridge address, the scoped registry/RustFS ports, and
forwarded TCP 80/443 from `192.0.2.0/24`. All other host input and routed traffic
retains UFW's default deny policy.

## Golden worker image

The image manifest and selected profile are a matched pair:

| Workload | Manifest | Profile | Docker |
| --- | --- | --- | --- |
| standard | `golden-image.yaml` | `nddev-linux-standard` | absent |
| integration | `golden-image-integration.yaml` | `nddev-linux-integration` | VM-local daemon |

The planner rejects either manifest on the other profile. Build and promote
the standard image first; materialize and inspect the integration profile
before applying its image transaction.

Two preconditions are easy to miss and both fail the transaction late.

The command requires the image project to contain no instances, so the warm pool
must be drained first: stop `gha-warm-pool.timer` and `garm.service`, re-prove
zero claims, then `warm-drain --apply` the exact unclaimed instance. Restore both
units on every exit path, including failure.

The command also re-runs its host pressure check immediately before the first
Incus mutation, after the drain has already happened. Entering on a single
load sample at the boundary is not enough: a legacy test burst moves one-minute
load by more than a unit within seconds, and the second gate then rejects the
build with `host-cpu-pressure` after the warm VM is already gone. Wait for a
sustained margin below the reserve, not one sample at it, and treat a
`host-cpu-pressure` rejection as backpressure to retry rather than a failure to
force. Never widen the reserve to admit a build.

Check the thin pool before starting. Two image builds add roughly 30 GiB of
transient builder and smoke volumes plus two published images. At 87 percent
pool data usage that is enough to fill it, and a full LVM thin pool is far
harder to recover than it is to avoid. Collect old golden images first as the
disk-pressure section describes, keeping the source image and every fingerprint
an active `current` or `previous` alias designates:

```bash
sudo incus image alias list --project gha-fleet --format csv
sudo lvs --noheadings -o lv_name,data_percent | grep gha-thin
```

Render the exact build plan without network access or Incus mutation:

```bash
gha-fleet reconcile-image \
  --config /etc/gha-fleet/platform.yaml \
  --manifest /etc/gha-fleet/golden-image.yaml
```

The explicit root-only form performs the complete transaction:

```bash
sudo gha-fleet reconcile-image \
  --config /etc/gha-fleet/platform.yaml \
  --manifest /etc/gha-fleet/golden-image.yaml \
  --apply
```

The equivalent gated integration transaction is:

```bash
sudo gha-fleet reconcile-image \
  --config /etc/gha-fleet/platform.yaml \
  --manifest /etc/gha-fleet/golden-image-integration.yaml \
  --profile nddev-linux-integration \
  --apply
```

Before downloading artifacts, this command applies the same Incus VM host
dependency check as foundation reconciliation. A missing distro-split QEMU
module therefore fails before image import or VM creation.

It obtains an exclusive host lock, requires the dedicated Incus project to
contain no active instances, and requires current available memory and
one-minute CPU load to preserve the configured coexistence reserve. The live
pressure check runs once before any download and again after artifact
verification, immediately before the first Incus mutation. A failed second
gate deletes the temporary verified artifacts and leaves Incus unchanged. The
pipeline authenticates Canonical's `SHA256SUMS` with the
pinned Ubuntu cloud-image key, verifies the exact source and official runner
digests, and imports the source into the project. The builder VM uses the
selected matching profile but overrides only its root volume to the
manifest-pinned 20 GiB standard or 24 GiB integration size. It upgrades and
installs the declared packages, runs
the official runner dependency installer, and caches the runner under
`/opt/cache/actions-runner/latest` without registering it.

It then bakes the manifest-pinned language toolchains described by ADR 0030.
Each archive is verified against its pinned SHA-256 before extraction, and each
installed executable must report the exact pinned version. Bun, Rust and uv land
on `PATH` under `/usr/local`; Go is seeded into the official runner's default
tool cache at `/home/runner/actions-runner/_work/_tool/go/<version>/x64` with
its `x64.complete` marker, which is where `actions/setup-go` looks before
downloading. Sanitation trims the
free filesystem extents before publishing so the image cache does not
materialize the full runtime volume merely to store zeroes.

Before publishing, the builder removes machine identity, purges the OpenSSH
server, masks its service/socket unit names, removes SSH host keys, and removes
cloud-init state, histories, logs and all runner credential/registration files.
For the integration variant, provisioning additionally installs the exact
manifest-pinned Ubuntu Docker Engine, Buildx, Compose, BusyBox and `pigz`
versions. It builds one static local action base, proves BuildKit, removes all
transient Docker objects, and stops the VM-local daemon before sealing. The
pipeline then destroys the builder, starts a new VM from the published
fingerprint, and proves runner version, fresh machine identity, public HTTPS,
blocked host/metadata routes, absent host sockets, absent VMX/SVM, absent nested
KVM, absent SSH server package, masked SSH units and no TCP/22 listener. The
standard smoke VM deliberately uses the unmodified 50-GiB profile; the
integration smoke uses its unmodified 70-GiB profile and additionally proves a
VM-local Docker socket/data root, non-root daemon access, local container-action
build and service-network behavior. Each must prove that the smaller image
filesystem expanded to its runtime size. Only a passing image is promoted. The
immutable version alias is never moved; the old current fingerprint is first
retained under the previous alias. No image is deleted by this command.

Any failed builder or smoke instance created by the transaction is deleted by
an exact generated name. A pre-existing instance causes a fail-closed stop and
is never adopted or removed. Preserve the command's JSON output, including the
independent recipe and smoke-policy fingerprints, as provenance and copy its
full image fingerprint into the deployment state before enabling a GARM Scale
Set.

## GARM/provider control plane

The reviewed non-secret deployment source is
`deploy/fleet-host/`. Render only the two documented GARM secret
tokens on the host; no other template substitution is permitted. Before first
start, record SHA-256 for the exact repository commit's provider and gateway
binaries and the upstream GARM/CLI archives. Installed files use these owners:

| Path | Owner/mode |
| --- | --- |
| `/etc/garm/config.toml` | `root:garm`, `0640` |
| `/etc/garm/provider-incus.toml` | `root:garm`, `0640` |
| `/etc/garm/queue-admission.json` | `root:garm`, `0640` |
| `/etc/garm/incus-client.key` | `root:garm`, `0640` |
| `/etc/gha-fleet/platform.yaml` | `root:garm`, `0640` |
| `/etc/gha-fleet/pki/worker-gateway.key` | `root:gha-gateway`, `0640` |
| `/var/lib/garm` | `garm:garm`, `0700` |
| `/var/lib/gha-fleet` | `garm:garm`, `0700` |

The Incus client certificate is restricted to project `gha-fleet`; GARM never
joins the Incus administrative group and never receives the Unix socket. Its
systemd unit passes a fixed trusted `PATH` to the external provider and grants
only `/dev/kvm` for the read-only host admission check.

Initialize GARM through its loopback API. The operator login URL is
`http://127.0.0.1:9997`; worker URLs are
`https://192.0.2.1:9443/api/v1/{metadata,callbacks}`. Supply the dedicated
gateway CA through GARM's controller CA-bundle field. Keep the agent URL set for
GARM schema completeness, but leave endpoint agent mode and remote shell
disabled: the worker gateway intentionally returns `404` for `/agent` and every
WebSocket route. The webhook URL is loopback-only and webhook management is
disabled because the pilot uses Scale Sets.

Acceptance before a GitHub endpoint is added:

1. `ss` shows GARM only on `127.0.0.1:9997` and the gateway only on
   `192.0.2.1:9443`;
2. the gateway certificate verifies for IP SAN `192.0.2.1` against its private
   CA;
3. worker metadata without an instance token returns `401`, while admin,
   webhook, metrics, pprof, agent and shell paths return `404` at the gateway;
4. `garm-provider-incus-nddev version` reports the version pinned in the host
   configuration's `control_plane.provider_version`, Incus SDK
   `v7.3.0` and the installed repository commit; running
   `garm-provider-incus-nddev probe --config /etc/garm/provider-incus.toml`
   as user `garm` passes its read-only project/profile/image/API probe and
   reports `cache_delivery_ready=true` with the exact non-secret pool role;
5. a second Incus foundation apply is empty and the gateway UFW/ACL rule is the
   only new bridge input permission;
6. all legacy runner listeners and both retained applications are unchanged.

The periodic warm-pool command reports an admission refusal as
`deferred=true`, includes the exact `deferral_reason` and admission decision,
and exits successfully without creating a VM. This is expected control flow
for host health, disk pressure, per-pool saturation and CPU/RAM reserve. A
configuration, inventory, host-probe, Incus or lifecycle error still exits
non-zero. Alert on the structured reason and observer metrics; do not use a
failed systemd oneshot as the backpressure signal.
The accepted v12 deployment and regression evidence is
`config/warm-backpressure-v12-rollout-audit.json`.

Unregistered warm capacity is speculative. A real cold request in another pool
may atomically reserve one or more unclaimed `warm-ready` leases for teardown.
The observer exposes the bounded hand-off as
`gha_fleet_provider_warm_preemptions`; a non-zero value is healthy only during
the delete-and-confirm interval and must converge to zero within the admission
lease. The monotonic `gha_fleet_provider_warm_preemptions_total` counter records
every durably committed victim reservation even when the transition finishes
between observer samples. Never manually clear `preempted_by` or return a
`deleting` VM to ready.
Use normal provider reconciliation so a retry, expiry or request release owns
the transition.

The accepted v14 rollout and live transition evidence is
`config/preemption-v14-rollout-audit.json`. It proves three distinct one-job
VMs, complete teardown, a durable preemption-total increment, replacement warm
capacity, synchronized RustFS diagnostics and preserved legacy/application
boundaries. It does not claim central queue-intent admission: seven bounded
`insufficient-cpu` retries observed during speculative warm preparation remain
the admission scheduler's next acceptance target.

The central scheduler is GARM's only queue-journal writer. Install
`queue-admission.json` before starting derivative `v0.2.1-nddev.11`; its unit
must point to the exact configuration, journal and sibling lock paths. On first
start GARM creates a private empty schema-1 generation. The provider and
observer may only read `/var/lib/gha-fleet/queue-intents.json`; do not grant
either component write access through another identity or manually edit the
journal.

Activation-mode changes are explicit two-state migrations, never accepted as
generic drift. With the queue empty, use `reconcile-garm` first without
`--apply` and inspect the planned
`disable_and_migrate_scale_set_activation` plus
`enable_verified_scale_set` actions. Apply direct activation with
`--activation-mode direct-jit --migrate-activation --apply --enable` only after
nddev.9 and provider nddev.19 are installed. Roll back the activation path with
the same command and `--activation-mode metadata`; the reconciler disables the
Scale Set before changing its exact `extra_specs`, verifies it while disabled,
then re-enables it. An unknown third field fails closed even when migration is
requested.

Every pilot Scale Set must retain `max_runners=1` and repository scope.
`JobAssigned` reliably carries only an opaque UUID `jobId`; GARM derives the
repository from its canonical `ForgeEntity` and must admit that UUID before
updating desired capacity. Organization and enterprise Scale Sets fail closed
until an equally early trustworthy repository signal exists. If another Scale
Set owns the global slot,
GARM leaves the assigned-job message unacknowledged and retries it once per
second. The later available-job message binds workflow/event/queue metadata and
the numeric request ID before `AcquireJobs`. Neither path may advance
`last_message_id` or call
`DeleteMessage` while deferred. A message containing more than one
available job fails closed because accepting that shape would make partial
acknowledgement ambiguous. Alert on that error and do not raise Scale Set
capacity until the central budget and message protocol are redesigned and
retested together.

An `acquiring` intent is written before the external GitHub call. After a GARM
restart, redelivery of that exact message repeats `AcquireJobs` without charging
weighted stride again. A transport failure returns the intent to `queued` and
keeps the message. A successful response that omits the ID is acknowledged but
retains the short 120-second `acquiring` uncertainty lease; an assigned/started
event promotes it, otherwise TTL cleanup releases the global slot. This is the
bounded reconciliation bridge across the API transaction and must be included
in restart and acquisition-failure canaries.

For a scheduler rollout, stop the observer, then the warm timer, then the
possibly active `gha-warm-pool.service` one-shot, and only then GARM. Stopping
the timer alone does not cancel a reconcile that fired concurrently; allowing
the later GARM stop to terminate that process can leave a transient failed
unit even though its durable retry is safe. Prove provider claims and queue
intents are both zero, retain one exact GARM/provider/observer/config rollback
set, then install all four merge-bound artifacts atomically. Starting
GARM must also start the required worker gateway through the reviewed unit
dependency. Run the provider compatibility probe only after both listeners are
ready, then start the observer; its reviewed dependency restores the warm
timer before `/healthz` is evaluated. Do not test observer health while the
timer is deliberately stopped: the timer is one of the observer's fixed
required services, so that state is correctly unhealthy. Any unknown field,
public/symlinked state, missing intent before a cold create or queue collection
failure is a hard stop. A normal queued job makes warm reconciliation return
`deferred=true` with `deferral_reason=queue-intent`, without failing its
systemd unit.

A narrow `stop garm.service` also stops `gha-fleet-gateway.service` because the
gateway requires GARM. The reverse start edge is explicit as well: starting or
restarting GARM must leave the gateway active without a separate operator
command. Starting the observer similarly restores `gha-warm-pool.timer`. Treat
either missing reverse edge as deployment drift; otherwise an apparently
healthy manager can admit a metadata-mode worker whose callback endpoint has
no listener.

The accepted merge-bound dependency rollout and the ordered stop/start proof
are recorded in `config/control-plane-lifecycle-rollout-audit.json`. The proof
observed two distinct successful warm reconciles after restart, rather than
counting older journal entries.

Create the initial standard pilot profile/Scale Set only, with
`min-idle-runners=0`, `max-runners=1`, runner setting `disable_update=true`,
provider extra spec `disable_updates=true`, the exact current image alias and provider
`nddev-incus`. These are separate controls: the first locks the official runner
binary and the second locks guest package updates. Keep the Scale Set disabled
until its GitHub App installation and repository allowlist are verified.

Do not create this Scale Set with the pinned `garm-cli`: its add command omits
the runner `disable_update` field even though the API supports it. Use the
loopback-only, idempotent API reconciler and the one-time bundle described in
[Dedicated GitHub App bootstrap](github-app-bootstrap.md):

Install the reviewed non-secret `config/garm-credential-anchor.json` as
`/etc/gha-fleet/garm-credential-anchor.json`, `root:garm`, mode `0640`, before
the first command. It is required even while the matching creation bundle is
present.

```bash
sudo gha-fleet reconcile-garm --app-bundle /run/gha-garm-app-import
sudo gha-fleet reconcile-garm --app-bundle /run/gha-garm-app-import --apply
sudo gha-fleet reconcile-garm --app-bundle /run/gha-garm-app-import --apply --enable
```

The apply and enable invocations are deliberately separate. Preserve their
redacted JSON in the deployment audit, then delete both exact bundle staging
copies before dispatching the managed canary. An omitted `--scale-set` means
exactly `nddev-linux-standard`; no other implicit selection exists.

After the Docker image, its exact provider mapping and a same-commit observer
are deployed, repeat the three gates with the explicit integration target:

```bash
sudo gha-fleet reconcile-garm --scale-set nddev-linux-integration
sudo gha-fleet reconcile-garm --scale-set nddev-linux-integration --apply
sudo gha-fleet reconcile-garm --scale-set nddev-linux-integration --apply --enable
```

These later calls validate the existing GARM credential against the public
anchor and never require the deleted App private key. A missing or drifted
credential is a hard failure, not a reason to extract GARM state or mint an
unreviewed key.

Before either apply or enable, `/healthz` must be HTTP 200 and the observer
`--version` commit must equal the provider/config rollout commit. A provider
schema change requires replacing every local schema consumer, including the
observer, before admission resumes; version skew is a failed rollout, even if
GARM itself remains active.

## One-job runner parity canary

`.github/workflows/self-hosted-canary.yml` is manual-only and accepts one of two
exact selectors. `nddev-canary` targets a manually created JIT proof runner;
`nddev-linux-standard` targets the GARM Scale Set whose unique name is its
system label. A normal repository runner cannot accidentally match either
selector. The canary must use a GitHub JIT configuration and one disposable VM;
registration state and tokens never enter the image or deployment ledger.

Run `mode=basic` first. It checks the immutable image record, absence of a host
Docker socket and cross-job sentinel, JavaScript action execution, command
files, outputs, annotations, post-actions and artifact upload. Then run
`mode=cancellation`, cancel it only after the wait step begins and verify the
runner exits plus the VM and GitHub registration disappear within the cleanup
lease. A VM that executed either mode is destroyed and is never returned to a
pool.

When the exact selector is `nddev-linux-standard`, the basic canary also
requires the consumed one-job assignment marker, proves that assignment and
readiness files were deleted before the first workflow step, validates the
installed public CA and performs signed RustFS PUT/GET requests in the trusted
namespace. Cross-trust PUT and same-prefix DELETE must both return `403`.
Delete the one exact positive-canary object with the host root identity after
the log/diagnostic leak audit; no runner identity receives delete permission.

After the standard canary, use the separate [integration runner parity
gate](integration-parity.md) for Docker actions, job containers, service
containers, protected-network denial, timeout and cancellation. Do not add
Docker behavior to the standard workflow or broaden its runner groups.

For the direct-JIT latency promotion, use
`scripts/direct-jit-latency-series.sh` from a clean merged revision. The harness
collects exactly 20 sequential basic canaries and refuses a sample unless the
observer is healthy, the queue and claim planes are empty, one warm VM is
ready, diagnostic export is synchronized, disk free space is at least 20
percent, retained services are healthy and host load is no greater than the
declared nominal threshold. It binds each GitHub run and job to the GARM opaque
job ID, durable logical-to-physical claim, exact diagnostic archive, provider
version and provider commit. A sample is durable only after one-job teardown,
distinct warm replacement, zero orphan/missing resources and RustFS diagnostic
synchronization.

Run it without parallel canaries:

```bash
GHA_REQUIRED_SAMPLES=20 GHA_MAX_LOAD_1=4 \
  scripts/direct-jit-latency-series.sh \
  config/direct-jit-nddev21-latency-audit.json
```

The reported latency is GitHub's observed interval from the workflow job's
`created_at` to `started_at`; both timestamps come from the same authoritative
GitHub API clock. Host-journal phases and the guest-local assignment marker are
recorded separately for diagnosis and are never subtracted across clocks.
Median and p95 use the documented nearest-rank method. Do not combine runs from
an older provider, workflow head, image, load boundary or failed series; start a
new evidence file instead. A p95 equal to five seconds fails the exclusive
below-five-second target.

For GARM `v0.2.1-nddev.11` and later, correlate every diagnostic sample with
structured `MESSAGE=direct JIT phase` records from
`journalctl -u garm -o json`. Accepted phases are
`acquire-jobs-started`, `acquire-jobs-completed`, `acquire-jobs-failed`,
`provider-create-started`, `provider-create-completed` and
`provider-create-failed`. Export only the journal timestamp, fixed phase,
runner/scale-set identity, numeric request IDs and `duration_ms`. Reject any
record containing JIT configuration, instance token, CA bundle, credentials or
a provider request body. ADR 0027 defines the metric semantics.

The first manual runtime proof completed on 2026-08-08: basic run
`31250944840` succeeded and cancellation run `31251086163` ended `cancelled`.
Both official `2.336.0` listeners removed `.credentials` and `.runner`, exited
zero, disappeared from the repository runner inventory and were followed by
full VM deletion after root-only diagnostic export. This proves the manual JIT
boundary; Docker actions, service containers and the GARM-managed path remain
separate gates.

Use the reviewed manifest flow in [Dedicated GitHub App
bootstrap](github-app-bootstrap.md) before adding a GARM credential or enabling
the Scale Set. The App must be selected-repository, webhook-free and exact on
permissions; do not use the desktop `gh` OAuth token as a shortcut.

## Unregistered warm-pool rollout

The warm controller is a provider subcommand, not GARM's registered idle-runner
setting. Install `gha-warm-pool.service` and `.timer` from the same repository
commit as the provider and observer. Create `/etc/garm/warm-pool.env` as
`root:garm`, mode `0640`, containing only:

```text
GARM_CONTROLLER_ID=<the exact existing GARM controller UUID>
```

Keep `warm.target_ready=0` for the first service rollout. Enable the timer and
prove repeated invocations are empty and `/healthz` remains green before
building the new immutable image. Then:

1. build and smoke the new versioned standard image without reusing an existing
   immutable alias;
2. promote its exact fingerprint through a reviewed configuration merge;
3. set only the standard target to one and deploy provider, observer, platform
   policy and image pin from that same merge;
4. prove one root-owned readiness attestation, one durable ready lease and zero
   claims before dispatch;
5. dispatch one managed job and prove the ready VM is claimed exactly once,
   receives no raw token in logs, executes once and is destroyed;
6. prove the timer creates a different replacement VM and the journal reports
   one ready lease, zero claims and zero orphan resources.

Any ambiguous injection, missing readiness evidence, ownership drift or expired
claim is a deletion condition. Never manually move a claimed instance back to
warm-ready. Rollback sets the target to zero, stops the timer, drains/deletes
only unclaimed ready VMs, and retains the previous image alias.

For a provider release transition, stop GARM and the warm timer, prove the
journal has zero claims, then dry-run and apply the exact old warm instance:

```bash
sudo -u garm /usr/local/libexec/gha-fleet/garm-provider-incus-nddev warm-drain \
  --controller-id "${GARM_CONTROLLER_ID}" \
  --instance warm-standard-EXACT
sudo -u garm /usr/local/libexec/gha-fleet/garm-provider-incus-nddev warm-drain \
  --controller-id "${GARM_CONTROLLER_ID}" \
  --instance warm-standard-EXACT \
  --apply
```

The command accepts only an owned VM with the selected warm pool's exact image,
profile, trust and security metadata, empty repository/job identity and a warm
lifecycle. Apply uses the normal diagnostic and journal-aware teardown path.
It refuses effective UID 0 so its atomic journal and diagnostic writes retain
the dedicated `garm:garm` ownership contract; use `sudo -u garm`, never plain
`sudo`.
It deliberately accepts a previous provider version so an immutable release
can be drained rather than mutated in place. Re-prove zero claims immediately
before apply; the manager must remain stopped until the old instance is gone.

## Routine admission

The manager takes a fresh host snapshot and invokes the same policy implemented
by `internal/admission`. Rejection is normal backpressure and emits one stable
reason code:

- `host-unhealthy`;
- `disk-pressure`;
- `pool-saturated`;
- `insufficient-cpu`;
- `insufficient-memory`.

The initial accounting treats one requested vCPU as one non-overcommitted CPU
unit. Only measured saturation tests may change that ratio.

An operator can evaluate a captured snapshot without changing external state:

```bash
go run ./cmd/gha-fleet admit \
  --config config/server-gha-runner-1.yaml \
  --pool nddev-linux-standard \
  --snapshot /path/to/host-snapshot.json
```

Exit code `0` means admitted, `3` means a policy rejection, and `1`/`2` means
invalid input or an operational error.

## Drain and maintenance

1. Set the selected pool to drain: no new admissions or warm replenishment.
2. Wait for assigned/running jobs to finish within their workflow timeouts.
3. Cancel only when the maintenance window requires it.
4. Export diagnostics and destroy every bound VM.
5. Remove or quarantine offline GitHub runner registrations.
6. Verify no active lease remains for the pool.
7. Apply host/image/provider change to a canary pool.
8. Run parity and fault-injection tests.
9. Promote gradually and restore warm targets.

Never restart all runner capacity simultaneously while it is the only pool with
a required label.

## Image upgrade and rollback

Each worker record contains an immutable image digest and runner version. A new
image starts with zero production weight, runs smoke and representative jobs,
then receives a bounded canary share. The previous image remains retained.

Rollback is a pool desired-digest change followed by drain and replenishment.

For an image digest transition, first build and smoke the new immutable alias
without moving the active aliases:

```bash
sudo /usr/local/bin/gha-fleet reconcile-image \
  --config /etc/gha-fleet/platform.yaml \
  --manifest /etc/gha-fleet/golden-image.yaml \
  --profile nddev-linux-standard \
  --apply \
  --stage-only
```

Record the returned fingerprint in the reviewed provider configuration. During
the bounded activation window, drain/stop GARM, rerun the command without
`--stage-only` to repeat smoke and promote `current`, atomically install the
matching provider contract, run its production-user probe, then restart GARM.
It must not require modifying a running VM or rebuilding the host.

GitHub runner auto-update is disabled only when the image pipeline has an update
SLA and verifies GitHub's current minimum-version requirement. A frozen runner
without that process is not acceptable.

## Disk pressure

At the configured free-disk threshold, admission and warm replenishment stop.
Running jobs are not killed automatically. Garbage collection proceeds in this
order, always respecting active leases:

1. expired disposable VM snapshots and orphan disks;
2. expired diagnostic bundles beyond retention;
3. unreferenced OCI/BuildKit layers;
4. expired isolated/untrusted RustFS cache objects;
5. expired trusted cache objects according to quota and LRU policy;
6. old golden images except the active and rollback digests.

If free space does not recover, the pool stays closed and alerts identify the
owning storage plane. GC never crosses into ExamplePlatform/Captcha volumes.

## External outages

- GitHub API/control outage: stop speculative creates, retain bounded warm
  capacity, back off with jitter and reconcile when service returns.
- Incus unavailable: close admission, preserve journal ownership, do not remove
  runner records until VM state is known.
- RustFS unavailable: use an explicitly configured cache bypass for non-release
  jobs; never merge trust namespaces as a workaround.
- OCI registry unavailable: allow direct upstream pull only if the pool's egress
  and supply-chain policy permits it.
- Journal unavailable: fail closed; no admission or external lifecycle mutation.
- Host reboot: reconcile journal, Incus and GitHub before replenishing warm VMs.

## Diagnostics and audit

Before teardown, export runner diagnostics, console/cloud-init logs and the last
lifecycle reason. Redact known credentials and restrict retention/access. Store
the correlation chain:

```text
GitHub job attempt -> job key -> runner ID -> VM ID -> image digest -> teardown
```

Required metrics include queue/admission/boot/online/assignment/teardown latency,
warm depth, reason codes, image/runner versions, cache hit and transferred bytes,
host pressure, GitHub API limits, and orphan resource counts.

The first host/provider observer is deliberately loopback-only:

```bash
curl --fail --silent http://127.0.0.1:9464/healthz
curl --fail --silent http://127.0.0.1:9464/metrics
curl --fail --silent http://127.0.0.1:9464/snapshot
```

`/healthz` fails when an essential sample is incomplete or older than 45
seconds, a required fleet service is inactive, or exact Incus/journal inventory
does not reconcile. Snapshot schema v3 keeps health green only during a
90-second `convergence-grace` when the independently sampled local diagnostic
spool and a successful, non-pending exporter status differ coherently. Metrics
expose the signed bundle/byte delta and remaining grace; exporter failures,
incoherent divergence and grace expiry fail closed. Workers and external
networks cannot reach this listener.
The future OpenTelemetry collector scrapes this local contract; it does not
receive GARM admin, Incus or cache credentials.

ADR 0008 defines the first implemented boundary: the provider captures a
strictly allowlisted, redacted, bounded bundle before stopping an owned VM. The
private local spool is limited to seven days and 1 GiB. Diagnostic collection
has a 15-second budget and cannot prevent teardown; RustFS upload remains gated
on production IAM/TLS/retention acceptance.

Repository-bound bundles and never-assigned warm-VM bundles have separate
RustFS object scopes. An unassigned bundle is accepted only when repository is
empty and pool ID is exactly `warm/<pool-name>`; every mixed or ambiguous
identity fails closed. See ADR 0017. When a failed exporter makes systemd
degraded, stop warm replenishment, repair and durably confirm the pending
bundle, then clear only the exporter failed marker before reopening admission.

## Branch protection

The protection the default branch must carry is declared in
`.github/branch-protection.yaml` and applied with `scripts/branch-protection.sh`.
The declaration is reviewed here; the setting lives in GitHub, so the script is
the only thing that crosses between them. It needs `gh`, `jq` and `yq`.

```console
$ scripts/branch-protection.sh --check   # report drift, exit 1 if any
$ scripts/branch-protection.sh --apply   # send the declaration, then re-read
```

`--check` reads the live object, projects it onto the payload shape and diffs.
It never repairs: a protection change is a decision, and a script that quietly
restored one would hide the fact that somebody made it.

The required context is the aggregate `Gate`, never a leaf proof. `Gate` runs on
`always()` and reads every other job's result, so requiring it makes all of them
blocking; requiring one proof directly leaves the rest advisory, which is how a
failing `GARM derivative` could merge while `Go verify` was the required context.
`internal/repositorycontract/branch_protection_test.go` holds that invariant
against `ci.yml` on every run, including the case where a job is added.

Moving the required context is two steps, not one. Add the new context alongside
the old one, observe it reporting on both a path-hit and a path-miss pull request
— the `garm-derivative` job is path-filtered, so a documentation change skips it
and the gate has to pass anyway — and only then remove the old context. Applying
the declaration in one step would leave the branch waiting on a context that has
never been seen to report.

## Emergency stop

Emergency containment closes admission first, then drains or cancels jobs by
trust priority, revokes the manager App credential if compromise is suspected,
blocks worker egress, exports bounded diagnostics and destroys workers. Production
tenant actions remain separate and require their own incident authorization.

## Retiring the shared example-legacy label

Four organization-scoped runner listeners on the host that used to carry the
fleet serve
roughly twenty-five workflows across ten repositories, and ten repositories
route GitHub Code Quality onto the same label. Both kinds must move before the
listeners can go. The order below is the one that never leaves a consumer with
no runner.

Do not begin any step until the previous one is proven. A half-migrated label
is worse than either end state, because a workflow that selects it will queue
forever rather than fail.

1. **Create the organization entity, disabled.** The fleet serves only its own
   repository until this exists.

   ```bash
   gha-fleet reconcile-garm --entity-kind organization --apply
   ```

   This creates the entity and one disabled scale set. Inspect both before
   enabling anything; the reconciler refuses to create an enabled scale set for
   exactly this reason. Repeat with `--scale-set nddev-linux-integration` for
   the second pool.

2. **Decide the runner-group boundary.** An organization entity serves every
   repository the account holds, including ones created later. GARM expresses
   no repository filter for it. If the fleet should serve a fixed list instead,
   restrict the GitHub runner group the scale sets were created in before
   enabling them, not after.

3. **Prove capacity for the load being moved.** Each runner host is eight cores
   and 16 GiB with an Incus project capped at three instances, six CPU units
   and 12 GiB. One standard or integration worker fills a host; three `fast`
   workers fit every one of those caps exactly, and `max_running: 1` is what
   holds that pool to one on runner-1 and runner-2 while runner-3 already
   carries three. Two concurrent Code Quality analyses were measured at roughly
   6.5 to 7 GiB resident each, which no pool admits beside a running job. Raise
   capacity deliberately and measure, or keep those analyses hosted.

4. **Enable one scale set and canary one repository.** Move a single
   non-critical repository's workflow onto the pool name, watch a real job
   through to teardown, and require the same postconditions any canary does:
   no orphan instance, lease or registration, a diagnostic bundle exported, and
   the observer healthy.

5. **Migrate the workflow consumers, one repository at a time.** Each is a pull
   request in its own repository changing `runs-on` from the shared label to
   the pool name. `config/code-quality-routing.yaml` and the label search that
   produced it are the inventory. Do not batch them; a failure isolated to one
   repository is diagnosable, a fleet-wide one is not.

6. **Migrate the analysis consumers.** Code Quality routing is not in any
   workflow file and is read and written through its own API:

   ```bash
   gh api repos/OWNER/NAME/code-quality/setup
   gh api -X PATCH repos/OWNER/NAME/code-quality/setup -f runner_type=standard
   ```

   Read back after every write. A `PATCH` on adjacent GitHub configuration
   surfaces replaces rather than merges, so verify rather than assume.

7. **Drain, then remove.** Only when no consumer selects the label: stop each
   listener, confirm no job is assigned to it, deregister it from the
   organization, disable the unit, and remove its directory. Keep the units
   installed but disabled through one full CI cycle before deleting anything,
   so a rollback is a `systemctl enable --now` rather than a reinstall.

8. **Update what claimed the old shape.** `docs/STATUS.md`, the host reserve
   comment in `config/server-gha-runner-1.yaml` and the integration gate's
   retained-listener assertion all describe the label's existence. Records of
   past runs keep what they observed.

## Re-pointing a fleet host at a transferred repository

GARM keys its entities by `owner/name` and refuses to drop a scale set whose
GitHub side it cannot reach. A repository transfer therefore leaves records that
can be neither renamed nor deleted in place, and the order below is the one that
works. It was established migrating the fleet to `NDDev-OpenNetwork` on 2026-08-10.

1. **Free the names on GitHub first.** A scale set deleted only in GARM stays on
   GitHub, and the next create fails with `RunnerScaleSetExistsException`. The
   UI has no delete for scale sets, so use the same Actions broker path GARM
   uses: mint an installation token, exchange it at
   `POST /actions/runner-registration` with `runner_event: register`, and call
   `DELETE {url}/_apis/runtime/runnerscalesets/{id}` against the **URL returned
   in that response** — it carries a tenant path, and the bare broker host
   answers `Invalid Claims`.
2. **Rebuild GARM's state rather than editing it.** Its database holds only the
   credential, the repository entity and the scale set, all of which
   `reconcile-garm` recreates from the reviewed contract. Stop the control
   plane, remove `garm.db`, start GARM, re-run first-run, then reconcile.
3. **Restore what the database carried.** A rebuild mints a new controller id,
   so re-install the gateway CA in controller metadata and rewrite
   `GARM_CONTROLLER_ID` in `/etc/garm/warm-pool.env`.
4. **Drain every warm VM.** They are stamped with the previous controller id and
   the provider correctly refuses to adopt them. Remove
   `/var/lib/gha-fleet/provider-journal.json` too: its leases name the old
   controller, and the warm reconciler fails closed on them.
5. **Clear the diagnostics spool.** Bundles captured before the migration record
   the former repository and pool id, and the exporter fails closed on them —
   which puts systemd in `degraded`, which the host probe reports as
   `system-unhealthy`, which makes admission defer every warm VM. Archive them
   out of the spool rather than exporting them under a namespace they do not
   belong to.
6. **Re-dispatch, do not wait.** A job assigned before the rebuild has no
   pre-`AcquireJobs` queue intent any more, and the provider refuses to claim a
   warm VM without one. Cancel it and dispatch again.

Two host-level failure modes worth recognising anywhere: any failed unit makes
`systemctl is-system-running` report `degraded`, which admission treats as
`system-unhealthy` — including transient `systemd-run` units left behind by
diagnosis. And a retry storm is self-sustaining: rejected creates raise the load
past the reserve, which rejects the next create.
