# Local cache plane

The cache plane is an optimization boundary, never an artifact-of-record or a
reason to weaken worker isolation. It consists of two independently scoped
services on the CI host:

- RustFS for S3-compatible compiler and content-addressed object caches;
- Zot minimal for OCI images and BuildKit registry cache.

Both listen only on the Incus bridge address, require TLS, run as separate
unprivileged users and use storage outside the Incus VM thin pool. Neither
service receives a Docker/Incus socket, a route to ExamplePlatform/Captcha data or a
credential through a general environment file.

## Artifact identity and promotion

`config/cache-artifacts.yaml` is the canonical, strict manifest. It pins source
commits, release asset names, HTTPS URLs and SHA-256 digests. The command below
validates it and emits its canonical fingerprint:

```bash
gha-fleet validate-cache \
  --manifest /etc/gha-fleet/cache-artifacts.yaml
```

The components are promoted independently; a blocked RustFS candidate does not
weaken or hide Zot's completed gate:

| Component | Pin | Evidence | Current stage |
| --- | --- | --- | --- |
| RustFS | `1.0.0-rc.1`, commit `778f1dfa2155cbbc61ad54e6896de9e29d2c4d8d` | GitHub-verified source commit; published SLSA v1 statement binds the exact archive digest to that commit; published CycloneDX SBOM | `canary-only` |
| Zot | `v2.1.20`, commit `3b5796d834e8661ea661a5fcc47add8d4405aebf`, minimal amd64 asset | GitHub-verified source commit; reproducible release SHA-256; isolated storage/authz audits; automatic Incus-ordered reboot recovery | `production` |

RustFS is a pre-release. OSV scanning of the RC.1 CycloneDX SBOM reports three
advisories: zero critical/high, two medium and one unknown severity. The
selected binary does not expose SFTP, FTPS, WebDAV or Swift listeners, those
features are explicitly disabled, and STS/OIDC/service-account paths are not
part of the cache design. Production promotion remains false until a stable,
rescanned artifact and an exact release-source feature/call-path review pass.
As of 2026-08-08, RC.1 is the newest official release and is still marked
pre-release; there is no stable RustFS artifact to promote.

Zot's stripped-binary vulnerability scan conservatively reports three symbols
from required-but-unused modules. The exact release source call-graph scan of
`./cmd/zot` reports zero called vulnerabilities and `go list -deps` excludes
the affected `s3crypto` and OpenPGP packages. The deployed profile is minimal,
filesystem-only and omits the entire `extensions` object. Zot does not publish
a GitHub artifact attestation for this release. Two clean source checkouts with
separate module and build caches, exact Go `1.26.5`, `CGO_ENABLED=0`,
`GOEXPERIMENT=jsonv2`, PIE and `-trimpath` both reproduced release SHA-256
`902ea958c4a59c0f5c4ac9fa2bbaad8716e80551bcaede7ab4ea998bf57190a6`
byte-for-byte. `scripts/zot-reproducible-build.sh` freezes and replays that
contract; `config/zot-v2.1.20-reproducibility.json` records the two independent
runs and its SHA-256 is pinned in the cache manifest. Production promotion
was accepted only after GC, full-disk, repository-scoped identity and automatic
host-reboot gates all passed. The three runtime evidence files and their
SHA-256 values are pinned in `config/cache-artifacts.yaml`.

## Network and service boundary

| Service | Address | Worker access | Host storage |
| --- | --- | --- | --- |
| RustFS | `https://192.0.2.1:9002` | S3 SigV4 with prefix-scoped credential | `/var/lib/gha-cache/rustfs` |
| Zot | `https://192.0.2.1:5001` | HTTP Basic over TLS with repository policy | `/var/lib/gha-cache/zot` |

The host firewall accepts those ports only from `192.0.2.0/24`. The systemd
units add an address allowlist, empty capability sets, strict filesystem
protection, credential loading through systemd's private credential directory,
memory/task limits and independent users. Private keys and root/admin cache
credentials remain root-owned below `/etc/gha-fleet/{pki/cache,secrets}`.
Both units require and start after Incus, wait for the Incus-owned `gha0`
interface through a bounded sysfs-only probe, and restart after any unexpected
clean or failed daemon exit. This ordering is part of the reboot contract, not
an operator timing assumption.

