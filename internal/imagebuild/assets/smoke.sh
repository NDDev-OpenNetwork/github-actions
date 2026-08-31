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
: "${GHA_PROVIDES:?}"

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
# The runner's cache root, proved by writing to it rather than by reading its
# mode. The image shipped /home/runner/.cache as root:root for months while
# .cache/go-build under it was correct, because both the build guard and this
# smoke checked the seeded trees and never the directory they hang from. uv,
# pip, npm and go all default under here, so a root-owned .cache is a job
# failure that reads as a code failure: "Failed to initialize cache at
# /home/runner/.cache/uv: Permission denied".
test "$(stat --format='%U:%G' /home/runner/.cache)" = "runner:runner"
runuser -u runner -- test -w /home/runner/.cache
runuser -u runner -- sh -c 'set -e; d=$(mktemp -d /home/runner/.cache/gha-smoke-XXXXXX); rmdir "$d"'
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
[[ "${smoke_toolchain_set}" == bun,codeql,gh,go,rustup,uv \
  || "${smoke_toolchain_set}" == bun,codeql,gh,go,node22,node24,node25,pnpm,rustup,uv,yarn \
  || "${smoke_toolchain_set}" == bun,codeql,flutter,gh,go,node22,node24,node25,pnpm,rustup,uv,yarn ]]
for smoke_toolchain in "${smoke_toolchain_names[@]}"; do
  entry="$(jq -ce --arg name "${smoke_toolchain}" '.[] | select(.name == $name)' <<<"${smoke_toolchains}")"
  expected_version="$(jq -er .version <<<"${entry}")"
  expected_sha256="$(jq -er .archive_sha256 <<<"${entry}")"
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].version' /etc/nddev/image-build.json)" == "${expected_version}" ]]
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].archive_sha256' /etc/nddev/image-build.json)" == "${expected_sha256}" ]]
	case "${smoke_toolchain}" in
    codeql)
      codeql_root="${runner_tool_cache}/CodeQL/0.0.0-codeql-bundle-v${expected_version}"
      test -f "${codeql_root}/x64.complete"
      [[ "$(stat --format='%U' -- "${codeql_root}/x64/codeql/codeql")" == runner ]]
      [[ "$(runuser -u runner -- "${codeql_root}/x64/codeql/codeql" version --format=terse)" == "${expected_version}" ]]
      ;;
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
    rustup)
      # Verified exactly as a job sees it: through the runner user, whose
      # home is where the action and the shims resolve toolchains. The
      # root-eye view once stayed green while every job saw nothing.
      [[ "$(runuser -u runner -- env HOME=/home/runner rustup --version 2>/dev/null)" == "rustup ${expected_version} "* ]]
      [[ "$(stat --format='%U' -- /home/runner/.rustup)" == runner ]]
      [[ "$(stat --format='%U' -- /home/runner/.cargo)" == runner ]]
      mapfile -t smoke_rust_channels < <(jq -r '.channels[]?' <<<"${entry}")
      smoke_rust_default="$(jq -er '.default_channel' <<<"${entry}")"
      [[ ${#smoke_rust_channels[@]} -gt 0 ]]
      for smoke_rust_channel in "${smoke_rust_channels[@]}"; do
        [[ "$(runuser -u runner -- env HOME=/home/runner rustup run "${smoke_rust_channel}" rustc --version)" == "rustc ${smoke_rust_channel}"* ]]
        [[ "$(runuser -u runner -- env HOME=/home/runner rustup run "${smoke_rust_channel}" cargo clippy --version)" == clippy* ]]
        [[ "$(runuser -u runner -- env HOME=/home/runner rustup run "${smoke_rust_channel}" rustfmt --version)" == rustfmt* ]]
      done
      [[ "$(runuser -u runner -- env HOME=/home/runner rustc --version)" == "rustc ${smoke_rust_default}"* ]]
      [[ "$(runuser -u runner -- env HOME=/home/runner cargo --version)" == "cargo "* ]]
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

# bubblewrap must be able to actually create its sandbox as the job user; the
# binary being present has already lied about this once.
test -f /etc/apparmor.d/bwrap-userns
runuser -u runner -- env HOME=/home/runner bwrap --ro-bind / / true

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
# Observed, not declared. This field read "cold-only" as a literal for as long
# as the image had no warm agent, and went on reading it after the agent came
# back -- a report that can only say one thing says nothing. It is derived from
# the units the image actually carries.
startup_mode=cold-only
if [ -x /usr/local/libexec/gha-warm-agent ] && [ "$(systemctl is-enabled gha-warm-agent.path 2>/dev/null)" = enabled ]; then
  startup_mode=warm-capable
fi
test -x /usr/local/libexec/gha-warm-agent
test "$(stat --format='%U:%G:%a:%F' /home/runner/.gha-cache)" = 'runner:runner:700:directory'
test "$(command -v openssl)" = /usr/bin/openssl
test "$(systemctl is-enabled gha-warm-agent.path)" = enabled
for _ in $(seq 1 60); do
  if systemctl is-active --quiet gha-warm-agent.path && systemctl is-active --quiet gha-warm-ready.service; then
    break
  fi
  sleep 1
done
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

curl --fail --silent --show-error --location --max-time 20 https://github.com/robots.txt >/dev/null
if curl --silent --max-time 3 http://169.254.169.254/latest/meta-data/ >/dev/null 2>&1; then
  echo "metadata endpoint is reachable" >&2
  exit 1
fi
if timeout 3 bash -c "</dev/tcp/${GHA_PUBLIC_HOST_ADDRESS}/22" 2>/dev/null; then
  echo "CI host SSH is reachable from worker" >&2
  exit 1
fi

# The command surface the manifest promises a job. Nothing stated it before, so
# consumers guessed: two repositories opened their command with actionlint,
# which the image does not carry, and their CI did not start for weeks; another
# apt-installs cmake and ninja-build every job, both of which have been here all
# along. Checked as a job sees it -- as runner, through a login shell -- because
# a tool on root's PATH is not a tool the job can call. A promise the image
# cannot keep fails the build rather than reaching a consumer.
missing_provided=()
for provided in ${GHA_PROVIDES}; do
  if ! sudo -u runner -- bash -lc "command -v -- '${provided}' >/dev/null 2>&1"; then
    missing_provided+=("${provided}")
  fi
done
if [[ "${#missing_provided[@]}" -gt 0 ]]; then
  printf 'image promises commands it does not carry: %s\n' "${missing_provided[*]}" >&2
  exit 1
fi
provided_count="$(printf '%s\n' ${GHA_PROVIDES} | wc -l)"

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
  --argjson provided_commands "${provided_count}" \
  --arg startup_mode "${startup_mode}" \
  '{runner_version:$runner_version,sccache_version:$sccache_version,toolchains:$toolchains,runner_tool_cache:$runner_tool_cache,machine_id:$machine_id,public_egress:$public_egress,host_route:$host_route,metadata_route:$metadata_route,forbidden_devices:"absent",nested_cpu_flags:$nested_cpu_flags,root_disk_bytes:$root_disk_bytes,root_filesystem_bytes:$root_filesystem_bytes,provided_commands:$provided_commands,registration_state:"absent",startup_mode:$startup_mode,ssh_server_package:"absent",ssh_units:"masked",ssh_listener:"absent"}'
