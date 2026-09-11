#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Libvirt QEMU hook: refuse a guest start if a Nex-owned repository is unavailable.
set -eu

# qemu.d hooks receive: domain name, operation, sub-operation, extra argument.
[ "${2:-}" = prepare ] && [ "${3:-}" = begin ] || exit 0
export LC_ALL=C
exec /usr/lib/nex/nex-core-local storage-guard --state-dir /var/lib/nex-core-agent
