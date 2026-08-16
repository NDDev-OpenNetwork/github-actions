<!--
GENERATED FILE - DO NOT EDIT DIRECTLY
generator: gds
bundle: 0.4.0-dev
source-tree-digest: sha256:f9f9787082b9f3b25ba11ec267f28c58a5d00a61e3a59c77a868756f360a901c
input-digest: sha256:51828487bd1128260a8feb5dead1dfc51f30c11b5fb209ccc57cbb459709d4d6
output-digest: sha256:e36a2321b01c15acf05c6ee58e33f868a6641f472d6dd0ae822660fbc845882f
edit-source:
  - .gds/repository.yaml
  - policies/base/repository-default.yaml
  - policies/owners/organization-default.yaml
  - policies/repositories/github-actions.yaml
  - templates/agents/repository.md.tmpl
  - templates/github-actions/go.yml.tmpl
  - templates/harnesses/claude.md.tmpl
-->
# Repository brief

Secure, disposable GitHub Actions capacity for NDDev. Every job receives a fresh Incus/KVM virtual machine that runs GitHub's official actions/runner exactly once, exports its diagnostics, and is destroyed. This repository owns the policy, admission, lifecycle and integration code around GARM and Incus; it does not reimplement the Actions protocol.

## What it does

- One job per immutable full VM; an executed worker is never reused or returned to a pool
- Pre-booted warm VMs hold no runner registration or repository identity until one job claims them
- Durable admission before GitHub job acquisition, with a global budget, per-repository quota, priority aging and weighted stride fairness
- An fsync-backed cross-process journal binds one job attempt to one VM and reconciles against observed Incus state
- Digest-pinned golden images with baked Go, Rust, Bun and uv toolchains and current/previous rollback aliases
- Trust-scoped RustFS compiler cache and a minimal Zot OCI registry, delivered per job and never baked into an image
- Bounded, redacted diagnostics exported outside the VM before teardown
- Loopback fleet observer and an OpenTelemetry collector exporting into a dedicated OpenObserve instance
- In-tree GARM and Incus-provider derivatives, reproducibly rebuilt from pinned upstream commits and patch digests

## Where to change what

- Fleet policy, pools, host reserve and cache manifests — `config`
- Typed platform configuration and its fail-closed validation — `internal/config`
- Host capacity, health and cold-pilot admission decisions — `internal/hostprobe`
- Central queue admission before GitHub job acquisition — `internal/queueintent`
- Worker state machine and job-attempt identity — `internal/lifecycle`
- Incus provisioning, warm claims and direct-JIT activation — `internal/garmproviderincus/provider`
- Durable provider journal, leases and reconciliation — `internal/providerjournal`
- GARM credential, entity and Scale Set reconciliation — `internal/garmbootstrap`
- Organization GitHub App bootstrap and verification — `internal/githubappbootstrap`
- Golden image build, manifest and rollback aliases — `internal/imagebuild`
- RustFS cache identities and Zot credentials — `internal/rustfscache`
- Pre-teardown diagnostic export — `internal/diagnosticexport`
- Loopback observer and fleet metrics — `internal/fleetobserve`
- Systemd, sysusers and tmpfiles deployment contracts — `deploy`
- Operator CLI: validate, preflight, reconcile, render — `cmd/gha-fleet`
- GARM derivative patches, overlay and reproducible build — `third_party/garm`

## How to verify

- Lint: `go vet ./...`
- Test: `go test ./...`
- Build: `go build ./...`

## Working here

- Generated files carry a `GENERATED FILE` header. Change the canonical input
  named in `edit-source` and regenerate; editing the output detaches it from
  `.gds/bundle.lock.yaml`.
- One Git repository is one mutation boundary. Work that crosses repositories
  starts with `gds context --json`; work inside this one does not need it.
- Provider writes go through plan → approve → apply and are journaled.
- Task-specific procedures live in `skills/canonical/<name>/SKILL.md`; the
  profiles active here are `core`. Load one when the task
  matches it.

## Facts

- Repository `repo_01KZFE1YZCB0GY6BQGBVGB49W9`, roles `project, module`, bundle `0.4.0-dev`.
- Canonical inputs: `.gds/repository.yaml`; compiled result: `.gds/compiled-policy.json`.
- Visibility `private`, data `private`.
