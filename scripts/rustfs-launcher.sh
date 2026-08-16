#!/usr/bin/env bash
set -euo pipefail
umask 077

credential_directory=${CREDENTIALS_DIRECTORY:?CREDENTIALS_DIRECTORY is required}
rpc_secret_file=${credential_directory}/rustfs-rpc-secret
root_secret_file=${credential_directory}/rustfs-secret-key

for credential in "${rpc_secret_file}" "${root_secret_file}"; do
  [[ -f ${credential} && -r ${credential} ]] || {
    printf 'required RustFS credential is not readable\n' >&2
    exit 1
  }
done

mapfile -t rpc_lines <"${rpc_secret_file}"
mapfile -t root_lines <"${root_secret_file}"
[[ ${#rpc_lines[@]} == 1 && ${#root_lines[@]} == 1 ]] || {
  printf 'RustFS credentials must contain exactly one line\n' >&2
  exit 1
}
rpc_secret=${rpc_lines[0]}
root_secret=${root_lines[0]}
[[ ${rpc_secret} =~ ^[A-Za-z0-9_+/=-]{43,256}$ ]] || {
  printf 'RustFS RPC credential has an invalid format\n' >&2
  exit 1
}
[[ ${rpc_secret} != rustfsadmin && ${rpc_secret} != "rustfs rpc" && ${rpc_secret} != "${root_secret}" ]] || {
  printf 'RustFS RPC credential must be independent and non-default\n' >&2
  exit 1
}

export RUSTFS_RPC_SECRET=${rpc_secret}
unset rpc_secret root_secret rpc_lines root_lines
exec /usr/local/bin/rustfs server /var/lib/gha-cache/rustfs/data
