#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Pin the authenticated build closure to its original download locations."""
import hashlib
import shlex
import shutil
import sys
from pathlib import Path
from urllib.parse import urlsplit


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def build_manifest(stage, uris, base, output):
    parsed = urlsplit(base)
    if parsed.scheme != 'https' or not parsed.netloc or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError('NEX_NETINST_BASE_URL must be an absolute HTTPS release directory')
    urls = {}
    for line in uris.read_text().splitlines():
        if line.startswith("'"):
            url, name, *_ = shlex.split(line)
            if urlsplit(url).hostname != 'snapshot.debian.org' or not url.startswith('https://'):
                raise ValueError('Debian packages must come directly from the pinned Debian snapshot')
            urls[name] = url
    output.mkdir(parents=True, exist_ok=True)
    rows = []
    for deb in sorted((stage / 'nex/repo').glob('*.deb')):
        checksum = digest(deb)
        if deb.name.startswith(('nex-core-local_', 'dlm-controld_')):
            name = f'{deb.name.split("_")[0]}-{checksum}.deb'
            shutil.copyfile(deb, output / name)
            url = f'{base.rstrip("/")}/{name}'
        else:
            url = urls[deb.name]
        rows.append(f'{checksum}\trepo/{deb.name}\t{url}\n')
    rows.append('303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d\tmedia/virtio-win.iso\thttps://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso\n')
    (stage / 'nex/netinst.tsv').write_text(''.join(rows))


if __name__ == '__main__':
    build_manifest(Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3], Path(sys.argv[4]))
