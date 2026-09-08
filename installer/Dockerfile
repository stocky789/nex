# SPDX-License-Identifier: AGPL-3.0-only
FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg gpgv xorriso dpkg-dev python3 apt-utils \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /work
ENTRYPOINT ["bash", "/work/installer/build.sh", "--inside"]
