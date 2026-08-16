#!/usr/bin/env bash
# Rebuild the in-tree GARM derivative from config/garm-derivative.yaml.
#
# The manifest is the input, not a description of one. Everything the build
# decides -- which commit, which patches and at what digests, which container,
# which toolchain, which tags, what the output must hash to -- is rendered into
# the generated region below by `make garm-derivative-script`, and
# TestGeneratedRegionIsCurrent fails if the region and the manifest disagree.
#
# Nothing outside the region may restate a manifest value. That is the whole
# point: this script used to carry every value as its own literal, and one of
# them -- the fifth patch digest -- was checked by nothing at all.
set -Eeuo pipefail

# BEGIN GENERATED REGION -- do not edit
# generator: gha-fleet render-garm-build
# edit-source: config/garm-derivative.yaml
#
# Every value below is the manifest's. Editing one here detaches the build
# from the provenance it is reviewed against, which is why the region is
# regenerated and compared rather than maintained.
readonly derivative_version="v0.2.1-nddev.15"
readonly upstream_repository="https://github.com/cloudbase/garm"
readonly upstream_commit="154638445c3949c1958b01812f69d9a1e4d82684"
readonly build_image="docker.io/library/golang@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599"
readonly build_go_version="go1.26.5"
readonly build_cgo_enabled="1"
readonly build_target_os="linux"
readonly build_target_arch="amd64"
readonly build_network="none"
readonly build_module_mode="vendor"
readonly build_tags="osusergo,netgo,sqlite_omit_load_extension"
readonly build_reproducible_rebuilds="2"
readonly build_maximum_required_glibc="2.34"
readonly expected_binary_sha256="35a93c8d009c2f5667e5f63c31b5f35db54b6c699b6374eaba3f2ef70f55348a"
readonly patch_paths=(
  "third_party/garm/patches/0001-event-driven-reconciliation.patch"
  "third_party/garm/patches/0002-central-queue-admission.patch"
  "third_party/garm/patches/0003-failed-scale-set-runner-cleanup.patch"
  "third_party/garm/patches/0004-direct-jit-provider-handoff.patch"
  "third_party/garm/patches/0005-direct-jit-phase-telemetry.patch"
)
readonly patch_sha256s=(
  "2f0571f141e7388d6ea0cb0341549ba5bf5dab26d0006382a71b76655e272d34"
  "2727ad8b3e92f7800af1251c032cd3b0a6d5b50babcbdab65bffe59a69d76131"
  "0cd77616af4160eae7be51f91340841b30e13d4b20ea5873e004c1b81d7879e1"
  "d23873baf6689c7e4cc366b14979ad86367ef9e321cb6d5d2745c78e64a3c172"
  "49de677a1c483e58cc009ef8f23d301ef4647ea01aec4100a3e6c49376f7889f"
)
readonly overlay_paths=(
  "third_party/garm/overlay/workers/scaleset/queue_intent.go"
  "third_party/garm/overlay/workers/scaleset/queue_intent_test.go"
)
readonly overlay_sha256s=(
  "874f3810a8d981eed3b4fe9c6bcb399604a1656e050818063832f3b49d904e4f"
  "48f08a2b8c0c7c13000cb3368911cee945015bfc5cfc3468d21513fef7f8b269"
)
readonly overlay_targets=(
  "workers/scaleset/queue_intent.go"
  "workers/scaleset/queue_intent_test.go"
)
# END GENERATED REGION

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output_path="${1:-${repo_root}/dist/garm-${derivative_version}-${build_target_os}-${build_target_arch}}"
container_engine="${CONTAINER_ENGINE:-docker}"

for command in awk cmp git install mkdir mktemp readelf realpath sha256sum; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is unavailable: ${command}" >&2
    exit 1
  fi
done
if ! command -v "${container_engine}" >/dev/null 2>&1; then
  echo "container engine is unavailable: ${container_engine}" >&2
  exit 1
fi

