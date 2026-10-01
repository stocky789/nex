#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Pin the authenticated build closure to its original download locations.

payloads.tsv rows are: SHA-256, destination, HTTPS URL. Destinations are repo/*.deb
and media/virtio-win.iso (classic edition), image/<release file> (image edition slot
set) and payload/<sha256>/<file> (image edition payloads kept outside the slot).
"""
import argparse
import hashlib
import shlex
import shutil
import subprocess
import sys
from pathlib import Path
from urllib.parse import urlsplit

VIRTIO_SHA256 = '303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d'
VIRTIO_URL = 'https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso'
IMAGE_REQUIRED = ('root.erofs', 'root.verity', 'roothash', 'manifest.json')
APPLIANCE_FILES = ('controller.qcow2', 'manifest.json', 'manifest.sig')


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def is_release_payload(deb):
    fields = {}
    for line in subprocess.check_output(['dpkg-deb', '-f', str(deb)], text=True).splitlines():
        if ': ' in line:
            key, value = line.split(': ', 1)
            fields[key] = value
    package = fields.get('Package', '')
    source = fields.get('Source', package).split(' ', 1)[0]
    return package in ('nex-core-local', 'dlm-controld') or source == 'libvirt'


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
    """Controller appliance and VirtIO driver ISO, in a directory named by the primary file's hash."""
    rows = []
    if appliance:
        primary = digest(appliance / 'controller.qcow2')
        for name in APPLIANCE_FILES:
            checksum = digest(appliance / name)
            rows.append(f'{checksum}\tpayload/{primary}/{name}\t{host(appliance / name, checksum, output, base)}\n')
    rows.append(f'{VIRTIO_SHA256}\tpayload/{VIRTIO_SHA256}/virtio-win.iso\t{VIRTIO_URL}\n')
    return rows


def build_manifest(stage, uris, base, output, image=None, appliance=None):
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
    debs = sorted((stage / 'nex/repo').glob('*.deb'))
    for deb in debs:
        checksum = digest(deb)
        if is_release_payload(deb):
            name = f'{deb.name.split("_")[0]}-{checksum}.deb'
            shutil.copyfile(deb, output / name)
            url = f'{base.rstrip("/")}/{name}'
        else:
            url = urls[deb.name]
        rows.append(f'{checksum}\trepo/{deb.name}\t{url}\n')
    if debs:
        rows.append(f'{VIRTIO_SHA256}\tmedia/virtio-win.iso\t{VIRTIO_URL}\n')
    if image:
        rows += image_rows(image, output, base) + payload_rows(appliance, output, base)
    (stage / 'nex/payloads.tsv').write_text(''.join(rows))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('stage', type=Path)
    parser.add_argument('uris', type=Path)
    parser.add_argument('base')
    parser.add_argument('output', type=Path)
    parser.add_argument('--image', type=Path, help='image release directory (image edition)')
    parser.add_argument('--appliance', type=Path, help='Controller appliance bundle directory')
    arguments = parser.parse_args()
    build_manifest(arguments.stage, arguments.uris, arguments.base, arguments.output,
                   image=arguments.image, appliance=arguments.appliance)
