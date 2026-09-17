# SPDX-License-Identifier: AGPL-3.0-only
"""Nex Core behaves as a distribution, not as packages added to a Debian system.

Two properties are checked here without installing anything on this workstation:
the package refuses a fresh install onto a system its installer did not create, and
both packages upgrade through apt against the signed Nex archive rather than by
installing a bare .deb, which cannot satisfy dependencies.
"""
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PREINST = ROOT / "packaging/core-local/preinst"
STAMP = "etc/nex/core-release"
KEYRING = "/usr/share/keyrings/nex-archive-keyring.gpg"
APT_SOURCE = "/etc/apt/sources.list.d/nex.sources"


def run_preinst(root, *args):
    return subprocess.run(
        ["sh", str(PREINST), *args],
        env={"PATH": "/usr/bin:/bin", "DPKG_ROOT": str(root)},
        check=False,
        capture_output=True,
        text=True,
    )


def stamp(root):
    path = Path(root) / STAMP
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("NEX_CORE=1\nINSTALLED_BY=nex-core-iso\n")


class InstallGuardTests(unittest.TestCase):
    def test_fresh_install_without_stamp_is_refused(self):
        with tempfile.TemporaryDirectory() as root:
            result = run_preinst(root, "install")
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertIn("not installed from the Nex Core ISO", result.stderr)
            self.assertIn("nexvirtualization.com", result.stderr)

    def test_fresh_install_with_stamp_is_allowed(self):
        with tempfile.TemporaryDirectory() as root:
            stamp(root)
            result = run_preinst(root, "install")
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_upgrade_without_stamp_is_allowed(self):
        # Hosts provisioned before the guard shipped have no stamp yet; postinst
        # backfills it. Blocking their upgrade would strand them on an old version.
        with tempfile.TemporaryDirectory() as root:
            result = run_preinst(root, "upgrade", "0.1.0")
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_reinstall_over_existing_config_is_allowed(self):
        with tempfile.TemporaryDirectory() as root:
            result = run_preinst(root, "install", "0.1.0")
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_abort_actions_are_not_blocked(self):
        # dpkg calls preinst with abort-upgrade during failure recovery; refusing
        # there would leave the package unconfigured with no way forward.
        with tempfile.TemporaryDirectory() as root:
            result = run_preinst(root, "abort-upgrade", "0.1.0")
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_installer_stamps_the_system_before_installing_packages(self):
        script = (ROOT / "installer/late-install.sh").read_text()
        self.assertIn("/etc/nex/core-release", script)
        self.assertLess(
            script.index("/etc/nex/core-release"),
            script.index("apt_local install"),
            "the stamp must exist before the package install the preinst guards",
        )

    def test_package_ships_the_guard(self):
        build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        self.assertIn("packaging/core-local/preinst", build)


class AptUpdatePathTests(unittest.TestCase):
    """Both packages upgrade through apt; neither installs a bare .deb."""

    def test_core_updater_uses_apt_and_not_dpkg_install(self):
        source = (ROOT / "internal/nexupdate/nexupdate.go").read_text()
        self.assertIn('"install", "-y", packageName', source)
        self.assertNotIn('"install", "-y", "--only-upgrade"', source)
        self.assertNotIn('"dpkg", "-i"', source)
        # The archive index is what apt authenticates; no second index is fetched.
        self.assertNotIn("updates.json", source)

    def test_controller_helper_uses_apt_and_not_dpkg_install(self):
        helper = (ROOT / "packaging/controller/apply-update").read_text()
        self.assertIn("'install', '-y', '--only-upgrade'", helper)
        self.assertNotIn("'dpkg', '-i'", helper)
        self.assertNotIn("updates.json", helper)
        # A failed upgrade must not leave the management plane stopped.
        self.assertIn("restart_then_fail", helper)

    def test_publisher_keeps_the_legacy_index_for_pre_apt_hosts(self):
        # Clients no longer read updates.json, but the publisher must keep writing it:
        # hosts released before the apt switch poll it, and a frozen copy would hide
        # every later release from them, stranding them with no in-product upgrade.
        publish = (ROOT / "scripts/apt-publish.py").read_text()
        self.assertIn('_safe_under(repo_root, "updates.json")', publish)
        self.assertIn("newest_packages(packages_text)", publish)
        # The signed suite tree is still what current hosts actually use.
        self.assertIn('Path("dists")', publish)
        self.assertIn("InRelease", publish)

    def test_both_packages_ship_the_archive_keyring_source_and_artwork_license(self):
        for build in ("packaging/core-local/build-inside.sh",
                      "packaging/controller/build-inside.sh"):
            with self.subTest(build=build):
                self.assertIn(KEYRING, (ROOT / build).read_text())
                self.assertIn('ARTWORK-LICENSE.md', (ROOT / build).read_text())
        for script in ("packaging/core-local/postinst",
                       "packaging/controller/postinst"):
            with self.subTest(script=script):
                text = (ROOT / script).read_text()
                self.assertIn(APT_SOURCE, text)
                self.assertIn("Signed-By: " + KEYRING, text)
        # Removal must not leave a source whose key is gone.
        for script in ("packaging/core-local/prerm", "packaging/controller/prerm"):
            with self.subTest(script=script):
                self.assertIn(APT_SOURCE, (ROOT / script).read_text())


