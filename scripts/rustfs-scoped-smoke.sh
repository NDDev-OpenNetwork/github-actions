#!/usr/bin/env bash
set -euo pipefail

endpoint=${RUSTFS_ENDPOINT:?RUSTFS_ENDPOINT is required}
access_key_file=${RUSTFS_ACCESS_KEY_FILE:?RUSTFS_ACCESS_KEY_FILE is required}
secret_key_file=${RUSTFS_SECRET_KEY_FILE:?RUSTFS_SECRET_KEY_FILE is required}
allowed_bucket=${RUSTFS_ALLOWED_BUCKET:?RUSTFS_ALLOWED_BUCKET is required}
denied_bucket=${RUSTFS_DENIED_BUCKET:?RUSTFS_DENIED_BUCKET is required}
region=${RUSTFS_REGION:-us-east-1}
ca_file=${RUSTFS_CA_FILE:-}

for command in curl head sha256sum; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${endpoint} =~ ^https?://[A-Za-z0-9.:-]+$ ]] || { printf 'invalid RUSTFS_ENDPOINT\n' >&2; exit 2; }
[[ -r ${access_key_file} && -r ${secret_key_file} ]] || { printf 'credential file is not readable\n' >&2; exit 2; }
if [[ -n ${ca_file} && ! -r ${ca_file} ]]; then
  printf 'RUSTFS_CA_FILE is not readable\n' >&2
  exit 2
fi
for bucket in "${allowed_bucket}" "${denied_bucket}"; do
  [[ ${bucket} =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || { printf 'invalid bucket name\n' >&2; exit 2; }
done
[[ ${allowed_bucket} != "${denied_bucket}" ]] || { printf 'allowed and denied buckets must differ\n' >&2; exit 2; }

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

object="worker-boundary-$(date +%s)-${BASHPID}.bin"
created_object=0

request_args=(
  --config "${curl_config}"
  --silent --show-error
  --connect-timeout 5 --max-time 120
  --aws-sigv4 "aws:amz:${region}:s3"
)
anonymous_args=(--silent --show-error --connect-timeout 5 --max-time 30)
if [[ -n ${ca_file} ]]; then
  request_args+=(--cacert "${ca_file}")
  anonymous_args+=(--cacert "${ca_file}")
fi

request_status() {
  curl "${request_args[@]}" -o "${work_dir}/response" -w '%{http_code}' "$@"
}

cleanup() {
  local original_status=$? cleanup_failed=0 status=000
  trap - EXIT INT TERM
  set +e
  if (( created_object == 1 )); then
    status=$(request_status -X DELETE "${endpoint}/${allowed_bucket}/${object}" 2>/dev/null)
    if [[ ${status} != 204 && ${status} != 404 ]]; then
      printf 'failed to remove scoped probe object (HTTP %s)\n' "${status}" >&2
      cleanup_failed=1
    fi
  fi
  rm -rf -- "${work_dir}"
  if (( original_status != 0 )); then
    exit "${original_status}"
  fi
  exit "${cleanup_failed}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

head -c 1048576 /dev/urandom >"${work_dir}/payload.bin"
payload_digest=$(sha256sum "${work_dir}/payload.bin" | cut -d' ' -f1)
status=000
created_object=1
for _ in {1..20}; do
  status=$(request_status -X PUT --data-binary "@${work_dir}/payload.bin" \
    "${endpoint}/${allowed_bucket}/${object}")
  [[ ${status} == 200 ]] && break
  sleep 0.5
done
[[ ${status} == 200 ]] || { printf 'scoped write returned HTTP %s\n' "${status}" >&2; exit 1; }

status=$(request_status "${endpoint}/${allowed_bucket}/${object}")
[[ ${status} == 200 ]] || { printf 'scoped read returned HTTP %s\n' "${status}" >&2; exit 1; }
[[ $(sha256sum "${work_dir}/response" | cut -d' ' -f1) == "${payload_digest}" ]] || {
  printf 'scoped object digest mismatch\n' >&2
  exit 1
}

status=$(request_status -X PUT --data-binary forbidden "${endpoint}/${denied_bucket}/forbidden")
[[ ${status} == 403 ]] || { printf 'cross-namespace write returned HTTP %s\n' "${status}" >&2; exit 1; }
status=$(request_status "${endpoint}/${denied_bucket}/forbidden")
[[ ${status} == 403 ]] || { printf 'cross-namespace read returned HTTP %s\n' "${status}" >&2; exit 1; }
status=$(curl "${anonymous_args[@]}" -o "${work_dir}/anonymous-response" -w '%{http_code}' "${endpoint}/")
[[ ${status} == 403 ]] || { printf 'anonymous request returned HTTP %s\n' "${status}" >&2; exit 1; }

status=$(request_status -X DELETE "${endpoint}/${allowed_bucket}/${object}")
[[ ${status} == 204 ]] || { printf 'scoped delete returned HTTP %s\n' "${status}" >&2; exit 1; }
created_object=0
status=$(request_status "${endpoint}/${allowed_bucket}/${object}")
[[ ${status} == 404 ]] || { printf 'deleted object remained readable (HTTP %s)\n' "${status}" >&2; exit 1; }

printf 'rustfs scoped smoke: ok (integrity, CRUD, anonymous deny, cross-namespace deny)\n'
