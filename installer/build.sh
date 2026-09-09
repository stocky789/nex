#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Build only: never mounts/writes physical disks or changes host networking.
set -euo pipefail
cd "$(dirname "$0")/.."

# Every ISO build regenerates corresponding source and injects it into the
# nex-core-local package copied onto the media, so installs never ship stale
# AGPL source from an older package rebuild.
prepare_iso_core_local() {
    local src_deb=$1
    local out_deb=$2
    local extract work
    [[ -f $src_deb ]] || { echo "missing nex-core-local package: $src_deb" >&2; exit 1; }
    [[ -f dist/nex-source.tar.gz ]] || { echo 'dist/nex-source.tar.gz missing after build-source' >&2; exit 1; }
    work=$(mktemp -d)
    extract=$work/pkg
    mkdir -p "$extract"
    dpkg-deb -x "$src_deb" "$extract"
    dpkg-deb -e "$src_deb" "$extract/DEBIAN"
    install -d "$extract/usr/share/nex/core-local"
    install -m644 dist/nex-source.tar.gz "$extract/usr/share/nex/core-local/nex-source.tar.gz"
    mkdir -p "$(dirname "$out_deb")"
    fakeroot dpkg-deb -Zxz -b "$extract" "$out_deb"
    rm -rf "$work"
}

