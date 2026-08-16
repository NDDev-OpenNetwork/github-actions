# ADR 0030: Baked worker toolchains

- Status: accepted
- Date: 2026-08-10

## Context

The first strict sccache series proved the compiler cache works and does not
deliver the required speedup. A cold Rust sample took 39.906 seconds and the
warm sample took 38.071 seconds, a factor of 1.048, while RustFS served all 57
compile requests with zero misses and zero cache errors. A 100 percent hit rate
that produces a 1.048x speedup means compilation is no longer the dominant cost
of the job.

Two costs remain, and they are independent:

1. every job downloads and installs its language toolchain, because a worker VM
   is disposable and the image carried only the official runner and sccache;
2. every job re-resolves its dependencies, because `CARGO_HOME` lives under
   `RUNNER_TEMP` and the Actions-level dependency cache restore in the
   representative workflow is deliberately GitHub-hosted only.

This decision addresses the first cost only. The second is a separate change
with a different mechanism, and bundling them would make the measurement
unattributable.

## Decision

Both managed worker images bake the exact toolchains the representative
workloads use: Bun 1.3.14, Go 1.26.5, Rust 1.97.1 and uv 0.11.30.

A manifest `toolchains` entry pins the vendor name, the exact
`MAJOR.MINOR.PATCH` version, the release archive filename, its HTTPS URL and its
SHA-256. Validation renders the expected archive name and URL path from the
pinned version, so a manifest cannot point at another platform, channel, vendor
or version. `Tool` continues to describe sccache, which is one executable with
its own binary digest; a multi-file toolchain cannot be described that way, so
`Toolchain` enforces the archive digest, which fully determines the extracted
tree, plus the exact version every installed executable must report.

Provisioning verifies each archive against its pinned digest before extraction
and asserts the reported version afterwards. Bun, Rust and uv land on `PATH`
under `/usr/local`. Go is seeded into the official runner's default tool cache
at `/home/runner/actions-runner/_work/_tool/go/<version>/x64` with the
`x64.complete` marker, because that is where `actions/setup-go` looks before it
downloads. No environment variable or runner `.env` entry is required, which
matters because the one-job cache delivery rewrites that file per assignment.

The build record at `/etc/nddev/image-build.json` becomes schema 2 and gains a
`toolchains` object and the `runner_tool_cache` path. Every toolchain is also
published as an image property, so a deployed worker can be audited without
booting it. Both smoke suites assert presence, reported version, build-record
agreement and, for Go, the tool-cache layout and runner ownership.

An executable contract binds the manifest pins to the benchmark pins in both
directions: each installer short-circuit string and the workflow's
`go-version:` must name the baked version. If they ever diverge, every job
silently resumes installing that toolchain while the image still carries the
unused copy, which is exactly the failure this change exists to prevent.

The standard builder volume grows from 16 to 20 GiB. The Rust standalone
installer needs roughly 3 GiB of transient space; the published image is
unaffected because sanitation trims free extents before publishing.

## Consequences

The images grow by roughly 1.6 GiB each. That cost is paid once per image, not
once per job, and the loop-backed pool has ample headroom.

Toolchain upgrades now require an image rebuild and a canary rollout rather than
a workflow edit. That is the intended trade: the pinned set is supply-chain
verified at build time instead of re-fetched from the internet on every job.

This change alone is not expected to reach the 3x median speedup gate. The
dependency-bootstrap cost is untouched, so the gate stays closed until a fresh
statistical series says otherwise. Do not relabel the gate on the strength of a
faster toolchain step.
