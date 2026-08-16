#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  printf 'usage: cache-network-ready INTERFACE TIMEOUT_SECONDS\n' >&2
  exit 2
fi

interface=$1
timeout_seconds=$2

[[ ${interface} =~ ^[A-Za-z0-9][A-Za-z0-9_.:-]{0,14}$ ]] || {
  printf 'cache interface must be a bounded Linux interface name\n' >&2
  exit 2
}
if [[ ! ${timeout_seconds} =~ ^[1-9][0-9]{0,2}$ ]] || ((timeout_seconds > 300)); then
  printf 'cache network timeout must be an integer from 1 through 300 seconds\n' >&2
  exit 2
fi

for ((elapsed = 0; elapsed < timeout_seconds; elapsed++)); do
  [[ -d /sys/class/net/${interface} ]] && exit 0
  sleep 1
done

# Check once more at the timeout boundary so an interface created during the
# final sleep is not rejected.
[[ -d /sys/class/net/${interface} ]] && exit 0

printf 'cache interface %s did not appear within %s seconds\n' "${interface}" "${timeout_seconds}" >&2
exit 1
