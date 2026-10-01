# Pinned builder for the nex-zfs kernel modules (plan: ZFS module and Secure Boot).
# Compiler, headers and DKMS exist only here, never on a host or in a slot.
# scripts/build-zfs-modules.sh supplies every ARG below so two builds see the same package bytes:
# APT_SOURCES is the exact sources.list (snapshot URLs, or a configured private mirror in CI).
ARG BUILDER_BASE=debian@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
FROM ${BUILDER_BASE}
ARG KVER
ARG ZFS_DKMS_VERSION
COPY sources.list /etc/nex-builder-sources.list
ENV DEBIAN_FRONTEND=noninteractive LC_ALL=C TZ=UTC
# ca-certificates comes from the base image's own mirror (https needs it); it never enters a build output.
RUN set -eu; test -n "$KVER"; \
    apt-get update; apt-get install -y --no-install-recommends ca-certificates; \
    rm -f /etc/apt/sources.list.d/debian.sources; \
    cp /etc/nex-builder-sources.list /etc/apt/sources.list; \
    printf 'Acquire::Retries "5";\nAcquire::Check-Valid-Until "false";\nAPT::Install-Recommends "false";\n' > /etc/apt/apt.conf.d/99nex; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      build-essential dkms kmod xz-utils zstd openssl \
      "linux-headers-${KVER}" "zfs-dkms${ZFS_DKMS_VERSION:+=$ZFS_DKMS_VERSION}"; \
    rm -rf /var/lib/apt/lists/*
