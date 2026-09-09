# SPDX-License-Identifier: AGPL-3.0-only
"""Disposable-target guards run without QEMU or touching a real disk."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    'fresh_install_lab', Path(__file__).parent / 'integration/fresh_install_lab.py')
lab_tools = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab_tools)


class FreshInstallTargetTests(unittest.TestCase):
    def test_only_selected_disposable_regular_disk_is_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cache = root / '.cache'
            cache.mkdir()
            lab = cache / 'nex-fresh-install.test'
            lab.mkdir()
            disk = lab / 'host.raw'
            disk.write_bytes(b'')
            config = lab / 'config.json'
            config.write_text(json.dumps({'disposable': True, 'lab_dir': str(lab)}))
            with patch.object(lab_tools, 'ROOT', root):
                lab_tools.validate_lab(lab)
                outside = root / 'nex-fresh-install.outside'
                outside.mkdir()
                with self.assertRaises(AssertionError):
                    lab_tools.validate_lab(outside)
                config.write_text(json.dumps({'disposable': False, 'lab_dir': str(lab)}))
                with self.assertRaises(AssertionError):
                    lab_tools.validate_lab(lab)
                config.write_text(json.dumps({'disposable': True, 'lab_dir': str(lab)}))
                disk.unlink()
                target = root / 'unrelated-data'
                target.write_bytes(b'keep')
                disk.symlink_to(target)
                with self.assertRaises(AssertionError):
                    lab_tools.validate_lab(lab)
                self.assertEqual(target.read_bytes(), b'keep')

    def test_qmp_must_identify_exact_raw_disk_before_control(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            lab = root / '.cache/nex-fresh-install.test'
            lab.mkdir(parents=True)
            (lab / 'host.raw').write_bytes(b'')
            (lab / 'config.json').write_text(json.dumps(
                {'disposable': True, 'lab_dir': str(lab)}))
            attached = [{'device': 'hostdisk', 'inserted': {
                'file': str(lab / 'host.raw'), 'drv': 'raw'}}]
            with patch.object(lab_tools, 'ROOT', root), patch.object(
                    lab_tools, 'qmp', return_value=attached):
                lab_tools.validate_target(lab)
                attached[0]['inserted']['file'] = str(root / 'unrelated-data')
                with self.assertRaises(AssertionError):
                    lab_tools.validate_target(lab)
                attached[0]['inserted']['file'] = str(lab / 'host.raw')
                attached[0]['inserted']['drv'] = 'qcow2'
                with self.assertRaises(AssertionError):
                    lab_tools.validate_target(lab)


if __name__ == '__main__':
    unittest.main()
