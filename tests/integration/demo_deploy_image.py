#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Verify safe republication of the real built image, including containerd stores."""
import argparse
import gzip
import importlib.machinery
import importlib.util
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='Actual built nex-demo:<revision> image')
    args = parser.parse_args()
    if not re.fullmatch(r'nex-demo:[0-9a-f]{40}', args.image):
        parser.error('expected a commit-tagged demo image')
    loader = importlib.machinery.SourceFileLoader('demo_deployer', str(ROOT / 'packaging/demo/nex-demo-deploy'))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    deployer = importlib.util.module_from_spec(spec)
    loader.exec_module(deployer)
    deployer.DOCKER = shutil.which('docker') or '/usr/bin/docker'
    revision = args.image.split(':', 1)[1]
    original_stdin = sys.stdin
    with tempfile.TemporaryDirectory(prefix='nex-demo-image-check-') as directory:
        deployer.STATE = Path(directory)
        imported = deployer.STATE / 'imported'
        original_config = deployer.image_config_id(args.image)
        imported.mkdir()
        with tempfile.TemporaryFile() as saved, tempfile.TemporaryFile() as compressed:
            subprocess.run((deployer.DOCKER, 'image', 'save', args.image),
                           stdout=saved, check=True, timeout=120)
            saved.seek(0)
            with gzip.GzipFile(fileobj=compressed, mode='wb', compresslevel=1) as stream:
                shutil.copyfileobj(saved, stream, length=1024 * 1024)
            compressed.seek(0)
            try:
                sys.stdin = SimpleNamespace(buffer=compressed)
                # The tag already exists: a byte-identical config must remain publishable,
                # even when inspect reports a manifest/index ID instead of the config ID.
                deployer.receive_archive(imported)
                deployer.import_image(revision, imported)
                if deployer.image_config_id(args.image) != original_config:
                    raise AssertionError('republication changed the selected image configuration')
            finally:
                sys.stdin = original_stdin
    print('Passed: real image republication preserves the validated immutable config')


if __name__ == '__main__':
    main()
