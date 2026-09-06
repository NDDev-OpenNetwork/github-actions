# Recovery evidence contract

Recovery eligibility and stalled-work observation are separate. Adapters may
emit `restart_blockers` as an optional list of bounded reason codes. The evaluator
retains exact stalled IDs while refusing a manager-wide restart. Reasons contain
only lowercase ASCII letters, digits and hyphens; at most 16 codes of 96 bytes.

Progress must partition the original attempt exactly into `progressed` and
`remaining`. Missing, duplicate, overlapping, foreign and empty receipts are
invalid. An unavailable observer leaves every unverified identity unresolved.
Resuming an attempt with invalid evidence does not restart the dispatcher again.
Previously verified progress is retained when resuming only the unresolved subset.

The adapter, not this structural validator, must prove a transition of each exact
job/instance. Disappearance from a filtered detector, expiry, unrelated work and
an advancing global heartbeat are not that proof. Original job identity and
attempt scope must survive retry and checkpoint boundaries.

## Rollout

Install the new recovery binary before an adapter emits `restart_blockers`: older
observers reject unknown fields. An old adapter can still speak the previous
schema, but it does not acquire the stronger semantic guarantees automatically.
Recheck eligibility immediately before a manager restart; an earlier observation
cannot fence newly arriving work. Never delete provider or queue journals to make
this check succeed. This change does not add a dependency between ordinary
application delivery and GitHub CI.

## Verification

Run `go test ./internal/schedulerrecovery` and `go test -race
./internal/schedulerrecovery` with the repository-pinned toolchain. The regression
suite includes malformed receipts, empty successful output, missing identities,
resume observation failure, partial-resume accounting, and blocker propagation.
No live recovery or throughput claim follows from unit tests.
