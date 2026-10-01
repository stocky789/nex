# SPDX-License-Identifier: AGPL-3.0-only
"""Reject bounded-input archive amplification before root writes or Docker load."""
import gzip
import importlib.machinery
import importlib.util
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
from types import SimpleNamespace
import unittest

ROOT = Path(__file__).resolve().parents[1]


class DemoArchiveTests(unittest.TestCase):
    def test_repeated_layer_references_cannot_amplify_repacked_output(self):
        loader = importlib.machinery.SourceFileLoader('demo_archive_deployer', str(ROOT / 'packaging/demo/nex-demo-deploy'))
        spec = importlib.util.spec_from_loader(loader.name, loader)
        deployer = importlib.util.module_from_spec(spec)
        loader.exec_module(deployer)
        revision = '0' * 40
        config = json.dumps({'os': 'linux', 'architecture': 'amd64', 'config': {
            'Labels': {'org.opencontainers.image.revision': revision}}}).encode()
        layer = io.BytesIO()
        with tarfile.open(fileobj=layer, mode='w') as archive:
            member = tarfile.TarInfo('fixture')
            member.size = 1
            archive.addfile(member, io.BytesIO(b'x'))
        source = io.BytesIO()
        manifest = json.dumps([{'Config': 'config.json', 'RepoTags': ['nex-demo:' + revision],
                                'Layers': ['layer.tar'] * 3}]).encode()
        with tarfile.open(fileobj=source, mode='w') as archive:
            for name, data in [('config.json', config), ('manifest.json', manifest),
                               ('layer.tar', layer.getvalue())]:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        # The input fits exactly. Repeated references plus tar overhead do not.
        deployer.MAX_ARCHIVE = len(source.getvalue())
        original_stdin = sys.stdin
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            try:
                sys.stdin = SimpleNamespace(buffer=io.BytesIO(gzip.compress(source.getvalue())))
                with self.assertRaises(ValueError):
                    deployer.import_image(revision, temporary)
            finally:
                sys.stdin = original_stdin
            self.assertFalse((temporary / 'validated.tar').exists(),
                             'oversized repack must be rejected before writing any output')


if __name__ == '__main__':
    unittest.main()
