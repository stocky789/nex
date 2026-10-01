# Throwaway Phase I-0 build tools: Debian 13 container so any workstation distribution works.
FROM debian:trixie
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      mkosi systemd systemd-ukify systemd-boot systemd-boot-efi-amd64-signed systemd-repart systemd-container \
      erofs-utils cryptsetup-bin sbsigntool python3-pefile python3-virt-firmware openssl \
      dosfstools mtools fdisk gdisk util-linux kmod \
      debian-archive-keyring ca-certificates curl xz-utils zstd cpio e2fsprogs squashfs-tools \
      diffoscope-minimal bubblewrap dpkg-dev apt-utils python3 \
      shim-signed shim-helpers-amd64-signed binutils qemu-utils \
    && rm -rf /var/lib/apt/lists/*
