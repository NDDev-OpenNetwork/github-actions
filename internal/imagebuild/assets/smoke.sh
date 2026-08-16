#!/usr/bin/env bash
set -Eeuo pipefail

: "${GHA_RUNNER_VERSION:?}"
: "${GHA_PUBLIC_HOST_ADDRESS:?}"
: "${GHA_EXPECTED_ROOT_DISK_GIB:?}"
: "${GHA_SCCACHE_VERSION:?}"
: "${GHA_SCCACHE_BINARY_SHA256:?}"
: "${GHA_TOOLCHAINS_B64:?}"

cloud-init status --wait >/dev/null
runner_version="${GHA_RUNNER_VERSION#v}"
actual_version="$(/opt/cache/actions-runner/latest/bin/Runner.Listener --version | tail -n 1 | tr -d '\r')"
[[ "${actual_version}" == "${runner_version}" ]]
test -s /etc/machine-id
if grep -qx uninitialized /etc/machine-id; then
  echo "machine-id was not regenerated" >&2
  exit 1
fi
test -s /etc/nddev/image-build.json
[[ "$(jq -er .sccache_version /etc/nddev/image-build.json)" == "${GHA_SCCACHE_VERSION}" ]]
[[ "$(jq -er .sccache_binary_sha256 /etc/nddev/image-build.json)" == "${GHA_SCCACHE_BINARY_SHA256}" ]]
[[ "$(sccache --version)" == "sccache ${GHA_SCCACHE_VERSION#v}" ]]
echo "${GHA_SCCACHE_BINARY_SHA256}  /usr/local/bin/sccache" | sha256sum --check --strict --status

# Every baked toolchain must be present, report its pinned version, and match
# the build record. A missing or drifted toolchain silently reintroduces the
# per-job install this image exists to remove, so it fails the smoke.
runner_tool_cache="$(jq -er .runner_tool_cache /etc/nddev/image-build.json)"
[[ "${runner_tool_cache}" == /home/runner/actions-runner/_work/_tool ]]
smoke_toolchains="$(printf '%s' "${GHA_TOOLCHAINS_B64}" | base64 --decode)"
mapfile -t smoke_toolchain_names < <(jq -r '.[].name' <<<"${smoke_toolchains}")
[[ "$(printf '%s\n' "${smoke_toolchain_names[@]}" | LC_ALL=C sort | paste -sd, -)" == bun,go,node,rust,uv ]]
for smoke_toolchain in "${smoke_toolchain_names[@]}"; do
  entry="$(jq -ce --arg name "${smoke_toolchain}" '.[] | select(.name == $name)' <<<"${smoke_toolchains}")"
  expected_version="$(jq -er .version <<<"${entry}")"
  expected_sha256="$(jq -er .archive_sha256 <<<"${entry}")"
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].version' /etc/nddev/image-build.json)" == "${expected_version}" ]]
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].archive_sha256' /etc/nddev/image-build.json)" == "${expected_sha256}" ]]
  case "${smoke_toolchain}" in
    bun) [[ "$(bun --version)" == "${expected_version}" ]] ;;
    go)
      go_root="${runner_tool_cache}/go/${expected_version}/x64"
      test -f "${go_root}.complete"
      test -x "${go_root}/bin/go"
      [[ "$(stat --format='%U' -- "${go_root}/bin/go")" == runner ]]
      [[ "$("${go_root}/bin/go" version)" == "go version go${expected_version} linux/amd64" ]]
      ;;
    node)
      # CodeQL spawns `node` by name, so the smoke proves the PATH entry, not
      # only the tool-cache one that setup-node would use.
      [[ "$(node --version)" == "v${expected_version}" ]]
      [[ "$(command -v node)" == /usr/local/bin/node ]]
      [[ -n "$(npm --version)" ]]
      node_root="${runner_tool_cache}/node/${expected_version}/x64"
      test -f "${node_root}.complete"
      test -x "${node_root}/bin/node"
      ;;
    rust)
      [[ "$(rustc --version)" == "rustc ${expected_version} "* ]]
      [[ "$(cargo --version)" == "cargo ${expected_version} "* ]]
      ;;
    uv)
      [[ "$(uv --version)" == "uv ${expected_version}"* ]]
      test -x /usr/local/bin/uvx
      ;;
  esac
done

for forbidden in \
  /var/run/docker.sock \
  /run/incus/unix.socket \
  /var/lib/incus/unix.socket \
  /var/snap/lxd/common/lxd/unix.socket \
  /dev/kvm; do
  test ! -e "${forbidden}"
done
if grep -Eq '(^|[[:space:]])(vmx|svm)([[:space:]]|$)' /proc/cpuinfo; then
  echo "nested virtualization CPU feature is visible" >&2
  exit 1
fi

