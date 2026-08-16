# Artifacts and how they are pinned

Every shipped binary and image, and the one place each is declared.

## The pattern

A manifest under `config/` is the authority; a Go package under `internal/` is
its typed reading; a contract test binds every consumer to it. Where that pattern
was missing, the value drifted -- twice, observably, in production.

| Artifact | Manifest | Package |
| --- | --- | --- |
| GARM derivative | `config/garm-derivative.yaml` | `internal/garmderivative` |
| Incus provider derivative | `config/provider-derivative.yaml` | `internal/providerrelease` |
| Worker images | `config/golden-image.yaml`, `-integration.yaml` | `internal/imagemanifest` |
| Cache plane | `config/cache-artifacts.yaml` | `internal/cachemanifest` |
| Telemetry | `config/telemetry-artifacts.yaml` | `internal/telemetrymanifest` |
| Cache identities | `config/rustfs-cache-identities.yaml` | `internal/rustfscache` |
| Fleet contract | `config/fleet-contract.yaml` | `internal/fleetcontract` |

## GARM derivative

Five patches and two overlays over a pinned upstream commit, built in a
digest-pinned golang container with no network, twice, and compared to an exact
binary digest. `scripts/build-garm-nddev.sh` carries a **generated region**
rendered from the manifest by `make garm-derivative-script`; nothing outside that
region may restate a manifest value.

`garmderivative.FieldDispositions()` requires every manifest field to be either a
shell assignment the build reads or a stated reason the build does not read it.
This exists because the contract test once enumerated four of five patch digests,
so the fifth could hold any value and the test still passed.

The build asserts, inside the container, the toolchain release, `GOOS`, `GOARCH`
and `CGO_ENABLED`, and measures the artifact's glibc requirement against the
manifest's ceiling.

## Provider derivative

The version was written in four places and bound to none, and two production
hosts ran different binaries that both reported `v0.1.5-nddev.30` -- built from
`ad8efaa` and `cae2d18`, a tenancy enforcement change apart (#263).

Now: the Makefile derives the stamp from the manifest, admission compares the
platform policy against the binary's own `Version`, and an unstamped build
reports `v0.0.0-unknown` and refuses every policy.

**A provider version names a release, not a build.** There is no reproducible
binary digest for it, unlike GARM. The deployed artifact is identified by the
commit its `version` subcommand reports.

## Images

`golden-image.yaml` and `golden-image-integration.yaml` share source, runner,
sccache and toolchains and differ in Docker capability. Parity is maintained by
hand, but no edit lands silently: `TestToolchainImageStageAudit` binds each
manifest's fingerprint to the image built from it, and the runner version is
cross-checked against the platform config.

## Provenance table

`docs/upstream-baseline.md` is what an operator reads to check a binary. Its rows
are bound to the manifests by `internal/repositorycontract`, because the GARM row
once carried a digest from two releases earlier.
