# SPDX-License-Identifier: AGPL-3.0-only
"""Catch a missing runtime artifact before either runner starts browser QA."""
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading
import textwrap
import unittest
from http.server import ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from test_gitea_release import FakeGitea

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
    def test_go_suite_has_an_explicit_bounded_ci_budget(self):
        # The shared runner can exceed Go's default ten-minute package budget.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/phase1.yml').read_text()
                self.assertIn('run: go test -timeout 40m ./...', workflow)

    def test_phase1_is_fast_checks_behind_one_gate(self):
        """Build, vet, type-check and unit-test; booting images and ISOs belongs to image.yml and release.yml."""
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/phase1.yml').read_text()
                for marker in ('go:', 'web:', 'checks:', 'phase1:', 'needs: [go, web, checks]',
                               'go build ./...', 'go vet ./...', 'generate_api.py --check'):
                    self.assertIn(marker, workflow)
                self.assertNotIn('playwright', workflow)
                self.assertNotIn('schedule:', workflow)

    def test_phase1_runs_once_per_pull_request_commit(self):
        # Integration pushes are covered directly; feature branch pushes are
        # covered by pull_request, so one commit does not schedule duplicate labs.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/phase1.yml').read_text()
                triggers = workflow[workflow.index('\non:\n'):workflow.index('\ndefaults:')]
                uncommented = '\n'.join(line for line in triggers.splitlines() if not line.lstrip().startswith('#'))
                # main (stable) and dev (development) are the integration branches; feature
                # branches are covered by their pull request.
                self.assertEqual(uncommented.split(), 'on: push: branches: - main - dev pull_request:'.split())

    def test_release_runs_for_both_integration_branches_and_nothing_else(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                workflow = (ROOT / provider / 'workflows/release.yml').read_text()
                triggers = workflow[workflow.index('\non:\n'):workflow.index('\ndefaults:')]
                uncommented = '\n'.join(line for line in triggers.splitlines() if not line.lstrip().startswith('#'))
                self.assertEqual(uncommented.split(), 'on: push: branches: - main - dev'.split())


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

    def test_no_secrets_fails_before_publishing(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                done, output = self.run_step(provider, {})
                self.assertNotEqual(done.returncode, 0)
                self.assertIn('NEX_DEPLOY_HOST', done.stderr)
                self.assertNotIn('enabled=true', output)

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
                # Node setup, website build, downloads/site publish, update tree publish, and reachability.
                self.assertEqual(text.count("if: steps.deploy.outputs.enabled == 'true'"), 5)
                self.assertIn('scripts/website-ci-deploy.sh', text)
                self.assertIn('npm --prefix website run build', text)
                self.assertIn('NEX_VERSION: ${{ steps.version.outputs.version }}', text)

    def test_download_site_is_reachable_before_the_tag_moves(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                text = (ROOT / provider / 'workflows/release.yml').read_text()
                release = text.index('- name: Create or replace the release')
                for earlier in ('- name: Check for deploy credentials',
                                '- name: Set up Node.js for the website',
                                '- name: Build the static website',
                                '- name: Publish to the website host',
                                '- name: Confirm the release is actually reachable'):
                    self.assertLess(text.index(earlier), release, earlier)

    def test_website_release_id_is_left_to_the_deploy_script(self):
        # BUG-0414: pinning it to the package version made a second push at the same
        # version, or any prerelease '~' version, fail after APT and downloads published.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                text = (ROOT / provider / 'workflows/release.yml').read_text()
                self.assertNotIn('NEX_WEBSITE_RELEASE_ID', text)


def release_step(provider, name):
    """The shell body of one named release step, exactly as CI runs it."""
    text = (ROOT / provider / 'workflows/release.yml').read_text()
    step = text.split(f'      - name: {name}\n', 1)[1].split('\n      - name:', 1)[0]
    return textwrap.dedent(step.split('        run: |\n', 1)[1])


class ReleaseChannelTests(unittest.TestCase):
    """main releases the stable channel and dev the development channel, each into its own subtree.

    The steps run for real against fake ssh/scp/python3 so a regression shows up as a stable
    release landing in the development tree, a development run replacing the live website, or a
    development build that never becomes a pre-release.
    """

    CHANGELOG = '# Changelog\n\n## [Unreleased]\n\n%s\n## [0.4.18] \u2014 2026-09-28\n\n- Shipped.\n'

    def run_step(self, provider, name, cwd, env, tools=()):
        bin_dir = Path(cwd) / 'bin'
        bin_dir.mkdir(exist_ok=True)
        for tool in tools:
            fake = bin_dir / tool
            fake.write_text(f'#!/bin/sh\nprintf "%s\\n" "$*" >> "{cwd}/{tool}.log"\n')
            fake.chmod(0o755)
        return subprocess.run(['bash', '-euo', 'pipefail', '-c', release_step(provider, name)], cwd=cwd, text=True,
                              capture_output=True, env={**os.environ, **env, 'PATH': f'{bin_dir}{os.pathsep}{os.environ["PATH"]}'})

    def checkout(self, root):
        """A CI checkout of dev: main's last release is origin/main, and dev has one commit of staged work."""
        def git(*args):
            subprocess.run(['git', '-c', 'user.name=CI', '-c', 'user.email=ci@example.invalid', *args],
                           cwd=root, check=True, capture_output=True)
        (root / 'scripts').mkdir()
        shutil.copy(ROOT / 'scripts/release-version.py', root / 'scripts')
        (root / 'CHANGELOG.md').write_text(self.CHANGELOG % '')
        git('init', '-q', '-b', 'main')
        git('add', '.')
        git('commit', '-q', '-m', 'release')
        git('update-ref', 'refs/remotes/origin/main', 'HEAD')
        git('checkout', '-q', '-b', 'dev')
        (root / 'CHANGELOG.md').write_text(self.CHANGELOG % '- Staged work.\n')
        git('commit', '-q', '-am', 'work')

    def test_branch_decides_the_channel_version_and_publish_paths(self):
        for provider in PROVIDERS:
            for branch, expected in (
                    ('main', 'tag=v0.4.18\nversion=0.4.18\npublic_version=0.4.18\nchannel=stable\ndownloads_path=downloads\n'),
                    ('dev', 'tag=v0.4.19-dev.2\nversion=0.4.19~~dev.2\npublic_version=0.4.19-dev.2\nchannel=dev\ndownloads_path=downloads/dev\n')):
                with self.subTest(provider=provider, branch=branch), tempfile.TemporaryDirectory() as tmp:
                    root = Path(tmp)
                    self.checkout(root)
                    output = root / 'output'
                    result = self.run_step(provider, 'Resolve release channel, version and notes', root,
                                           {'NEX_BRANCH': branch, 'GITHUB_OUTPUT': str(output)})
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(output.read_text(), expected)
                    # A development build ships what dev has staged; a stable release its heading.
                    notes = (root / '.cache/release-notes.md').read_text()
                    self.assertEqual(notes, '- Staged work.\n' if branch == 'dev' else '- Shipped.\n')

    def test_any_other_branch_is_refused(self):
        for provider in PROVIDERS:
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                self.checkout(root)
                result = self.run_step(provider, 'Resolve release channel, version and notes', root,
                                       {'NEX_BRANCH': 'feature', 'GITHUB_OUTPUT': str(root / 'output')})
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('main and dev only', result.stderr)
                self.assertFalse((root / 'output').exists())

    def test_development_publishes_into_its_own_subtree_and_never_the_website(self):
        for provider in PROVIDERS:
            for channel, version, flags in (
                    ('stable', '0.4.18', ("release-publish.py' --iso",)),
                    ('dev', '0.4.19~~dev.7', ("release-publish.py' --root /srv/nex-downloads/dev --iso",))):
                with self.subTest(provider=provider, channel=channel), tempfile.TemporaryDirectory() as tmp:
                    root = Path(tmp)
                    (root / 'dist/netinst').mkdir(parents=True)
                    (root / 'dist/netinst' / ('nex-core-local-' + 'a' * 64 + '.deb')).write_bytes(b'x')
                    (root / 'scripts').mkdir()
                    deploy = root / 'scripts/website-ci-deploy.sh'
                    deploy.write_text(f'#!/bin/sh\ntouch "{root}/website-deployed"\n')
                    deploy.chmod(0o755)
                    result = self.run_step(provider, 'Publish to the website host', root, {
                        'NEX_VERSION': version, 'NEX_PUBLIC_VERSION': version.replace('~~', '-'),
                        'NEX_CHANNEL': channel, 'NEX_DEPLOY_HOST': 'host.example.invalid',
                        'NEX_DEPLOY_USER': 'publisher', 'NEX_DEPLOY_KEY': 'key', 'NEX_DEPLOY_KNOWN_HOSTS': 'hosts',
                        'GITHUB_SHA': 'f' * 40}, tools=('ssh', 'scp'))
                    self.assertEqual(result.returncode, 0, result.stderr)
                    remote = ' '.join((root / 'ssh.log').read_text().split())
                    for flag in flags:
                        self.assertIn(flag, remote)
                    self.assertNotIn('apt-publish', remote)
                    # Only the stable channel replaces the live pages.
                    self.assertEqual((root / 'website-deployed').exists(), channel == 'stable')

    def test_forge_release_uses_readable_names_and_matching_checksums(self):
        server = ThreadingHTTPServer(('127.0.0.1', 0), FakeGitea)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        for provider in PROVIDERS:
            for channel, version, public in (
                    ('stable', '0.4.18', '0.4.18'),
                    ('stable', '0.4.19~rc1', '0.4.19-rc1'),
                    ('dev', '0.4.20~~dev.247', '0.4.20-dev.247')):
                with self.subTest(provider=provider, channel=channel, version=version), tempfile.TemporaryDirectory() as tmp:
                    root = Path(tmp)
                    (root / 'scripts').mkdir()
                    shutil.copy(ROOT / 'scripts/gitea-release.py', root / 'scripts')
                    (root / '.cache').mkdir()
                    (root / '.cache/release-notes.md').write_text('Release notes.\n')
                    (root / 'dist').mkdir()
                    expected = {}
                    for suffix in ('amd64.iso', 'amd64-netinst.iso'):
                        data = f'ISO payload: {version} {suffix}'.encode()
                        original = root / f'dist/nex-core-{public}-{suffix}'
                        original.write_bytes(data)
                        original.with_name(original.name + '.sha256').write_text(
                            f'{hashlib.sha256(data).hexdigest()}  {original.name}\n')
                        expected[f'nex-core-{public}-{suffix}'] = data
                    FakeGitea.calls = []
                    FakeGitea.state = {'release': None, 'tag_sha': None}
                    FakeGitea.drop_uploads, FakeGitea.stored_size, FakeGitea.uploads = (), None, 0
                    FakeGitea.reject_uploads = False
                    FakeGitea.release_payload = None
                    result = self.run_step(provider, 'Create or replace the release', root, {
                        'GITHUB_TOKEN': 'token', 'GITHUB_SHA': 'f' * 40, 'NEX_CHANNEL': channel,
                        'NEX_TAG': f'v{public}',
                        'GITHUB_API_URL': f'http://127.0.0.1:{server.server_port}/api/v1',
                        'GITHUB_REPOSITORY': 'o/r'})
                    self.assertEqual(result.returncode, 0, result.stderr)
                    release = FakeGitea.release_payload
                    title = f'Nex Core {public}' + (' (development)' if channel == 'dev' else '')
                    self.assertEqual(release['name'], title)
                    self.assertEqual(release['tag_name'], f'v{public}')
                    self.assertEqual(release['prerelease'], '-' in public)
                    self.assertFalse(release['draft'])
                    uploaded = {
                        parse_qs(urlparse(call[1]).query)['name'][0]: call[2]
                        for call in FakeGitea.calls if call[0] == 'POST' and '/assets?' in call[1]}
                    self.assertEqual(set(uploaded), set(expected) | {name + '.sha256' for name in expected})
                    downloaded = root / 'downloaded'
                    downloaded.mkdir()
                    for name, data in uploaded.items():
                        (downloaded / name).write_bytes(data)
                    for name, data in expected.items():
                        self.assertEqual(uploaded[name], data)
                        checked = subprocess.run(['sha256sum', '-c', name + '.sha256'],
                                                 cwd=downloaded, capture_output=True, text=True)
                        self.assertEqual(checked.returncode, 0, checked.stdout + checked.stderr)


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

    def test_image_workflow_is_forge_only_parallel_and_uncapped(self):
        # Each job builds an image and boots it once: heavy and disposable, so forge-only. They run side by side and take
        # whatever CPU and memory the runner has.
        for provider in PROVIDERS:
            with self.subTest(provider=provider):
                path = ROOT / provider / 'workflows/image.yml'
                text = path.read_text()
                jobs = dict(self.jobs(path))
                self.assertEqual(list(jobs), ['image-build-boot', 'controller-image'])
                for name, block in jobs.items():
                    self.assertIn(self.SKIP, block, name)
                    self.assertIn('timeout-minutes:', block, name)
                    self.assertNotIn('needs:', block, name)
                self.assertNotIn('NEX_DOCKER_CPUS', text)
                self.assertNotIn('NEX_DOCKER_MEMORY', text)
                for step in ('image/build.sh all', 'tests/integration/image_boot.py',
                             'appliance/build.py', 'tests/integration/controller_image_boot.py'):
                    self.assertIn(step, text)
                # The runner is a nested guest that its hypervisor hard-resets while KVM guests run: the labs emulate.
                self.assertIn('NEX_QEMU_ACCEL: tcg', text)
                self.assertIn('NEX_QEMU_SLOWDOWN:', text)
                self.assertNotIn('/dev/kvm', text)

    def test_image_jobs_set_up_go_the_qemu_host_and_the_docker_workspace_before_they_need_them(self):
        # A clean runner has none of this: host-side `go build` in image/build.sh, the QEMU/OVMF/KVM boot labs and the
        # workspace bind mounts of a runner whose job runs in a container each need a step that runs first.
        needs_go = ('image/build.sh', 'appliance/build.py')
        needs_qemu = ('image_boot.py', 'controller_image_boot.py')
        needs_docker_host = ('image/build.sh', 'appliance/build.py', 'tests/integration/')
        for provider in PROVIDERS:
            path = ROOT / provider / 'workflows/image.yml'
            for name, block in self.jobs(path):
                steps = re.split(r'(?m)^      - ', block.split('\n    steps:\n', 1)[1])[1:]
                with self.subTest(provider=provider, job=name):
                    def first(markers):
                        return next((i for i, step in enumerate(steps) if any(m in step for m in markers)), None)
                    go = first(('uses: https://github.com/actions/setup-go@',))
                    if first(needs_go) is not None:
                        self.assertIsNotNone(go, 'a job running go needs the pinned setup-go step')
                        self.assertLess(go, first(needs_go))
                        self.assertRegex(steps[go], r'setup-go@[0-9a-f]{40} ')
                    if first(needs_qemu) is not None:
                        kvm = first(('QEMU lab prerequisites',))
                        self.assertIsNotNone(kvm, 'a QEMU lab job must check its prerequisites')
                        self.assertLess(kvm, first(needs_qemu))
                        if 'controller_image_boot.py' in block:
                            for tool in ('openssl', 'sfdisk', 'mcopy', 'mkfs.vfat'):
                                self.assertIn(tool, steps[kvm], f'the QEMU prerequisite step must check {tool}')
                    if first(needs_docker_host) is not None:
                        host = first(('scripts/ci-host-workspace.sh',))
                        self.assertIsNotNone(host, 'a Docker job must locate its workspace on the Docker host')
                        # A failing substitution inside `echo "...$(cmd)"` does not trip errexit: assign it on its own line.
                        self.assertRegex(steps[host], r'(?m)^\s+ws=\$\(scripts/ci-host-workspace\.sh\)\s*$')
                        self.assertLess(host, first(needs_docker_host))
                        for step in steps[first(needs_docker_host):]:
                            if any(m in step for m in needs_docker_host):
                                self.assertIn('NEX_HOST_WORKSPACE: ${{ steps.host.outputs.workspace }}', step)

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
