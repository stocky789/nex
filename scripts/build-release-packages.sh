#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Build the Nex-built Debian packages that the Nex Core image installs besides nex-core-local:
# the matched libvirt (recovery snapshot safety fixes) and the matched dlm-controld. Build only.
#
#   scripts/build-release-packages.sh OUTPUT_DIR
#
# OUTPUT_DIR (repository-relative) receives the .deb files, the libvirt source and notices, and the
# build records. Each build keeps a digest of everything it reads beside its output and is reused
# when the digest still matches. NEX_DOCKER selects the docker command (scripts/ci-docker.sh on a
# runner whose job runs in a container). NEX_DEBIAN_SOURCE_SNAPSHOT and
# NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT pin the Debian sources.
set -euo pipefail
cd "$(dirname "$0")/.."

output_root=${1:?usage: build-release-packages.sh OUTPUT_DIR}
docker_bin=${NEX_DOCKER:-docker}
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
mkdir -p "$output_root"

# Digest of everything a build reads: its snapshot pins, then its recipe files.
inputs_digest() {
    { printf '%s\n' "$1"; shift; find "$@" -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum; } | sha256sum | cut -d' ' -f1
}

build_matched_dlm() {
    local output=$output_root/dlm digest deb
    digest=$(inputs_digest '' packaging/cluster-storage)
    if [[ $(cat "$output.inputs" 2>/dev/null) == "$digest" ]]; then
        echo 'reusing the matched dlm-controld build: its inputs are unchanged' >&2
    else
        rm -rf "$output" "$output.inputs"
        mkdir -p "$output"
        "$docker_bin" build -t nex-dlm-pacemaker-abi -f packaging/cluster-storage/Dockerfile packaging/cluster-storage >&2
        "$docker_bin" run --rm -v "$PWD/$output:/output" nex-dlm-pacemaker-abi >&2
    fi
    deb=$(find "$output" -maxdepth 1 -type f -name 'dlm-controld_4.3.0-3+nex1_amd64.deb' -print -quit)
    [[ -n $deb ]] || { echo 'matched dlm-controld build did not produce its pinned package' >&2; exit 1; }
    [[ $(dpkg-deb -f "$deb" Package) == dlm-controld ]]
    dpkg-deb -f "$deb" Depends | grep -Fq 'libstonithd56 (= 3.0.0-2)'
    printf '%s\n' "$digest" > "$output.inputs"
}

build_matched_libvirt() {
    local output=$output_root/libvirt digest
    [[ -f tests/integration/libvirt_readonly_policy.py ]] || {
        echo 'matched libvirt helper-policy regression is missing' >&2
        exit 1
    }
    digest=$(inputs_digest "$source_snapshot $security_snapshot" packaging/libvirt tests/integration/libvirt_readonly_policy.py)
    if [[ $(cat "$output.inputs" 2>/dev/null) == "$digest" ]]; then
        echo 'reusing the matched libvirt build: its inputs are unchanged' >&2
    else
        rm -rf "$output" "$output.inputs"
        mkdir -p "$output"
        (
            recipe=$(mktemp -d "$output_root/libvirt-recipe.XXXXXX")
            trap 'rm -rf "$recipe"' EXIT
            cp -a packaging/libvirt/. "$recipe/"
            cp tests/integration/libvirt_readonly_policy.py "$recipe/"
            "$docker_bin" run --rm --init --platform linux/amd64 \
                -v nex-build-cache:/apt-cache \
                -v "$PWD/$recipe:/recipe:ro" -v "$PWD/$output:/output" \
                -e "NEX_DEBIAN_SOURCE_SNAPSHOT=$source_snapshot" \
                -e "NEX_DEBIAN_SECURITY_SOURCE_SNAPSHOT=$security_snapshot" \
                debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 \
                /recipe/build-matched-libvirt.sh >&2
        )
    fi
    [[ -f $output/SHA256SUMS && -f $output/binary-packages.txt && -f $output/source/libvirt_11.3.0-3+deb13u3+nex4.dsc ]] || {
        echo 'matched libvirt build is missing its verified source or binary manifest' >&2
        exit 1
    }
    (cd "$output" && sha256sum --check SHA256SUMS) >&2
    [[ $(grep -Fxc 'local_version=11.3.0-3+deb13u3+nex4' "$output/build-inputs.txt") == 1 ]] || {
        echo 'matched libvirt build has an unexpected local version' >&2
        exit 1
    }
    printf '%s\n' "$digest" > "$output.inputs"
}

build_matched_dlm
build_matched_libvirt
