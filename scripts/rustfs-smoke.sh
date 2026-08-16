#!/usr/bin/env bash
set -euo pipefail

endpoint=${RUSTFS_ENDPOINT:?RUSTFS_ENDPOINT is required}
access_key_file=${RUSTFS_ACCESS_KEY_FILE:?RUSTFS_ACCESS_KEY_FILE is required}
secret_key_file=${RUSTFS_SECRET_KEY_FILE:?RUSTFS_SECRET_KEY_FILE is required}
region=${RUSTFS_REGION:-us-east-1}
ca_file=${RUSTFS_CA_FILE:-}
restart_service=${RUSTFS_RESTART_SERVICE:-}
crash_recovery=${RUSTFS_CRASH_RECOVERY:-0}

for command in curl jq sha256sum sed awk systemctl; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${endpoint} =~ ^https?://[A-Za-z0-9.:-]+$ ]] || { printf 'invalid RUSTFS_ENDPOINT\n' >&2; exit 2; }
[[ -r ${access_key_file} && -r ${secret_key_file} ]] || { printf 'credential file is not readable\n' >&2; exit 2; }
if [[ -n ${ca_file} && ! -r ${ca_file} ]]; then
  printf 'RUSTFS_CA_FILE is not readable\n' >&2
  exit 2
fi
if [[ -n ${restart_service} && ! ${restart_service} =~ ^[A-Za-z0-9_.@-]+\.service$ ]]; then
  printf 'invalid RUSTFS_RESTART_SERVICE\n' >&2
  exit 2
fi
if [[ ${crash_recovery} != 0 && ${crash_recovery} != 1 ]]; then
  printf 'RUSTFS_CRASH_RECOVERY must be 0 or 1\n' >&2
  exit 2
fi

access_key=$(tr -d '\r\n' <"${access_key_file}")
secret_key=$(tr -d '\r\n' <"${secret_key_file}")
[[ ${access_key} =~ ^[A-Za-z0-9_-]{12,128}$ ]] || { printf 'invalid access key format\n' >&2; exit 2; }
[[ ${secret_key} =~ ^[A-Za-z0-9_+/=-]{32,256}$ ]] || { printf 'invalid secret key format\n' >&2; exit 2; }

work_dir=$(mktemp -d)
chmod 0700 "${work_dir}"
curl_config=${work_dir}/curl.conf
printf 'user = "%s:%s"\n' "${access_key}" "${secret_key}" >"${curl_config}"
chmod 0600 "${curl_config}"
unset access_key secret_key

bucket="gha-smoke-$(date +%s)-${BASHPID}"
object=single.bin
multipart=multipart.bin
created_bucket=0

s3() {
  local args=(
    --config "${curl_config}"
    --silent --show-error --fail-with-body
    --connect-timeout 5 --max-time 120
    --aws-sigv4 "aws:amz:${region}:s3"
  )
  if [[ -n ${ca_file} ]]; then
    args+=(--cacert "${ca_file}")
  fi
  curl "${args[@]}" "$@"
}

delete_bucket() {
  local status=000
  s3 -X DELETE "${endpoint}/${bucket}/${object}" >/dev/null 2>&1 || true
  s3 -X DELETE "${endpoint}/${bucket}/${multipart}" >/dev/null 2>&1 || true
  for _ in {1..20}; do
    status=$(s3 -o "${work_dir}/cleanup-response" -w '%{http_code}' \
      -X DELETE "${endpoint}/${bucket}" 2>/dev/null)
    if [[ ${status} == 204 || ${status} == 404 ]]; then
      return 0
    fi
    sleep 0.25
  done
  printf 'failed to remove audit bucket %s (HTTP %s)\n' "${bucket}" "${status}" >&2
  return 1
}

cleanup() {
  local original_status=$? cleanup_failed=0
  trap - EXIT INT TERM
  set +e
  if (( created_bucket == 1 )); then
    delete_bucket || cleanup_failed=1
  fi
  rm -rf -- "${work_dir}"
  if (( original_status != 0 )); then
    exit "${original_status}"
  fi
  exit "${cleanup_failed}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

wait_ready() {
  local _
  for _ in {1..30}; do
    if s3 "${endpoint}/" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  printf 'RustFS did not become ready\n' >&2
  return 1
}

wait_ready
s3 -X PUT "${endpoint}/${bucket}" >/dev/null
created_bucket=1

head -c 1048576 /dev/urandom >"${work_dir}/single.bin"
single_digest=$(sha256sum "${work_dir}/single.bin" | awk '{print $1}')
s3 -X PUT --data-binary "@${work_dir}/single.bin" "${endpoint}/${bucket}/${object}" >/dev/null
s3 "${endpoint}/${bucket}/${object}" -o "${work_dir}/single.out"
[[ $(sha256sum "${work_dir}/single.out" | awk '{print $1}') == "${single_digest}" ]] || {
  printf 'single-object digest mismatch\n' >&2
  exit 1
}

head -c 6291456 /dev/urandom >"${work_dir}/multipart.bin"
multipart_digest=$(sha256sum "${work_dir}/multipart.bin" | awk '{print $1}')
init_xml=$(s3 -X POST "${endpoint}/${bucket}/${multipart}?uploads")
upload_id=$(sed -n 's:.*<UploadId>\([^<]*\)</UploadId>.*:\1:p' <<<"${init_xml}")
[[ -n ${upload_id} ]] || { printf 'multipart upload ID missing\n' >&2; exit 1; }
encoded_upload_id=$(printf '%s' "${upload_id}" | jq -sRr @uri)
s3 -D "${work_dir}/part.headers" -o /dev/null -X PUT \
  --data-binary "@${work_dir}/multipart.bin" \
  "${endpoint}/${bucket}/${multipart}?partNumber=1&uploadId=${encoded_upload_id}"
etag=$(awk -F': ' 'tolower($1) == "etag" {gsub("\r", "", $2); print $2}' "${work_dir}/part.headers" | tail -1)
[[ -n ${etag} ]] || { printf 'multipart ETag missing\n' >&2; exit 1; }
printf '<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>' \
  "${etag}" >"${work_dir}/complete.xml"
s3 -X POST -H 'Content-Type: application/xml' --data-binary "@${work_dir}/complete.xml" \
  "${endpoint}/${bucket}/${multipart}?uploadId=${encoded_upload_id}" >/dev/null

if [[ -n ${restart_service} ]]; then
  systemctl restart "${restart_service}"
  wait_ready
fi
if [[ ${crash_recovery} == 1 ]]; then
  [[ -n ${restart_service} ]] || { printf 'crash recovery requires RUSTFS_RESTART_SERVICE\n' >&2; exit 2; }
  systemctl kill --kill-who=main --signal=KILL "${restart_service}"
  wait_ready
  systemctl is-active --quiet "${restart_service}"
fi

s3 "${endpoint}/${bucket}/${object}" -o "${work_dir}/single.recovered"
[[ $(sha256sum "${work_dir}/single.recovered" | awk '{print $1}') == "${single_digest}" ]] || {
  printf 'single-object recovery digest mismatch\n' >&2
  exit 1
}
s3 "${endpoint}/${bucket}/${multipart}" -o "${work_dir}/multipart.out"
[[ $(sha256sum "${work_dir}/multipart.out" | awk '{print $1}') == "${multipart_digest}" ]] || {
  printf 'multipart-object digest mismatch\n' >&2
  exit 1
}

printf 'rustfs smoke: ok (CRUD, multipart, restart=%s, crash=%s)\n' \
  "$([[ -n ${restart_service} ]] && printf yes || printf no)" "${crash_recovery}"
