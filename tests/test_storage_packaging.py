# SPDX-License-Identifier: AGPL-3.0-only
"""Storage client delivery stays native and does not install ZFS modules."""
from pathlib import Path
import importlib.util
import os
import subprocess
import sys
import tempfile
import sqlite3
import unittest


ROOT = Path(__file__).resolve().parents[1]
CLIENTS = {"nfs-common", "cifs-utils", "open-iscsi"}


class StoragePackagingTests(unittest.TestCase):
    def test_installer_and_local_core_include_native_storage_clients(self):
        installer = set((ROOT / "installer/packages.txt").read_text().split())
        package = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        self.assertTrue(CLIENTS <= installer)
        self.assertTrue(all(client in package for client in CLIENTS))

    def test_zfs_stays_an_optional_host_capability(self):
        package = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        installer = (ROOT / "installer/packages.txt").read_text()
        self.assertNotIn("zfs", package.lower())
        self.assertNotIn("zfs", installer.lower())

    def test_agent_sandbox_allows_only_core_owned_storage_recovery_paths(self):
        unit = (ROOT / "packaging/systemd/nex-core-agent.service").read_text()
        for path in ("/var/lib/nex-storage",
                     "/var/lib/nex-core-agent/storage-credentials",
                     "/etc/systemd/system",
                     "/var/lib/iscsi",
                     "/run/lock/iscsi"):
            self.assertIn("ReadWritePaths=-" + path, unit)
        self.assertNotIn("ReadWritePaths=-/var/lib/nex-core-agent/storage\n", unit)

    def test_packaged_qemu_prepare_hook_uses_the_local_core_guard(self):
        hook = (ROOT / "packaging/core-local/nex-storage-guard.sh").read_text()
        build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        self.assertIn('[ "${2:-}" = prepare ] && [ "${3:-}" = begin ] || exit 0', hook)
        self.assertIn('exec /usr/lib/nex/nex-core-local storage-guard --state-dir /var/lib/nex-core-agent', hook)
        self.assertIn('$pkg/etc/libvirt/hooks/qemu.d', build)
        self.assertIn('packaging/core-local/nex-storage-guard.sh "$pkg/etc/libvirt/hooks/qemu.d/nex-storage-guard"', build)
        self.assertNotIn('/etc/libvirt/hooks/qemu\n', build)

    def test_packaged_hook_preserves_admin_apparmor_policy(self):
        helper = ROOT / "packaging/core-local/libvirtd-apparmor-policy.py"
        build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        postinst = (ROOT / "packaging/core-local/postinst").read_text()
        prerm = (ROOT / "packaging/core-local/prerm").read_text()
        spec = importlib.util.spec_from_file_location("apparmor_policy", helper)
        module = importlib.util.module_from_spec(spec)
        assert spec.loader is not None
        spec.loader.exec_module(module)
        self.assertEqual(module.policy_path(""), Path("/etc/apparmor.d/local/usr.sbin.libvirtd"))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            policy = root / "etc/apparmor.d/local/usr.sbin.libvirtd"
            policy.parent.mkdir(parents=True)
            policy.write_text("# administrator rule\n/usr/local/bin/keep rix,\n")
            for action in ("install", "install", "remove"):
                subprocess.run([str(helper), str(root), action], check=True)
            self.assertEqual(policy.read_text(), "# administrator rule\n/usr/local/bin/keep rix,\n")
            policy.write_bytes(b"# administrator rule without terminal newline")
            subprocess.run([str(helper), str(root), "install"], check=True)
            subprocess.run([str(helper), str(root), "remove"], check=True)
            self.assertEqual(policy.read_bytes(), b"# administrator rule without terminal newline")
            subprocess.run([str(helper), str(root), "install"], check=True)
            rule = policy.read_text()
            self.assertEqual(rule.count("# BEGIN Nex storage guard"), 1)
            self.assertIn("/usr/lib/nex/nex-core-local rix,", rule)
            self.assertNotIn("PUx", rule)
            for malformed in (
                "# BEGIN Nex storage guard\n/usr/lib/nex/nex-core-local rix,\n# administrator trailing rule\n",
                "# BEGIN Nex storage guard\n/usr/lib/nex/nex-core-local rix,\n# END Nex storage guard\n# BEGIN Nex storage guard\n/usr/lib/nex/nex-core-local rix,\n# END Nex storage guard\n",
                "# BEGIN Nex storage guard\n/usr/lib/nex/foreign rix,\n# END Nex storage guard\n",
            ):
                policy.write_text(malformed)
                before = policy.read_bytes()
                for action in ("install", "remove"):
                    result = subprocess.run([str(helper), str(root), action], capture_output=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(policy.read_bytes(), before)
            policy.unlink()
            target = policy.with_name("admin-policy")
            target.write_text("# central administrator policy\n")
            policy.symlink_to(target.name)
            for action in ("install", "remove"):
                result = subprocess.run([str(helper), str(root), action], capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertTrue(policy.is_symlink())
                self.assertEqual(target.read_text(), "# central administrator policy\n")
        self.assertIn("libvirtd-apparmor-policy", build)
        self.assertIn('policy_helper="$root/usr/lib/nex/libvirtd-apparmor-policy"', postinst)
        self.assertIn('systemctl is-active --quiet libvirtd.service', postinst)
        self.assertIn('systemctl try-restart libvirtd.service', postinst)
        self.assertLess(postinst.index('apparmor_parser -r /etc/apparmor.d/usr.sbin.libvirtd'),
                        postinst.index('systemctl try-restart libvirtd.service'))
        self.assertIn('libvirtd-apparmor-policy "${DPKG_ROOT:-}" remove', prerm)
        self.assertIn("apparmor_parser -r /etc/apparmor.d/usr.sbin.libvirtd", prerm)
        self.assertIn("AppArmor is active but apparmor_parser is unavailable", postinst)
        self.assertIn("AppArmor is active but apparmor_parser is unavailable", prerm)

    def test_apt_guard_refuses_legacy_archives_only_when_v2_state_requires_it(self):
        guard = ROOT / "packaging/core-local/storage-downgrade-guard.py"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "bin"
            binary.mkdir()
            dpkg_deb = binary / "dpkg-deb"
            dpkg_deb.write_text("#!/bin/sh\ncase $2 in *v2.deb) echo storage-format-v2;; esac\n")
            dpkg_deb.chmod(0o755)
            env = dict(os.environ, DPKG_ROOT=str(root), PATH=str(binary) + ":" + os.environ["PATH"])
            def guard_result(mode, archive):
                return subprocess.run([sys.executable, str(guard), mode], input=f"VERSION 3\nnex-{'core-local' if mode == '--core' else 'controller'} 1 amd64 none - - none {archive}\n",
                                      text=True, capture_output=True, env=env)
            self.assertEqual(guard_result("--core", "/legacy.deb").returncode, 0)
            journals = root / "var/lib/nex-core-agent/managed-vms"
            journals.mkdir(parents=True)
            (journals / "storage.json").write_text('{"command":{"storage":{"kind":"nfs"}}}')
            self.assertNotEqual(guard_result("--core", "/legacy.deb").returncode, 0)
            self.assertEqual(guard_result("--core", "/v2.deb").returncode, 0)
            receipt = root / "var/lib/nex-core-agent/storage-credentials/receipt.json"
            receipt.parent.mkdir(parents=True)
            receipt.write_text('{}')
            (journals / "storage.json").unlink()
            journals.rmdir()
            self.assertNotEqual(subprocess.run([sys.executable, str(guard), "--core", "--check"], text=True, capture_output=True, env=env).returncode, 0)
            receipt.unlink()
            self.assertEqual(subprocess.run([sys.executable, str(guard), "--core", "--check"], text=True, capture_output=True, env=env).returncode, 0)
            database = root / "var/lib/nex-controller/controller.db"
            database.parent.mkdir(parents=True)
            connection = sqlite3.connect(database)
            try:
                connection.executescript("CREATE TABLE hosts(id TEXT PRIMARY KEY, revoked INTEGER); CREATE TABLE host_capabilities(host_id TEXT, capabilities TEXT); INSERT INTO hosts VALUES('core',0); INSERT INTO host_capabilities VALUES('core','[\"storage-repositories-v1\"]');")
            finally:
                connection.close()
            self.assertNotEqual(guard_result("--controller", "/legacy.deb").returncode, 0)
            self.assertEqual(guard_result("--controller", "/v2.deb").returncode, 0)
            self.assertNotEqual(subprocess.run([sys.executable, str(guard), "--controller", "--check"], text=True, capture_output=True, env=env).returncode, 0)

    def test_packages_install_apt_downgrade_guards_and_v2_artifact_markers(self):
        core_build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        controller_build = (ROOT / "packaging/controller/build-inside.sh").read_text()
        core_postinst = (ROOT / "packaging/core-local/postinst").read_text()
        controller_postinst = (ROOT / "packaging/controller/postinst").read_text()
        for build in (core_build, controller_build):
            self.assertIn("storage-downgrade-guard.py", build)
            self.assertIn("storage-format-v2", build)
        self.assertIn("storage-downgrade-guard --core", core_postinst)
        self.assertIn("storage-downgrade-guard --controller", controller_postinst)
        self.assertIn("storage-downgrade-guard --core --check", (ROOT / "packaging/core-local/prerm").read_text())
        self.assertIn("storage-downgrade-guard --controller --check", (ROOT / "packaging/controller/prerm").read_text())


if __name__ == "__main__":
    unittest.main()
