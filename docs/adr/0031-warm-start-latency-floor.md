# ADR 0031: The warm-start latency floor

- Status: accepted
- Date: 2026-08-10

## Context

The nddev.21 series measured a clock-safe warm queue-to-online median and p95 of
7 seconds against an exclusive below-five-second promotion gate. ADR 0027 added
host-side phase telemetry and ADR 0029 made the evidence clock-safe, but neither
answered whether the remaining interval is something this repository can remove.

That question is now measured rather than assumed. All twenty samples of
`config/direct-jit-nddev21-latency-audit.json` decompose as follows, each
segment inside a single clock domain:

| Segment | Median | p95 | Clock |
| --- | --- | --- | --- |
| end to end, `created_at` to `started_at` | 7000 ms | 7000 ms | GitHub |
| assignment to provider start | 1041 ms | 1213 ms | host |
| provider call | 439 ms | 659 ms | host |
| assignment to provider complete | 1451 ms | 1746 ms | host |
| guest assignment script to runner exec | 308 ms | 513 ms | guest |
| runner exec to broker session created | 2940 ms | 3317 ms | guest |

Reading the official runner's own log across the same twenty samples shows what
those last three seconds are, and it is not a retry or a backoff. After
`Attempt to create session` the runner loads its OAuth credential from the JIT
RSA key, opens three concurrent `Location.GetConnectionData` calls to GitHub,
builds the Vss connections, reloads the RSA parameters and performs one OAuth
token request. Every sample shows the same shape with no failed attempt.

The Phase 0 pilot recorded the same workflow on GitHub-hosted runners: fifteen
observations with a 3-second median and a 5-second p95 queue-to-start. A hosted
runner is already registered and connected to the broker before its job is
created; it never pays that sequence on the critical path.

## Decision

The below-five-second gate is recorded as **failed and unreachable**, not
relaxed, not reformulated into something that passes, and not removed.

The floor is derived by subtracting only the segments this repository can
change from the observed end-to-end median:

```text
7000 ms end to end
- 1451 ms assignment to provider complete   (host, ours)
-  308 ms guest assignment setup            (guest, ours)
= 5241 ms floor
```

If every controllable millisecond went to zero the metric would still be
5241 ms, above the 5000 ms exclusive target. The derivation deliberately does
not use the runner-connect measurement, so the verdict does not depend on the
one-second resolution of the runner's own log timestamps. The runner-connect
number explains where the unreachable time goes; it is not load-bearing.

The cause is the unregistered warm pool of ADR 0014 and ADR 0034. A warm VM
holds no registration state until a job claims it, so official runner
registration and broker connect are on the critical path by construction. That
is a security property this platform paid for on purpose, and ADR 0001 forbids
replacing the official runner to shorten it.

Two consequences follow, and both are recorded rather than chosen silently:

1. `config/warm-start-latency-decomposition.json` is the machine-readable
   evidence, and `TestWarmStartLatencyFloorIsDerivedNotAsserted` recomputes
   every number in it from the primary audit and the Phase 0 pilot record. The
   test fails if the floor ever drops below the target, if the verdict is
   edited to claim the gate passed, or if a segment stops naming its clock.
2. Optimizing the controllable 1759 ms remains worthwhile and is still tracked,
   because it is real latency and it narrows the gap to the hosted reference.
   It is no longer presented as a path to the five-second gate.

## Consequences

Closing the gate as written requires changing the unregistered warm invariant so
a warm VM registers and holds a broker session before a job arrives. That would
remove roughly three seconds and make the target reachable, at the cost of a VM
holding registration credentials before it is claimed. That is a threat-model
change, not a performance tuning change, and it needs its own ADR, security
review and fault analysis. This ADR does not make it.

Until then the honest statement is: the platform starts a warm job in 7 seconds
against a 3-second hosted median and a 5-second hosted p95, of which 5241 ms is
outside this repository's control.
