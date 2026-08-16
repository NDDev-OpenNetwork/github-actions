# Example legacy host baseline

Status: read-only observation captured at `2026-08-08T02:08:01Z` with the
repository-owned `gha-fleet preflight` command. The snapshot contains no
process environment, runner registration, container environment or credential
material.

## Observed platform

| Property | Observation |
| --- | --- |
| Provider boundary | DigitalOcean KVM guest |
| OS | Ubuntu 24.04.4 LTS |
| Running kernel | `6.8.0-136-generic` |
| CPU | 8 exposed cores, one socket, one thread per core, one NUMA node |
| Memory | 32,095 MiB total; 8,191 MiB swap |
| KVM | `/dev/kvm` present and accessible; nested KVM enabled |
| Root storage | one ext4 virtual disk, 633,764 MiB total |
| Root free space | 73 percent; 60 percent free inodes |
| Storage signal | provider reports the virtual disk as rotational |
| Docker | 29.7.1 |
| Incus | Ubuntu Incus 6.0.0 LTS; bounded foundation initialized after capture |
| systemd | `running` |
| Legacy runners | 12 listeners retained; 6 workers active at capture |

This is not the physical NVMe on-prem topology assumed by the original design
review. It has no independent CI disk or physical failure domain. The
architecture remains valid, but density, storage latency and availability must
be evaluated against this actual provider boundary.

## Cold-pilot decision

For `nddev-linux-fast`, repository policy requires:

- six total CPU units: four reserved for the host plus two for the VM;
- 22,528 MiB total memory: 16,384 MiB reserved plus 6,144 MiB for the VM;
- at least 20 percent free blocks and inodes.

The host satisfies those capacity constraints. The preflight correctly failed
closed for two operational blockers:

1. a reboot is pending for `linux-image-6.8.0-137-generic`;
2. Incus was not installed or initialized at capture. The pinned Ubuntu Incus
   6.0.0 LTS package, QEMU and LVM tooling were subsequently installed without
   restarting Docker tenants or legacy runner listeners.

It also reports two benchmark warnings: nested virtualization and rotational
storage. A single cold VM remains the maximum pilot concurrency until the
controlled saturation benchmark proves otherwise.

The root ext4 filesystem does not have project quotas enabled. The pilot uses a
bounded 120-GiB loop-backed LVM thin pool with a 100-GiB project ceiling instead
of unbounded `dir` storage. This is a transitional pilot choice; production
requires a dedicated measured block device as recorded in ADR 0005.

## Reconciled foundation

At `2026-08-08T02:44:51Z`, commit
`37668cf2ca2c334c1e3b513cbd6156152348242d` was built as a static Linux AMD64
binary with SHA-256
`86b710c38199f0086b8923c46234cb509ae384fea9a63c106d20762d24374b78`.
It reconciled the following host state:

| Resource | Applied state |
| --- | --- |
| Incus API | `127.0.0.1:8443` only; local Unix socket remains the reconciler boundary |
| Storage | `gha-lvm`, 120-GiB loop-backed LVM thin pool, thin LV `gha-thin` |
| Network | managed bridge `gha0`, `192.0.2.1/24`, IPv4 NAT, IPv6 disabled |
| ACL | `gha-public-egress`, default deny with explicit public HTTPS/DNS/cache rules |
| Project | `gha-fleet`, restricted, 2 VMs / 4 CPUs / 12,288 MiB / 100 GiB ceilings |
| Profile | only `nddev-linux-standard`, 4 vCPU / 10,240 MiB / 50 GiB |
| Instances | zero |

The mandatory second apply returned an empty `changes` array. LVM data usage
was zero percent, root disk remained 72 percent free, all twelve legacy
listeners remained active, every retained Docker container stayed up/healthy,
and `example-platform.nddev.asia` plus `captcha.nddev.asia` both returned HTTP 200.

The host was then drained without canceling any legacy job and rebooted into
`6.8.0-137-generic`. All twelve enabled listeners, their active resource slice,
Incus, every tenant container and both public endpoints recovered automatically
with zero failed systemd units. A post-reboot apply from merged commit
`c92e7dc11a1fc38dd19f9105925413f3662a9b90` again returned no changes; its
Linux AMD64 binary SHA-256 was
`7784e054f436d04d370a6f38438d74e0f49797f4a9b59b1b0124b87266a25ccd`.

The post-reboot preflight at `2026-08-08T02:58:25Z` returned exit code zero and
`pilot_ready: true`. Rotational storage and nested virtualization remain
benchmark warnings, not readiness blockers.

## Storage headroom amendment

At `2026-08-08T10:42:09Z`, after current/source/rollback images and two manual
JIT proofs were retained, the official Incus grow operation expanded `gha-lvm`
from 120 to 200 GiB. No instance existed during the mutation. The thin LV grew
from `128593166336` to `214324740096` bytes; data usage fell from 58.15 to
34.89 percent and metadata usage from 26.51 to 18.07 percent. Root usage stayed
at 46 percent.

All three image fingerprints and every alias were unchanged. Merged policy
commit `d2ab1b127ee96bf0c6edcff598e7e81b0243f74d` reconciled with an empty change
set and cold preflight remained ready. All four fleet services, twelve legacy
listeners, ExamplePlatform and Captcha passed their post-mutation checks. ADR 0005
records the exact provenance and inventory digests.

## Golden-image runtime evidence

The signed image pipeline from commit `85eb31d` was built for Linux AMD64 with
binary SHA-256
`b174211f1717ca6854f2050e751105383c787396e02560ca9dfd997f60d98886`.
The applied platform and image files had SHA-256 values
`90cc91f042354a24e0920bd146d2299335d366530cb0c315a343aaf3e640ea7e`
and `388bc9b1cad01822d47f27e33d0aa7fd870b2d4a8f234e15bc7c603ac23af3a3`.

The accepted build produced:

| Evidence | Value |
| --- | --- |
| manifest fingerprint | `sha256:8835a0a379d2b009ae131d8339ecc0a854ea25300d5eab2e1e87d3c2f4051ecf` |
| build recipe fingerprint | `sha256:16404b434321144d25e1a0dba57050a052add99200fd9856fe760c10de7b0afb` |
| smoke policy fingerprint | `sha256:05b1845142a95960033f7651f97945a7e9843de536c45ec3e7effdb64df1c002` |
| Canonical source fingerprint | `e934afab20c76870416bd1a5203a084d3a9b7c5a6699256f0afedb6ad6dc7759` |
| current image fingerprint | `62d9aa2415f97d666092b20ca9f0cc2a52d50c6af4e4b965ec009368da67611c` |
| retained previous fingerprint | `216046456da8c0658c01fca7872fb7bdf76420d99a55e1e41b424e6205beaf95` |
| package manifest SHA-256 | `6e16e7606f4bbed6db53344dd08f9420274cfa6a2b120b7de64846fc94c33938` |

The full signed build, sanitation, publish, independent smoke and promotion
took `525.85` seconds. The mandatory second apply took `40.40` seconds, returned
`built: false`, resolved the same fingerprints and ran a fresh smoke VM with a
different machine ID.

Both accepted smokes proved official runner `2.336.0`, public HTTPS, blocked
host and metadata routes, absent runner registration, absent host sockets,
absent VMX/SVM and absent `/dev/kvm`. The 16-GiB immutable image expanded under
the real standard profile to a 53,687,091,200-byte block device and a
50,884,108,288-byte ext4 filesystem.

An earlier 50-GiB build exposed expensive LVM image-cache materialization. The
pipeline now trims a bounded 16-GiB builder before publishing. The accepted
image's cached block volume is 16 GiB instead of 50 GiB; the failed,
never-promoted intermediate fingerprint was deleted exactly without backup.
The thin pool ended at 58.15 percent data usage while retaining the old verified
50-GiB image as rollback. The fleet project ended with zero instances,
ExamplePlatform/Captcha returned HTTP 200 throughout, and all legacy listeners remained
enabled.

