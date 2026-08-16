# Worker lifecycle contract

This contract is normative for the NDDev control plane. The Go implementation
in `internal/lifecycle` is the executable reference.

## Identity

The primary Scale Set path uses the identity exposed by GitHub's message queue:

```text
(scale_set_id, job_id)
```

Repeated delivery of the same opaque `jobId` must converge on one admission;
the later numeric `runnerRequestId` is acquisition metadata, not lifecycle
identity.
For a webhook-driven fallback pool, the identity is:

```text
(repository_id, workflow_job_id, run_attempt)
```

The control plane derives a domain-separated, versioned SHA-256 key from either
tuple. A rerun produces a new `jobId` or `run_attempt` and therefore
receives a new VM.

At any time, one job key may own at most one active worker. A worker may be
owned by at most one job key for its entire lifetime.

## States

| State | Meaning |
| --- | --- |
| `provisioning` | immutable VM clone is being created or booted |
| `available-warm` | VM is healthy, unregistered and contains no job identity |
| `admitted` | capacity and policy accepted one job; ownership is durable |
| `registering` | one-job JIT configuration is being applied |
| `online` | GitHub sees the ephemeral runner; no job assigned yet |
| `assigned` | GitHub assigned the admitted job |
| `running` | official runner started workflow execution |
| `collecting` | result is known or startup failed; diagnostics are exporting |
| `destroying` | registration, VM, disks and transient network are deleting |
| `destroyed` | terminal state; all owned resources are gone |
| `quarantined` | deletion did not converge; resource is isolated for GC/retry |

## Required path

```mermaid
stateDiagram-v2
    [*] --> provisioning
    provisioning --> available-warm: warm-ready
    available-warm --> admitted: admit(job-key)
    admitted --> registering: begin-registration
    registering --> online: runner-online
    online --> assigned: assigned
    assigned --> running: job-started
    running --> collecting: succeeded / failed / cancelled
    collecting --> destroying: diagnostics exported or bounded timeout
    destroying --> destroyed: all resources absent
    destroying --> quarantined: deletion failed
    quarantined --> destroying: retry with lease
    destroyed --> [*]
```

Provisioning, registration, assignment and execution failures converge toward
`collecting` or `destroying`. There is deliberately no transition from any
post-admission state back to `available-warm`.

## Invariants

1. Warm VMs are never registered with GitHub.
2. Images never contain registration tokens, runner state or repository secrets.
3. Admission ownership is persisted before registration begins.
4. Every job-bearing transition must match the admitted job key.
5. A worker executes no more than one job.
6. An executed worker is destroyed even when the job is cancelled or fails.
7. Diagnostic collection is bounded; it cannot block destruction forever.
8. `destroyed` means GitHub registration, VM, disks and transient network are
   all absent, not merely that a delete request was sent.
9. A failed delete is quarantined and visible; it is never silently forgotten.
10. Manager restart and duplicate events converge without duplicate active VMs.

## Durable journal requirements

Each transition records at minimum:

- event ID and idempotency key;
- previous and new state;
- source identity: Scale Set/request or repository/job/run-attempt;
- runner ID and VM ID when known;
- image digest and runner version;
- lease owner and expiry;
- reason code and retry count;
- timestamps from the manager clock;
- teardown confirmation for every owned resource.

The provider's warm-pool journal additionally stores a one-to-one claim from
GARM's stable requested runner name to the actual pre-booted Incus VM name.
That binding includes controller, pool, image fingerprint, reserved/injected
state and a bounded lease. It is committed before assignment bytes cross the
Incus API. GARM receives the actual VM name as provider ID, so later get/delete
calls resolve deterministically even though the job-facing name differs.

The storage transaction must persist the event and new aggregate atomically.
External calls are reconciled after timeouts instead of being assumed failed.

## Reconciliation rules

- GitHub runner exists, journal lacks runner ID: match immutable instance
  metadata, adopt only when ownership is unambiguous, otherwise quarantine.
- Incus VM exists, durable record is terminal or absent: quarantine and delete
  after its ownership label and lease are proven.
- Journal expects VM, Incus reports absent: mark infrastructure failure and
  remove stale GitHub registration.
- GitHub job is cancelled before assignment: export available diagnostics and
  destroy the bound VM.
- Manager restarts in any non-terminal state: acquire an expired lease and
  continue or compensate; never create a second VM speculatively.

Fault-injection tests must stop the manager before and after every external call
and verify convergence to one terminal state with zero orphan resources.
