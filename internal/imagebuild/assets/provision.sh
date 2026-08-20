#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "$(id -u)" != "0" ]]; then
  echo "provision must run as root" >&2
  exit 1
fi

: "${GHA_RUNNER_VERSION:?}"
: "${GHA_RUNNER_SHA256:?}"
: "${GHA_RUNNER_ARCHIVE:?}"
: "${GHA_PACKAGES:?}"
: "${GHA_MANIFEST_FINGERPRINT:?}"
: "${GHA_RECIPE_FINGERPRINT:?}"
: "${GHA_SOURCE_RELEASE_ID:?}"
: "${GHA_SOURCE_ARTIFACT_SHA256:?}"
: "${GHA_WARM_AGENT_B64:?}"
: "${GHA_SCCACHE_VERSION:?}"
: "${GHA_SCCACHE_ARCHIVE:?}"
: "${GHA_SCCACHE_ARCHIVE_SHA256:?}"
: "${GHA_SCCACHE_BINARY_PATH:?}"
: "${GHA_SCCACHE_BINARY_SHA256:?}"
: "${GHA_TOOLCHAINS_B64:?}"

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get -y -o Dpkg::Options::=--force-confold dist-upgrade
# Package names are validated before this deliberately unquoted expansion.
# shellcheck disable=SC2086
apt-get install -y --no-install-recommends ${GHA_PACKAGES}

systemctl disable --now apt-daily.timer apt-daily-upgrade.timer unattended-upgrades.service 2>/dev/null || true
git lfs install --system
groupadd --force docker
groupadd --force lxd

if ! id runner >/dev/null 2>&1; then
  useradd --create-home --home-dir /home/runner --shell /bin/bash --groups sudo runner
fi
install -d -o root -g root -m 0750 /etc/sudoers.d
printf 'runner ALL=(ALL) NOPASSWD:ALL\n' >/etc/sudoers.d/90-nddev-runner
chmod 0440 /etc/sudoers.d/90-nddev-runner
visudo --check --file=/etc/sudoers.d/90-nddev-runner

install -d -o root -g root -m 0755 /usr/local/libexec
install -d -o runner -g runner -m 0700 /home/runner/.gha-cache
printf '%s' "${GHA_WARM_AGENT_B64}" | base64 --decode >/usr/local/libexec/gha-warm-agent
chown root:root /usr/local/libexec/gha-warm-agent
chmod 0755 /usr/local/libexec/gha-warm-agent
install -d -o root -g root -m 0700 /var/lib/gha-warm/claims /run/gha-warm/assignments
cat >/etc/tmpfiles.d/gha-warm.conf <<'EOF'
d /run/gha-warm 0700 root root -
d /run/gha-warm/assignments 0700 root root -
EOF
cat >/etc/systemd/system/gha-warm-agent.service <<'UNIT'
[Unit]
Description=Consume exactly one NDDev warm-runner assignment
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/gha-warm-agent
PrivateTmp=false
ProtectSystem=strict
ReadWritePaths=/run/gha-warm /run/lock /var/lib/gha-warm /home/runner /tmp /etc/systemd/system /usr/local/share/ca-certificates /etc/ssl/certs
NoNewPrivileges=false
UNIT
cat >/etc/systemd/system/gha-warm-agent.path <<'UNIT'
[Unit]
Description=Watch for an NDDev warm-runner assignment