root_source="$(findmnt --noheadings --output SOURCE / | tr -d '[:space:]')"
parent_name="$(lsblk --noheadings --nodeps --output PKNAME "${root_source}" | tr -d '[:space:]')"
if [[ -z "${parent_name}" ]]; then
  echo "cannot resolve root block device" >&2
  exit 1
fi
root_disk_bytes="$(blockdev --getsize64 "/dev/${parent_name}")"
expected_disk_bytes="$(( GHA_EXPECTED_ROOT_DISK_GIB * 1024 * 1024 * 1024 ))"
if [[ ! "${root_disk_bytes}" =~ ^[0-9]+$ ]] || (( root_disk_bytes < expected_disk_bytes )); then
  echo "root block device does not match the runtime profile" >&2
  exit 1
fi

root_bytes="$(findmnt --bytes --noheadings --output SIZE / | tr -d '[:space:]')"
# GPT, EFI, /boot and ext4 metadata consume part of the nominal profile disk.
minimum_root_bytes="$(( (GHA_EXPECTED_ROOT_DISK_GIB - 4) * 1024 * 1024 * 1024 ))"
if [[ ! "${root_bytes}" =~ ^[0-9]+$ ]] || (( root_bytes < minimum_root_bytes )); then
  echo "root filesystem did not expand to the runtime profile" >&2
  exit 1
fi
if find /opt/cache/actions-runner -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; then
  echo "runner registration state found in smoke VM" >&2
  exit 1
fi
id runner >/dev/null
[[ "$(id --groups --name runner | tr ' ' '\n' | grep -vx runner | LC_ALL=C sort | paste -sd' ' -)" == "sudo" ]]
test -x /home/runner/actions-runner/bin/Runner.Listener
test -x /usr/local/libexec/gha-warm-agent
test "$(command -v openssl)" = /usr/bin/openssl
test "$(systemctl is-enabled gha-warm-agent.path)" = enabled
systemctl is-active --quiet gha-warm-agent.path
systemctl is-active --quiet gha-warm-ready.service
grep -Fx 'ready-unregistered-v1' /run/gha-warm/ready >/dev/null
test -d /run/gha-warm/assignments
test -z "$(find /run/gha-warm/assignments /var/lib/gha-warm/claims -mindepth 1 -print -quit)"
if dpkg -s openssh-server >/dev/null 2>&1; then
  echo "OpenSSH server package is installed in smoke VM" >&2
  exit 1
fi
for unit in ssh.service ssh.socket sshd.service sshd.socket; do
  if [[ "$(systemctl is-enabled "${unit}" 2>/dev/null || true)" != "masked" ]]; then
    echo "SSH unit ${unit} is not masked in smoke VM" >&2
    exit 1
  fi
  if systemctl is-active --quiet "${unit}"; then
    echo "SSH unit ${unit} is active in smoke VM" >&2
    exit 1
  fi
done
if ss -H -lnt 'sport = :22' | grep -q .; then
  echo "SSH listener is present in smoke VM" >&2
  exit 1
fi

curl --fail --silent --show-error --location --max-time 20 https://api.github.com/meta >/dev/null
if curl --silent --max-time 3 http://169.254.169.254/latest/meta-data/ >/dev/null 2>&1; then
  echo "metadata endpoint is reachable" >&2
  exit 1
fi
if timeout 3 bash -c "</dev/tcp/${GHA_PUBLIC_HOST_ADDRESS}/22" 2>/dev/null; then
  echo "CI host SSH is reachable from worker" >&2
  exit 1
fi

jq -n \
  --arg runner_version "${actual_version}" \
  --arg sccache_version "${GHA_SCCACHE_VERSION}" \
  --argjson toolchains "$(jq -c '.toolchains | map_values(.version)' /etc/nddev/image-build.json)" \
  --arg runner_tool_cache "${runner_tool_cache}" \
  --arg machine_id "$(cat /etc/machine-id)" \
  --arg public_egress "ok" \
  --arg host_route "blocked" \
  --arg metadata_route "blocked" \
  --arg nested_cpu_flags "absent" \
  --argjson root_disk_bytes "${root_disk_bytes}" \
  --argjson root_filesystem_bytes "${root_bytes}" \
  '{runner_version:$runner_version,sccache_version:$sccache_version,toolchains:$toolchains,runner_tool_cache:$runner_tool_cache,machine_id:$machine_id,public_egress:$public_egress,host_route:$host_route,metadata_route:$metadata_route,forbidden_devices:"absent",nested_cpu_flags:$nested_cpu_flags,root_disk_bytes:$root_disk_bytes,root_filesystem_bytes:$root_filesystem_bytes,registration_state:"absent",warm_agent:"ready-unregistered",ssh_server_package:"absent",ssh_units:"masked",ssh_listener:"absent"}'
