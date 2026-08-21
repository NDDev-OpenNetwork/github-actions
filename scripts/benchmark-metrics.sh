#!/usr/bin/env bash
set -euo pipefail

umask 077

usage() {
  printf 'usage: %s <start|finish>\n' "${0##*/}" >&2
  exit 64
}

require_match() {
  local name="$1"
  local value="$2"
  local pattern="$3"
  if [[ ! "${value}" =~ ${pattern} ]]; then
    printf '%s has an invalid value\n' "${name}" >&2
    exit 64
  fi
}

network_rx_bytes() {
  local counter
  local total=0
  for counter in /sys/class/net/*/statistics/rx_bytes; do
    [[ -r "${counter}" ]] || continue
    local value
    value="$(<"${counter}")"
    [[ "${value}" =~ ^[0-9]+$ ]] || continue
    total=$((total + value))
  done
  printf '%s\n' "${total}"
}

[[ $# -eq 1 ]] || usage
mode="$1"
[[ "${mode}" == start || "${mode}" == finish ]] || usage

: "${NDDEV_BENCHMARK_WORKLOAD:?NDDEV_BENCHMARK_WORKLOAD is required}"
: "${NDDEV_BENCHMARK_ENVIRONMENT:?NDDEV_BENCHMARK_ENVIRONMENT is required}"
: "${NDDEV_BENCHMARK_CACHE_MODE:?NDDEV_BENCHMARK_CACHE_MODE is required}"
: "${NDDEV_BENCHMARK_ITERATION:?NDDEV_BENCHMARK_ITERATION is required}"
: "${RUNNER_TEMP:?RUNNER_TEMP is required}"

require_match NDDEV_BENCHMARK_WORKLOAD "${NDDEV_BENCHMARK_WORKLOAD}" '^(go|rust|python-uv|bun-next|docker)$'
require_match NDDEV_BENCHMARK_ENVIRONMENT "${NDDEV_BENCHMARK_ENVIRONMENT}" '^(github-hosted|nddev)$'
require_match NDDEV_BENCHMARK_CACHE_MODE "${NDDEV_BENCHMARK_CACHE_MODE}" '^(cold|warm)$'
require_match NDDEV_BENCHMARK_ITERATION "${NDDEV_BENCHMARK_ITERATION}" '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$'
[[ "${RUNNER_TEMP}" == /* && -d "${RUNNER_TEMP}" ]] || {
  printf 'RUNNER_TEMP must be an existing absolute directory\n' >&2
  exit 64
}

state_directory="${RUNNER_TEMP}/nddev-benchmark-${NDDEV_BENCHMARK_WORKLOAD}"
start_time_file="${state_directory}/start-time-ns"
start_rx_file="${state_directory}/start-rx-bytes"
result_file="${state_directory}/result.json"

if [[ "${mode}" == start ]]; then
  install -d -m 0700 "${state_directory}"
  printf '%s\n' "$(date -u +%s%N)" >"${start_time_file}"
  network_rx_bytes >"${start_rx_file}"
  printf '%s\n' "${state_directory}"
  exit 0
fi

: "${NDDEV_BENCHMARK_CACHE_HIT:?NDDEV_BENCHMARK_CACHE_HIT is required for finish}"
: "${NDDEV_BENCHMARK_TOOLCHAIN:?NDDEV_BENCHMARK_TOOLCHAIN is required for finish}"
: "${GITHUB_SHA:?GITHUB_SHA is required for finish}"
: "${GITHUB_RUN_ID:?GITHUB_RUN_ID is required for finish}"
: "${GITHUB_RUN_ATTEMPT:?GITHUB_RUN_ATTEMPT is required for finish}"

require_match NDDEV_BENCHMARK_CACHE_HIT "${NDDEV_BENCHMARK_CACHE_HIT}" '^(true|false|disabled)$'
require_match GITHUB_SHA "${GITHUB_SHA}" '^[0-9a-f]{40,64}$'
require_match GITHUB_RUN_ID "${GITHUB_RUN_ID}" '^[1-9][0-9]*$'
require_match GITHUB_RUN_ATTEMPT "${GITHUB_RUN_ATTEMPT}" '^[1-9][0-9]*$'
if [[ -z "${NDDEV_BENCHMARK_TOOLCHAIN}" || ${#NDDEV_BENCHMARK_TOOLCHAIN} -gt 256 || "${NDDEV_BENCHMARK_TOOLCHAIN}" == *$'\n'* ]]; then
  printf 'NDDEV_BENCHMARK_TOOLCHAIN must be a bounded single-line value\n' >&2
  exit 64
fi
[[ -r "${start_time_file}" && -r "${start_rx_file}" ]] || {
  printf 'benchmark start state is missing\n' >&2
  exit 65
}

start_time_ns="$(<"${start_time_file}")"
start_rx_bytes="$(<"${start_rx_file}")"
finish_time_ns="$(date -u +%s%N)"
finish_rx_bytes="$(network_rx_bytes)"
for value in "${start_time_ns}" "${start_rx_bytes}" "${finish_time_ns}" "${finish_rx_bytes}"; do
  [[ "${value}" =~ ^[0-9]+$ ]] || {
    printf 'benchmark state contains a non-numeric counter\n' >&2
    exit 65
  }
done
((finish_time_ns >= start_time_ns)) || {
  printf 'wall clock moved backwards during benchmark\n' >&2
  exit 65
}
((finish_rx_bytes >= start_rx_bytes)) || {
  printf 'network counter moved backwards during benchmark\n' >&2
  exit 65
}

machine_id_sha256="unavailable"
if [[ -r /etc/machine-id ]]; then
  machine_id_sha256="$(sha256sum /etc/machine-id | awk '{print $1}')"
fi

jq -n \
  --arg workload "${NDDEV_BENCHMARK_WORKLOAD}" \
  --arg environment "${NDDEV_BENCHMARK_ENVIRONMENT}" \
  --arg cache_mode "${NDDEV_BENCHMARK_CACHE_MODE}" \
  --arg iteration "${NDDEV_BENCHMARK_ITERATION}" \
  --arg cache_hit "${NDDEV_BENCHMARK_CACHE_HIT}" \
  --arg toolchain "${NDDEV_BENCHMARK_TOOLCHAIN}" \
  --arg commit "${GITHUB_SHA}" \
  --arg machine_id_sha256 "${machine_id_sha256}" \
  --argjson run_id "${GITHUB_RUN_ID}" \
  --argjson run_attempt "${GITHUB_RUN_ATTEMPT}" \
  --argjson start_time_ns "${start_time_ns}" \
  --argjson finish_time_ns "${finish_time_ns}" \
  --argjson elapsed_ns "$((finish_time_ns - start_time_ns))" \
  --argjson network_rx_bytes "$((finish_rx_bytes - start_rx_bytes))" \
  '{
    schema_version: 1,
    workload: $workload,
    environment: $environment,
    cache_mode: $cache_mode,
    iteration: $iteration,
    cache_hit: $cache_hit,
    toolchain: $toolchain,
    commit: $commit,
    run_id: $run_id,
    run_attempt: $run_attempt,
    machine_id_sha256: $machine_id_sha256,
    start_time_ns: $start_time_ns,
    finish_time_ns: $finish_time_ns,
    elapsed_ns: $elapsed_ns,
    network_rx_bytes: $network_rx_bytes
  }' >"${result_file}"
chmod 0600 "${result_file}"
printf '%s\n' "${result_file}"
