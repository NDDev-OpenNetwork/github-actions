# Dedicated GitHub App bootstrap

Status: manifest bootstrap and fail-closed GARM API reconciliation are
implemented and locally tested; live owner confirmation and Scale Set canary
remain deployment gates.

GARM uses a GitHub App owned by the account it serves and installed
account-wide, so one fleet can serve every repository that account holds; a
narrower `selected` installation remains valid and is verified the same way.

One account is one App. `--tenant <id>` selects which registered account is
being bootstrapped and supplies the repository, App name and homepage; the
overrides remain so a mismatch can be expressed and refused, not configured.
The set of accounts lives in `internal/tenant` and is closed: registering an
App is the single irreversible step in this flow, so an account the reconciler
would later refuse fails here, before a key exists.

The App is **private**, which is the control that keeps it scoped. A public App
can be installed by anyone, and GitHub offers no allowlist of permitted
installers; a private App can only ever be installed on the account that owns
it. Private plus organization-owned therefore means exactly one installation is
possible, on this organization, covering only its repositories. A broad PAT
is still not an accepted substitute, because the App carries exactly two
permissions and no webhook events, while a PAT carries the user's whole
account. At
repository scope, GitHub's official runner guidance requires
`Administration: Read and write`; `Metadata: Read-only` is implicit. Webhook
permission and webhook subscriptions are omitted because this deployment uses
the outbound Runner Scale Set listener and GARM webhook management is disabled.

GitHub does not expose GitHub App registration as an authenticated `gh` or REST
mutation. Its official manifest flow contains one account-owner confirmation
in the browser. `gha-fleet bootstrap-github-app` confines that flow to a random
loopback callback and independently verifies the resulting App before
persisting its one-time key:

- listener address must be `127.0.0.1`;
- CSRF state is 256 random bits;
- manifest `redirect_url` and `setup_url` values have no pre-existing query or
  fragment; state is carried in GitHub's documented registration and
  installation URL parameters and verified on both loopback callbacks;
- the App is private and has no webhook events or active webhook;
- only `administration=write` is requested;
- the App must be owned by the `NDDev-OpenNetwork` organization, and the
  installation must target that same account; the owning kind is an explicit
  `--owner-type` input, never inferred, so a personal App can never satisfy an
  organization contract by accident;
- repository selection must be `selected` or `all`, and nothing else;
- a short-lived installation token proves the actual scope: a `selected`
  installation is enumerated and must expose exactly
  `NDDev-OpenNetwork/github-actions`, while an `all` installation is proven by
  resolving that repository through the token itself, which is exact where
  paginating an account-wide listing would not be;
- any extra permission, repository or owner fails closed;
- private key and verified metadata are written only to a new mode-`0700`
  directory, with mode-`0600` files.

