# `server-example-legacy` control plane

This directory is the non-secret, reviewable deployment contract for the pilot.
It does not contain a GitHub credential, GARM secret, private key or RustFS key.

## Fixed topology

- GARM `v0.2.1-nddev.11` listens only on `127.0.0.1:9997`. The derivative is
  reproducibly built from upstream `v0.2.1`; it adds event-driven convergence,
  startup-state protection, one fsync-backed admission point at `JobAssigned`
  before desired capacity, request reconciliation before `AcquireJobs`, and
  durable JIT-registration cleanup before failed-instance teardown, plus an
  opt-in dynamic handoff of the official JIT blob to prebooted warm VMs.
  The official Actions runner and GARM protocol remain
  unchanged.
- `gha-fleet-gateway` listens with TLS on `192.0.2.1:9443` and proxies only
  enumerated worker callback and metadata routes.
- `gha-fleet-observer` exposes secret-free health, inventory and Prometheus
  metrics only on `127.0.0.1:9464`; it has no route outside loopback. Snapshot
  schema v6 exposes provider lifecycle, durable preemption accounting, bounded
  pre-`AcquireJobs` queue state and signed local/exporter deltas, including the
  90-second asynchronous diagnostic-export convergence window. A running queue
  intent without a durable execution lease is an explicit unhealthy coverage
  gap rather than a superficially healthy empty Incus inventory.
- GARM starts its required worker gateway, and the observer starts the warm
  timer that it evaluates. These reverse dependency edges make a narrow
  stop/start or restart converge to the same service inventory as boot; an
  operator cannot accidentally leave a callback listener or observed timer
  down while considering the rollout restored.
- `gha-diagnostic-exporter.timer` runs a short-lived, teardown-independent S3
  exporter against the dedicated RustFS canary bucket and writes only its
  secret-free journal under `/var/lib/gha-diagnostic-exporter`. PID 1 copies
  its reviewed configuration and three S3 inputs into the private credential
  mount and exposes the GARM spool only as a read-only `/run` bind view.
- The hardened provider talks to Incus only through mTLS on
  `https://127.0.0.1:8443`, in project `gha-fleet`.
- The provider accepts only the per-flavor immutable image fingerprints and
  numeric runner UID/GID recorded in `provider-incus.toml`, plus JIT-configured
  Linux/amd64 workers.
- GARM and the gateway run as distinct unprivileged system users. GARM receives
  `/dev/kvm` only so its admission probe can prove KVM availability; neither
  service receives the Incus Unix socket or host Docker socket.
- `/etc/gha-fleet` is a non-sensitive traversal root (`root:root`, `0755`);
  individual platform files remain `root:garm` `0640`, while the PKI subtree is
  `root:gha-gateway` `0750`. This lets the gateway reach only its own
  certificate material without joining the `garm` group.
- The public RustFS trust anchor is a singly linked `root:root` mode-`0644`
  file below `/etc/gha-fleet/trust`, whose parents are mode `0755`. It is kept
  outside the mode-`0700` cache PKI directory so `garm` can deliver the CA
  without gaining traversal access to cache service private keys.
- RustFS and Zot use distinct `gha-rustfs` and `gha-zot` identities, separate
  `/var/lib/gha-cache/{rustfs,zot}` trees and only the bridge listeners
  `192.0.2.1:{9002,5001}`. Both consume TLS/credential material through
  systemd credentials; neither joins `garm` or receives a host socket. Their
  units require and start after Incus, then use the bounded, secret-free
  `cache-network-ready` probe before binding to the Incus-owned `gha0` bridge.

The public GitHub control plane never reaches this host. Scale Sets use GitHub's
outbound listener protocol. The loopback webhook URL is deliberately inert and
webhook management is disabled.

## Secret rendering

`garm.toml.tmpl` has exactly two render tokens:

- `@GARM_JWT_SECRET@`: a random high-entropy JWT signing secret;
- `@GARM_DATABASE_PASSPHRASE@`: a random, exactly 32-character database
  encryption passphrase.

The rendered file is root-owned, group-readable by `garm`, and mode `0640`.
Generated secrets are never printed or copied back to the repository.

Cache root/admin credentials live only in `/etc/gha-fleet/secrets` and are
loaded by PID 1 into private credential directories. Jobs receive only
repository/trust-scoped non-root identities after the IAM negative tests pass.
The RustFS cache reconciler writes those non-root identity pairs only beneath
`/etc/garm/cache`, a `root:garm` mode-`0750` directory; every file is
`root:garm` mode `0640`, atomically created without replacement and absent from
the golden image and Incus configuration. Its public CA is copied separately
to `/etc/gha-fleet/trust/rustfs-ca.pem` as `root:root` mode `0644`; never make
the private `/etc/gha-fleet/pki/cache` directory traversable to `garm`.
`zot.json` is default-deny and intentionally omits all extensions.
RustFS receives an independent random RPC authentication credential through
the same private mechanism. The root-owned launcher rejects a default value or
reuse of the S3 root secret, exports it only to the RustFS process and then
replaces itself with the pinned server binary.

