#!/usr/bin/env bash
set -euo pipefail

endpoint=${RUSTFS_ENDPOINT:?RUSTFS_ENDPOINT is required}
root_access_file=${RUSTFS_ACCESS_KEY_FILE:?RUSTFS_ACCESS_KEY_FILE is required}
root_secret_file=${RUSTFS_SECRET_KEY_FILE:?RUSTFS_SECRET_KEY_FILE is required}
region=${RUSTFS_REGION:-us-east-1}
ca_file=${RUSTFS_CA_FILE:-}
quota_bytes=${RUSTFS_QUOTA_BYTES:-1048576}
quota_ready_timeout_seconds=${RUSTFS_QUOTA_READY_TIMEOUT_SECONDS:-180}

for command in curl head jq; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${endpoint} =~ ^https?://[A-Za-z0-9.:-]+$ ]] || { printf 'invalid RUSTFS_ENDPOINT\n' >&2; exit 2; }
[[ -r ${root_access_file} && -r ${root_secret_file} ]] || { printf 'root credential file is not readable\n' >&2; exit 2; }
if [[ -n ${ca_file} && ! -r ${ca_file} ]]; then
  printf 'RUSTFS_CA_FILE is not readable\n' >&2
  exit 2
fi
[[ ${quota_bytes} =~ ^[0-9]+$ ]] || { printf 'RUSTFS_QUOTA_BYTES must be numeric\n' >&2; exit 2; }
(( quota_bytes >= 1048576 && quota_bytes <= 1073741824 && quota_bytes % 2 == 0 )) || {
  printf 'RUSTFS_QUOTA_BYTES must be an even value between 1 MiB and 1 GiB\n' >&2
  exit 2
}
[[ ${quota_ready_timeout_seconds} =~ ^[0-9]+$ ]] || {
  printf 'RUSTFS_QUOTA_READY_TIMEOUT_SECONDS must be numeric\n' >&2
  exit 2
}
(( quota_ready_timeout_seconds >= 60 && quota_ready_timeout_seconds <= 900 )) || {
  printf 'RUSTFS_QUOTA_READY_TIMEOUT_SECONDS must be between 60 and 900\n' >&2
  exit 2
}

root_access=$(tr -d '\r\n' <"${root_access_file}")
root_secret=$(tr -d '\r\n' <"${root_secret_file}")
[[ ${root_access} =~ ^[A-Za-z0-9_-]{12,128}$ ]] || { printf 'invalid root access key format\n' >&2; exit 2; }
[[ ${root_secret} =~ ^[A-Za-z0-9_+/=-]{32,256}$ ]] || { printf 'invalid root secret key format\n' >&2; exit 2; }

work_dir=$(mktemp -d)
chmod 0700 "${work_dir}"
root_config=${work_dir}/root.conf
printf 'user = "%s:%s"\n' "${root_access}" "${root_secret}" >"${root_config}"
chmod 0600 "${root_config}"
unset root_access root_secret

suffix="$(date +%s)-${BASHPID}"
bucket="gha-quota-${suffix}"
first_object=first.bin
second_object=second.bin
over_object=over.bin
reclaimed_object=reclaimed.bin
created_bucket=0

request_args=(
  --config "${root_config}"
  --silent --show-error
  --connect-timeout 5 --max-time 120
  --aws-sigv4 "aws:amz:${region}:s3"
)
anonymous_args=(--silent --show-error --connect-timeout 5 --max-time 30)
if [[ -n ${ca_file} ]]; then
  request_args+=(--cacert "${ca_file}")
  anonymous_args+=(--cacert "${ca_file}")
fi

root_request() { curl "${request_args[@]}" --fail-with-body "$@"; }
request_status() { curl "${request_args[@]}" -o "${work_dir}/response" -w '%{http_code}' "$@"; }

