# ADR 0021: One-job cache credential delivery

Status: accepted on 2026-08-09; merge-bound host and workflow canaries remain
required before enabling measured cache workloads.

## Context

ADR 0020 created exact RustFS identities but deliberately stopped before
worker distribution. A credential in a golden image, cloud-init, Incus
configuration or GARM state would outlive the narrow assignment boundary or
become visible through ordinary control-plane inspection. A persistent runner
environment would also violate the one-job VM invariant. Cold and warm workers
must converge on one mechanism even though cold bootstrap starts through
cloud-init and a warm VM is already booted and unregistered.

The official runner supports a synchronous administrator hook through
`ACTIONS_RUNNER_HOOK_JOB_STARTED`. The runner executes it after accepting a job
but before the first workflow step, processes workflow commands from its
output, initializes the standard file-command files and processes `GITHUB_ENV`
when the hook exits. This remains inside the official execution engine rather
than replacing GitHub Actions semantics. GitHub documents the hook contract in
[Running scripts before or after a
job](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts).

## Decision

Provider `v0.1.5-nddev.11` owns the complete delivery boundary:

1. The validated pool policy maps only `trusted/trusted`,
   `untrusted/isolated` and `release/none` to `trusted-writer`,
   `untrusted-writer` and `release-reader`. The host-only `promoter` role is
   never deliverable.
2. Immediately after a cold VM starts, the provider writes a root-owned mode
   `0400` JSON assignment through the Incus Agent and only then writes a
   separate zero-byte readiness marker. Cloud-init contains the non-secret
   waiting/setup program, never the assignment.
3. A warm VM receives the same JSON inside its existing root-owned one-job
   assignment before the official runner registration script is invoked. No
   credential enters unregistered warm capacity.
4. The setup program validates exact keys, values, ownership, modes and one-job
   delivery ID, installs the public RustFS CA, creates the hook outside the
   runner application directory and writes only the hook path to the runner
   `.env` file.
5. The hook validates the assignment again, emits `add-mask` for both S3
   values, appends only the exact cache environment to the runner-created
   `GITHUB_ENV`, writes a non-secret consumed marker and removes the assignment
   and readiness marker before any workflow step.
6. Existing-instance recovery accepts only an exact consumed marker or the
   matching ready assignment. It does not overwrite an assignment while guest
   setup and runner startup race.
7. The ordinary read-only provider probe loads and validates the selected
   role as the `garm` service identity and reports only role/readiness. A
   rollout cannot resume admission when host credential ownership, mode, link
   count, secret length, CA or trust contract has drifted.
8. Every exact image mapping pins the immutable image's numeric runner UID and
   GID. Cold retry accepts consumed or ready files only with that image-bound
   ownership; it never assumes a distribution-default `1000:1000` identity.

The delivered environment intentionally exposes a trust-scoped prefix root,
not a final compiler cache key. Reusable workflows must append platform,
architecture, toolchain version, dependency-lock digest and ref class before
setting `SCCACHE_S3_KEY_PREFIX`. The IAM policy remains the authoritative
repository/trust boundary even if workflow code chooses a different suffix.

The merge-bound standard canary validates the consumed marker and removed
staging files before performing a real SigV4 PUT/GET under that complete
namespace. It also proves cross-trust PUT and runner DELETE denial. The host
root identity removes only the exact canary object after the secrecy audit.

## Consequences

- Cache secrets exist only on the host and inside one disposable job VM.
- Incus metadata, cloud-init, golden images, provider results and audit logs
  remain secret-free.
- Warm capacity stays reusable only before assignment; an assigned VM is still
  destroyed after one job.
- A missing, drifted or unreadable identity blocks the job before runner
  registration rather than silently widening access.
- Cache outage fallback must be explicit in the reusable workflow; it cannot
  substitute another trust identity.
- RustFS RC.1 remains canary-only. Delivery correctness and component
  production promotion are independent gates.
