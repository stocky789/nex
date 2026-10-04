#!/bin/sh
# Build one optional feature layer as a sysext erofs on top of a finished base build.
#
#   image/mkosi.images/build-layer.sh nex-zfs|nex-cluster
#
# Needs image/out/base (image/build.sh base) and image/out/base-dpkg.tar. Output:
#   image/out/<layer>.raw          erofs sysext, bound to the base BUILD_ID
#   image/out/<layer>.raw.json     manifest fragment {id, kind, sha256, bytes, requires_base}
#   image/out/<layer>.finalize.json  what layer-finalize normalised
# Environment: NEX_IMAGE_OUT, NEX_MKOSI_CACHE, NEX_PACKAGE_DIRS (as image/build.sh), SOURCE_DATE_EPOCH,
# NEX_MODULE_KEY/NEX_MODULE_CERT (nex-zfs; NEX_RELEASE=1 requires them, otherwise --dev-key is used).
set -eu

layer=${1:?usage: build-layer.sh nex-zfs|nex-cluster}
here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$(dirname "$here")")
out=${NEX_IMAGE_OUT:-$repo/image/out}
cache=${NEX_MKOSI_CACHE:-$out/cache}
[ -d "$here/$layer" ] || { echo "unknown layer $layer" >&2; exit 2; }
[ -d "$out/base" ] || { echo "no base build in $out/base: run image/build.sh base first" >&2; exit 1; }
out=$(cd "$out" && pwd)
mkdir -p "$cache"; cache=$(cd "$cache" && pwd)
snapshot=$(cat "$repo/image/snapshot")
sde=${SOURCE_DATE_EPOCH:-1790000000}

build_id=$(sed -n 's/^BUILD_ID=//p' "$out/base/usr/lib/os-release" | tr -d '"')
[ -n "$build_id" ] || { echo "base os-release has no BUILD_ID" >&2; exit 1; }

pkgdirs=""; dockerargs=""
if [ "$layer" = nex-zfs ]; then
    if [ -n "${NEX_MODULE_KEY:-}" ]; then keyflag=""; else keyflag="--dev-key"; fi
    # shellcheck disable=SC2086
    # NEX_REUSE_MODULES=1 keeps an existing build for iteration; never set it for a reproducibility check.
    if [ "${NEX_REUSE_MODULES:-0}" != 1 ] || [ ! -d "$out/zfs-modules" ]; then
        "$repo/scripts/build-zfs-modules.sh" --base-tree "$out/base" --out "$out/zfs-modules" $keyflag
    fi
    # zfs-zed requires zfs-modules | zfs-dkms. The modules ship as a package providing zfs-modules, so apt
    # never pulls zfs-dkms, a compiler or headers into the layer.
    pkgdirs="$pkgdirs --package-directory=/out/zfs-deb"
fi

pkgs=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$here/$layer/layer.packages")
args=""
for p in $pkgs; do args="$args --package=$p"; done
IFS=:
for d in ${NEX_PACKAGE_DIRS:-}; do
    d=$(cd "$d" && pwd)
    dockerargs="$dockerargs -v $d:/pkgs$d:ro"
    pkgdirs="$pkgdirs --package-directory=/pkgs$d"
done
unset IFS

# Same pinned tools image as image/build.sh; NEX_TOOLS_IMAGE substitutes another (lab use). mkosi needs
# reprepro to turn --package-directory into a repository, so the tools image must carry it.
tools=${NEX_TOOLS_IMAGE:-nex-image-tools}
if [ -z "${NEX_TOOLS_IMAGE:-}" ]; then
    docker build -q -t "$tools" --build-arg "SNAPSHOT=$snapshot" "$repo/image/tools" >/dev/null
