#!/usr/bin/env bash
# Print every module-relative directory the provider binary compiles.
#
# The release manifest promises that config/provider-derivative.yaml's
# binary_sha256 is what this tree builds. Proving that needs to know which
# sources reach the binary, and that list was written by hand: it named eight
# packages while `go list -deps` reports eighteen. internal/cachebroker and
# internal/queueintent are both real provider dependencies and were both
# missing, so a change to either skipped the reproducible-build job and left
# the manifest describing a commit main no longer builds.
#
# Reading it from the build graph cannot drift, because it is the same graph
# the compiler walks.
set -Eeuo pipefail

module=$(go list -m)
go list -deps ./cmd/garm-provider-incus-nddev |
  grep -E "^${module}(/|\$)" |
  sed -e "s|^${module}\$|.|" -e "s|^${module}/||" |
  sort -u
