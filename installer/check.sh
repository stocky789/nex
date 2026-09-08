#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Source-level installation safety check; actual offline installation is separate.
set -euo pipefail
cd "$(dirname "$0")/.."
bash -n installer/build.sh
sh -n installer/late-install.sh
sh -n installer/root-account.sh
python3 - <<'PY'
from pathlib import Path

entries = [line.split(None, 3) for line in Path('installer/preseed.cfg').read_text().splitlines()
           if line.strip() and not line.lstrip().startswith('#')]
account_policy = {'passwd/root-login': 'true', 'passwd/make-user': 'false'}
for key, value in account_policy.items():
    assert entries.count(['d-i', key, 'boolean', value]) == 1, f'Required account policy: {key}={value}'
for entry in entries:
    assert len(entry) >= 3, f'Malformed preseed entry: {entry}'
    key = entry[1]
    # Only the root-only policy is preseeded; passwords must remain interactive.
    if key.startswith('passwd/'):
        assert key in account_policy and entry == ['d-i', key, 'boolean', account_policy[key]], entry
    assert not key.startswith(('partman', 'netcfg/', 'grub-installer/')), key
    if key == 'preseed/early_command':
        assert entry == ['d-i', key, 'string', '/bin/sh /cdrom/nex/root-account.sh'], entry
    assert key not in ('preseed/run', 'auto-install/enable'), key
assert entries.count(['d-i', 'preseed/early_command', 'string', '/bin/sh /cdrom/nex/root-account.sh']) == 1

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