delete_bucket() {
  local status=000
  for object in "${first_object}" "${second_object}" "${over_object}" "${reclaimed_object}"; do
    root_request -X DELETE "${endpoint}/${bucket}/${object}" >/dev/null 2>&1 || true
  done
  root_request -X DELETE "${endpoint}/rustfs/admin/v3/quota/${bucket}" >/dev/null 2>&1 || true
  for _ in {1..20}; do
    status=$(request_status -X DELETE "${endpoint}/${bucket}" 2>/dev/null)
    if [[ ${status} == 204 || ${status} == 404 ]]; then
      return 0
    fi
    sleep 0.25
  done
  printf 'failed to remove quota audit bucket (HTTP %s)\n' "${status}" >&2
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

root_request -X PUT "${endpoint}/${bucket}" >/dev/null
created_bucket=1

quota_ready=0
for ((attempt = 0; attempt < quota_ready_timeout_seconds; attempt++)); do
  status=$(request_status "${endpoint}/rustfs/admin/v3/quota-stats/${bucket}")
  if [[ ${status} == 200 ]]; then
    quota_ready=1
    break
  fi
  [[ ${status} == 503 ]] || { printf 'quota readiness returned HTTP %s\n' "${status}" >&2; exit 1; }
  sleep 1
done
(( quota_ready == 1 )) || {
  printf 'quota usage did not become authoritative within %s seconds\n' "${quota_ready_timeout_seconds}" >&2
  exit 1
}

printf '{"quota":%s,"quota_type":"HARD"}' "${quota_bytes}" >"${work_dir}/quota.json"
root_request -X PUT -H 'Content-Type: application/json' --data-binary "@${work_dir}/quota.json" \
  "${endpoint}/rustfs/admin/v3/quota/${bucket}" >/dev/null
root_request "${endpoint}/rustfs/admin/v3/quota/${bucket}" -o "${work_dir}/quota-response.json"
jq -e --argjson expected "${quota_bytes}" '.quota == $expected and .quota_type == "HARD"' \
  "${work_dir}/quota-response.json" >/dev/null

half_quota=$((quota_bytes / 2))
head -c "${half_quota}" /dev/zero >"${work_dir}/half.bin"
for object in "${first_object}" "${second_object}"; do
  status=$(request_status -X PUT --data-binary "@${work_dir}/half.bin" "${endpoint}/${bucket}/${object}")
  [[ ${status} == 200 ]] || { printf 'within-quota write returned HTTP %s\n' "${status}" >&2; exit 1; }
done

head -c 1024 /dev/zero >"${work_dir}/over.bin"
status=$(request_status -X PUT --data-binary "@${work_dir}/over.bin" "${endpoint}/${bucket}/${over_object}")
[[ ${status} != 200 ]] || { printf 'over-quota write was accepted\n' >&2; exit 1; }
status=$(request_status "${endpoint}/${bucket}/${over_object}")
[[ ${status} == 404 ]] || { printf 'rejected over-quota object remained visible (HTTP %s)\n' "${status}" >&2; exit 1; }

status=$(request_status -X DELETE "${endpoint}/${bucket}/${first_object}")
[[ ${status} == 204 ]] || { printf 'quota reclaim delete returned HTTP %s\n' "${status}" >&2; exit 1; }
reclaimed=0
for _ in {1..30}; do
  status=$(request_status -X PUT --data-binary "@${work_dir}/over.bin" "${endpoint}/${bucket}/${reclaimed_object}")
  if [[ ${status} == 200 ]]; then
    reclaimed=1
    break
  fi
  sleep 1
done
(( reclaimed == 1 )) || { printf 'deleted bytes were not reclaimed within the audit window\n' >&2; exit 1; }

status=$(curl "${anonymous_args[@]}" -o "${work_dir}/anonymous-response" -w '%{http_code}' \
  "${endpoint}/rustfs/admin/v3/quota/${bucket}")
[[ ${status} == 403 ]] || { printf 'anonymous quota admin request returned HTTP %s\n' "${status}" >&2; exit 1; }

printf 'rustfs quota smoke: ok (hard limit, rejection, reclaim, anonymous deny)\n'