verify_digest() {
  local path=$1
  local expected=$2
  local label=$3
  local actual
  actual=$(sha256sum "${path}" | awk '{print $1}')
  if [[ "${actual}" != "${expected}" ]]; then
    echo "GARM ${label} digest mismatch: got ${actual}, want ${expected}" >&2
    exit 1
  fi
}

# Every declared patch and overlay is verified, and the loop bound is the list
# length rather than a number written next to it. Five patches used to be five
# hand-written pairs of variables, and the contract test enumerated four.
if [[ "${#patch_paths[@]}" -ne "${#patch_sha256s[@]}" ]]; then
  echo "generated region is inconsistent: ${#patch_paths[@]} patch paths, ${#patch_sha256s[@]} digests" >&2
  exit 1
fi
if [[ "${#overlay_paths[@]}" -ne "${#overlay_sha256s[@]}" ]] || [[ "${#overlay_paths[@]}" -ne "${#overlay_targets[@]}" ]]; then
  echo "generated region is inconsistent: overlay paths, digests and targets differ in length" >&2
  exit 1
fi
for index in "${!patch_paths[@]}"; do
  verify_digest "${repo_root}/${patch_paths[index]}" "${patch_sha256s[index]}" "patch $((index + 1))"
done
for index in "${!overlay_paths[@]}"; do
  verify_digest "${repo_root}/${overlay_paths[index]}" "${overlay_sha256s[index]}" "overlay $((index + 1))"
done

