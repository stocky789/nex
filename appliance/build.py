#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build the Controller appliance; never provision a live host.

The output is the read-only erofs+verity system image of Core's two-disk Controller profile (image/controller/):
a vm-image component for a release, whose digest in the root-signed release manifest is its only integrity check.
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


def build(args, output):
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
        subprocess.run([args.docker, 'run', '--rm', '-e', f'OUTPUT_UID={os.getuid()}', '-e', f'OUTPUT_GID={os.getgid()}', '-e', f'NEX_VERSION={args.version}', '-v', f'{inputs}:/input:ro', '-v', f'{output}:/output', 'nex-appliance-builder'], check=True)
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
    parser.add_argument('--version', required=True)
    parser.add_argument('--sequence', type=int, default=0, help='the release sequence stamped into the image')
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/appliance')
    parser.add_argument('--docker', default='docker', help='docker executable; use a wrapper if sudo is needed')
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error('output directory must be empty')
    build(args, output)


if __name__ == '__main__':
    main()
