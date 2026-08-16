# ADR 0010: canary export of teardown diagnostics to RustFS

Status: accepted for canary implementation

## Context

One-job VMs are destroyed immediately after bounded diagnostic collection. The
private manager-host spool preserves evidence through teardown, but it remains
in the same physical failure domain as the runner host. RustFS is the selected
S3-compatible object plane, but the pinned `1.0.0-rc.1` release remains a
pre-release and its lifecycle implementation is still a canary gate.

## Decision

A separate one-shot Go exporter runs from a systemd timer. It is not part of
GARM or the provider teardown path. Each run:

1. opens only versioned mode-`0600`, single-link bundles in the mode-`0700`
   GARM spool, without following symlinks;
2. revalidates gzip/tar structure, the bounded manifest, every artifact size
   and SHA-256, the exact repository identity, equality of pool and Scale Set,
   and membership in a reviewed, sorted pool allowlist;
3. derives an immutable object key from repository, trust, platform,
   architecture, the verified manifest pool, capture date and the compressed
   bundle SHA-256;
4. performs HEAD-before-PUT, an `If-None-Match: *` PUT with SHA-256 checksum,
   and a required HEAD confirmation;
5. persists only a secret-free, fsync-backed local journal and status record;
6. never deletes or mutates a source bundle.

The client uses the official AWS SDK for Go v2 with `BaseEndpoint`, forced
path-style S3 addressing, SigV4, the private cache CA, TLS 1.3, no environment
proxy and no redirects. Credentials arrive only through the exporter unit's
systemd credential mount. The reviewed non-secret configuration is also copied
there by PID 1, so the process does not need a readable `/etc`.

The canary IAM identity has exactly `GetObject`/`PutObject` below
`diagnostics/v1/`; it cannot list buckets or objects, delete objects, write
outside the prefix or access another bucket. Bootstrap replaces the user's
direct policy mapping rather than appending to it. The bucket has a 1 GiB hard
quota and a seven-day lifecycle rule, but local retention remains authoritative
until RustFS lifecycle and restart/recovery gates pass.

Configuration schema `v2` currently allows exactly `nddev-linux-integration`
and `nddev-linux-standard`. The allowlist is not a wildcard: adding fast,
release or untrusted pools requires a reviewed config change, and release or
untrusted diagnostics may require a distinct trust/IAM boundary.

The exporter is intentionally a short-lived root process with only
`CAP_DAC_READ_SEARCH`: this lets it read the private GARM spool without sharing
GARM's Unix identity or widening bundle modes. PID 1 exposes only the spool as
a read-only bind at `/run/gha-diagnostic-exporter-source`; the mount namespace
hides all of `/etc`, the original GARM/fleet/cache state trees and host sockets.
Its only writable path is its state directory, and its only permitted IP is the
RustFS bridge address.

The observer samples the local spool and the exporter's atomically published
status concurrently every 15 seconds, while the exporter timer runs every
minute with up to five seconds of jitter. A newly published immutable bundle
can therefore be visible to only one source during a normal sample. Snapshot
schema v2 represents that condition explicitly as `convergence-grace` instead
of producing a transient false-negative:

- the exporter status must itself be valid, successful, non-pending and no more
  than 90 seconds old;
- local bundle and byte deltas must move coherently in the same direction;
- signed deltas and remaining grace seconds remain visible in the snapshot and
  Prometheus metrics;
- pending or failed exporter state, mixed-sign or same-count byte divergence
  and any mismatch older than 90 seconds remain immediately or eventually
  unhealthy.

The pre-existing three-minute maximum status age still detects a stopped timer
when the spool is unchanged. The narrower 90-second mismatch budget detects a
stopped or non-converging exporter sooner when new diagnostics exist.

## Consequences

- a RustFS outage cannot delay or prevent VM teardown;
- GARM and the Incus provider never receive RustFS credentials;
- content collisions, corrupt local bundles and incomplete remote writes fail
  closed and remain pending for retry;
- the loopback fleet observer treats stale, failed, pending, incoherently
  divergent or grace-expired export status as unhealthy and exposes bounded
  aggregate metrics;
- this does not promote RustFS to production or make it the sole diagnostic
  record. Promotion still requires release, IAM, lifecycle, quota, restart,
  recovery and sustained workload gates.
