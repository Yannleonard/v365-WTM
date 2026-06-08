#!/usr/bin/env bash
# Seed two demo KVM VMs (web-server-01, db-server-01) WITH real qcow2 disks + a VNC
# console, into the libvirt 'default' storage pool, so the UniHV UI shows working
# VMs you can snapshot and open a console on. Run on a host with libvirt + the
# 'default' pool (e.g. inside the WSL2 Ubuntu that hosts KVM):
#
#   bash seed-demo-vms.sh
#
# These are EVALUATION VMs (no OS installed — they boot to firmware). Register the
# libvirt connection in UniHV (Hypervisors page) to manage them. NOT a mock: they
# are real libvirt domains.
set -euo pipefail
URI="${LIBVIRT_URI:-qemu:///system}"
DIR="$(cd "$(dirname "$0")" && pwd)"

virsh -c "$URI" net-info default >/dev/null 2>&1 || virsh -c "$URI" net-define /dev/stdin <<'EOF'
<network><name>default</name><forward mode="nat"/>
  <ip address="192.168.122.1" netmask="255.255.255.0">
    <dhcp><range start="192.168.122.2" end="192.168.122.254"/></dhcp></ip>
</network>
EOF
virsh -c "$URI" net-start default 2>/dev/null || true
virsh -c "$URI" net-autostart default 2>/dev/null || true

for spec in "web-server-01 5" "db-server-01 8"; do
  name="${spec% *}"; gb="${spec#* }"
  virsh -c "$URI" destroy "$name" 2>/dev/null || true
  virsh -c "$URI" undefine "$name" 2>/dev/null || true
  virsh -c "$URI" vol-list default 2>/dev/null | grep -q "$name.qcow2" || \
    virsh -c "$URI" vol-create-as default "$name.qcow2" "${gb}G" --format qcow2
  virsh -c "$URI" define "$DIR/$name.xml"
done
virsh -c "$URI" start web-server-01
echo "Seeded demo VMs (web-server-01 running, db-server-01 defined) with real disks + VNC."
virsh -c "$URI" list --all | grep server-01
