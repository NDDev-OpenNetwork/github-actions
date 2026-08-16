# ADR 0018: Warm VM host-reboot recovery

Status: accepted

## Context

An unregistered warm VM is durable Incus state, but provider versions through
`v0.1.5-nddev.6` left Incus' `boot.autostart` default disabled. After a host
reboot the lifecycle journal and metadata could still identify the VM as
warm-ready while its runtime state was stopped. The claim path rejected that
VM, but warm reconciliation counted it toward capacity and would not repair the
pool. This was safe but unavailable and made the first post-reboot job fail.

## Decision

Provider `v0.1.5-nddev.7` creates every warm VM with
`boot.autostart=true`. A VM counts as warm-ready only when all immutable
metadata matches, autostart is enabled and Incus reports it running. The same
checks guard assignment, so neither reconciliation nor a GitHub job can consume
stale stopped capacity.

Existing unassigned warm VMs from an older provider are drained only while
claims are zero and replaced before the controlled reboot. Reconciliation binds
ready inventory to the exact provider version, commit, image, profile, security
and trust metadata; it never mutates an old VM into a new release and never
reuses a VM that has executed job code.

## Consequences

- Incus restarts the clean unregistered VM during host boot.
- Failure to start is visible as failed warm reconciliation instead of a false
  ready count.
- Warm capacity remains fail-closed until the observer, journal and Incus state
  converge.
- Any VM that has received a job remains disposable and is never returned to
  this pool.
