#!/bin/sh
# Build an unsigned UKI for a release. Usage: mkuki.sh <out.efi> <label> <roothash> [extra cmdline...]
# Env: EXTRA_INITRDS (space separated cpio files appended to the image initrd), SIGN=1 uses /lab/keys/uki.{key,crt}.
set -eu
out=$1; label=$2; hash=$3; shift 3
cat > /tmp/os-release <<EOT
NAME="Nex Core"
ID=nex-core
PRETTY_NAME="Nex Core $label"
VERSION_ID=$label
IMAGE_ID=nex-core
IMAGE_VERSION=$label
EOT
initrds="--initrd=/o/nexspike.initrd"
for i in ${EXTRA_INITRDS:-}; do initrds="$initrds --initrd=$i"; done
sign=""
[ "${SIGN:-0}" = 1 ] && sign="--secureboot-private-key=/lab/keys/uki.key --secureboot-certificate=/lab/keys/uki.crt"
ukify build --linux=/o/nexspike.vmlinuz $initrds \
  --cmdline="roothash=$hash console=tty0 console=ttyS0,115200n8 panic=10 systemd.ssh_auto=no $*" \
  --os-release=@/tmp/os-release --uname=6.12.111+deb13-amd64 $sign --output="$out" >/dev/null
