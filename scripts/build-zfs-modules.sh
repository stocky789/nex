#!/bin/sh
# Build, sign and stage the OpenZFS kernel modules for the base image's kernel (nex-zfs layer input).
#
#   scripts/build-zfs-modules.sh [--base-tree DIR] [--kver VERSION] [--out DIR] [--dev-key]
#
# The compiler, kernel headers and DKMS exist only in a pinned builder container
# (image/mkosi.images/nex-zfs/zfs-builder); nothing of them reaches a host or a slot.
# Output (default image/out/zfs-modules): usr/lib/modules/<kver>/updates/nex-zfs/*.ko[.xz]
# plus usr/share/nex/layers/nex-zfs-modules.json (build record). image/mkosi.images/build-layer.sh packs the
# tree as the nex-zfs-modules .deb (Provides: zfs-modules, which zfs-zed requires) so no dkms package is installed.
#
# Inputs (environment, never committed):
#   NEX_APT_SOURCES    sources.list used inside the builder (default: public snapshot.debian.org URLs
#                      at NEX_SNAPSHOT, security snapshot included). A private mirror is supplied here.
#   NEX_SNAPSHOT       snapshot timestamp for the default sources (default: the contents of image/snapshot, the same pin
#                      image/build.sh and build-layer.sh use, so modules and zfsutils come from one OpenZFS version).
#   ZFS_DKMS_VERSION   optional exact zfs-dkms version to install (default: candidate of the sources).
#   NEX_MODULE_KEY / NEX_MODULE_CERT   module signing key and DER or PEM certificate. Release builds
#                      (NEX_RELEASE=1) fail closed without them. --dev-key generates a throwaway pair
#                      under .cache/image-keys/ for lab use only.
#   SOURCE_DATE_EPOCH  fixed timestamp for the build (default 1790000000, same as image/build.sh).
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
base_tree=$root/image/out/base
out=$root/image/out/zfs-modules
kver=
dev_key=0
while [ $# -gt 0 ]; do
  case $1 in
    --base-tree) base_tree=$2; shift 2 ;;
    --kver) kver=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --dev-key) dev_key=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$kver" ]; then
  # The module must match the base kernel exactly: read it from the tree the layer is built on.
  set -- "$base_tree"/usr/lib/modules/*/
  [ $# -eq 1 ] && [ -d "$1" ] || { echo "expected exactly one kernel under $base_tree/usr/lib/modules; pass --kver" >&2; exit 1; }
  kver=$(basename "$1")
fi

snapshot=${NEX_SNAPSHOT:-$(cat "$root/image/snapshot")}
[ -n "$snapshot" ] || { echo "no snapshot: image/snapshot is empty or unreadable and NEX_SNAPSHOT is unset" >&2; exit 1; }
sde=${SOURCE_DATE_EPOCH:-1790000000}
key=${NEX_MODULE_KEY:-}
cert=${NEX_MODULE_CERT:-}
if [ -z "$key" ] || [ -z "$cert" ]; then
  if [ "${NEX_RELEASE:-0}" = 1 ]; then
    echo "release build: NEX_MODULE_KEY and NEX_MODULE_CERT are required (no throwaway-key fallback)" >&2
    exit 1
  fi
  if [ "$dev_key" != 1 ]; then
    echo "no module signing key: set NEX_MODULE_KEY/NEX_MODULE_CERT or pass --dev-key for a lab-only key" >&2
    exit 1
  fi
  kd=$root/.cache/image-keys
  key=$kd/nex-module.key
  cert=$kd/nex-module.crt
  if [ ! -s "$key" ] || [ ! -s "$cert" ]; then
    mkdir -p "$kd"
    # A certificate with code-signing usage: the kernel's platform keyring (MOK) accepts it for modules.
    openssl req -new -x509 -newkey rsa:3072 -nodes -days 3650 -sha256 \
      -subj "/CN=Nex Core module signing (development only)/" \
      -addext "basicConstraints=critical,CA:FALSE" -addext "keyUsage=critical,digitalSignature" \
      -addext "extendedKeyUsage=codeSigning" -addext "subjectKeyIdentifier=hash" \
      -keyout "$key" -out "$cert" >/dev/null 2>&1
    chmod 600 "$key"
    openssl x509 -in "$cert" -outform DER -out "$kd/nex-module.der"
  fi
fi
[ -r "$key" ] && [ -r "$cert" ] || { echo "module key or certificate not readable" >&2; exit 1; }
key=$(realpath -- "$key")
cert=$(realpath -- "$cert")

builddir=$root/image/mkosi.images/nex-zfs/zfs-builder
ctx=$root/.cache/image-builder/zfs
mkdir -p "$ctx"
if [ -n "${NEX_APT_SOURCES:-}" ]; then
  cp "$NEX_APT_SOURCES" "$ctx/sources.list"
else
  {
    echo "deb https://snapshot.debian.org/archive/debian/$snapshot/ trixie main contrib"
    echo "deb https://snapshot.debian.org/archive/debian-security/$snapshot/ trixie-security main contrib"
  } > "$ctx/sources.list"
fi
cp "$builddir/Dockerfile" "$ctx/Dockerfile"
tag=nex-zfs-builder:$(cat "$ctx/Dockerfile" "$ctx/sources.list" | sha256sum | cut -c1-16)-$(printf %s "$kver${ZFS_DKMS_VERSION:-}" | sha256sum | cut -c1-8)
docker image inspect "$tag" >/dev/null 2>&1 || \
  docker build -q -t "$tag" --build-arg "KVER=$kver" --build-arg "ZFS_DKMS_VERSION=${ZFS_DKMS_VERSION:-}" "$ctx" >/dev/null

rm -rf "$out"
mkdir -p "$out"
out=$(cd "$out" && pwd -P)
docker run --rm -i -e "KVER=$kver" -e "SOURCE_DATE_EPOCH=$sde" -e "HOST_UID=$(id -u)" -e "HOST_GID=$(id -g)" \
  -v "$out":/out -v "$key":/keys/module.key:ro -v "$cert":/keys/module.crt:ro "$tag" sh -eu <<'EOF'
export LC_ALL=C TZ=UTC KBUILD_BUILD_USER=nex KBUILD_BUILD_HOST=nex KBUILD_BUILD_TIMESTAMP="$(date -u -d "@$SOURCE_DATE_EPOCH")"
zver=$(dpkg-query -W -f='${Version}' zfs-dkms)
zsrc=$(dpkg-query -W -f='${Source}' zfs-dkms)
khdr=$(dpkg-query -W -f='${Version}' "linux-headers-$KVER")
# Module compression follows the kernel's own build config so one modprobe path serves base and layer.
if grep -q '^CONFIG_MODULE_COMPRESS_XZ=y' "/usr/src/linux-headers-$KVER/.config"; then EXT=.xz
elif grep -q '^CONFIG_MODULE_COMPRESS_ZSTD=y' "/usr/src/linux-headers-$KVER/.config"; then EXT=.zst
else EXT=; fi
src=$(ls -d /usr/src/zfs-* | sed -n 1p)
mv_ver=${src#/usr/src/zfs-}
# dkms 3 signs with its own MOK key only when one exists; there is none here, so signing below is ours alone.
rm -rf /var/lib/dkms/zfs
dkms build -m zfs -v "$mv_ver" -k "$KVER" >/tmp/dkms.log 2>&1 || { cat /tmp/dkms.log >&2; exit 1; }
mods=/var/lib/dkms/zfs/$mv_ver/$KVER/x86_64/module
ls "$mods" | grep -q . || { echo "dkms produced no modules" >&2; exit 1; }
sign=$(find /usr/lib -name sign-file -path '*scripts*' | sed -n 1p)
[ -n "$sign" ] || { echo "sign-file not found in the kernel build tools" >&2; exit 1; }
dest=/out/usr/lib/modules/$KVER/updates/nex-zfs
mkdir -p "$dest"
openssl x509 -in /keys/module.crt -outform DER -out /tmp/module.der 2>/dev/null || cp /keys/module.crt /tmp/module.der
fpr=$(openssl x509 -inform DER -in /tmp/module.der -noout -fingerprint -sha256 | cut -d= -f2 | tr -d : | tr A-F a-f)
for m in "$mods"/*.ko*; do
  n=$(basename "$m")
  case $n in *.ko.xz) xz -dc "$m" > "/tmp/${n%.xz}" ;; *.ko.zst) zstd -dcq "$m" > "/tmp/${n%.zst}" ;; *) cp "$m" "/tmp/$n" ;; esac
  n=${n%.xz}; n=${n%.zst}
  strip --strip-debug "/tmp/$n"
  "$sign" sha256 /keys/module.key /tmp/module.der "/tmp/$n"
  case $EXT in
    .xz) xz -9e --check=crc32 -T1 -c "/tmp/$n" > "$dest/$n.xz" ;;
    .zst) zstd -19 -q -c "/tmp/$n" > "$dest/$n.zst" ;;
    *) cp "/tmp/$n" "$dest/$n" ;;
  esac
done
touch -d "@$SOURCE_DATE_EPOCH" "$dest"/*
mkdir -p /out/usr/share/nex/layers
{
  printf '{\n  "kernel": "%s",\n  "kernel_headers": "%s",\n  "zfs_source_package": "%s",\n  "zfs_dkms": "%s",\n  "module_key_sha256": "%s",\n  "modules": {\n' "$KVER" "$khdr" "$zsrc" "$zver" "$fpr"
  first=1
  for f in "$dest"/*; do
    [ $first = 1 ] || printf ',\n'
    first=0
    printf '    "%s": "%s"' "$(basename "$f")" "$(sha256sum "$f" | cut -d' ' -f1)"
  done
  printf '\n  }\n}\n'
} > /out/usr/share/nex/layers/nex-zfs-modules.json
chown -R "$HOST_UID:$HOST_GID" /out
EOF
echo "zfs modules for $kver staged in $out"
