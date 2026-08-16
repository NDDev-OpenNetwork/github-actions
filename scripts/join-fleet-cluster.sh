#!/usr/bin/env bash
# Join this host to the fleet's Incus cluster.
#
# usage: join-fleet-cluster.sh <join-token> <this-host-private-ip:8443>
#
# The second argument is *this* member's own bind address, not the bootstrap's.
# The bootstrap is inside the token. Passing the bootstrap's address here makes
# the daemon try to bind an address it does not own and the join fails with
# "cannot assign requested address".
#
# Joining wipes this host's Incus state -- that is Incus' own requirement, not a
# choice made here. Wiping /var/lib/incus is not enough: the LVM volume group,
# its loop device, the gha0 bridge and its dnsmasq all live outside it, and a
# leftover of any one of them fails the join later and less clearly ("A volume
# group already exists", "Network interface gha0 already exists", "failed to
# create listening socket for 192.0.2.1").
#
# Every member-specific storage key is passed explicitly. Omitting them does not
# fail -- it silently joins with Incus' defaults, which is a 30 GiB pool named
# IncusThinPool where the fleet expects 200 GiB of gha-thin.
set -Eeuo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <join-token> <this-host-ip:8443>" >&2
  exit 2
fi
readonly token="$1"
readonly bind_address="$2"

sudo systemctl stop incus incus.socket 2>/dev/null || true
sudo pkill -f "dnsmasq.*gha0" 2>/dev/null || true
sudo ip link delete gha0 2>/dev/null || true
sudo rm -rf /var/lib/incus

for group in $(sudo vgs --noheadings -o vg_name 2>/dev/null | tr -d ' '); do
  sudo vgremove -f "${group}" >/dev/null 2>&1 || true
done
for volume in $(sudo pvs --noheadings -o pv_name 2>/dev/null | tr -d ' '); do
  sudo pvremove -f "${volume}" >/dev/null 2>&1 || true
done
for device in $(sudo losetup -a 2>/dev/null | cut -d: -f1); do
  sudo losetup -d "${device}" 2>/dev/null || true
done

sudo systemctl start incus.socket incus
sleep 6

preseed=$(mktemp)
trap 'rm -f "${preseed}"' EXIT
cat > "${preseed}" <<YAML
cluster:
  enabled: true
  server_address: ${bind_address}
  cluster_token: ${token}
  member_config:
    - entity: storage-pool
      name: gha-lvm
      key: source
      value: "/var/lib/incus/disks/gha-lvm.img"
    - entity: storage-pool
      name: gha-lvm
      key: size
      value: "200GiB"
    - entity: storage-pool
      name: gha-lvm
      key: lvm.vg_name
      value: "gha-lvm"
    - entity: storage-pool
      name: gha-lvm
      key: lvm.thinpool_name
      value: "gha-thin"
YAML

# The redirect is the caller's, not sudo's, so the preseed is piped in.
cat "${preseed}" | sudo incus admin init --preseed
echo "joined the fleet cluster as ${bind_address}"
