#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Build Debian Trixie's libvirt with Nex recovery snapshot safety fixes.
set -euo pipefail

readonly source_version=11.3.0-3+deb13u3
readonly local_version="${source_version}+nex4"
readonly output=/output
readonly source_dir=/build/source
readonly unpacked=/build/libvirt-${source_version}
readonly patch_path=/recipe/readonly-disk-apparmor.patch
readonly revert_patch_path=/recipe/unique-revert-overlay.patch
readonly partial_chain_patch_path=/recipe/partial-backing-chain.patch
readonly qmp_depth_patch_path=/recipe/qmp-json-depth.patch
readonly readonly_revert_patch_path=/recipe/preserve-readonly-revert-media.patch
readonly source_snapshot=${NEX_DEBIAN_SOURCE_SNAPSHOT:-https://snapshot.debian.org/archive/debian/20260920T000000Z/}
readonly security_snapshot=${NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT:-https://snapshot.debian.org/archive/debian-security/20260920T000000Z/}
readonly built_utc="$(date -u +%FT%TZ)"
readonly recipe_sha256="$(cd /recipe && LC_ALL=C find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"

snapshot_url_ok() {
    # Reject whitespace/control injection into sources.list; require an exact
    # immutable snapshot.debian.org archive stamp URL (trailing slash).
    [[ "$1" =~ ^https://snapshot\.debian\.org/archive/(debian|debian-security)/[0-9]{8}T[0-9]{6}Z/$ ]]
}
snapshot_url_ok "$source_snapshot" && snapshot_url_ok "$security_snapshot" || {
    echo 'NEX_DEBIAN_SOURCE_SNAPSHOT and NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT must be immutable https://snapshot.debian.org/archive/(debian|debian-security)/<stamp>/ URLs.' >&2
    exit 1
}

test -d "$output"
test -f "$patch_path"
test -f "$revert_patch_path"
test -f "$partial_chain_patch_path"
test -f "$qmp_depth_patch_path"
test -f "$readonly_revert_patch_path"
install -d -m 0755 "$source_dir" "$output/source" "$output/notices" "$output/recipe"
cp -a /recipe/. "$output/recipe/"
install -d -m 0755 /apt-cache/partial
exec > >(tee "$output/build.log") 2>&1

# The ISO builder and recovery CI share only the archive cache; serialize its
# writers, then release the lock before the CPU-heavy package build.
readonly apt_cache_lock=/apt-cache/recovery-libvirt-apt.lock

cat > /build/snapshot-sources.list <<SOURCES
deb ${source_snapshot} trixie main
deb ${source_snapshot} trixie-updates main
deb ${security_snapshot} trixie-security main
deb-src ${source_snapshot} trixie main
deb-src ${source_snapshot} trixie-updates main
deb-src ${security_snapshot} trixie-security main
SOURCES
readonly -a apt_snapshot=(
    -o Dir::Etc::sourcelist=/build/snapshot-sources.list
    -o Dir::Etc::sourceparts=-
    -o Acquire::Check-Valid-Until=false
    -o Dir::Cache::archives=/apt-cache
    -o Dir::Cache::archives::partial=/apt-cache/partial
)
flock "$apt_cache_lock" apt-get "${apt_snapshot[@]}" update
flock "$apt_cache_lock" apt-get "${apt_snapshot[@]}" install -y --no-install-recommends ca-certificates devscripts dpkg-dev fakeroot quilt
flock "$apt_cache_lock" apt-get "${apt_snapshot[@]}" build-dep -y --no-install-recommends "libvirt=${source_version}"

# The exact source and build dependencies come only from the immutable
# Debian/security snapshots used by the installer build.
cd "$source_dir"
flock "$apt_cache_lock" apt-get "${apt_snapshot[@]}" source --download-only "libvirt=${source_version}"
mapfile -t source_files < <(find . -maxdepth 1 -type f \( \
    -name "libvirt_${source_version}.dsc" -o \
    -name 'libvirt_11.3.0.orig.*' -o \
    -name "libvirt_${source_version}.debian.tar.*" \
\) -printf '%f\n' | sort)
[[ ${#source_files[@]} == 4 ]] || { echo 'unexpected Debian libvirt source artifact set' >&2; exit 1; }
for source in "${source_files[@]}"; do
    test -f "$source"
    install -m 0644 "$source" "$output/source/$source"
done

dpkg-source -x "libvirt_${source_version}.dsc" "$unpacked"
cd "$unpacked"
install -m 0644 "$patch_path" debian/patches/readonly-disk-apparmor.patch
install -m 0644 "$revert_patch_path" debian/patches/unique-revert-overlay.patch
install -m 0644 "$partial_chain_patch_path" debian/patches/partial-backing-chain.patch
install -m 0644 "$qmp_depth_patch_path" debian/patches/qmp-json-depth.patch
install -m 0644 "$readonly_revert_patch_path" debian/patches/preserve-readonly-revert-media.patch
for patch in readonly-disk-apparmor.patch unique-revert-overlay.patch partial-backing-chain.patch qmp-json-depth.patch preserve-readonly-revert-media.patch; do
    if grep -Fqx "$patch" debian/patches/series; then
        echo "$patch is already in Debian series" >&2
        exit 1
    fi
    printf '%s\n' "$patch" >> debian/patches/series
done
quilt push -a

# Record the local build in Debian metadata while retaining the untouched
# source artifacts above and the exact patched source package below.
DEBFULLNAME='Nex packaging' DEBEMAIL='noreply@example.invalid' \
    dch --newversion "$local_version" --distribution trixie \
        'Backport libvirt fixes for safe snapshot recovery operations.'
install -m 0644 debian/copyright "$output/notices/libvirt-copyright"

# Do not allow callers to claim success with Debian's test suite disabled.
case "${DEB_BUILD_OPTIONS:-}" in
    *nocheck*) echo 'DEB_BUILD_OPTIONS=nocheck is not allowed' >&2; exit 1 ;;
esac
DEB_BUILD_OPTIONS=parallel=4 dpkg-buildpackage -b -us -uc

cd /build
shopt -s nullglob
debs=(/build/*.deb)
[[ ${#debs[@]} -gt 0 ]] || { echo 'libvirt build produced no binary packages' >&2; exit 1; }
binary_manifest="$output/binary-packages.txt"
: > "$binary_manifest"
for package in "${debs[@]}"; do
    binary_version=$(dpkg-deb -f "$package" Version)
    binary_source=$(dpkg-deb -f "$package" Source)
    [[ $binary_version == "$local_version" ]] || {
        echo "unexpected libvirt binary version in $(basename "$package"): $binary_version" >&2
        exit 1
    }
    [[ $binary_source == libvirt || $binary_source == "libvirt ($local_version)" ]] || {
        echo "unexpected source package in $(basename "$package"): $binary_source" >&2
        exit 1
    }
    printf '%s %s %s\n' "$(dpkg-deb -f "$package" Package)" \
        "$binary_version" "$(dpkg-deb -f "$package" Architecture)" >> "$binary_manifest"
    install -m 0644 "$package" "$output/$(basename "$package")"
done
for metadata in libvirt_*_*.changes libvirt_*_*.buildinfo; do
    test -f "$metadata" && install -m 0644 "$metadata" "$output/$(basename "$metadata")"
done

cd "$unpacked"
dpkg-buildpackage -S -us -uc
cd /build
for source in "libvirt_${local_version}.dsc" "libvirt_${local_version}.debian.tar."*; do
    test -f "$source"
    install -m 0644 "$source" "$output/source/$source"
done

# This package is where Debian ships virt-aa-helper. Keep a source-test result
# in the evidence rather than treating successful compilation as sufficient.
helper_deb=$(find "$output" -maxdepth 1 -name "libvirt-daemon_${local_version}_*.deb" -type f -print -quit)
test -n "$helper_deb"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dpkg-deb -x "$helper_deb" "$work/root"
test -x "$work/root/usr/lib/libvirt/virt-aa-helper"
while IFS= read -r package; do
    package_deb=$(find "$output" -maxdepth 1 -name "${package}_*.deb" -type f -print -quit)
    test -n "$package_deb"
    dpkg-deb -x "$package_deb" "$work/root"
done < <(awk '$1 == "libvirt0" { print $1 }' "$binary_manifest")
libvirt_library=$(find "$work/root/usr/lib" -name 'libvirt.so.0' -print -quit)
test -n "$libvirt_library"
LD_LIBRARY_PATH="$(dirname "$libvirt_library")${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
python3 /recipe/libvirt_readonly_policy.py \
        --helper "$work/root/usr/lib/libvirt/virt-aa-helper"
grep -Eq '(^|[[:space:]])[0-9]+/[0-9]+.*virt-aa-helper.*(OK|PASS)' "$output/build.log" || {
    echo 'Debian build log lacks a passing virt-aa-helper Meson test result' >&2
    exit 1
}

printf '%s\n' \
    "source_package=libvirt" \
    "source_version=${source_version}" \
    "local_version=${local_version}" \
    'patch=upstream:7b889af2d235d3fadfc172f371f02df0b74e40f2' \
    'patch=local:unique-revert-overlay.patch' \
    'patch=local:partial-backing-chain.patch' \
    'patch=local:qmp-json-depth.patch' \
    'patch=local:preserve-readonly-revert-media.patch' \
    "debian_snapshot=${source_snapshot}" \
    "debian_security_snapshot=${security_snapshot}" \
    "build_arch=$(dpkg --print-architecture)" \
    "built_utc=${built_utc}" > "$output/build-inputs.txt"
printf '%s\n' "recipe_sha256=${recipe_sha256}" >> "$output/build-inputs.txt"

# Hash every retained immutable package/source/evidence file. The active build
# log is excluded because tee still has it open while this checksum is made.
(cd "$output" && find . -type f ! -name SHA256SUMS ! -name build.log \
    -printf '%P\0' | sort -z | xargs -0 sha256sum > SHA256SUMS)