## Loopback observer runtime evidence

At `2026-08-08T11:35:23Z`, merged commit
`b3dfb93033a4095d40b643a0fbc49abaa40c06f1` was deployed without restarting
GARM. The installed Linux AMD64 observer and provider SHA-256 values are:

| Artifact | SHA-256 |
| --- | --- |
| `gha-fleet-observer` | `c23ae9debb45b57fcb07b0fb1fdc8de73b5efaac4abb23f8dd786463aabb6deb` |
| `garm-provider-incus-nddev` | `cf0e7a0b1acd06a10ff296d73af12c55e421e5069b366a192a192111a2759072` |
| `gha-fleet-observer.service` | `f7e2f413f5c428bf57808287db4d1a74e6c3a010f4cd07a0db00c036aed982c0` |

The observer bound only `127.0.0.1:9464`. Its fresh snapshot reported all four
policy pools pilot-ready, accessible nested KVM, zero journal leases, zero
visible/orphan/missing Incus instances, zero diagnostic bundles, and every
required fleet service active. Prometheus output reported both observer and
platform health as `1`; it contained only bounded policy/service labels and no
runner or instance identity labels.

The systemd security review reported exposure `1.7 OK`. A controlled observer
restart returned fresh healthy status with `NRestarts=0`; GARM also remained at
`NRestarts=0`. The provider compatibility probe resolved the exact current
image fingerprint with an empty instance inventory. The four-file staging
directory and temporary provider rollback copy were then removed exactly.
Twelve legacy listeners remained active, and both ExamplePlatform and Captcha returned
HTTP 200 after deployment and restart.

## RustFS RC.1 live-canary evidence

At `2026-08-08T12:19:07Z`, the fleet-cache service reported RustFS RC.1 commit
`778f1dfa2155cbbc61ad54e6896de9e29d2c4d8d` and binary SHA-256
`eb63af6574150a62a4509461f16b178976e67485ce6beacf41e6b67944d41db0`.
The installed unit SHA-256 remained
`1119d775cbb3f07e908bc3deaacf3d14a86912840ab184090be2a52643e83480`
and its systemd exposure score remained `2.8 OK`.

The first live attempt deliberately rolled back to exact beta.12 when a
60-second quota readiness window expired before the first authoritative usage
snapshot. Beta.12 immediately passed the retained-data CRUD/multipart probe.
After the scanner produced the v2 snapshot and the gate gained a bounded
180-second convergence window, the second transaction passed migration-time
and post-crash hard-quota enforcement, CRUD, multipart, orderly restart,
main-process `SIGKILL` recovery, IAM isolation/revocation, TLS protocol checks
and journal credential scanning. All temporary audit buckets converged to
deletion.

The final observer snapshot was fresh and healthy with zero visible, orphan or
missing Incus instances. GARM stayed active at `NRestarts=0`; the single RustFS
automatic restart was the intentional crash probe. Root filesystem free space
was 55 percent, twelve legacy listeners remained active, and ExamplePlatform plus
Captcha both returned HTTP 200. The exact local/remote artifact stages and
temporary beta.12 rollback binary were removed without backup after every gate
passed. RC.1 remains a pre-release canary and is not production-promoted.

## SSH-free b4 golden-image evidence

At `2026-08-08T13:18Z`, controller commit
`397290a51b57600faa42573ccb3978aaeb311771` and Linux AMD64 binary SHA-256
`7c5b1436d9ae5fbdc19450b115ca47ec96bfc63a1e8e5f8a971dbb9b8e5b32a0`
promoted the SSH-free b4 image. The applied golden-image file had SHA-256
`8715aeb3d1f29fc1f3009d8a6edfad9c6171e6be144d911acc6511649e2dd1d7`.

| Evidence | Value |
| --- | --- |
| manifest fingerprint | `sha256:644b0f003a578277ce303677fce84d745b64cf72846cd1d53355debff51722d2` |
| build recipe fingerprint | `sha256:348f4de634b1e4151342f62e5cd96ee3c5f4d6080c8ca5eff1f7bccb97996130` |
| smoke policy fingerprint | `sha256:24af94a0e51bd19b3b54a6e6233ad39e500469369ee937fd6f66a40479af077b` |
| Canonical source fingerprint | `e934afab20c76870416bd1a5203a084d3a9b7c5a6699256f0afedb6ad6dc7759` |
| current b4 image fingerprint | `139dc137b35dd1f686a3b155b502c0da32cef51cd82ffd7466f07ce85b95acc3` |
| retained b3 rollback fingerprint | `62d9aa2415f97d666092b20ca9f0cc2a52d50c6af4e4b965ec009368da67611c` |
| package manifest SHA-256 | `10e1275e19482490da4803ddf30c167dcf2007d56dc43b5c74bcf5bc920eb9d2` |

Three earlier b4 attempts failed closed during sanitation and deleted their
builders without publishing or changing `current`. The first showed that a
runner dependency installer can reactivate SSH after an early purge. The second
moved the final stop after all installers but still purged too early. The third
used installer, purge, grouped `disable --now`, then mask; a disposable-VM
reproduction proved that the grouped command exits on the first purged unit
file before stopping the still-loaded `ssh.socket`. The accepted recipe stops
and disables each canonical and compatibility unit independently after purge,
then masks every unit name. A separate disposable b3 VM proved this exact loop
before the fourth production attempt.

The first fourth-attempt admission also failed closed without creating a VM
when four legacy jobs raised load1 to `6.15`, above the coexistence reserve of
`4`. The unchanged policy admitted the build after load1 naturally fell to
`3.63`; no legacy job was canceled. The accepted smoke reported official runner
`2.336.0`, absent OpenSSH server package, masked and inactive SSH units, no TCP
22 listener, absent registration state and forbidden devices, blocked host and
metadata routes, no nested CPU flags, public egress, and a 50-GiB runtime disk.

The mandatory second apply returned `built: false`, resolved the same image and
package fingerprints, and repeated every runtime assertion in a fresh VM. Its
machine ID `aca67b1a80084522abf53e9b968b35cb` differed from the first accepted
smoke ID `609647dee2c2449e882411473d4684f0`. Both smoke VMs were deleted. The
repository provider contract now pins the full b4 fingerprint; the previous b3
fingerprint remains on the `previous` alias for rollback.

At `2026-08-08T13:26:19Z`, merged contract commit
`b387d5e49ed108fe3440c5ec13f780fc5db544b2` was deployed transactionally.
The provider binary SHA-256 was
`0f18aeacb54d34f38575a9dac1416cd567e388de0058c4cd74c68f5051ea5582`
and the provider config SHA-256 was
`f56d541b4bb37aff5ffb97c3124e4a7050124112c8fbbb2d9c7c8d6fe3dd00a4`.
The candidate and live probes both resolved the full b4 fingerprint with zero
visible instances. GARM had zero credentials, repositories and pools; its one
controlled restart changed PID `832056` to `1263484`, returned active/running,
kept `NRestarts=0` and emitted no warning-or-higher journal entries.

The observer had correctly reported the short rollout window unhealthy while
the old b3 provider pin disagreed with the newly promoted b4 alias. Its first
post-deployment sample at `2026-08-08T13:26:37Z` returned healthy and fresh with
no collection errors, instances, orphans, missing resources or leases, and all
four policy pools ready. Diagnostics remained empty. The 200-GiB thin pool was
at 42.93 percent data and 20.31 percent metadata usage; the root filesystem was
51 percent used. All five replacement services were active, all twelve legacy
listeners remained present, and `example-platform.nddev.asia` plus
`captcha.nddev.asia` both returned HTTP 200.

