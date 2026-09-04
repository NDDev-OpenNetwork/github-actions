#!/usr/bin/env bash
set -Eeuo pipefail

version=${1:?usage: write-controller-release-manifest.sh <version> <commit> <output> <binary>...}
commit=${2:?usage: write-controller-release-manifest.sh <version> <commit> <output> <binary>...}
output=${3:?usage: write-controller-release-manifest.sh <version> <commit> <output> <binary>...}
shift 3

(( $# > 0 )) || { echo "controller release has no binaries" >&2; exit 1; }
[[ "${commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "invalid controller source commit: ${commit}" >&2; exit 1; }

for binary in "$@"; do
  [[ -x "${binary}" ]] || { echo "controller binary is absent or not executable: ${binary}" >&2; exit 1; }
done

temporary="${output}.tmp"
trap 'rm -f -- "${temporary}"' EXIT

{
  printf '{\n'
  printf '  "schema_version": 1,\n'
  printf '  "version": "%s",\n' "${version}"
  printf '  "source_commit": "%s",\n' "${commit}"
  printf '  "build": {\n'
  printf '    "go_version": "%s",\n' "$(go env GOVERSION)"
  printf '    "goos": "%s",\n' "$(go env GOOS)"
  printf '    "goarch": "%s",\n' "$(go env GOARCH)"
  printf '    "cgo_enabled": false,\n'
  printf '    "trimpath": true,\n'
  printf '    "buildvcs": false,\n'
  printf '    "build_id": ""\n'
  printf '  },\n'
  printf '  "binaries": {\n'
  index=0
  count=$#
  for binary in "$@"; do
    index=$((index + 1))
    separator=,
    if (( index == count )); then separator=; fi
    printf '    "%s": "sha256:%s"%s\n' "$(basename -- "${binary}")" "$(sha256sum "${binary}" | awk '{print $1}')" "${separator}"
  done
  printf '  }\n'
  printf '}\n'
} >"${temporary}"

mv -- "${temporary}" "${output}"
trap - EXIT
