#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Pin the network installer's downloads to their locations.

payloads.tsv rows are: SHA-256, destination, HTTPS URL. Destinations are image/<release file>
(the slot set, every file keeps its path below image/) and payload/<sha256>/<file> (the
Controller system image and VirtIO driver ISO, kept outside the slot).
"""
import argparse
import hashlib
import shutil
from pathlib import Path
from urllib.parse import urlsplit

VIRTIO_SHA256 = '303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d'
VIRTIO_URL = 'https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso'
IMAGE_REQUIRED = ('root.erofs', 'root.verity', 'roothash', 'manifest.json')
APPLIANCE_FILES = ('controller-system.img',)


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def host(path, checksum, output, base):
    """Copy a Nex-built file into the hosted tree under its hash and return its URL."""
    name = f'{checksum}-{path.name}'
    shutil.copyfile(path, output / name)
    return f'{base.rstrip("/")}/{name}'


def image_rows(image, output, base):
    """The image release set: every file keeps its path below image/."""
    missing = [name for name in IMAGE_REQUIRED if not (image / name).is_file()]
    if missing or not list(image.glob('nex-core_*.efi')):
        raise ValueError(f'image directory incomplete: {missing or "UKI"}')
    rows = []
    for path in sorted(p for p in image.rglob('*') if p.is_file()):
        checksum = digest(path)
        rows.append(f'{checksum}\timage/{path.relative_to(image).as_posix()}\t{host(path, checksum, output, base)}\n')
    return rows


def payload_rows(appliance, output, base):
    """Controller system image and VirtIO driver ISO, in a directory named by the primary file's hash."""
    rows = []
    primary = digest(appliance / 'controller-system.img')
    for name in APPLIANCE_FILES:
        checksum = digest(appliance / name)
        rows.append(f'{checksum}\tpayload/{primary}/{name}\t{host(appliance / name, checksum, output, base)}\n')
    rows.append(f'{VIRTIO_SHA256}\tpayload/{VIRTIO_SHA256}/virtio-win.iso\t{VIRTIO_URL}\n')
    return rows


def build_manifest(stage, base, output, image, appliance):
    parsed = urlsplit(base)
    if parsed.scheme != 'https' or not parsed.netloc or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError('NEX_NETINST_BASE_URL must be an absolute HTTPS release directory')
    output.mkdir(parents=True, exist_ok=True)
    rows = image_rows(image, output, base) + payload_rows(appliance, output, base)
    (stage / 'nex/payloads.tsv').write_text(''.join(rows))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('stage', type=Path)
    parser.add_argument('base')
    parser.add_argument('output', type=Path)
    parser.add_argument('--image', type=Path, required=True, help='image release directory')
    parser.add_argument('--appliance', type=Path, required=True, help='Controller build output directory')
    arguments = parser.parse_args()
    build_manifest(arguments.stage, arguments.base, arguments.output, arguments.image, arguments.appliance)
