# Verification and gates

What has to pass, and what makes it blocking.

## The command

`make verify` -- and CI runs exactly that, not a subset, so a workstation and a
pull request cannot answer differently. It covers: `fmt-check`, `vet`,
`staticcheck`, `test-race`, `test-umask`, `build`, `validate`, five plan targets,
`shellcheck`, `actionlint`, `reproducible-binaries`, `vulncheck`.

`reproducible-binaries` builds six commands twice and compares digests. The GARM
derivative is separate (`make build-garm-derivative`) because it fetches upstream
and builds in a container.

The verification toolchain is read from `go.mod`'s `toolchain` directive and
nowhere else. It is **not** the toolchain baked into worker images -- that one is
pinned by `config/golden-image*.yaml` and moves with an image rebuild.

## The gate

`.github/workflows/ci.yml` publishes four checks: `Changed paths`, `Go verify`,
`GARM derivative` (path-filtered) and `Gate`. `Gate` runs on `always()`, reads
every other job's result, and treats `skipped` as a pass only for the
path-filtered derivative job.

**Branch protection requires `Gate` and nothing else.** Requiring a leaf proof
instead leaves the others advisory -- which is what happened: `Go verify` was the
required context while a failing `GARM derivative` could merge.

The desired protection is declared in `.github/branch-protection.yaml` and
applied by `scripts/branch-protection.sh --check|--apply`. The script never
repairs drift; it reports it, because a protection change is a decision.

`internal/repositorycontract` holds the required context to being the aggregate,
and exercises the gate's shell against a nine-case truth table.

## Two enforcement mechanisms

GitHub exposes rulesets and classic branch protection through different
endpoints. `main` here uses **classic protection**; the organization ruleset adds
deletion, non-fast-forward and signature requirements. A tool that reads only
`/rules/branches/{branch}` sees the second and concludes there is no protection
at all -- that produced a wrong issue once.

## Test discipline

A new test is observed **red on the defect it describes** before it is observed
green. A test that has never failed proves nothing. Every contract test in this
repository was introduced that way, and the commit message says which mutation
made it fail.
