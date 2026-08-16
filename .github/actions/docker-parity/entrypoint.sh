#!/bin/sh
set -eu

marker="${1:-}"
if [ "${marker}" != "official-docker-action-ok" ]; then
  echo "unexpected or missing Docker action marker" >&2
  exit 1
fi
if [ ! -S /var/run/docker.sock ]; then
  echo "official runner did not mount the VM-local Docker socket" >&2
  exit 1
fi
if [ -S /run/docker.sock ]; then
  echo "unexpected second Docker socket path inside the action container" >&2
  exit 1
fi
if [ -z "${GITHUB_OUTPUT:-}" ] || [ -z "${GITHUB_STEP_SUMMARY:-}" ]; then
  echo "official runner command files are unavailable" >&2
  exit 1
fi

printf 'value=docker-%s\n' "${marker}" >>"${GITHUB_OUTPUT}"
printf '### Local Docker action parity\n\n- socket: official VM-local mount present\n' >>"${GITHUB_STEP_SUMMARY}"
