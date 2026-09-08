# SPDX-License-Identifier: AGPL-3.0-only
"""Catch a missing runtime artifact before either runner starts browser QA."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]


class BrowserArtifactTests(unittest.TestCase):
    def test_both_runners_build_local_browser_binary(self):
        for provider in ('.github', '.gitea'):
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/phase1.yml').read_text()
                build = workflow.find('go build -o dist/nex-core-local ./cmd/nex-core-local')
                browser = workflow.find('python tests/integration/localcore_browser.py')
                self.assertGreaterEqual(build, 0, 'local browser default binary must be built')
                self.assertGreater(browser, build, 'build must run before local browser QA')