class ControllerCgoBuildTests(unittest.TestCase):
    """The Controller links go-sqlite3; a CGO_ENABLED=0 build cannot open its database."""

    def test_controller_builders_have_a_c_toolchain_and_enable_cgo(self):
        for dockerfile in ("packaging/controller/Dockerfile", "appliance/Dockerfile"):
            with self.subTest(dockerfile=dockerfile):
                packages = (ROOT / dockerfile).read_text().split()
                self.assertIn("gcc", packages)
                self.assertIn("libc6-dev", packages)
        for build in ("packaging/controller/build-inside.sh", "appliance/build-inside.sh"):
            with self.subTest(build=build):
                lines = (ROOT / build).read_text().splitlines()
                export = lines.index("export CGO_ENABLED=1")
                go_build = next(i for i, line in enumerate(lines) if line.startswith("go build"))
                self.assertLess(export, go_build)


class VirtioWinReleaseExclusionTests(unittest.TestCase):
    """Public ISO builds must not carry media whose source accounting is incomplete."""

    def test_build_neither_stages_nor_allows_virtio_win(self):
        build = (ROOT / "installer/build.sh").read_text()
        self.assertNotIn("stage_virtio_win", build)
        self.assertNotIn("third-party/virtio-win", build)
        self.assertIn("refusing to publish virtio-win.iso without complete corresponding-source and legal accounting", build)


