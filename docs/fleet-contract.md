# Fleet contract

One statement of what this fleet offers, addressed to the repositories that
consume it. It exists because there was no artifact to point at: the control
plane pins this repository as a submodule and has held that pin frozen at
`2d277bd` — 108 commits back at the time of writing — waiting for
"an immutable surface:5 handoff that defines the supported runner label, the
admission and authorization contract, the rollout state, and the exact module
commit with its acceptance evidence" (`NDDev-OpenNetwork/github-device-sync#172`).

## Obtaining it

```console
$ gha-fleet fleet-contract
```

It is rendered, not committed. The contract names the commit it describes, and a
file holding its own commit is stale the moment it is written; so the tree holds
only what cannot be derived and the artifact is produced on demand.

A consumer deciding whether to pin commit `X` renders the contract at `X`:

```console
$ git -C github-actions checkout X
$ gha-fleet fleet-contract --root github-actions
```

`--commit` states the commit explicitly when the working tree is not the thing
being described.

## What is in it, and where each part comes from

| Part | Source |
| --- | --- |
| runner classes and their Docker capability | `garmbootstrap.PublishedScaleSets()` |
| tenants and their boundaries | `internal/tenant` |
| GARM version and binary digest | `config/garm-derivative.yaml` |
| provider version, interface, Incus SDK | `config/provider-derivative.yaml` |
| required merge context | `.github/branch-protection.yaml` |
| guarantees, non-guarantees, open blockers | `config/fleet-contract.yaml` |

Only the last row is written by hand. Everything else is read from the code and
manifests that already state it, and
`TestTheDeclarationRestatesNothingItsSourcesAlreadyState` fails if a derived
value is written into the declaration — a contract that restates its sources is
a contract that can disagree with them, which is how the GARM binary digest in
`docs/upstream-baseline.md` came to name a binary that had not been built for
two releases.

## What `contract_version` means

It is bumped when a consumer would have to re-read the contract to stay correct:
a runner class withdrawn, a tenancy boundary narrowed, an admission rule changed.
Adding a class or a tenant does not bump it — that only widens what is on offer,
and a consumer relying on the previous contract stays correct.

## What it deliberately does not claim

The contract describes the tree, not the fleet's live state. It does not say
which hosts are serving, how deep the warm pool is, or whether a given class has
capacity right now — those change without a commit and cannot be honestly stated
by an artifact rendered from source.

It also states no provider binary digest. The provider is not built reproducibly
to a declared digest the way the GARM derivative is, so its version names a
release rather than a build; the deployed artifact is identified by the commit
its `version` subcommand reports. That gap is `#263`, and it is listed among the
open blockers rather than papered over.
