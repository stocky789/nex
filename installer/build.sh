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

build_matched_dlm() {
    local docker_bin=$1 output=.cache/nex-iso/cluster-storage-dlm deb
    rm -rf "$output"
    mkdir -p "$output"
    "$docker_bin" build -t nex-dlm-pacemaker-abi -f packaging/cluster-storage/Dockerfile packaging/cluster-storage >&2
    "$docker_bin" run --rm -v "$PWD/$output:/output" nex-dlm-pacemaker-abi >&2
    deb=$(find "$output" -maxdepth 1 -type f -name 'dlm-controld_4.3.0-3+nex1_amd64.deb' -print -quit)
    [[ -n $deb ]] || { echo 'matched dlm-controld build did not produce its pinned package' >&2; exit 1; }
    [[ $(dpkg-deb -f "$deb" Package) == dlm-controld ]]
    dpkg-deb -f "$deb" Depends | grep -Fq 'libstonithd56 (= 3.0.0-2)'
    printf '%s\n' "$deb"
}

ensure_perl_exact_source() {
    # Base installer / appliance still ship perl 5.40.1-6 while newer snapshot
    # indices prefer 5.40.1-6+deb13u1. apt-get source then substitutes and the
    # retained-source gate fails. Pin the exact dsc and tarballs from the
    # snapshot where 5.40.1-6 last appeared.
    local dest=$cache/perl-exact-source
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
    local fetch=$cache/virtio-win
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
    gzip -9n -c "$fetch/$iso_name" > "$stage/nex/media/virtio-win.iso.gz"
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

stage_ovmf_generic() {
    # OVMF's 2023 Microsoft certificates are not available in trixie. Keep sid
    # isolated to this exact signed package and its matching edk2 source; it is
    # never an APT candidate for the rest of the image.
    local version=2026.05-2
    local binary=ovmf-generic_${version}_all.deb
    local binary_sha256=0f2d4de080ebeaf0972c5e212253c28dd2d90aa641542f42f37c6d6ecd4c67eb
    local source=edk2_${version}
    local upstream_source=edk2_2026.05
    local apt_root=$cache/ovmf-generic-sid
    local fetch
    mkdir -p "$apt_root/lists/partial" "$apt_root/archives/partial"
    cat > "$apt_root/sources.list" <<'SOURCES'
deb https://deb.debian.org/debian sid main
deb-src https://deb.debian.org/debian sid main
SOURCES
    local -a sid_apt=(
        -o "Dir::Etc::sourcelist=$apt_root/sources.list"
        -o Dir::Etc::sourceparts=-
        -o "Dir::State::lists=$apt_root/lists"
        -o "Dir::Cache::archives=$apt_root/archives"
    )
    apt-get "${sid_apt[@]}" update
    fetch=$(mktemp -d "$apt_root/fetch.XXXXXX")
    (
        cd "$fetch"
        apt-get "${sid_apt[@]}" download "ovmf-generic=$version"
        apt-get "${sid_apt[@]}" source "edk2=$version"
    )
    test -f "$fetch/$binary"
    printf '%s  %s\n' "$binary_sha256" "$fetch/$binary" | sha256sum --check
    grep -Fx "Version: $version" "$fetch/$source.dsc"
    grep -Fq 'b2495b2473d620240b2c8ece10f2c8b75771a8f9cab58573b74de8d4d01c6851 61298772 edk2_2026.05.orig.tar.xz' "$fetch/$source.dsc"
    grep -Fq '460a8c66db4c18e070612ea51f15a1cf71679653ccb9bf4414f56259d1107870 76388 edk2_2026.05-2.debian.tar.xz' "$fetch/$source.dsc"
    local extract=$fetch/extract
    dpkg-deb -x "$fetch/$binary" "$extract"
    # This is the enrolled template selected by libvirt for Core's explicit
    # enrolled-keys Secure Boot feature. Keep the standard descriptor paths.
    local descriptor=$extract/usr/share/qemu/firmware/40-edk2-x86_64-secure-enrolled.json
    test -f "$descriptor"
    grep -Fq '/usr/share/OVMF/OVMF_CODE_4M.ms.fd' "$descriptor"
    grep -Fq '/usr/share/OVMF/OVMF_VARS_4M.ms.fd' "$descriptor"
    test -f "$extract/usr/share/OVMF/OVMF_CODE_4M.ms.fd"
    test -f "$extract/usr/share/OVMF/OVMF_VARS_4M.ms.fd"
    grep -aFq 'Microsoft UEFI CA 2023' "$extract/usr/share/OVMF/OVMF_VARS_4M.ms.fd"
    grep -aFq 'Windows UEFI CA 2023' "$extract/usr/share/OVMF/OVMF_VARS_4M.ms.fd"
    cp "$fetch/$binary" "$stage/nex/repo/"
    local third_party=$stage/nex/third-party/ovmf-generic
    mkdir -p "$third_party/source"
    cp "$fetch/$source.dsc" "$fetch/$upstream_source.orig.tar.xz" "$fetch/$source.debian.tar.xz" "$third_party/source/"
    cp "$extract/usr/share/doc/ovmf-generic/copyright" "$third_party/"
    (
        cd "$third_party"
        sha256sum copyright source/* > checksums.sha256
        cat > manifest.tsv <<MANIFEST
component\tversion\tbinary\tbinary_sha256\tsource
ovmf-generic\t$version\t$binary\t$binary_sha256\tedk2 $version
MANIFEST
    )
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
    # Keep binary downloads and source-bundle fetches on the same immutable
    # snapshots. Live deb.debian.org advanced past 20260916 with unbound
    # 1.26.1-0+deb13u1 (first on snapshot 2026-09-19) and release run 4223
    # failed apt-get source for a version the pinned sources did not have.
    source_snapshot=${NEX_DEBIAN_SOURCE_SNAPSHOT:-https://snapshot.debian.org/archive/debian/20260920T000000Z/}
    security_snapshot=${NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT:-https://snapshot.debian.org/archive/debian-security/20260920T000000Z/}
    [[ $source_snapshot == https://snapshot.debian.org/archive/* && $security_snapshot == https://snapshot.debian.org/archive/* ]] || {
        echo 'NEX_DEBIAN_SOURCE_SNAPSHOT and NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT must be immutable snapshot.debian.org archive URLs.' >&2
        exit 1
    }
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
    matched_dlm_deb=$(build_matched_dlm "$docker_bin")
    iso_core_local=.cache/nex-iso/core-local/nex-core-local_${version}_amd64.deb
    assert_iso_artifacts_current "$iso_core_local" "$version"
    "$docker_bin" build -t nex-iso-builder -f installer/Dockerfile installer
    exec "$docker_bin" run --rm --platform linux/amd64 \
        -v "$PWD:/work" -e BUILD_UID="$(id -u)" -e BUILD_GID="$(id -g)" \
        -e "CORE_LOCAL_DEB=/work/${iso_core_local#"$PWD"/}" \
        -e "MATCHED_DLM_DEB=/work/${matched_dlm_deb#"$PWD"/}" \
        -e "NEX_DEBIAN_SOURCE_SNAPSHOT=$source_snapshot" \
        -e "NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT=$security_snapshot" \
        -e "NEX_ISO_VERSION=$version" \
        nex-iso-builder
fi
[[ $(dpkg --print-architecture) == amd64 ]]
cache=/work/.cache/nex-iso
stage=$cache/stage
source_snapshot=${NEX_DEBIAN_SOURCE_SNAPSHOT:?NEX_DEBIAN_SOURCE_SNAPSHOT required}
security_snapshot=${NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT:?NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT required}
# The ISO carries exactly one Nex Core version: the nex-core-local package it installs.
core_local_deb=${CORE_LOCAL_DEB:?nex-core-local package path required}
[[ -f $core_local_deb ]] || { echo "missing nex-core-local package: $core_local_deb" >&2; exit 1; }
matched_dlm_deb=${MATCHED_DLM_DEB:?matched dlm-controld package path required}
[[ -f $matched_dlm_deb ]] || { echo "missing matched dlm-controld package: $matched_dlm_deb" >&2; exit 1; }
[[ $(dpkg-deb -f "$matched_dlm_deb" Package) == dlm-controld ]]
[[ $(dpkg-deb -f "$matched_dlm_deb" Version) == 4.3.0-3+nex1 ]]
dpkg-deb -f "$matched_dlm_deb" Depends | grep -Fq 'libstonithd56 (= 3.0.0-2)'
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
# These optional firmware packages are outside the reviewed server-hardware
# scope. Remove their pool copies and repository metadata together.
python3 scripts/prune-iso-packages.py --tree "$stage" \
    dahdi-firmware-nonfree atmel-firmware bluez-firmware firmware-sof-signed \
    firmware-b43-installer firmware-b43legacy-installer
# Both frontends must exist on the pinned media before advertising the choice.
for image in install.amd/vmlinuz install.amd/initrd.gz install.amd/gtk/initrd.gz; do
    test -s "$stage/$image"
done
# Late install restores the compressed VirtIO ISO before APT installs anything.
# Both installer frontends must therefore provide BusyBox's gunzip applet.
for initrd in install.amd/initrd.gz install.amd/gtk/initrd.gz; do
    initrd_root=$(mktemp -d)
    (cd "$initrd_root" && gzip -dc "$stage/$initrd" | cpio -id --quiet usr/bin/busybox)
    "$initrd_root/usr/bin/busybox" --list | grep -Fx gunzip
    rm -rf "$initrd_root"
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
# Resolve the offline closure from the same immutable snapshots used for the
# corresponding-source archive. Live trixie/security moves ahead of those pins
# and leaves apt-get source unable to find the versions on the media.
cat > "$cache/binary-sources.list" <<SOURCES
deb $source_snapshot trixie main contrib non-free non-free-firmware
deb $source_snapshot trixie-updates main contrib non-free non-free-firmware
deb $security_snapshot trixie-security main contrib non-free non-free-firmware
SOURCES
apt_snapshot=(
    -o "Dir::Etc::sourcelist=$cache/binary-sources.list"
    -o 'Dir::Etc::sourceparts=-'
    -o 'Acquire::Check-Valid-Until=false'
)
apt-get "${apt_snapshot[@]}" update
: > "$cache/empty-status"
deb_cache=$(mktemp -d "$cache/debs.XXXXXX")
mkdir -p "$deb_cache/partial"
mapfile -t packages < installer/packages.txt
apt_packages=()
for package in "${packages[@]}"; do
    [[ $package == ovmf-generic ]] || apt_packages+=("$package")
done
apt-get -y --download-only --no-install-recommends \
    "${apt_snapshot[@]}" \
    -o Dir::State::status="$cache/empty-status" \
    -o Dir::Cache::archives="$deb_cache" install "${apt_packages[@]}"
mapfile -t downloaded_debs < <(find "$deb_cache" -maxdepth 1 -type f -name '*.deb' -printf '%f\n' | sort)
((${#downloaded_debs[@]})) || { echo 'APT did not download the requested offline package closure' >&2; exit 1; }
for deb in "${downloaded_debs[@]}"; do
    cp "$deb_cache/$deb" "$stage/nex/repo/"
done
rm -rf "$deb_cache"
stage_ovmf_generic
stage_virtio_win
# Keep only the DLM rebuilt against this media's Pacemaker fencing ABI.
rm -f "$stage/nex/repo"/dlm-controld_*.deb
cp -f "$matched_dlm_deb" "$stage/nex/repo/"
runtime_version=$(dpkg-deb -f "$matched_dlm_deb" Depends | sed -n 's/.*libstonithd56 (= \([^)]*\)).*/\1/p')
[[ -n $runtime_version ]]
for deb in "$stage/nex/repo"/libstonithd56_*.deb; do
    [[ $(dpkg-deb -f "$deb" Version) == "$runtime_version" ]] || {
        echo "APT closure libstonithd56 does not match custom DLM runtime pin $runtime_version" >&2
        exit 1
    }
done
dlm_source=$(dirname "$matched_dlm_deb")
[[ -f $dlm_source/build.log && -f $dlm_source/source/dlm_4.3.0-3.dsc && -f $dlm_source/notices/dlm-copyright ]]
mkdir -p "$stage/nex/third-party"
cp -a "$dlm_source" "$stage/nex/third-party/dlm-pacemaker-abi"
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
APT sources: immutable snapshot.debian.org pins ($source_snapshot, $security_snapshot);
isolated Debian sid for ovmf-generic 2026.05-2 only
Bundled media: Fedora virtio-win 0.1.302 with matching Fedora corresponding-source RPM
Build UTC: $(date -u +%FT%TZ)
Nex package version: ${NEX_ISO_VERSION:-unknown}
Nex source: AGPL-3.0-only; upstream packages retain their own licenses.
Core and Controller on this media were rebuilt from the same corresponding-source
snapshot for this ISO build (see nex-source.product.sha256).
Public redistribution requires the matching source bundle and compliance manifest.
Build recipe: installer/build.sh; installed packages: nex/package-manifest.tsv
Packages authenticated by Debian APT signed repository metadata during build.
Package versions follow the pinned snapshots above (not live trixie).
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
# A public ISO must have one retained bundle covering the base installer, all
# added packages, rebuilt packages, appliance closure, notices, and exact hashes.
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
    --version "$version" --iso "$cache/$iso" --tree "$stage" --repository "$stage/nex/repo" \
    --apt-sources "$cache/source-sources.list" \
    --source-input "$cache/virtio-win-retained-source" --source-input "$stage/nex/third-party" --source-input "$stage/nex/nex-source.tar.gz" \
    --source-input "$cache/perl-exact-source" \
    --output "/work/dist/nex-core-${version}-source.tar.gz"
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
xorriso -indev "$cache/$iso" -outdev "$output" \
    -boot_image any replay -update_r "$stage" / -volid NEX_CORE -commit -end
cp "$stage/nex/package-manifest.tsv" /work/dist/package-manifest.tsv
python3 /work/scripts/build-source-bundle.py \
    --version "$version" --iso "$output" --tree "$stage" --repository "$stage/nex/repo" \
    --source-bundle "/work/dist/nex-core-${version}-source.tar.gz" \
    --compliance-manifest "/work/dist/nex-core-${version}-compliance-manifest.json"
(cd /work/dist && sha256sum "nex-core-${version}-compliance-manifest.json" > "nex-core-${version}-compliance-manifest.json.sha256")
cp "$stage/nex/BUILD.txt" /work/dist/BUILD.txt
(cd /work/dist && sha256sum "$(basename "$output")" > "$(basename "$output").sha256")
printf '\nBuilt %s\n' "$output"
