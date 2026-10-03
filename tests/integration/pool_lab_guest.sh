#!/bin/sh
# Build the tiny lab guest for the pool rollout lab: a 64 MiB BIOS-bootable raw disk (syslinux, one FAT partition)
# holding a Linux kernel and a busybox initramfs whose init prints a marker, then waits for the ACPI power button
# (the event virsh shutdown sends) and powers the machine off. The kernel and its two modules (acpi button, evdev) are
# the Debian linux-image package's, downloaded in the container. Needs docker and network. No root, no loop device;
# everything runs unprivileged in a debian container.
#   pool_lab_guest.sh OUT.raw
set -eu
[ $# -eq 1 ] || { echo "usage: pool_lab_guest.sh OUT.raw" >&2; exit 2; }
out=$(readlink -f -m "$1")
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/build.sh" <<'IN'
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends syslinux syslinux-common mtools dosfstools busybox-static fdisk cpio xz-utils >/dev/null
cd /w
mkdir kernel && (cd kernel && apt-get download "$(apt-cache depends linux-image-amd64 | awk '/Depends: linux-image-/ {print $2; exit}')" >/dev/null && dpkg-deb -x linux-image-*.deb .)
cp kernel/boot/vmlinuz-* vmlinuz
cp $(find kernel -path '*drivers/acpi/button.ko.xz') $(find kernel -path '*drivers/input/evdev.ko.xz') .
rm -rf kernel
mkdir -p rd/bin rd/dev rd/proc rd/sys rd/tmp
cp /bin/busybox rd/bin/busybox
xz -dc button.ko.xz > rd/button.ko; xz -dc evdev.ko.xz > rd/evdev.ko
cat > rd/init <<'EOT'
#!/bin/busybox sh
/bin/busybox --install -s /bin
mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev; mount -t tmpfs tmp /tmp
insmod /button.ko > /dev/tty0 2>&1; insmod /evdev.ko > /dev/tty0 2>&1
echo NEXLAB-GUEST-UP > /dev/tty0
# wait for any input device, then for the first event on any of them: the ACPI power-button press that `virsh shutdown`
# sends (the PS/2 keyboard of the machine never produces one)
n=0
while [ ! -e /dev/input/event0 ] && [ $n -lt 100 ]; do sleep 0.2; n=$((n+1)); done
{ ls /dev/input; cat /sys/class/input/*/name; } > /dev/tty0 2>&1
[ -e /dev/input/event0 ] || { echo NEXLAB-GUEST-NO-INPUT > /dev/tty0; sleep 100000; }
for d in /dev/input/event*; do ( dd if=$d bs=24 count=1 of=/dev/null 2>/dev/null; echo NEXLAB-GUEST-EVENT $d > /dev/tty0; kill 1 2>/dev/null; echo > /tmp/ev ) & done
while [ ! -e /tmp/ev ]; do sleep 0.5; done
echo NEXLAB-GUEST-POWEROFF > /dev/console
poweroff -f
EOT
chmod 755 rd/init
(cd rd && find . | cpio -o -H newc --quiet) | xz -C crc32 -9 > initrd.xz
truncate -s 64M disk.raw
echo 'start=2048,type=c,bootable' | sfdisk -q disk.raw
mkfs.vfat -F 16 --offset 2048 disk.raw 63488 >/dev/null
syslinux --offset 1048576 --install disk.raw
dd if=/usr/lib/syslinux/mbr/mbr.bin of=disk.raw bs=440 count=1 conv=notrunc 2>/dev/null
printf 'DEFAULT nexlab\nPROMPT 0\nTIMEOUT 1\nLABEL nexlab\n KERNEL vmlinuz\n INITRD initrd.xz\n APPEND console=tty0 console=ttyS0\n' > syslinux.cfg
mcopy -i disk.raw@@1M vmlinuz initrd.xz syslinux.cfg ::
chown -R "$OWNER" /w
IN
docker run --rm --cpus 1 --memory 1g -e OWNER="$(id -u):$(id -g)" -v "$work":/w debian:trixie-slim sh -c 'cd /w && sh build.sh'
mv "$work/disk.raw" "$out"
echo "guest disk $out"
