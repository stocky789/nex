#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build a Debian Controller appliance; never provision a live host.

Two forms:
  --profile single-disk (default)  the locally signed qcow2 with its own manifest and signature
  --profile controller             the read-only erofs+verity system image of Core's two-disk Controller profile
                                   (image/controller/): a vm-image component for a release, no separate signature
"""
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


def stage_build_context(source, context):
    """Extract appliance/Dockerfile and every file it COPYs from the source archive into a Docker context."""
    context.mkdir()
    with tarfile.open(source) as archive:
        dockerfile = archive.extractfile('nex-source/appliance/Dockerfile').read()
        (context / 'Dockerfile').write_bytes(dockerfile)
        for line in dockerfile.decode().splitlines():
            fields = line.split()
            if fields and fields[0] == 'COPY':
                for name in fields[1:-1]:
                    (context / name).write_bytes(archive.extractfile('nex-source/appliance/' + name).read())
                    (context / name).chmod(0o755)


def build_controller_image(args, output):
    """The vm-image component: the payload tree from the appliance builder, then image/build.sh controller."""
    source = ROOT / 'dist/nex-source.tar.gz'
    subprocess.run(['python3', str(ROOT / 'scripts/build-source.py')], cwd=ROOT, check=True)
    work = output / 'image-work'
    with tempfile.TemporaryDirectory(prefix='nex-appliance-input-') as temporary:
        inputs = Path(temporary)
        shutil.copyfile(source, inputs / 'nex-source.tar.gz')
        stage_build_context(inputs / 'nex-source.tar.gz', inputs / 'appliance')
        subprocess.run([args.docker, 'build', '-t', 'nex-appliance-builder', str(inputs / 'appliance')], check=True)
        payload = output / 'payload'
        subprocess.run([args.docker, 'run', '--rm', '--entrypoint', '/usr/local/bin/nex-appliance-controller-payload', '-e', f'OUTPUT_UID={os.getuid()}', '-e', f'OUTPUT_GID={os.getgid()}', '-e', f'NEX_VERSION={args.version}', '-v', f'{inputs}:/input:ro', '-v', f'{output}:/output', 'nex-appliance-builder'], check=True)
    env = dict(os.environ, NEX_CONTROLLER_PAYLOAD=str(payload), NEX_IMAGE_OUT=str(work), NEX_LABEL=args.version, NEX_SEQUENCE=str(args.sequence))
    subprocess.run(['sh', str(ROOT / 'image/build.sh'), 'controller'], cwd=ROOT, env=env, check=True)
    release = work / 'controller-release'
    image = output / 'controller-system.img'
    shutil.copyfile(release / 'system.img', image)
    roothash = (release / 'roothash').read_text().strip()
    sha = digest(image)
    # What update-publish.py takes (--payload vm-image:controller=FILE) turns into the release manifest's component; the
    # digest in that root-signed manifest is the only integrity check of this image (no separate appliance signature).
    (output / 'vm-image.json').write_text(json.dumps({
        'format_version': 1, 'kind': 'vm-image', 'id': 'controller', 'name': image.name, 'sha256': sha, 'size': image.stat().st_size,
        'roothash': roothash, 'label': args.version, 'sequence': args.sequence, 'architecture': 'amd64', 'boot': 'uefi',
        'disks': {'system': 'read-only raw, erofs+verity', 'data': 'controller-data.qcow2: /var, holds /var/lib/nex-controller and /etc/nex-controller'},
        'publish_argument': 'vm-image:controller=' + str(image)}, indent=2, sort_keys=True) + '\n')
    if (work / 'controller.manifest').exists():
        shutil.copyfile(work / 'controller.manifest', output / 'components.json')
    # The work tree is root-owned (it was built in a container); remove it the same way.
    subprocess.run([args.docker, 'run', '--rm', '-v', f'{output}:/output', '--entrypoint', 'rm', 'nex-appliance-builder', '-rf', '/output/image-work', '/output/payload'], check=True)
    print(output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile', choices=('single-disk', 'controller'), default='single-disk')
    parser.add_argument('--base-image', type=Path, help='single-disk: pinned Debian 13 amd64 genericcloud qcow2')
    parser.add_argument('--base-sha256', help='single-disk: SHA256 of base image verified against upstream SHA512SUMS')
    parser.add_argument('--version', required=True)
    parser.add_argument('--sequence', type=int, default=0, help='controller: the release sequence stamped into the image')
    parser.add_argument('--signing-key', type=Path, help='single-disk: operator supplied Ed25519 PEM private key outside source tree')
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/appliance')
    parser.add_argument('--docker', default='docker', help='docker executable; use a wrapper if sudo is needed')
    args = parser.parse_args()
    if args.profile == 'controller':
        output = args.output.resolve()
        output.mkdir(parents=True, exist_ok=True)
        if any(output.iterdir()):
            parser.error('output directory must be empty')
        build_controller_image(args, output)
        return
    if not (args.base_image and args.base_sha256 and args.signing_key):
        parser.error('--base-image, --base-sha256 and --signing-key are required for --profile single-disk')
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
        stage_build_context(inputs / 'nex-source.tar.gz', inputs / 'appliance')
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
