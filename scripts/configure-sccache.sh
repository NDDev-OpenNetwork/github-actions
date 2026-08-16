#!/usr/bin/env bash
set -Eeuo pipefail
set +x
umask 077

usage() {
  printf 'usage: %s <lock-file> <toolchain> <ref-class>\n' "${0##*/}" >&2
  exit 2
}

[[ "$#" == 3 ]] || usage
lock_file="$1"
toolchain="$2"
ref_class="$3"

: "${GITHUB_ENV:?}"
: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_WORKSPACE:?}"
: "${NDDEV_CACHE_ROLE:?}"
: "${NDDEV_CACHE_MODE:?}"
: "${NDDEV_CACHE_PREFIX_ROOT:?}"
: "${AWS_ACCESS_KEY_ID:?}"
: "${AWS_SECRET_ACCESS_KEY:?}"
: "${SCCACHE_BUCKET:?}"
: "${SCCACHE_ENDPOINT:?}"
: "${SCCACHE_REGION:?}"
: "${SCCACHE_S3_USE_SSL:?}"

[[ "${GITHUB_REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]
[[ "${toolchain}" =~ ^[a-z0-9][a-z0-9._-]{1,63}$ ]]
[[ "${ref_class}" =~ ^(branch|merge-queue|nightly|release|benchmark)$ ]]
[[ "${AWS_ACCESS_KEY_ID}" =~ ^AKIA[0-9A-F]{16}$ ]]
[[ "${AWS_SECRET_ACCESS_KEY}" =~ ^[A-Za-z0-9_-]{64}$ ]]
[[ "${SCCACHE_BUCKET}" == github-actions-cache ]]
[[ "${SCCACHE_ENDPOINT}" == https://192.0.2.1:9002 ]]
[[ "${SCCACHE_REGION}" == us-east-1 ]]
[[ "${SCCACHE_S3_USE_SSL}" == true ]]
[[ "$(command -v sccache)" == /usr/local/bin/sccache ]]
[[ "$(sccache --version)" == 'sccache 0.17.0' ]]

case "${NDDEV_CACHE_ROLE}:${NDDEV_CACHE_MODE}:${ref_class}" in
  trusted-writer:read-write:branch|trusted-writer:read-write:merge-queue|trusted-writer:read-write:nightly|trusted-writer:read-write:benchmark)
    s3_mode=READ_WRITE
    ;;
  release-reader:read-only:release)
    s3_mode=READ_ONLY
    ;;
  *)
    printf 'cache role/mode/ref-class combination is not authorized\n' >&2
    exit 1
    ;;
esac

expected_prefix="${GITHUB_REPOSITORY}/trust/"
case "${NDDEV_CACHE_ROLE}" in
  trusted-writer) expected_prefix+=trusted ;;
  release-reader) expected_prefix+=promoted ;;
  *) printf 'cache role is not supported by the sccache adapter\n' >&2; exit 1 ;;
esac
[[ "${NDDEV_CACHE_PREFIX_ROOT}" == "${expected_prefix}" ]]

workspace="$(realpath --canonicalize-existing -- "${GITHUB_WORKSPACE}")"
lock_path="$(realpath --canonicalize-existing -- "${lock_file}")"
case "${lock_path}" in
  "${workspace}"/*) ;;
  *) printf 'lock file is outside GITHUB_WORKSPACE\n' >&2; exit 1 ;;
esac
test ! -L "${lock_file}"
test -f "${lock_path}"
lock_bytes="$(stat --format='%s' -- "${lock_path}")"
[[ "${lock_bytes}" =~ ^[0-9]+$ ]]
(( lock_bytes > 0 && lock_bytes <= 16 * 1024 * 1024 ))
lock_digest="$(sha256sum -- "${lock_path}" | cut -d' ' -f1)"
[[ "${lock_digest}" =~ ^[0-9a-f]{64}$ ]]

namespace="${NDDEV_CACHE_PREFIX_ROOT}/linux/amd64/${toolchain}/${lock_digest}/${ref_class}"
namespace_digest="$(printf '%s' "${namespace}" | sha256sum | cut -d' ' -f1)"
[[ "${namespace_digest}" =~ ^[0-9a-f]{64}$ ]]

test ! -L "${GITHUB_ENV}"
test -f "${GITHUB_ENV}"
{
  printf 'RUSTC_WRAPPER=/usr/local/bin/sccache\n'
  printf 'SCCACHE_CLIENT_SIDE=1\n'
  printf 'SCCACHE_S3_KEY_PREFIX=%s\n' "${namespace}"
  printf 'SCCACHE_S3_RW_MODE=%s\n' "${s3_mode}"
  printf 'NDDEV_SCCACHE_NAMESPACE_SHA256=%s\n' "${namespace_digest}"
} >>"${GITHUB_ENV}"

printf 'sccache namespace configured: role=%s mode=%s namespace_sha256=%s\n' \
  "${NDDEV_CACHE_ROLE}" "${s3_mode}" "${namespace_digest}"
