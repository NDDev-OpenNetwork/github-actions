# ADR 0008: bounded diagnostics before worker teardown

Status: accepted for implementation

## Context

An ephemeral runner VM is the correct security boundary, but destroying it also
destroys the evidence needed to explain bootstrap, cancellation and runner
failures. GitHub log masking does not apply to host-level console, cloud-init or
runner diagnostic files. Keeping a failed VM indefinitely would preserve that
evidence at the cost of capacity, isolation and disk safety.

## Decision

The Incus provider captures a bounded diagnostic bundle after it has proved
that the instance belongs to its controller and before it stops the VM. The
collector reads only these allowlisted sources:

- the Incus container-console log when the instance type supports that API, and
  host-side Incus instance logs such as the full-VM `qemu.log`;
- `/var/log/cloud-init.log` and `/var/log/cloud-init-output.log`;
- regular `.log` files immediately below
  `/home/runner/actions-runner/_diag`.

It never exports runner credential files, cloud-init source data, workflow
workspaces, process environments, arbitrary guest paths or complete Incus
configuration. Stable ownership and image fields are copied into the manifest
through an allowlist; `user.user-data` is excluded.

Collection has a 15-second teardown budget, a 2-MiB limit per artifact, at most
32 artifacts and a 16-MiB uncompressed bundle budget. Known credential forms
and authorization headers are redacted before persistence. Bundles are written
atomically as mode `0600` tar-gzip files below a private provider-owned
directory. SHA-256 values in the manifest cover the redacted bytes actually
stored.

Diagnostic failure is recorded in provider logs but does not block instance
destruction. This is deliberate: the lifecycle permits a bounded diagnostics
failure transition to teardown, while leaked executable state is not an
acceptable debugging mechanism.

The collector does not call Incus's container-only console endpoint for a full
VM. VM boot evidence comes from the host-side QEMU log and guest-agent reads;
an expected `Instance is not container type` response must never be recorded as
a collection failure.

The pilot keeps at most 1 GiB and seven days of matching diagnostic bundles.
Garbage collection examines only regular files whose names match the versioned
bundle grammar. It never follows symlinks or crosses into RustFS, Zot,
ExamplePlatform/Captcha or legacy-runner storage.

## Consequences

- GARM retries and cancellations retain useful evidence without retaining the
  worker.
- Logs remain sensitive even after redaction and require root/operator access.
- The local directory is a durable spool, not the final observability backend.
  A later trust-scoped RustFS exporter may upload encrypted bundles only after
  cache IAM, TLS, retention and negative tests pass.
- Runtime acceptance must prove successful, partial and timed-out collection,
  unchanged teardown latency bounds, mode/ownership, retention and zero orphan
  VMs.