The callback shape follows GitHub's manifest handshake: the registration
request carries `state`, GitHub appends `code` and `state` when redirecting to
`redirect_url`, and the App installation URL carries the same `state` before
GitHub returns to `setup_url`. See GitHub's official
[manifest-flow documentation](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest)
and [installation-link state documentation](https://docs.github.com/en/apps/sharing-github-apps/sharing-your-github-app#sharing-your-github-app-via-an-install-link).

Run from the trusted desktop checkout:

```bash
bootstrap_suffix="$(openssl rand -hex 8)"
bootstrap_output="${XDG_RUNTIME_DIR}/nddev-garm-app-${bootstrap_suffix}"
go run ./cmd/gha-fleet bootstrap-github-app \
  --repository NDDev-OpenNetwork/github-actions \
  --owner-type organization \
  --app-name nddev-gha-fleet \
  --homepage https://github.com/NDDev-OpenNetwork/github-actions \
  --output-dir "${bootstrap_output}" \
  --open-browser \
  --timeout 20m
```

For any other registered account, name the tenant and let the registry supply
the repository, App name and homepage:

```bash
bootstrap_suffix="$(openssl rand -hex 8)"
bootstrap_output="${XDG_RUNTIME_DIR}/guild-garm-app-${bootstrap_suffix}"
go run ./cmd/gha-fleet bootstrap-github-app \
  --tenant guild \
  --owner-type organization \
  --organization-runners \
  --output-dir "${bootstrap_output}" \
  --open-browser \
  --timeout 20m
```

`--organization-runners` adds `organization_self_hosted_runners: write` to the
manifest. An organization entity needs it — `POST /orgs/{org}/actions/runners/
registration-token` is not covered by repository `administration` — and a
repository entity does not, so it is requested on demand rather than always. An
App created without it cannot back an organization entity, and the reconciler
now refuses that pairing before creating anything rather than after the entity
exists and the first scale set fails on a 403.

The browser confirmation is GitHub's, not this tool's: an App is created under
the account that approves the manifest, so the person holding that account has
to be the one who approves it. Everything either side of that step — the
loopback listener, the CSRF state, the independent verification of the App that
comes back, and where the key is written — is what this command owns.

The browser shows GitHub's own reviewed registration page and then its
installation page. Because `--owner-type organization` is the default, that
registration page is the organization form,
`https://github.com/organizations/NDDev-OpenNetwork/settings/apps/new`; the personal
form cannot create an organization-owned App and is reached only by asking for
`--owner-type user`. Confirm the private App, then install it on
`NDDev-OpenNetwork`. The deployed installation is account-wide, which the flow
verifies by resolving the managed repository through the installation token
itself; a narrower `selected` installation is equally acceptable and is
enumerated exactly.
The command does not complete merely because the user clicked Install: it mints
a short-lived installation token and verifies account, permission and exact
repository scope through GitHub's API.

The output bundle is staging material, not a backup. It has a 24-hour import
window. Transfer only its two files over SSH into a new root-owned mode-`0700`
directory under `/run`; preserve file mode `0600`. Never place the bundle in
this repository, a golden image, shell history or a general secrets directory
shared with workers.

## When only the bundle is stale

An App's permissions can change after its bundle was written — a permission
added on the App and approved on the installation is an ordinary thing to do.
The bundle records what it was written with, and `garmbootstrap.Reconcile`
reads permissions from that file rather than from GitHub, deliberately, because
the file is what a reviewer approved. So the installation is correct, the App is
correct, and the bundle is wrong about both.

`bootstrap-github-app` cannot fix that: the manifest flow always creates a new
App, so the remedy was a second App, a second installation, a new anchor and a
registry row edited to match — all to correct one JSON file.

```bash
go run ./cmd/gha-fleet verify-github-app \
  --tenant example-media \
  --owner-type organization \
  --organization-runners \
  --app-id 100003 \
  --installation-id 200003 \
  --key /run/user/1000/<bundle>/github-app-private-key.pem \
  --output-dir "${XDG_RUNTIME_DIR}/<new-bundle>"
```

It re-reads the live installation and writes a current bundle for the App that
already exists. Every check the bootstrap applies after approval applies here,
because it is the same `verifyInstallation`: the installation must belong to the
tenant's account, its repository selection must be one this deployment accepts,
and its permissions must be exactly the least-privilege set the tenant asked
for. What it omits is only the step that creates an App.

The key is copied rather than reissued, so **the credential anchor does not
change** and does not need re-reviewing. Verified on the `example-media` bundle:
the same PKIX SHA-256 before and after.

It needs no browser and no sudo mode, which is the point — the human step in
the bootstrap exists because an App is created under whoever approves the
manifest, and nothing is created here.

The public-key identity is a separate, non-secret review anchor. Compute the
same PKIX public-key SHA-256 used by the reconciler, record it with the App and
installation IDs, review that change, then install the file owned by
`root:garm` and mode `0640`:

| Tenant | Reviewed in | Installed as |
| --- | --- | --- |
| `nddev` | `config/garm-credential-anchor.json` | `/etc/gha-fleet/garm-credential-anchor.json` |
| any other | `config/garm-credential-anchor-<tenant>.json` | `/etc/gha-fleet/garm-credential-anchor-<tenant>.json`, passed with `--credential-anchor` |

One anchor per tenant, because `loadCredentialAnchor` refuses a file whose
`credential_name` is not the selected tenant's — a single shared file would
make every reconciliation but one fail closed, which is the right failure and
still the wrong file. The default path stays the default so the deployed
`nddev` reconciliation is unchanged.

```bash
openssl rsa -in github-app-private-key.pem -pubout -outform DER 2>/dev/null |
  sha256sum
```

The anchor detects credential replacement or key drift but cannot authenticate
to GitHub. It is safe to retain and is mandatory for every reconciliation.

GARM `v0.2.1` supports the GitHub Runner Scale Set `disable_update` field in its
API, but its pinned CLI does not expose that field during Scale Set creation.
Using `garm-cli scaleset add` would therefore silently enable runner binary
updates and violate the immutable-image policy. The repository-owned
`reconcile-garm` command calls the loopback API directly and verifies the
returned value.

Build and install the reviewed controller binary, then run on the server:

```bash
sudo /usr/local/bin/gha-fleet reconcile-garm \
  --app-bundle /run/gha-garm-app-import

sudo /usr/local/bin/gha-fleet reconcile-garm \
  --app-bundle /run/gha-garm-app-import \
  --apply
```

The omitted `--scale-set` defaults to `nddev-linux-standard`, preserving the
original pilot contract. The first invocation is read-only. The second may
create only missing credential, repository and disabled Scale Set resources.
It fails on duplicate names or any observed drift; it never updates a
mismatched resource. Partial failure is safe to rerun because each completed
resource is idempotently reconciled and the Scale Set starts disabled.

Inspect the second invocation's redacted JSON. It must report both
`disable_runner_update: true` and `immutable_guest_updates_disabled: true`,
`ready_to_enable: true`, capacity `1/0`, remote shell false and the exact image
alias. Enable only through a separate invocation:

```bash
sudo /usr/local/bin/gha-fleet reconcile-garm \
  --app-bundle /run/gha-garm-app-import \
  --apply \
  --enable
```

`--enable` refuses to bootstrap any missing resource. It can only change the
enabled bit of an already-created Scale Set after every immutable field has
been revalidated. Once the credential exists and the anchor matches it, delete
the exact local bundle and exact `/run/gha-garm-app-import` directory. GARM
keeps the imported key encrypted at rest; subsequent pool operations neither
need nor accept an invented replacement key.

The Docker-capable pool is a separate, explicit reconciliation target. Create,
inspect and enable it only after the integration image and provider mapping are
already live and the loopback observer is healthy:

```bash
sudo /usr/local/bin/gha-fleet reconcile-garm \
  --scale-set nddev-linux-integration
sudo /usr/local/bin/gha-fleet reconcile-garm \
  --scale-set nddev-linux-integration \
  --apply
sudo /usr/local/bin/gha-fleet reconcile-garm \
  --scale-set nddev-linux-integration \
  --apply \
  --enable
```

No other scale-set name is accepted. If the anchored GARM credential is ever
missing, anchor-only reconciliation fails closed and requires a separately
verified one-time bundle to recreate it; it never generates or recovers a key.

Create the disabled pilot with these closed values:

| Field | Value |
| --- | --- |
| credential | `nddev-gha-fleet` / `app` / `github.com` |
| entity | repository `NDDev-OpenNetwork/github-actions`, or the `NDDev-OpenNetwork` organization with `--entity-kind organization` |
| webhook | not installed; random unused schema guard only |
| Scale Set | `nddev-linux-standard` |
| provider | `nddev-incus` |
| image | `nddev-ubuntu-24.04-amd64-current` (provider pins its exact fingerprint) |
| flavor | `nddev-linux-standard` |
| OS | `linux` / `amd64` |
| capacity | min idle `0`, max runners `1` |
| runner group | repository-scoped `Default` |
| runner update | `disable_update=true` |
| extra specs | `{"disable_updates":true}` |
| remote shell | disabled |
| initial state | disabled until inspection, then a single canary job |

## Which forge entity the scale sets hang from

GARM binds a scale set to one forge entity, and that choice decides which
repositories can reach the pool behind it.

A **repository** entity, the default, serves exactly the repository it names.
This is what the fleet was built on and what is deployed: the scale sets under
`NDDev-OpenNetwork/github-actions` serve that repository and nothing else.

An **organization** entity, selected with `--entity-kind organization`, serves
every repository the organization holds, including ones created later. This is
what lets one fleet replace listeners that were shared across an account, and
it is deliberately opt-in rather than inferred, because it widens who can reach
these pools. GARM itself expresses no repository filter for an organization
entity; if one is wanted, it belongs on the GitHub runner group the scale set
is created in, which is `Default` today.

The reconciler validates the entity it finds against the kind it was asked for
and fails closed on a mismatch, so an organization run will not silently adopt
a repository entity or the reverse. GARM populates exactly one owner field per
scale set according to its entity, and scale-set validation selects the field
by kind rather than accepting whichever is non-empty.

**Onboarding a tenant requires redeploying the provider.** The Incus provider
asserts its own boundary on every bootstrap rather than trusting what GARM hands
it: repository URL, callback URL, metadata URL and runner group must all match
what it was built with. The repository half is derived from the tenant registry,
so adding a tenant there widens it — but only in a newly built binary. Until
`garm-provider-incus-nddev` is rebuilt and redeployed on the host, a new tenant
completes every earlier step and then fails per retry with
`repository is outside the configured provider boundary`, with the workflow
still showing nothing but a queued job.

**An organization entity cannot serve jobs today.** The queue admission pilot
in the GARM overlay builds a queue intent only for a repository entity
(`queueIntentFromLifecycle` requires `ForgeEntityTypeRepository`). GitHub
registers an organization scale set and assigns jobs to it normally, so the
failure does not look like a refusal: the listener logs
`job "<id>" has incomplete queue admission identity` in a retry loop, no runner
is ever created, and the workflow queues until it is cancelled. Observed on
`example-guild` on 2026-08-12; that tenant now runs on a repository
entity. Until the admission pilot learns organization entities,
`--entity-kind organization` creates resources that cannot be used.

The integration target differs only in the three pool identity fields:
Scale Set and flavor `nddev-linux-integration`, plus image
`nddev-ubuntu-24.04-amd64-docker-current`. Capacity, update locks, remote-shell
policy, repository boundary and provider remain identical.

The workflow selector for the automated pilot is the Scale Set name
`nddev-linux-standard`. The manual JIT proof uses `nddev-canary`; both choices
are explicit inputs to `self-hosted-canary.yml`.

Dispatch and inspect the first managed proof only through `gh`:

```bash
gh workflow run self-hosted-canary.yml \
  --repo NDDev-OpenNetwork/github-actions \
  -f runner_label=nddev-linux-standard \
  -f mode=basic
gh run list --repo NDDev-OpenNetwork/github-actions --workflow self-hosted-canary.yml
```
