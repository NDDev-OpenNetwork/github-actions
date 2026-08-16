# Cache plane

A trust-scoped compiler cache on RustFS, delivered per job and never baked into
an image.

## Namespace

`internal/cachenamespace` is the one place a key is built. Shape:

```text
{organization}/{repository}/trust/{trust}/{platform}/{architecture}/{toolchain}/{lock_digest}/{ref_class}
```

`trust` is a literal segment; `{trust}` is one of `trusted`, `untrusted`,
`promoted`. `config/server-gha-runner-*.yaml` declares this template and
`internal/config` holds it to `cachenamespace.Template()` -- it used to only
check that each token appeared somewhere, so the declared template described
eight segments while the code wrote nine.

The root was a literal in 24 places across five files before it was one function.
Two files nobody had inventoried: `internal/rustfscache/reconcile.go`, which also
derived lifecycle-rule identifiers by trimming the root, and
`.github/workflows/self-hosted-canary.yml`.

## Trust classes and who may write

| Class | Written by | Retention |
| --- | --- | --- |
| `trusted` | jobs on a reviewed ref of a reviewed repository | 30 days |
| `untrusted` | everything else; disposable | 7 days |
| `promoted` | the promoter, from the host only | 90 days |

`release-reader` reads `promoted` and writes nothing. The promoter role is never
delivered to a worker.

## Delivery

A worker receives one credential for one role, scoped to one prefix root, for one
job. The role list is declared once in
`internal/garmproviderincus/provider/cache_delivery.go` and rendered into both
guest jq expressions, so the host and the guest cannot enforce different terms.
The clause is substituted, not formatted, because the guest scripts are full of
`printf` verbs.

## Open

The organization and repository are constants: every tenant shares one
read-write namespace (#236). Making them per-tenant is now a change in one place
rather than twenty-four, which is what the refactor was for. Live IAM, RustFS and
Zot operations are outside the repository's authority.