fi
mkdir -p "$out/layers"
# shellcheck disable=SC2086
docker run --rm --privileged ${NEX_DOCKER_CPUS:+--cpus $NEX_DOCKER_CPUS} ${NEX_DOCKER_MEMORY:+--memory $NEX_DOCKER_MEMORY} -v "$repo":/nex:ro -v "$out":/out -v "$cache":/cache $dockerargs \
    -e SOURCE_DATE_EPOCH="$sde" -e LAYER="$layer" -e BUILD_ID="$build_id" -e SNAPSHOT="$snapshot" \
    -e MKOSI_ARGS="$pkgdirs $args" -w /out "$tools" sh -eu -c '
    # The dpkg database was removed from the base output; a copy of the tree with it restored is the
    # overlay lower, so apt knows what is already installed and installs only the layer delta.
    if [ "$LAYER" = nex-zfs ]; then
        kver=$(ls /out/zfs-modules/usr/lib/modules)
        zver=$(sed -n "s/.*\"zfs_dkms\": \"\(.*\)\",/\1/p" /out/zfs-modules/usr/share/nex/layers/nex-zfs-modules.json)
        rm -rf /out/zfs-deb /out/zfs-deb-root; mkdir -p /out/zfs-deb /out/zfs-deb-root/DEBIAN
        cp -a /out/zfs-modules/usr /out/zfs-deb-root/usr
        cat > /out/zfs-deb-root/DEBIAN/control <<EOT
Package: nex-zfs-modules
Version: $zver+nex1
Architecture: amd64
Maintainer: Nex Core <nex@localhost>
Provides: zfs-modules
Section: kernel
Priority: optional
Description: OpenZFS kernel modules for $kver, built and signed by the Nex image build
 Built from the Debian zfs-linux source (zfs-dkms $zver) against the base kernel; CDDL-1.0.
EOT
        find /out/zfs-deb-root -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
        dpkg-deb --root-owner-group -Zxz --build /out/zfs-deb-root "/out/zfs-deb/nex-zfs-modules_${zver}+nex1_amd64.deb"
        rm -rf /out/zfs-deb-root
    fi
    # root-owned leftovers of an earlier run can only be removed in here
    rm -rf /out/base-with-dpkg /out/layers/$LAYER
    cp -a --reflink=auto /out/base /out/base-with-dpkg
    if [ -f /out/base-dpkg.tar ]; then tar -xf /out/base-dpkg.tar -C /out/base-with-dpkg; fi
    test -f /out/base-with-dpkg/var/lib/dpkg/status
    # Layers need contrib (zfsutils-linux); mkosi adds the live security archive, so pin it to the snapshot
    # exactly as image/build.sh does, with the same components as the main mirror.
    rm -rf /out/sandbox-layers
    mkdir -p /out/sandbox-layers/etc/apt/sources.list.d
    cat > /out/sandbox-layers/etc/apt/sources.list.d/nex-security.sources <<EOT
Types: deb
URIs: https://snapshot.debian.org/archive/debian-security/$SNAPSHOT/
Suites: trixie-security
Components: main contrib
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
EOT
    mkosi --directory /nex/image/mkosi.images/$LAYER --output-dir /out/layers --cache-dir /cache \
        --workspace-dir /out/work --base-tree=/out/base-with-dpkg \
        --mirror "https://snapshot.debian.org/archive/debian/$SNAPSHOT/" --sandbox-tree=/out/sandbox-layers \
        --force $MKOSI_ARGS build
    python3 /nex/scripts/layer-finalize.py --layer-tree /out/layers/$LAYER --base-tree /out/base-with-dpkg \
        --spec /nex/image/mkosi.images/$LAYER/layer.json --build-id "$BUILD_ID" --report /out/$LAYER.finalize.json
    rm -f /out/$LAYER.raw
    mkfs.erofs -zlzma,level=9 -C65536 -T "$SOURCE_DATE_EPOCH" -U 5e1ec7ed-0000-4000-8000-000000000004 \
        --all-root --quiet /out/$LAYER.raw /out/layers/$LAYER
    sum=$(sha256sum /out/$LAYER.raw | cut -d" " -f1)
    printf "{\"id\": \"%s\", \"kind\": \"layer\", \"sha256\": \"%s\", \"bytes\": %s, \"requires_base\": \"%s\"}\n" \
        "$LAYER" "$sum" "$(stat -c %s /out/$LAYER.raw)" "$BUILD_ID" > /out/$LAYER.raw.json
    rm -rf /out/base-with-dpkg /out/work /out/sandbox-layers
'
echo "layer $layer built for base $build_id: $out/$layer.raw"
