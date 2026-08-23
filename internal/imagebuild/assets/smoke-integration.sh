#!/usr/bin/env bash
set -Eeuo pipefail

report_error() {
  local rc="$1"
  local line="$2"
  local command="$3"
  printf >&2 'integration smoke failed at line %s: %s (exit %s)\n' "${line}" "${command}" "${rc}"
  exit "${rc}"
}

trap 'report_error "$?" "${LINENO:-0}" "$BASH_COMMAND"' ERR

: "${GHA_RUNNER_VERSION:?}"
: "${GHA_PUBLIC_HOST_ADDRESS:?}"
: "${GHA_EXPECTED_ROOT_DISK_GIB:?}"
: "${GHA_DOCKER_ACTION_BASE_REF:?}"
: "${GHA_DOCKER_STORAGE_DRIVER:?}"
: "${GHA_INSTANCE_TYPE:?}"
[[ "${GHA_INSTANCE_TYPE}" == "virtual-machine" || "${GHA_INSTANCE_TYPE}" == "container" ]]
: "${GHA_SCCACHE_VERSION:?}"
: "${GHA_SCCACHE_BINARY_SHA256:?}"
: "${GHA_TOOLCHAINS_B64:?}"
: "${GHA_BROWSER:=}"

cleanup_paths=()
smoke_user_created=0
cleanup() {
  local original_status=$?
  trap - ERR EXIT INT TERM
  set +e
  docker rm --force gha-image-service-smoke >/dev/null 2>&1
  docker network rm gha-image-network-smoke >/dev/null 2>&1
  docker image rm nddev/gha-action-smoke:runtime >/dev/null 2>&1
  docker builder prune --all --force >/dev/null 2>&1
  if (( smoke_user_created == 1 )); then
    userdel --remove gha-docker-smoke >/dev/null 2>&1
  fi
  for path in "${cleanup_paths[@]}"; do
    rm -rf -- "${path}"
  done
  exit "${original_status}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

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
[[ "$(jq -er .image_variant /etc/nddev/image-build.json)" == "integration" ]]
[[ "$(jq -er .browser /etc/nddev/image-build.json)" == "${GHA_BROWSER}" ]]
[[ "$(jq -er .docker_action_base_ref /etc/nddev/image-build.json)" == "${GHA_DOCKER_ACTION_BASE_REF}" ]]
[[ "$(jq -er .sccache_version /etc/nddev/image-build.json)" == "${GHA_SCCACHE_VERSION}" ]]
[[ "$(jq -er .sccache_binary_sha256 /etc/nddev/image-build.json)" == "${GHA_SCCACHE_BINARY_SHA256}" ]]
[[ "$(sccache --version)" == "sccache ${GHA_SCCACHE_VERSION#v}" ]]
echo "${GHA_SCCACHE_BINARY_SHA256}  /usr/local/bin/sccache" | sha256sum --check --strict --status

# The integration image bakes the same toolchain set as the standard image, so
# a Docker-capable job never pays a toolchain install either.
runner_tool_cache="$(jq -er .runner_tool_cache /etc/nddev/image-build.json)"
[[ "${runner_tool_cache}" == /home/runner/actions-runner/_work/_tool ]]
smoke_toolchains="$(printf '%s' "${GHA_TOOLCHAINS_B64}" | base64 --decode)"
mapfile -t smoke_toolchain_names < <(jq -r '.[].name' <<<"${smoke_toolchains}")
[[ "$(printf '%s\n' "${smoke_toolchain_names[@]}" | LC_ALL=C sort | paste -sd, -)" == bun,gh,go,node22,node24,node25,pnpm,rust,uv,yarn ]]
for smoke_toolchain in "${smoke_toolchain_names[@]}"; do
  entry="$(jq -ce --arg name "${smoke_toolchain}" '.[] | select(.name == $name)' <<<"${smoke_toolchains}")"
  expected_version="$(jq -er .version <<<"${entry}")"
  expected_sha256="$(jq -er .archive_sha256 <<<"${entry}")"
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].version' /etc/nddev/image-build.json)" == "${expected_version}" ]]
  [[ "$(jq -er --arg name "${smoke_toolchain}" '.toolchains[$name].archive_sha256' /etc/nddev/image-build.json)" == "${expected_sha256}" ]]
	case "${smoke_toolchain}" in
	  bun) [[ "$(bun --version)" == "${expected_version}" ]] ;;
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
      ;;
	yarn) [[ "$(yarn --version)" == "${expected_version}" ]] ;;
  esac
