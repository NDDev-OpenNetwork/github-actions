# Background CI feedback

CI is evidence for agents, not the interactive development loop. Ordinary
application delivery must not acquire a dependency on this feedback workflow.
Failures stay failures. A repository owner can remove general CI contexts from
merge/deploy requirements without changing their outcome to success.

## Interface

The reusable workflow `ci-feedback.yml` accepts a run ID and exact attempt.
Completed attempts are the default; unfinished observation is an explicit option.
It executes an immutable public composite action on a standard hosted
runner. No project checkout, PR script, artifact, cache, log or title is executed.
The token only needs repository Actions read and Issues write. Private callers
publish their evidence in their own repository, never in this public module.
GitHub.com is supported; Enterprise Server routing is deliberately not inferred.

For each failed completed attempt the action reads authoritative API metadata,
checks repository ID, run ID, attempt and source SHA, then creates one unassigned
repository-local issue. The body contains a `ci-feedback:v1` marker and JSON
evidence: repository, workflow/run/attempt/commit, observed job count, failed job
IDs/links, observation time, run creation time, a metadata-derived failure
reason, `blocking: false`, and `delivery_state: unassigned`. Product versus
infrastructure classification stays `unknown` until evidence establishes it. Names, arbitrary
text and raw logs are omitted. Only the first 100 failed-job links are included;
total and omitted counts are explicit. Remaining exact-attempt jobs are read from
the API.

Success, neutral and skipped conclusions create no issue. A cancelled attempt
creates an issue only when a job on that attempt already failed; a clean cancel
does not. A missing or unknown result is not converted to success. A run-level
failure with zero jobs is still reported. Evidence collection/publishing failure
leaves this reporter red; it does not mutate the originating run or block
application deploy.

## Early job failure observation

Set the composite action's `allow-in-progress: 'true'`, the reusable workflow's
boolean `allow-in-progress: true`, or the Python publisher's
`allow_in_progress=True` only in a trusted reporter. This enables observation
of an exact unfinished attempt through GitHub's attempt-specific jobs endpoint.
It creates an issue only when a job is explicitly `completed` with a failing
conclusion. A queued job, an unfinished job, or a missing final run conclusion
is never treated as a failure or success by itself.

Early evidence records `run_status`, `attempt_complete: false` and a null run
conclusion. It is a dated failure snapshot; its observed job count and failure
list do not claim to include jobs that finish later. The attempt marker is the
same as for terminal delivery, so completion or cancellation cannot create a
second issue for that attempt. The publisher preserves the original issue and
any human edits; it does not rewrite that snapshot to claim a final outcome.

In early mode every Python return includes `attempt_complete`, `run_status`
and `run_conclusion`. An unfinished attempt with no failed jobs returns
`status: pending`, never `not-a-failure`. Polling executors must keep every
unfinished attempt pending even after `published` or `already-published`, and
record terminal receipts only after an explicit `attempt_complete: true`.
The final observed status belongs in that receipt. The exact-attempt link in
the issue provides subsequent authoritative job outcomes. A fresh rerun has
its own attempt key and is never substituted for the original.

This option does not itself schedule polling or add an event subscription.
Completed `workflow_run` delivery stays supported; an independently configured
reconciler is responsible for observing early failures and missed events.

## Deduplication and bounded work

Serialize reporters for the same repository/run/attempt with cancellation off.
Direct issue listing avoids search-index lag; exact-publisher markers deduplicate
re-delivery, including an already closed issue. Collection is bounded to 10 pages
of 100 jobs/issues and 4 MiB per response. Exceeding the inventory bound fails
explicitly instead of assuming no prior issue. Mutation is a single issue POST;
there are no blind write retries. A rerun rechecks the durable marker first.

API requests remain repository-local on api.github.com. Redirects are refused so
a token cannot follow a redirected request to another origin. No production
credentials, broad PAT or private runner is needed.

## Issues stay unassigned until the owner assigns work

An issue is durable evidence, not proof that an agent received or executed it.
`delivery_state: unassigned` is intentional: this publisher does not invent a
repair owner or start a model session. The repository owner assigns the agent.
Deduplicate by repository/run/attempt and the configured account numeric ID and type.
The composite action defaults `publisher-id` to GitHub Actions' bot ID. A custom
App token must supply its own bot account ID from trusted configuration, never
from the triggering event or issue body. A trusted private executor may instead
configure `publisher-type: User` with its exact authorized user ID; the default
remains `Bot`. The executor must verify its authenticated identity before
publishing, serialize delivery and keep durable pending events/receipts. This
option does not grant permissions or broaden an inventory App's read authority. Neither `type: Bot` nor a `[bot]` login
suffix alone establishes trust. After an ambiguous POST timeout the publisher re-reads
the durable marker instead of creating a second issue. Commands and manifests
come from trusted project configuration, never issue text or CI log instructions.

Close only after verifying a repair or documenting that newer work explicitly
supersedes this failure. Do not automatically retry product failures or weaken
assertions. Ordinary application development continues while this diagnostic
work runs.

GitHub's GITHUB_TOKEN-created issue events do not automatically start another
`on: issues` Actions workflow. Monitor reporter failures separately and
reconcile missed workflow_run events. This action does not supply an always-on
agent scheduler.

## Adoption

Add a workflow_run caller on the default branch, scoped to actual verification
workflow names; do not subscribe to the feedback workflow itself. Include
`cancelled` in the conclusion filter so already-failed jobs on a cancelled or
superseded run still reach the publisher; `publish()` creates no issue for a
clean cancel. The in-repo example is `.github/workflows/ci-feedback-events.yml`.
Pin the reusable workflow to the reviewed immutable commit. Grant actions: read
and issues: write in the caller. Its own check is advisory and must not become a
deploy prerequisite.

Latest-only cancellation applies to stateless PR verification. In-flight deploy,
migration and other stateful apply runs retain their own serialization policy.
Changing a workflow's concurrency does not change branch rulesets automatically.

## Verification

`python3 -m unittest discover -s tests -p test_ci_feedback.py -v`

Tests execute the production publisher with API fixtures. They cover exact-attempt
binding, unassigned delivery, cancelled runs that already failed, duplicate
delivery including App bots, lost POST replies, spoofed markers, incorrect
identities, partial pagination, bounded large failure evidence and token routing.
Early-mode cases cover completed failed jobs beside unfinished work, explicit
opt-in, no-failure pending observations, invalid run/job states, foreign attempts
and source commits, and one durable issue across completion or cancellation.
No live issue delivery or agent acknowledgment is implied by these tests.

References: GitHub Actions workflow_run security, GITHUB_TOKEN event recursion,
REST workflow-run attempts, and workflow concurrency documentation. Consult their
current official docs when modifying event or permission behavior.

The relevant primary API contracts are [issue creation](https://docs.github.com/en/rest/issues/issues#create-an-issue),
[workflow run attempts](https://docs.github.com/en/rest/actions/workflow-runs#get-a-workflow-run-attempt)
and [attempt jobs](https://docs.github.com/en/rest/actions/workflow-jobs#list-jobs-for-a-workflow-run-attempt).
