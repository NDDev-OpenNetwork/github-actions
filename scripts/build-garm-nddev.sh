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
readonly derivative_version="v0.2.1-nddev.51"
readonly upstream_repository="https://github.com/cloudbase/garm"
readonly upstream_commit="154638445c3949c1958b01812f69d9a1e4d82684"
readonly build_image="docker.io/library/golang@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36"
readonly build_go_version="go1.26.6"
readonly build_cgo_enabled="1"
readonly build_target_os="linux"
readonly build_target_arch="amd64"
readonly build_network="none"
readonly build_module_mode="vendor"
readonly build_tags="osusergo,netgo,sqlite_omit_load_extension"
readonly build_reproducible_rebuilds="2"
readonly build_maximum_required_glibc="2.34"
readonly expected_binary_sha256="57b43c3ca94ad88cf64abc3dbd69494991acc2a86b098ed9c0e0d8501afaa1bb"
readonly patch_paths=(
  "third_party/garm/patches/0001-event-driven-reconciliation.patch"
  "third_party/garm/patches/0002-central-queue-admission.patch"
  "third_party/garm/patches/0003-failed-scale-set-runner-cleanup.patch"
  "third_party/garm/patches/0004-direct-jit-provider-handoff.patch"
  "third_party/garm/patches/0005-direct-jit-phase-telemetry.patch"
  "third_party/garm/patches/0006-durable-provider-create-retry.patch"
  "third_party/garm/patches/0007-fast-authoritative-job-reconciliation.patch"
  "third_party/garm/patches/0008-authoritative-scale-set-job-reconciliation.patch"
  "third_party/garm/patches/0009-precreate-provider-admission.patch"
  "third_party/garm/patches/0010-synchronize-instance-manager-state.patch"
  "third_party/garm/patches/0011-protect-incomplete-provider-lifecycle.patch"
  "third_party/garm/patches/0012-require-admitted-intent-before-scale-up.patch"
  "third_party/garm/patches/0013-wake-capacity-after-delete.patch"
  "third_party/garm/patches/0014-refresh-terminal-instance-state.patch"
  "third_party/garm/patches/0015-authoritative-terminal-delete.patch"
  "third_party/garm/patches/0016-recheck-terminal-delete-conflict.patch"
  "third_party/garm/patches/0017-authoritative-queue-intent-reconciliation.patch"
  "third_party/garm/patches/0018-oldest-first-stale-job-reconciliation.patch"
  "third_party/garm/patches/0019-authoritative-live-job-rehydration.patch"
  "third_party/garm/patches/0020-reachable-scale-set-job-reconciliation.patch"
  "third_party/garm/patches/0021-reap-idle-offline-jit-runner.patch"
  "third_party/garm/patches/0022-bound-capacity-probe-burst.patch"
  "third_party/garm/patches/0023-backoff-authoritative-access-refusal.patch"
)
readonly patch_sha256s=(
  "2f0571f141e7388d6ea0cb0341549ba5bf5dab26d0006382a71b76655e272d34"
  "7a4cd4efa8b2c8b072c5de78cd58f2b3367d91938425fdcd58402c77e8a8b1c6"
  "0cd77616af4160eae7be51f91340841b30e13d4b20ea5873e004c1b81d7879e1"
  "d23873baf6689c7e4cc366b14979ad86367ef9e321cb6d5d2745c78e64a3c172"
  "49de677a1c483e58cc009ef8f23d301ef4647ea01aec4100a3e6c49376f7889f"
  "2c5ebf1cc435345cb6cc9646cacbe27206c0c17d0b30f08b5aaeb856d731974c"
  "3c9631ee2c73eedee90fc2f26a7f87361021236b6a1bc7b7ffffa2f0d1c3c3a3"
  "b38bc35afa7a93d703e8e141495ac9e930462feb74e941ee983e98b02d1ee565"
  "6bd28c42a3da9de1b9839d500c2a68d8507bc6f638caf935ccb81fa0fbef5f29"
  "be418618739cc44d0bb2481ad3d6b33f8e7e2ef6b696f70987e8d20aa4df743f"
  "7c77950cabfd74ddd5d3d0bb65b823aa61ebc1605c3ad97e88a195a7e242f29f"
  "5bc07422308b630f0a005ce6ae7212428ea386d235e3ac75cb71046c18713f2e"
  "953e671d0d14dd64cdb9930ac5fa4ef6e62ba08438277e7bd385f138345c6e2e"
  "43d3a7cf1986c1e00250053c82a0996bbf40263ab410c679258bff72cf6d1b00"
  "50123c3502e060566b4a78f55f850ae36b37f25988d200b10c6cec414a6655ac"
  "60254f449fd0175db2636108cc65b8ea761a7b3933b1b185d6417ba48caffecb"
  "fb67643be9a2ddce1eab86182cf844bceda7f6d40b3e8386fc7c5d4fd2caa5ad"
  "7e8822d4bd13dcab7990e15df38e828211609f2c20afb4d9721f524a617b0cb2"
  "e6eefa3cc56acf049161f8f020ae796f30aacf51cb55c7b14d8db452e53a544b"
  "24661113a5fa3db00fc12f57da9764b1c5dc2c112e0a14b50eb39ffa9f6079ae"
  "cb072aeacbbfa2761dc9fb9ec0799e9e2116135bdaf70372ed388205f89868f2"
  "d4a4f73c5a45708149edc9782c2772cede3c1bc9fcb523be07d4f9c98f51fc4b"
  "45edfadc2668cc413f0409d1b3924d52127630ee3086009907fccdbe992eb7a8"
)
readonly overlay_paths=(
  "third_party/garm/overlay/workers/scaleset/queue_intent.go"
  "third_party/garm/overlay/workers/scaleset/queue_intent_test.go"
  "third_party/garm/overlay/workers/provider/nddev_create_retry.go"
  "third_party/garm/overlay/workers/provider/nddev_create_retry_test.go"
)
readonly overlay_sha256s=(
  "11fc73716cef147a889ef8220a10c831c12c017d75c1033a07aaa095e93508d1"
  "58b33d01eb978fe2913e0689bc13a9c4d7b5059fa6cd6dbd68fdc98bf1c7cabc"
  "2ac19df649b166a1ea987bb491200baae5fcc846446ff9faef402972c2b43b27"
  "73ff35df51cae78dd222cb84457930424a678815e687afd10a8061bfcf17913e"
)
readonly overlay_targets=(
  "workers/scaleset/queue_intent.go"
  "workers/scaleset/queue_intent_test.go"
  "workers/provider/nddev_create_retry.go"
  "workers/provider/nddev_create_retry_test.go"
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
