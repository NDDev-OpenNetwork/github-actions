#!/usr/bin/env bash
set -Eeuo pipefail

report_error() {
  local rc="$1"
  local line="$2"
  local command="$3"
  printf >&2 'sanitize failed at line %s: %s (exit %s)\n' "$line" "$command" "$rc"
  exit "$rc"
}

trap 'report_error "$?" "${BASH_LINENO[0]}" "$BASH_COMMAND"' ERR

if [[ "$(id -u)" != "0" ]]; then
  echo "sanitize must run as root" >&2
  exit 1
fi

if find /etc/systemd/system -maxdepth 1 -type f -name 'actions.runner.*' -print -quit | grep -q .; then
  echo "runner service found before sealing" >&2
  exit 1
fi

find /opt/cache/actions-runner -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -delete
find /root /home -xdev -type f \( -name authorized_keys -o -name known_hosts -o -name .bash_history -o -name .zsh_history \) -delete 2>/dev/null || true
rm -f /etc/ssh/ssh_host_*

if id ubuntu >/dev/null 2>&1; then
  # shadow's userdel returns non-zero when the conventional mail spool is
  # absent, even though Canonical cloud images do not create it. Materialize a
  # correctly owned empty spool so removal remains strict instead of masking a
  # potentially partial account deletion with `|| true`.
  install -d -m 0755 /var/mail
  if [[ ! -e /var/mail/ubuntu ]]; then
    install -o ubuntu -g mail -m 0600 /dev/null /var/mail/ubuntu
  fi
  userdel --remove ubuntu
fi
if id ubuntu >/dev/null 2>&1 || getent passwd ubuntu >/dev/null 2>&1; then
  echo "default ubuntu account remained after sanitation" >&2
  exit 1
fi

cloud-init clean --logs --machine-id
rm -rf /var/lib/cloud/instances /var/lib/cloud/instance /var/lib/cloud/data
rm -f /var/lib/dbus/machine-id
truncate -s 0 /etc/machine-id

journalctl --rotate 2>/dev/null || true
journalctl --vacuum-time=1s 2>/dev/null || true
find /var/log -xdev -type f -exec truncate -s 0 {} +
find /tmp -mindepth 1 -xdev -delete
find /var/tmp -mindepth 1 -xdev -delete

test -s /etc/nddev/image-build.json
test -x /opt/cache/actions-runner/latest/bin/Runner.Listener
test ! -s /etc/machine-id
test ! -e /var/lib/dbus/machine-id
test ! -e /var/run/docker.sock
test ! -e /run/incus/unix.socket
test ! -e /var/lib/incus/unix.socket
test ! -e /var/snap/lxd/common/lxd/unix.socket
test ! -e /dev/kvm
if dpkg -s openssh-server >/dev/null 2>&1; then
  echo "OpenSSH server package remained after provisioning" >&2
  exit 1
fi
for unit in ssh.service ssh.socket sshd.service sshd.socket; do
  if [[ "$(systemctl is-enabled "${unit}" 2>/dev/null || true)" != "masked" ]]; then
    echo "SSH unit ${unit} is not masked" >&2
    exit 1
  fi
  if systemctl is-active --quiet "${unit}"; then
    echo "SSH unit ${unit} remained active" >&2
    exit 1
  fi
done
if ss -H -lnt 'sport = :22' | grep -q .; then
  echo "SSH listener remained after provisioning" >&2
  exit 1
fi
if compgen -G '/etc/ssh/ssh_host_*' >/dev/null; then
  echo "SSH host key remained after sanitation" >&2
  exit 1
fi

sync
fstrim --verbose /
sync
