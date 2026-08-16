#!/usr/bin/env bash
set -euo pipefail

endpoint=${RUSTFS_ENDPOINT:?RUSTFS_ENDPOINT is required}
root_access_file=${RUSTFS_ACCESS_KEY_FILE:?RUSTFS_ACCESS_KEY_FILE is required}
root_secret_file=${RUSTFS_SECRET_KEY_FILE:?RUSTFS_SECRET_KEY_FILE is required}
region=${RUSTFS_REGION:-us-east-1}
ca_file=${RUSTFS_CA_FILE:-}

for command in curl openssl awk; do
  command -v "${command}" >/dev/null || { printf 'missing command: %s\n' "${command}" >&2; exit 2; }
done
[[ ${endpoint} =~ ^https?://[A-Za-z0-9.:-]+$ ]] || { printf 'invalid RUSTFS_ENDPOINT\n' >&2; exit 2; }
[[ -r ${root_access_file} && -r ${root_secret_file} ]] || { printf 'root credential file is not readable\n' >&2; exit 2; }
if [[ -n ${ca_file} && ! -r ${ca_file} ]]; then
  printf 'RUSTFS_CA_FILE is not readable\n' >&2
  exit 2
fi

root_access=$(tr -d '\r\n' <"${root_access_file}")
root_secret=$(tr -d '\r\n' <"${root_secret_file}")
[[ ${root_access} =~ ^[A-Za-z0-9_-]{12,128}$ ]] || { printf 'invalid root access key format\n' >&2; exit 2; }
[[ ${root_secret} =~ ^[A-Za-z0-9_+/=-]{32,256}$ ]] || { printf 'invalid root secret key format\n' >&2; exit 2; }

work_dir=$(mktemp -d)
chmod 0700 "${work_dir}"
root_config=${work_dir}/root.conf
user_config=${work_dir}/user.conf
printf 'user = "%s:%s"\n' "${root_access}" "${root_secret}" >"${root_config}"
chmod 0600 "${root_config}"
unset root_access root_secret

suffix="$(date +%s)-${BASHPID}"
bucket="gha-iam-${suffix}"
denied_bucket="gha-iam-denied-${suffix}"
user="gha-iam-${suffix}"
policy="gha-iam-${suffix}"
user_secret=$(openssl rand -hex 32)
printf 'user = "%s:%s"\n' "${user}" "${user_secret}" >"${user_config}"
chmod 0600 "${user_config}"

root_args=(--config "${root_config}" --silent --show-error --connect-timeout 5 --max-time 120 --aws-sigv4 "aws:amz:${region}:s3")
user_args=(--config "${user_config}" --silent --show-error --connect-timeout 5 --max-time 120 --aws-sigv4 "aws:amz:${region}:s3")
if [[ -n ${ca_file} ]]; then
  root_args+=(--cacert "${ca_file}")
  user_args+=(--cacert "${ca_file}")
fi

created_bucket=0
created_denied_bucket=0
created_policy=0
created_user=0

root_request() { curl "${root_args[@]}" --fail-with-body "$@"; }
user_status() { curl "${user_args[@]}" -o "${work_dir}/response" -w '%{http_code}' "$@"; }

delete_bucket() {
  local target=$1 status=000
  root_request -X DELETE "${endpoint}/${target}/allowed" >/dev/null 2>&1 || true
  root_request -X DELETE "${endpoint}/${target}/denied" >/dev/null 2>&1 || true
  for _ in {1..20}; do
    status=$(curl "${root_args[@]}" -o "${work_dir}/cleanup-response" -w '%{http_code}' \
      -X DELETE "${endpoint}/${target}" 2>/dev/null)
    if [[ ${status} == 204 || ${status} == 404 ]]; then
      return 0
    fi
    sleep 0.25
  done
  printf 'failed to remove audit bucket %s (HTTP %s)\n' "${target}" "${status}" >&2
  return 1
}

cleanup() {
  local original_status=$? cleanup_failed=0
  trap - EXIT INT TERM
  set +e
  if (( created_user == 1 )); then
    root_request -X DELETE "${endpoint}/rustfs/admin/v3/remove-user?accessKey=${user}" >/dev/null 2>&1
  fi
  if (( created_policy == 1 )); then
    root_request -X DELETE "${endpoint}/rustfs/admin/v3/remove-canned-policy?name=${policy}" >/dev/null 2>&1
  fi
  if (( created_bucket == 1 )); then
    delete_bucket "${bucket}" || cleanup_failed=1
  fi
  if (( created_denied_bucket == 1 )); then
    delete_bucket "${denied_bucket}" || cleanup_failed=1
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
root_request -X PUT "${endpoint}/${denied_bucket}" >/dev/null
created_denied_bucket=1

printf '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket"],"Resource":["arn:aws:s3:::%s"]},{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::%s/*"]}]}' \
  "${bucket}" "${bucket}" >"${work_dir}/policy.json"
root_request -X PUT -H 'Content-Type: application/json' --data-binary "@${work_dir}/policy.json" \
  "${endpoint}/rustfs/admin/v3/add-canned-policy?name=${policy}" >/dev/null
created_policy=1

printf '{"secretKey":"%s","status":"enabled"}' "${user_secret}" >"${work_dir}/user.json"
unset user_secret
root_request -X PUT -H 'Content-Type: application/json' --data-binary "@${work_dir}/user.json" \
  "${endpoint}/rustfs/admin/v3/add-user?accessKey=${user}" >/dev/null
created_user=1

status=$(user_status -X PUT --data-binary 'denied-before-attach' "${endpoint}/${bucket}/before-attach")
[[ ${status} == 403 ]] || { printf 'unscoped user unexpectedly received HTTP %s before policy attach\n' "${status}" >&2; exit 1; }

printf '{"policies":["%s"],"user":"%s"}' "${policy}" "${user}" >"${work_dir}/attach.json"
root_request -X POST -H 'Content-Type: application/json' --data-binary "@${work_dir}/attach.json" \
  "${endpoint}/rustfs/admin/v3/idp/builtin/policy/attach" >/dev/null

for _ in {1..20}; do
  status=$(user_status -X PUT --data-binary 'allowed' "${endpoint}/${bucket}/allowed")
  [[ ${status} == 200 ]] && break
  sleep 0.5
done
[[ ${status} == 200 ]] || { printf 'scoped user did not gain permitted write access (HTTP %s)\n' "${status}" >&2; exit 1; }
status=$(user_status -X PUT --data-binary 'denied-cross-bucket' "${endpoint}/${denied_bucket}/denied")
[[ ${status} == 403 ]] || { printf 'scoped user unexpectedly received HTTP %s for cross-bucket write\n' "${status}" >&2; exit 1; }

root_request -X DELETE "${endpoint}/rustfs/admin/v3/remove-user?accessKey=${user}" >/dev/null
created_user=0
status=$(user_status -X PUT --data-binary 'denied-after-delete' "${endpoint}/${bucket}/after-delete")
[[ ${status} == 403 ]] || { printf 'deleted user unexpectedly received HTTP %s\n' "${status}" >&2; exit 1; }

printf 'rustfs IAM smoke: ok (unscoped deny, scoped allow, cross-bucket deny, revocation)\n'