class ControllerUpdateHelperRecoveryTests(unittest.TestCase):
    """BUG-0193: one failed GUI update must not wedge every later update."""

    def load_helper(self, state):
        import importlib.machinery, importlib.util, types
        loader = importlib.machinery.SourceFileLoader("apply_update", str(ROOT / "packaging/controller/apply-update"))
        spec = importlib.util.spec_from_loader("apply_update", loader)
        helper = importlib.util.module_from_spec(spec)
        loader.exec_module(helper)
        helper.STATE = state
        helper.PENDING = state / "pending-update.json"
        helper.APPLYING = state / "applying-update.json"
        helper.PROGRESS = state / "update-progress.json"
        helper.APT_SOURCE = state / "nex.sources"
        return helper

    def test_failed_apply_consumes_request_and_reports_readable_progress(self):
        import json, os, tempfile, types
        from unittest import mock
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            helper = self.load_helper(state)
            helper.PENDING.write_text(json.dumps({"package": "nex-controller", "repo_url": "https://example.invalid/apt"}))
            done = types.SimpleNamespace(returncode=0, stdout="0.2.4", stderr="")
            failed = types.SimpleNamespace(returncode=100, stdout="", stderr="E: simulated apt failure")
            chowned = []
            with mock.patch.object(helper.os, "geteuid", return_value=0), \
                    mock.patch.object(helper, "run", return_value=done), \
                    mock.patch.object(helper.subprocess, "run", return_value=failed), \
                    mock.patch.object(helper.os, "fchown", side_effect=lambda fd, uid, gid: chowned.append((fd, uid, gid))), \
                    mock.patch.object(helper, "Path", side_effect=lambda value: state / Path(value).name):
                with self.assertRaises(SystemExit):
                    helper.main()
            # A request left behind makes PathExists= re-run the failing helper until
            # systemd's start limit disables the path unit for good.
            self.assertFalse(helper.PENDING.exists(), "failed apply left its request to retrigger the path unit")
            self.assertFalse(helper.APPLYING.exists(), "failed apply left its in-progress marker")
            progress = json.loads(helper.PROGRESS.read_text())
            self.assertEqual(progress["phase"], "failed")
            self.assertIn("simulated apt failure", progress["error"])
            owner = os.stat(state)
            self.assertTrue(any(uid == owner.st_uid and gid == owner.st_gid for _, uid, gid in chowned),
                            "progress was not handed to the state directory owner the Controller runs as")

    def test_package_upgrade_clears_a_wedged_update_path_unit(self):
        postinst = (ROOT / "packaging/controller/postinst").read_text()
        reset = postinst.find("reset-failed nex-controller-update.path nex-controller-update.service")
        start = postinst.find("deb-systemd-invoke start nex-controller-update.path")
        self.assertNotEqual(reset, -1, "postinst never clears a path unit that hit its start limit")
        self.assertLess(reset, start)

    def test_postinst_restarts_unless_the_helper_is_running_and_clears_stale_requests(self):
        # Only a running helper restarts the service itself. A request or marker without a
        # running helper is stale (a killed helper, a wedged trigger): this install supersedes
        # it, and starting the path unit must not then run the helper under the outer apt lock.
        postinst = (ROOT / "packaging/controller/postinst").read_text()
        gate = postinst.find("ActiveState --value nex-controller-update.service")
        clear = postinst.find("rm -f /var/lib/nex-controller/pending-update.json /var/lib/nex-controller/applying-update.json")
        start = postinst.find("deb-systemd-invoke start nex-controller-update.path")
        self.assertNotEqual(gate, -1, "postinst restart is not gated on the helper actually running")
        self.assertNotEqual(clear, -1, "postinst leaves stale update requests to trigger under its apt lock")
        self.assertLess(gate, clear)
        self.assertLess(clear, start)
        self.assertNotIn("! -f /var/lib/nex-controller/applying-update.json", postinst)

    def test_progress_write_does_not_follow_a_planted_symlink(self):
        import json, tempfile
        from unittest import mock
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            helper = self.load_helper(state)
            victim = state / "victim"
            victim.write_text("root-owned content")
            (state / "update-progress.tmp").symlink_to(victim)
            with mock.patch.object(helper.os, "fchown"):
                helper.write_progress("failed", 0, error="x")
            self.assertEqual(victim.read_text(), "root-owned content", "root followed a symlink the Controller user can plant")
            self.assertFalse(helper.PROGRESS.is_symlink())
            self.assertEqual(json.loads(helper.PROGRESS.read_text())["phase"], "failed")

    def run_helper_expecting_exit(self, helper, state, **patches):
        import types
        from unittest import mock
        done = types.SimpleNamespace(returncode=0, stdout="", stderr="")
        with mock.patch.object(helper.os, "geteuid", return_value=0), \
                mock.patch.object(helper.os, "fchown"), \
                mock.patch.object(helper, "Path", side_effect=lambda value: state / Path(value).name), \
                mock.patch.object(helper, "run", patches.get("run", mock.Mock(return_value=done))) as run, \
                mock.patch.object(helper.subprocess, "run", patches.get("subprocess_run", mock.Mock(return_value=done))), \
                mock.patch.object(helper, "installed_version", patches.get("installed_version", mock.Mock(return_value="0.2.4"))), \
                mock.patch.object(helper, "ensure_service_running", patches.get("ensure_service_running", mock.Mock())):
            code = None
            try:
                helper.run_helper()
            except SystemExit as exit:
                code = exit.code
            return code, run

    def test_corrupt_request_is_consumed_and_reported(self):
        import json, tempfile
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            helper = self.load_helper(state)
            helper.PENDING.write_text("not json")
            code, _ = self.run_helper_expecting_exit(helper, state)
            self.assertEqual(code, 1)
            self.assertFalse(helper.PENDING.exists(), "an unreadable request stays to retrigger the path unit")
            self.assertFalse(helper.APPLYING.exists())
            self.assertIn("invalid pending update request", json.loads(helper.PROGRESS.read_text())["error"])

    def test_unexpected_error_releases_the_request_and_restarts_the_service(self):
        import json, tempfile
        from unittest import mock
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            helper = self.load_helper(state)
            helper.PENDING.write_text(json.dumps({"package": "nex-controller"}))
            code, run = self.run_helper_expecting_exit(helper, state, installed_version=mock.Mock(side_effect=FileNotFoundError("dpkg-query")))
            self.assertEqual(code, 1)
            self.assertFalse(helper.APPLYING.exists(), "an unexpected error left the in-progress marker behind")
            self.assertIn("update helper error", json.loads(helper.PROGRESS.read_text())["error"])
            self.assertIn(mock.call(["systemctl", "start", helper.SERVICE], check=False), run.call_args_list)

    def test_request_fields_cannot_inject_apt_configuration_or_other_packages(self):
        # The request file is writable by the Controller user; root must not copy a newline
        # into the APT source (for example "Trusted: yes") or upgrade another package.
        import json, os, tempfile
        cases = {
            "newline in repo_url": {"package": "nex-controller", "repo_url": "https://archive.example.test/apt\nTrusted: yes"},
            "non-http repo_url": {"package": "nex-controller", "repo_url": "file:///srv/evil"},
            "other package": {"package": "openssh-server", "repo_url": "https://archive.example.test/apt"},
        }
        for name, request in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                state = Path(tmp)
                helper = self.load_helper(state)
                helper.PENDING.write_text(json.dumps(request))
                code, _ = self.run_helper_expecting_exit(helper, state)
                self.assertEqual(code, 1)
                self.assertFalse(helper.APT_SOURCE.exists(), "a rejected request still rewrote the APT source")
                self.assertFalse(helper.PENDING.exists())
                self.assertIn("invalid pending update request", json.loads(helper.PROGRESS.read_text())["error"])

    def test_planted_marker_directory_or_fifo_cannot_wedge_the_helper(self):
        import json, os, tempfile
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            helper = self.load_helper(state)
            helper.PENDING.write_text(json.dumps({"package": "nex-controller"}))
            helper.APPLYING.mkdir()
            code, _ = self.run_helper_expecting_exit(helper, state)
            self.assertEqual(code, 1)
            self.assertFalse(helper.PENDING.exists(), "a planted marker directory left the request to retrigger")
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            helper = self.load_helper(state)
            os.mkfifo(helper.PENDING)
            code, _ = self.run_helper_expecting_exit(helper, state)  # must not block reading the FIFO
            self.assertEqual(code, 1)
            self.assertFalse(helper.APPLYING.exists())
            self.assertIn("invalid pending update request", json.loads(helper.PROGRESS.read_text())["error"])

    def test_postinst_clears_stale_progress_with_a_stale_request(self):
        postinst = (ROOT / "packaging/controller/postinst").read_text()
        self.assertIn("/var/lib/nex-controller/update-progress.json", postinst,
                      "a cleared stale request leaves the page on 'Update requested'")

    def test_successful_apply_clears_its_marker_and_a_request_queued_meanwhile(self):
        import json, tempfile, types
        from unittest import mock
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            helper = self.load_helper(state)
            helper.PENDING.write_text(json.dumps({"package": "nex-controller"}))
            def apt(args, **kwargs):
                if "install" in args:
                    helper.PENDING.write_text(json.dumps({"package": "nex-controller"}))  # a second click
                return types.SimpleNamespace(returncode=0, stdout="", stderr="")
            code, _ = self.run_helper_expecting_exit(helper, state, subprocess_run=mock.Mock(side_effect=apt),
                                                     installed_version=mock.Mock(side_effect=["0.2.5", "0.2.6"]))
            self.assertIn(code, (None, 0))
            self.assertFalse(helper.APPLYING.exists())
            self.assertFalse(helper.PENDING.exists(), "a request queued during a successful apply re-runs it and reports failure")
            self.assertEqual(json.loads(helper.PROGRESS.read_text())["phase"], "done")


if __name__ == "__main__":
    unittest.main()
