# ADR 0022: Pinned sccache client and trust-scoped namespaces

Status: accepted for canary and benchmark use on 2026-08-09; immutable-image
runtime proof and RustFS production promotion remain independent gates.

## Context

ADR 0021 delivers a one-job RustFS identity and a repository/trust prefix root
without choosing compiler-cache semantics. Installing a cache client during a
workflow would put an unpinned network download in the job hot path, weaken
image provenance and make cold and warm measurements incomparable. Reusing one
mutable namespace across trust classes, toolchains or dependency graphs would
also permit cache poisoning even when IAM correctly limits the repository.

RustFS `1.0.0-rc.1` remains a pre-release canary. Client delivery and namespace
correctness can therefore be tested without treating the object service as a
production dependency.

## Decision

Both standard and integration golden-image manifests pin the official Mozilla
`sccache v0.17.0` Linux amd64 musl release to:

- source commit `c037e117c7625a2668633574028a6addf2a96a6e`;
- archive `sccache-v0.17.0-x86_64-unknown-linux-musl.tar.gz` with SHA-256
  `67c4a96dd237c1f518f6b36083f270f9976d516f1e57fce891755ea782e50006`;
- installed binary SHA-256
  `066c5a84c85044c8f48b3ab571ac114293ea717c3d36985db022af8206e21e63`.

The image builder downloads only that exact HTTPS release URL, verifies the
archive before guest delivery, re-verifies it in the builder VM, permits only
the expected directory, binary, licence and README archive entries, extracts
only the binary without inherited ownership or permissions, and verifies the
installed binary digest and version. Image properties, build records and smoke
results bind the same version and digests. No compiler-cache credential enters
the image.

`scripts/configure-sccache.sh` consumes the one-job environment and constructs
the final key prefix as:

`repository/trust/{trusted|promoted}/linux/amd64/toolchain/lock-digest/ref-class`

Trusted branch, merge-queue, nightly and benchmark jobs may use the trusted
writer in read/write mode. Release jobs must use the release reader, the
promoted prefix and sccache read-only mode. The adapter validates the endpoint,
bucket, region, role, prefix, lock file and baked client version before writing
only derived non-secret settings to `GITHUB_ENV`; credentials remain in the
official runner's already-masked one-job environment.

The representative Rust benchmark uses local sccache only for NDDev warm runs,
records upstream JSON statistics, fails on cache errors and reports observed
hits. The GitHub-hosted baseline retains `actions/cache`, so the comparison
does not silently require the private RustFS plane.

## Consequences

- Rust compilation no longer downloads a cache client inside each job.
- Cache keys cannot collide across repository trust, platform, architecture,
  toolchain, dependency-lock digest or ref class.
- Release jobs cannot write cache entries, even if workflow code changes the
  suffix beneath the promoted namespace.
- RustFS remains canary-only until an independently reviewed stable artifact
  and the statistical speedup, hit-rate and reliability gates pass.
- Go, uv and Bun caches require separate measured adapters; BuildKit continues
  to use the Zot-backed OCI/registry-cache plane rather than generic S3.
