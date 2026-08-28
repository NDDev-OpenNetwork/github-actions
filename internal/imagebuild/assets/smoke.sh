#!/usr/bin/env bash
set -Eeuo pipefail

: "${GHA_RUNNER_VERSION:?}"
: "${GHA_PUBLIC_HOST_ADDRESS:?}"
: "${GHA_EXPECTED_ROOT_DISK_GIB:?}"
: "${GHA_SCCACHE_VERSION:?}"
: "${GHA_SCCACHE_BINARY_SHA256:?}"
: "${GHA_TOOLCHAINS_B64:?}"
: "${GHA_GO_CACHE_SEED_COMMIT:?}"
: "${GHA_GO_CACHE_SEED_SHA256:?}"
: "${GHA_INSTANCE_TYPE:?}"

for _ in $(seq 1 300); do
  if [[ "$(systemctl is-active gha-incus-cloud-init.service 2>/dev/null || true)" == "active" ]] || [[ "${GHA_INSTANCE_TYPE}" == "virtual-machine" ]]; then
    break
  fi
  sleep 1
done
if [[ "${GHA_INSTANCE_TYPE}" == "container" ]] && [[ "$(systemctl is-active gha-incus-cloud-init.service 2>/dev/null || true)" != "active" ]]; then
  echo "container metadata cloud-init service did not become active within 300 seconds" >&2
  systemctl status gha-incus-cloud-init.service --no-pager >&2 || true
  exit 1
fi
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
[[ "$(jq -er .schema_version /etc/nddev/image-build.json)" == 4 ]]
[[ "$(jq -er .go_cache_seed.commit /etc/nddev/image-build.json)" == "${GHA_GO_CACHE_SEED_COMMIT}" ]]
[[ "$(jq -er .go_cache_seed.archive_sha256 /etc/nddev/image-build.json)" == "${GHA_GO_CACHE_SEED_SHA256}" ]]
test "$(jq -er .go_cache_seed.bytes /etc/nddev/image-build.json)" -gt 0
test -d /home/runner/.cache/go-build
test -d /home/runner/go/pkg/mod
test "$(stat --format='%U:%G' /home/runner/go /home/runner/go/bin /home/runner/go/pkg)" = $'runner:runner\nrunner:runner\nrunner:runner'
test -n "$(find /home/runner/.cache/go-build -type f -print -quit)"
[[ "$(sccache --version)" == "sccache ${GHA_SCCACHE_VERSION#v}" ]]
echo "${GHA_SCCACHE_BINARY_SHA256}  /usr/local/bin/sccache" | sha256sum --check --strict --status

# Every baked toolchain must be present, report its pinned version, and match
# the build record. A missing or drifted toolchain silently reintroduces the
# per-job install this image exists to remove, so it fails the smoke.
runner_tool_cache="$(jq -er .runner_tool_cache /etc/nddev/image-build.json)"
[[ "${runner_tool_cache}" == /home/runner/actions-runner/_work/_tool ]]
smoke_toolchains="$(printf '%s' "${GHA_TOOLCHAINS_B64}" | base64 --decode)"
mapfile -t smoke_toolchain_names < <(jq -r '.[].name' <<<"${smoke_toolchains}")
smoke_toolchain_set="$(printf '%s\n' "${smoke_toolchain_names[@]}" | LC_ALL=C sort | paste -sd, -)"
[[ "${smoke_toolchain_set}" == bun,gh,go,rust,uv \
  || "${smoke_toolchain_set}" == bun,gh,go,node22,node24,node25,pnpm,rust,uv,yarn \
  || "${smoke_toolchain_set}" == bun,flutter,gh,go,node22,node24,node25,pnpm,rust,uv,yarn ]]
