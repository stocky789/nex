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
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MODULE = 'nex.local/nex/'
# Imports are resolved by reading the sources rather than by shelling out to `go list`. The
# contracts job that runs this suite installs no Go toolchain, so a check that needed one would
# fail there (it did) — and making it skip instead would leave the guard silently absent, which
# is the failure mode BUG-0216 is about. The import graph inside one module is simple enough to
# read directly, and doing so keeps this check runnable anywhere the repository is checked out.
IMPORT_BLOCK = re.compile(r'^import\s*\((.*?)^\)', re.S | re.M)
IMPORT_SINGLE = re.compile(r'^import\s+(?:[\w.]+\s+)?"([^"]+)"', re.M)
IMPORT_LINE = re.compile(r'^\s*(?:[\w.]+\s+)?"([^"]+)"', re.M)
GUARD = 'runWithStubbedHostTools'
# A package is treated as executing virsh when it both names the binary and runs subprocesses.
# Judging it per line was tried and rejected: excluding assignment lines dropped
# internal/protocol/cluster.go's `FenceVirsh = "virsh"` correctly, a fencing-method identifier
# that runs nothing, but it would also drop `bin = "virsh"` followed by `exec.Command(bin, ...)`
# — an exec site written one line apart. Asking whether the package as a whole does both keeps
# protocol out (it executes nothing at all) without depending on how any single line is written.
EXECUTES = re.compile(r'exec\.Command(?:Context)?\(')


def go_packages():
    """Every package directory under internal/ that holds Go sources."""
    return sorted(MODULE + 'internal/' + d.name
                  for d in (ROOT / 'internal').iterdir()
                  if d.is_dir() and any(d.glob('*.go')))


def package_dir(pkg):
    return ROOT / pkg.split(MODULE, 1)[1]


def imports_of(pkg, tests=False):
    """Import paths named by pkg's sources, non-test only unless tests is set."""
    found = set()
    for src in sorted(package_dir(pkg).glob('*.go')):
        if src.name.endswith('_test.go') and not tests:
            continue
        body = src.read_text()
        for block in IMPORT_BLOCK.findall(body):
            found.update(IMPORT_LINE.findall(block))
        found.update(IMPORT_SINGLE.findall(body))
    return {i for i in found if i.startswith(MODULE)}


def reaches(pkg, targets, graph):
    """Whether pkg or anything it imports, transitively, is in targets."""
    seen, stack = set(), [pkg]
    while stack:
        current = stack.pop()
        if current in targets:
            return True
        if current in seen:
            continue
        seen.add(current)
        stack.extend(graph.get(current, ()))
    return False


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

        # Non-test imports only, which is what `go list -deps` reports and what the measured
        # set matches: {vmhost, agent, localcore}, the three packages whose suites were observed
        # making real virsh calls. Including test-only imports additionally flags
        # internal/controller, whose tests were measured making none, so it would demand a guard
        # the evidence does not support.
        graph = {p: imports_of(p) for p in packages}

        for pkg in packages:
            directory = package_dir(pkg)
            if not list(directory.glob('*_test.go')):
                continue
            if not reaches(pkg, executes, graph):
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
