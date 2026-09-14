#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Source-level installation safety check; actual offline installation is separate.
set -euo pipefail
cd "$(dirname "$0")/.."
bash -n installer/build.sh
bash -n installer/build-gui.sh
sh -n installer/late-install.sh
sh -n installer/root-account.sh
python3 - <<'PY'
from pathlib import Path
import re

build = Path('installer/build.sh').read_text()
bios = build.split("<<'BIOS'\n")[2].split('\nBIOS')[0]
grub = build.split("<<'GRUB'\n")[1].split('\nGRUB')[0]
# Both firmware menus must offer the same frontend choice and shared safe policy.
for label, directory in [('GUI install - Nex Core', 'install.amd/gtk'),
                         ('Minimal install - Nex Core (text)', 'install.amd')]:
    b = next(entry for entry in re.split(r'(?m)^label ', bios)[1:]
             if f'menu label {label}\n' in entry)
    g = grub.split(f"menuentry '{label}' {{\n")[1].split('}')[0]
    assert 'kernel /install.amd/vmlinuz' in b
    assert f'initrd=/{directory}/initrd.gz' in b
    assert 'linux /install.amd/vmlinuz' in g
    assert f'initrd /{directory}/initrd.gz' in g
    if directory.endswith('/gtk'):
        assert 'preseed/file=' not in b + g
    else:
        assert 'preseed/file=/cdrom/preseed.cfg' in b and 'preseed/file=/cdrom/preseed.cfg' in g
    assert 'auto=true' not in b + g
assert 'timeout 0' in build and 'set timeout=-1' in grub
assert 'console=ttyS0,115200n8' in bios and 'console=ttyS0,115200n8' in grub
assert 'rescue/enable=true' in bios and 'rescue/enable=true' in grub
print('PASS: BIOS/UEFI GUI and minimal text entries, shared preseed, serial/rescue, no timeout')

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
assert {'qemu-system-x86', 'libvirt-daemon-system', 'libvirt-clients', 'openssh-server', 'genisoimage'} <= set(packages)
assert 'nex-core-local' not in packages, 'Ship nex-core-local via the ISO repo copy, not Debian APT download'
assert all(p.replace('-', '').isalnum() for p in packages), 'Unexpected package argument'
late = Path('installer/late-install.sh').read_text()
assert 'Dir::Etc::sourceparts=-' in late and 'file:/var/cache/nex-install/repo' in late
assert 'PermitRootLogin no' in late
assert 'systemd.ssh_auto=no' in late
assert 'nex-core-local.service' in late
assert 'Local alpha: no Nex Controller' not in late
assert r"Host: \n  Console: \l" in late
assert 'CORE_LOCAL_DEB' in build and 'nex-core-local_' in build
assert "printf 'nex-core-local\\n' >>" in build
assert 'scripts/build-source.py' in build, 'ISO builds must regenerate corresponding source'
assert 'appliance/build.py' in build, 'ISO builds must rebuild the Controller appliance'
assert 'assert_iso_artifacts_current' in build, 'ISO builds must verify Core/Controller source freshness'
assert 'Unset CORE_LOCAL_DEB and NEX_SKIP_APPLIANCE_REBUILD' in build, 'ISO builds must reject reused packages'
assert 'dist/nex-source.tar.gz required' in build
assert 'cp -a installer "$source_stage/nex-source/"' in build
assert re.search(r'tar .* -czf "\$stage/nex/nex-source\.tar\.gz" \\\s*LICENSE NOTICE docs/LICENSING\.md installer', build) is None, \
    'Do not ship installer-only source as the sole ISO archive'
# BUG-0004: raw chroot inherits the installer's stale debconf descriptors.
assert '\nin-target /bin/sh /var/cache/nex-install/configure.sh\n' in late, 'Use Debian in-target for package configuration'
assert '\nchroot /target ' not in late, 'Raw chroot bypasses installer setup'
post = Path('packaging/core-local/postinst').read_text()
assert 'dpkg-divert --local --add --rename --divert /etc/os-release.debian /etc/os-release' in post
assert 'ID=nex-core\nID_LIKE=debian\n' in post and "dpkg-query -W -f='${Version}' nex-core-local" in post
assert 'systemd.ssh_auto=no' in post
assert '0.1.0-alpha1' not in build + late and 'test "$ID" = nex-core' in late, 'One Nex Core version: the package'
assert '> /usr/lib/os-release' not in late, 'Preserve upstream release metadata'
print('PASS: shell syntax, interactive disk/account/network contract, package inputs, offline APT scope')
PY