The Incus client private key is mode `0640`, owned by `root:garm`, so the GARM
process can read but cannot rewrite it. Its certificate is registered in Incus
as a restricted client scoped to project `gha-fleet`. The
gateway private key is mode `0640`, owned by `root:gha-gateway`. A dedicated
internal CA signs a server certificate whose SAN contains only `192.0.2.1`.
That CA is installed in GARM controller metadata so cloud-init can validate the
gateway before sending its instance JWT.

## Initial rollout

The first runner pilot enabled `nddev-linux-standard` and then the separately
imaged `nddev-linux-integration`, both with GARM idle runners fixed at zero and
one-VM ceilings. The unregistered warm controller is installed independently;
`/etc/garm/warm-pool.env` contains only the stable, non-secret
`GARM_CONTROLLER_ID`, is `root:garm` mode `0640`, and the timer is safe at a
zero target. The standard warm target remains zero until image build/readiness,
one-job teardown and replenishment gates pass. Fast/release profiles remain
disabled until their own gates pass.
Existing persistent runners remain active throughout the pilot; ExamplePlatform and
Captcha are outside this deployment boundary.

Every installed binary is compared with its recorded SHA-256 digest before a
service starts. A deployment records:

```text
repository commit -> controller/provider/gateway digest -> GARM release digest
-> image fingerprint -> platform fingerprint
```

The provider is built with Incus Go SDK `v7.3.0`; deployment must complete a
read-only live compatibility probe against the host's Incus 6.0 LTS API before
the first pool is enabled.

The GitHub endpoint is created with a dedicated GitHub App. A broad PAT is not
an accepted production substitute; infrastructure can be installed and tested
without enabling a Scale Set until that App installation exists.
The loopback-only registration and exact-scope verification procedure is
implemented by `gha-fleet bootstrap-github-app` and documented in
`docs/github-app-bootstrap.md`. Its output is one-time staging material: GARM
encrypts the imported key at rest, after which every exact staging copy is
deleted.

`config/garm-credential-anchor.json` is the retained non-secret identity for
that imported credential. Install it at
`/etc/gha-fleet/garm-credential-anchor.json` as `root:garm`, mode `0640`.
Subsequent pool reconciliation validates the existing App ID, installation ID
and public-key SHA-256 through this anchor; it does not recover or restage the
private key. Only initial credential creation accepts `--app-bundle`.

The same binary owns `reconcile-garm`. It talks only to GARM's loopback API,
creates a disabled pilot with both runner and guest update locks, and refuses
to enable it until the complete returned resource matches the reviewed
contract. This path is mandatory because pinned `garm-cli v0.2.1` omits the
runner `disable_update` create field. It accepts exactly the standard and
integration Scale Set names; standard remains the default, while integration
must always be selected with `--scale-set nddev-linux-integration`.

The observer and provider both parse the provider configuration. Whenever that
schema changes, install both binaries from the same repository commit and
require loopback `/healthz` to return HTTP 200 before any Scale Set apply or
enable operation. A running GARM process does not make mixed-version local
consumers an acceptable rollout.

Cache services are independently gated. `config/cache-artifacts.yaml` records
Zot v2.1.20 as production-ready after its TLS/IAM/storage/reboot evidence, while
RustFS RC.1 remains canary-only and production-blocked. Component promotion
does not itself distribute a credential to a runner; workflow rollout remains
an explicit trust-scoped change.

Diagnostic export is a narrower canary inside that gate. Install
`config/diagnostic-exporter.yaml`, the exporter service/timer and all four
systemd credentials (the root-owned config, access key, secret key and CA),
then run `rustfs-diagnostic-exporter-bootstrap.sh` before enabling the timer.
The unit hides `/etc` and all GARM/cache state, then binds only the diagnostics
spool into `/run/gha-diagnostic-exporter-source` read-only. The bootstrap
creates only the exact bucket, prefix-only identity, 1 GiB quota and seven-day
lifecycle rule; it proves allowed PUT/GET/HEAD and denied delete, cross-prefix,
cross-bucket, list/location and anonymous access. Run the exporter once manually and require
source/exported bundle and byte counts to match before replacing the observer
binary. RustFS failure must leave the local spool and VM teardown behavior
unchanged.
Exporter config schema `v3` accepts exact sorted repository, account and pool
allowlists, requires manifest pool and Scale Set equality, and derives each
immutable object namespace from the verified manifest identity. Unknown
repositories and accounts fail closed. Keep each deployed host's pool allowlist
no broader than the Scale Sets that host actually serves.

Install this directory's `otelcol-fleet.yaml` only on execution hosts that run
the loopback fleet observer, with host-specific values loaded from
`/etc/gha-fleet/otelcol-fleet.env`. The telemetry backend uses
`deploy/services-host/otelcol-fleet.yaml` and its matching unit: that role
retains OpenObserve and collector journald ingestion but has no observer scrape
or fleet-metrics pipeline. Validate the selected file with the pinned collector
binary and its host environment before restarting the service.

These units are the fleet-host contract rather than one host's copy: the same
service, sysusers, tmpfiles, timer and provider files are what `gha-runner-1`
and `gha-runner-2` run. The directory keeps this name because the deployed
hosts were provisioned from these paths. Only `otelcol-fleet.yaml` here is
specific to this host.