After this evidence was merged, the exact rollout stage
`/run/gha-image-b4-deploy.7diq00ev` and the two-generation-old image
`216046456da8c0658c01fca7872fb7bdf76420d99a55e1e41b424e6205beaf95`
with its sole `nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801` alias were
deleted without backup. Read-only prechecks proved zero consumers and exact
b4/b3 current/previous targets. The post-GC inventory contained only the
Canonical source, current b4 and rollback b3 images; it had zero instances and
the observer remained fresh and healthy. Thin-pool data and metadata usage fell
to 17.86 and 13.30 percent respectively. Root usage remained 51 percent, all
five replacement services stayed active, the exact legacy listener count stayed
at twelve, and both retained public applications still returned HTTP 200.

## GitHub App and managed Scale Set cold-pilot evidence

At `2026-08-08T14:47Z`, the private GitHub App
`nddev-gha-fleet` (App ID `100001`, installation ID `200001`, owned by and installed on the `NDDev-OpenNetwork` organization) is
installed on exactly `example-user/github-actions`. Its verified permission set
was `administration=write` plus implicit `metadata=read`; webhook delivery and
all webhook events were disabled. The one-time private-key import bundles were
deleted locally and remotely after GARM encrypted the key in its database.

GARM repository `204a82c0-0a5a-401b-bdd2-e702fcb406aa` remained bound to the
personal credential, `github.com`, `agent_mode=false`, no traditional pools and
the explicit `roundrobin` balancer. Scale Set ID `1`, GitHub Scale Set ID `1`,
was enabled with this exact boundary:

| Field | Value |
| --- | --- |
| name | `nddev-linux-standard` |
| provider / image / flavor | `nddev-incus` / `nddev-ubuntu-24.04-amd64-current` / `nddev-linux-standard` |
| capacity | `max_runners=1`, `min_idle_runners=0` |
| update / shell | `disable_update=true`, `enable_shell=false` |
| runner group | `Default` |
| provider extra specs | `{"disable_updates":true}` |

The final provider was built from merge commit
`a03701b5dcc196737d9b384e7b17e427769ad121`; its Linux AMD64 SHA-256 was
`6396b2a01658037f22afe5858cccc1c09486bc671a71393ee6238dd15d6ff8fb`.
Candidate and live probes both resolved b4 fingerprint
`139dc137b35dd1f686a3b155b502c0da32cef51cd82ffd7466f07ce85b95acc3`
with zero visible instances.

Two fail-closed canaries preceded acceptance:

