#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build a Debian 13 local-host package; never install it or change host networking."""
import argparse
import json
import hashlib
from pathlib import Path
import re
import subprocess
import tempfile
import tarfile
import shutil

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True, type=Path)
    parser.add_argument('--bundle', required=True, type=Path)
    parser.add_argument('--trusted-key', required=True, type=Path,
                        help='independently trusted raw 32-byte Ed25519 release public key')
    parser.add_argument('--version', default='0.1.5~alpha1')
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/core-local')
    parser.add_argument('--docker', default='docker', help='Docker executable or wrapper path')
    args = parser.parse_args()
    if not re.fullmatch(r'[0-9][A-Za-z0-9.+~]*', args.version):
        parser.error('invalid Debian package version')
    if len(args.trusted_key.read_bytes()) != 32:
        parser.error('trusted key must contain exactly 32 raw bytes')
    for name in ['controller.qcow2', 'manifest.json', 'manifest.sig', 'nex-source.tar.gz', 'components.tsv', 'LICENSE', 'NOTICE', 'build-inputs.json']:
        if not (args.bundle / name).is_file():
            parser.error('missing appliance artifact: ' + name)
    args.output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='nex-local-input-') as temporary:
        stage = Path(temporary)
        shutil.copyfile(args.source, stage / 'nex-source.tar.gz')
        shutil.copyfile(args.trusted_key, stage / 'trusted-key.raw')
        # Ed25519 SubjectPublicKeyInfo prefix followed by the independently supplied key.
        (stage / 'key.der').write_bytes(bytes.fromhex('302a300506032b6570032100') + args.trusted_key.read_bytes())
        subprocess.run(['openssl', 'pkeyutl', '-verify', '-pubin', '-keyform', 'DER',
                        '-inkey', str(stage / 'key.der'), '-rawin',
                        '-in', str(args.bundle / 'manifest.json'),
                        '-sigfile', str(args.bundle / 'manifest.sig')], check=True)
        manifest = json.loads((args.bundle / 'manifest.json').read_text())
        with (args.bundle / 'controller.qcow2').open('rb') as image:
            digest = hashlib.file_digest(image, 'sha256').hexdigest()
        if manifest.get('image_sha256') != digest:
            parser.error('appliance digest does not match the signed manifest')
        context = stage / 'docker-context'
        context.mkdir()
        with tarfile.open(stage / 'nex-source.tar.gz', 'r:gz') as archive:
            for name in ['Dockerfile', 'build-inside.sh']:
                member = archive.getmember('nex-source/packaging/core-local/' + name)
                if not member.isfile():
                    parser.error('package build source must contain regular files')
                (context / name).write_bytes(archive.extractfile(member).read())
        (context / 'build-inside.sh').chmod(0o755)
        subprocess.run([args.docker, 'build', '-t', 'nex-core-local-builder', str(context)], check=True)
        # Docker needs nested mount targets present before mounting the parent read-only.
        (stage / 'bundle').mkdir()
        # Avoid another multi-gigabyte image copy; bind the selected bundle read-only.
        subprocess.run([args.docker, 'run', '--rm',
                        '-v', str(stage) + ':/input:ro',
                        '-v', str(args.bundle.resolve()) + ':/input/bundle:ro',
                        '-v', str(args.output.resolve()) + ':/output',
                        '-e', 'NEX_VERSION=' + args.version, 'nex-core-local-builder'], check=True)
    print(args.output / ('nex-core-local_' + args.version + '_amd64.deb'))


if __name__ == '__main__':
    main()
