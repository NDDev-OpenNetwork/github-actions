#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "$(id -u)" != "0" ]]; then
  echo "container provision must run as root" >&2
  exit 1
fi

install -d -m 0755 /etc/systemd/network
cat >/etc/systemd/network/10-eth0.network <<'EOF'
[Match]
Name=eth0

[Network]
DHCP=ipv4
LinkLocalAddressing=ipv6
IPv6AcceptRA=no
EOF
cat >/etc/cloud/cloud.cfg.d/99-nddev-incus.cfg <<'EOF'
datasource_list: [ LXD, None ]
warnings:
  dsid_missing_source: off
EOF
cat >/usr/local/libexec/gha-incus-cloud-init <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
test -S /dev/incus/sock
ln -sfn /dev/incus /dev/lxd
for attempt in $(seq 1 30); do
  if curl --fail --silent --unix-socket /dev/lxd/sock http://localhost/1.0 >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --unix-socket /dev/lxd/sock http://localhost/1.0 >/dev/null
user_data="$(curl --silent --unix-socket /dev/lxd/sock http://localhost/1.0/config/user.user-data || true)"
cloud-init clean --logs
cloud-init init --local
cloud-init init
cloud-init modules --mode=config
cloud-init modules --mode=final
if ! compgen -G '/etc/systemd/system/actions.runner.*.service' >/dev/null; then
  if [[ "${user_data}" == "not found" ]]; then
    exit 0
  fi
  echo "cloud-init completed without installing the one-job runner service" >&2
  exit 1
fi
EOF
chmod 0755 /usr/local/libexec/gha-incus-cloud-init
cat >/etc/systemd/system/gha-incus-cloud-init.service <<'EOF'
[Unit]
Description=Apply one-job Incus metadata through cloud-init
After=systemd-networkd.service systemd-resolved.service
Wants=systemd-networkd.service systemd-resolved.service
Before=gha-warm-ready.service
StartLimitIntervalSec=0

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/gha-incus-cloud-init
RemainAfterExit=yes
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
systemctl enable gha-incus-cloud-init.service
