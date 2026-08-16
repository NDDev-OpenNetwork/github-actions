#!/usr/bin/env bash
set -euo pipefail

endpoint=${ZOT_ENDPOINT:?ZOT_ENDPOINT is required}
username_file=${ZOT_USERNAME_FILE:-}
password_file=${ZOT_PASSWORD_FILE:-}
readonly_username_file=${ZOT_READONLY_USERNAME_FILE:-}
readonly_password_file=${ZOT_READONLY_PASSWORD_FILE:-}
ca_file=${ZOT_CA_FILE:-}
restart_service=${ZOT_RESTART_SERVICE:-}
crash_recovery=${ZOT_CRASH_RECOVERY:-0}
repository_prefix=${ZOT_REPOSITORY_PREFIX:-cache}
denied_repository=${ZOT_DENIED_REPOSITORY:-outside-cache/smoke}

for command in curl sha256sum awk stat systemctl; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${endpoint} =~ ^https?://[A-Za-z0-9.:-]+$ ]] || { printf 'invalid ZOT_ENDPOINT\n' >&2; exit 2; }
if [[ -n ${ca_file} && ! -r ${ca_file} ]]; then
  printf 'ZOT_CA_FILE is not readable\n' >&2
  exit 2
fi
if [[ -n ${restart_service} && ! ${restart_service} =~ ^[A-Za-z0-9_.@-]+\.service$ ]]; then
  printf 'invalid ZOT_RESTART_SERVICE\n' >&2
  exit 2
fi
if [[ ${crash_recovery} != 0 && ${crash_recovery} != 1 ]]; then
  printf 'ZOT_CRASH_RECOVERY must be 0 or 1\n' >&2
  exit 2
