# Representative benchmark fixtures

These fixtures keep the Phase 0 comparison stable while exercising the five
workload classes selected in the rollout plan:

| Workload | Dependency phase | Build phase | Test/integration phase |
| --- | --- | --- | --- |
| Go | `go mod download` | repository-wide compile | repository-wide tests |
| Rust | locked crates.io fetch | release build with Rayon/Serde/SHA-2 | unit tests |
| Python/uv | frozen `uv sync` | wheel and sdist build | pytest |
| Bun/Next | frozen Bun install | production Next build | Bun tests |
| Docker | digest-pinned base pull | BuildKit image build | container and Compose health |

The fixtures are deterministic application work, not microbenchmarks and not
production artifacts. Their lockfiles are committed. Every workflow action and
container base is pinned by commit or digest. The benchmark workflow is manual,
read-only, secret-free and has no deploy permission. Its `representative-v1-*`
GitHub cache keys, plus the Docker fixture's `representative-v2-*` key, are
isolated measurement namespaces and are never inputs to release or production
builds.

The Docker fixture's benchmark-only cache transport is a mode-`min` inline
cache embedded in an image archive and moved between disposable workers with
`actions/cache`. This works with the default Docker driver even when its
containerd image store is disabled. It does not replace the production Zot
registry-backed BuildKit cache described in the cache-plane design.

Repository policy permits only GitHub-owned actions. Rustup, uv and Bun are
therefore installed by the in-repository toolchain installer from exact HTTPS
release URLs with hard-coded SHA-256 verification; no third-party setup action
receives the workflow token.

The `github-hosted` environment resolves to `ubuntu-24.04`. The `nddev`
environment resolves Go/Rust/Python/Bun jobs to `nddev-linux-standard` and the
Docker job to `nddev-linux-integration`. This keeps the OS generation aligned
while preserving the integration worker's VM-local Docker boundary.

`cache_mode=cold` disables dependency-cache restore/save. `cache_mode=warm`
uses an environment- and lock-specific GitHub Actions cache key. Prime one warm
run, exclude that miss from the sample, then collect 20 cold and 20 cache-hit
runs per environment. Dispatch runs sequentially for isolated latency; a later
saturation gate measures queue fairness separately.

Step timestamps from the GitHub Jobs API are authoritative for checkout,
toolchain setup, dependency, build, test and artifact-upload durations. Each
job also uploads a small JSON record with the cache result, bounded network RX
delta, exact toolchain, commit and a hashed machine identity. Network RX is a
VM-wide diagnostic signal, not protocol-level dependency accounting.

## Evidence collector

`gha-benchmark collect` turns one completed run into one strict, ordered JSON
record. It requires `Actions: read` access through `GH_TOKEN` or
`GITHUB_TOKEN`; if both variables exist they must contain the same identity.
The token is used only for the GitHub API and is never emitted. The collector:

- requires the exact manual workflow, successful run SHA and run attempt;
- requires five successful jobs, expected labels, complete phase timestamps
  and five distinct runner names;
- verifies every artifact name, declared size and SHA-256 before opening it;
- accepts one bounded `result.json` per ZIP and rejects unknown JSON fields;
- requires coherent environment/cache/iteration fields across all workloads;
- requires five unique hashed machine identities for NDDev disposable VMs;
- downloads the signed artifact URL without forwarding GitHub authorization.

Collect evidence before the one-day artifacts expire:

```bash
GH_TOKEN="$(gh auth token)" go run ./cmd/gha-benchmark collect \
  --run-id RUN_ID
```

The implementation follows GitHub's versioned
[workflow-run/job REST API](https://docs.github.com/en/rest/actions/workflow-runs)
and the documented one-minute redirect flow for
[artifact downloads](https://docs.github.com/en/rest/actions/artifacts).

## Recorded pilot

The six-run protocol pilot from 2026-08-09 is preserved in the
[Phase 0 evidence report](../docs/benchmark-phase0.md) and its
[machine-readable record](evidence/phase0-pilots-2026-08-09.json). The pilot
proved artifact collection, cache miss/hit classification and disposable NDDev
worker uniqueness. It also measured queue serialization and showed that remote
GitHub cache transfer is not the intended local cache plane.

These runs are pilots only. The warm primes are excluded from the pilot
comparison, and all six runs are excluded from the later 20+20 statistical
series. No median, p95 or production speedup claim is derived from them.

## Sampling protocol

1. Merge the harness before dispatching it; `workflow_dispatch` is evaluated
   from the default branch.
2. Run one cold pilot in each environment and reject the protocol if any job,
   artifact or Jobs API phase is incomplete.
3. Run one warm prime for each environment. It must report cache misses and is
   excluded from the sample.
4. Run `cold-01` through `cold-20`, then cache-hit `warm-01` through `warm-20`,
   sequentially within each environment. Do not overlap environments during
   the isolated-latency series.
5. Preserve run IDs and collect Jobs API timestamps before the one-day artifact
   retention expires. Report failures separately from application failures.

Example pilot dispatch:

```bash
gh workflow run representative-benchmark.yml --ref main \
  -f environment=github-hosted -f cache_mode=cold -f iteration=pilot-01
```

The harness is complete when local frozen fixture builds and its contract tests
pass. Phase 0 is complete only after the required measurements are collected
and reviewed; merging this harness alone does not satisfy the gate.
