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

    def test_both_packages_ship_the_archive_keyring_and_source(self):
        for build in ("packaging/core-local/build-inside.sh",
                      "packaging/controller/build-inside.sh"):
            with self.subTest(build=build):
                self.assertIn(KEYRING, (ROOT / build).read_text())
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


if __name__ == "__main__":
    unittest.main()