[Path]
PathExistsGlob=/run/gha-warm/assignments/*.sh
Unit=gha-warm-agent.service

[Install]
WantedBy=multi-user.target
UNIT
cat >/etc/systemd/system/gha-warm-ready.service <<'UNIT'
[Unit]
Description=Attest that the NDDev worker is warm and unregistered
After=gha-warm-agent.path network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/bash -c 'systemctl is-active --quiet gha-warm-agent.path; ! find /opt/cache/actions-runner /home/runner/actions-runner -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; runuser --user runner -- /home/runner/actions-runner/bin/Runner.Listener warmup >/dev/null; ! find /opt/cache/actions-runner /home/runner/actions-runner -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; printf "ready-unregistered-v1\n" >/run/gha-warm/ready'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
systemctl enable gha-warm-agent.path
systemctl enable gha-warm-ready.service

echo "${GHA_RUNNER_SHA256}  ${GHA_RUNNER_ARCHIVE}" | sha256sum --check --strict
runner_version="${GHA_RUNNER_VERSION#v}"
cache_root=/opt/cache/actions-runner
version_root="${cache_root}/${runner_version}"
latest_root="${cache_root}/latest"
install -d -m 0755 "${version_root}" "${latest_root}"
tar --extract --gzip --file "${GHA_RUNNER_ARCHIVE}" --directory "${version_root}"
chown -R root:root "${cache_root}"
chmod -R go-w "${cache_root}"

"${version_root}/bin/installdependencies.sh"
actual_version="$("${version_root}/bin/Runner.Listener" --version | tail -n 1 | tr -d '\r')"
if [[ "${actual_version}" != "${runner_version}" ]]; then
  echo "runner reports ${actual_version}, expected ${runner_version}" >&2
  exit 1
fi

echo "${GHA_SCCACHE_ARCHIVE_SHA256}  ${GHA_SCCACHE_ARCHIVE}" | sha256sum --check --strict
[[ "${GHA_SCCACHE_BINARY_PATH}" == "sccache-${GHA_SCCACHE_VERSION}-x86_64-unknown-linux-musl/sccache" ]]
mapfile -t sccache_entries < <(tar --list --gzip --file "${GHA_SCCACHE_ARCHIVE}")
if [[ "${#sccache_entries[@]}" != 4 ]]; then
  echo "sccache archive has an unexpected entry count" >&2
  exit 1
fi
for expected in \
  "sccache-${GHA_SCCACHE_VERSION}-x86_64-unknown-linux-musl/" \
  "${GHA_SCCACHE_BINARY_PATH}" \
  "sccache-${GHA_SCCACHE_VERSION}-x86_64-unknown-linux-musl/LICENSE" \
  "sccache-${GHA_SCCACHE_VERSION}-x86_64-unknown-linux-musl/README.md"; do
  if ! printf '%s\n' "${sccache_entries[@]}" | grep -Fxq -- "${expected}"; then
    printf 'sccache archive is missing exact entry %s\n' "${expected}" >&2
    exit 1
  fi
done
sccache_scratch="$(mktemp -d /var/tmp/gha-sccache.XXXXXXXXXX)"
tar --extract --gzip --file "${GHA_SCCACHE_ARCHIVE}" \
  --directory "${sccache_scratch}" --no-same-owner --no-same-permissions \
  -- "${GHA_SCCACHE_BINARY_PATH}"
test ! -L "${sccache_scratch}/${GHA_SCCACHE_BINARY_PATH}"
test -f "${sccache_scratch}/${GHA_SCCACHE_BINARY_PATH}"
echo "${GHA_SCCACHE_BINARY_SHA256}  ${sccache_scratch}/${GHA_SCCACHE_BINARY_PATH}" | sha256sum --check --strict
install -o root -g root -m 0755 "${sccache_scratch}/${GHA_SCCACHE_BINARY_PATH}" /usr/local/bin/sccache
find "${sccache_scratch}" -mindepth 1 -delete
rmdir "${sccache_scratch}"
[[ "$(sccache --version)" == "sccache ${GHA_SCCACHE_VERSION#v}" ]]

# Remove the server after every package/dependency installer has completed.
# Purging removes the unit files while a loaded socket can remain active, and
# one grouped `disable --now` aborts before stopping it when another alias is
# already absent. Stop and disable each loaded alias independently, then mask
# every name so neither package hooks nor a later boot can reactivate SSH.
if dpkg -s openssh-server >/dev/null 2>&1; then
  apt-get purge -y openssh-server
fi
for unit in ssh.service ssh.socket sshd.service sshd.socket; do
  systemctl stop "${unit}" 2>/dev/null || true
  systemctl disable "${unit}" 2>/dev/null || true
  systemctl mask "${unit}"
done
systemctl daemon-reload

# The pinned GARM provider expects latest to be a real directory. Hard links
# avoid a second copy while ensuring cp -a does not preserve a broken symlink.
cp -al "${version_root}/." "${latest_root}/"
install -d -o runner -g runner -m 0755 /home/runner/actions-runner
cp -a --reflink=auto "${latest_root}/." /home/runner/actions-runner/
chown -R runner:runner /home/runner/actions-runner
if find /home/runner/actions-runner -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; then
  echo "runner registration state appeared while preparing the warm filesystem" >&2
  exit 1
fi

# Bake every pinned language toolchain so no job repeats its download and
# install. Each archive is verified against its manifest digest before use, and
# each installed executable must report the exact pinned version. Go is seeded
# into the official runner's default tool cache so actions/setup-go resolves it
# without a network fetch; the others land on PATH where the representative
# installers short-circuit on an exact version match.
runner_tool_cache=/home/runner/actions-runner/_work/_tool
install -d -o runner -g runner -m 0755 \
  /home/runner/actions-runner/_work "${runner_tool_cache}"
toolchain_manifest="$(printf '%s' "${GHA_TOOLCHAINS_B64}" | base64 --decode)"
jq -e 'type == "array"' <<<"${toolchain_manifest}" >/dev/null
mapfile -t toolchain_names < <(jq -r '.[].name' <<<"${toolchain_manifest}")
toolchain_set="$(printf '%s\n' "${toolchain_names[@]}" | LC_ALL=C sort | paste -sd, -)"
if [[ "${toolchain_set}" != bun,gh,go,rust,uv && "${toolchain_set}" != bun,gh,go,node22,node24,node25,rust,uv ]]; then
  echo "toolchain manifest does not pin the exact baked set" >&2
  exit 1
fi
for toolchain_name in "${toolchain_names[@]}"; do
  entry="$(jq -ce --arg name "${toolchain_name}" '.[] | select(.name == $name)' <<<"${toolchain_manifest}")"
  toolchain_version="$(jq -er .version <<<"${entry}")"
  toolchain_archive="$(jq -er .archive <<<"${entry}")"
  toolchain_sha256="$(jq -er .archive_sha256 <<<"${entry}")"
  [[ "${toolchain_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
  [[ "${toolchain_archive}" == /var/tmp/* && "${toolchain_archive}" != *..* ]]
  [[ "${toolchain_sha256}" =~ ^[0-9a-f]{64}$ ]]
  echo "${toolchain_sha256}  ${toolchain_archive}" | sha256sum --check --strict
  toolchain_scratch="$(mktemp -d /var/tmp/gha-toolchain.XXXXXXXXXX)"
	case "${toolchain_name}" in
	  bun)
      unzip -q "${toolchain_archive}" -d "${toolchain_scratch}"
      install -o root -g root -m 0755 \
        "${toolchain_scratch}/bun-linux-x64/bun" /usr/local/bin/bun
	    [[ "$(bun --version)" == "${toolchain_version}" ]]
	    ;;
	  gh)
	    tar --extract --gzip --file "${toolchain_archive}" \
	      --directory "${toolchain_scratch}" --no-same-owner --no-same-permissions
	    install -o root -g root -m 0755 \
	      "${toolchain_scratch}/gh_${toolchain_version}_linux_amd64/bin/gh" /usr/local/bin/gh
	    [[ "$(gh --version | head -n 1)" == "gh version ${toolchain_version} "* ]]
	    gh attestation --help >/dev/null
	    ;;
    go)
      tar --extract --gzip --file "${toolchain_archive}" \
        --directory "${toolchain_scratch}" --no-same-owner --no-same-permissions
      install -d -o runner -g runner -m 0755 \
        "${runner_tool_cache}/go" "${runner_tool_cache}/go/${toolchain_version}"
      mv -- "${toolchain_scratch}/go" "${runner_tool_cache}/go/${toolchain_version}/x64"
      chown -R runner:runner "${runner_tool_cache}/go/${toolchain_version}"
      install -o runner -g runner -m 0644 /dev/null \
        "${runner_tool_cache}/go/${toolchain_version}/x64.complete"
      [[ "$("${runner_tool_cache}/go/${toolchain_version}/x64/bin/go" version)" == "go version go${toolchain_version} linux/amd64" ]]
      ;;
    node22|node24|node25)
      tar --extract --xz --file "${toolchain_archive}" \
        --directory "${toolchain_scratch}" --no-same-owner --no-same-permissions
      node_root="${runner_tool_cache}/node/${toolchain_version}"
      install -d -o runner -g runner -m 0755 "${runner_tool_cache}/node" "${node_root}"
      mv -- "${toolchain_scratch}/node-v${toolchain_version}-linux-x64" "${node_root}/x64"
      chown -R runner:runner "${node_root}"
      install -o runner -g runner -m 0644 /dev/null "${node_root}/x64.complete"
      [[ "$("${node_root}/x64/bin/node" --version)" == "v${toolchain_version}" ]]
      if [[ "${toolchain_name}" == node24 ]]; then
        for executable in node npm npx corepack; do
          test -e "${node_root}/x64/bin/${executable}"
          ln -sfn "${node_root}/x64/bin/${executable}" "/usr/local/bin/${executable}"
        done
        [[ "$(node --version)" == "v${toolchain_version}" ]]
        npm --version >/dev/null
        corepack --version >/dev/null
      fi
      ;;
    rust)
      tar --extract --xz --file "${toolchain_archive}" \
        --directory "${toolchain_scratch}" --no-same-owner --no-same-permissions
      "${toolchain_scratch}/rust-${toolchain_version}-x86_64-unknown-linux-gnu/install.sh" \
        --prefix=/usr/local \
        --components=rustc,cargo,rust-std-x86_64-unknown-linux-gnu >/dev/null
      [[ "$(rustc --version)" == "rustc ${toolchain_version} "* ]]
      [[ "$(cargo --version)" == "cargo ${toolchain_version} "* ]]
      ;;
    uv)
      tar --extract --gzip --file "${toolchain_archive}" \
        --directory "${toolchain_scratch}" --no-same-owner --no-same-permissions
      install -o root -g root -m 0755 \
        "${toolchain_scratch}/uv-x86_64-unknown-linux-gnu/uv" /usr/local/bin/uv
      install -o root -g root -m 0755 \
        "${toolchain_scratch}/uv-x86_64-unknown-linux-gnu/uvx" /usr/local/bin/uvx
      [[ "$(uv --version)" == "uv ${toolchain_version}"* ]]
      ;;
  esac
  find "${toolchain_scratch}" -mindepth 1 -delete
  rmdir "${toolchain_scratch}"
  rm -f -- "${toolchain_archive}"
done
if find "${runner_tool_cache}" -maxdepth 3 ! -user runner -print -quit | grep -q .; then
  echo "runner tool cache contains an entry the runner does not own" >&2
  exit 1
fi

install -d -m 0755 /etc/nddev
dpkg-query --show --showformat='${Package}\t${Version}\n' | LC_ALL=C sort > /etc/nddev/packages.tsv
package_sha="$(sha256sum /etc/nddev/packages.tsv | cut -d' ' -f1)"
jq -n \
  --arg manifest "${GHA_MANIFEST_FINGERPRINT}" \
  --arg recipe "${GHA_RECIPE_FINGERPRINT}" \
  --arg runner_version "${GHA_RUNNER_VERSION}" \
  --arg runner_sha256 "${GHA_RUNNER_SHA256}" \
  --arg source_release "${GHA_SOURCE_RELEASE_ID}" \
  --arg source_artifact_sha256 "${GHA_SOURCE_ARTIFACT_SHA256}" \
  --arg package_manifest_sha256 "${package_sha}" \
  --arg sccache_version "${GHA_SCCACHE_VERSION}" \
  --arg sccache_archive_sha256 "${GHA_SCCACHE_ARCHIVE_SHA256}" \
  --arg sccache_binary_sha256 "${GHA_SCCACHE_BINARY_SHA256}" \
  --argjson toolchains "$(jq -c 'map({key:.name,value:{version:.version,archive_sha256:.archive_sha256}}) | from_entries' <<<"${toolchain_manifest}")" \
  --arg runner_tool_cache "${runner_tool_cache}" \
  '{schema_version:3, manifest_fingerprint:$manifest, recipe_fingerprint:$recipe, runner_version:$runner_version, runner_sha256:$runner_sha256, source_release:$source_release, source_artifact_sha256:$source_artifact_sha256, package_manifest_sha256:$package_manifest_sha256, sccache_version:$sccache_version, sccache_archive_sha256:$sccache_archive_sha256, sccache_binary_sha256:$sccache_binary_sha256, runner_tool_cache:$runner_tool_cache, toolchains:$toolchains}' \
  > /etc/nddev/image-build.json
chmod 0644 /etc/nddev/image-build.json /etc/nddev/packages.tsv

rm -f "${GHA_RUNNER_ARCHIVE}" "${GHA_SCCACHE_ARCHIVE}"
apt-get clean
find /var/lib/apt/lists -type f -delete

if find "${cache_root}" -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; then
  echo "runner registration state found in cache" >&2
  exit 1
fi
test -x /usr/local/libexec/gha-warm-agent
test "$(stat --format='%U:%G:%a:%F' /home/runner/.gha-cache)" = 'runner:runner:700:directory'
test -x /usr/local/bin/sccache
test -x /usr/local/bin/bun
test -x /usr/local/bin/gh
test -x /usr/local/bin/uv
test -x /usr/local/bin/uvx
test -x /usr/local/bin/rustc
test -x /usr/local/bin/cargo
test "$(systemctl is-enabled gha-warm-agent.path)" = enabled
test "$(systemctl is-enabled gha-warm-ready.service)" = enabled
