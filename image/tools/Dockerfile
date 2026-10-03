# Build tools for the Nex Core image: Debian 13 in a container so any workstation distribution works.
# Pinned twice: the base image by digest, every package by the snapshot archive date (build.sh passes
# image/snapshot as SNAPSHOT). Nothing here reaches the live archive.
FROM debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
ARG SNAPSHOT
ENV DEBIAN_FRONTEND=noninteractive
RUN test -n "$SNAPSHOT" \
    && rm -f /etc/apt/sources.list /etc/apt/sources.list.d/* \
    && printf '%s\n' \
      'Types: deb' \
      "URIs: http://snapshot.debian.org/archive/debian/$SNAPSHOT/" \
      'Suites: trixie trixie-updates' \
      'Components: main' \
      'Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg' \
      '' \
      'Types: deb' \
      "URIs: http://snapshot.debian.org/archive/debian-security/$SNAPSHOT/" \
      'Suites: trixie-security' \
      'Components: main' \
      'Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg' \
      > /etc/apt/sources.list.d/snapshot.sources \
    && echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/10snapshot \
    && apt-get update && apt-get install -y --no-install-recommends \
      mkosi systemd systemd-ukify systemd-boot systemd-boot-efi-amd64-signed systemd-repart systemd-container \
      erofs-utils cryptsetup-bin sbsigntool python3-pefile python3-virt-firmware openssl \
      dosfstools mtools fdisk gdisk util-linux kmod \
      debian-archive-keyring ca-certificates curl xz-utils zstd cpio e2fsprogs \
      diffoscope-minimal bubblewrap dpkg-dev apt-utils python3 \
      shim-signed shim-helpers-amd64-signed binutils qemu-utils udev mdadm reprepro \
    && rm -rf /var/lib/apt/lists/*
