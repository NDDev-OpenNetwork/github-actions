# ADR 0027: Secret-free direct-JIT phase telemetry

- Status: accepted
- Date: 2026-08-10

## Context

The first 20-sample warm direct-JIT series proved the one-job lifecycle but
measured a 6.700-second p95 against the 5-second target. Second-resolution
official-runner diagnostics could not distinguish GitHub scale-set delivery,
durable admission, `AcquireJobs`, GARM instance creation and the external
provider handoff. Optimizing from that indirect signal would risk changing a
correct isolation or reconciliation boundary without evidence.

The GARM journal already emits `new job assigned` and `job available` with the
host journal's high-resolution timestamp. Two boundaries were missing:
`AcquireJobs` and `Provider.CreateInstance`.

## Decision

GARM `v0.2.1-nddev.10` adds structured `direct JIT phase` events around those
two calls. Each event contains only:

- a fixed phase name;
- the existing runner or scale-set identity needed for correlation;
- numeric runner request IDs already present in GARM lifecycle logs;
- a monotonic elapsed duration in integer milliseconds.

Start, success and failure are all observable. The patch must never log the JIT
configuration, instance token, CA bundle, provider request body, credentials or
environment. It adds no journal file, database column, network listener, fsync,
retry, timer or lifecycle transition. The system journal remains the immediate
source; the existing external diagnostic/export path will carry the bounded
events before ephemeral evidence is retired.

The phase chain used for diagnosis is:

```text
JobAssigned observed
  -> JobAvailable observed
  -> acquire-jobs-started/completed
  -> provider-create-started/completed
  -> official runner ExecuteCommand/session
```

Wall-clock timestamps compare boundaries on the same host. The emitted
`duration_ms` values use Go's monotonic clock and are authoritative for the two
wrapped calls. Runner diagnostic timestamps remain supporting evidence, not a
replacement for host-side phase telemetry.

## Consequences

The next canary can attribute latency before any performance behavior changes.
If GitHub delivery dominates, local provider changes cannot solve it. If GARM
or the provider dominates, optimization is limited to the measured phase.
Cardinality is bounded by the current one-job pilot and logs are retained under
the existing restricted diagnostic policy.
