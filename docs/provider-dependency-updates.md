# Provider dependency updates

Root `go.mod` changes can alter the in-tree Incus provider binary. Dependabot
therefore groups all minor and patch Go updates into one pull request, and an
agent completes one release boundary rather than publishing one derivative per
module.

1. Update `go.mod` and `go.sum`, run `go mod tidy` and the full Go suite, then
   commit only the dependency graph. This commit is the new provider
   `build.source_commit`.
2. Increment `config/provider-derivative.yaml::derivative_version` once.
3. From the exact source commit, build `cmd/garm-provider-incus-nddev` twice
   with the manifest's Go version, target, trimpath, disabled VCS metadata and
   release ldflags. Require byte identity and record the SHA-256.
4. In a second commit, update the provider manifest, binary digest and every
   `config/example-*.yaml` provider version. Do not amend the source commit
   after recording it.
5. Require `Reproducible provider derivative` and `Gate` to pass. A root
   dependency update without the second commit is intentionally unmergeable.

Major dependency updates remain separate because grouping must not hide a
review-significant compatibility change. GARM derivative inputs are rebuilt by
CI too, but root Go module changes do not change its vendored upstream source;
its existing reproducible digest must remain identical.
