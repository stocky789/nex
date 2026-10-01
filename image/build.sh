#!/bin/sh
# Build the Nex Core image inside the pinned Debian trixie tools container (any workstation
# distribution works; Docker or a compatible runtime is the only host requirement besides Go).
#
#   image/build.sh base      mkosi base tree -> image/out/base/, root.erofs, root.verity, roothash, boot/ kernel+initrd
#   image/build.sh release   UKI, ESP tree and manifest.json from the base build and any layers (image/out/*.raw)
#   image/build.sh all       base, then release
#
# Environment (all optional):
#   NEX_LABEL, NEX_SEQUENCE   release label and monotonic sequence (default dev, 0)
#   SOURCE_DATE_EPOCH         build timestamp (default: SourceDateEpoch in mkosi.conf)
#   NEX_PACKAGE_DIRS          colon-separated directories of local .deb files (Nex libvirt, nex-core-local)
#   NEX_UKI_KEY, NEX_UKI_CERT PEM paths; when both are set the UKI is signed and the shim chain is used
#   NEX_UPDATE_TRUST          trust.json (from scripts/update-publish.py) pinned into the verity root at
#                             /usr/share/nex/update/trust.json. Without it the image cannot verify any update.
#   NEX_STATE_FORMAT          state_format this image reads (default 3), checked by nex-state-guard.service
#   NEX_IMAGE_OUT             output directory (default image/out)
#   NEX_MKOSI_CACHE           package cache directory (default <out>/cache)
#   NEX_DOCKER_CPUS, NEX_DOCKER_MEMORY   container caps (default 4 and 8g)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
out=${NEX_IMAGE_OUT:-$here/out}
cache=${NEX_MKOSI_CACHE:-$out/cache}
step=${1:-all}
mkdir -p "$out" "$cache"
out=$(cd "$out" && pwd)
cache=$(cd "$cache" && pwd)
snapshot=$(cat "$here/snapshot")

docker build -q -t nex-image-tools --build-arg "SNAPSHOT=$snapshot" "$here/tools" >/dev/null

# The image carries the same static binary Core's slot writer and the installer use.
(cd "$repo" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$out/nex-hostimage" ./cmd/nex-hostimage)
(cd "$repo" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$out/nex-update" ./internal/nexupdate/cmd/nex-update)

# mkosi 25.3 always adds the live security archive; pin it to the same snapshot through a sandbox tree.
sandbox=$out/sandbox
rm -rf "$sandbox"
mkdir -p "$sandbox/etc/apt/sources.list.d" "$sandbox/etc/apt/preferences.d"
# mkosi 25.3 hard-codes the live deb.debian.org and security.debian.org archives; refuse their packages so
# only the snapshot (the Mirror and the source above) can supply anything.
cat > "$sandbox/etc/apt/preferences.d/nex-snapshot-only" <<EOT
Package: *
Pin: origin deb.debian.org
Pin-Priority: -1

Package: *
Pin: origin security.debian.org
Pin-Priority: -1
EOT
cat > "$sandbox/etc/apt/sources.list.d/nex-security.sources" <<EOT
Types: deb
URIs: https://snapshot.debian.org/archive/debian-security/$snapshot/
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
EOT
cp "$sandbox/etc/apt/sources.list.d/nex-security.sources" "$out/apt-sources.list"

pkgdirs=""
dockerargs=""
IFS=:
for d in ${NEX_PACKAGE_DIRS:-}; do
    [ -d "$d" ] || { echo "image/build.sh: package directory not found: $d" >&2; exit 1; }
    d=$(cd "$d" && pwd)
    dockerargs="$dockerargs -v $d:/pkgs$d:ro"
    pkgdirs="$pkgdirs --package-directory=/pkgs$d"
done
unset IFS

# The pinned update trust anchor lives in the verity root, so it can only change with a new image.
trustarg=""
if [ -n "${NEX_UPDATE_TRUST:-}" ]; then
    [ -f "$NEX_UPDATE_TRUST" ] || { echo "image/build.sh: NEX_UPDATE_TRUST not found: $NEX_UPDATE_TRUST" >&2; exit 1; }
    trust=$(cd "$(dirname "$NEX_UPDATE_TRUST")" && pwd)/$(basename "$NEX_UPDATE_TRUST")
    dockerargs="$dockerargs -v $trust:/trust.json:ro"
    trustarg="--extra-tree=/trust.json:/usr/share/nex/update/trust.json"
else
    echo "image/build.sh: NEX_UPDATE_TRUST is not set: this image carries no update trust and cannot verify or install updates" >&2
fi

run() {
    # shellcheck disable=SC2086
    docker run --rm --privileged --cpus "${NEX_DOCKER_CPUS:-4}" --memory "${NEX_DOCKER_MEMORY:-8g}" \
        -v "$repo":/nex:ro -v "$out":/out -v "$cache":/cache $dockerargs \
        -e NEX_LABEL="${NEX_LABEL:-dev}" -e NEX_SEQUENCE="${NEX_SEQUENCE:-0}" -e NEX_STATE_FORMAT="${NEX_STATE_FORMAT:-3}" \
        -e SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-}" \
        -e NEX_UKI_KEY="${NEX_UKI_KEY:-}" -e NEX_UKI_CERT="${NEX_UKI_CERT:-}" \
        -w /out nex-image-tools "$@"
}

base() {
    pkgs=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$here/base.packages")
    args=""
    for p in $pkgs; do args="$args --package=$p"; done
    extra=""
    # Local Nex debs (matched libvirt, nex-core-local) are installed when supplied.
    if [ -n "$pkgdirs" ]; then
        extra="--package=nex-core-local"
    fi
    # The container runs as root, so the previous outputs are root-owned: remove them from inside it.
    run sh -c 'rm -rf /out/base /out/base.manifest /out/base.vmlinuz /out/base.initrd /out/base-dpkg.tar /out/base.dpkg-list /out/work'
    # shellcheck disable=SC2086
    # mkosi.images/ holds the optional layers (built by mkosi.images/build-layer.sh on top of this base). mkosi would
    # parse them as sub-images of the base, so the base is built from a config directory of links without them.
    conf=$out/baseconf
    rm -rf "$conf"
    mkdir -p "$conf"
    for f in mkosi.conf mkosi.extra mkosi.skeleton mkosi.finalize mkosi.postinst.chroot; do
        ln -s "/nex/image/$f" "$conf/$f"
    done
    run mkosi --directory /out/baseconf --output-dir /out --cache-dir /cache --workspace-dir /out/work \
        --mirror "https://snapshot.debian.org/archive/debian/$snapshot/" \
        --sandbox-tree=/out/sandbox --extra-tree=/out/nex-hostimage:/usr/bin/nex-hostimage \
        --extra-tree=/out/nex-update:/usr/bin/nex-update $trustarg \
        --force $pkgdirs $args $extra build
    run /nex/image/release.py base
}

release() {
    run /nex/image/release.py release
}

case $step in
    base) base ;;
    release) release ;;
    all) base; release ;;
    *) echo "usage: image/build.sh base|release|all" >&2; exit 2 ;;
esac
