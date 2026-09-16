#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Tests must never reach the host's own hypervisor.

BUG-0216: several Go test paths shelled out to the real `virsh` because they were reached
without the per-test PATH stub `fakeCommand` installs. On CI there is no libvirt, so the calls
failed and the tests tolerated it; on a contributor's workstation they reached `qemu:///system`
for real and raised a polkit authorisation prompt for each one. A single `go test ./internal/...`
run made 28 such calls, including `virsh event --all --loop`, which retries continuously and
flooded a desktop with hundreds of prompts.

The fix is a `TestMain` per affected package that puts a refusing `virsh` first on PATH. This
check stops a fourth package quietly reappearing without one, because that failure is invisible
to whoever adds it: it works on a box whose user is in the `libvirt` group, and prompts everyone
else. The rule follows the dependency graph rather than the text of each package, because the
worst offender (`internal/localcore`, 23 of the 28 calls) never mentions `virsh` at all — it
reaches it through `internal/vmhost`.
"""
import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GUARD = 'runWithStubbedHostTools'
# A package is treated as executing virsh when it both names the binary and runs subprocesses.
# Judging it per line was tried and rejected: excluding assignment lines dropped
# internal/protocol/cluster.go's `FenceVirsh = "virsh"` correctly, a fencing-method identifier
# that runs nothing, but it would also drop `bin = "virsh"` followed by `exec.Command(bin, ...)`
# — an exec site written one line apart. Asking whether the package as a whole does both keeps
# protocol out (it executes nothing at all) without depending on how any single line is written.
EXECUTES = re.compile(r'exec\.Command(?:Context)?\(')


def go_packages():
    out = subprocess.run(['go', 'list', './internal/...'], cwd=ROOT, check=True,
                         stdout=subprocess.PIPE, text=True).stdout
    return [line.strip() for line in out.splitlines() if line.strip()]


def package_dir(pkg):
    return ROOT / pkg.split('nex.local/nex/', 1)[1]


class HostToolIsolationTests(unittest.TestCase):
    def test_packages_that_can_reach_virsh_refuse_it_in_tests(self):
        packages = go_packages()
        self.assertTrue(packages, 'no Go packages found under ./internal/...')

        executes = set()
        for pkg in packages:
            sources = [f.read_text() for f in sorted(package_dir(pkg).glob('*.go'))
                       if not f.name.endswith('_test.go')]
            names_virsh = any('"virsh"' in body for body in sources)
            runs_anything = any(EXECUTES.search(body) for body in sources)
            if names_virsh and runs_anything:
                executes.add(pkg)
        self.assertTrue(executes, 'no package was found executing virsh; the detection rule is stale')
        # Pin the two ends the rule has to get right, so a future edit cannot quietly widen or
        # narrow it: agent and vmhost really do run virsh, protocol only names it as a constant.
        self.assertIn('nex.local/nex/internal/vmhost', executes)
        self.assertIn('nex.local/nex/internal/agent', executes)
        self.assertNotIn('nex.local/nex/internal/protocol', executes)

        for pkg in packages:
            directory = package_dir(pkg)
            if not list(directory.glob('*_test.go')):
                continue
            deps = subprocess.run(['go', 'list', '-deps', pkg], cwd=ROOT, check=True,
                                  stdout=subprocess.PIPE, text=True).stdout.split()
            if not ({pkg} | set(deps)) & executes:
                continue
            guarded = any(GUARD in f.read_text() for f in directory.glob('*_test.go'))
            self.assertTrue(guarded, (
                f'{pkg} has tests and can reach virsh, but installs no refusing stub. '
                f'Add a TestMain calling {GUARD} (see internal/vmhost/stubs_test.go). '
                'Without it, running this package\'s tests on a workstation with libvirt '
                'installed executes virsh against the real hypervisor and raises a polkit '
                'prompt per call (BUG-0216).'))

    def test_the_stub_refuses_rather_than_silently_succeeding(self):
        """A stub that exits 0 would hide a real call instead of surfacing it."""
        guards = [f for f in ROOT.glob('internal/*/stubs_test.go')]
        self.assertTrue(guards, 'no stub guards found')
        for f in guards:
            body = f.read_text()
            self.assertIn('"virsh"', body, f'{f.name} installs no virsh stub')
            self.assertIn('exit 1', body, f'{f.name} stub must fail, not succeed silently')
            self.assertIn('os.Setenv("PATH"', body, f'{f.name} stub is never put on PATH')


if __name__ == '__main__':
    unittest.main()
