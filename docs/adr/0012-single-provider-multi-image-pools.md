# ADR 0012: One provider ownership domain with per-pool images

- Status: accepted
- Date: 2026-08-08

## Context

The standard worker is deliberately Docker-free while the integration worker
contains a private Docker daemon. GARM identifies both through one external
provider name but passes an image alias and a flavor for each Scale Set.

Running a second provider configuration against the same Incus project would
split reconciliation and admission ownership. Separate processes could observe
the same instances while maintaining independent capacity journals, creating a
race at exactly the lifecycle boundary that must remain idempotent.

The upstream cloud-config library also grants the runner a broad default group
set containing both `docker` and `lxd`. That membership is wider than either
NDDev worker class requires.

## Decision

Use one `nddev-incus` provider, one controller ID and one admission journal for
all local pools. Its root-owned configuration contains a strict
`worker_images.<flavor>` map. Each entry pins:

- one local Incus alias;
- one immutable SHA-256 fingerprint;
- either the `standard` or `integration` image variant.

Provider startup rejects mappings to unknown platform pools and rejects a
variant that disagrees with the pool's Docker capability. Probe and create
paths both resolve the alias and verify its fingerprint and variant. Instance
ownership tags record the selected alias, fingerprint and flavor; adoption,
restart and admission reconciliation derive the expected image from that
flavor.

Before the official runner starts, provider-owned cloud-init replaces all
supplementary groups. A standard runner receives exactly `sudo`; an integration
runner receives exactly `sudo,docker`. Both paths assert that `lxd` is absent.

## Consequences

- Standard and integration jobs share resource accounting and orphan cleanup.
- A flavor cannot silently select another pool's image.
- Docker access exists only inside the disposable integration VM.
- Adding a future image class requires an explicit policy/config change and
  canary, not another controller racing over the same Incus project.
- A new immutable alias is built and smoked with `reconcile-image --apply
  --stage-only`; `current` moves only in the bounded provider activation window.
- A provider/config/platform upgrade is deployed atomically while the managed
  instance inventory is empty.
