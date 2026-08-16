# ADR 0017: Separate RustFS scope for unassigned warm diagnostics

## Status

Accepted after production rollout fault discovery.

## Context

An unregistered warm VM has deliberately empty repository identity until GARM
admits a job. Draining such a VM still captures useful host-owned boot and
runner diagnostics before deletion. The original RustFS exporter accepted only
bundles whose repository matched its configured repository, so this valid
pre-assignment bundle failed verification. The exporter failure marked systemd
degraded, and host admission correctly stopped warm replenishment.

Deleting, weakening verification for, or pretending that this bundle belonged
to a repository would lose evidence or collapse two trust scopes.

## Decision

The exporter classifies exactly two bundle scopes:

1. repository-bound: repository equals the configured owner/repository and the
   pool ID is not a warm-pool identity;
2. unassigned warm: repository is empty and pool ID is exactly
   `warm/<pool-name>`.

Both scopes still require an allowlisted pool and exact pool/Scale Set equality.
Every ambiguous combination fails closed. Repository bundles retain their
existing object keys. Unassigned warm bundles use the separate prefix:

```text
diagnostics/v1/unassigned-warm/trust/<trust>/platform/<os>/<arch>/pool/<pool>/...
```

The object remains content-addressed and is subject to the same ownership,
archive, manifest, artifact digest, TLS, IAM, retention and confirmation checks.

## Consequences

Draining a clean warm VM preserves its diagnostics durably without assigning a
fake repository identity. Warm and job diagnostics cannot collide or share an
object namespace. A failed exporter remains a host-health blocker; recovery
must repair or explicitly classify the evidence rather than bypass admission.