done

# Browser bytes are qualification input, not image content. Launch the pinned
# Chrome-for-Testing archive against the baked OS libraries as the unprivileged
# runner, then let cleanup delete the entire extracted tree and profile.
browser_launch=not-declared
browser_bytes_retained=false
if [[ "${GHA_BROWSER}" == "chromium" ]]; then
  : "${GHA_BROWSER_SMOKE_VERSION:?}"
  : "${GHA_BROWSER_SMOKE_ARCHIVE:?}"
  : "${GHA_BROWSER_SMOKE_SHA256:?}"
  : "${GHA_BROWSER_SMOKE_BINARY:?}"
  test -f "${GHA_BROWSER_SMOKE_ARCHIVE}"
  echo "${GHA_BROWSER_SMOKE_SHA256}  ${GHA_BROWSER_SMOKE_ARCHIVE}" | sha256sum --check --strict --status
  browser_root="$(mktemp -d /var/tmp/gha-browser-smoke.XXXXXXXX)"
  cleanup_paths+=("${browser_root}")
  unzip -q "${GHA_BROWSER_SMOKE_ARCHIVE}" -d "${browser_root}"
  browser_binary="${browser_root}/${GHA_BROWSER_SMOKE_BINARY}"
  test -x "${browser_binary}"
  chown -R runner:runner "${browser_root}"
  actual_browser_version="$(runuser -u runner -- "${browser_binary}" --version | xargs)"
  [[ "${actual_browser_version}" == "Google Chrome for Testing ${GHA_BROWSER_SMOKE_VERSION}" ]]
  browser_dom="$(runuser -u runner -- env HOME=/home/runner "${browser_binary}" \
    --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage \
    --no-first-run --no-default-browser-check --user-data-dir="${browser_root}/profile" \
    --dump-dom 'data:text/html,<title>nddev-browser-smoke</title><body>browser-ok</body>')"
  grep -Fq '<title>nddev-browser-smoke</title>' <<<"${browser_dom}"
  grep -Fq '<body>browser-ok</body>' <<<"${browser_dom}"
  browser_launch=ok
fi

python --version >/dev/null
python3 --version >/dev/null
python3 -m pip --version >/dev/null
pip --version >/dev/null
pip3 --version >/dev/null

for forbidden in \
  /run/incus/unix.socket \
	  /var/lib/incus/unix.socket \
	  /var/snap/lxd/common/lxd/unix.socket \
	  /dev/kvm \
	  /dev/vhost-net \
	  /dev/vhost-vsock; do
	test ! -e "${forbidden}"
done
if [[ "${GHA_INSTANCE_TYPE}" == "virtual-machine" ]]; then
	if grep -Eq '(^|[[:space:]])(vmx|svm)([[:space:]]|$)' /proc/cpuinfo; then
		echo "nested virtualization CPU feature is visible" >&2
		exit 1
	fi
	nested_cpu_flags=absent
	docker_socket_scope=vm-local
else
	# A system container shares the host CPU description and therefore sees
	# vmx/svm flags. The isolation proof is device/socket absence above: no KVM
	# or vhost device is delegated, so the flags cannot enable virtualization.
	nested_cpu_flags=host-visible-without-devices
	docker_socket_scope=container-local
fi