RustFS alone is allowed `AF_NETLINK` because its startup validation enumerates
local interfaces before accepting the bridge address. It retains an empty
capability set and cannot use Netlink to bypass the IP or filesystem policy.

RustFS writes only `warn` and higher events to an hourly rolling log and mirrors
that small stream to journald. Its local cleaner runs every 15 minutes with a
512 MiB total ceiling, a 64 MiB per-file ceiling, 48-file retention and bounded
compression workers. OTLP exporters remain explicitly disabled until the
external collector and its failure behavior are reviewed.

## Trust and credential model

Root RustFS credentials exist only to start and reconcile the service. A
separate random RPC authentication secret is loaded as a systemd credential;
the launcher rejects default values and reuse of the S3 root secret before
starting the server. None of these credentials is rendered into a golden
image, GARM metadata or a job. The manager creates
long-lived, narrowly scoped IAM users per repository and trust class through
the root-invoked `gha-fleet reconcile-rustfs-cache` boundary; STS and service
accounts are not used. A credential can address only its namespace:

```text
organization/repository/trust/platform/architecture/toolchain/lock_digest/ref_class
```

The required classes are:

- trusted branch: read/write only its repository's trusted prefix;
- untrusted PR: read/write only its disposable untrusted prefix;
- release: read-only access to a separately promoted immutable prefix;
- no credential: public/fork jobs retained on GitHub-hosted capacity.

Zot has a default-deny catch-all and three exact recursive repository paths:

- `cache/example-user/github-actions/trusted/**`: one trusted writer;
- `cache/example-user/github-actions/untrusted/**`: a different untrusted writer;
- `cache/example-user/github-actions/promoted/**`: a host-only promoter and a
  read-only release identity.

No runner identity is an administrator. Neither writer can cross the trust
boundary, and release jobs cannot write promoted data. The credential
reconciler replaces the broad pilot users atomically, switching htpasswd only
after all four root-only credential pairs are durable.

RustFS uses an independent four-identity contract in
`config/rustfs-cache-identities.yaml`: trusted and untrusted writers have
different prefixes, a host-only promoter writes the promoted prefix, and the
release identity can only read that prefix. The reconciler creates a dedicated
64 GiB bucket, applies 7/30/90-day prefix lifecycle rules, and proves effective
read/write, cross-prefix and delete boundaries with real signed requests after
every apply. Managed pairs are stored as `root:garm` mode `0640` beneath a
root-writable mode-`0750` directory. A remote identity without its local secret,
a partial local set, permission drift or policy drift fails closed.

Creating these identities does not authorize production workflow rollout.
Provider `v0.1.5-nddev.11` implements the reviewed one-job delivery path for
cold and warm VMs: no secret enters Incus metadata or cloud-init, and the
official runner pre-job hook masks, exports and deletes the staged identity
before workflow code. Merge-bound warm and cold authorization canaries now
prove own-prefix read/write, cross-trust and delete denial, exact worker-file
ownership, Docker action and job/service-container parity, complete one-job VM
destruction and zero credential matches in GitHub logs or worker diagnostics.
The secret-free evidence is
`config/one-job-cache-delivery-audit.json`. RustFS remains `canary-only` while
RC.1 is the only upstream release; delivery correctness does not waive the
stable-artifact promotion gate or the statistical speedup and hit-rate gates.

Image revisions `nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801-b7`
and `nddev-ubuntu-24.04-amd64-docker-runner-2.336.0-r20260801-b3` pin
`sccache v0.17.0` by source commit, official release archive digest and
extracted binary digest. Both immutable images passed stage-only runtime smoke
on the target host; their exact fingerprints and evidence are recorded in
`config/sccache-image-stage-audit.json`. The workflow adapter derives the final
namespace from repository, trust role, platform, architecture, toolchain,
dependency-lock digest and ref class. Trusted benchmark jobs use read/write
mode; release jobs are restricted to the promoted prefix and read-only mode.
Provider activation and the first live cache-hit canary are complete. The exact
prime and hit runs used the same trust-separated Rust namespace: the prime
reported `0/57` hits and the following disposable VM reported `57/57` hits,
zero misses and zero read, write or backend errors. The selected Rust-only run
also skipped all four unrelated workload jobs. Exact run IDs, artifact digests,
image fingerprints, teardown/replenishment evidence and non-completion gates
are recorded in `config/sccache-runtime-canary-audit.json`.

