# SPDX-License-Identifier: AGPL-3.0-only
"""Keep the release Controller package runtime smoke check wired before uploads."""
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class ControllerPackageSmokeTests(unittest.TestCase):
    def test_both_ci_workflows_run_the_runtime_regression_on_push_and_pull_request(self):
        for workflow in (".github/workflows/phase1.yml", ".gitea/workflows/phase1.yml"):
            with self.subTest(workflow=workflow):
                text = (ROOT / workflow).read_text()
                self.assertIn("  push:", text)
                self.assertIn("  pull_request:", text)
                self.assertIn("python3 tests/integration/controller_package_smoke.py", text)

    def test_both_release_workflows_smoke_before_creating_a_release(self):
        for workflow in (".github/workflows/release.yml", ".gitea/workflows/release.yml"):
            with self.subTest(workflow=workflow):
                text = (ROOT / workflow).read_text()
                smoke = text.index("scripts/check-controller-package.py")
                upload = text.index("Create or replace the release")
                self.assertLess(smoke, upload)
                self.assertIn('nex-controller-builder', text)
                self.assertIn('--entrypoint python3', text)
