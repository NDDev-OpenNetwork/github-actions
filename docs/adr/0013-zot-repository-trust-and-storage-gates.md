# ADR 0013: Zot repository trust and destructive storage gates

Status: accepted on 2026-08-08; production promotion passed after destructive
storage, live identity and automatic host-reboot evidence.

## Context

The pilot Zot registry originally used one writer for `cache/**` and one global
reader. That was sufficient for isolated protocol smoke, but it did not bind a
credential to a repository or distinguish trusted, untrusted and release
inputs. It also left garbage collection and full-disk recovery unproven.

RustFS and Zot have independent artifact and runtime maturity. RustFS RC.1 must
remain canary-only without preventing the stable Zot component from completing
its own promotion gates.

## Decision

Use only these Zot repository patterns:

```text
cache/example-user/github-actions/trusted/**
cache/example-user/github-actions/untrusted/**
cache/example-user/github-actions/promoted/**
**                                             # default deny
```

The trusted and untrusted paths have different writers. The promoted path has
a host-only writer and a separate read-only release identity. There is no admin
identity and no broad `cache/**` grant. Zot applies the longest matching glob,
so each exact recursive pattern takes precedence over the deny-all catch-all.

`gha-fleet reconcile-zot-credentials` owns the migration from the two known
bootstrap usernames. It accepts only a complete bootstrap or complete managed
state, generates independent 384-bit passwords, writes root-only files
atomically, and switches the bcrypt-cost-12 htpasswd file last. Unknown, mixed,
partial, symlinked or permission-drifted states fail closed. Command output
contains identities and paths, never passwords or hashes.

Run GC and disk-pressure fault injection only in a disposable full VM on a
dedicated 192–512 MiB loopback ext4 filesystem. The audit must:

- preserve a referenced tagged blob while offline GC deletes a distinct orphan;
- stop Zot before `verify-feature retention`, because the backend is local;
- prove the registry remains active and retained content stays readable when a
  new write receives a controlled 4xx/5xx response at filesystem exhaustion;
- reclaim the filler, restart, push and pull new content, run GC again, unmount,
  and pass read-only `e2fsck`;
- destroy the VM and its loopback image after exporting secret-free evidence.

The official retention verifier previews retention decisions, but orphan-blob
GC is destructive. This is why it can never point at an ambiguous path or run
against the mounted live registry while Zot is active.

The live authorization audit must execute from a disposable full VM attached
only to `gha0`. It proves OCI CRUD independently for the trusted, untrusted and
promoter identities, read-only promoted access for release, HTTP 403 across
every trust boundary, anonymous HTTP 401 and denial of worker access to host
SSH. The VM, temporary image and volume are destroyed before evidence is
accepted. Host load is an admission input: an audit VM is torn down before
tests if legacy workload pushes the load circuit breaker.

Both bridge-bound cache units require and start after Incus, wait up to 120
seconds for `gha0` through a sysfs-only probe, and use a 150-second systemd
startup budget. `Restart=always` is required because Zot v2.1.20 can log a bind
failure and exit with status zero. A failed reboot is retained as evidence and
cannot be repaired into an accepted result; acceptance requires a new drain
and boot with no manual cache start or restart.

## Consequences

- A compromised untrusted workflow cannot poison trusted or promoted layers.
- Release jobs cannot mutate the cache they consume.
- Promotion can be evaluated for Zot without weakening the RustFS RC gate.
- Adding a repository requires an explicit policy and credential change; there
  is no implicit organization-wide wildcard.
- Accepted authorization evidence binds the exact controller merge commit,
  deployed Zot/config/smoke digests, VM policy and post-cleanup health without
  retaining a worker credential.
- Accepted reboot evidence binds the corrective units/probe, rejected and
  successful boot IDs, monotonic Incus/cache ordering, post-boot role tests,
  secret-free journal result and retained-estate recovery.
- The storage audit proves failure behavior, not high availability. The single
  physical host remains a failure domain until a second host exists.

## References

- [Zot authentication and repository authorization](https://zotregistry.dev/v2.1.15/articles/authn-authz/)
- [Zot retention verification](https://zotregistry.dev/v2.1.15/articles/retention/)
- [Zot storage and garbage collection](https://zotregistry.dev/v2.1.18/articles/storage/)
