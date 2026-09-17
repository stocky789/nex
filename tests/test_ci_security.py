# SPDX-License-Identifier: AGPL-3.0-only
"""Catch a missing runtime artifact before either runner starts browser QA."""
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[1]
PROVIDERS = ('.github', '.gitea')
DEPLOY_SECRETS = ('NEX_DEPLOY_HOST', 'NEX_DEPLOY_USER', 'NEX_DEPLOY_KEY', 'NEX_DEPLOY_KNOWN_HOSTS')


def credentials_script(provider):
    """Return the shell body of the release workflow's deploy-credentials step.

    Reading it out of the workflow keeps the test honest: it exercises the script CI
    actually runs, so editing the workflow cannot leave the test passing against a copy.
    """
    text = (ROOT / provider / 'workflows/release.yml').read_text()
    step = text.index('- name: Check for deploy credentials')
    body = text.index('run: |', step) + len('run: |\n')
    lines = []
    for line in text[body:].splitlines():
        if line.strip() and not line.startswith(' ' * 10):
            break
        lines.append(line)
    return textwrap.dedent('\n'.join(lines))


class BrowserArtifactTests(unittest.TestCase):
    def test_go_race_suite_has_an_explicit_bounded_ci_budget(self):
        # Full-strength password hashing under race instrumentation exceeded Go's
        # default ten-minute package budget on the shared runner.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/phase1.yml').read_text()
                self.assertIn('run: go test -race -timeout 20m ./...', workflow)

    def test_phase1_runs_independent_lanes_in_parallel(self):
        """Serial phase1 hid storage UI failures behind ~15 minutes of unrelated work."""
        required = (
            'go:',
            'contracts:',
            'web:',
            'controller-browser:',
            'website:',
            'phase1:',
            'iso_url_browser.py',
            'storage_repositories_browser.py',
        )
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/phase1.yml').read_text()
                for marker in required:
                    self.assertIn(marker, workflow)
                smoke = workflow.index('iso_url_browser.py')
                gallery = workflow.index('website_controller_captures.py')
                self.assertLess(smoke, gallery, 'storage UI smoke must run before gallery in the browser lane')
                self.assertIn('cancel-in-progress: true', workflow)
                # The runner cannot unshare or bind-mount, so a host lane only ever skipped.
                self.assertNotIn('  host:', workflow)
                self.assertNotIn('schedule:', workflow)


class ReleaseDeployCredentialTests(unittest.TestCase):
    """BUG-0075: a half-configured deploy skipped the website while the run stayed green.

    The tag and the Gitea release are created before this step, so a silent skip left a
    passing release whose ISO never reached the download page. These run the workflow's
    own script, so the check cannot drift away from what CI executes.
    """

    def run_step(self, provider, secrets):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'github_output'
            output.touch()
            env = {'PATH': '/usr/bin:/bin', 'GITHUB_OUTPUT': str(output)}
            env.update(secrets)
            done = subprocess.run(['sh', '-c', credentials_script(provider)],
                                  capture_output=True, text=True, env=env)
            return done, output.read_text()

    def test_every_secret_present_enables_publishing(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                done, output = self.run_step(provider, {name: 'value' for name in DEPLOY_SECRETS})
                self.assertEqual(done.returncode, 0, done.stderr)
                self.assertIn('enabled=true', output)

    def test_no_secrets_skips_quietly_for_a_fork(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                done, output = self.run_step(provider, {})
                self.assertEqual(done.returncode, 0, done.stderr)
                self.assertIn('enabled=false', output)

    def test_a_missing_secret_fails_the_release_instead_of_skipping(self):
        for provider in PROVIDERS:
            for absent in DEPLOY_SECRETS:
                with self.subTest(provider=provider, absent=absent):
                    secrets = {name: 'value' for name in DEPLOY_SECRETS if name != absent}
                    done, output = self.run_step(provider, secrets)
                    self.assertNotEqual(done.returncode, 0,
                                        f'{absent} missing must fail, not skip the website')
                    self.assertIn(absent, done.stderr)
                    self.assertNotIn('enabled=true', output)

    def test_publish_and_verify_steps_are_gated_on_the_same_result(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                text = (ROOT / provider / 'workflows/release.yml').read_text()
                # Publishing and its reachability check must share one gate; verifying
                # behind a different condition would let an unpublished release pass.
                self.assertEqual(text.count("if: steps.deploy.outputs.enabled == 'true'"), 2)
                self.assertLess(text.index('- name: Create or replace the release'),
                                text.index('- name: Check for deploy credentials'),
                                'tagging happens first, which is why a silent skip is invisible')
