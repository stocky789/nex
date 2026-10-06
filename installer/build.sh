#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Build only: never mounts/writes physical disks or changes host networking.
set -euo pipefail
cd "$(dirname "$0")/.."

# Every ISO build rebuilds from current tree source: appliance/build.py regenerates the corresponding
# source archive (dist/nex-source.tar.gz) and builds the Controller system image, a payload of the media.
# The Core system image (NEX_IMAGE_DIR) and nex-hostimage are built beforehand and passed in.
rebuild_iso_artifacts() {
    local version=$1
    local docker_bin=${NEX_DOCKER:-docker}
    local appliance_out=.cache/nex-iso/appliance
    rm -rf "$appliance_out"
    mkdir -p "$appliance_out"
    python3 appliance/build.py --version "$version" --output "$appliance_out" --docker "$docker_bin"
    [[ -s $appliance_out/controller-system.img && -s dist/nex-source.tar.gz ]] || {
        echo 'the Controller build produced no system image or source archive' >&2
        exit 1
    }
    mkdir -p dist/appliance
    rm -rf dist/appliance/*
    cp -a "$appliance_out"/. dist/appliance/
}

ensure_perl_exact_source() {
    # Base installer / appliance still ship perl 5.40.1-6 while newer snapshot
    # indices prefer 5.40.1-6+deb13u1. apt-get source then substitutes and the
    # retained-source gate fails. Pin the exact dsc and tarballs from the
    # snapshot where 5.40.1-6 last appeared.
    local dest=$downloads/perl-exact-source
    local dsc_base=https://snapshot.debian.org/archive/debian/20250729T023717Z/pool/main/p/perl
    local orig_base=https://snapshot.debian.org/archive/debian/20250119T203702Z/pool/main/p/perl
    mkdir -p "$dest"
    [[ -f $dest/perl_5.40.1-6.dsc ]] || curl -fL --retry 3 -o "$dest/perl_5.40.1-6.dsc" "$dsc_base/perl_5.40.1-6.dsc"
    [[ -f $dest/perl_5.40.1-6.debian.tar.xz ]] || curl -fL --retry 3 -o "$dest/perl_5.40.1-6.debian.tar.xz" "$dsc_base/perl_5.40.1-6.debian.tar.xz"
    [[ -f $dest/perl_5.40.1.orig.tar.xz ]] || curl -fL --retry 3 -o "$dest/perl_5.40.1.orig.tar.xz" "$orig_base/perl_5.40.1.orig.tar.xz"
    [[ -f $dest/perl_5.40.1.orig-regen-configure.tar.xz ]] || curl -fL --retry 3 -o "$dest/perl_5.40.1.orig-regen-configure.tar.xz" "$orig_base/perl_5.40.1.orig-regen-configure.tar.xz"
    python3 - <<'PY' "$dest"
import hashlib, sys
from pathlib import Path
dest = Path(sys.argv[1])
dsc = (dest / 'perl_5.40.1-6.dsc').read_text(errors='replace')
fields, key = {}, None
for line in dsc.splitlines():
    if line == '-----BEGIN PGP SIGNATURE-----':
        break
    if line[:1].isspace() and key:
        fields[key] += '\n' + line.strip()
    elif ':' in line:
        key, value = line.split(':', 1)
        fields[key] = value.lstrip()
assert fields.get('Source') == 'perl' and fields.get('Version') == '5.40.1-6', fields
for row in fields['Checksums-Sha256'].splitlines():
    if not row.strip():
        continue
    expected, size, name = row.split(maxsplit=2)
    artifact = dest / name
    assert artifact.is_file() and artifact.stat().st_size == int(size), name
    digest = hashlib.sha256(artifact.read_bytes()).hexdigest()
    assert digest == expected, (name, digest, expected)
print('perl exact source ok', dest)
PY
}

stage_virtio_win() {
    # Fedora virtio-win 0.1.302: BSD VirtIO drivers plus GPL/LGPL guest-agent and
    # SPICE tooling. Stage the unmodified ISO only with the matching Fedora
    # corresponding-source RPM retained under third-party/virtio-win.
    local version=0.1.302
    local release=1
    local iso_name=virtio-win-${version}.iso
    local iso_sha256=303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d
    local iso_bytes=877373440
    local src_rpm=virtio-win-${version}-${release}.src.rpm
    local src_sha256=25561bba120e67bfae842429d5e25764c38e74b92f78d9ef90c20ec41d2ad75d
    local base=https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-${version}-${release}
    local fetch=$downloads/virtio-win
    local third_party=$stage/nex/third-party/virtio-win
    # The complete source tree is needed for the separately distributed source
    # bundle, but the installation media needs only the source RPM itself.
    local retained_source=$cache/virtio-win-retained-source
    mkdir -p "$fetch" "$stage/nex/media" "$third_party/source"
    if [[ ! -f $fetch/$iso_name ]] || ! printf '%s  %s\n' "$iso_sha256" "$fetch/$iso_name" | sha256sum --check --status; then
        curl -fL --retry 3 -o "$fetch/$iso_name.partial" "$base/$iso_name"
        mv "$fetch/$iso_name.partial" "$fetch/$iso_name"
    fi
    printf '%s  %s\n' "$iso_sha256" "$fetch/$iso_name" | sha256sum --check
    [[ $(stat -c%s "$fetch/$iso_name") -eq $iso_bytes ]]
    if [[ ! -f $fetch/$src_rpm ]] || ! printf '%s  %s\n' "$src_sha256" "$fetch/$src_rpm" | sha256sum --check --status; then
        curl -fL --retry 3 -o "$fetch/$src_rpm.partial" "$base/$src_rpm"
        mv "$fetch/$src_rpm.partial" "$fetch/$src_rpm"
    fi
    printf '%s  %s\n' "$src_sha256" "$fetch/$src_rpm" | sha256sum --check
    # Compressing the driver ISO takes minutes and is deterministic (-n), so keep the result.
    if [[ ! -f $fetch/$iso_name.gz ]]; then
        gzip -9n -c "$fetch/$iso_name" > "$fetch/$iso_name.gz.partial"
        mv "$fetch/$iso_name.gz.partial" "$fetch/$iso_name.gz"
    fi
    cp "$fetch/$iso_name.gz" "$stage/nex/media/virtio-win.iso.gz"
    printf '%s  virtio-win.iso\n' "$iso_sha256" > "$stage/nex/media/virtio-win.iso.sha256"
    rm -rf "$retained_source"
    mkdir -p "$retained_source/source"
    cp "$fetch/$src_rpm" "$retained_source/source/"
    bsdtar -xf "$retained_source/source/$src_rpm" -C "$retained_source/source"
    # Nested QEMU guest-agent source RPM: retain its payloads as well.
    mkdir -p "$retained_source/source/mingw-qemu-ga-win"
    bsdtar -xf "$retained_source/source/mingw-qemu-ga-win-110.2.3-2.el10.src.rpm" -C "$retained_source/source/mingw-qemu-ga-win"
    for required in \
        virtio-win-prewhql-0.1-302-sources.zip \
        mingw-qemu-ga-win-110.2.3-2.el10.src.rpm \
        spice-vdagent-win-0.10.0-5-sources.zip \
        spice-qxl-wddm-dod-0.21-2-sources.zip \
        qxl-win-unsigned-0.1-24-sources.zip \
        virtio-win-guest-tools-installer-0.1.302.tar.gz \
        winfsp-master-sources.zip \
        virtio-win.spec; do
        test -f "$retained_source/source/$required"
    done
    test -d "$retained_source/source/mingw-qemu-ga-win"
    cp "$retained_source/source/$src_rpm" "$third_party/source/"
    cat > "$third_party/NOTICE" <<'NOTICE'
virtio-win 0.1.302 (Fedora virtio-win-0.1.302-1)

Unmodified Fedora public build redistributed with Nex Core. The ISO is not only
BSD driver code. Component inventory of the checksum-verified ISO:

- Windows VirtIO drivers (Balloon, NetKVM, viostor, vioscsi, vioserial, viorng,
  vioinput, viofs, viosock and others): BSD-3-Clause. Source retained as
  virtio-win-prewhql-0.1-302-sources.zip from the matching Fedora source RPM.
- guest-agent/qemu-ga-*.msi (MSI 110.2.3): QEMU guest agent, GPL-2.0-or-later,
  plus LGPL libraries. Corresponding source retained as
  mingw-qemu-ga-win-110.2.3-2.el10.src.rpm (and its nested payloads).
- virtio-win-guest-tools.exe and virtio-win-gt-*.msi: installer bundle embedding
  the driver MSIs, qemu-ga, SPICE vdagent and QXL. Installer sources retained as
  virtio-win-guest-tools-installer-0.1.302.tar.gz; SPICE as
  spice-vdagent-win-0.10.0-5-sources.zip, spice-qxl-wddm-dod-0.21-2-sources.zip
  and qxl-win-unsigned-0.1-24-sources.zip; WinFSP/MS-RL runtime as
  winfsp-master-sources.zip.

Exact matching corresponding source is the Fedora source RPM
virtio-win-0.1.302-1.src.rpm (SHA-256 pinned in installer/build.sh), retained
under this directory and in the release source bundle. See docs/LICENSING.md.
NOTICE
    cp "$third_party/NOTICE" "$retained_source/NOTICE"
    cat > "$third_party/manifest.tsv" <<MANIFEST
component	version	binary	binary_sha256	license	source_retained
virtio-win-iso	$version	virtio-win.iso	$iso_sha256	mixed (see NOTICE)	yes
virtio-win-drivers	$version	(on ISO)	$iso_sha256	BSD-3-Clause	yes
qemu-ga-win	110.2.3	guest-agent/qemu-ga-*.msi	$iso_sha256	GPL-2.0-or-later + LGPL deps	yes
spice-vdagent-win	0.10.0-5	(in guest-tools)	$iso_sha256	GPL-2.0	yes
spice-qxl-wddm-dod	0.21-2	(in guest-tools)	$iso_sha256	GPL-2.0	yes
virtio-win-guest-tools	$version	virtio-win-guest-tools.exe	$iso_sha256	mixed (see NOTICE)	yes
winfsp	master	(installer runtime)	$iso_sha256	MS-RL	yes
MANIFEST
    (
        cd "$third_party"
        sha256sum NOTICE manifest.tsv source/"$src_rpm" > checksums.sha256
    )
}

# The release directory built by image/build.sh (NEX_IMAGE_DIR, relative to the repository), the
# static nex-hostimage that installs and repairs it, and the payloads that live on nex-data rather
# than in the verity slot, in directories named by the SHA-256 of their main file.
stage_image_media() {
    local image=/work/${NEX_IMAGE_DIR:?NEX_IMAGE_DIR is required} hostimage=/work/${NEX_HOSTIMAGE_BIN:?NEX_HOSTIMAGE_BIN is required}
    local appliance=/work/dist/appliance virtio_sha=303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d
    [[ -x $hostimage ]] || { echo "nex-hostimage missing or not executable: $hostimage" >&2; exit 1; }
    "$hostimage" verify "$image"
    [[ -s $appliance/controller-system.img ]] || { echo "Controller system image missing: $appliance/controller-system.img" >&2; exit 1; }
    mkdir -p "$stage/nex/image" "$stage/nex/bin"
    cp -a "$image"/. "$stage/nex/image/"
    install -m755 "$hostimage" "$stage/nex/bin/nex-hostimage"
    install -m755 installer/nex-image-install.sh "$stage/nex/nex-image-install.sh"
    local primary
    primary=$(sha256sum "$appliance/controller-system.img" | awk '{print $1}')
    mkdir -p "$stage/nex/payload/$primary" "$stage/nex/payload/$virtio_sha"
    cp "$appliance/controller-system.img" "$stage/nex/payload/$primary/"
    cp "$stage/nex/media/virtio-win.iso.gz" "$stage/nex/payload/$virtio_sha/virtio-win.iso.gz"
}

# BIOS firmware: Debian Installer has no grub-install. nex-image-install.sh copies this root fragment to RAM
# and chroots into it, so it carries grub-install, its helpers, the i386-pc modules and the full shared
# library closure, all taken from the system image's own tree (the same GRUB the installed host runs).
stage_grub_bios() {
    local tree=$cache/grub-image-tree grub=$stage/nex/grub bin lib
    rm -rf "$tree" "$grub"
    mkdir -p "$tree" "$grub/usr/lib" "$grub/usr/lib64" "$grub/usr/bin" "$grub/usr/sbin"
    fsck.erofs --extract="$tree" "$stage/nex/image/root.erofs" > /dev/null
    for link in bin sbin lib lib64; do ln -s "usr/$link" "$grub/$link"; done
    for file in usr/sbin/grub-install usr/sbin/grub-probe usr/bin/grub-mkimage usr/bin/grub-mkrelpath \
                usr/bin/grub-editenv usr/lib/grub/i386-pc/grub-bios-setup; do
        [[ -f $tree/$file ]] || { echo "the system image carries no $file; add grub2-common, grub-common and grub-pc-bin to image/base.packages" >&2; exit 1; }
    done
    mkdir -p "$grub/usr/lib/grub"
    cp -a "$tree/usr/lib/grub/i386-pc" "$grub/usr/lib/grub/"
    [[ ! -d $tree/usr/share/grub ]] || { mkdir -p "$grub/usr/share"; cp -a "$tree/usr/share/grub" "$grub/usr/share/"; }
    for bin in usr/sbin/grub-install usr/sbin/grub-probe usr/bin/grub-mkimage usr/bin/grub-mkrelpath \
               usr/bin/grub-editenv usr/lib/grub/i386-pc/grub-bios-setup; do
        install -D -m755 "$tree/$bin" "$grub/$bin"
        # ldd runs in the image's own tree so the closure is the image's libraries, not this builder's.
        chroot "$tree" ldd "/$bin" | awk '/=> \// {print $3} $1 ~ /^\// {print $1}' | while read -r lib; do
            mkdir -p "$(dirname "$grub/${lib#/}")"
            cp -L --no-preserve=ownership "$tree/${lib#/}" "$grub/${lib#/}"
        done
    done
    [[ -x $grub/usr/sbin/grub-install ]] || { echo 'GRUB for BIOS firmware was not staged' >&2; exit 1; }
    [[ -e $grub/lib64/ld-linux-x86-64.so.2 ]] || { echo 'the dynamic loader is missing from the staged GRUB' >&2; exit 1; }
    stage_efibootmgr "$tree"
    rm -rf "$tree"
}

# UEFI firmware: Debian Installer has no efibootmgr, so no firmware boot entry could be made and a firmware that boots
# only from explicit entries found no loader. nex-image-install.sh copies this root fragment to RAM and runs it through
# its own loader; it carries efibootmgr and its shared library closure, taken from the system image's tree.
stage_efibootmgr() {
    local tree=$1 efi=$stage/nex/efi lib
    rm -rf "$efi"
    mkdir -p "$efi"/usr/{bin,sbin,lib,lib64}
    for link in bin sbin lib lib64; do ln -s "usr/$link" "$efi/$link"; done
    [[ -f $tree/usr/bin/efibootmgr ]] || { echo 'the system image carries no efibootmgr; add it to image/base.packages' >&2; exit 1; }
    install -D -m755 "$tree/usr/bin/efibootmgr" "$efi/usr/bin/efibootmgr"
    chroot "$tree" ldd /usr/bin/efibootmgr | awk '/=> \// {print $3} $1 ~ /^\// {print $1}' | while read -r lib; do
        mkdir -p "$(dirname "$efi/${lib#/}")"
        cp -L --no-preserve=ownership "$tree/${lib#/}" "$efi/${lib#/}"
    done
    [[ -e $efi/lib64/ld-linux-x86-64.so.2 ]] || { echo 'the dynamic loader is missing from the staged efibootmgr' >&2; exit 1; }
}

if [[ ${1:-} != --inside ]]; then
    mkdir -p .cache/nex-iso dist
    # Keep every source fetch (cdebconf, retained Debian sources) on the same immutable
    # snapshots. Live deb.debian.org advanced past 20260916 with unbound
    # 1.26.1-0+deb13u1 (first on snapshot 2026-09-19) and release run 4223
    # failed apt-get source for a version the pinned sources did not have.
    source_snapshot=${NEX_DEBIAN_SOURCE_SNAPSHOT:-https://snapshot.debian.org/archive/debian/20260920T000000Z/}
    security_snapshot=${NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT:-https://snapshot.debian.org/archive/debian-security/20261001T000000Z/}
    snapshot_url_ok() {
        # Reject whitespace/control injection into sources.list; require an exact
        # immutable snapshot.debian.org archive stamp URL (trailing slash).
        [[ "$1" =~ ^https://snapshot\.debian\.org/archive/(debian|debian-security)/[0-9]{8}T[0-9]{6}Z/$ ]]
    }
    snapshot_url_ok "$source_snapshot" && snapshot_url_ok "$security_snapshot" || {
        echo 'NEX_DEBIAN_SOURCE_SNAPSHOT and NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT must be immutable https://snapshot.debian.org/archive/(debian|debian-security)/<stamp>/ URLs.' >&2
        exit 1
    }
    # One version identifies a release. Default to the newest CHANGELOG heading so an
    # local build is not stamped with a stale constant; the release workflow passes
    # the same value explicitly.
    version=${NEX_VERSION:-$(python3 "$(dirname "$0")/../scripts/release-version.py" --current)}
    [[ -n $version ]] || { echo 'could not resolve a build version' >&2; exit 1; }
    docker_bin=${NEX_DOCKER:-docker}
    : "${NEX_IMAGE_DIR:?NEX_IMAGE_DIR is required: the image release directory built by image/build.sh}"
    : "${NEX_HOSTIMAGE_BIN:?NEX_HOSTIMAGE_BIN is required: the static nex-hostimage that installs the image}"
    rebuild_iso_artifacts "$version"
    "$docker_bin" build -t nex-iso-builder -f installer/Dockerfile installer
    exec "$docker_bin" run --rm --platform linux/amd64 \
        -v "$PWD:/work" -e BUILD_UID="$(id -u)" -e BUILD_GID="$(id -g)" \
        -e "NEX_DEBIAN_SOURCE_SNAPSHOT=$source_snapshot" \
        -e "NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT=$security_snapshot" \
        -e "NEX_ISO_VERSION=$version" \
        -e "NEX_NETINST_BASE_URL=${NEX_NETINST_BASE_URL:-}" \
        -e "NEX_IMAGE_DIR=$NEX_IMAGE_DIR" \
        -e "NEX_HOSTIMAGE_BIN=$NEX_HOSTIMAGE_BIN" \
        nex-iso-builder
fi
[[ $(dpkg --print-architecture) == amd64 ]]
cache=/work/.cache/nex-iso
stage=$cache/stage
# Pinned downloads are kept between builds and only fetched when missing. CI mounts a
# persistent Docker volume here (scripts/ci-docker.sh); a local build keeps them in the tree.
downloads=${NEX_DOWNLOADS:-$cache}
source_snapshot=${NEX_DEBIAN_SOURCE_SNAPSHOT:?NEX_DEBIAN_SOURCE_SNAPSHOT required}
security_snapshot=${NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT:?NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT required}
snapshot_url_ok() {
    [[ "$1" =~ ^https://snapshot\.debian\.org/archive/(debian|debian-security)/[0-9]{8}T[0-9]{6}Z/$ ]]
}
snapshot_url_ok "$source_snapshot" && snapshot_url_ok "$security_snapshot" || {
    echo 'NEX_DEBIAN_SOURCE_SNAPSHOT and NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT must be immutable https://snapshot.debian.org/archive/(debian|debian-security)/<stamp>/ URLs.' >&2
    exit 1
}
# The ISO carries exactly one Nex Core version: the one this build was started for.
version=${NEX_ISO_VERSION:?NEX_ISO_VERSION required}
# Artifact names use the readable release version (0.4.23-dev.251), not Debian's 0.4.23~~dev.251.
file_version=${version//\~\~/-}
file_version=${file_version//\~/-}
output=/work/dist/nex-core-${file_version}-amd64.iso
mkdir -p "$cache" "$downloads" /work/dist
trap 'chown -R "${BUILD_UID:-0}:${BUILD_GID:-0}" /work/dist "$cache"' EXIT
# Explicit release + expected digest; update together only after Debian signature
# verification. Use the archive because this release has left the current mirror.
iso=debian-13.6.0-amd64-netinst.iso
expected=65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7
base=https://cdimage.debian.org/cdimage/archive/13.6.0/amd64/iso-cd
curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 "$base/SHA256SUMS" -o "$cache/SHA256SUMS.tmp"
mv "$cache/SHA256SUMS.tmp" "$cache/SHA256SUMS"
curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 "$base/SHA256SUMS.sign" -o "$cache/SHA256SUMS.sign"
curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 https://www.debian.org/CD/key-DA87E80D6294BE9B.txt -o "$cache/cd-key.asc"
mkdir -p "$cache/gnupg"
chmod 700 "$cache/gnupg"
export GNUPGHOME=$cache/gnupg
gpg --batch --import "$cache/cd-key.asc"
fingerprint=DF9B9C49EAA9298432589D76DA87E80D6294BE9B
gpg --batch --export "$fingerprint" > "$cache/cd-key.gpg"
gpgv --keyring "$cache/cd-key.gpg" "$cache/SHA256SUMS.sign" "$cache/SHA256SUMS"
grep -Fx "$expected  $iso" "$cache/SHA256SUMS"
if [[ ! -f $downloads/$iso ]]; then
    # Resume across transient TLS/network drops; --retry alone does not cover mid-transfer EOFs.
    curl -fL --retry 5 --retry-all-errors --retry-delay 2 --continue-at - \
      "$base/$iso" -o "$downloads/$iso.partial"
    mv "$downloads/$iso.partial" "$downloads/$iso"
fi
printf '%s  %s\n' "$expected" "$downloads/$iso" | sha256sum --check
rm -rf "$stage"
mkdir -p "$stage/nex"
xorriso -osirrox on -indev "$downloads/$iso" -extract / "$stage"
chmod -R u+w "$stage"
# These optional firmware packages are outside the reviewed server-hardware
# scope. Remove their pool copies and repository metadata together.
python3 scripts/prune-iso-packages.py --tree "$stage" \
    dahdi-firmware-nonfree atmel-firmware bluez-firmware firmware-sof-signed \
    firmware-b43-installer firmware-b43legacy-installer
# The installer environment must exist on the pinned media (rescue boots the plain initrd).
for image in install.amd/vmlinuz install.amd/initrd.gz install.amd/gtk/initrd.gz; do
    test -s "$stage/$image"
done
# nex-image-install.sh expands the compressed VirtIO ISO with BusyBox's gunzip applet, so the
# initrd that runs the wizard must provide it.
initrd_root=$(mktemp -d)
(cd "$initrd_root" && gzip -dc "$stage/install.amd/gtk/initrd.gz" | cpio -id --quiet usr/bin/busybox)
"$initrd_root/usr/bin/busybox" --list | grep -Fx gunzip
rm -rf "$initrd_root"
printf '%s\n' "Nex Core $version amd64 development installer — derived from Debian 13" > "$stage/.disk/info"
cat > "$stage/README-NEX.txt" <<MEDIA
Nex Core $version — development installation media

Nex Core is an independent Debian-derived virtualization host distribution.
This is modified Nex media, not an official Debian release. Debian 13 supplies
its kernel, installer, base system and signed boot components; KVM/QEMU/libvirt
provide virtualization. Nex supplies host configuration and its own identity.

Read /nex/docs/LICENSING.md, LICENSE and NOTICE for licensing and release limits.
Original Debian README/notice files are retained as upstream documentation.
This local alpha installs Nex Core with its host-local web UI. Central Nex Controller
is deployed from that UI or separately. Install only onto an explicitly selected
disposable lab disk and retain recovery access.
MEDIA
install -m755 installer/root-account.sh "$stage/nex/root-account.sh"
# The same immutable snapshots as the corresponding-source archive. build-gui.sh fetches the
# exact cdebconf ABI from these pins (release run 4228 failed when only binary URIs were
# active in a bare apt-get source).
cat > "$cache/snapshot-sources.list" <<SOURCES
deb-src $source_snapshot trixie main contrib non-free non-free-firmware
deb-src $source_snapshot trixie-updates main contrib non-free non-free-firmware
deb-src $security_snapshot trixie-security main contrib non-free non-free-firmware
SOURCES
export NEX_SNAPSHOT_SOURCES=$cache/snapshot-sources.list
stage_virtio_win
stage_image_media
stage_grub_bios
cp "$cache/SHA256SUMS" "$cache/SHA256SUMS.sign" "$cache/cd-key.asc" "$stage/nex/"
cat > "$stage/nex/BUILD.txt" <<BUILD
Nex Core $version / internal development image
Base: $base/$iso
Base SHA256: $expected
Debian signing fingerprint: $fingerprint
Debian sources: immutable Debian trixie snapshot.debian.org pins ($source_snapshot, $security_snapshot)
Bundled media: Fedora virtio-win 0.1.302 with matching Fedora corresponding-source RPM
Build UTC: $(date -u +%FT%TZ)
Nex version: ${NEX_ISO_VERSION:-unknown}
Nex source: AGPL-3.0-only; upstream packages retain their own licenses.
Core and Controller on this media were rebuilt from the same corresponding-source
snapshot for this ISO build (see nex-source.product.sha256).
Public redistribution requires the matching source bundle and compliance manifest.
Build recipe: installer/build.sh; the system image on this media is built by image/build.sh.
BUILD
./installer/build-gui.sh
mkdir -p "$stage/nex/docs"
[[ -f ARTWORK-LICENSE.md ]] || { echo 'ARTWORK-LICENSE.md is required for branded media.' >&2; exit 1; }
cp LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$stage/nex/docs/"
# Full corresponding source from this build (host always regenerates dist/nex-source.tar.gz),
# plus the installer recipe used to produce this ISO.
[[ -f /work/dist/nex-source.tar.gz ]] || { echo 'dist/nex-source.tar.gz required; host build must run scripts/build-source.py' >&2; exit 1; }
source_stage=$(mktemp -d)
tar -xzf /work/dist/nex-source.tar.gz -C "$source_stage"
[[ -d $source_stage/nex-source ]] || { echo 'dist/nex-source.tar.gz missing nex-source/ prefix' >&2; exit 1; }
rm -rf "$source_stage/nex-source/installer"
cp -a installer "$source_stage/nex-source/"
cp -f LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$source_stage/nex-source/"
tar --sort=name --owner=0 --group=0 --numeric-owner -C "$source_stage" -czf "$stage/nex/nex-source.tar.gz" nex-source
rm -rf "$source_stage"
(cd "$stage/nex" && sha256sum nex-source.tar.gz > nex-source.tar.gz.sha256)
# Record that media source matches the regenerated archive inputs.
cp /work/dist/nex-source.tar.gz.sha256 "$stage/nex/nex-source.product.sha256"
# A public ISO must have one retained bundle covering the base installer, rebuilt and
# bundled components, notices, and exact hashes.
cat > "$cache/source-sources.list" <<SOURCES
deb-src $source_snapshot trixie main contrib non-free non-free-firmware
deb-src $source_snapshot trixie-updates main contrib non-free non-free-firmware
deb-src $security_snapshot trixie-security main contrib non-free non-free-firmware
deb-src https://snapshot.debian.org/archive/debian/20260711T235959Z/ trixie main contrib non-free non-free-firmware
deb-src https://snapshot.debian.org/archive/debian/20260711T235959Z/ trixie-updates main contrib non-free non-free-firmware
deb-src https://snapshot.debian.org/archive/debian-security/20260711T235959Z/ trixie-security main contrib non-free non-free-firmware
SOURCES
# Perl 5.40.1-6 remains on the media while newer snapshot indices prefer
# 5.40.1-6+deb13u1; retain the exact dsc/tarballs so the source bundle stays complete.
ensure_perl_exact_source
python3 /work/scripts/build-source-bundle.py \
    --version "$file_version" --iso "$downloads/$iso" --tree "$stage" \
    --apt-sources "$cache/source-sources.list" \
    --source-input "$cache/virtio-win-retained-source" --source-input "$stage/nex/third-party" --source-input "$stage/nex/nex-source.tar.gz" \
    --source-input "$downloads/perl-exact-source" --source-cache "$downloads/sources" \
    --output "/work/dist/nex-core-${file_version}-source.tar.gz"
cp installer/assets/boot-splash.png "$stage/isolinux/splash.png"
cat > "$stage/isolinux/isolinux.cfg" <<'BIOS'
path
include menu.cfg
default vesamenu.c32
prompt 0
timeout 0
BIOS
cat > "$stage/isolinux/menu.cfg" <<'BIOS'
menu background splash.png
menu title Nex Core
menu width 72
menu margin 4
menu vshift 9
menu rows 8
menu color title 0 #ff17233b #00f6f7fc none
menu color unsel 0 #ff17233b #00f6f7fc none
menu color sel 0 #ffffffff #ff2456d9 none
menu color border 0 #0017233b #00f6f7fc none
menu color tabmsg 0 #ff17233b #00f6f7fc none
label graphical
  menu label GUI install - Nex Core
  menu default
  kernel /install.amd/vmlinuz
  append vga=788 initrd=/install.amd/gtk/initrd.gz --- quiet
label nex
  menu label Minimal install - Nex Core (text)
  kernel /install.amd/vmlinuz
  append initrd=/install.amd/gtk/initrd.gz DEBIAN_FRONTEND=newt --- quiet
label serial
  menu label Install Nex Core (serial console ttyS0)
  kernel /install.amd/vmlinuz
  append initrd=/install.amd/gtk/initrd.gz DEBIAN_FRONTEND=newt console=ttyS0,115200n8 --- console=ttyS0,115200n8
label rescue
  menu label Debian rescue mode
  kernel /install.amd/vmlinuz
  append initrd=/install.amd/initrd.gz rescue/enable=true --- quiet
BIOS
cat > "$stage/boot/grub/nex-theme.txt" <<'THEME'
desktop-image: "/isolinux/splash.png"
title-text: ""
+ boot_menu {
  left = 6%
  top = 130
  width = 88%
  height = 240
  item_height = 35
  item_color = "#17233B"
  selected_item_color = "#2456D9"
  item_padding = 6
}
THEME
cat > "$stage/boot/grub/grub.cfg" <<'GRUB'
set default=0
set timeout=-1
if loadfont /boot/grub/font.pf2; then
  set gfxmode=640x480
  insmod all_video
  insmod gfxterm
  insmod png
  terminal_output gfxterm
  set theme=/boot/grub/nex-theme.txt
fi
set menu_color_normal=black/light-gray
set menu_color_highlight=white/blue
menuentry 'GUI install - Nex Core' {
  linux /install.amd/vmlinuz vga=788 --- quiet
  initrd /install.amd/gtk/initrd.gz
}
menuentry 'Minimal install - Nex Core (text)' {
  linux /install.amd/vmlinuz DEBIAN_FRONTEND=newt --- quiet
  initrd /install.amd/gtk/initrd.gz
}
menuentry 'Install Nex Core (serial console ttyS0)' {
  linux /install.amd/vmlinuz DEBIAN_FRONTEND=newt console=ttyS0,115200n8 --- console=ttyS0,115200n8
  initrd /install.amd/gtk/initrd.gz
}
menuentry 'Debian rescue mode' {
  linux /install.amd/vmlinuz rescue/enable=true --- quiet
  initrd /install.amd/initrd.gz
}
GRUB
# Recompute media checksums without the boot catalog (regenerated by xorriso).
(cd "$stage" && find . -type f ! -name md5sum.txt ! -path './isolinux/boot.cat' ! -path './isolinux/isolinux.bin' -print0 | sort -z | xargs -0 md5sum > md5sum.txt)
rm -f "$output"
# virtio-win is compressed for transport and restored byte-for-byte during install.
[[ -f $stage/nex/media/virtio-win.iso.gz && -f $stage/nex/media/virtio-win.iso.sha256 ]] || {
    echo 'compressed virtio-win media or checksum missing from staged media' >&2
    exit 1
}
[[ -f $stage/nex/third-party/virtio-win/NOTICE && -f $stage/nex/third-party/virtio-win/manifest.tsv ]] || {
    echo 'virtio-win third-party notice/manifest missing' >&2
    exit 1
}
[[ -f $stage/nex/third-party/virtio-win/source/virtio-win-0.1.302-1.src.rpm ]] || {
    echo 'virtio-win corresponding source RPM missing' >&2
    exit 1
}
grep -Fq $'\tsource_retained' "$stage/nex/third-party/virtio-win/manifest.tsv"
grep -Fq $'\tyes' "$stage/nex/third-party/virtio-win/manifest.tsv"
[[ $(gzip -dc "$stage/nex/media/virtio-win.iso.gz" | sha256sum | awk '{print $1}') == 303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d ]]
# Replay original hybrid BIOS/UEFI boot structure and signed EFI binaries.
xorriso -indev "$downloads/$iso" -outdev "$output" \
    -boot_image any replay -update_r "$stage" / -volid NEX_CORE -commit -end
python3 /work/scripts/build-source-bundle.py \
    --version "$file_version" --iso "$output" --tree "$stage" \
    --source-bundle "/work/dist/nex-core-${file_version}-source.tar.gz" \
    --compliance-manifest "/work/dist/nex-core-${file_version}-compliance-manifest.json"
(cd /work/dist && sha256sum "nex-core-${file_version}-compliance-manifest.json" > "nex-core-${file_version}-compliance-manifest.json.sha256")
cp "$stage/nex/BUILD.txt" /work/dist/BUILD.txt
(cd /work/dist && sha256sum "$(basename "$output")" > "$(basename "$output").sha256")
printf '\nBuilt %s\n' "$output"

# The net installer carries the same boot files; the image set and payloads are downloaded
# by hash (payloads.tsv rows image/ and payload/) instead of carried.
if [[ -n ${NEX_NETINST_BASE_URL:-} ]]; then
    netstage=$cache/netinst-stage
    rm -rf "$netstage" /work/dist/netinst
    cp -al "$stage" "$netstage"
    python3 installer/netinst-manifest.py "$netstage" "$NEX_NETINST_BASE_URL" /work/dist/netinst \
        --image "$stage/nex/image" --appliance /work/dist/appliance
    install -m755 installer/netinst-fetch.sh "$netstage/nex/netinst-fetch.sh"
    rm -f "$netstage/nex/media/virtio-win.iso.gz"
    rm -rf "$netstage/nex/image" "$netstage/nex/payload"
    # The shared source bundle retains these redundant payloads and exact notices.
    rm -rf "$netstage/nex/third-party/virtio-win/source"
    # Replace transport descriptions without modifying hardlinked media files.
    python3 - "$netstage" <<'PYNOTICE'
from pathlib import Path
import sys
root = Path(sys.argv[1]) / 'nex'
notice = root / 'third-party/virtio-win/NOTICE'
text = notice.read_text().replace('Unmodified Fedora public build redistributed with Nex Core.',
                                  'Unmodified Fedora public build downloaded directly during installation.').replace(
    'under this directory and in the release source bundle.', 'in the shared release source bundle.')
notice.unlink()
notice.write_text(text)
checksums = notice.with_name('checksums.sha256')
checksums.unlink()
build = root / 'BUILD.txt'
text = build.read_text().replace('Bundled media: Fedora virtio-win 0.1.302 with matching Fedora corresponding-source RPM',
    'Downloaded media: Fedora virtio-win 0.1.302; matching source RPM retained in the shared release source bundle')
text += 'Network installation: exact download URLs and SHA256 values are in nex/payloads.tsv.\n'
build.unlink()
build.write_text(text)
PYNOTICE
    (cd "$netstage/nex/third-party/virtio-win" && sha256sum NOTICE manifest.tsv > checksums.sha256)
    netoutput=/work/dist/nex-core-${file_version}-amd64-netinst.iso
    rm -f "$netstage/.disk/info" "$netstage/md5sum.txt"
    printf '%s\n' "Nex Core $version amd64 net installer — internet required" > "$netstage/.disk/info"
    (cd "$netstage" && find . -type f ! -name md5sum.txt ! -path './isolinux/boot.cat' ! -path './isolinux/isolinux.bin' -print0 | sort -z | xargs -0 md5sum > md5sum.txt)
    rm -f "$netoutput"
    xorriso -indev "$downloads/$iso" -outdev "$netoutput" \
        -boot_image any replay -update_r "$netstage" / -volid NEX_NETINST -commit -end
    python3 /work/scripts/build-source-bundle.py \
        --version "$file_version" --iso "$netoutput" --tree "$netstage" \
        --source-bundle "/work/dist/nex-core-${file_version}-source.tar.gz" \
        --compliance-manifest "/work/dist/nex-core-${file_version}-netinst-compliance-manifest.json"
    python3 - "/work/dist/nex-core-${file_version}-netinst-compliance-manifest.json" /work/dist/netinst "$netstage/nex/payloads.tsv" <<'PYMANIFEST'
import hashlib, json, sys
from pathlib import Path
path = Path(sys.argv[1])
data = json.loads(path.read_text())
data['netinst_downloads'] = [dict(zip(('sha256', 'destination', 'url'), line.split('\t')))
                             for line in Path(sys.argv[3]).read_text().splitlines()]
data['netinst_payloads'] = []
for payload in sorted(p for p in Path(sys.argv[2]).iterdir() if p.is_file()):
    with payload.open('rb') as stream:
        checksum = hashlib.file_digest(stream, 'sha256').hexdigest()
    data['netinst_payloads'].append({'file': payload.name, 'sha256': checksum})
path.write_text(json.dumps(data, indent=2, sort_keys=True) + '\n')
PYMANIFEST
    (cd /work/dist && sha256sum "$(basename "$netoutput")" > "$(basename "$netoutput").sha256" && \
        sha256sum "nex-core-${file_version}-netinst-compliance-manifest.json" > "nex-core-${file_version}-netinst-compliance-manifest.json.sha256")
    printf '\nBuilt %s\n' "$netoutput"
fi
