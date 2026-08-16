# Changelog

All notable changes are documented here. The project follows Semantic
Versioning.

## [0.1.1] - 2026-08-16

First release of the NDDev GitHub Actions fleet as an open-source control plane
under `github.com/NDDev-OpenNetwork/github-actions`. The version line starts
here: this is a new module path, so the numbering of the private predecessor it
grew out of resolves to releases no tag here can name. The repository ships
example host and tenant configuration; a real estate is supplied at deploy time
and is never vendored here.

### Added

- **Disposable full-VM workers** on Incus, one job per machine, created and
  destroyed around a single GitHub Actions job. A worker never outlives the job
  it was made for, so nothing a job leaves behind can reach the next one.
- **A GARM derivative** built from a pinned upstream release plus reviewed
  patches and overlays, each bound by digest in `config/garm-derivative.yaml`,
  so what runs can be reproduced from what is declared.
- **A central queue with admission control**: per-repository shares, priority
  aging, a weighted stride scheduler and a durable single-writer journal under
  `flock`, so capacity is allocated deliberately rather than by arrival order.
- **A restricted worker gateway**, the one component permitted to cross from the
  worker network to the queue. A worker may reach the public internet and its
  own bridge, nothing else; the gateway forwards a narrow, uncredentialed route
  and refuses anything that would make the hop ambiguous.
- **A per-host content cache** (RustFS, S3-compatible) with trust-scoped
  namespaces, so a job's cache writes cannot reach another trust class.
- **Multi-tenant service** from a closed tenant registry: several GitHub
  accounts share one queue, and the provider boundary is the account a scale set
  belongs to.
- **Telemetry and diagnostics** that stay on the private network by contract,
  with redaction applied before anything is written.

### Notes

The fleet is not a general-purpose scheduler or a container runtime. It answers
one question — how a GitHub Actions job gets a clean machine and gives it back —
and it is deliberately opinionated about isolation over reuse.
