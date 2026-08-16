# Critical path

The path a job takes, and which package owns each step. Read this before
changing anything in the middle of it.

```text
GitHub queues a job
  -> GARM scale-set listener receives JobAssigned / JobAvailable
  -> queue intent is journaled durably           internal/queueintent
  -> admission decides                           internal/garmproviderincus/provider
  -> provider journal binds job attempt to VM    internal/providerjournal
  -> Incus creates or claims a VM                internal/garmproviderincus/provider
  -> official actions/runner executes once
  -> diagnostics exported outside the VM         internal/diagnosticexport
  -> VM destroyed
```

## Invariants that hold the whole design up

- **One job per immutable full VM.** An executed worker is never reused and never
  returned to a pool.
- **A warm VM holds no runner registration and no repository identity** until a
  job claims it. That is what makes pre-booting safe.
- **Admission is durable and happens before GitHub job acquisition.** The intent
  is fsync'd before `AcquireJobs`, so a crash between the two cannot lose work
  GitHub believes was taken. `internal/deploycontract` asserts that ordering
  against the GARM patch text.
- **Teardown exports before it destroys.** Diagnostics are bounded and redacted,
  and leave the VM before it does.

## Where the boundaries are

- `internal/config` -- typed platform configuration and its fail-closed
  validation. `Load` validates; there is no way to obtain an unvalidated config.
- `internal/hostprobe` -- host capacity, health, cold-pilot admission.
- `internal/lifecycle` -- worker state machine and job-attempt identity.
- `internal/garmbootstrap` -- GARM credential, entity and scale-set
  reconciliation. `PublishedScaleSets()` is the one declaration of which runner
  classes exist; the canary, the diagnostic exporter and the fleet contract all
  derive from it.
- `third_party/garm` -- the in-tree GARM derivative: five patches and two
  overlays over a pinned upstream commit. See `SUPPLY-01-ARTIFACTS`.

## Pool versus scale set

These are different things and conflating them causes real outages.

- A **pool** is provider-side policy: image, resources, trust class, cache write
  scope. Named `nddev-linux-standard`, `-integration`, `-fast`.
- A **scale set** is GitHub-side routing for one entity. Several entities have
  their own scale sets against the same pool on the same host.

Tenancy therefore belongs to the entity, not the pool -- ADR 0036.