This pair is functional evidence, not the statistical cache gate. The warm run
reduced the measured workload interval by 27.673 percent in one sample, but the
required repeated cold/hit protocol remains. The canary also reproduced an
expected-capacity defect: `pool-saturated` made the warm-pool oneshot fail and
that failed unit caused one `host-unhealthy` retry before automatic recovery.
The cache result is accepted; warm backpressure semantics remain incomplete
until expected admission refusal is a successful, observable no-op.

The deliverable public CA is `/etc/gha-fleet/trust/rustfs-ca.pem`, a singly
linked `root:root` mode-`0644` file below mode-`0755` parents. Cache TLS private
keys remain under `/etc/gha-fleet/pki/cache`, whose mode-`0700` boundary must
never be relaxed for runner delivery.

The identity plane was applied on `server-example-legacy` on 2026-08-09 by
merge-bound controller commit `4d3ebc2670d78271961d6809ba12455b09194c16`.
Runtime discovery exposed and corrected two compatibility details before the
gate was accepted: the low-level SigV4 signer required the S3
`X-Amz-Content-Sha256` middleware behavior, and RustFS returns canned policies
inside a metadata envelope. The retained credentials were neither regenerated
nor deleted during recovery. The final managed apply passed own-prefix writes,
release-write denial, cross-prefix denial, delete denial and root cleanup; the
next read-only plan had no actions and the bucket contained zero probe objects.
The complete secret-free evidence is
`config/rustfs-cache-identity-rollout-audit.json`.

## Runtime evidence

On 2026-08-08 both exact binaries ran in a disposable VM cloned from the
current worker golden image. The tests performed real network operations, not
mock calls:

- RustFS: bucket CRUD, 1 MiB object, 6 MiB multipart object, byte-for-byte
  SHA-256 validation, orderly restart, main-process `SIGKILL`, recovery and
  cleanup;
- RustFS IAM: an unscoped user was denied, gained access only after a
  bucket-specific policy attach, remained denied on a second bucket and lost
  access immediately after deletion;
- RustFS worker boundary: a worker received only an ephemeral bucket-scoped
  identity, passed byte-integrity CRUD from the VM and remained denied on a
  second bucket; no root credential entered the VM;
- Zot: OCI config/layer upload, manifest push/pull, digest validation, manifest
  deletion, orderly restart, main-process `SIGKILL` and recovery;
- TLS: both services validated against a private cache CA, accepted TLS 1.2
  and rejected TLS 1.1; anonymous requests were denied;
- Zot pilot authorization: a writer could mutate only `cache/**`, a read-only
  user could pull but not upload, and the writer was denied outside its
  namespace;
- both services converged to active with exactly one automatic restart after
  fault injection.

Later that day RustFS RC.1 was evaluated before changing the live canary. Its
GitHub asset digest, published checksum and SLSA subject all matched; the
extracted binary reported the exact verified source commit. In a fresh full VM
it passed TLS-backed CRUD, multipart, orderly restart, main-process `SIGKILL`
recovery, unscoped denial, policy attach, cross-bucket denial and revocation.
New destructive audit gates also proved a 1 MiB hard quota rejects excess
writes and reclaims deleted capacity, while a time-compressed lifecycle scanner
expired only the matching prefix and retained its negative control. TLS 1.2
was accepted, TLS 1.1 rejected, credentials were absent from the journal, and
the VM was fully destroyed.

The first live RC.1 attempt then failed closed at the original 60-second quota
readiness deadline because the upgraded store had not yet completed its first
authoritative usage snapshot. The exact beta.12 binary was restored
atomically, its CRUD/multipart smoke passed, and GARM plus retained applications
remained healthy. Upstream RC.1 source documents this migration window and the
default scanner cycle is itself 60 seconds, so the bounded gate was corrected
to allow 180 seconds rather than accepting approximate usage.

The second live attempt passed both migration-time and post-crash quota gates:
hard-limit rejection, absence of a rejected residual object, deleted-capacity
reclaim and anonymous admin denial. CRUD, multipart, orderly restart,
main-process `SIGKILL`, IAM isolation/revocation, TLS 1.2 acceptance, TLS 1.1
rejection and journal credential scanning also passed. The installed live
canary is now RC.1 at binary digest
`eb63af6574150a62a4509461f16b178976e67485ce6beacf41e6b67944d41db0`.
Production promotion remains false because RC.1 is a pre-release and no runner
receives a RustFS cache credential before the remaining RustFS gates.

