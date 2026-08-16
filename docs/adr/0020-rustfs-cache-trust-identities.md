# ADR 0020: RustFS cache trust identities

Status: accepted on 2026-08-09; live apply and workflow distribution remain
separate evidence gates while RustFS RC.1 is canary-only.

## Context

RustFS is the selected S3-compatible backend for compiler and
content-addressed caches. A single shared writable identity would let an
untrusted pull request poison inputs later consumed by trusted or release
jobs. Root credentials cannot enter a worker, golden image, GARM metadata or
Incus configuration. RustFS RC.1 is still a pre-release, so identity
provisioning must not silently promote the component or enable workflow use.

## Decision

`config/rustfs-cache-identities.yaml` is the exact, validated contract for one
64 GiB `github-actions-cache` bucket and four identities:

| Role | Prefix | Permission | Retention |
| --- | --- | --- | --- |
| trusted writer | `example-user/github-actions/trust/trusted` | read/write, no delete | 30 days |
| untrusted writer | `example-user/github-actions/trust/untrusted` | read/write, no delete | 7 days |
| promoter | `example-user/github-actions/trust/promoted` | read/write, no delete | 90 days |
| release reader | `example-user/github-actions/trust/promoted` | read-only | 90 days |

`gha-fleet reconcile-rustfs-cache` is the only supported provisioning path.
It is root-only, authenticates over pinned-CA TLS and SigV4, creates local
384-bit secrets atomically without replacing an existing path, applies the
bucket quota/lifecycle and IAM policies, and then performs real positive and
negative object operations. Cross-prefix writes, release writes and all
identity deletes must return HTTP 403. Root removes every probe object.

Credential filenames and AWS-shaped access keys are deterministic; secret
keys are independent random values. Files live only in the root-writable
`/etc/garm/cache` directory, are `root:garm` mode `0640`, singly linked and
never emitted in JSON or errors. Partial local sets, remote users without a
recoverable local secret, ownership/mode/link drift, unknown YAML fields and
policy drift fail closed. An incomplete fresh write is rolled back and synced
before returning an error.

The reconciler does not distribute credentials. A later provider change must
inject only the selected role into an already-clean disposable VM immediately
before assignment, mask values through the official runner job-start hook and
remove staging material. Public/fork work remains on GitHub-hosted capacity.

## Consequences

- Untrusted workflows cannot write trusted or promoted cache objects.
- Release jobs cannot mutate their cache inputs.
- The manager can read a selected non-root pair but cannot rewrite it.
- IAM reconciliation is idempotent and independently testable from runner
  rollout and RustFS production promotion.
- Adding a repository or trust class requires a reviewed config/code change;
  there is no organization-wide wildcard.
- RustFS RC.1 remains canary-only until its independent stable-release and
  runtime gates pass.