for smoke_toolchain in "${smoke_toolchain_names[@]}"; do
  entry="$(jq -ce --arg name "${smoke_toolchain}" '.[] | select(.name == $name)' <<<"${smoke_toolchains}")"
  expected_version="$(jq -er .version <<<"${entry}")"
  expected_sha256="$(jq -er .archive_sha256 <<<"${entry}")"
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].version' /etc/nddev/image-build.json)" == "${expected_version}" ]]
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].archive_sha256' /etc/nddev/image-build.json)" == "${expected_sha256}" ]]
	case "${smoke_toolchain}" in
	  bun)
	    [[ "$(bun --version)" == "${expected_version}" ]]
	    # The path oven-sh/setup-bun probes before deciding to download.
	    test -x /home/runner/.bun/bin/bun
	    test -x /home/runner/.bun/bin/bunx
	    [[ "$(/home/runner/.bun/bin/bun --version)" == "${expected_version}" ]]
	    ;;
    flutter)
      # The exact path the consumer's setup action probes before downloading.
      flutter_root="${runner_tool_cache}/flutter/stable-${expected_version}-x64"
      test -x "${flutter_root}/flutter/bin/flutter"
      [[ "$(stat --format='%U' -- "${flutter_root}/flutter/bin/flutter")" == runner ]]
      [[ "$(jq -er .frameworkVersion \
        "${flutter_root}/flutter/bin/cache/flutter.version.json")" == "${expected_version}" ]]
      test -x "${flutter_root}/flutter/bin/dart"
      ;;
	  gh)
	    [[ "$(gh --version | head -n 1)" == "gh version ${expected_version} "* ]]
	    gh attestation --help >/dev/null
	    ;;
    go)
      go_root="${runner_tool_cache}/go/${expected_version}/x64"
      test -f "${go_root}.complete"
      test -x "${go_root}/bin/go"
      [[ "$(stat --format='%U' -- "${go_root}/bin/go")" == runner ]]
      [[ "$("${go_root}/bin/go" version)" == "go version go${expected_version} linux/amd64" ]]
		[[ "$(go version)" == "go version go${expected_version} linux/amd64" ]]
      ;;
    node22|node24|node25)
      node_root="${runner_tool_cache}/node/${expected_version}"
      test -f "${node_root}/x64.complete"
      test -x "${node_root}/x64/bin/node"
      [[ "$(stat --format='%U' -- "${node_root}/x64/bin/node")" == runner ]]
      [[ "$("${node_root}/x64/bin/node" --version)" == "v${expected_version}" ]]
      if [[ "${smoke_toolchain}" == node24 ]]; then
        [[ "$(node --version)" == "v${expected_version}" ]]
        npm --version >/dev/null
        corepack --version >/dev/null
      fi
      ;;
	pnpm) [[ "$(pnpm --version)" == "${expected_version}" ]] ;;
    rust)
      [[ "$(rustc --version)" == "rustc ${expected_version} "* ]]
      [[ "$(cargo --version)" == "cargo ${expected_version} "* ]]
      ;;
    uv)
      [[ "$(uv --version)" == "uv ${expected_version}"* ]]
      test -x /usr/local/bin/uvx
      # The tool-cache entry astral-sh/setup-uv resolves before downloading.
      uv_root="${runner_tool_cache}/uv/${expected_version}"
      test -f "${uv_root}/x86_64.complete"
      test -x "${uv_root}/x86_64/uv"
      [[ "$(stat --format='%U' -- "${uv_root}/x86_64/uv")" == runner ]]
      [[ "$("${uv_root}/x86_64/uv" --version)" == "uv ${expected_version}"* ]]
      ;;
	yarn) [[ "$(yarn --version)" == "${expected_version}" ]] ;;
  esac
done

python --version >/dev/null
python3 --version >/dev/null
python3 -m pip --version >/dev/null
pip --version >/dev/null
pip3 --version >/dev/null

for forbidden in \
  /var/run/docker.sock \
  /run/incus/unix.socket \
  /var/lib/incus/unix.socket \
  /var/snap/lxd/common/lxd/unix.socket \
  /dev/kvm; do
  test ! -e "${forbidden}"
done
nested_cpu_flags="absent"
if [[ "${GHA_INSTANCE_TYPE}" == "container" ]]; then
  nested_cpu_flags="not-applicable-without-kvm-device"
elif grep -Eq '(^|[[:space:]])(vmx|svm)([[:space:]]|$)' /proc/cpuinfo; then
  echo "nested virtualization CPU feature is visible" >&2
  exit 1
fi

expected_disk_bytes="$(( GHA_EXPECTED_ROOT_DISK_GIB * 1024 * 1024 * 1024 ))"
if [[ "${GHA_INSTANCE_TYPE}" == "virtual-machine" ]]; then
  root_source="$(findmnt --noheadings --output SOURCE / | tr -d '[:space:]')"
  parent_name="$(lsblk --noheadings --nodeps --output PKNAME "${root_source}" | tr -d '[:space:]')"
  if [[ -z "${parent_name}" ]]; then
    echo "cannot resolve root block device" >&2
    exit 1
  fi
  root_disk_bytes="$(blockdev --getsize64 "/dev/${parent_name}")"
  if [[ ! "${root_disk_bytes}" =~ ^[0-9]+$ ]] || (( root_disk_bytes < expected_disk_bytes )); then
    echo "root block device does not match the runtime profile" >&2
    exit 1
  fi
else
  test "$(systemctl is-enabled gha-incus-cloud-init.service)" = enabled
  systemctl is-active --quiet gha-incus-cloud-init.service
  test -L /dev/lxd
  test "$(readlink -f /dev/lxd)" = /dev/incus
fi

root_bytes="$(findmnt --bytes --noheadings --output SIZE / | tr -d '[:space:]')"
if [[ "${GHA_INSTANCE_TYPE}" == "container" ]]; then
  root_disk_bytes="${root_bytes}"
fi
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
test "$(stat --format='%U:%G:%a:%F' /home/runner/.gha-cache)" = 'runner:runner:700:directory'
test "$(command -v openssl)" = /usr/bin/openssl
test ! -e /usr/local/libexec/gha-warm-agent
test ! -e /etc/systemd/system/gha-warm-agent.service
test ! -e /etc/systemd/system/gha-warm-agent.path
test ! -e /etc/systemd/system/gha-warm-ready.service
test ! -e /run/gha-warm
test ! -e /var/lib/gha-warm
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

curl --fail --silent --show-error --location --max-time 20 https://github.com/robots.txt >/dev/null
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
  --arg nested_cpu_flags "${nested_cpu_flags}" \
  --argjson root_disk_bytes "${root_disk_bytes}" \
  --argjson root_filesystem_bytes "${root_bytes}" \
  '{runner_version:$runner_version,sccache_version:$sccache_version,toolchains:$toolchains,runner_tool_cache:$runner_tool_cache,machine_id:$machine_id,public_egress:$public_egress,host_route:$host_route,metadata_route:$metadata_route,forbidden_devices:"absent",nested_cpu_flags:$nested_cpu_flags,root_disk_bytes:$root_disk_bytes,root_filesystem_bytes:$root_filesystem_bytes,registration_state:"absent",startup_mode:"cold-only",ssh_server_package:"absent",ssh_units:"masked",ssh_listener:"absent"}'