root_source="$(findmnt --noheadings --output SOURCE / | tr -d '[:space:]')"
expected_disk_bytes="$(( GHA_EXPECTED_ROOT_DISK_GIB * 1024 * 1024 * 1024 ))"
root_bytes="$(findmnt --bytes --noheadings --output SIZE / | tr -d '[:space:]')"
minimum_root_bytes="$(( (GHA_EXPECTED_ROOT_DISK_GIB - 4) * 1024 * 1024 * 1024 ))"
if [[ ! "${root_bytes}" =~ ^[0-9]+$ ]] || (( root_bytes < minimum_root_bytes )); then
  echo "root filesystem did not expand to the runtime profile" >&2
	exit 1
fi
if [[ "${GHA_INSTANCE_TYPE}" == "virtual-machine" ]]; then
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
	# Incus container root volumes expose their enforced filesystem quota, not
	# the host block device, inside the mount namespace.
	root_disk_bytes="${root_bytes}"
fi
if find /opt/cache/actions-runner -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; then
  echo "runner registration state found in smoke VM" >&2
  exit 1
fi
id runner >/dev/null
[[ "$(id --groups --name runner | tr ' ' '\n' | grep -vx runner | LC_ALL=C sort | paste -sd' ' -)" == "docker sudo" ]]
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

[[ "$(command -v docker)" == "/usr/bin/docker" ]]
test ! -L /usr/bin/docker
systemctl is-active --quiet docker.service
systemctl is-active --quiet containerd.service
systemctl is-enabled --quiet docker.service
systemctl is-enabled --quiet docker.socket
test -S /run/docker.sock
[[ "$(stat -c %G /run/docker.sock)" == "docker" ]]
[[ "$(stat -c %a /run/docker.sock)" == "660" ]]
docker_version="$(docker version --format '{{.Server.Version}}')"
docker_storage="$(docker info --format '{{.Driver}}')"
docker_cgroup="$(docker info --format '{{.CgroupDriver}}')"
[[ "${docker_storage}" == "${GHA_DOCKER_STORAGE_DRIVER}" ]]
[[ "${docker_cgroup}" == "systemd" ]]
docker buildx version >/dev/null
docker compose version >/dev/null

docker_root_source="$(findmnt --noheadings --output SOURCE --target /var/lib/docker | tr -d '[:space:]')"
[[ "${docker_root_source}" == "${root_source}" ]]
socket_mount="$(findmnt --json --target /run/docker.sock)"
socket_mount_target="$(jq -er '.filesystems | if length == 1 then .[0].target else error("ambiguous socket mount") end' <<<"${socket_mount}")"
socket_mount_source="$(jq -er '.filesystems[0].source' <<<"${socket_mount}")"
socket_mount_fstype="$(jq -er '.filesystems[0].fstype' <<<"${socket_mount}")"
[[ "${socket_mount_target}" == "/run" ]]
[[ "${socket_mount_source}" == "tmpfs" ]]
[[ "${socket_mount_fstype}" == "tmpfs" ]]

expected_base_id="$(jq -er .docker_action_base_id /etc/nddev/image-build.json)"
actual_base_id="$(docker image inspect --format '{{.Id}}' "${GHA_DOCKER_ACTION_BASE_REF}")"
[[ "${actual_base_id}" == "${expected_base_id}" ]]

useradd --system --create-home --groups docker --shell /bin/sh gha-docker-smoke
smoke_user_created=1
runuser -u gha-docker-smoke -- docker run --rm --network none "${GHA_DOCKER_ACTION_BASE_REF}" /bin/true