Zot deletion removes the manifest immediately; unreferenced blobs remain until
its bounded GC interval. RustFS and Zot data are disposable caches, so recovery
testing proves integrity and lifecycle behavior but does not turn them into a
backup system.

Zot's destructive promotion audit uses a dedicated loopback ext4 filesystem in
a disposable VM. It stops the local registry before offline retention
verification, proves referenced/orphan separation, forces a bounded ENOSPC
write rejection, verifies retained reads during pressure, reclaims space,
proves a new push/pull and finishes with offline `e2fsck`. The live cache path
and retained tenants are never audit targets.

The accepted 2026-08-08 run used exact Zot SHA-256
`902ea958c4a59c0f5c4ac9fa2bbaad8716e80551bcaede7ab4ea998bf57190a6`
and a 256 MiB loopback filesystem in a two-vCPU, 4 GiB, networkless VM copied
from the current standard image. GC removed only the orphan. Exhaustion
returned HTTP 500 while Zot stayed active and retained content remained
readable; reclaim restored writes, final GC completed, and offline `e2fsck`
was clean. The VM, temporary image copy and staging files were destroyed, GARM
instances returned to zero and the observer remained healthy. The exact result
is digest-bound in `config/zot-v2.1.20-storage-audit.json`.

The accepted live authorization run then replaced both pilot users with four
repository/trust-scoped identities and activated the three exact namespaces.
From a disposable one-vCPU full VM on `gha0`, trusted, untrusted and promoter
each passed OCI CRUD only in their own prefix; release read promoted content
but received HTTP 403 for writes; all four identities received HTTP 403 for
cross-namespace reads; anonymous access returned 401; and host SSH was
unreachable. The VM, temporary image and volume were destroyed, the fleet
observer remained fresh, diagnostics stayed at 23/23 with zero pending or
failures, and ExamplePlatform/Captcha remained HTTP 200. The result is digest-bound in
`config/zot-v2.1.20-authz-audit.json`.

## Promotion gates

No runner receives credentials for a cache component until all applicable
gates below are true:

1. TLS validates against the cache CA inside a disposable worker;
2. anonymous, wrong-user and cross-namespace requests are denied;
3. repository/trust-specific credentials pass positive and negative tests;
4. restart, `SIGKILL`, host reboot, full-disk and GC tests converge;
5. free disk remains above 20 percent and cache paths do not intersect retained
   application storage;
6. artifact manifest, installed binary digests and service configuration are
   recorded against one repository commit;
7. for RustFS only, a stable promotion candidate or an explicit reviewed
   exception exists.

For Zot, the artifact, TLS, identity, destructive storage, VM authorization and
automatic reboot recovery are digest-bound and its component promotion flag is
true. The accepted boot started Incus before both bridge-bound caches, required
no manual cache start/restart, retained twelve legacy listeners plus
ExamplePlatform/Captcha, and converged to zero fleet instances, leases and orphans.
RustFS remains independently blocked because RC.1 is a pre-release. The
manifest's aggregate promotion flag therefore remains false even though Zot is
production-ready.

Cache outage is allowed to fall back to an uncached trusted build where policy
permits. It must never merge namespaces, expose root credentials or make a
release job consume untrusted writable state.

## Zot cache namespaces still carry the former repository owner

The repository moved to `NDDev-OpenNetwork` on 2026-08-10. The Zot ACL namespaces
(`cache/example-user/github-actions/...`) and the four `gha-zot-example-user-*`
identities deliberately did not move with it.

They are internal object-storage paths, not a GitHub identity: nothing resolves
them against the forge, and renaming them buys no behaviour. What renaming does
cost is real. `config/zot-v2.1.20-authz-audit.json` and
`config/zot-v2.1.20-reboot-audit.json` bind themselves to the SHA-256 of
`deploy/fleet-host/zot.json`, so editing that file invalidates both
records — including a reboot audit that can only be re-gathered by actually
rebooting a fleet host. Renaming without re-gathering would leave the evidence
describing a configuration that no longer exists while the tests still read
green, which is precisely the failure those digests exist to prevent.

Renaming them is therefore a separate change whose cost is re-running the Zot
authorization smoke and the post-reboot audit against the new configuration and
replacing both records with the observations that run produces.
