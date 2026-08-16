# Upstream baseline

Observed on 2026-08-08. These are pilot candidates, not permission to float to a
new release. Production consumes exact binary/container/image digests recorded
in the golden-image provenance.

| Component | Candidate / source | Linux amd64 release digest | Policy |
| --- | --- | --- | --- |
| GitHub Actions runner | [`v2.336.0`](https://github.com/actions/runner/releases/tag/v2.336.0) | `sha256:04cf0be1aff4c3ec3554466c39124ca250e3effd8873bb7e8d68535aa9505d5d` | official runner only; image-canary updates |
| GARM | NDDev `v0.2.1-nddev.15`, derived from [`v0.2.1`](https://github.com/cloudbase/garm/releases/tag/v0.2.1), commit `154638445c3949c1958b01812f69d9a1e4d82684` | upstream asset `sha256:11176acb8a725f914b9b947891b4837d374fb616195562cc0ad45a7be8b6c746`; NDDev binary `sha256:35a93c8d009c2f5667e5f63c31b5f35db54b6c699b6374eaba3f2ef70f55348a` | A queue of configurable width. max_in_flight is the fleet's concurrency and the scale set's own max_runners is GitHub's and GARM's; nddev.14 asserted both were 1, so raising a scale set to 3 made every delivered message unacknowledgeable and the listener spun without placing anything. The width is bounded at 64, a per-repository share may not exceed it, and SelectForAcquire now reserves every eligible candidate in a message instead of one per round trip -- safe because only tryAdmitQueueIntent charges the budget and advances the stride scheduler. Plus nddev.14 behaviour: a started job with no intent is acknowledged rather than poisoning the listener. Plus nddev.13 behaviour, an assigned intent that expires on the acquired TTL instead of the execution TTL, so a job that wins the slot and never advances no longer holds a one-wide class for twenty-four hours. nddev.12 was nddev.11 minus the message-batch refusal that made a backlog refuse itself: how many jobs GitHub batches into one protocol message is GitHub's decision, and the single acquisition slot is enforced in SelectForAcquire regardless. Plus, from nddev.11, a queue coordinator that admits an organization entity: a live JobAssigned message names only the job, so such an intent is admitted against its account and binds the repository when JobAvailable arrives, to a repository that account owns and no other |
| GARM Incus provider | NDDev `v0.1.5-nddev.32`, derived from [`v0.1.5`](https://github.com/cloudbase/garm-provider-incus/releases/tag/v0.1.5) commit `f3ae31910c6443c31d841de268a377985e7c60a5` | upstream asset `sha256:1489b5f9b3f01528e338c604c13dabe8321ed6f1bc6de77c7344119d7731c43f`; the NDDev binary is not reproducible from this table alone -- the version names a release, not a build, so the deployed artifact is identified by the commit its `version` subcommand reports (#263) | nddev.31 behaviour, plus: the queue host is a supported platform and the clustered admission path never probes the local machine -- a dedicated queue host runs no hypervisor, so every KVM check there would close admission for a healthy fleet. Plus, from nddev.31: the Incus endpoint may be a cluster member inside the fleet private network rather than only loopback; host state for admission is summed across online cluster members instead of probed from whichever machine runs the provider; and the example-guild tenant serves its whole account, because its scale sets hang from an organization entity and its jobs arrive from repositories the registry does not name one by one. Plus nddev.29 behaviour: schema-2 typed execution backends binding every pool to an explicit platform, architecture, implementation and failure domain; Incus SDK `v7.3.0`; interface `v0.1.0` |
| Incus | Ubuntu `6.0.0-1ubuntu0.3`; upstream [`v6.0.0`](https://github.com/lxc/incus/releases/tag/v6.0.0), commit `714bcc5e42b189f54025b8567df1f3408a1cae2c` | distribution package | 6.0 LTS API contract; security updates through Ubuntu |
| Ubuntu worker source | Canonical Noble release `20260801` | disk `sha256:0533b0655c32e68b31d792ecd6ccfca95abdbc536c4446874fe0513bd4140ffe`; metadata `sha256:4881b54323d62bb2a791a48c5bfa841492e55cf7a27af18b047edc904d595051` | verify signed `SHA256SUMS` with pinned UEC signer before import |
| RustFS | [`1.0.0-rc.1`](https://github.com/rustfs/rustfs/releases/tag/1.0.0-rc.1), commit `778f1dfa2155cbbc61ad54e6896de9e29d2c4d8d` | archive `sha256:01c03ba58adfca757578729501f4f886a98f13acfb18be5a2db60e04f5cbb595`; binary `sha256:eb63af6574150a62a4509461f16b178976e67485ce6beacf41e6b67944d41db0` | pre-release canary only; S3 API only; optional protocols and STS disabled |
| Zot OCI registry | [`v2.1.20`](https://github.com/project-zot/zot/releases/tag/v2.1.20), commit `3b5796d834e8661ea661a5fcc47add8d4405aebf` | minimal asset `sha256:902ea958c4a59c0f5c4ac9fa2bbaad8716e80551bcaede7ab4ea998bf57190a6` | production-ready; minimal filesystem build, extensions omitted, reproducible provenance and storage/authz/reboot gates passed |
| sccache | [`v0.17.0`](https://github.com/mozilla/sccache/releases/tag/v0.17.0), commit `c037e117c7625a2668633574028a6addf2a96a6e` | musl archive `sha256:67c4a96dd237c1f518f6b36083f270f9976d516f1e57fce891755ea782e50006`; binary `sha256:066c5a84c85044c8f48b3ab571ac114293ea717c3d36985db022af8206e21e63` | baked into immutable workers; exact archive layout, version and binary digest verified; RustFS use remains canary-only |

RustFS's published SLSA v1 statement binds the selected archive digest to the
exact GitHub-verified commit and build workflow; its CycloneDX SBOM and unpacked
binary digest are pinned separately. GitHub's artifact-attestation endpoint has
no record for this release, so the published provenance file is verified as an
artifact rather than treated as a GitHub-hosted attestation. The RC remains
production-blocked even though real CRUD, multipart, restart and `SIGKILL`
recovery, IAM isolation, quota enforcement and lifecycle expiry passed.

Zot's release has a GitHub-verified source commit and matching release
checksums, but no GitHub artifact attestation. Source-mode `govulncheck v1.6.0`
found zero called vulnerabilities for `./cmd/zot`; the minimal import graph
does not include the OpenPGP or S3 Crypto packages conservatively reported by
stripped-binary analysis. Two clean checkouts with separate module and build
caches reproduce the release asset byte-for-byte using the pinned Go build
contract. The runtime reports `binary-type=minimal` and the expected commit.
The machine-readable records and their digest binding are in
`config/zot-v2.1.20-{reproducibility,storage-audit,authz-audit,reboot-audit}.json`
and `config/cache-artifacts.yaml`. GC, full-disk, repository-scoped identity
and automatic reboot gates passed; Zot is promoted independently while RustFS
RC.1 remains canary-only.

The inspected GARM and provider tags are annotated but not cryptographically
signed. The digests above are GitHub release-asset metadata, not a substitute
for independent download verification and NDDev provenance. Candidate cache
data-plane behavior was first exercised in isolated disposable audit VMs after
digest and source/release verification. The live cache services then remained
isolated from retained application tenants and production runner credentials.
Zot's native configuration verifier also ran on the build workstation.

The Incus/provider runtime probe is a documented exception to the stock-binary
candidate: Incus 6.0.0 exposes nested CPU features despite
`security.nesting=false`, while the provider does not accept an instance config
map. The NDDev provider build remains source-bound to the commit above and adds
only the fixed `raw.qemu=-cpu host,-vmx,-svm` VM policy. Its resulting binary
digest is recorded separately before deployment.

## Update process

1. Fetch release metadata and checksums from the upstream project.
2. Record source tag, commit and artifact digest.
3. Verify signatures/checksums where upstream publishes them.
4. Build or import into the controlled image pipeline.
5. Run static/security scan and representative parity suite.
6. Deploy to a zero-weight canary pool.
7. Inject restart, cancellation and failed-delete scenarios.
8. Promote gradually; retain the previous digest for rollback.

GitHub's required runner update window is monitored independently of normal
dependency cadence. Disabling in-VM auto-update transfers that responsibility
to this pipeline; it does not remove it.

`make verify` pins `staticcheck v0.7.0` and `govulncheck v1.6.0`, and rejects
static-analysis findings or symbol-reachable Go vulnerabilities. The original
provider dependency on `github.com/lxc/incus v0.7.0` failed this gate and is
intentionally replaced with `github.com/lxc/incus/v7 v7.3.0`. Server-side Incus
remains the Ubuntu 6.0 LTS package; the newer client is validated against that
API before rollout.

The Ubuntu source is an exact dated release directory, not the mutable
`release/` alias. `config/golden-image.yaml` pins the signer fingerprint,
artifact names and digests. The build records the resulting package manifest,
recipe digest and Incus fingerprint; those runtime outputs are the deployment
and rollback identity.

## Bootstrap CI actions

Repository CI initially runs on GitHub-hosted `ubuntu-latest` so it does not
depend on the fleet being built. Actions are pinned by commit SHA:

- `actions/checkout` at `3d3c42e5aac5ba805825da76410c181273ba90b1`;
- `actions/setup-go` at `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`;
- `actions/upload-artifact` at
  `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a`.

Dependabot may propose updates, but promotion remains review- and test-gated.
