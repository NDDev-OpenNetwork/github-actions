# ADR 0014: Activate unregistered warm VMs through the Incus Agent

Status: accepted; production activation remains canary-gated.

## Context

GARM's `min_idle_runners` provisions runners that already hold GitHub JIT
identity. The platform contract instead requires a healthy, pre-booted VM with
no repository, runner registration or job credential until a job is admitted.
The host has capacity for one conservative standard worker, so the first target
is a single ready VM rather than an overcommitted fleet.

A warm VM and GARM also have different useful identities: GARM retries creation
with its requested runner name, while the already-created Incus VM has an
immutable actual name. Activation must survive duplicate calls, provider process
exit and manager restart without assigning two jobs to one VM or leaking a JIT
credential.

## Decision

Keep GARM idle runners at zero and implement warm capacity inside the in-tree
provider boundary.

- The timer creates an exact pinned-image VM tagged `warm-preparing` with no job
  identity and no registration data.
- The image pre-bakes the official runner unregistered and starts a root-owned
  path activator. A readiness service attests that the path unit is active and
  registration files are absent.
- The controller promotes a VM to `warm-unregistered` only after reading the
  exact root-owned readiness file through the Incus Agent.
- The fsync-backed journal atomically claims one ready VM for one GARM runner
  name before metadata or assignment injection. Concurrent and duplicate claims
  converge on the same durable mapping.
- Provider metadata is changed irreversibly to `ephemeral-one-job`, then a
  root-owned `0700` assignment is written through the Incus Agent. The guest
  validates its digest, creates a started marker before execution and invokes
  the official GARM assignment as the unprivileged `runner` user.
- The actual warm VM name is returned as GARM's provider ID. Get and delete
  accept either identity and resolve through the journal.
- Claimed, injected, ambiguous and executed VMs never re-enter the ready pool.
  Reconciliation refreshes valid ownership and converges failures to deletion;
  replenishment always uses a fresh clone.

The optional controller CA bundle is validated as PEM, installed root-owned and
mode `0600`, and consumed only by the assignment. The assignment file contains
an encoded opaque JIT token, is never shell-traced, and is removed after the
one-shot activator reads it.

## Consequences

Warm startup avoids image boot, toolchain installation and official-runner
download in the job hot path while preserving GitHub compatibility and the
full-VM security boundary. The journal schema advances to version 2 and migrates
schema 1 by adding an empty claims map.

The timer, provider, observer, policy and image must be deployed from one
reviewed commit. A changed image recipe requires a new immutable alias and host
build/smoke evidence before `target_ready` may become nonzero. The first
production activation must prove exclusive claim, one job, destruction, fresh
replacement and zero orphans; until then the committed target remains zero.