fi
valid_repository_path() {
  local value=$1
  [[ ${#value} -le 200 && ${value} =~ ^[a-z0-9]+([._-][a-z0-9]+|/[a-z0-9]+([._-][a-z0-9]+)*)*$ ]]
}
valid_repository_path "${repository_prefix}" || { printf 'invalid ZOT_REPOSITORY_PREFIX\n' >&2; exit 2; }
valid_repository_path "${denied_repository}" || { printf 'invalid ZOT_DENIED_REPOSITORY\n' >&2; exit 2; }
[[ ${denied_repository} != "${repository_prefix}" && ${denied_repository} != "${repository_prefix}/"* ]] || {
  printf 'denied repository must be outside the allowed prefix\n' >&2
  exit 2
}

work_dir=$(mktemp -d)
chmod 0700 "${work_dir}"
curl_config=${work_dir}/curl.conf
readonly_curl_config=${work_dir}/readonly-curl.conf
: >"${curl_config}"
: >"${readonly_curl_config}"
chmod 0600 "${curl_config}"
chmod 0600 "${readonly_curl_config}"
if [[ -n ${username_file} || -n ${password_file} ]]; then
  [[ -r ${username_file} && -r ${password_file} ]] || { printf 'Zot credential file is not readable\n' >&2; exit 2; }
  username=$(tr -d '\r\n' <"${username_file}")
  password=$(tr -d '\r\n' <"${password_file}")
  [[ ${username} =~ ^[A-Za-z0-9_-]{3,64}$ && ${password} =~ ^[A-Za-z0-9_+/=-]{24,256}$ ]] || {
    printf 'invalid Zot credential format\n' >&2
    exit 2
  }
  printf 'user = "%s:%s"\n' "${username}" "${password}" >"${curl_config}"
  unset username password
fi
if [[ -n ${readonly_username_file} || -n ${readonly_password_file} ]]; then
  [[ -r ${readonly_username_file} && -r ${readonly_password_file} ]] || { printf 'Zot read-only credential file is not readable\n' >&2; exit 2; }
  readonly_username=$(tr -d '\r\n' <"${readonly_username_file}")
  readonly_password=$(tr -d '\r\n' <"${readonly_password_file}")
  [[ ${readonly_username} =~ ^[A-Za-z0-9_-]{3,64}$ && ${readonly_password} =~ ^[A-Za-z0-9_+/=-]{24,256}$ ]] || {
    printf 'invalid Zot read-only credential format\n' >&2
    exit 2
  }
  printf 'user = "%s:%s"\n' "${readonly_username}" "${readonly_password}" >"${readonly_curl_config}"
  unset readonly_username readonly_password
fi

repo="${repository_prefix}/smoke-$(date +%s)-${BASHPID}"
tag=integrity
manifest_digest=

registry() {
  local args=(--config "${curl_config}" --silent --show-error --fail-with-body --connect-timeout 5 --max-time 120)
  if [[ -n ${ca_file} ]]; then
    args+=(--cacert "${ca_file}")
  fi
  curl "${args[@]}" "$@"
}

readonly_registry() {
  local args=(--config "${readonly_curl_config}" --silent --show-error --fail-with-body --connect-timeout 5 --max-time 120)
  if [[ -n ${ca_file} ]]; then
    args+=(--cacert "${ca_file}")
  fi
  curl "${args[@]}" "$@"
}

anonymous_status() {
  local args=(--silent --show-error -o "${work_dir}/anonymous-response" -w '%{http_code}' --connect-timeout 5 --max-time 30)
  if [[ -n ${ca_file} ]]; then
    args+=(--cacert "${ca_file}")
  fi
  curl "${args[@]}" "$@"
}

readonly_status() {
  local args=(--config "${readonly_curl_config}" --silent --show-error -o "${work_dir}/readonly-response" -w '%{http_code}' --connect-timeout 5 --max-time 30)
  if [[ -n ${ca_file} ]]; then
    args+=(--cacert "${ca_file}")
  fi
  curl "${args[@]}" "$@"
}

registry_status() {
  local args=(--config "${curl_config}" --silent --show-error -o "${work_dir}/writer-response" -w '%{http_code}' --connect-timeout 5 --max-time 30)
  if [[ -n ${ca_file} ]]; then
    args+=(--cacert "${ca_file}")
  fi
  curl "${args[@]}" "$@"
}

cleanup() {
  local original_status=$?
  trap - EXIT INT TERM
  set +e
  if [[ -n ${manifest_digest} ]]; then
    registry -X DELETE "${endpoint}/v2/${repo}/manifests/${manifest_digest}" >/dev/null 2>&1
  fi
  rm -rf -- "${work_dir}"
  exit "${original_status}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

wait_ready() {
  local _
  for _ in {1..30}; do
    if registry "${endpoint}/v2/" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  printf 'Zot did not become ready\n' >&2
  return 1
}

resolve_location() {
  local location=$1
  if [[ ${location} == http://* || ${location} == https://* ]]; then
    printf '%s' "${location}"
  else
    printf '%s%s' "${endpoint}" "${location}"
  fi
}

push_blob() {
  local file=$1 digest=$2 headers=${work_dir}/upload.headers location upload_url separator
  registry -D "${headers}" -o /dev/null -X POST "${endpoint}/v2/${repo}/blobs/uploads/"
  location=$(awk -F': ' 'tolower($1) == "location" {gsub("\r", "", $2); print $2}' "${headers}" | tail -1)
  [[ -n ${location} ]] || { printf 'blob upload location missing\n' >&2; return 1; }
  upload_url=$(resolve_location "${location}")
  separator='?'
  [[ ${upload_url} == *\?* ]] && separator='&'
  registry -o /dev/null -X PUT --data-binary "@${file}" "${upload_url}${separator}digest=${digest}"
}

wait_ready
if [[ -n ${username_file} ]]; then
  status=$(anonymous_status "${endpoint}/v2/")
  [[ ${status} == 401 ]] || { printf 'anonymous registry request unexpectedly received HTTP %s\n' "${status}" >&2; exit 1; }
  status=$(registry_status -X POST "${endpoint}/v2/${denied_repository}/blobs/uploads/")
  [[ ${status} == 403 ]] || { printf 'cache writer unexpectedly received HTTP %s in denied repository\n' "${status}" >&2; exit 1; }
fi
printf '{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}' >"${work_dir}/config.json"
head -c 1048576 /dev/urandom >"${work_dir}/layer.bin"
config_digest="sha256:$(sha256sum "${work_dir}/config.json" | awk '{print $1}')"
layer_digest="sha256:$(sha256sum "${work_dir}/layer.bin" | awk '{print $1}')"
config_size=$(stat -c %s "${work_dir}/config.json")
layer_size=$(stat -c %s "${work_dir}/layer.bin")
push_blob "${work_dir}/config.json" "${config_digest}"
push_blob "${work_dir}/layer.bin" "${layer_digest}"
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%s},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"%s","size":%s}]}' \
  "${config_digest}" "${config_size}" "${layer_digest}" "${layer_size}" >"${work_dir}/manifest.json"
manifest_digest="sha256:$(sha256sum "${work_dir}/manifest.json" | awk '{print $1}')"
registry -o /dev/null -X PUT -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' \
  --data-binary "@${work_dir}/manifest.json" "${endpoint}/v2/${repo}/manifests/${tag}"

if [[ -n ${restart_service} ]]; then
  systemctl restart "${restart_service}"
  wait_ready
fi
if [[ ${crash_recovery} == 1 ]]; then
  [[ -n ${restart_service} ]] || { printf 'crash recovery requires ZOT_RESTART_SERVICE\n' >&2; exit 2; }
  systemctl kill --kill-who=main --signal=KILL "${restart_service}"
  wait_ready
  systemctl is-active --quiet "${restart_service}"
fi

registry -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
  "${endpoint}/v2/${repo}/manifests/${tag}" -o "${work_dir}/manifest.out"
[[ "sha256:$(sha256sum "${work_dir}/manifest.out" | awk '{print $1}')" == "${manifest_digest}" ]] || {
  printf 'manifest digest mismatch\n' >&2
  exit 1
}
registry "${endpoint}/v2/${repo}/blobs/${layer_digest}" -o "${work_dir}/layer.out"
[[ $(sha256sum "${work_dir}/layer.out" | awk '{print $1}') == "${layer_digest#sha256:}" ]] || {
  printf 'blob digest mismatch\n' >&2
  exit 1
}

authz_tested=no
if [[ -n ${readonly_username_file} ]]; then
  readonly_registry -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
    "${endpoint}/v2/${repo}/manifests/${tag}" -o "${work_dir}/readonly-manifest.out"
  [[ "sha256:$(sha256sum "${work_dir}/readonly-manifest.out" | awk '{print $1}')" == "${manifest_digest}" ]] || {
    printf 'read-only manifest digest mismatch\n' >&2
    exit 1
  }
  status=$(readonly_status -X POST "${endpoint}/v2/${repo}/blobs/uploads/")
  [[ ${status} == 403 ]] || { printf 'read-only registry user unexpectedly received HTTP %s for write\n' "${status}" >&2; exit 1; }
  authz_tested=yes
fi

status=$(registry_status -X DELETE "${endpoint}/v2/${repo}/manifests/${manifest_digest}")
[[ ${status} == 202 ]] || { printf 'manifest delete returned HTTP %s\n' "${status}" >&2; exit 1; }
for _ in {1..20}; do
  status=$(registry_status -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
    "${endpoint}/v2/${repo}/manifests/${tag}")
  [[ ${status} == 404 ]] && break
  sleep 0.25
done
[[ ${status} == 404 ]] || { printf 'deleted manifest remained readable (HTTP %s)\n' "${status}" >&2; exit 1; }
manifest_digest=

printf 'zot smoke: ok (OCI push/pull, verified manifest delete, authz=%s, restart=%s, crash=%s)\n' \
  "${authz_tested}" "$([[ -n ${restart_service} ]] && printf yes || printf no)" "${crash_recovery}"
