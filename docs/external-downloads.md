# External download contract

Every network-bearing bootstrap surface is classified here. A repository test
fails when a new shell or provider file containing `curl` or an HTTPS locator
appears outside this inventory.

| Surface | Classification | Resilience and authority |
| --- | --- | --- |
| `internal/imagebuild/artifacts.go`, `config/golden-image*.yaml` | image materialization | Exact HTTPS host/path and SHA identities; size bounds; transient transport/read and HTTP 408/429/5xx receive at most three attempts. Verified bytes are baked once into the immutable worker image. |
| `actions/tool-cache/tool-cache.sh` | per-job standalone tool | Trust-scoped immutable RustFS object first; size and SHA reverified; unavailable, missing, incomplete or corrupt cache falls back to exact upstream with three total attempts and emits `nddev_tool_cache_event`. |
| `scripts/install-benchmark-toolchain.sh` | representative benchmark fallback | Exact preinstalled version exits without download. Hosted/cold fallback uses fixed URLs, SHA-256 and three total attempts; this script is benchmark evidence, not a normal fleet start hook. |
| `.github/workflows/ci.yml` | public self-CI | GitHub-hosted `setup-go`; private fleet consumers instead use the baked toolchain paths published by `ci-workflows`. |
| `actions/package-cache/package-cache.sh`, `scripts/configure-sccache.sh` | private package/cache data | Authenticated repository-scoped S3-compatible cache traffic; never an executable download authority. Cache failure degrades to the package manager or upstream path. |
| `internal/garmproviderincus/provider/{incus.go,specs.go,admission.go,cache_delivery.go}` | VPC-local runner bootstrap | One-use instance identity and pinned runner metadata from the fleet gateway; install-script fetches use three total attempts. No arbitrary external tool origin is accepted. |
| `scripts/build-garm-nddev.sh` | reviewed source build | Fetches the exact reviewed upstream commit and applies the checked-in derivative patch set; not executed in a job start hook. |
| `internal/imagebuild/assets/{provision.sh,container-provision.sh}` | local image provisioning | Talks only to the guest-local Incus socket or installs already-downloaded, verified image inputs. |
| `internal/imagebuild/assets/{smoke.sh,smoke-integration.sh}` | reachability smoke | HTTP requests assert egress, service-container and metadata isolation. Returned bytes are never executed or promoted. |

The correctness rule is uniform: cache/image state is an optimization, never
provenance. A miss or outage may increase duration but cannot skip checksum,
signature, size, source or test obligations.
