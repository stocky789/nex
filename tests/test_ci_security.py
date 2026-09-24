# SPDX-License-Identifier: AGPL-3.0-only
"""Catch a missing runtime artifact before either runner starts browser QA."""
from pathlib import Path
import re
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

    def test_phase1_keeps_separate_lanes_with_runner_safe_stagger(self):
        """Separate jobs still surface storage UI failures; needs caps Docker load.

        A single serial job hid late UI failures behind ~15 minutes of unrelated work.
        Five fully parallel containers on the shared Gitea act_runner then wedged Docker
        cleanup (run 4168). Keep named lanes, stagger to two concurrent containers, run
        the browser lane alone first so Go race cannot starve stalled-probe heartbeats
        (run 4171).
        """
        required = (
            'go:',
            'contracts:',
            'web:',
            'controller-browser:',
            'website:',
            'phase1:',
            'iso_url_browser.py',
            'storage_repositories_browser.py',
            'needs: [controller-browser]',
            'needs: [go]',
            'needs: [web]',
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
                group = ("${{ github.server_url == 'https://github.com' && github.workflow || 'nex-shared-runner' }}"
                         if provider == '.github' else 'nex-shared-runner')
                self.assertIn(f'  group: {group}\n', workflow)
                # Browser lane must be first and must not wait on Go.
                browser_job = workflow.index('\n  controller-browser:')
                go_job = workflow.index('\n  go:')
                self.assertLess(browser_job, go_job)
                browser_block = workflow[browser_job:go_job]
                self.assertNotIn('needs:', browser_block)
                self.assertNotIn('  host:', workflow)
                self.assertNotIn('schedule:', workflow)

    def test_phase1_runs_once_per_pull_request_commit(self):
        # BUG-0412: push and pull_request share the cancelling nex-shared-runner group,
        # so a '**' push trigger made the two runs of one commit cancel each other.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/phase1.yml').read_text()
                triggers = workflow[workflow.index('\non:\n'):workflow.index('\nconcurrency:')]
                uncommented = '\n'.join(line for line in triggers.splitlines() if not line.lstrip().startswith('#'))
                self.assertEqual(uncommented.split(), 'on: push: branches: - main pull_request:'.split())

    def test_release_shares_the_shared_runner_concurrency_group(self):
        # Release ISO builds and Controller foundation browser labs must not
        # overlap on the single Gitea act_runner (runs 4222+4223).
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                release = (ROOT / provider / 'workflows/release.yml').read_text()
                group = ("${{ github.server_url == 'https://github.com' && github.workflow || 'nex-shared-runner' }}"
                         if provider == '.github' else 'nex-shared-runner')
                self.assertIn(f'  group: {group}\n', release)
                self.assertIn('cancel-in-progress: true', release)

    def test_forge_release_only_attaches_netinst_payload_debs(self):
        # Bare Core/Controller packages remain in APT; netinst needs its exact
        # hash-named packages available even on forge-only releases.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                text = (ROOT / provider / 'workflows/release.yml').read_text()
                create = text.split('- name: Create or replace the release\n', 1)[1]
                step = create.split('\n      - name:', 1)[0]
                self.assertIn('scripts/gitea-release.py', step)
                self.assertIn('dist/netinst/*.deb', step)
                self.assertNotIn('dist/core-local/', step)
                self.assertNotIn('dist/controller/', step)
                self.assertIn('nex-core-${NEX_VERSION}-amd64.iso', step)
                self.assertIn('nex-core-${NEX_VERSION}-source.tar.gz', step)
                self.assertIn('nex-core-${NEX_VERSION}-compliance-manifest.json', step)


class ReleaseDeployCredentialTests(unittest.TestCase):
    """BUG-0075: a half-configured deploy skipped the website while the run stayed green.

    A silent skip left a passing release whose ISO never reached the download page. These
    run the workflow's own script, so the check cannot drift away from what CI executes.
    """

    def run_step(self, provider, secrets):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'github_output'
            output.touch()
            env = {
                'PATH': '/usr/bin:/bin',
                'GITHUB_OUTPUT': str(output),
            }
            env.update(secrets)
            done = subprocess.run(
                ['sh', '-c', credentials_script(provider)],
                capture_output=True, text=True, env=env, cwd=ROOT)
            return done, output.read_text()

    def test_every_secret_present_enables_publishing(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                secrets = {name: 'value' for name in DEPLOY_SECRETS}
                done, output = self.run_step(provider, secrets)
                self.assertEqual(done.returncode, 0, done.stderr)
                self.assertIn('enabled=true', output)

    def test_release_workflow_has_no_manifest_component_publish_gate(self):
        forbidden = (
            'check-zfs-blocked',
            'NEX_PUBLISH_WITH_ZFS',
            'ZFS-enabled ISO',
            'publish_downloads=',
            'allow_zfs=',
            'zfs_gate=',
        )
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                text = (ROOT / provider / 'workflows/release.yml').read_text()
                for token in forbidden:
                    self.assertNotIn(token, text, msg=f'{provider} still references {token!r}')

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
                # Node setup, website build, APT/downloads/site publish, and reachability.
                self.assertEqual(text.count("if: steps.deploy.outputs.enabled == 'true'"), 4)
                self.assertIn('scripts/website-ci-deploy.sh', text)
                self.assertIn('npm --prefix website run build', text)
                self.assertIn('NEX_VERSION: ${{ steps.version.outputs.version }}', text)

    def test_deploy_checks_and_website_build_run_before_the_tag_moves(self):
        # BUG-0413: a half-set secret or a broken website build used to abort the run
        # after the tag had moved and the forge release existed, leaving APT and the
        # download tree behind on the previous version.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                text = (ROOT / provider / 'workflows/release.yml').read_text()
                release = text.index('- name: Create or replace the release')
                for earlier in ('- name: Check for deploy credentials',
                                '- name: Set up Node.js for the website',
                                '- name: Build the static website'):
                    self.assertLess(text.index(earlier), release, earlier)

    def test_website_release_id_is_left_to_the_deploy_script(self):
        # BUG-0414: pinning it to the package version made a second push at the same
        # version, or any prerelease '~' version, fail after APT and downloads published.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                text = (ROOT / provider / 'workflows/release.yml').read_text()
                self.assertNotIn('NEX_WEBSITE_RELEASE_ID', text)


class GitHubMinuteBudgetTests(unittest.TestCase):
    """Hosted github.com minutes are paid for; the forge runs the full suite for free.

    The forge executes .github/workflows too (BUG-0043), so the split is made per job,
    not per directory: every job there is skipped on github.com except the one in
    github.yml. A new job added without that condition would silently bill Playwright,
    race-instrumented Go tests or an ISO build to the hosted account.
    """

    SKIP = "github.server_url != 'https://github.com'"
    ONLY = "if: github.server_url == 'https://github.com'"

    def jobs(self, path):
        """Yield (job name, job block) for each job in a workflow file."""
        text = path.read_text()
        body = text.split('\njobs:\n', 1)[1]
        top = re.search(r'(?m)^[A-Za-z]', body)
        if top:
            body = body[:top.start()]
        starts = [(m.start(), m.group(1)) for m in re.finditer(r'(?m)^  ([A-Za-z0-9_-]+):$', body)]
        self.assertTrue(starts, f'{path} declares no jobs')
        for index, (start, name) in enumerate(starts):
            end = starts[index + 1][0] if index + 1 < len(starts) else len(body)
            yield name, body[start:end]

    def test_only_the_cheap_job_runs_on_github_com(self):
        workflows = sorted((ROOT / '.github/workflows').glob('*.yml'))
        self.assertGreater(len(workflows), 1)
        self.assertIn('github.yml', [path.name for path in workflows])
        for path in workflows:
            for name, block in self.jobs(path):
                with self.subTest(workflow=path.name, job=name):
                    if path.name == 'github.yml':
                        self.assertIn(self.ONLY, block)
                    else:
                        self.assertIn(self.SKIP, block)
