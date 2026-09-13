# Queue journal reconciliation

A durable queue intent and a GARM workflow-job row have different lifetimes.
Normal webhook cleanup can remove a DB row before the scale-set listener
settles its journal entry. Absence of that row is not proof of completion.

GARM now reads the journal independently during periodic reconciliation.
Candidates are old, repository-bound queued/assigned entries without a runner
or acquire request. Running/acquired work, fresh entries, foreign owners and
active terminal receipts are excluded. The read never renews TTLs or changes
resource ownership. Authoritative rehydration preserves workflow run identity,
including on an already repository-bound entry, and refuses a conflicting run.

The exact GitHub verifier binds GUID, owner/repository, current run attempt,
check_run_url, GitHub Actions producer, source SHA, labels and the expected
scale-set ID before releasing an intent. Its normal journal transaction writes
a terminal receipt and preserves unrelated execution. No job is cancelled or
rerun, and no outcome is inferred from expiry, DB absence or a name match.

Legacy entries without run identity may use a complete window of at most ten
runs: thirty minutes before the first queue observation through one minute
after it. Every returned run and check-page boundary is validated; absent,
incomplete or duplicate GUID evidence remains unproven. Two candidates per
pass and a five-minute per-GUID budget bound this exceptional search. A
successful DB reconciliation that retains an in-progress journal entry keeps
that budget, preventing a repeated-request loop.

Regression tests exercise the periodic entry point with SQLite and the native
private journal, including missing DB rows, preserved running siblings,
restart/idempotence, identity conflict, access refusal, incomplete discovery,
duplicate GUIDs and wrong scale sets. Consumers record actual runtime adoption
and observed terminal convergence separately from these tests.

API identity fields: [workflow jobs](https://docs.github.com/en/rest/actions/workflow-jobs)
and [workflow runs](https://docs.github.com/en/rest/actions/workflow-runs).
