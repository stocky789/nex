# SPDX-License-Identifier: AGPL-3.0-only
# Same pin as image/tools/Dockerfile: the base image by digest, every package by the snapshot date build.py passes
# (image/snapshot). Without SNAPSHOT the build would reach the live archive, so it is required.
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
    && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "3";' > /etc/apt/apt.conf.d/10snapshot \
    && apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev libpam0g-dev python3 dpkg-dev openssl ca-certificates xz-utils && rm -rf /var/lib/apt/lists/*
# Pin the supported upstream compiler; C compilation still uses Debian gcc/libc.
RUN python3 -c 'import urllib.request; urllib.request.urlretrieve("https://go.dev/dl/go1.27.1.linux-amd64.tar.gz", "/tmp/go.tar.gz")' && echo "63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445  /tmp/go.tar.gz" | sha256sum -c - && tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz
# The web build's Node and npm: one upstream archive instead of Debian's npm, which drags in about 300 node-* packages.
RUN python3 -c 'import urllib.request; urllib.request.urlretrieve("https://nodejs.org/dist/v22.23.3/node-v22.23.3-linux-x64.tar.xz", "/tmp/node.tar.xz")' && echo "df450af89261115ef9f9e3830c3eeb2cc9213b63c720b1af623cb5dcbe2e02de  /tmp/node.tar.xz" | sha256sum -c - && tar -C /usr/local --strip-components=1 -xJf /tmp/node.tar.xz && rm /tmp/node.tar.xz
ENV PATH="/usr/local/go/bin:${PATH}" GOTOOLCHAIN=local
COPY build-inside.sh /usr/local/bin/build-nex-core-local
ENTRYPOINT ["/usr/local/bin/build-nex-core-local"]