- run [`31261577100`](https://github.com/example-user/github-actions/actions/runs/31261577100)
  proved that pinned GARM `v0.2.1` injects its Linux install
  wrapper at provider invocation time; the old provider rejected it before any
  Incus mutation;
- run [`31262282506`](https://github.com/example-user/github-actions/actions/runs/31262282506)
  passed the exact wrapper contract and proved that this
  host's Incus 6.0 API rejects a duplicated per-instance
  `security.nesting=false` key before VM creation. The managed profile retains
  that value, while the instance-owned fixed `raw.qemu` policy removes VMX/SVM.

Both runs were cancelled with the Scale Set disabled, zero created VM and full
GARM cleanup before changes were made. Neither boundary was relaxed: only the
exact GARM wrapper is accepted and then discarded, while profile-expanded
security state remains mandatory.

The accepted runtime evidence was:

| Run | Result | Queue to job start | Job duration | Purpose |
| --- | --- | ---: | ---: | --- |
| [`31262519705`](https://github.com/example-user/github-actions/actions/runs/31262519705) | success | 64 s | 13 s | first official-runner full-VM parity canary |
| [`31262659143`](https://github.com/example-user/github-actions/actions/runs/31262659143) | success | 61 s | 11 s | second fresh VM and cross-job isolation |
| [`31262735341`](https://github.com/example-user/github-actions/actions/runs/31262735341) | cancelled as requested | 65 s | 34 s | cancellation during the active five-minute step |

Both successful jobs validated the immutable image manifest, official runner
`2.336.0`, absent OpenSSH package/listener, absent host sockets and `/dev/kvm`,
absent registration state in the cached runner, checkout, a composite action,
command files, annotations, outputs, post-actions and GitHub artifact upload.
Their artifacts had different machine IDs but identical b4 manifest and recipe
fingerprints. The sentinel written by the first job was absent in the second,
proving that an executed VM was destroyed rather than returned to capacity.

The cancellation job reached the intended long-running step before the cancel
request. GitHub completed its post-action and marked the run cancelled at
`2026-08-08T14:46:57Z`; by the same observation the provider had exported
diagnostics and GARM/Incus had converged to zero runners and zero instances.

Each executed VM produced one private mode-`0600` diagnostics bundle in the
mode-`0700` GARM spool:

| Lifecycle | Bundle SHA-256 |
| --- | --- |
| first success | `fe37fdcdd49f199ecb7f24c9fec10daea5183c24287ee844b1c3d0f530929cce` |
| second success | `365c8d5bc10824ac807c019e8e8ce53cf9cec87a00a4c78e6ca7eb9fcdb2b8d1` |
| cancellation | `a8daac719f92fd02abbda155dcfb4e4ae4f04c52750b07153ea6e7ee935fdc89` |

The final observer sample was fresh and healthy with collection errors `0`,
journal leases `0`, visible instances `0`, orphan instances `0`, missing
instances `0` and diagnostic bundles `3`. All required services were up. The
twelve legacy listeners remained running throughout, and ExamplePlatform plus Captcha
continued to return HTTP 200.

The cold queue-to-start values of 61-65 seconds do **not** meet the initial
sub-30-second cold target. This is now a measured optimization backlog item,
not an estimated performance claim. No idle GitHub-registered runner is enabled
as a shortcut; the target architecture still requires an unregistered warm VM
pool before warm-start acceptance can be measured.

## RustFS diagnostic-export canary evidence

PR [#43](https://github.com/example-user/github-actions/pull/43) merged as
`0549b3a21d51590789d7f65e0e84a2503afd850d` after GitHub-hosted run
[`31265597055`](https://github.com/example-user/github-actions/actions/runs/31265597055)
completed the full `make verify` gate. The exact Linux AMD64 deployment digests
were:

| Component | SHA-256 |
| --- | --- |
| Incus provider | `b23eb68f954675f6188b1cc93aa4954944fb20af30b2395e2294817cce9b5700` |
| fleet observer | `59cf61a21ef1a05672066ff11657203f6ca1ec7f913d5f8f3dc20eb2eaa44511` |
| diagnostic exporter | `f8b4003288057a06c4c492f7ba6bc444e022cc55f5c9369d04fd548b8e34b0cc` |

The installed provider reported that exact merge commit and passed its
read-only Incus 6.0 compatibility probe with b4 image fingerprint
`139dc137b35dd1f686a3b155b502c0da32cef51cd82ffd7466f07ce85b95acc3`
and zero visible instances. The observer reported the same merge commit after
restart.

The dedicated RustFS canary bootstrap replaced the exporter's direct policy
mapping with exactly `s3:GetObject` and `s3:PutObject` below
`gha-diagnostics-canary/diagnostics/v1/`. Runtime probes accepted PUT, GET and
HEAD, and returned `403` for object listing, bucket location, DELETE,
cross-prefix, cross-bucket and anonymous access. The bucket round-tripped a
1 GiB hard quota and a seven-day prefix lifecycle rule. This remains canary
evidence for pinned RustFS `1.0.0-rc.1`, not production promotion.

At `2026-08-08T16:04:17Z`, the first one-shot exporter run independently
verified and durably confirmed all four retained bundles: `4/4`, zero pending,
and `146500/146500` source/exported bytes. The SHA-256 of their sorted
`sha256sum` ledger was
`c1d6a9e5f0a239c89ebfeefcf3847f77111d9d47becf541f19fd97a62ebd4f8a`.
The fourth bundle added after the VM-console compatibility fix had SHA-256
`09cb8c4fad1cabe5cd0b7c3c29ccd44b71aef4fe471c7fd00b4f1e6707215907`;
the three earlier bundle digests remained unchanged.

The systemd isolation audit proved that the exporter could read the bind-mounted
spool but could not write it, could not see the original `/etc`, GARM, fleet or
cache state paths, and could not reach the loopback observer. The actual service
could reach only the allowed RustFS bridge IP and wrote a mode-`0640`
`root:garm` secret-free journal. Config, S3 keys and the private CA were visible
only through the service credential mount.

A controlled endpoint fault then replaced only the non-secret canary endpoint
with an unused port. The exporter recorded stable reason `remote-head`, four
pending bundles and one consecutive failure; the observer changed from HTTP
200 to 503. ExamplePlatform and Captcha remained HTTP 200. Restoring the exact merged
config rebuilt the derived journal from remote HEAD records, returned the
exporter to `4/4` and the observer to HTTP 200. The ordered local bundle digest
ledger was identical before failure and after recovery. The timer was then
enabled and the sample at `2026-08-08T16:12:12Z` reported zero failures and
zero pending bundles.

Final reconciliation reported required services active, Incus visible/orphan
instances `0/0`, journal missing instances `0`, no active `Runner.Worker`, and
ExamplePlatform plus Captcha HTTP 200. The exact root-only deployment staging directory
was deleted after installed binary, config, unit and tmpfiles digests matched
the merged repository.

## Multi-image and staged standard b5 evidence

PR [#51](https://github.com/example-user/github-actions/pull/51) merged as
`6ebc6ef30c47554948904c73f6eb8fe0ec092e99` after GitHub-hosted run
[`31269940860`](https://github.com/example-user/github-actions/actions/runs/31269940860)
passed. It introduced one controller/journal ownership domain with independently
pinned standard and integration images. A production-user deployment probe
correctly rejected standard b4 because that older image predated explicit
`user.nddev.image.variant` metadata. No VM or journal lease existed; the prior
provider/config/platform contract was restored and GARM plus its gateway were
returned to active state before further work.

PR [#52](https://github.com/example-user/github-actions/pull/52) merged as
`6a14bb728397502cff7116dcbd19e94d8c5a364f` after GitHub-hosted run
[`31270347065`](https://github.com/example-user/github-actions/actions/runs/31270347065)
passed. It made image variant metadata mandatory for every image and added a
stage-only rollout mode. Standard b5 was then built from manifest fingerprint
`sha256:0905246dd81e48c787294eefc1b77132c4d34751ecfe0759314c16b1703d123e`
and recipe fingerprint
`sha256:8c0f702aeb2df4c07c11e65e6a47d619747f62246b5bfe921d1312034919b38a`.

The stage-only live result was `built:true`, `promoted:false` with immutable
fingerprint
`2c30f163eaf4ba6df14148359c3030a1aeca5627e1677637fc98fef9f3cc0b18`
and package-manifest SHA-256
`10e1275e19482490da4803ddf30c167dcf2007d56dc43b5c74bcf5bc920eb9d2`.
Smoke proved runner `2.336.0`, a 50-GiB root device, expanded filesystem,
public GitHub egress, blocked host and metadata routes, absent nested CPU flags,
registration state and SSH. The smoke VM was destroyed. `current` remained b4,
`previous` remained unchanged, and the b5 image property reported explicit
variant `standard`.

PR [#53](https://github.com/example-user/github-actions/pull/53) merged as
`2915c822ced2b85d9f298a571b89ebeec0f20a5d` after GitHub-hosted run
[`31270930332`](https://github.com/example-user/github-actions/actions/runs/31270930332)
passed. A bounded activation repeated the b5 smoke, returned `built:false` and
`promoted:true`, moved b4 to `previous`, installed the matching multi-image
provider contract and passed production-user probes for standard b5 and
integration b1 with zero visible instances.

The first post-activation managed canary
[`31271113537`](https://github.com/example-user/github-actions/actions/runs/31271113537)
then failed its new exact-group assertion. External diagnostics showed that the
base standard image contains a dormant system `docker` group and upstream
cloud-init initially requested broad `docker,lxd,...` membership. The provider
pre-install script incorrectly treated existence of the group as fatal before
running `usermod`; it should permit the group object but remove the runner from
it. GARM destroyed the VM, released the sole lease and retained the diagnostic
bundle. The corrected provider first replaces supplementary membership with
exactly `sudo` for standard or `sudo,docker` for integration, then verifies the
result and explicitly rejects `lxd` membership.

PR [#54](https://github.com/example-user/github-actions/pull/54) merged as
`7d10de945b21f256ca4aaf233cc1d60ba7b43cff` after GitHub-hosted run
[`31271322615`](https://github.com/example-user/github-actions/actions/runs/31271322615)
passed. Provider `v0.1.5-nddev.3` was installed with SHA-256
`9fca75bff6298dc05762b2a3ee2cc9e7febcaf262042e51bb466e8b9cbbc37fd`;
production-user probes resolved both exact image mappings. Managed canary
[`31271432728`](https://github.com/example-user/github-actions/actions/runs/31271432728)
then passed the immutable boundary, JavaScript/composite action, command-file,
output, artifact and post-action checks on standard b5. Its one VM was fully
destroyed, the provider journal returned to zero leases and the retained
diagnostic bundle was durably exported.

That rollout also exercised the fail-closed local schema boundary. The running
observer still identified commit `0549b3a21d51590789d7f65e0e84a2503afd850d`
and rejected the new per-pool `worker_images` provider keys, so `/healthz`
returned 503 even though GARM remained active. No new pool was admitted. After
the exporter confirmed all six bundles and `214150/214150` bytes in RustFS with
zero pending objects, the observer was atomically replaced from the same
`7d10de9` commit at SHA-256
`50748f37634fc94c8d38e8a739c3f1930294b4e4e9d42499b4ac770297bc08b0`.
Health returned HTTP 200 with zero collection errors, instances, orphans,
missing leases and pending exports. ExamplePlatform and Captcha remained HTTP 200.

## Integration pool and lifecycle parity evidence

PR [#55](https://github.com/example-user/github-actions/pull/55) added the
repository-scoped integration Scale Set; PR
[#56](https://github.com/example-user/github-actions/pull/56) anchored the
already-imported GARM credential without copying its private key. The enabled
pool uses exact label and flavor `nddev-linux-integration`, maximum capacity
one, minimum idle zero and image alias
`nddev-ubuntu-24.04-amd64-docker-current`. The resolved full-VM image
fingerprint is
`88576e9649dc4d73d490f7042eba87209605f7d42fd5144250c263e20d3555cf`.
It contains official runner `2.336.0`, Docker `29.1.3` and a private daemon
inside the disposable VM; it has no CI-host socket or SSH path.

PRs [#57](https://github.com/example-user/github-actions/pull/57) through
[#62](https://github.com/example-user/github-actions/pull/62) built and refined
the manual parity gate from observed runner behavior. The official runner
deliberately mounts the VM-local Docker socket into Docker actions and job
containers. Early runs rejected those compatibility mounts and then exposed
Ubuntu's `/var/run -> /run` alias. A later run reached pinned `actions/checkout`
but correctly failed because root with all capabilities dropped could not
write runner-owned command files. The final job-container contract uses exact
numeric `1000:1000`, retains zero capabilities and no-new-privileges, proves
the socket is unreadable and unwritable, and keeps the mounted workspace and
command files writable. These were test-contract corrections; the full VM
remained the security boundary throughout.

That paragraph records the image identity used by the earlier canary. The
subsequent image-bound cache-delivery contract pins the current integration
runner to UID `1001` and GID `1002`; parity workflow run `31315211771` exposed
the stale `1000:1000` workflow value through a command-file `EACCES`. The
current contract therefore follows the provider image mapping at `1001:1002`.

The first integration jobs also exposed a fail-closed diagnostics schema that
allowed only the standard pool. No incorrectly classified object was uploaded.
PR [#59](https://github.com/example-user/github-actions/pull/59) replaced it with
schema v2 and the exact sorted allowlist `nddev-linux-integration` and
`nddev-linux-standard`. The installed exporter reports merge commit
`fbb18a19bb3388fbd8b43bfad45bb1848f2994eb`, binary SHA-256
`fa6d65c112baad188d2c9844bf2c7e8297f82e5d11a3b7ff916377f355a9efd9`
and config SHA-256
`67936382285bae99e2c7f11f05b318eadf618810db83b2e97ddc75c26f383b01`.

The final parity run
[`31275371962`](https://github.com/example-user/github-actions/actions/runs/31275371962)
executed merge commit
`ff6277be7515d2165bb5889af115b52a12723718` in three distinct one-job VMs:

| Job | Queue/provision wait | Execution | Result |
| --- | ---: | ---: | --- |
| VM-local Docker boundary | 73 s | 6 s | success |
| local Docker container action | 74 s | 10 s | success |
| job and service containers | 73 s | 20 s | success |

The third job passed digest-pinned Ubuntu and nginx pulls, service health and
DNS, numeric UID/GID, zero capabilities, no-new-privileges, inaccessible
Docker socket, pinned JavaScript checkout, command files, workspace and
post-action cleanup. The three resulting diagnostic bundle SHA-256 values
were:

- `0387085951619ed5c1a399681ccc9378041b9c8057269da2aa07270241065540`;
- `c227ba25c9ecd16739125fe329e9388b8bb56d4cb07aa273017f5e7162dc086d`;
- `774b5aa6c67f829f781e81c96120e47cb61a79c9337a0124c16840d9a0c3cbe7`.

The separate network-negative run
[`31275052462`](https://github.com/example-user/github-actions/actions/runs/31275052462)
retained public GitHub HTTPS while blocking GARM administration, the Incus
bridge host, cloud metadata and representative RFC1918 routes from both the VM
and a nested container. It waited 62 seconds for a cold VM and passed in six
seconds.

The timeout run
[`31275584801`](https://github.com/example-user/github-actions/actions/runs/31275584801)
waited 66 seconds for its VM and was automatically cancelled after 1 minute 8
seconds. Its log recorded `timeout signal received`, followed by GitHub's
`The operation was canceled.` and orphan-process cleanup. This `cancelled`
conclusion is the documented GitHub `timeout-minutes` behavior, not an
infrastructure failure. Its bundle SHA-256 was
`6d7b306f7384559db0f826b0ef463c6ba59d5293fd104e1d62d5376b819743a2`.

The explicit cancellation run
[`31275719110`](https://github.com/example-user/github-actions/actions/runs/31275719110)
waited 67 seconds for its VM. The proof step logged readiness before the
repository-scoped `gh run cancel` request, received the cancellation signal,
and completed orphan-process cleanup. The run and job concluded `cancelled`;
its bundle SHA-256 was
`779b31a7bc8f439504554028a99e4e29926a5d724dc09983b111831d7178d5f3`.

After the final timer cycle, the external RustFS canary confirmed `23/23`
source/exported bundles, `722255/722255` bytes, zero pending objects and zero
consecutive failures. Reconciliation reported zero Incus instances, leases,
orphans, missing instances and repository runner registrations; the loopback
observer returned HTTP 200. All twelve legacy listener services remained
running, and ExamplePlatform plus Captcha continued to return HTTP 200. Cold waits of
62-74 seconds make the unregistered warm pool the measured next startup
optimization; they do not justify a persistent registered runner.

## First cache promotion reboot rejected

The controlled cache-promotion reboot into boot ID
`d00fa803-a30a-4ff6-a2e9-c0bf55ef3a14` began at
`2026-08-08T22:36:16Z` only after all twelve legacy services had drained
without signaling an active worker. Incus, GARM, the gateway, observer,
RustFS, all legacy listeners, ExamplePlatform and Captcha recovered. Zot did not, so
the reboot gate was rejected and production promotion remained false.

The journal provided a deterministic ordering failure. Zot attempted to bind
`192.0.2.1:5001` at `22:36:26Z`, logged `bind: cannot assign requested
address`, and exited with status zero. Incus created `gha0` at `22:36:32Z` and
became active at `22:36:33Z`. The old unit was ordered only after
`network-online.target`; `Restart=on-failure` therefore neither waited for the
Incus-owned interface nor retried Zot's clean exit.

The corrective deployment contract requires both bridge-bound cache services
to start after and require Incus, runs a bounded sysfs-only wait for `gha0`,
raises the startup budget to 150 seconds and uses `Restart=always`. A manual
Zot restart cannot close this gate. The complete drain and reboot proof must be
repeated from the merged corrective commit before the Zot manifest may move to
production.

## Corrective cache promotion reboot accepted

PR [#68](https://github.com/example-user/github-actions/pull/68) was merged only
through the two-parent merge commit
`547f3cab399fb9bc107f107a6bb1d1f387a64f99`. Its probe and RustFS/Zot units
were installed at SHA-256 values `2430d54b9faaebe2a111c8a3b76608faf1202b90d039ffa49f8d869e3ecac2c4`,
`463deaddac603f249142d43d7e8e7705f6bcb0ed3a6b8afed878dfd07391ed64`
and `aeaf09612d5ee107482968dffacb6bc3812cac3a8f608fb47ea70729bebeaeb7`.
Server-side systemd verification passed before rollout. Zot then recovered to
authenticated HTTP 401 without disturbing the running RustFS PID.

All twelve legacy listeners were drained without signaling a Worker or
cancelling a job. The accepted boot ID is
`3a3ed9f4-6a6b-4732-9d27-d22afce1442a`, started at
`2026-08-08T22:57:44Z`. The journal recorded Incus active at monotonic
`12229714` microseconds, Zot active at `12340195` and RustFS active at
`12342090`. Both readiness probes exited zero, Zot had zero restarts, the bind
race was absent, and no manual cache start or restart occurred after boot.

Post-boot OCI CRUD passed independently for trusted, untrusted and promoter
identities; each writer received HTTP 403 outside its namespace. Release read
promoted content and received HTTP 403 for a write; anonymous access remained
HTTP 401. The installed audit helper was found at the older reviewed digest
`6c518d1d622a540a062773f94690e61e0a9779ada7ecc4728d8dbf0f12021fd9`
and was reconciled to the current merge-bound digest
`7a7401873c271011ff2d76148630419bf23fd762691d504d73777460552d9aa8`
before those role checks. This helper-only correction restarted neither cache
service and is recorded rather than hidden.

The final observer sample was healthy with zero collection errors, instances,
leases, orphans and missing instances. Diagnostics remained `23/23`,
`722255/722255` bytes, with zero pending objects or failures. All twelve legacy
listeners, five retained containers, ExamplePlatform and Captcha recovered; systemd had
zero failed units and root storage remained 40 percent free. No retained cache
secret matched the complete boot journal. The exact result is digest-bound in
`config/zot-v2.1.20-reboot-audit.json`; Zot is production-ready while RustFS
RC.1 remains canary-only.

## Observer v2 bounded-convergence runtime evidence

PR [#76](https://github.com/example-user/github-actions/pull/76) merged only
through two-parent merge commit
`bbe83ce3100a1d4faea21c5364b90843850a65fc` after CI run
[`31288730335`](https://github.com/example-user/github-actions/actions/runs/31288730335)
passed. The exact merge-bound `gha-fleet-observer` binary was atomically
installed at SHA-256
`6b6e71737a49f21b9c217302040e464b85c5f777c85c00464fcc2aaa69908d18`.
Only the observer was restarted; its systemd `NRestarts` remained zero and no
GARM, provider, cache or retained-service restart occurred.

The initial schema-v2 snapshot was healthy and synchronized at 38/38 diagnostic
bundles. Deployment-smoke run
[`31288871282`](https://github.com/example-user/github-actions/actions/runs/31288871282)
passed and converged to 39/39. Controlled proof run
[`31288969439`](https://github.com/example-user/github-actions/actions/runs/31288969439)
then ran on one official disposable standard VM from the exact merge commit and
passed in 12 seconds after its 65-second cold wait.

The exporter status was anchored at `2026-08-09T01:48:00.704429096Z` with
39/39 bundles and zero pending objects. After teardown, the observer captured
the new local bundle at `01:48:26.078088572Z`: snapshot schema v2 remained
healthy with no collection error, state `convergence-grace`, signed delta
`+1 bundle / +36603 bytes` and 65 seconds remaining. Incus instances, journal
leases, orphans and missing instances were already zero.

The ordinary timer tick at `01:49:04.634476115Z` exported the bundle. The next
snapshot reported `synchronized`, 40/40 bundles and `1380782/1380782` bytes,
zero pending objects/failures and zero deltas. Repository runner registrations
returned to zero. All six platform units were active, systemd had zero failed
units, root storage remained 40 percent free, all twelve legacy listeners were
preserved, and ExamplePlatform plus Captcha returned HTTP 200. The exact evidence is
machine-readable in `config/observer-v2-convergence-audit.json`.

## Unregistered warm image stage-only gate

At `2026-08-09T02:59:23Z`, merge commit
`5c080d32e3ddb5df4924f5f8dcf62de086a11e06` built immutable standard image
`nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801-b6` on the target host. Its
Incus fingerprint is
`d36fc3f425133fd2a5335e48fd8d2e4f8598d6ff54675b84c5523d842b93578c`;
the existing `current` alias remained on `b5` because the command used
`--stage-only`.

The disposable smoke VM proved official runner `2.336.0`, root-owned warm
readiness, absent registration state, absent nested CPU flags and forbidden
devices, blocked host/metadata routes, masked SSH units and working public
egress. Builder and smoke instances were deleted, journal leases/claims and
Incus orphans returned to zero, root storage remained 40 percent free, and all
twelve legacy listeners remained active. The exact evidence is machine-readable
in `config/warm-image-b6-audit.json`.

At `2026-08-09T03:09:12Z`, ordinary merge
`c8c5c72469e4afb47ea71fcb43b64b40ab15d8de` was deployed in a bounded
maintenance transaction. The existing immutable `b6` image passed smoke again;
`current` moved to `b6`, `previous` retained `b5`, and the same-commit provider
probe resolved the exact reviewed fingerprint with zero visible instances.
GARM, gateway, observer, RustFS, Zot and the zero-target warm timer returned
active; observer health was green and all twelve legacy listeners were retained.
The exact promotion record is
`config/warm-image-b6-promotion-audit.json`.

## Pinned sccache worker image stage gate

On 2026-08-09, implementation merge
`95945e401a2cd0f5a1c6bfd4196a4ec8ada5489b` built standard `b7` with pinned
`sccache v0.17.0`. The first smoke correctly failed closed after the successful
digest check emitted a non-JSON status line; it moved no active alias and its
smoke VM was deleted. Merge `465bf9f3355a3e5a9921f5ab60abe2a3ebefa507`
made both smoke variants use silent SHA-256 verification and added the
regression contract.

The repeated standard stage-only smoke accepted fingerprint
`bed3af7a52d9a0de5502e23a8a37a076aa42f6b15b16ba7db095e0ebf453a2ce`.
Integration `b3` was then built and accepted as
`8c5ca5c79ba5b24b58fe653460ce4f3950ed3991f0837b46a834b897b525f960`.
Both proved runner `2.336.0`, sccache `v0.17.0`, fresh machine identity,
unregistered warm readiness, absent SSH and nested CPU/device exposure, blocked
host/metadata routes and public egress. Integration additionally proved Docker
`29.1.3`, overlay2, systemd cgroups, non-root VM-local socket access, BuildKit
action build and service networking.

The stage left zero builder/smoke instances, leases, claims, orphans or failed
systemd units, retained all twelve legacy listeners and returned HTTP 200 for
ExamplePlatform and Captcha. No active image alias was moved. The machine-readable
stage record is `config/sccache-image-stage-audit.json`. Provider activation
and the first live RustFS compiler-cache prime/hit pair are now recorded
separately in `config/sccache-runtime-canary-audit.json`; the statistical and
RustFS production-promotion gates remain open.

## Direct-JIT warm image stage gate

On 2026-08-09, implementation merge
`54ae35a64cb1da9871669cdf27069dae0641accc` built standard `b8` and integration
`b4` with the official `Runner.Listener warmup` executed before unregistered
readiness. Both were built and smoked with `--stage-only`; standard fingerprint
`c8feffddd19ba89b7a49362d75fb07cc89ba45f77451234e072f6f6f227a3e9d` and
integration fingerprint
`ddd02f9d62c35d9955cd2cd5057c6926375da25177d916fc0f5d054640ac7bcf`
were added as immutable aliases while active b7/b3 and previous b6/b2 aliases
remained unchanged.

Both disposable smoke VMs proved official runner `2.336.0`, sccache `v0.17.0`,
fresh machine identities, absent registration state after warmup, masked SSH,
no nested CPU/device exposure, blocked host/metadata routes and public egress.
The integration smoke additionally proved a VM-local Docker 29.1.3 socket on
tmpfs, overlay2, systemd cgroups, non-root access, BuildKit action build and
service networking. Builders and smoke VMs were removed before the old b7 warm
capacity was restored. The restored state had one ready unregistered b7 VM,
zero claims/queue work/orphans, RustFS diagnostics 107/107 exported, twelve
legacy listeners, healthy GARM/gateway/observer/RustFS/Zot, and HTTP 200 from
ExamplePlatform and Captcha. Exact machine-readable evidence is
`config/direct-jit-image-stage-audit.json`.

## Warm backpressure v12 rollout

Ordinary merge `d8ad116bef2c609ae9218a4268cb73705525a6e4` promoted provider
`v0.1.5-nddev.12`. The rollout stopped only warm replenishment, proved zero
claims, captured diagnostics for and destroyed the exact unregistered v11 VM,
then atomically installed merge-bound provider, controller, observer and
platform policy. One exact v11 rollback set was retained; temporary staging was
deleted after verification.

Workflow run
[`31311977563`](https://github.com/example-user/github-actions/actions/runs/31311977563)
claimed v12 VM `warm-standard-c2b20676d8f0`. While it ran, the systemd timer
observed `pool-saturated` and then memory-driven `host-unhealthy`. Both returned
`deferred=true` with exit status zero, created no VM and left zero failed units.
The Rust job completed in 48 seconds with `57/57` RustFS hits and zero cache
errors; its VM was destroyed and never reused. Different v12 VM
`warm-standard-dba89199dee8` reached `warm-ready` with zero claims.

Observer health returned HTTP 200 with one exact Incus/journal lease, no
orphan or missing instances, 73/73 diagnostics exported, twelve retained
legacy listeners and HTTP 200 from ExamplePlatform and Captcha. This closes expected
warm backpressure semantics, not weighted fairness, integration starvation,
statistical cache, production reliability or HA. The exact evidence is
`config/warm-backpressure-v12-rollout-audit.json`.

## First unregistered warm one-job proof

Merge `aa574c8510e9b9a6ac5bac49c01d703dcfe9db98` created ready VM
`warm-standard-3c2651bbda83`. It held no repository, job or GitHub registration
identity. Workflow run
[`31292321729`](https://github.com/example-user/github-actions/actions/runs/31292321729)
caused GARM runner `nddev-yulcyrrnguto` to claim that exact VM. The durable claim
was injected at `03:23:31.188Z`; the official runner started the GitHub job at
`03:23:41Z` and completed every basic parity step successfully in 13 seconds.

GARM requested deletion at `03:23:55.807Z`. The executed VM, its lease and its
claim were absent by the first `03:24:10Z` poll. The timer created different VM
`warm-standard-173f11a2cbe2` at `03:24:29.931Z`; it reached `warm-ready` at
`03:25:06.670Z` with zero claims. Observer schema v2 was healthy with one exact
bound instance, no orphan/missing resources, 41/41 diagnostic bundles exported
to RustFS, 40 percent root free space and twelve retained legacy listeners.
GitHub reported zero repository runner registrations after teardown.

The first proof is not a latency SLO result. GitHub job creation-to-start was 19
seconds and durable injection-to-start was about 10 seconds, both above the
five-second p95 target. This establishes correctness and gives an optimization
baseline; GARM queue acquisition and activation remain to be profiled across the
required statistical run set. The exact evidence is machine-readable in
`config/warm-pool-first-job-audit.json`.

## Controlled host reboot recovery

Provider `v0.1.5-nddev.8` and all schema consumers were deployed from ordinary
merge `6e865cf2b011b465cfb162b4faffb2841dab86f2`. With zero active legacy
workers, one ready unclaimed VM and all retained services healthy, the host was
rebooted from boot ID `3a3ed9f4-6a6b-4732-9d27-d22afce1442a` into
`ddc33bc5-c743-4415-bfec-0926859c9201`.

The host booted at `06:49:19Z`, SSH returned at `06:49:24Z`, and systemd,
observer, all twelve legacy listeners, ExamplePlatform and Captcha were ready by
`06:49:42Z`. Incus automatically restarted the same unregistered warm VM; its
durable lease remained `warm-ready` with zero claims and no orphan or missing
resource.

Official-runner canary
[`31299762665`](https://github.com/example-user/github-actions/actions/runs/31299762665)
then consumed that exact VM and passed every boundary, composite-action,
command-file, post-action and artifact check in 11 seconds. The VM was destroyed
and never returned to the pool. A different merge-bound VM reached warm-ready,
and RustFS converged to 52/52 diagnostics with zero pending objects. The exact
evidence and non-HA verdict are machine-readable in
`config/host-reboot-recovery-audit.json`.

## Cross-pool preemption v14 rollout

Ordinary merges `8ad1b08a1c053f7f22600c7af145bb6aaaef2dc0`,
`169aaef868cbaae2931aafbc6453b0fe3351ed4e` and
`d56a4d7cde6f6568cbef2c25f91c72a6a8f03402` delivered the GARM startup guard,
the image-bound job-container runner identity and provider
`v0.1.5-nddev.14`. The rollout migrated the admission journal from schema 3 to
schema 4, retained one checksum-verified v13 rollback set and removed temporary
deployment staging after postchecks.

Corrected parity run
[`31315682132`](https://github.com/example-user/github-actions/actions/runs/31315682132)
completed the VM-local Docker boundary, local Docker action, and job/service
container jobs in three distinct disposable VMs. Each VM emitted a uniquely
hashed diagnostic archive, was destroyed after its one job and was never
returned to the warm pool. During the hand-off to the third job, a standard
warm VM was still preparing; the integration request received seven bounded
`insufficient-cpu` retries from `13:26:02.667994492Z` through
`13:26:32.997088231Z`, then succeeded after that warm VM became eligible for
safe preemption. This is evidence for the remaining central queue-intent
admission requirement, not a lifecycle failure.

Network-boundary canary
[`31316397001`](https://github.com/example-user/github-actions/actions/runs/31316397001)
then committed preemption of `warm-standard-02dc98ce1075`, advanced the durable
counter from zero to one in the same journal generation, destroyed the victim,
ran successfully in `nddev-pydzqkkbyor4` and converged with replacement
`warm-standard-461072e0a52e`. The transient gauge returned to zero while the
monotonic total remained one.

The accepted postcondition contained one exact warm-ready Incus/journal lease,
zero claims, orphans or missing instances, and 91/91 diagnostic bundles
confirmed in RustFS with zero pending objects or consecutive export failures.
GARM and observer restart counts were zero, root free space was 36 percent,
all twelve legacy listeners remained active, and ExamplePlatform and Captcha remained
healthy. Exact binary/configuration digests, job/VM identities, timestamps and
diagnostic hashes are machine-readable in
`config/preemption-v14-rollout-audit.json`.

## Warm identity nddev.19 rollout

Direct-JIT sample 13, run `31341001674`, exposed a cross-plane identity defect:
GARM reconciled the physical warm name `warm-standard-7cf6ba0130ef` against the
logical runner `nddev-qofu6jxasefm` and the provider deleted the VM while the
official worker was uploading an artifact. Provider `v0.1.5-nddev.19` now
projects the durable logical name into GARM inventory while retaining the
physical Incus name as `ProviderID`; observer `v0.6.0` schema 6 makes any running
queue intent without an execution lease unhealthy.

The merge-bound binaries were built twice reproducibly and deployed with an
armed checksum-verified rollback set. Reconciliation canary `31342261697` then
held the job for 45 seconds across the delayed started event, completed artifact
upload and post-actions, and retained one exact logical-to-physical claim in all
in-job observations. Teardown converged to zero active queue intents, claims,
runner registrations, orphan or missing instances; a distinct warm VM replaced
the consumed VM and RustFS diagnostics converged to 129/129. GARM and observer
restart counts remained zero, all twelve legacy listeners remained active, and
ExamplePlatform and Captcha remained healthy. Exact chronology, identities, artifact
digests and postconditions are in
`config/warm-identity-nddev19-rollout-audit.json`. At rollout capture time, the
fresh 20-sample series still remained before any warm-start p95 claim.

That series completed on 2026-08-10 from workflow head
`9688c62ed225037f761acf4bcd14dbab79cb9e02`. All 20 runs and jobs, opaque GARM
IDs, logical runners, physical VMs, replacements, diagnostic archives and
archive digests were unique. GitHub independently reported all runs successful,
and all 20 on-host archive hashes matched the evidence. Preflight load stayed
between 0.82 and 3.80; every sample advanced the diagnostic count by exactly
one and the replacement of one sample became the physical worker of the next.
The final fleet had one ready VM, zero claims, queue work, runner registrations,
orphan or missing instances, 149/149 RustFS diagnostics, all twelve legacy
listeners and healthy retained applications.

The statistically valid performance result failed promotion: nearest-rank
median was 5.320 seconds, p95 6.700 seconds and maximum 6.829 seconds against an
exclusive below-five-second target. Runner logs show that the observed interval
contains a variable pre-`Runner.Listener ExecuteCommand` component plus roughly
two to four seconds of official runner startup and broker session creation.
Host load alone does not explain the tail: its sample correlation was weak and
a 6.491-second sample occurred at load 0.82. Exact boundary instrumentation and
a new series are required before promotion. The raw machine-readable baseline
is `config/direct-jit-nddev19-latency-audit.json`.

## Clock-safe direct-JIT telemetry rollout

Ordinary merge `dfc1decc1a024d524cab3fdd3db99cb57234c4c2` installed GARM
`v0.2.1-nddev.11` with secret-free `AcquireJobs` and provider-call phase
events. Canary run `31347393955` proved the provider began 862 ms after
`JobAssigned` and completed in 328 ms while all cache and one-job checks passed.

Ordinary merge `2899b44fa4625aa5edeb2065af59e3d43c6758eb` installed provider
`v0.1.5-nddev.20` and same-commit observer, replacing the per-job global CA
rebuild with process-scoped RustFS trust. Canary `31348084752` passed the exact
bundle ownership, environment, TLS and RustFS access contract.

The two canaries also exposed that a resumed guest clock could lag the host;
the guest runner timestamp could precede host-side injection. Ordinary merge
`0b6e72ba39d92fce0bd0f11623a551b1b0b04a82` therefore installed provider
`v0.1.5-nddev.21`, same-commit observer and schema-2 clock-safe evidence.
Canary `31348692974` completed with:

- GitHub job `created_at` to `started_at`: 7000 ms;
- host `JobAssigned` to provider start: 1186 ms;
- host provider call: 411 ms;
- guest-local cache/setup path: 340 ms;
- zero token-shaped diagnostic matches;
- zero claims, queue intents, orphan/missing instances and pending exports;
- one distinct replacement warm VM, 12 legacy listeners and healthy retained
  ExamplePlatform/Captcha services.

This is a one-sample instrumentation smoke, not the 20-sample promotion series;
the below-five-second p95 gate remains explicitly open. Its immutable evidence
is `config/direct-jit-nddev21-clock-smoke-audit.json`.

The replacement 20-sample series completed from workflow head
`ca246a312ebbd0082742640e05ff455edce20016` while the deployed provider remained
the independently identified build `0b6e72ba39d92fce0bd0f11623a551b1b0b04a82`.
All jobs succeeded and used distinct physical workers. Nearest-rank minimum was
6 seconds, median and p95 were 7 seconds, and maximum was 8 seconds. The
exclusive below-five-second promotion gate therefore remains failed rather
than being relaxed.

Same-clock phase evidence localizes the delay. Assignment-to-provider-start
had median 1041 ms and p95 1213 ms; the provider call had median 439 ms and p95
659 ms; assignment-to-provider-complete had median 1451 ms and p95 1746 ms;
guest-local setup had median 308 ms and p95 513 ms. The remaining interval is
primarily official runner registration, broker-session creation and GitHub job
acquisition. At completion the fleet had one ready VM, zero queue work, claims,
registrations, orphan or missing instances, 175/175 exported diagnostics, all
twelve legacy listeners and healthy ExamplePlatform/Captcha services. Exact evidence is
`config/direct-jit-nddev21-latency-audit.json`.

That remaining interval has since been measured rather than inferred. Across the
same twenty samples, guest-clock `runner exec` to broker `Session created` has a
median of 2940 ms and a p95 of 3317 ms. The official runner log shows the same
shape in every sample and no retry or backoff: OAuth credential load from the
JIT RSA key, three concurrent `Location.GetConnectionData` calls, Vss connection
setup, and one OAuth token request. Subtracting only the segments this
repository can change from the 7-second end-to-end median leaves a 5241 ms
floor, above the 5000 ms exclusive target, so the gate is unreachable while warm
VMs remain unregistered. The Phase 0 pilot recorded the same workflow on
GitHub-hosted runners at a 3-second median and 5-second p95 queue-to-start;
those runners are registered and connected before their job is created.
ADR 0031 records the decision, `config/warm-start-latency-decomposition.json` is
the machine-readable evidence, and its contract test recomputes every number
from the primary records.

## Baked toolchain image stage

Both ADR 0030 images were built and smoked on 2026-08-10 without moving either
current alias. The standard image is
`nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801-b9` at fingerprint
`366866d4ec60`, the integration image is
`nddev-ubuntu-24.04-amd64-docker-runner-2.336.0-r20260801-b5` at `fa25e3753bbd`,
and both carry the manifest fingerprint the repository pins. The standard and
Docker current aliases still resolve to `c8feffddd19b` and `ddd02f9d62c3`, so no
pool changed image.

Each smoke ran inside its published image and reported Bun 1.3.14, Go 1.26.5,
Rust 1.97.1 and uv 0.11.30, a runner tool cache at
`/home/runner/actions-runner/_work/_tool`, and the unchanged worker boundary:
absent registration state, ready-unregistered warm agent, absent forbidden
devices and nested CPU flags, blocked host and metadata routes, purged and
masked SSH. The integration smoke additionally proved the VM-local Docker
socket, non-root access, local action build and service network.

The build window converged with one ready warm VM, zero claims, queue work,
orphan or missing instances and pending exports, all twelve legacy listeners and
healthy ExamplePlatform and Captcha services. Exact evidence is
`config/toolchain-image-stage-audit.json`.

Two preconditions were learned by hitting them and are now in the runbook. The
thin pool stood at 86.71 percent before the build and needed the documented
collection of images no active alias designates, which returned it to 50.54
percent; it finished at 72.63 percent with both new images present. The build's
own preflight re-checks host load after the warm pool has already been drained,
so entering on a single sample at the coexistence reserve fails the transaction
with the warm VM already gone.

## Baked toolchain promotion and canary

Both images were promoted on 2026-08-10 inside a three-minute activation window
in the runbook order: drain, promote `current`, install the matching provider
contract, probe as the production user, restart the manager. The standard
current alias moved to `366866d4ec60` and the Docker current alias to
`fa25e3753bbd`, with `c8feffddd19b` and `ddd02f9d62c3` retained as previous. The
provider pins both alias and fingerprint, so a half-applied promotion fails
closed. The replacement warm VM reported the baked Go 1.26.5 and Rust 1.97.1 in
its image properties.

The runtime canary then proved the claim ADR 0030 exists for. `Install
toolchain` fell from 19 to 2 seconds on Go and from 21 to 1 second on Rust.
`actions/setup-go` now logs `Found in cache @
/home/runner/actions-runner/_work/_tool/go/1.26.5/x64` where it previously
logged `Attempting to download 1.26.5`, and the Rust installer exits early
because `rustc` and `cargo` already report the pinned version.

Only that step is a controlled comparison. Total job time is not, because the
surrounding steps differ by cache mode, host contention and repository growth
since the baseline runs. The saving is also a constant present in both cold and
warm runs, so it improves absolute job time without moving the cold-to-warm
ratio the Phase 3 cache gate measures; ADR 0032 records why that gate needs
representative fixtures instead. Evidence is
`config/baked-toolchain-canary-audit.json`.

Running the suite on the fleet also exposed a defect that had nothing to do with
the images. Since direct-JIT activation landed on 2026-08-10 at 00:22, the
official runner executes under `umask 077`, and eight assertions across five
packages depended on permission bits that a umask masks: four on modes a test
creates and four on modes a checkout writes. The worker umask was not relaxed;
the assertions were made umask-independent and `make test-umask` now reproduces
both classes by running the suite over a copy of the tracked tree with group and
other bits stripped.

## Retained boundary

ExamplePlatform, Captcha and all twelve legacy listener services remain outside the
replacement deployment lifecycle. Active legacy workers are informational,
not a preflight failure, because coexistence is a required migration invariant.
The replacement receives separate Incus, RustFS, registry, state, credentials,
network and storage namespaces.

## Reproduce

Build the binary from the exact repository revision and run as an account that
can open `/dev/kvm`:

```bash
go build -trimpath -o gha-fleet ./cmd/gha-fleet
sudo ./gha-fleet preflight \
  --config config/server-example-legacy.yaml \
  --pool nddev-linux-fast
```

Exit code `0` means the cold pilot is ready. Exit code `3` is a normal
fail-closed policy result; the JSON findings identify every known blocker.
