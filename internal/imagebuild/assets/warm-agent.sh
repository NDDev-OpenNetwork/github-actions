#!/usr/bin/env bash
set -Eeuo pipefail

assignment_root=/run/gha-warm/assignments
claim_root=/var/lib/gha-warm/claims
lock_file=/run/lock/gha-warm-agent.lock

install -d -o root -g root -m 0700 "${assignment_root}" "${claim_root}"
exec 9>"${lock_file}"
flock --exclusive 9

shopt -s nullglob
for assignment in "${assignment_root}"/*.sh; do
  name="${assignment##*/}"
  claim_id="${name%.sh}"
  if [[ ! "${claim_id}" =~ ^[0-9a-f]{64}$ ]] || [[ -L "${assignment}" ]] || [[ ! -f "${assignment}" ]]; then
    printf 'reject malformed warm assignment path %q\n' "${assignment}" >&2
    rm -f -- "${assignment}"
    continue
  fi
  metadata="$(stat --format='%u:%g:%a:%F' -- "${assignment}")"
  if [[ "${metadata}" != '0:0:700:regular file' ]]; then
    printf 'reject warm assignment %s with metadata %q\n' "${claim_id}" "${metadata}" >&2
    rm -f -- "${assignment}"
    continue
  fi

  marker="${claim_root}/${claim_id}.started"
  ca_file="${assignment_root}/${claim_id}.ca.pem"
  if [[ -e "${marker}" ]]; then
    rm -f -- "${assignment}" "${ca_file}"
    continue
  fi

  if [[ -e "${ca_file}" ]]; then
    ca_metadata="$(stat --format='%u:%g:%a:%F' -- "${ca_file}")"
    if [[ -L "${ca_file}" ]] || [[ ! -f "${ca_file}" ]] || [[ "${ca_metadata}" != '0:0:600:regular file' ]]; then
      printf 'reject warm CA %s with metadata %q\n' "${claim_id}" "${ca_metadata}" >&2
      rm -f -- "${assignment}" "${ca_file}"
      continue
    fi
    openssl x509 -in "${ca_file}" -noout >/dev/null
    install -o root -g root -m 0644 "${ca_file}" /usr/local/share/ca-certificates/nddev-worker-gateway.crt
    update-ca-certificates >/dev/null
  fi
  install -o root -g root -m 0600 /dev/null "${marker}"
  printf 'started\n' >"${marker}"

  # Root opens the private assignment before dropping privileges. The script
  # and the official GARM install script it downloads execute as runner.
  if runuser --user runner -- /bin/bash -s <"${assignment}"; then
    printf 'completed\n' >"${marker}"
  else
    status=$?
    printf 'failed:%d\n' "${status}" >"${marker}"
    rm -f -- "${assignment}" "${ca_file}"
    exit "${status}"
  fi
  rm -f -- "${assignment}" "${ca_file}"
done
