# ADR 0019: run operator warm drain as the GARM identity

Status: accepted

## Context

The provider journal and retained diagnostic spool are single-writer assets
owned by `garm:garm`. Running the operator-only `warm-drain` command through
plain `sudo` caused its atomic replacements to become `root:root`. The observer
and exporter correctly failed closed, but admission remained unavailable until
the ownership metadata was repaired.

## Decision

Provider `v0.1.5-nddev.8` rejects `warm-drain` when its effective UID is zero.
Operators must invoke it through `sudo -u garm`. Required identity arguments are
validated before the privilege check, and the privilege check happens before
configuration or mutable state is opened.

Systemd-managed provider, warm-pool, observer and exporter processes retain
their existing dedicated service identities. The restriction applies only to
the manual destructive drain command.

## Consequences

Atomic journal replacement and diagnostic capture preserve their ownership
contract during release transitions. A mistaken root invocation is rejected
before mutation with a stable diagnostic. Recovery no longer relies on a later
metadata-only `chown` operation.
