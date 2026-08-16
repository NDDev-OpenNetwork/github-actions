#!/usr/bin/env bash
set -Eeuo pipefail

report_error() {
  local rc="$1"
  local line="$2"
  local command="$3"
  printf >&2 'Docker seal failed at line %s: %s (exit %s)\n' "${line}" "${command}" "${rc}"
  exit "${rc}"
}

trap 'report_error "$?" "${LINENO:-0}" "$BASH_COMMAND"' ERR

if [[ "$(id -u)" != "0" ]]; then
  echo "Docker sealing must run as root" >&2
  exit 1
fi

: "${GHA_DOCKER_ACTION_BASE_REF:?}"

systemctl is-active --quiet docker.service
systemctl is-active --quiet containerd.service
test -z "$(docker ps --all --quiet)"
test -z "$(docker volume ls --quiet)"

mapfile -t image_ids < <(docker image ls --all --quiet --no-trunc | LC_ALL=C sort -u)
if [[ "${#image_ids[@]}" != "1" ]]; then
  echo "sealed integration image must contain exactly one OCI image" >&2
  exit 1
fi
[[ "$(docker image inspect --format '{{.Id}}' "${GHA_DOCKER_ACTION_BASE_REF}")" == "${image_ids[0]}" ]]

mapfile -t networks < <(docker network ls --format '{{.Name}}' | LC_ALL=C sort)
[[ "${networks[*]}" == "bridge host none" ]]

systemctl stop docker.service
systemctl stop docker.socket
systemctl stop containerd.service
systemctl is-enabled --quiet docker.service
systemctl is-enabled --quiet docker.socket
systemctl is-enabled --quiet containerd.service
if systemctl is-active --quiet docker.service || systemctl is-active --quiet docker.socket || systemctl is-active --quiet containerd.service; then
  echo "Docker services remained active before snapshot" >&2
  exit 1
fi
if [[ -e /run/docker.sock ]]; then
  if [[ ! -S /run/docker.sock ]]; then
    echo "Docker socket path is not a Unix socket" >&2
    exit 1
  fi
  if ss -H -xl | grep -Fq -- '/run/docker.sock'; then
    echo "Docker Unix listener remained active before snapshot" >&2
    exit 1
  fi
  rm -f -- /run/docker.sock
fi
test ! -e /run/docker.sock
test ! -e /var/run/docker.sock
sync
