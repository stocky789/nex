#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Read installer media and discover hardware; probe link without configuring IP or disks.
set -eu
export DEBIAN_FRONTEND=noninteractive
. /usr/share/debconf/confmodule
umask 077
mkdir -p /run/nex-installer
exec >>/var/log/nex-installer-prepare.log 2>&1

if [ ! -e /cdrom/.disk/info ]; then
    udpkg --configure cdrom-detect
fi
test -e /cdrom/.disk/info
test -f /cdrom/nex/root-account.sh
anna cdrom-retriever default
# Native module packages expose supported physical and virtual NICs and disks.
anna-install nic-modules nic-shared-modules
# SATA/SCSI/virtio disk drivers are already in the base installer initrd;
# hw-detect also requests any extra modules supported by detected hardware.
CHECK_MISSING_FIRMWARE=0 hw-detect
/bin/sh /cdrom/nex/root-account.sh
udevadm settle
# Link carrier can only be read reliably with the adapter enabled. Keep this
# installer-only probe address-free: suppress IPv6 autoconfiguration first.
for interface in /sys/class/net/*; do
    [ -e "$interface/device" ] || continue
    # Preserve any adapter already configured by the boot environment.
    read -r flags < "$interface/flags" || continue
    [ "$((flags & 1))" -eq 0 ] || continue
    name=${interface##*/}
    ipv6=/proc/sys/net/ipv6/conf/$name/disable_ipv6
    if [ -e "$ipv6" ] && ! (echo 1 > "$ipv6"); then
        echo "Cannot suppress IPv6 on $name; link probe unavailable"
        continue
    fi
    /sbin/ip link set dev "$name" up || echo "Cannot enable $name; link probe unavailable"
done
touch /run/nex-installer/prepared
