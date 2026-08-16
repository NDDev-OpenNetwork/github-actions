#!/usr/bin/env bash
set -euo pipefail

source_dir=${ZOT_SOURCE_DIR:?ZOT_SOURCE_DIR is required}
output_path=${ZOT_OUTPUT_PATH:?ZOT_OUTPUT_PATH is required}
gomodcache=${ZOT_GOMODCACHE:?ZOT_GOMODCACHE is required}
gocache=${ZOT_GOCACHE:?ZOT_GOCACHE is required}
expected_sha256=${ZOT_EXPECTED_SHA256:?ZOT_EXPECTED_SHA256 is required}
expected_commit=${ZOT_EXPECTED_COMMIT:?ZOT_EXPECTED_COMMIT is required}
version=${ZOT_VERSION:-v2.1.20}
expected_go_version=${ZOT_GO_VERSION:-go1.26.5}
commit_description=${ZOT_COMMIT_DESCRIPTION:-v2.1.20-0-g3b5796d}

for command in git go jq realpath sha256sum; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done

for path in "${source_dir}" "${output_path}" "${gomodcache}" "${gocache}"; do
  [[ ${path} == /* && ${path} != / ]] || { printf 'all paths must be absolute and non-root\n' >&2; exit 2; }
done
[[ ${expected_sha256} =~ ^[0-9a-f]{64}$ ]] || { printf 'invalid expected SHA-256\n' >&2; exit 2; }
[[ ${expected_commit} =~ ^[0-9a-f]{40}$ ]] || { printf 'invalid expected commit\n' >&2; exit 2; }
[[ ${version} =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { printf 'invalid Zot version\n' >&2; exit 2; }
[[ ${expected_go_version} =~ ^go[0-9]+\.[0-9]+\.[0-9]+$ ]] || { printf 'invalid Go version\n' >&2; exit 2; }
[[ ${commit_description} == "${version}-0-g${expected_commit:0:7}" ]] || {
  printf 'commit description does not bind the exact version and commit\n' >&2
  exit 2
}

source_real=$(realpath "${source_dir}")
output_real=$(realpath -m "${output_path}")
gomodcache_real=$(realpath -m "${gomodcache}")
gocache_real=$(realpath -m "${gocache}")
for external_path in "${output_real}" "${gomodcache_real}" "${gocache_real}"; do
  [[ ${external_path} != "${source_real}" && ${external_path} != "${source_real}/"* ]] || {
    printf 'output and caches must remain outside the source checkout\n' >&2
    exit 2
  }
done
[[ ! -e ${output_real} ]] || { printf 'refusing to overwrite output: %s\n' "${output_real}" >&2; exit 2; }

[[ $(git -C "${source_real}" rev-parse HEAD) == "${expected_commit}" ]] || {
  printf 'source commit mismatch\n' >&2
  exit 1
}
[[ $(git -C "${source_real}" describe --always --tags --long) == "${commit_description}" ]] || {
  printf 'source tag/description mismatch\n' >&2
  exit 1
}
[[ -z $(git -C "${source_real}" status --porcelain --untracked-files=all) ]] || {
  printf 'source checkout is not clean\n' >&2
  exit 1
}
[[ $(go version | awk '{print $3}') == "${expected_go_version}" ]] || {
  printf 'Go toolchain mismatch\n' >&2
  exit 1
}

mkdir -p "$(dirname "${output_real}")" "${gomodcache_real}" "${gocache_real}"
temporary_output=$(mktemp "${output_real}.tmp.XXXXXX")
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -e ${temporary_output} ]]; then
    rm -f -- "${temporary_output}"
  fi
  exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

go_env=(
  "GOTOOLCHAIN=local"
  "GOENV=off"
  "GOWORK=off"
  "GO111MODULE=on"
  "GOFLAGS=-mod=readonly"
  "GOPROXY=https://proxy.golang.org,direct"
  "GOSUMDB=sum.golang.org"
  "GOMODCACHE=${gomodcache_real}"
  "GOCACHE=${gocache_real}"
)
(
  cd "${source_real}"
  env "${go_env[@]}" go mod download
  env "${go_env[@]}" go mod verify >&2
  env "${go_env[@]}" \
    CGO_ENABLED=0 GOEXPERIMENT=jsonv2 GOOS=linux GOARCH=amd64 GOAMD64=v1 \
    go build -o "${temporary_output}" -buildmode=pie -trimpath \
    -ldflags "-X zotregistry.dev/zot/v2/pkg/buildinfo.ReleaseTag=${version} -X zotregistry.dev/zot/v2/pkg/buildinfo.Commit=${commit_description} -X zotregistry.dev/zot/v2/pkg/buildinfo.BinaryType=minimal -X zotregistry.dev/zot/v2/pkg/buildinfo.GoVersion=${expected_go_version} -s -w" \
    ./cmd/zot
)

actual_sha256=$(sha256sum "${temporary_output}" | awk '{print $1}')
[[ ${actual_sha256} == "${expected_sha256}" ]] || {
  printf 'reproduced binary SHA-256 mismatch: got %s\n' "${actual_sha256}" >&2
  exit 1
}
build_metadata=$(go version -m "${temporary_output}")
for required in \
  '-buildmode=pie' \
  '-trimpath=true' \
  'CGO_ENABLED=0' \
  'GOARCH=amd64' \
  'GOEXPERIMENT=jsonv2' \
  'GOOS=linux' \
  'GOAMD64=v1' \
  "vcs.revision=${expected_commit}" \
  'vcs.modified=false'; do
  grep -Fx $'\tbuild\t'"${required}" <<<"${build_metadata}" >/dev/null || {
    printf 'reproduced binary is missing build metadata: %s\n' "${required}" >&2
    exit 1
  }
done

chmod 0755 "${temporary_output}"
mv "${temporary_output}" "${output_real}"
trap - EXIT INT TERM
jq -n \
  --arg schema_version '1' \
  --arg repository 'project-zot/zot' \
  --arg version "${version}" \
  --arg source_commit "${expected_commit}" \
  --arg commit_description "${commit_description}" \
  --arg go_version "${expected_go_version}" \
  --arg output_sha256 "${actual_sha256}" \
  '{
    schema_version: ($schema_version | tonumber),
    repository: $repository,
    version: $version,
    source_commit: $source_commit,
    commit_description: $commit_description,
    toolchain: $go_version,
    module_verification: true,
    build: {
      cgo_enabled: false,
      goexperiment: "jsonv2",
      goamd64: "v1",
      build_mode: "pie",
      trimpath: true,
      vcs_modified: false
    },
    output_sha256: $output_sha256,
    release_asset_match: true
  }'
