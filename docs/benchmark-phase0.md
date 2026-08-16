# Phase 0 representative benchmark evidence

Status: the six-run protocol pilot is complete; the Phase 0 statistical gate is
not complete. None of these pilot runs belongs to the later 20 cold plus 20
cache-hit sample series for either environment.

The authoritative machine-readable record is
[`benchmark/evidence/phase0-pilots-2026-08-09.json`](../benchmark/evidence/phase0-pilots-2026-08-09.json).
It contains the exact workflow and collector commits, run and artifact IDs,
artifact SHA-256 digests, queue/job durations, selected Jobs API phase
durations, toolchains, bounded network counters and hashed machine identities.
The Go contract test rejects extra fields, altered run identity, incomplete
phases, invalid artifacts, reused NDDev workers and a false claim that the
statistical gate passed.

## Provenance

- benchmark workflow commit: `ec1e3a1d0e3f6dd8b159c5822e1db6e0d1956544`;
- collector implementation commit: `01ba7539ab9f0e8bcbb46071bd2b3587a7050638`;
- collector merge commit: `db113334032074c1bf4f691b50c6f9bca5f10c23`;
- repository: private `example-user/github-actions`;
- collection date: 2026-08-09 UTC.

The warm-prime runs prove that the dedicated cache keys were initially absent.
They are excluded from both the cold-versus-hit pilot comparison and the later
statistical series.

## Run inventory

| Run | Environment | Role | Cache result | Run duration | Maximum queue | Runner IDs | Machine hashes |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: |
| `31285558405` | GitHub-hosted | cold pilot | disabled | 103 s | 3 s | 5 | 1 |
| `31286086226` | GitHub-hosted | warm prime | miss | 95 s | 4 s | 5 | 1 |
| `31286171098` | GitHub-hosted | warm-hit pilot | hit | 33 s | 5 s | 5 | 1 |
| `31285673882` | NDDev | cold pilot | disabled | 606 s | 504 s | 5 | 5 |
| `31286214729` | NDDev | warm prime | miss | 612 s | 573 s | 5 | 5 |
| `31286637765` | NDDev | warm-hit pilot | hit | 667 s | 568 s | 5 | 5 |

`Run duration` is workflow creation to final workflow update. It includes
GitHub scheduling and, on NDDev, the serial cold-VM admission path. It must not
be interpreted as application execution time.

## Workload elapsed time

The table uses the fixture's monotonic process measurement. A ratio below one
means that the cache-hit pilot was faster; it remains a single-run observation,
not a speedup claim.

| Environment | Workload | Cold | Cache hit | Hit/cold |
| --- | --- | ---: | ---: | ---: |
| GitHub-hosted | Bun/Next | 18.441 s | 12.152 s | 0.659 |
| GitHub-hosted | Docker | 13.453 s | 14.773 s | 1.098 |
| GitHub-hosted | Go | 93.622 s | 16.631 s | 0.178 |
| GitHub-hosted | Python/uv | 22.236 s | 19.126 s | 0.860 |
| GitHub-hosted | Rust | 34.114 s | 17.547 s | 0.514 |
| NDDev | Bun/Next | 19.241 s | 58.387 s | 3.035 |
| NDDev | Docker | 19.532 s | 29.478 s | 1.509 |
| NDDev | Go | 89.946 s | 85.979 s | 0.956 |
| NDDev | Python/uv | 28.159 s | 39.384 s | 1.399 |
| NDDev | Rust | 40.582 s | 45.896 s | 1.131 |

## Pilot findings

1. Queue time dominates the current NDDev end-to-end result. The five-job
   workflow is deliberately serialized by the safe one-VM capacity limit and
   every worker is still cold-created. Warm unregistered capacity is therefore
   the next provisioning optimization.
2. GitHub `actions/cache` is not a substitute for the planned local cache
   plane. Four of five NDDev workloads regressed on the cache-hit pilot; Go
   improved by only about four percent. RustFS, Zot and tool-native cache
   measurements must replace WAN transfer in the target comparison.
3. Disposable-worker uniqueness held. Every NDDev run used five distinct
   runner names and five distinct hashed machine identities.
4. The evidence is sufficient to accept the harness and collector protocol,
   but insufficient for median, p95, variance or an end-to-end speedup claim.

## Post-run convergence

The post-run snapshot at `2026-08-09T01:14:42.007524475Z` reported a healthy
observer; zero visible, orphan or missing Incus instances; zero journal leases;
zero registered disposable GitHub runners; and 38 of 38 diagnostic bundles
confirmed in RustFS with no pending bundle or exporter failure. Root filesystem
free space was 41 percent. All twelve retained legacy listeners remained
present, systemd reported no failed units, and ExamplePlatform plus Captcha returned
HTTP 200.

## Remaining Phase 0 gate

After the observer-lag and warm-pool changes are independently accepted, run
20 cold and 20 cache-hit samples per environment using fresh iteration names.
Keep isolated-latency runs sequential, exclude primes, preserve every evidence
record, and calculate median/p95 only from the completed statistical series.
Saturation and fairness measurements remain a separate scheduler gate.
