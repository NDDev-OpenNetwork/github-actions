#!/usr/bin/env bash
set -Eeuo pipefail

report_error() {
  local rc="$1"
  local line="$2"
  local command="$3"
  printf >&2 'Docker provision failed at line %s: %s (exit %s)\n' "${line}" "${command}" "${rc}"
  exit "${rc}"
}

trap 'report_error "$?" "${LINENO:-0}" "$BASH_COMMAND"' ERR

if [[ "$(id -u)" != "0" ]]; then
  echo "Docker provisioning must run as root" >&2
  exit 1
fi

: "${GHA_DOCKER_ACTION_BASE_REF:?}"
: "${GHA_DOCKER_STORAGE_DRIVER:?}"
[[ "${GHA_DOCKER_STORAGE_DRIVER}" == "overlay2" || "${GHA_DOCKER_STORAGE_DRIVER}" == "overlayfs" ]]

for package in busybox-static docker-buildx docker-compose-v2 docker.io pigz; do
  dpkg-query --show --showformat='${Status}' "${package}" | grep -qx 'install ok installed'
done
[[ "$(command -v docker)" == "/usr/bin/docker" ]]
test ! -L /usr/bin/docker
getent group docker >/dev/null
usermod --groups sudo,docker runner
actual_runner_groups="$(id --groups --name runner | tr ' ' '\n' | grep -vx runner | LC_ALL=C sort | paste -sd' ' -)"
[[ "${actual_runner_groups}" == "docker sudo" ]]
if id --groups --name runner | tr ' ' '\n' | grep -qx lxd; then
  echo "runner retained forbidden lxd group membership" >&2
  exit 1
fi

# The daemon.json below names the member's registry mirror. dockerd with the
# containerd image store loads its trust once, from the system store, when
# it starts, so the CA that signs the mirror has to be in that store before
# the first start -- delivered later by a claim, into certs.d, or into the
# store without a restart, it never reached the daemon (measured on the .110
# and .111 workers: every pull logged an unknown authority and fell through
# to docker.io). The build host's copy is the estate's trust anchor; the
# manifest pins its digest and subject, and the file is proven here again.
: "${GHA_REGISTRY_MIRROR_CA:?}"
: "${GHA_REGISTRY_MIRROR_CA_SHA256:?}"
: "${GHA_REGISTRY_MIRROR_CA_SUBJECT:?}"
[[ "$(sha256sum "${GHA_REGISTRY_MIRROR_CA}" | cut -d' ' -f1)" == "${GHA_REGISTRY_MIRROR_CA_SHA256}" ]]
mirror_ca_subject="$(openssl x509 -in "${GHA_REGISTRY_MIRROR_CA}" -noout -subject | sed -e 's/^subject=//' -e 's/ = /=/g' -e 's/, /,/g')"
[[ "${mirror_ca_subject}" == "${GHA_REGISTRY_MIRROR_CA_SUBJECT}" ]]
install -o root -g root -m 0644 "${GHA_REGISTRY_MIRROR_CA}" /usr/local/share/ca-certificates/nddev-gha-cache-ca.crt
rm -f "${GHA_REGISTRY_MIRROR_CA}"
update-ca-certificates >/dev/null
openssl verify -CAfile /etc/ssl/certs/ca-certificates.crt /usr/local/share/ca-certificates/nddev-gha-cache-ca.crt >/dev/null

install -d -m 0755 /etc/docker
daemon_config="$(mktemp)"
jq -n --arg storage_driver "${GHA_DOCKER_STORAGE_DRIVER}" '({
  "default-address-pools":[{"base":"172.30.0.0/16","size":24}],
  "exec-opts":["native.cgroupdriver=systemd"],
  "features":{"buildkit":true},
  "registry-mirrors":["https://192.0.2.1:5001"],
  "live-restore":false,
  "log-driver":"local",
  "log-opts":{"max-file":"3","max-size":"10m"},
  "shutdown-timeout":15,
  "userland-proxy":false
} | if $storage_driver == "overlay2" then . + {"storage-driver":"overlay2"} else . end)' >"${daemon_config}"
install -o root -g root -m 0644 "${daemon_config}" /etc/docker/daemon.json
rm -f "${daemon_config}"

