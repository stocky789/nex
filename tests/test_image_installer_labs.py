# SPDX-License-Identifier: AGPL-3.0-only
"""Installer lab behavior without launching Docker, QEMU or KVM."""
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

INTEGRATION = Path(__file__).resolve().parent / 'integration'
sys.path.insert(0, str(INTEGRATION))


def load(name):
    spec = importlib.util.spec_from_file_location(f'installer_lab_{name}', INTEGRATION / f'{name}.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class InstallerConsoleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.lab = load('image_install_lab')

    def run_console(self, lines):
        console = mock.Mock()
        text = []
        events = iter(lines)

        def expect(*args):
            try:
                text.append(next(events))
            except StopIteration:
                raise RuntimeError('installer rebooted')

        console.expect.side_effect = expect
        console.log_text.side_effect = lambda: '\n'.join(text)
        proc = mock.Mock()
        proc.poll.return_value = None
        with mock.patch.object(self.lab, 'qemu', return_value=(proc, console)), \
                mock.patch.object(self.lab, 'stop') as stop, mock.patch.object(self.lab, 'info'):
            state, output = self.lab.run_installer('work', 'iso', [], 'ctl', 'vars', budget=10)
        stop.assert_called_once_with(proc)
        return state, output

    def test_selected_array_stop_progress_continues_to_install_completion(self):
        state, output = self.run_console([
            'nex-installer: Stopping md0, which holds a selected disk',
            'nex-installer: Installing the system image on /dev/sda (single)',
            'nex-installer: Installation complete. Remove the installation media; the host restarts now.',
        ])
        self.assertEqual(state, 'done')
        self.assertIn('Installation complete.', output)

    def test_actual_installer_refusals_stop_the_harness(self):
        for failure in (
            'nex-installer: Could not stop md0, which holds a selected disk',
            'nex-installer: The media is damaged. No disk has been changed.',
            'nex-installer: The image edition installs on UEFI firmware only.',
            'LAB-VALIDATE-REFUSED',
        ):
            with self.subTest(failure=failure):
                state, output = self.run_console([failure, 'must not continue'])
                self.assertEqual(state, 'refused')
                self.assertNotIn('must not continue', output)


class ISOOutputSafetyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.lab = load('image_iso_lab')

    def test_output_work_overlap_is_refused_before_cleanup_or_build(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            work = root / 'work'
            work.mkdir()
            (work / '.image_iso_lab').touch()
            sentinel = work / 'keep.txt'
            sentinel.write_text('existing lab evidence')
            alias = root / 'alias'
            alias.symlink_to(work, target_is_directory=True)
            release = root / 'release'
            release.mkdir()
            cases = (
                (work, work / 'result.iso'),
                (work, work),
                (work, root),
                (work, alias / 'result.iso'),
                (alias, work / 'result.iso'),
                (work, work / 'nested/../result.iso'),
            )
            for work_arg, out_arg in cases:
                with self.subTest(work=work_arg, out=out_arg), \
                        mock.patch.object(sys, 'argv', ['driver', '--release', str(release),
                                                      '--hostimage', str(root / 'hostimage'),
                                                      '--work', str(work_arg), '--out', str(out_arg)]), \
                        mock.patch.object(self.lab.shutil, 'rmtree') as cleanup, \
                        mock.patch.object(self.lab, 'run') as run, \
                        mock.patch.object(self.lab, 'docker') as docker:
                    with self.assertRaises(SystemExit):
                        self.lab.main()
                    cleanup.assert_not_called()
                    run.assert_not_called()
                    docker.assert_not_called()
                    self.assertEqual(sentinel.read_text(), 'existing lab evidence')


if __name__ == '__main__':
    unittest.main()
