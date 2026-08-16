# ADR 0006: signed, immutable golden-image inputs

Status: accepted for the secure pilot on 2026-08-08.

## Context

An alias such as `ubuntu:24.04` or `latest` is operationally convenient but is
not a reproducible or auditable supply-chain input. A runner archive embedded
without independent verification can also make every job execute substituted
code. Conversely, placing JIT credentials or an already registered runner in
an image would clone identity across workers.

## Decision

Build the x86-64 worker from Canonical's exact Noble release directory
`release-20260801`. Authenticate its checksum file with
`/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg`, require signer fingerprint
`D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`, and require the pinned metadata
and disk SHA-256 values in the standard and integration manifests under
`config/golden-image*.yaml`.

Cache only the official `actions/runner` `v2.336.0` release archive after its
GitHub-published SHA-256 is verified. The cache contains executable bits and
dependencies but no `.runner`, `.credentials`, `.credentials_rsaparams`,
`.service`, token, URL or repository identity.

The byte-producing build recipe is content-addressed over the canonical
manifest, closed VM instance configuration, and embedded provision and
sanitation programs. The promotion smoke policy has a separate fingerprint
over that recipe, the runtime profile inputs and the smoke program. Tightening
a runtime assertion therefore forces revalidation without claiming that the
image bytes changed. Publish to an immutable version alias, resolve it to a full
Incus image fingerprint, boot a disposable smoke VM, and only then update the
operator-facing current alias. Retain the old current fingerprint as previous
before promotion. Never reuse a builder or smoke VM and never delete an image
as part of promotion.

Build the standard image on a manifest-pinned 16-GiB root volume and the
Docker integration image on a 24-GiB build volume. Trim free extents before
publishing, then run promotion smoke with the real 50-GiB standard or 70-GiB
integration profile. Each smoke must prove that cloud-init expanded the
filesystem to its runtime profile. This avoids materializing unused runtime
capacity in the LVM image cache while still testing the production disk shape.

The worker has no remote-login plane. The build purges `openssh-server`, masks
both SSH service and socket unit names, and the sanitation plus independent
smoke stages reject an installed server package, an active or unmasked SSH unit
or any TCP/22 listener. Incus Agent remains the host-controlled maintenance
channel and is not exposed to workflow code.

## Consequences

- GARM can copy a local verified runner cache instead of downloading the large
  archive for every worker.
- Image creation is slower than an unverified remote launch because it performs
  signature checks, a package update, sanitation and a second boot.
- The smaller immutable base reduces image import/cache I/O; per-pool root
  volumes expand from it during disposable instance creation.
- Debian packages are resolved at build time; the resulting sorted package
  inventory digest and final Incus fingerprint therefore form part of the
  runtime provenance. Exact bit-for-bit reproduction requires a future Ubuntu
  package snapshot mirror.
- Runner updates require a new manifest, immutable alias, smoke/canary evidence
  and promotion within GitHub's supported update window.