systemctl enable containerd.service docker.service docker.socket
systemctl restart containerd.service docker.service
systemctl is-active --quiet containerd.service
systemctl is-active --quiet docker.service

docker_version="$(docker version --format '{{.Server.Version}}')"
docker_storage="$(docker info --format '{{.Driver}}')"
docker_cgroup="$(docker info --format '{{.CgroupDriver}}')"
[[ -n "${docker_version}" ]]
[[ "${docker_storage}" == "${GHA_DOCKER_STORAGE_DRIVER}" ]]
[[ "${docker_cgroup}" == "systemd" ]]
docker buildx version >/dev/null
docker compose version >/dev/null

rootfs="$(mktemp -d)"
build_context="$(mktemp -d)"
cleanup() {
  rm -rf -- "${rootfs}" "${build_context}"
}
trap cleanup EXIT

install -d -m 0755 "${rootfs}/bin"
install -d -m 1777 "${rootfs}/tmp"
install -o root -g root -m 0755 /usr/bin/busybox "${rootfs}/bin/busybox"
for applet in cat echo httpd sh sleep true wget; do
  ln -s busybox "${rootfs}/bin/${applet}"
done
tar --create --file "${build_context}/rootfs.tar" --numeric-owner --owner=0 --group=0 \
  --sort=name --mtime='UTC 1970-01-01' --directory "${rootfs}" .
docker import --change 'ENV PATH=/bin' "${build_context}/rootfs.tar" "${GHA_DOCKER_ACTION_BASE_REF}" >/dev/null
base_id="$(docker image inspect --format '{{.Id}}' "${GHA_DOCKER_ACTION_BASE_REF}")"
[[ "${base_id}" =~ ^sha256:[0-9a-f]{64}$ ]]
docker run --rm --network none "${GHA_DOCKER_ACTION_BASE_REF}" /bin/true

printf 'FROM %s\nRUN /bin/echo buildkit-ok > /buildkit-ok\n' "${GHA_DOCKER_ACTION_BASE_REF}" >"${build_context}/Dockerfile"
docker build --tag nddev/gha-image-build-smoke:sealed "${build_context}" >/dev/null
[[ "$(docker run --rm --network none nddev/gha-image-build-smoke:sealed /bin/cat /buildkit-ok)" == "buildkit-ok" ]]
docker image rm nddev/gha-image-build-smoke:sealed >/dev/null
docker builder prune --all --force >/dev/null

image_build_tmp="$(mktemp /etc/nddev/.image-build.XXXXXX)"
jq \
  --arg variant integration \
  --arg docker_engine_version "${docker_version}" \
  --arg docker_storage_driver "${docker_storage}" \
  --arg docker_cgroup_driver "${docker_cgroup}" \
  --arg docker_action_base_ref "${GHA_DOCKER_ACTION_BASE_REF}" \
  --arg docker_action_base_id "${base_id}" \
  --arg browser "${GHA_BROWSER:-}" \
  '. + {
    image_variant:$variant,
    docker_engine_version:$docker_engine_version,
    docker_storage_driver:$docker_storage_driver,
    docker_cgroup_driver:$docker_cgroup_driver,
    docker_action_base_ref:$docker_action_base_ref,
    docker_action_base_id:$docker_action_base_id,
    browser:$browser
  }' /etc/nddev/image-build.json >"${image_build_tmp}"
chmod 0644 "${image_build_tmp}"
mv -f "${image_build_tmp}" /etc/nddev/image-build.json

test -z "$(docker ps --all --quiet)"
test -z "$(docker volume ls --quiet)"
sync
