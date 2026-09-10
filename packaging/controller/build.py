#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build a Debian 13 nex-controller package; never install it."""
import argparse
from pathlib import Path
import re
import subprocess
import tempfile
import tarfile
import shutil

ROOT = Path(__file__).resolve().parents[2]


def default_version():
    """The newest CHANGELOG version, in Debian notation."""
    result = subprocess.run(
        ['python3', str(ROOT / 'scripts/release-version.py'), '--current'],
        check=False, capture_output=True, text=True)
    if result.returncode != 0 or not result.stdout.strip():
        raise SystemExit('nex-controller build: ' +
                         (result.stderr.strip() or 'could not resolve a build version'))
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True, type=Path)
    # Default to the newest CHANGELOG heading rather than a constant that goes stale
    # and silently mislabels a build. The release workflow passes --version explicitly.
    parser.add_argument('--version', default=default_version())
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/controller')
    parser.add_argument('--docker', default='docker', help='Docker executable or wrapper path')
    args = parser.parse_args()
    if not re.fullmatch(r'[0-9][A-Za-z0-9.+~]*', args.version):
        parser.error('invalid Debian package version')
    args.output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='nex-controller-input-') as temporary:
        stage = Path(temporary)
        shutil.copyfile(args.source, stage / 'nex-source.tar.gz')
        context = stage / 'docker-context'
        context.mkdir()
        with tarfile.open(stage / 'nex-source.tar.gz', 'r:gz') as archive:
            for name in ['Dockerfile', 'build-inside.sh']:
                member = archive.getmember('nex-source/packaging/controller/' + name)
                if not member.isfile():
                    parser.error('package build source must contain regular files')
                (context / name).write_bytes(archive.extractfile(member).read())
        (context / 'build-inside.sh').chmod(0o755)
        subprocess.run([args.docker, 'build', '-t', 'nex-controller-builder', str(context)], check=True)
        subprocess.run([args.docker, 'run', '--rm',
                        '-v', str(stage) + ':/input:ro',
                        '-v', str(args.output.resolve()) + ':/output',
                        '-e', 'NEX_VERSION=' + args.version, 'nex-controller-builder'], check=True)
    print(args.output / ('nex-controller_' + args.version + '_amd64.deb'))


if __name__ == '__main__':
    main()
