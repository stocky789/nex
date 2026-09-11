#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Build only: never mounts/writes physical disks or changes host networking.
set -euo pipefail
cd "$(dirname "$0")/.."

# Every ISO build rebuilds from current tree source:
#   1) corresponding source archive
#   2) Controller appliance (qcow with current nex-controller/UI)
#   3) nex-core-local package from that appliance + source
# Reusing an older CORE_LOCAL_DEB is intentionally not supported.
default_appliance_base=.cache/appliance/debian-13-genericcloud-amd64-20260831-2587.qcow2
default_appliance_sha256=85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3
default_signing_key=$HOME/.local/share/nex-dev/nex-appliance-signing.pem

rebuild_iso_artifacts() {
    local version=$1
    local docker_bin=${NEX_DOCKER:-docker}
    local signing_key=${NEX_APPLIANCE_SIGNING_KEY:-$default_signing_key}
    local base_image=${NEX_APPLIANCE_BASE_IMAGE:-$default_appliance_base}
    local base_sha=${NEX_APPLIANCE_BASE_SHA256:-$default_appliance_sha256}
    local appliance_out=.cache/nex-iso/appliance
    local trusted_key=.cache/nex-iso/release-public-key.raw
    local package_out=.cache/nex-iso/core-local
    [[ -f $signing_key ]] || { echo "appliance signing key missing: $signing_key (set NEX_APPLIANCE_SIGNING_KEY)" >&2; exit 1; }
    [[ -f $base_image ]] || { echo "Debian cloud base image missing: $base_image (set NEX_APPLIANCE_BASE_IMAGE)" >&2; exit 1; }
    python3 scripts/build-source.py
    rm -rf "$appliance_out"
    mkdir -p "$appliance_out"
    # Rebuilds dist/nex-source.tar.gz again and embeds that exact archive in the qcow.
    python3 appliance/build.py \
        --base-image "$base_image" \
        --base-sha256 "$base_sha" \
        --version "$version" \
        --signing-key "$signing_key" \
        --output "$appliance_out" \
        --docker "$docker_bin"
    install -m644 "$appliance_out/public-key.raw" "$trusted_key"
    # Use the same source snapshot the appliance just embedded — do not regenerate
    # again, or Core and Controller could diverge.
    product_source=$(sha256sum dist/nex-source.tar.gz | awk '{print $1}')
    appliance_source=$(sha256sum "$appliance_out/nex-source.tar.gz" | awk '{print $1}')
    [[ $product_source == "$appliance_source" ]] || {
        echo "dist/nex-source.tar.gz ($product_source) != appliance source ($appliance_source)" >&2
        exit 1
    }
    rm -rf "$package_out"
    mkdir -p "$package_out"
    python3 packaging/core-local/build.py \
        --source dist/nex-source.tar.gz \
        --bundle "$appliance_out" \
        --trusted-key "$trusted_key" \
        --version "$version" \
        --output "$package_out" \
        --docker "$docker_bin"
    mkdir -p dist/core-local dist/appliance
    cp -f "$package_out/nex-core-local_${version}_amd64.deb" "dist/core-local/"
    rm -rf dist/appliance/*
    cp -a "$appliance_out"/. dist/appliance/
}

assert_iso_artifacts_current() {
    local deb=$1
    local want=$2
    local work extract appliance_version package_source product_source appliance_source
    work=$(mktemp -d)
    extract=$work/pkg
    dpkg-deb -x "$deb" "$extract"
    appliance_version=$(python3 -c "import json; print(json.load(open('$extract/usr/share/nex/appliance/manifest.json'))['version'])")
    package_source=$(sha256sum "$extract/usr/share/nex/core-local/nex-source.tar.gz" | awk '{print $1}')
    product_source=$(sha256sum dist/nex-source.tar.gz | awk '{print $1}')
    appliance_source=$(sha256sum "$extract/usr/share/nex/appliance/nex-source.tar.gz" | awk '{print $1}')
    rm -rf "$work"
    [[ $appliance_version == "$want" ]] || {
        echo "Controller appliance in package is $appliance_version; expected $want from this ISO build." >&2
        exit 1
    }
    [[ $package_source == "$product_source" ]] || {
        echo "Core package source digest $package_source does not match dist/nex-source.tar.gz $product_source." >&2
        exit 1
    }
    # Appliance source is captured when the qcow is built; it must still match the
    # product source used for this ISO (no intervening tree drift).
    [[ $appliance_source == "$product_source" ]] || {
        echo "Controller appliance source digest $appliance_source does not match dist/nex-source.tar.gz $product_source." >&2
        echo "Rebuild refused: Core and Controller must share the same current source snapshot." >&2
        exit 1
    }
}

if [[ ${1:-} != --inside ]]; then
    mkdir -p .cache/nex-iso dist
    # One version identifies a release. Default to the newest CHANGELOG heading so an
    # local build is not stamped with a stale constant; the release workflow passes
    # the same value explicitly.
    version=${NEX_VERSION:-$(python3 "$(dirname "$0")/../scripts/release-version.py" --current)}
    [[ -n $version ]] || { echo 'could not resolve a build version' >&2; exit 1; }
    docker_bin=${NEX_DOCKER:-docker}
    if [[ -n ${CORE_LOCAL_DEB:-} || ${NEX_SKIP_APPLIANCE_REBUILD:-} == 1 ]]; then
        echo 'ISO builds always rebuild Core and Controller from current source.' >&2
        echo 'Unset CORE_LOCAL_DEB and NEX_SKIP_APPLIANCE_REBUILD.' >&2
        exit 1
    fi
    rebuild_iso_artifacts "$version"
    iso_core_local=.cache/nex-iso/core-local/nex-core-local_${version}_amd64.deb
    assert_iso_artifacts_current "$iso_core_local" "$version"
    "$docker_bin" build -t nex-iso-builder -f installer/Dockerfile installer
    exec "$docker_bin" run --rm --platform linux/amd64 \
        -v "$PWD:/work" -e BUILD_UID="$(id -u)" -e BUILD_GID="$(id -g)" \
        -e "CORE_LOCAL_DEB=/work/${iso_core_local#"$PWD"/}" \
        -e "NEX_ISO_VERSION=$version" \
        nex-iso-builder
fi
[[ $(dpkg --print-architecture) == amd64 ]]
cache=/work/.cache/nex-iso
stage=$cache/stage
# The ISO carries exactly one Nex Core version: the nex-core-local package it installs.
core_local_deb=${CORE_LOCAL_DEB:?nex-core-local package path required}
[[ -f $core_local_deb ]] || { echo "missing nex-core-local package: $core_local_deb" >&2; exit 1; }
version=$(dpkg-deb -f "$core_local_deb" Version)
output=/work/dist/nex-core-${version}-amd64.iso
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
    # Resume across transient TLS/network drops; --retry alone does not cover mid-transfer EOFs.
    curl -fL --retry 5 --retry-all-errors --retry-delay 2 --continue-at - \
      "$base/$iso" -o "$cache/$iso.partial"
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
# Host-local UI is a Nex package, not from Debian APT; CORE_LOCAL_DEB was validated above.
cp -f "$core_local_deb" "$stage/nex/repo/"
printf 'nex-core-local\n' >> "$stage/nex/packages.txt"
(cd "$stage/nex/repo" && dpkg-scanpackages --multiversion . /dev/null > Packages)
printf 'package\tversion\tarchitecture\tsource\n' > "$stage/nex/package-manifest.tsv"
for deb in "$stage/nex/repo/"*.deb; do
    dpkg-deb --show --showformat='${Package}\t${Version}\t${Architecture}\t${Source}\n' "$deb"
done | sort >> "$stage/nex/package-manifest.tsv"
cp "$cache/SHA256SUMS" "$cache/SHA256SUMS.sign" "$cache/cd-key.asc" "$stage/nex/"
cat > "$stage/nex/BUILD.txt" <<BUILD
Nex Core $version / internal development image
Base: $base/$iso
Base SHA256: $expected
Debian signing fingerprint: $fingerprint
APT sources: Debian trixie, trixie-updates, trixie-security
Build UTC: $(date -u +%FT%TZ)
Nex package version: ${NEX_ISO_VERSION:-unknown}
Nex source: AGPL-3.0-only; upstream packages retain their own licenses.
Core and Controller on this media were rebuilt from the same corresponding-source
snapshot for this ISO build (see nex-source.product.sha256).
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
    -boot_image any replay -update_r "$stage" / -volid NEX_CORE -commit -end
cp "$stage/nex/package-manifest.tsv" /work/dist/package-manifest.tsv
cp "$stage/nex/BUILD.txt" /work/dist/BUILD.txt
(cd /work/dist && sha256sum "$(basename "$output")" > "$(basename "$output").sha256")
printf '\nBuilt %s\n' "$output"
