#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build a locally signed Debian Controller appliance; never provision a live host."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-image', type=Path, required=True, help='pinned Debian 13 amd64 genericcloud qcow2')
    parser.add_argument('--base-sha256', required=True, help='SHA256 of base image verified against upstream SHA512SUMS')
    parser.add_argument('--version', required=True)
    parser.add_argument('--signing-key', type=Path, required=True, help='operator supplied Ed25519 PEM private key outside source tree')
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/appliance')
    parser.add_argument('--docker', default='docker', help='docker executable; use a wrapper if sudo is needed')
    args = parser.parse_args()
    key = args.signing_key.resolve()
    if key.is_relative_to(ROOT) or not key.is_file() or key.stat().st_mode & 0o077:
        parser.error('signing key must be a private mode-0600 file outside the repository')
    if len(args.base_sha256) != 64 or digest(args.base_image) != args.base_sha256.lower():
        parser.error('Debian base image checksum mismatch')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error('output directory must be empty')
    source = ROOT / 'dist/nex-source.tar.gz'
    subprocess.run(['python3', str(ROOT / 'scripts/build-source.py')], cwd=ROOT, check=True)
    with tempfile.TemporaryDirectory(prefix='nex-appliance-input-') as temporary:
        inputs = Path(temporary)
        shutil.copyfile(source, inputs / 'nex-source.tar.gz')
        shutil.copyfile(args.base_image, inputs / 'base.qcow2')
        (inputs / 'appliance').mkdir()
        with tarfile.open(inputs / 'nex-source.tar.gz') as archive:
            for name in ('Dockerfile', 'build-inside.sh'):
                (inputs / 'appliance' / name).write_bytes(archive.extractfile('nex-source/appliance/' + name).read())
        (inputs / 'appliance' / 'build-inside.sh').chmod(0o755)
        subprocess.run([args.docker, 'build', '-t', 'nex-appliance-builder', str(inputs / 'appliance')], check=True)
        # libguestfs customizes the image several times faster under KVM. The image forces
        # TCG so a host without /dev/kvm still builds; lift that only when the daemon can
        # lend the device, which a job container cannot tell from its own /dev.
        kvm = subprocess.run([args.docker, 'run', '--rm', '--device', '/dev/kvm', '--entrypoint', 'true', 'nex-appliance-builder'],
                             capture_output=True).returncode == 0
        accel = ['--device', '/dev/kvm', '-e', 'LIBGUESTFS_BACKEND_SETTINGS='] if kvm else []
        print('appliance build accelerator:', 'KVM' if kvm else 'TCG (the Docker host did not lend /dev/kvm)', flush=True)
        subprocess.run([args.docker, 'run', '--rm', *accel, '-e', f'OUTPUT_UID={os.getuid()}', '-e', f'OUTPUT_GID={os.getgid()}', '-e', f'NEX_VERSION={args.version}', '-v', f'{inputs}:/input:ro', '-v', f'{output}:/output', 'nex-appliance-builder'], check=True)
    manifest = {'format_version': 1, 'version': args.version, 'architecture': 'amd64',
                'image_sha256': digest(output / 'controller.qcow2'), 'protocol_version': 1,
                'format': 'qcow2', 'minimum_vcpus': 2, 'minimum_memory_mib': 4096,
                'minimum_disk_gib': 32}
    (output / 'manifest.json').write_text(json.dumps(manifest, sort_keys=True, separators=(',', ':')) + '\n')
    subprocess.run(['openssl', 'pkeyutl', '-sign', '-inkey', str(key), '-rawin', '-in', str(output / 'manifest.json'), '-out', str(output / 'manifest.sig')], check=True)
    public = subprocess.check_output(['openssl', 'pkey', '-in', str(key), '-pubout', '-outform', 'DER'])
    if not public.startswith(bytes.fromhex('302a300506032b6570032100')) or len(public) != 44:
        raise ValueError('an Ed25519 signing key is required')
    (output / 'public-key.raw').write_bytes(public[-32:])
    (output / 'build-inputs.json').write_text(json.dumps({'debian_image': args.base_image.name, 'debian_sha256': args.base_sha256, 'nex_source_sha256': digest(output / 'nex-source.tar.gz'), 'go_version': (output / 'go-version.txt').read_text().strip().split()[-1], 'redistribution_ready': False}, indent=2) + '\n')
    print(output)


if __name__ == '__main__':
    main()
