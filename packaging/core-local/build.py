#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build a Debian 13 local-host package; never install it or change host networking."""
import argparse
import os
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
    parser.add_argument('--version', default='0.1.5~alpha1')
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/core-local')
    parser.add_argument('--docker', default='docker', help='Docker executable or wrapper path')
    args = parser.parse_args()
    if not re.fullmatch(r'[0-9][A-Za-z0-9.+~]*', args.version):
        parser.error('invalid Debian package version')
    if not args.source.is_file():
        parser.error('missing Nex source archive')
    args.output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='nex-local-input-') as temporary:
        stage = Path(temporary)
        shutil.copyfile(args.source, stage / 'nex-source.tar.gz')
        context = stage / 'docker-context'
        context.mkdir()
        with tarfile.open(stage / 'nex-source.tar.gz', 'r:gz') as archive:
            for name in ['Dockerfile', 'build-inside.sh']:
                member = archive.getmember('nex-source/packaging/core-local/' + name)
                if not member.isfile():
                    parser.error('package build source must contain regular files')
                (context / name).write_bytes(archive.extractfile(member).read())
        (context / 'build-inside.sh').chmod(0o755)
        # The builder is pinned to the image's package snapshot, and the package's file times to the image's epoch.
        snapshot = (ROOT / 'image/snapshot').read_text().strip()
        epoch = os.environ.get('SOURCE_DATE_EPOCH') or re.search(
            r'^SourceDateEpoch=(\d+)$', (ROOT / 'image/mkosi.conf').read_text(), re.M).group(1)
        subprocess.run([args.docker, 'build', '-t', 'nex-core-local-builder', '--build-arg', 'SNAPSHOT=' + snapshot,
                        str(context)], check=True)
        run = [args.docker, 'run', '--rm', '-v', str(stage) + ':/input:ro']
        subprocess.run(run + ['-v', str(args.output.resolve()) + ':/output',
                              '-e', 'NEX_VERSION=' + args.version,
                              '-e', 'SOURCE_DATE_EPOCH=' + epoch,
                              'nex-core-local-builder'], check=True)
    print(args.output / ('nex-core-local_' + args.version + '_amd64.deb'))


if __name__ == '__main__':
    main()
