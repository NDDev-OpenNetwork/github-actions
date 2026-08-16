#!/usr/bin/env bash
set -euo pipefail

endpoint=${RUSTFS_ENDPOINT:?RUSTFS_ENDPOINT is required}
root_access_file=${RUSTFS_ACCESS_KEY_FILE:?RUSTFS_ACCESS_KEY_FILE is required}
root_secret_file=${RUSTFS_SECRET_KEY_FILE:?RUSTFS_SECRET_KEY_FILE is required}
ca_file=${RUSTFS_CA_FILE:?RUSTFS_CA_FILE is required}
region=${RUSTFS_REGION:-us-east-1}

export_access_file=/etc/gha-fleet/secrets/rustfs-diagnostics-access-key
export_secret_file=/etc/gha-fleet/secrets/rustfs-diagnostics-secret-key
export_access=gha-diagnostics-exporter
bucket=gha-diagnostics-canary
prefix=diagnostics/v1
policy=gha-diagnostics-canary-write
quota_bytes=1073741824

for command in curl openssl jq grep find install stat tr; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${EUID} == 0 ]] || { printf 'diagnostic exporter bootstrap must run as root\n' >&2; exit 2; }
[[ ${endpoint} =~ ^https://[A-Za-z0-9.:-]+$ ]] || { printf 'invalid RUSTFS_ENDPOINT\n' >&2; exit 2; }
[[ -r ${root_access_file} && -r ${root_secret_file} && -r ${ca_file} ]] || {
  printf 'root credential or CA file is not readable\n' >&2
  exit 2
}
[[ $(stat -c '%a' /etc/gha-fleet/secrets) == 700 ]] || {
  printf 'RustFS exporter credential parent must be mode 0700\n' >&2
  exit 2
}

root_access=$(tr -d '\r\n' <"${root_access_file}")
root_secret=$(tr -d '\r\n' <"${root_secret_file}")
[[ ${root_access} =~ ^[A-Za-z0-9_-]{12,128}$ ]] || { printf 'invalid root access key format\n' >&2; exit 2; }
[[ ${root_secret} =~ ^[A-Za-z0-9_+/=-]{32,256}$ ]] || { printf 'invalid root secret key format\n' >&2; exit 2; }

if [[ -e ${export_access_file} || -e ${export_secret_file} ]]; then
  [[ -f ${export_access_file} && ! -L ${export_access_file} && -f ${export_secret_file} && ! -L ${export_secret_file} ]] || {
    printf 'exporter credential pair is incomplete or unsafe\n' >&2
    exit 2
  }
  [[ $(stat -c '%U:%G:%a' "${export_access_file}") == root:root:600 ]] || {
    printf 'exporter access-key file ownership or mode is unsafe\n' >&2
    exit 2
  }
  [[ $(stat -c '%U:%G:%a' "${export_secret_file}") == root:root:600 ]] || {
    printf 'exporter secret-key file ownership or mode is unsafe\n' >&2
    exit 2
  }
  [[ $(tr -d '\r\n' <"${export_access_file}") == "${export_access}" ]] || {
    printf 'exporter access-key identity drifted\n' >&2
    exit 2
  }
else
  temporary_access=$(mktemp /etc/gha-fleet/secrets/.rustfs-diagnostics-access.XXXXXX)
  temporary_secret=$(mktemp /etc/gha-fleet/secrets/.rustfs-diagnostics-secret.XXXXXX)
  chmod 0600 "${temporary_access}" "${temporary_secret}"
  printf '%s\n' "${export_access}" >"${temporary_access}"
  openssl rand -base64 48 >"${temporary_secret}"
  install -o root -g root -m 0600 "${temporary_access}" "${export_access_file}"
  install -o root -g root -m 0600 "${temporary_secret}" "${export_secret_file}"
  find "${temporary_access}" "${temporary_secret}" -maxdepth 0 -type f -delete
fi

export_secret=$(tr -d '\r\n' <"${export_secret_file}")
[[ ${export_secret} =~ ^[A-Za-z0-9_+/=-]{32,256}$ ]] || { printf 'invalid exporter secret format\n' >&2; exit 2; }

work_dir=$(mktemp -d)
chmod 0700 "${work_dir}"
root_config=${work_dir}/root.conf
export_config=${work_dir}/export.conf
printf 'user = "%s:%s"\n' "${root_access}" "${root_secret}" >"${root_config}"
printf 'user = "%s:%s"\n' "${export_access}" "${export_secret}" >"${export_config}"
chmod 0600 "${root_config}" "${export_config}"
unset root_access root_secret export_secret

suffix="$(date +%s)-${BASHPID}"
denied_bucket="gha-diagnostics-denied-${suffix}"
probe_key="${prefix}/bootstrap/${suffix}.txt"
outside_key="outside-prefix/${suffix}.txt"
created_denied_bucket=0

root_args=(
  --config "${root_config}"
  --silent --show-error --connect-timeout 5 --max-time 120
  --cacert "${ca_file}" --aws-sigv4 "aws:amz:${region}:s3"
)
export_args=(
  --config "${export_config}"
  --silent --show-error --connect-timeout 5 --max-time 120
  --cacert "${ca_file}" --aws-sigv4 "aws:amz:${region}:s3"
)
anonymous_args=(--silent --show-error --connect-timeout 5 --max-time 30 --cacert "${ca_file}")

root_request() { curl "${root_args[@]}" --fail-with-body "$@"; }
root_status() { curl "${root_args[@]}" -o "${work_dir}/root-response" -w '%{http_code}' "$@"; }
export_status() { curl "${export_args[@]}" -o "${work_dir}/export-response" -w '%{http_code}' "$@"; }

cleanup() {
  local original_status=$? status=000
  trap - EXIT INT TERM
  set +e
  root_request -X DELETE "${endpoint}/${bucket}/${probe_key}" >/dev/null 2>&1
  if (( created_denied_bucket == 1 )); then
    for _ in {1..20}; do
      status=$(root_status -X DELETE "${endpoint}/${denied_bucket}" 2>/dev/null)
      [[ ${status} == 204 || ${status} == 404 ]] && break
      sleep 0.25
    done
  fi
  find "${work_dir}" -xdev -depth -delete
  exit "${original_status}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

status=$(root_status -X PUT "${endpoint}/${bucket}")
[[ ${status} == 200 || ${status} == 409 ]] || {
  printf 'create diagnostic bucket returned HTTP %s\n' "${status}" >&2
  exit 1
}
status=$(root_status -X PUT "${endpoint}/${denied_bucket}")
[[ ${status} == 200 ]] || { printf 'create denied audit bucket returned HTTP %s\n' "${status}" >&2; exit 1; }
created_denied_bucket=1

jq -n --arg bucket "${bucket}" --arg prefix "${prefix}" '{
  Version: "2012-10-17",
  Statement: [
    {
      Effect: "Allow",
      Action: ["s3:GetObject", "s3:PutObject"],
      Resource: ["arn:aws:s3:::" + $bucket + "/" + $prefix + "/*"]
    }
  ]
}' >"${work_dir}/policy.json"
root_request -X PUT -H 'Content-Type: application/json' --data-binary "@${work_dir}/policy.json" \
  "${endpoint}/rustfs/admin/v3/add-canned-policy?name=${policy}" >/dev/null

jq -n --rawfile secret "${export_secret_file}" '{secretKey: ($secret | gsub("[\\r\\n]"; "")), status: "enabled"}' \
  >"${work_dir}/user.json"
root_request -X PUT -H 'Content-Type: application/json' --data-binary "@${work_dir}/user.json" \
  "${endpoint}/rustfs/admin/v3/add-user?accessKey=${export_access}" >/dev/null
root_request -X PUT \
  "${endpoint}/rustfs/admin/v3/set-user-or-group-policy?policyName=${policy}&userOrGroup=${export_access}&isGroup=false" \
  >/dev/null

quota_ready=0
for _ in {1..180}; do
  status=$(root_status "${endpoint}/rustfs/admin/v3/quota-stats/${bucket}")
  if [[ ${status} == 200 ]]; then
    quota_ready=1
    break
  fi
  [[ ${status} == 503 ]] || { printf 'quota readiness returned HTTP %s\n' "${status}" >&2; exit 1; }
  sleep 1
done
(( quota_ready == 1 )) || { printf 'diagnostic bucket quota did not become authoritative\n' >&2; exit 1; }
jq -n --argjson quota "${quota_bytes}" '{quota: $quota, quota_type: "HARD"}' >"${work_dir}/quota.json"
root_request -X PUT -H 'Content-Type: application/json' --data-binary "@${work_dir}/quota.json" \
  "${endpoint}/rustfs/admin/v3/quota/${bucket}" >/dev/null
root_request "${endpoint}/rustfs/admin/v3/quota/${bucket}" -o "${work_dir}/quota-response.json"
jq -e --argjson quota "${quota_bytes}" '.quota == $quota and .quota_type == "HARD"' \
  "${work_dir}/quota-response.json" >/dev/null

printf '%s' \
  '<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ID>diagnostic-canary-seven-day-expiry</ID><Filter><Prefix>diagnostics/v1/</Prefix></Filter><Status>Enabled</Status><Expiration><Days>7</Days></Expiration></Rule></LifecycleConfiguration>' \
  >"${work_dir}/lifecycle.xml"
root_request -X PUT -H 'Content-Type: application/xml' --data-binary "@${work_dir}/lifecycle.xml" \
  "${endpoint}/${bucket}?lifecycle" >/dev/null
root_request "${endpoint}/${bucket}?lifecycle" -o "${work_dir}/lifecycle-response.xml"
for expected in \
  '<ID>diagnostic-canary-seven-day-expiry</ID>' \
  '<Prefix>diagnostics/v1/</Prefix>' \
  '<Days>7</Days>' \
  '<Status>Enabled</Status>'; do
  grep -F "${expected}" "${work_dir}/lifecycle-response.xml" >/dev/null || {
    printf 'diagnostic lifecycle response omitted %s\n' "${expected}" >&2
    exit 1
  }
done

for _ in {1..20}; do
  status=$(export_status -X PUT --data-binary 'diagnostic-exporter-bootstrap' "${endpoint}/${bucket}/${probe_key}")
  [[ ${status} == 200 ]] && break
  sleep 0.5
done
[[ ${status} == 200 ]] || { printf 'exporter identity could not write its prefix (HTTP %s)\n' "${status}" >&2; exit 1; }
status=$(export_status "${endpoint}/${bucket}/${probe_key}")
[[ ${status} == 200 ]] || { printf 'exporter identity could not read its object (HTTP %s)\n' "${status}" >&2; exit 1; }
status=$(export_status -I "${endpoint}/${bucket}/${probe_key}")
[[ ${status} == 200 ]] || { printf 'exporter identity could not HEAD its object (HTTP %s)\n' "${status}" >&2; exit 1; }
status=$(export_status "${endpoint}/${bucket}?list-type=2&prefix=${prefix}/")
[[ ${status} == 403 ]] || { printf 'exporter identity unexpectedly received HTTP %s for object listing\n' "${status}" >&2; exit 1; }
status=$(export_status "${endpoint}/${bucket}?location")
[[ ${status} == 403 ]] || { printf 'exporter identity unexpectedly received HTTP %s for bucket location\n' "${status}" >&2; exit 1; }
status=$(export_status -X DELETE "${endpoint}/${bucket}/${probe_key}")
[[ ${status} == 403 ]] || { printf 'exporter identity unexpectedly received HTTP %s for delete\n' "${status}" >&2; exit 1; }
status=$(export_status -X PUT --data-binary denied "${endpoint}/${bucket}/${outside_key}")
[[ ${status} == 403 ]] || { printf 'exporter identity unexpectedly received HTTP %s outside prefix\n' "${status}" >&2; exit 1; }
status=$(export_status -X PUT --data-binary denied "${endpoint}/${denied_bucket}/${probe_key}")
[[ ${status} == 403 ]] || { printf 'exporter identity unexpectedly received HTTP %s across bucket\n' "${status}" >&2; exit 1; }
status=$(curl "${anonymous_args[@]}" -o "${work_dir}/anonymous-response" -w '%{http_code}' \
  "${endpoint}/${bucket}/${probe_key}")
[[ ${status} == 403 ]] || { printf 'anonymous diagnostic object read returned HTTP %s\n' "${status}" >&2; exit 1; }

printf 'rustfs diagnostic exporter bootstrap: ok (GET/PUT/HEAD allow, list/location/delete/cross-prefix/cross-bucket/anonymous deny, quota, lifecycle)\n'
