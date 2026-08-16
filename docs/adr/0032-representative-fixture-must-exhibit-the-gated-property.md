# ADR 0032: A representative fixture must exhibit the gated property

- Status: accepted
- Date: 2026-08-10

## Context

Phase 3 gates the cache plane on a median end-to-end speedup of at least 3x with
a hit rate above 70 percent. The first strict series reported a 100 percent hit
rate and a 1.048x speedup, and ADR 0030 attributed the missing win to per-job
toolchain installation and dependency bootstrap.

The per-step durations of those exact two runs, taken from the GitHub Jobs API,
show that only half of that attribution was right.

| Step | cold | warm, 57/57 hits |
| --- | --- | --- |
| Install toolchain | 21 s | 20 s |
| Resolve dependencies | 1 s | 2 s |
| Build workload | 9 s | 8 s |
| Test workload | 8 s | 8 s |

Three facts follow, and two of them contradict the earlier hypothesis.

Toolchain installation is 20 to 21 seconds in **both** modes. It inflates
absolute job time and it is worth removing, which ADR 0030 does, but because it
is identical in both modes it cannot change the ratio the gate measures.

Dependency resolution is 1 to 2 seconds. `cargo fetch --locked` is not a
material cost, so the claim that dependency bootstrap consumed the cache win is
refuted. That claim was recorded in the roadmap and is corrected here.

The only cacheable phase is compilation, and a full cache hit on all 57 rustc
invocations moved it from 17 seconds to 16. The fixture is a 55-line crate with
29 locked packages. For a build that small, cargo's own orchestration, the
proc-macro build scripts and linking dominate, and none of them is something a
compilation cache can serve. Fetching 57 small objects from RustFS costs about
as much as compiling them locally.

Removing the toolchain install from both modes therefore leaves roughly 18
seconds cold against 18 seconds warm. The absolute job halves; the ratio does
not move.

## Decision

The 3x median speedup gate is **not reachable with the current fixtures**, and
it is not relaxed, reweighted or re-scoped to make it reachable.

The defect is in the fixture, not in the gate and not in the cache plane. Zot,
RustFS and the pinned sccache adapter are doing exactly what they are built to
do: the hit rate is 100 percent with zero cache errors. A cache that serves
every request cannot speed up a job whose cacheable phase is nine seconds out of
thirty-nine.

Phase 0 already states the requirement this violates: *select representative*
Go, Rust, Python/uv, Bun/Node and Docker workflows. A 55-line crate is a
correctness fixture, not a representative one. Closing the gate honestly
requires fixtures whose cacheable compilation dominates the job, drawn from the
shape of the estate's real workloads, and then a fresh statistical series.

`config/representative-workload-fixture-audit.json` records the measurement, and
its contract test recomputes the arithmetic and refuses a recorded verdict that
claims the gate is reachable as things stand.

## Consequences

The cache-plane work already merged keeps its value. ADR 0030's baked toolchains
remove 20 seconds from every job, which is a real and large improvement to
absolute latency even though it does not move the ratio. The sccache namespace,
trust separation and IAM work stand on their own.

What changes is the order of the remaining work. Enlarging the representative
fixtures now precedes any further cache tuning, because until the fixture can
exhibit the property, every speedup measurement taken against it is
uninformative. Reporting a ratio from a fixture that cannot produce one is worse
than reporting nothing, because it invites tuning a cache that is already
perfect.

The independent RustFS stable-release gate is unaffected and remains blocked
upstream.
