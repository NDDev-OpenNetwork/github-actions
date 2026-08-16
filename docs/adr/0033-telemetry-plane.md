# ADR 0033: OpenTelemetry into a dedicated OpenObserve instance

- Status: accepted
- Date: 2026-08-10

## Context

The fleet is blind over time. Three facts, measured rather than assumed:

The observer computes 53 Prometheus-format metrics on `127.0.0.1:9464/metrics`
and **nothing scrapes them**. There were zero established connections to that
port. Every value is computed and discarded.

No collection component is installed on the CI host at all: no collector, no
time-series store, no log shipper.

`journald` is capped at 200 MB and is sitting at 198.7 MB. The unit file asks
for `MaxRetentionSec=7day`, but the oldest entry was two days old. So the GARM
phase telemetry added by ADR 0027, provider decisions and warm-pool refusal
reasons all evaporate within about two days.

The consequence is concrete: no question of the form "how did this behave last
week" can be answered, no trend exists, and every audit in `config/` is a
snapshot someone took by hand at one moment.

The roadmap's remaining observability milestone asks for an OpenTelemetry
collector and a durable **external** metrics and log backend.

## Decision

OpenTelemetry is the only collection standard and OpenObserve is the only
telemetry store. Nothing in the fleet writes to a telemetry store directly; a
new signal gets a pipeline in the collector or it is not stored.

The estate already runs exactly this pair on `server-example-media`, healthy,
digest-pinned and backed by RustFS object storage. That instance belongs to a
different project. Rather than becoming a tenant of it, the fleet gets **its
own OpenObserve instance on the same host**: its own image digest, its own
RustFS bucket `nddev-ci-telemetry`, its own credentials, its own retention and
its own alerts. The two projects share hardware and nothing else, so neither
one's retention policy or access changes can reach the other's evidence.

It runs off the CI host on purpose. A store that dies with the host it observes
cannot explain why the host died, and that is what "external" is for. The
receiving host also sits at load 0.35 against a CI host that routinely runs at
9 to 25 on eight cores, so collection cannot compete with the jobs it measures.

`config/telemetry-artifacts.yaml` pins both components the way the cache plane
is pinned. The collector is a release tarball, because the CI control plane
deliberately has no container runtime; its checksum, CycloneDX SBOM and
Sigstore bundle are pinned alongside the archive, so a build cannot be accepted
on its archive digest alone. The store is pinned by image digest, the only
immutable identity a registry offers. Validation rejects a tagged image, a
`latest` URL, a public transport address, a privileged port, a shared bucket
and any stream that was not declared.

Transport is OTLP over HTTP across the private cloud network the two hosts
already share, never a public address. The collector buffers to disk, which
turns a store outage into a delay rather than a hole in the record.

## Consequences

The Prometheus wire format survives in exactly one place: as the scrape source
the collector reads from the observer. No Prometheus server, no alertmanager
and no separate log store is deployed, and journald stops being a retention
mechanism and becomes only a source the collector reads. Making the observer
emit OTLP natively would remove that last translation and is worth doing, but
it changes a load-bearing service and is deliberately not bundled here.

The fleet now depends on a second host for its own observability. That is the
point of external durability, but it means a network partition delays evidence.
The disk-backed queue bounds that: telemetry is retained locally and delivered
late rather than lost.

Both components start at `canary`. Promotion requires the same treatment every
other component got: runtime evidence that the pipeline delivers, that no
credential reaches a stream, and that the store survives a restart of either
host.
