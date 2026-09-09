#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Check actual concatenated installer initrd contains usable approved artwork.

python3 tests/integration/installer-logo.py PATH/TO/gtk/initrd.gz
"""
import gzip
from pathlib import Path
import sys

payload = gzip.decompress(Path(sys.argv[1]).read_bytes())
offset = 0
directories = set()
symlinks = set()
artwork = None
expected = {
    "usr/lib/nex-installer/prepare": Path("installer/gui/prepare.sh").read_bytes(),
    "usr/lib/debian-installer.d/S66nex-wizard": Path("installer/gui/start.sh").read_bytes(),
    "usr/lib/partman/init.d/00nex-recheck": Path("installer/gui/recheck.sh").read_bytes(),
}
found = {}
while offset < len(payload):
    if payload[offset] == 0:
        offset += 1
        continue
    assert payload[offset:offset + 6] in (b'070701', b'070702'), 'Unexpected cpio format'
    fields = [int(payload[offset + 6 + n * 8:offset + 14 + n * 8], 16) for n in range(13)]
    size, namesize, mode = fields[6], fields[11], fields[1]
    name = payload[offset + 110:offset + 110 + namesize - 1].decode()
    name = name.removeprefix('./')
    start = (offset + 110 + namesize + 3) & ~3
    if mode & 0o170000 == 0o120000:
        symlinks.add(name)
    if mode & 0o170000 == 0o040000:
        assert name not in symlinks, f"Overlay directory replaces base symlink: {name}"
        directories.add(name)
    if name == 'usr/share/nex-installer/nex-core-lockup.png':
        assert 'usr/share/nex-installer' in directories, 'Kernel needs parent directory before logo file'
        artwork = payload[start:start + size]
    if name in expected:
        found[name] = payload[start:start + size]
    offset = (start + size + 3) & ~3
assert found == expected, 'Missing or stale installer startup/recheck scripts'
assert artwork == Path('installer/assets/nex-core-lockup.png').read_bytes(), 'Missing or different approved artwork'
assert artwork[25] == 6, 'Lockup must have native RGBA transparency, not a painted background'
print('PASS: complete initrd contains approved artwork with its parent directory before the file')
