# ADR 0023: Protect starting Scale Set runners from generic scale-down

## Context

During the first live cross-pool preemption canary, workflow run `31313695820`
was acquired for the integration Scale Set. Provider `v0.1.5-nddev.13`
atomically admitted cold runner `nddev-ghrinauog1ni` and marked unregistered
standard warm VM `warm-standard-f67369eefc8f` deleting with that exact
`preempted_by` owner. The warm VM was destroyed and the integration VM reached
provider state `running`.

Before the official runner registered, a later GitHub statistics message set
the desired count to zero. Upstream `handleScaleDown` protects `active` and
`terminated` runner states, but not `pending` or `installing`. It therefore
deleted the newly running VM about 40 seconds after acquisition even though the
job remained queued. The run was explicitly cancelled after evidence capture.

GARM already has a separate bounded timeout reaper for `pending` and
`installing` runners. Generic desired-count scale-down racing that reaper makes
cold provisioning depend on registration winning a five-second reconciliation
tick, while warm activation often hides the defect by registering faster.

## Decision

Derivative `v0.2.1-nddev.2` treats `pending`, `installing`, `active` and
`terminated` as protected from generic scale-down. Idle, failed, offline and
unknown runners retain the existing scale-down path. A starting runner that
never registers remains bounded by the existing configured runner timeout and
provider reconciliation; the change does not create an unbounded lease.

The patch remains limited to GARM Scale Set/provider reconciliation and tests.
It does not modify the official GitHub Actions runner, JIT configuration,
provider interface or GitHub protocol.

## Consequences

- cold VM startup no longer races transient desired-count reduction;
- cancellation before registration converges when the runner becomes idle or
  through the existing timeout reaper;
- event-driven and five-second periodic reconciliation remain enabled;
- the derivative must be reproducibly rebuilt, deployed from an ordinary merge
  with an exact rollback binary and verified by a repeated integration canary;
- repository fairness before `AcquireJobs` remains a separate control-plane
  milestone.
