#!/usr/bin/env bash
set -euo pipefail

zot_binary=${ZOT_BINARY:?ZOT_BINARY is required}
zot_expected_sha256=${ZOT_EXPECTED_SHA256:?ZOT_EXPECTED_SHA256 is required}
audit_root=${ZOT_AUDIT_ROOT:-/var/lib/zot-storage-audit}
audit_image=${ZOT_AUDIT_IMAGE:-/var/lib/zot-storage-audit.img}
audit_image_size_mib=${ZOT_AUDIT_IMAGE_SIZE_MIB:-256}
listen_port=${ZOT_AUDIT_PORT:-15003}
audit_user=${ZOT_AUDIT_USER:-ubuntu}

for command in awk chmod chown curl df e2fsck fallocate findmnt grep head id jq mkdir mkfs.ext4 mktemp mount mountpoint rm runuser sha256sum sleep stat sync systemctl systemd-run tail timeout tr truncate umount; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${EUID} == 0 ]] || { printf 'zot storage audit requires root inside a disposable VM\n' >&2; exit 2; }
[[ -x ${zot_binary} && ${zot_binary} == /* && ${zot_binary} != / ]] || { printf 'ZOT_BINARY must be an absolute executable path\n' >&2; exit 2; }
[[ ${zot_expected_sha256} =~ ^[0-9a-f]{64}$ ]] || { printf 'invalid ZOT_EXPECTED_SHA256\n' >&2; exit 2; }
[[ $(sha256sum "${zot_binary}" | awk '{print $1}') == "${zot_expected_sha256}" ]] || {
  printf 'Zot binary digest mismatch\n' >&2
  exit 1
}
[[ ${audit_root} == /var/lib/zot-storage-audit && ${audit_image} == /var/lib/zot-storage-audit.img ]] || {
  printf 'audit storage paths must remain the exact disposable-VM paths\n' >&2
  exit 2
}
[[ ! -e ${audit_root} && ! -e ${audit_image} ]] || {
  printf 'audit storage paths already exist; refusing an ambiguous or repeated run\n' >&2
  exit 2
}
[[ ${audit_image_size_mib} =~ ^[0-9]+$ && ${audit_image_size_mib} -ge 192 && ${audit_image_size_mib} -le 512 ]] || {
  printf 'ZOT_AUDIT_IMAGE_SIZE_MIB must be between 192 and 512\n' >&2
  exit 2
}
[[ ${listen_port} =~ ^[0-9]+$ && ${listen_port} -ge 1024 && ${listen_port} -le 65535 ]] || {
  printf 'invalid ZOT_AUDIT_PORT\n' >&2
  exit 2
}
[[ ${audit_user} =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || { printf 'invalid ZOT_AUDIT_USER\n' >&2; exit 2; }
audit_uid=$(id -u "${audit_user}" 2>/dev/null) || { printf 'disposable VM audit identity is missing\n' >&2; exit 2; }
audit_group=$(id -gn "${audit_user}")
[[ ${audit_uid} =~ ^[0-9]+$ && ${audit_uid} -ge 1000 && ${audit_uid} -lt 60000 ]] || {
  printf 'ZOT_AUDIT_USER must be an unprivileged human-range identity\n' >&2
  exit 2
}

runtime_directory=$(mktemp -d /run/zot-storage-audit.XXXXXX)
chown "root:${audit_group}" "${runtime_directory}"
chmod 0750 "${runtime_directory}"
serve_config=${runtime_directory}/serve.json
gc_config=${runtime_directory}/gc.json
endpoint="http://127.0.0.1:${listen_port}"
current_unit=
unit_generation=0
mounted=false

stop_zot() {
  if [[ -n ${current_unit} ]]; then
    systemctl stop "${current_unit}.service" >/dev/null 2>&1 || true
    systemctl reset-failed "${current_unit}.service" >/dev/null 2>&1 || true
    current_unit=
  fi
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  stop_zot
  if [[ ${mounted} == true ]] && mountpoint -q "${audit_root}"; then
    umount "${audit_root}"
  fi
  find "${runtime_directory}" -xdev -depth -delete >/dev/null 2>&1
  exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

truncate -s "${audit_image_size_mib}M" "${audit_image}"
chmod 0600 "${audit_image}"
mkfs.ext4 -q -F -m 0 -L zot-audit "${audit_image}"
mkdir -m 0700 "${audit_root}"
mount -o loop,nodev,nosuid,noexec,noatime "${audit_image}" "${audit_root}"
mounted=true
[[ $(findmnt -n -o SOURCE --target "${audit_root}") == /dev/loop* ]]
for required_option in nodev noexec nosuid noatime; do
  findmnt -n -o OPTIONS --target "${audit_root}" | tr ',' '\n' | grep -Fx "${required_option}" >/dev/null
done
chown "${audit_user}:${audit_group}" "${audit_root}"
mkdir -m 0700 "${audit_root}/data"
chown "${audit_user}:${audit_group}" "${audit_root}/data"

jq -n \
  --arg root "${audit_root}/data" \
  --arg port "${listen_port}" \
  '{
    distSpecVersion: "1.1.1",
    storage: {rootDirectory: $root, gc: false},
    http: {address: "127.0.0.1", port: $port},
    log: {level: "error"}
  }' >"${serve_config}"
jq -n \
  --arg root "${audit_root}/data" \
  --arg port "${listen_port}" \
  '{
    distSpecVersion: "1.1.1",
    storage: {rootDirectory: $root, gc: true, gcDelay: "1s", gcInterval: "1s"},
    http: {address: "127.0.0.1", port: $port},
    log: {level: "error"}
  }' >"${gc_config}"
chown "root:${audit_group}" "${serve_config}" "${gc_config}"
chmod 0640 "${serve_config}" "${gc_config}"
runuser -u "${audit_user}" -- "${zot_binary}" verify "${serve_config}" >/dev/null
runuser -u "${audit_user}" -- "${zot_binary}" verify "${gc_config}" >/dev/null

start_zot() {
  unit_generation=$((unit_generation + 1))
  current_unit="zot-storage-audit-${BASHPID}-${unit_generation}"
  systemd-run --quiet --collect --unit="${current_unit}" \
    --property="User=${audit_user}" --property="Group=${audit_group}" \
    --property=NoNewPrivileges=yes --property=PrivateTmp=yes --property=PrivateDevices=yes \
    --property=ProtectSystem=strict --property=ProtectHome=yes \
    --property=ProtectKernelTunables=yes --property=ProtectKernelModules=yes \
    --property=ProtectKernelLogs=yes --property=ProtectControlGroups=yes \
    --property=RestrictSUIDSGID=yes --property=LockPersonality=yes \
    --property=MemoryDenyWriteExecute=yes --property=CapabilityBoundingSet= \
    --property=RestrictAddressFamilies=AF_INET \
    --property=IPAddressDeny=any --property=IPAddressAllow=127.0.0.0/8 \
    --property="ReadOnlyPaths=${zot_binary} ${serve_config}" \
    --property="ReadWritePaths=${audit_root}" \
    "${zot_binary}" serve "${serve_config}"
  wait_ready
}

wait_ready() {
  local _
  for _ in {1..30}; do
    if curl --silent --fail --output /dev/null --connect-timeout 2 --max-time 5 "${endpoint}/v2/" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  printf 'audit Zot did not become ready\n' >&2
  return 1
}

registry() {
  curl --silent --show-error --fail-with-body --connect-timeout 2 --max-time 60 "$@"
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
  local repository=$1 file=$2 digest=$3
  local headers=${runtime_directory}/upload.headers location upload_url separator
  registry -D "${headers}" -o /dev/null -X POST "${endpoint}/v2/${repository}/blobs/uploads/"
  location=$(awk -F': ' 'tolower($1) == "location" {gsub("\r", "", $2); print $2}' "${headers}" | tail -1)
  [[ -n ${location} ]] || { printf 'audit blob upload location missing\n' >&2; return 1; }
  upload_url=$(resolve_location "${location}")
  separator='?'
  [[ ${upload_url} == *\?* ]] && separator='&'
  registry -o /dev/null -X PUT --data-binary "@${file}" "${upload_url}${separator}digest=${digest}"
}

push_image() {
  local repository=$1 tag=$2 layer_file=$3
  local config_file=${runtime_directory}/config.json manifest_file=${runtime_directory}/manifest.json
  local config_digest layer_digest config_size layer_size
  printf '{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}' >"${config_file}"
  config_digest="sha256:$(sha256sum "${config_file}" | awk '{print $1}')"
  layer_digest="sha256:$(sha256sum "${layer_file}" | awk '{print $1}')"
  config_size=$(stat -c %s "${config_file}")
  layer_size=$(stat -c %s "${layer_file}")
  push_blob "${repository}" "${config_file}" "${config_digest}"
  push_blob "${repository}" "${layer_file}" "${layer_digest}"
  printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%s},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"%s","size":%s}]}' \
    "${config_digest}" "${config_size}" "${layer_digest}" "${layer_size}" >"${manifest_file}"
  PUSH_MANIFEST_DIGEST="sha256:$(sha256sum "${manifest_file}" | awk '{print $1}')"
  PUSH_LAYER_DIGEST=${layer_digest}
  registry -o /dev/null -X PUT -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' \
    --data-binary "@${manifest_file}" "${endpoint}/v2/${repository}/manifests/${tag}"
}

pull_and_verify() {
  local repository=$1 tag=$2 manifest_digest=$3 layer_digest=$4
  registry -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
    "${endpoint}/v2/${repository}/manifests/${tag}" -o "${runtime_directory}/manifest.out"
  [[ "sha256:$(sha256sum "${runtime_directory}/manifest.out" | awk '{print $1}')" == "${manifest_digest}" ]]
  registry "${endpoint}/v2/${repository}/blobs/${layer_digest}" -o "${runtime_directory}/layer.out"
  [[ $(sha256sum "${runtime_directory}/layer.out" | awk '{print $1}') == "${layer_digest#sha256:}" ]]
}

retained_repository=audit/retained
orphan_repository=audit/orphan
recovery_repository=audit/recovery
head -c 1048576 /dev/urandom >"${runtime_directory}/retained-layer.bin"
head -c 1048576 /dev/urandom >"${runtime_directory}/orphan-layer.bin"
head -c 2097152 /dev/urandom >"${runtime_directory}/full-layer.bin"
head -c 1048576 /dev/urandom >"${runtime_directory}/recovery-layer.bin"

start_zot
push_image "${retained_repository}" stable "${runtime_directory}/retained-layer.bin"
retained_manifest_digest=${PUSH_MANIFEST_DIGEST}
retained_layer_digest=${PUSH_LAYER_DIGEST}
push_image "${orphan_repository}" delete-me "${runtime_directory}/orphan-layer.bin"
orphan_manifest_digest=${PUSH_MANIFEST_DIGEST}
orphan_layer_digest=${PUSH_LAYER_DIGEST}
delete_status=$(curl --silent --show-error --output "${runtime_directory}/delete.out" --write-out '%{http_code}' \
  --connect-timeout 2 --max-time 30 -X DELETE "${endpoint}/v2/${orphan_repository}/manifests/${orphan_manifest_digest}")
[[ ${delete_status} == 202 ]]
stop_zot

retained_blob_path="${audit_root}/data/${retained_repository}/blobs/sha256/${retained_layer_digest#sha256:}"
orphan_blob_path="${audit_root}/data/${orphan_repository}/blobs/sha256/${orphan_layer_digest#sha256:}"
[[ -f ${retained_blob_path} && -f ${orphan_blob_path} ]]
sleep 2
runuser -u "${audit_user}" -- timeout --signal=TERM --kill-after=5s 15s \
  "${zot_binary}" verify-feature retention -i 1s -t 5s "${gc_config}" \
  >"${runtime_directory}/gc-first.log" 2>&1
[[ -f ${retained_blob_path} && ! -e ${orphan_blob_path} ]]

start_zot
pull_and_verify "${retained_repository}" stable "${retained_manifest_digest}" "${retained_layer_digest}"
orphan_status=$(curl --silent --show-error --output "${runtime_directory}/orphan.out" --write-out '%{http_code}' \
  --connect-timeout 2 --max-time 30 "${endpoint}/v2/${orphan_repository}/manifests/delete-me")
[[ ${orphan_status} == 404 ]]

available_bytes=$(df --output=avail -B1 "${audit_root}" | awk 'NR == 2 {print $1}')
[[ ${available_bytes} =~ ^[0-9]+$ && ${available_bytes} -gt 1048576 ]]
filler_size=$((available_bytes - 131072))
fallocate -l "${filler_size}" "${audit_root}/disk-pressure-filler"
remaining_bytes=$(df --output=avail -B1 "${audit_root}" | awk 'NR == 2 {print $1}')
[[ ${remaining_bytes} =~ ^[0-9]+$ && ${remaining_bytes} -le 262144 ]]

full_headers=${runtime_directory}/full-upload.headers
post_status=$(curl --silent --show-error -D "${full_headers}" -o "${runtime_directory}/full-post.out" --write-out '%{http_code}' \
  --connect-timeout 2 --max-time 30 -X POST "${endpoint}/v2/audit/full/blobs/uploads/")
full_disk_status=${post_status}
if [[ ${post_status} == 202 ]]; then
  full_location=$(awk -F': ' 'tolower($1) == "location" {gsub("\r", "", $2); print $2}' "${full_headers}" | tail -1)
  [[ -n ${full_location} ]]
  full_upload_url=$(resolve_location "${full_location}")
  full_separator='?'
  [[ ${full_upload_url} == *\?* ]] && full_separator='&'
  full_digest="sha256:$(sha256sum "${runtime_directory}/full-layer.bin" | awk '{print $1}')"
  full_disk_status=$(curl --silent --show-error -o "${runtime_directory}/full-put.out" --write-out '%{http_code}' \
    --connect-timeout 2 --max-time 30 -X PUT --data-binary "@${runtime_directory}/full-layer.bin" \
    "${full_upload_url}${full_separator}digest=${full_digest}")
fi
[[ ${full_disk_status} =~ ^[45][0-9]{2}$ ]]
[[ $(systemctl is-active "${current_unit}.service") == active ]]
pull_and_verify "${retained_repository}" stable "${retained_manifest_digest}" "${retained_layer_digest}"

rm -f -- "${audit_root}/disk-pressure-filler"
sync -f "${audit_root}"
stop_zot
start_zot
pull_and_verify "${retained_repository}" stable "${retained_manifest_digest}" "${retained_layer_digest}"
push_image "${recovery_repository}" recovered "${runtime_directory}/recovery-layer.bin"
recovery_manifest_digest=${PUSH_MANIFEST_DIGEST}
recovery_layer_digest=${PUSH_LAYER_DIGEST}
pull_and_verify "${recovery_repository}" recovered "${recovery_manifest_digest}" "${recovery_layer_digest}"
stop_zot

runuser -u "${audit_user}" -- timeout --signal=TERM --kill-after=5s 15s \
  "${zot_binary}" verify-feature retention -i 1s -t 5s "${gc_config}" \
  >"${runtime_directory}/gc-final.log" 2>&1
sync -f "${audit_root}"
umount "${audit_root}"
mounted=false
e2fsck -fn "${audit_image}" >"${runtime_directory}/e2fsck.log" 2>&1

jq -n \
  --arg zot_sha256 "${zot_expected_sha256}" \
  --arg full_disk_http_status "${full_disk_status}" \
  --argjson image_size_mib "${audit_image_size_mib}" \
  '{
    schema_version: 1,
    zot_sha256: $zot_sha256,
    storage_boundary: "dedicated-loopback-ext4",
    image_size_mib: $image_size_mib,
    gc: {
      retained_blob_preserved: true,
      orphan_blob_removed: true,
      retained_manifest_readable_after_gc: true
    },
    full_disk: {
      write_rejected: true,
      http_status: ($full_disk_http_status | tonumber),
      service_remained_active: true,
      retained_content_readable: true,
      post_reclaim_write_succeeded: true
    },
    filesystem_check_clean: true
  }'
