# Scheduler recovery evidence contract

## Implemented boundary

A stalled identity remains observable even while restarting the dispatcher is
unsafe. The observation command may return `recovery_blockers`, an array of
bounded reason codes. `Evaluate` preserves `Decision.Stuck` and refuses recovery;
the controller emits `unhealthy`, not `healthy`, for this condition. The collector
must evaluate actual worker/claim occupancy independently from stalled-job
observation. Do not remove worker protection to make the queue appear responsive.

Progress commands must return exactly one JSON object whose `progressed` and
`remaining` arrays form a complete disjoint partition of the attempt's expected
identities. Missing identities, unrelated work, empty objects, duplicate IDs and
concatenated JSON values are rejected. The recovery engine checks this contract
for every executor, not only the command adapter. Invalid attempts are refused
before acquiring their durable state.

Interrupted attempts may verify progress but must not replay a manager restart
from old authorization. Incomplete or unknown progress finishes the attempt as
failed; a subsequent current observation must pass the normal policy and
cooldown. A syntactically valid partition still needs real per-identity evidence
from the deployment adapter. A vanished row is not a forward transition.

## Coordinated rollout

Pause only the recovery timer during the binary/adapter/config replacement;
do not stop running build workers. Back up the installed recovery artifacts and
state using the deployment's normal maintenance path. Install the new binary
before the adapter that emits `recovery_blockers`: older binaries reject the
unknown field rather than interpreting it as permission to restart. Verify the
exact installed source/build/config hashes and exercise observation without an
actual restart before re-enabling the timer.

The adapter must bind checkpoints and progress to `GHA_SCHEDULER_RECOVERY_ATTEMPT`
and the exact `GHA_SCHEDULER_RECOVERY_STUCK` set; preserve original snapshots and
recheck eligibility immediately before a restart. Do not hold provider-journal
locks across systemctl shutdown. An atomic admission fence and scale-set-local
repair are separate work; a last-moment read alone does not eliminate that race.

## Verification

Run `go test -race ./internal/schedulerrecovery` and the repository's full checks
on its pinned Go toolchain. The focused offline review exercised the changed
production files with Go 1.23.2, an unchanged type-only extraction of `Heartbeat`,
and the new standard-library regression tests. It did not execute FileStore,
the full dependency graph, systemd, Incus, or real GitHub job delivery.

The regressions cover exact partitions, unknown resume progress, fresh-decision
requirements, blocked-but-visible incidents, and strict single-value JSON.
Runtime acceptance additionally requires an affected real job to advance, no
unrelated running job lost, and matched pre-start/total-workflow measurements.
Neither a green PR nor fewer notifications is fleet throughput acceptance.
