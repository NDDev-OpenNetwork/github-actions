#!/usr/bin/env bash
set -euo pipefail

endpoint=${RUSTFS_ENDPOINT:?RUSTFS_ENDPOINT is required}
root_access_file=${RUSTFS_ACCESS_KEY_FILE:?RUSTFS_ACCESS_KEY_FILE is required}
root_secret_file=${RUSTFS_SECRET_KEY_FILE:?RUSTFS_SECRET_KEY_FILE is required}
region=${RUSTFS_REGION:-us-east-1}
ca_file=${RUSTFS_CA_FILE:-}
accelerated=${RUSTFS_LIFECYCLE_AUDIT_ACCELERATED:-0}

for command in curl grep; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${endpoint} =~ ^https?://[A-Za-z0-9.:-]+$ ]] || { printf 'invalid RUSTFS_ENDPOINT\n' >&2; exit 2; }
[[ -r ${root_access_file} && -r ${root_secret_file} ]] || { printf 'root credential file is not readable\n' >&2; exit 2; }
if [[ -n ${ca_file} && ! -r ${ca_file} ]]; then
  printf 'RUSTFS_CA_FILE is not readable\n' >&2
  exit 2
fi
[[ ${accelerated} == 1 ]] || {
  printf 'RUSTFS_LIFECYCLE_AUDIT_ACCELERATED=1 is required for this destructive time-compressed audit\n' >&2
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
bucket="gha-lifecycle-${suffix}"
expiring_object=expire/object.bin
survivor_object=keep/object.bin
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
  root_request -X DELETE "${endpoint}/${bucket}?lifecycle" >/dev/null 2>&1 || true
  root_request -X DELETE "${endpoint}/${bucket}/${expiring_object}" >/dev/null 2>&1 || true
  root_request -X DELETE "${endpoint}/${bucket}/${survivor_object}" >/dev/null 2>&1 || true
  for _ in {1..20}; do
    status=$(request_status -X DELETE "${endpoint}/${bucket}" 2>/dev/null)
    if [[ ${status} == 204 || ${status} == 404 ]]; then
      return 0
    fi
    sleep 0.25
  done
  printf 'failed to remove lifecycle audit bucket (HTTP %s)\n' "${status}" >&2
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
root_request -X PUT --data-binary expiring "${endpoint}/${bucket}/${expiring_object}" >/dev/null
root_request -X PUT --data-binary survivor "${endpoint}/${bucket}/${survivor_object}" >/dev/null

printf '%s' \
  '<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ID>expire-audit-prefix</ID><Filter><Prefix>expire/</Prefix></Filter><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule></LifecycleConfiguration>' \
  >"${work_dir}/lifecycle.xml"
root_request -X PUT -H 'Content-Type: application/xml' --data-binary "@${work_dir}/lifecycle.xml" \
  "${endpoint}/${bucket}?lifecycle" >/dev/null
root_request "${endpoint}/${bucket}?lifecycle" -o "${work_dir}/lifecycle-response.xml"
grep -F '<ID>expire-audit-prefix</ID>' "${work_dir}/lifecycle-response.xml" >/dev/null
grep -F '<Prefix>expire/</Prefix>' "${work_dir}/lifecycle-response.xml" >/dev/null
grep -F '<Days>1</Days>' "${work_dir}/lifecycle-response.xml" >/dev/null
grep -F '<Status>Enabled</Status>' "${work_dir}/lifecycle-response.xml" >/dev/null

status=$(curl "${anonymous_args[@]}" -o "${work_dir}/anonymous-response" -w '%{http_code}' \
  "${endpoint}/${bucket}?lifecycle")
[[ ${status} == 403 ]] || { printf 'anonymous lifecycle request returned HTTP %s\n' "${status}" >&2; exit 1; }

expired=0
for _ in {1..90}; do
  status=$(request_status "${endpoint}/${bucket}/${expiring_object}")
  if [[ ${status} == 404 ]]; then
    expired=1
    break
  fi
  [[ ${status} == 200 ]] || { printf 'expiring object returned HTTP %s\n' "${status}" >&2; exit 1; }
  sleep 1
done
(( expired == 1 )) || { printf 'accelerated lifecycle did not expire the matching object\n' >&2; exit 1; }
status=$(request_status "${endpoint}/${bucket}/${survivor_object}")
[[ ${status} == 200 ]] || { printf 'lifecycle removed the negative-control object (HTTP %s)\n' "${status}" >&2; exit 1; }

printf 'rustfs lifecycle smoke: ok (round-trip, prefix isolation, scanner expiry, anonymous deny)\n'