if [[ ${1:-} != --inside ]]; then
    mkdir -p .cache/nex-iso dist
    python3 scripts/build-source.py
    docker build -t nex-iso-builder -f installer/Dockerfile installer
    env_args=()
    core_local_deb=${CORE_LOCAL_DEB:-}
    if [[ -z $core_local_deb ]]; then
        shopt -s nullglob
        core_local_candidates=(dist/core-local/nex-core-local_*_amd64.deb)
        shopt -u nullglob
        if [[ ${#core_local_candidates[@]} -eq 0 ]]; then
            echo 'nex-core-local .deb required: set CORE_LOCAL_DEB or place a package under dist/core-local/' >&2
            exit 1
        fi
        core_local_deb=$(ls -1t "${core_local_candidates[@]}" | head -n1)
    fi
    abs=$(readlink -f -- "$core_local_deb")
    case $abs in
        "$PWD"/*) ;;
        *)
            echo 'CORE_LOCAL_DEB must be a path inside this repository' >&2
            exit 1
            ;;
    esac
    version=$(dpkg-deb -f "$abs" Version)
    iso_core_local=.cache/nex-iso/nex-core-local_${version}_amd64.deb
    prepare_iso_core_local "$abs" "$iso_core_local"
    env_args+=(-e "CORE_LOCAL_DEB=/work/${iso_core_local}")
    exec docker run --rm --platform linux/amd64 \
        -v "$PWD:/work" -e BUILD_UID="$(id -u)" -e BUILD_GID="$(id -g)" \
        "${env_args[@]}" \
        nex-iso-builder
fi
[[ $(dpkg --print-architecture) == amd64 ]]
cache=/work/.cache/nex-iso
stage=$cache/stage
output=/work/dist/nex-core-0.1.0-alpha1-amd64.iso
mkdir -p "$cache" /work/dist
trap 'chown -R "${BUILD_UID:-0}:${BUILD_GID:-0}" /work/dist "$cache"' EXIT
# Explicit release + expected digest; update together only after Debian signature
# verification. Archive fallback supports rebuilding after current advances.
iso=debian-13.6.0-amd64-netinst.iso
expected=65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7
base=https://cdimage.debian.org/debian-cd/13.6.0/amd64/iso-cd
if ! curl -fsSL "$base/SHA256SUMS" -o "$cache/SHA256SUMS.tmp"; then
    base=https://cdimage.debian.org/mirror/cdimage/archive/13.6.0/amd64/iso-cd
    curl -fsSL "$base/SHA256SUMS" -o "$cache/SHA256SUMS.tmp"
fi
mv "$cache/SHA256SUMS.tmp" "$cache/SHA256SUMS"
curl -fsSL "$base/SHA256SUMS.sign" -o "$cache/SHA256SUMS.sign"
curl -fsSL https://www.debian.org/CD/key-DA87E80D6294BE9B.txt -o "$cache/cd-key.asc"
mkdir -p "$cache/gnupg"
chmod 700 "$cache/gnupg"
export GNUPGHOME=$cache/gnupg
gpg --batch --import "$cache/cd-key.asc"
fingerprint=DF9B9C49EAA9298432589D76DA87E80D6294BE9B
gpg --batch --export "$fingerprint" > "$cache/cd-key.gpg"
gpgv --keyring "$cache/cd-key.gpg" "$cache/SHA256SUMS.sign" "$cache/SHA256SUMS"
grep -Fx "$expected  $iso" "$cache/SHA256SUMS"
if [[ ! -f $cache/$iso ]]; then
    curl -fL --retry 3 "$base/$iso" -o "$cache/$iso.partial"
    mv "$cache/$iso.partial" "$cache/$iso"
fi
printf '%s  %s\n' "$expected" "$cache/$iso" | sha256sum --check
rm -rf "$stage"
mkdir -p "$stage/nex/repo"
xorriso -osirrox on -indev "$cache/$iso" -extract / "$stage"
chmod -R u+w "$stage"
# Both frontends must exist on the pinned media before advertising the choice.
for image in install.amd/vmlinuz install.amd/initrd.gz install.amd/gtk/initrd.gz; do
    test -s "$stage/$image"
done
printf '%s\n' 'Nex Core 0.1.0-alpha1 amd64 development installer — derived from Debian 13' > "$stage/.disk/info"
cat > "$stage/README-NEX.txt" <<'MEDIA'
Nex Core 0.1.0-alpha1 — development installation media

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
cp installer/preseed.cfg "$stage/preseed.cfg"
cp installer/late-install.sh installer/root-account.sh installer/packages.txt "$stage/nex/"
# Empty dpkg status is intentional: resolve the whole closure, including packages
# already installed in this build container. Real installer base may differ.
apt-get update
: > "$cache/empty-status"
mkdir -p "$cache/debs/partial"
mapfile -t packages < installer/packages.txt
apt-get -y --download-only --no-install-recommends \
    -o Dir::State::status="$cache/empty-status" \
    -o Dir::Cache::archives="$cache/debs" install "${packages[@]}"
cp "$cache/debs/"*.deb "$stage/nex/repo/"
# Host-local UI is a Nex package, not from Debian APT. Prefer CORE_LOCAL_DEB.
core_local_deb=${CORE_LOCAL_DEB:-}
if [[ -z $core_local_deb ]]; then
    shopt -s nullglob
    core_local_candidates=(/work/dist/core-local/nex-core-local_*_amd64.deb)
    shopt -u nullglob
    if [[ ${#core_local_candidates[@]} -eq 0 ]]; then
        echo 'nex-core-local .deb required: set CORE_LOCAL_DEB or place a package under dist/core-local/' >&2
        exit 1
    fi
    core_local_deb=$(ls -1t "${core_local_candidates[@]}" | head -n1)
fi
[[ -f $core_local_deb ]] || { echo "missing nex-core-local package: $core_local_deb" >&2; exit 1; }
cp -f "$core_local_deb" "$stage/nex/repo/"
printf 'nex-core-local\n' >> "$stage/nex/packages.txt"
(cd "$stage/nex/repo" && dpkg-scanpackages --multiversion . /dev/null > Packages)
printf 'package\tversion\tarchitecture\tsource\n' > "$stage/nex/package-manifest.tsv"
for deb in "$stage/nex/repo/"*.deb; do
    dpkg-deb --show --showformat='${Package}\t${Version}\t${Architecture}\t${Source}\n' "$deb"
done | sort >> "$stage/nex/package-manifest.tsv"
cp "$cache/SHA256SUMS" "$cache/SHA256SUMS.sign" "$cache/cd-key.asc" "$stage/nex/"
cat > "$stage/nex/BUILD.txt" <<BUILD
Nex Core 0.1.0-alpha1 / internal development image
Base: $base/$iso
Base SHA256: $expected
Debian signing fingerprint: $fingerprint
APT sources: Debian trixie, trixie-updates, trixie-security
Build UTC: $(date -u +%FT%TZ)
Nex source: AGPL-3.0-only; upstream packages retain their own licenses.
Not approved for public redistribution: source-compliance/release review pending.
Build recipe: installer/build.sh; installed packages: nex/package-manifest.tsv
Packages authenticated by Debian APT signed repository metadata during build.
Package versions reflect build-time trixie repositories; not bit-reproducible yet.
BUILD
./installer/build-gui.sh
mkdir -p "$stage/nex/docs"
cp LICENSE NOTICE docs/LICENSING.md "$stage/nex/docs/"
# Full corresponding source from this build (host always regenerates dist/nex-source.tar.gz),
# plus the installer recipe used to produce this ISO.
[[ -f /work/dist/nex-source.tar.gz ]] || { echo 'dist/nex-source.tar.gz required; host build must run scripts/build-source.py' >&2; exit 1; }
source_stage=$(mktemp -d)
tar -xzf /work/dist/nex-source.tar.gz -C "$source_stage"
[[ -d $source_stage/nex-source ]] || { echo 'dist/nex-source.tar.gz missing nex-source/ prefix' >&2; exit 1; }
rm -rf "$source_stage/nex-source/installer"
cp -a installer "$source_stage/nex-source/"
cp -f LICENSE NOTICE docs/LICENSING.md "$source_stage/nex-source/"
tar --sort=name --owner=0 --group=0 --numeric-owner -C "$source_stage" -czf "$stage/nex/nex-source.tar.gz" nex-source
rm -rf "$source_stage"
(cd "$stage/nex" && sha256sum nex-source.tar.gz > nex-source.tar.gz.sha256)
# Record that media source matches the regenerated archive inputs.
cp /work/dist/nex-source.tar.gz.sha256 "$stage/nex/nex-source.product.sha256"
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
menu title Nex Core 0.1.0-alpha1
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
  append initrd=/install.amd/initrd.gz preseed/file=/cdrom/preseed.cfg --- quiet
label serial
  menu label Install Nex Core (serial console ttyS0)
  kernel /install.amd/vmlinuz
  append initrd=/install.amd/initrd.gz preseed/file=/cdrom/preseed.cfg console=ttyS0,115200n8 --- console=ttyS0,115200n8
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
  linux /install.amd/vmlinuz preseed/file=/cdrom/preseed.cfg --- quiet
  initrd /install.amd/initrd.gz
}
menuentry 'Install Nex Core (serial console ttyS0)' {
  linux /install.amd/vmlinuz preseed/file=/cdrom/preseed.cfg console=ttyS0,115200n8 --- console=ttyS0,115200n8
  initrd /install.amd/initrd.gz
}
menuentry 'Debian rescue mode' {
  linux /install.amd/vmlinuz rescue/enable=true --- quiet
  initrd /install.amd/initrd.gz
}
GRUB
# Recompute media checksums without the boot catalog (regenerated by xorriso).
(cd "$stage" && find . -type f ! -name md5sum.txt ! -path './isolinux/boot.cat' ! -path './isolinux/isolinux.bin' -print0 | sort -z | xargs -0 md5sum > md5sum.txt)
rm -f "$output"
# Replay original hybrid BIOS/UEFI boot structure and signed EFI binaries.
xorriso -indev "$cache/$iso" -outdev "$output" \
    -boot_image any replay -update_r "$stage" / -volid NEX_CORE_010A1 -commit -end
cp "$stage/nex/package-manifest.tsv" /work/dist/package-manifest.tsv
cp "$stage/nex/BUILD.txt" /work/dist/BUILD.txt
(cd /work/dist && sha256sum "$(basename "$output")" > "$(basename "$output").sha256")
printf '\nBuilt %s\n' "$output"
