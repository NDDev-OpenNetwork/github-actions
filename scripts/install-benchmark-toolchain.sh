#!/usr/bin/env bash
set -euo pipefail

umask 077

usage() {
  printf 'usage: %s <rust|uv|bun>\n' "${0##*/}" >&2
  exit 64
}

download_verified() {
  local url="$1"
  local expected_sha256="$2"
  local output="$3"
  local actual_sha256

  [[ "${url}" == https://* ]] || {
    printf 'toolchain URL must use HTTPS\n' >&2
    exit 65
  }
  [[ "${expected_sha256}" =~ ^[0-9a-f]{64}$ ]] || {
    printf 'toolchain SHA-256 is invalid\n' >&2
    exit 65
  }
  curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
    --retry 2 --retry-all-errors --connect-timeout 15 --max-time 300 \
    --output "${output}" "${url}"
  chmod 0600 "${output}"
  actual_sha256="$(sha256sum "${output}" | awk '{print $1}')"
  if [[ "${actual_sha256}" != "${expected_sha256}" ]]; then
    printf 'toolchain SHA-256 mismatch\n' >&2
    exit 65
  fi
}

[[ $# -eq 1 ]] || usage
toolchain="$1"
[[ "${toolchain}" =~ ^(rust|uv|bun)$ ]] || usage
: "${RUNNER_TEMP:?RUNNER_TEMP is required}"
: "${GITHUB_PATH:?GITHUB_PATH is required}"
: "${GITHUB_ENV:?GITHUB_ENV is required}"
[[ "${RUNNER_TEMP}" == /* && -d "${RUNNER_TEMP}" ]] || {
  printf 'RUNNER_TEMP must be an existing absolute directory\n' >&2
  exit 64
}
[[ "${GITHUB_PATH}" == /* ]] || {
  printf 'GITHUB_PATH must be absolute\n' >&2
  exit 64
}
[[ "${GITHUB_ENV}" == /* ]] || {
  printf 'GITHUB_ENV must be absolute\n' >&2
  exit 64
}
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] || {
  printf 'benchmark toolchains support only Linux x86_64\n' >&2
  exit 65
}

install_root="${RUNNER_TEMP}/nddev-benchmark-toolchains"
binary_root="${install_root}/bin"
scratch="$(mktemp -d "${RUNNER_TEMP}/nddev-benchmark-installer.XXXXXX")"
cleanup() {
  find "${scratch}" -mindepth 1 -delete
  rmdir -- "${scratch}"
}
trap cleanup EXIT
install -d -m 0700 "${install_root}" "${binary_root}"

case "${toolchain}" in
  rust)
    if [[ "$(rustc --version 2>/dev/null || true)" == "rustc 1.98.0 "* &&
      "$(cargo --version 2>/dev/null || true)" == "cargo 1.98.0 "* ]]; then
      exit 0
    fi
    : "${CARGO_HOME:?CARGO_HOME is required for Rust installation}"
    [[ "${CARGO_HOME}" == "${RUNNER_TEMP}"/nddev-benchmark-cache/* ]] || {
      printf 'CARGO_HOME is outside the benchmark cache root\n' >&2
      exit 65
    }
    rustup_home="${install_root}/rustup"
    rustup_init="${scratch}/rustup-init"
    download_verified \
      "https://static.rust-lang.org/rustup/archive/1.28.2/x86_64-unknown-linux-gnu/rustup-init" \
      "20a06e644b0d9bd2fbdbfd52d42540bdde820ea7df86e92e533c073da0cdd43c" \
      "${rustup_init}"
    chmod 0700 "${rustup_init}"
    export RUSTUP_HOME="${rustup_home}"
    "${rustup_init}" --default-toolchain 1.98.0 --profile minimal --no-modify-path -y
    printf 'RUSTUP_HOME=%s\n' "${rustup_home}" >>"${GITHUB_ENV}"
    printf '%s/bin\n' "${CARGO_HOME}" >>"${GITHUB_PATH}"
    export PATH="${CARGO_HOME}/bin:${PATH}"
    [[ "$(rustc --version)" == "rustc 1.98.0 "* ]]
    [[ "$(cargo --version)" == "cargo 1.98.0 "* ]]
    ;;
  uv)
    if [[ "$(uv --version 2>/dev/null || true)" == "uv 0.11.30"* ]]; then
      exit 0
    fi
    archive="${scratch}/uv.tar.gz"
    download_verified \
      "https://github.com/astral-sh/uv/releases/download/0.11.30/uv-x86_64-unknown-linux-gnu.tar.gz" \
      "04bc7d180d6138bf6dc08387acf507a823f397a98fea55da36b0ccc7fbce3b68" \
      "${archive}"
    tar --extract --gzip --file "${archive}" --directory "${scratch}"
    install -m 0755 "${scratch}/uv-x86_64-unknown-linux-gnu/uv" "${binary_root}/uv"
    install -m 0755 "${scratch}/uv-x86_64-unknown-linux-gnu/uvx" "${binary_root}/uvx"
    printf '%s\n' "${binary_root}" >>"${GITHUB_PATH}"
    export PATH="${binary_root}:${PATH}"
    [[ "$(uv --version)" == "uv 0.11.30"* ]]
    ;;
  bun)
    if [[ "$(bun --version 2>/dev/null || true)" == "1.4.0" ]]; then
      exit 0
    fi
    archive="${scratch}/bun.zip"
    download_verified \
      "https://github.com/oven-sh/bun/releases/download/bun-v1.4.0/bun-linux-x64.zip" \
      "2d03fb5fb83ac8b567aca0a281b2ce1a1a19d488f56c2968d88c3f25e92fe452" \
      "${archive}"
    unzip -q "${archive}" -d "${scratch}"
    install -m 0755 "${scratch}/bun-linux-x64/bun" "${binary_root}/bun"
    printf '%s\n' "${binary_root}" >>"${GITHUB_PATH}"
    export PATH="${binary_root}:${PATH}"
    [[ "$(bun --version)" == "1.4.0" ]]
    ;;
esac