work_dir=$(mktemp -d)
cleanup() {
  if [[ -n "${work_dir:-}" && "${work_dir}" == /tmp/* && -d "${work_dir}" ]]; then
    rm -rf -- "${work_dir}"
  fi
}
trap cleanup EXIT

source_dir="${work_dir}/source"
artifact_dir="${work_dir}/artifacts"
mkdir -p "${artifact_dir}"
git init --quiet "${source_dir}"
git -C "${source_dir}" remote add origin "${upstream_repository}"
git -C "${source_dir}" fetch --quiet --depth=1 origin "${upstream_commit}"
git -C "${source_dir}" checkout --quiet --detach FETCH_HEAD

actual_commit=$(git -C "${source_dir}" rev-parse HEAD)
if [[ "${actual_commit}" != "${upstream_commit}" ]]; then
  echo "GARM source commit mismatch: got ${actual_commit}, want ${upstream_commit}" >&2
  exit 1
fi
for index in "${!patch_paths[@]}"; do
  patch_path="${repo_root}/${patch_paths[index]}"
  git -C "${source_dir}" apply --check "${patch_path}"
  git -C "${source_dir}" apply "${patch_path}"
done
for index in "${!overlay_paths[@]}"; do
  install -m 0644 "${repo_root}/${overlay_paths[index]}" "${source_dir}/${overlay_targets[index]}"
done
git -C "${source_dir}" diff --check

"${container_engine}" image inspect "${build_image}" >/dev/null 2>&1 ||
  "${container_engine}" pull "${build_image}" >/dev/null

# The single-quoted program is intentionally expanded only inside the build
# container. Every manifest value it needs arrives through --env, so the program
# text holds no copy of one: a literal in here would be a value the generated
# region could not reach and the contract test could not see. The derivative
# version was exactly that -- compiled into the binary from a literal that
# nothing compared against the manifest.
# shellcheck disable=SC2016
"${container_engine}" run --rm \
  --network "${build_network}" \
  --mount "type=bind,src=${source_dir},dst=/src,readonly" \
  --mount "type=bind,src=${artifact_dir},dst=/out" \
  --env GOTOOLCHAIN=local \
  --env GOPROXY=off \
  --env GOSUMDB=off \
  --env HOME=/tmp/home \
  --env GOCACHE=/tmp/go-cache \
  --env GOTMPDIR=/tmp/go-build \
  --env "NDDEV_GO_VERSION=${build_go_version}" \
  --env "CGO_ENABLED=${build_cgo_enabled}" \
  --env "GOOS=${build_target_os}" \
  --env "GOARCH=${build_target_arch}" \
  --env "NDDEV_MODULE_MODE=${build_module_mode}" \
  --env "NDDEV_BUILD_TAGS=${build_tags}" \
  --env "NDDEV_DERIVATIVE_VERSION=${derivative_version}" \
  --env "NDDEV_REBUILDS=${build_reproducible_rebuilds}" \
  --workdir /src \
  "${build_image}" \
  bash -Eeuo pipefail -c '
    mkdir -p "${HOME}" "${GOCACHE}" "${GOTMPDIR}"
    test "$(go env GOVERSION)" = "${NDDEV_GO_VERSION}"
    test "$(go env GOOS)" = "${GOOS}"
    test "$(go env GOARCH)" = "${GOARCH}"
    test "$(go env CGO_ENABLED)" = "${CGO_ENABLED}"
    go vet "-mod=${NDDEV_MODULE_MODE}" -tags testing ./workers/provider ./workers/scaleset
    go test -race "-mod=${NDDEV_MODULE_MODE}" -tags testing -timeout=15m -parallel=4 -count=1 ./...
    build() {
      go build \
        "-mod=${NDDEV_MODULE_MODE}" \
        -trimpath \
        -buildvcs=false \
        -tags "${NDDEV_BUILD_TAGS}" \
        -ldflags="-buildid= -s -w -X github.com/cloudbase/garm/util/appdefaults.Version=${NDDEV_DERIVATIVE_VERSION}" \
        -o "$1" ./cmd/garm
    }
    build /out/garm.1
    for attempt in $(seq 2 "${NDDEV_REBUILDS}"); do
      go clean -cache
      build "/out/garm.${attempt}"
      cmp /out/garm.1 "/out/garm.${attempt}"
    done
  '

first_sha256=$(sha256sum "${artifact_dir}/garm.1" | awk '{print $1}')
for attempt in $(seq 2 "${build_reproducible_rebuilds}"); do
  rebuild_sha256=$(sha256sum "${artifact_dir}/garm.${attempt}" | awk '{print $1}')
  if [[ "${first_sha256}" != "${rebuild_sha256}" ]]; then
    echo "GARM rebuild ${attempt} is not reproducible: ${first_sha256} != ${rebuild_sha256}" >&2
    exit 1
  fi
done
if [[ "${first_sha256}" != "${expected_binary_sha256}" ]]; then
  echo "GARM binary digest mismatch: got ${first_sha256}, want ${expected_binary_sha256}" >&2
  exit 1
fi

# The manifest states the newest glibc the artifact may require, which is what
# lets a fleet host run it. Nothing checked it before, so a build that started
# needing a newer symbol would have been found by a host refusing to start GARM
# rather than here.
required_glibc=$(readelf --dyn-syms --wide "${artifact_dir}/garm.1" 2>/dev/null |
  grep -o 'GLIBC_[0-9]\+\.[0-9]\+' | sed 's/GLIBC_//' | sort -V | tail -1 || true)
if [[ -n "${required_glibc}" ]]; then
  newest=$(printf '%s\n%s\n' "${required_glibc}" "${build_maximum_required_glibc}" | sort -V | tail -1)
  if [[ "${newest}" != "${build_maximum_required_glibc}" ]]; then
    echo "GARM needs glibc ${required_glibc}, manifest allows at most ${build_maximum_required_glibc}" >&2
    exit 1
  fi
fi

output_path=$(realpath -m -- "${output_path}")
mkdir -p "$(dirname -- "${output_path}")"
install -m 0755 "${artifact_dir}/garm.1" "${output_path}"
printf 'version=%s\ncommit=%s\nbinary_sha256=%s\nrequired_glibc=%s\noutput=%s\n' \
  "${derivative_version}" "${upstream_commit}" "${first_sha256}" "${required_glibc:-none}" "${output_path}"
for index in "${!patch_paths[@]}"; do
  printf 'patch_%d_sha256=%s\n' "$((index + 1))" "${patch_sha256s[index]}"
done
for index in "${!overlay_paths[@]}"; do
  printf 'overlay_%d_sha256=%s\n' "$((index + 1))" "${overlay_sha256s[index]}"
done
