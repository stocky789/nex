#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Source-level installation safety check; actual offline installation is separate.
set -euo pipefail
cd "$(dirname "$0")/.."
bash -n installer/build.sh
sh -n installer/late-install.sh
python3 - <<'PY'
from pathlib import Path

entries = [line.split(None, 3) for line in Path('installer/preseed.cfg').read_text().splitlines()
           if line.strip() and not line.lstrip().startswith('#')]
for entry in entries:
    assert len(entry) >= 3, f'Malformed preseed entry: {entry}'
    key = entry[1]
    # The shipped image must not answer identity, networking or disk questions.
    assert not key.startswith(('partman', 'passwd/', 'netcfg/', 'grub-installer/')), key
    assert key not in ('preseed/early_command', 'preseed/run', 'auto-install/enable'), key

packages = Path('installer/packages.txt').read_text().split()
assert len(packages) == len(set(packages)), 'Duplicate package'
assert {'qemu-system-x86', 'libvirt-daemon-system', 'libvirt-clients', 'openssh-server'} <= set(packages)
assert all(p.replace('-', '').isalnum() for p in packages), 'Unexpected package argument'
late = Path('installer/late-install.sh').read_text()
assert 'Dir::Etc::sourceparts=-' in late and 'file:/var/cache/nex-install/repo' in late
assert 'PermitRootLogin no' in late
# BUG-0004: raw chroot inherits the installer's stale debconf descriptors.
assert '\nin-target /bin/sh /var/cache/nex-install/configure.sh\n' in late, 'Use Debian in-target for package configuration'
assert '\nchroot /target ' not in late, 'Raw chroot bypasses installer setup'
assert 'dpkg-divert --local --add --rename --divert /etc/os-release.debian /etc/os-release' in late
assert 'ID=nex-core\nID_LIKE=debian\n' in late
assert 'DEBIAN_CODENAME=trixie' in late
assert '> /usr/lib/os-release' not in late, 'Preserve upstream release metadata'
print('PASS: shell syntax, interactive disk/account/network contract, package inputs, offline APT scope')
PY
