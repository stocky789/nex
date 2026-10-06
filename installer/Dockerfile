# SPDX-License-Identifier: AGPL-3.0-only
FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
RUN sed -i 's/^Components: main$/Components: main contrib/' /etc/apt/sources.list.d/debian.sources
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg gpgv xorriso dpkg-dev python3 cpio libarchive-tools erofs-utils \
    build-essential autoconf automake pkg-config libdebian-installer4-dev libgtk2.0-dev tzdata \
    && rm -rf /var/lib/apt/lists/*
RUN printf 'deb-src http://deb.debian.org/debian trixie main contrib\n' > /etc/apt/sources.list.d/nex-build-source.list
WORKDIR /work
ENTRYPOINT ["bash", "/work/installer/build.sh", "--inside"]