action_context="$(mktemp -d)"
cleanup_paths+=("${action_context}")
printf '#!/bin/sh\nset -eu\nprintf action-ok > /result/action\n' >"${action_context}/entrypoint.sh"
chmod 0755 "${action_context}/entrypoint.sh"
printf 'FROM %s\nCOPY entrypoint.sh /entrypoint.sh\nENTRYPOINT ["/bin/sh","/entrypoint.sh"]\n' "${GHA_DOCKER_ACTION_BASE_REF}" >"${action_context}/Dockerfile"
docker build --tag nddev/gha-action-smoke:runtime "${action_context}" >/dev/null
action_result="$(mktemp -d)"
cleanup_paths+=("${action_result}")
docker run --rm --network none --volume "${action_result}:/result" nddev/gha-action-smoke:runtime
[[ "$(cat "${action_result}/action")" == "action-ok" ]]

service_root="$(mktemp -d)"
cleanup_paths+=("${service_root}")
printf 'service-ok\n' >"${service_root}/index.html"
docker network create gha-image-network-smoke >/dev/null
docker run --detach --name gha-image-service-smoke --network gha-image-network-smoke \
  --volume "${service_root}:/www:ro" "${GHA_DOCKER_ACTION_BASE_REF}" \
  /bin/httpd -f -p 8080 -h /www >/dev/null
for _ in {1..20}; do
  if [[ "$(docker run --rm --network gha-image-network-smoke "${GHA_DOCKER_ACTION_BASE_REF}" /bin/wget -qO- http://gha-image-service-smoke:8080/)" == "service-ok" ]]; then
    service_ready=1
    break
  fi
  sleep 0.25
done
[[ "${service_ready:-0}" == "1" ]]
docker rm --force gha-image-service-smoke >/dev/null
docker network rm gha-image-network-smoke >/dev/null
docker image rm nddev/gha-action-smoke:runtime >/dev/null
docker builder prune --all --force >/dev/null

test -z "$(docker ps --all --quiet)"
test -z "$(docker volume ls --quiet)"
mapfile -t networks < <(docker network ls --format '{{.Name}}' | LC_ALL=C sort)
[[ "${networks[*]}" == "bridge host none" ]]

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
  --arg docker_engine_version "${docker_version}" \
  --arg docker_storage_driver "${docker_storage}" \
  --arg docker_cgroup_driver "${docker_cgroup}" \
  --arg docker_action_base_id "${actual_base_id}" \
  --arg browser "${GHA_BROWSER}" \
  --arg browser_smoke_version "${GHA_BROWSER_SMOKE_VERSION:-}" \
  --arg browser_launch "${browser_launch}" \
  --argjson browser_bytes_retained "${browser_bytes_retained}" \
  --arg docker_socket_filesystem "${socket_mount_fstype}:${socket_mount_target}" \
  --arg public_egress ok \
  --arg host_route blocked \
  --arg metadata_route blocked \
	  --arg nested_cpu_flags "${nested_cpu_flags}" \
	  --arg docker_socket_scope "${docker_socket_scope}" \
  --argjson root_disk_bytes "${root_disk_bytes}" \
  --argjson root_filesystem_bytes "${root_bytes}" \
	  '{runner_version:$runner_version,sccache_version:$sccache_version,toolchains:$toolchains,runner_tool_cache:$runner_tool_cache,machine_id:$machine_id,image_variant:"integration",browser:$browser,browser_smoke_version:$browser_smoke_version,browser_launch:$browser_launch,browser_bytes_retained:$browser_bytes_retained,docker_engine_version:$docker_engine_version,docker_storage_driver:$docker_storage_driver,docker_cgroup_driver:$docker_cgroup_driver,docker_action_base_id:$docker_action_base_id,docker_socket:$docker_socket_scope,docker_socket_filesystem:$docker_socket_filesystem,docker_nonroot_access:"ok",docker_action_build:"ok",docker_service_network:"ok",public_egress:$public_egress,host_route:$host_route,metadata_route:$metadata_route,forbidden_devices:"absent",nested_cpu_flags:$nested_cpu_flags,root_disk_bytes:$root_disk_bytes,root_filesystem_bytes:$root_filesystem_bytes,registration_state:"absent",startup_mode:"cold-only",ssh_server_package:"absent",ssh_units:"masked",ssh_listener:"absent"}'
