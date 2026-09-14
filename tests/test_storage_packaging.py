# SPDX-License-Identifier: AGPL-3.0-only
"""Storage client delivery stays native and does not install ZFS modules."""
from pathlib import Path
import hashlib
import importlib.util
import os
import subprocess
import sys
import tempfile
import sqlite3
import shutil
import unittest
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[1]
CLIENTS = {"nfs-common", "libnfs-utils", "cifs-utils", "smbclient", "open-iscsi"}
CLUSTERED_GFS2 = {"corosync", "dlm-controld", "gfs2-utils", "pacemaker", "pacemaker-cli-utils", "pcs",
                  "fence-agents-redfish", "fence-agents-ipmilan", "fence-agents-virsh"}
MATCHED_DLM_VERSION = "4.3.0-3+nex1"


class StoragePackagingTests(unittest.TestCase):
    def test_clustered_iscsi_ocf_probe_is_packaged_and_read_only(self):
        wrapper = ROOT / "packaging/cluster-storage/cluster-iscsi-session"
        text = wrapper.read_text()
        build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        installer = (ROOT / "installer/build.sh").read_text()
        self.assertIn('SPDX-License-Identifier: AGPL-3.0-only', text)
        self.assertIn('readonly core=/usr/lib/nex/nex-core-local', text)
        self.assertIn('readonly timeout=/usr/bin/timeout', text)
        self.assertIn("/usr/lib/ocf/resource.d/nex/cluster-iscsi-session", build)
        self.assertIn("rebuild_iso_artifacts", installer)
        self.assertIn("CORE_LOCAL_DEB", installer)
        self.assertIn('cluster-iscsi-session --validate-only --portal', text)
        self.assertIn('cluster-iscsi-session --portal', text)
        self.assertIn('--kill-after=1s 1s "$core" cluster-iscsi-session --validate-only', text)
        self.assertIn('--kill-after=1s 8s', text)
        self.assertIn("[ \"$(/usr/bin/stat -c '%u:%a' \"$core\")\" = '0:755' ]", text)
        self.assertIn('OCF_NOT_RUNNING=7', text)
        self.assertIn('stop) exit "$OCF_SUCCESS"', text)
        self.assertIn('validate-all) validate; exit $?', text)
        self.assertNotIn('        validate || exit $?\n        check_session', text)
        self.assertGreaterEqual(text.count('required_parameters || exit "$OCF_ERR_CONFIGURED"'), 2)
        for forbidden in ('iscsiadm', 'login', 'logout', 'discovery', 'mount ', 'pcs ', 'cibadmin'):
            self.assertNotIn(forbidden, text.lower())
        result = subprocess.run(["sh", str(wrapper), "meta-data"], check=True,
                                capture_output=True, text=True)
        root = ET.fromstring(result.stdout)
        self.assertEqual(root.tag, "resource-agent")
        self.assertEqual(root.attrib.get("name"), "cluster-iscsi-session")
        self.assertEqual({item.attrib["name"] for item in root.findall("./parameters/parameter")},
                         {"portal", "target", "session_id"})
        parameters = {item.attrib["name"]: item.attrib for item in root.findall("./parameters/parameter")}
        self.assertEqual(parameters["portal"].get("unique"), "0")
        self.assertEqual(parameters["target"].get("unique"), "0")
        self.assertEqual(parameters["session_id"].get("unique"), "1")
        monitor = root.find("./actions/action[@name='monitor']")
        self.assertIsNotNone(monitor)
        self.assertEqual(monitor.attrib.get("interval"), "5s")
        # Stop is a no-op even for a non-root caller; it cannot issue a session query.
        self.assertEqual(subprocess.run(["sh", str(wrapper), "stop"]).returncode, 0)

    def test_iso_rebuilds_dlm_against_the_pinned_pacemaker_fencing_abi(self):
        recipe = (ROOT / "packaging/cluster-storage/build-matched-dlm.sh").read_text()
        dockerfile = (ROOT / "packaging/cluster-storage/Dockerfile").read_text()
        build = (ROOT / "installer/build.sh").read_text()
        late_install = (ROOT / "installer/late-install.sh").read_text()
        self.assertIn('readonly dlm_version=4.3.0-3', recipe)
        self.assertIn('readonly pacemaker_version=3.0.0-2', recipe)
        self.assertIn('pacemaker-dev=3.0.0-2', dockerfile)
        self.assertIn('libstonithd56 (= 3.0.0-2)', recipe)
        self.assertIn('pacemaker-stonith-library-soname.patch', recipe)
        self.assertIn('pacemaker-stonith-ng.h.original', recipe)
        self.assertIn('pacemaker-stonith-ng.h.overlay', recipe)
        self.assertIn('PACEMAKER_CFLAGS="-I$(CURDIR)/debian/nex-pacemaker-overlay', recipe)
        self.assertIn('STONITH_LIBRARY "libstonithd.so.26"', recipe)
        self.assertIn('STONITH_LIBRARY "libstonithd.so.56"', recipe)
        self.assertNotIn('ln -s', recipe)
        self.assertIn("dpkg-buildpackage -us -uc -b", recipe)
        self.assertIn("dpkg-buildpackage -us -uc -S", recipe)
        self.assertIn("dlopen(\"libstonithd.so.56\"", recipe)
        self.assertIn('dlsym(library, "stonith_api_kick")', recipe)
        self.assertNotIn("stonith_api_kick(", recipe)
        self.assertNotIn("stonith_api_time(", recipe)
        self.assertIn(f"dlm-controld_{MATCHED_DLM_VERSION}_amd64.deb", build)
        self.assertIn('rm -f "$stage/nex/repo"/dlm-controld_*.deb', build)
        self.assertIn('APT closure libstonithd56 does not match', build)
        self.assertIn('dlm-pacemaker-abi', late_install)

    def test_installer_and_local_core_include_native_storage_clients(self):
        installer = set((ROOT / "installer/packages.txt").read_text().split())
        package = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        self.assertTrue(CLIENTS <= installer)
        self.assertTrue(all(client in package for client in CLIENTS))

    def test_clustered_gfs2_uses_native_packages_and_stays_disabled_until_configured(self):
        installer = set((ROOT / "installer/packages.txt").read_text().split())
        package = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        late_install = (ROOT / "installer/late-install.sh").read_text()
        postinst = (ROOT / "packaging/core-local/postinst").read_text()
        self.assertTrue(CLUSTERED_GFS2 <= installer)
        self.assertIn("linux-image-amd64", installer)
        # The ISO builder resolves Debian Depends with --no-install-recommends;
        # Pacemaker pulls pacemaker-resource-agents then resource-agents, which
        # supplies ocf:heartbeat:Filesystem. Do not add a redundant direct package.
        iso_builder = (ROOT / "installer/build.sh").read_text()
        self.assertIn('install "${packages[@]}"', iso_builder)
        self.assertIn("pacemaker", installer)
        self.assertIn("fence-agents-virsh", installer)
        self.assertTrue(all(client in package for client in CLUSTERED_GFS2))
        self.assertIn("systemctl disable corosync.service dlm.service pacemaker.service pcsd.service pcsd-ruby.service || true", late_install)
        self.assertNotIn("systemctl enable --now corosync.service", late_install)
        self.assertNotIn("systemctl start corosync.service", late_install)
        self.assertIn('if [ ! -e "$root/etc/corosync/corosync.conf" ]; then', postinst)
        self.assertIn('deb-systemd-helper disable "$unit"', postinst)
        self.assertIn('deb-systemd-invoke stop "$unit" || true', postinst)
        self.assertIn('exit 101', late_install)
        self.assertLess(late_install.index('policy_rc_created='), late_install.index('apt_local install'))
        self.assertIn('/usr/lib/nex/corosync-pristine', late_install)
        self.assertIn('packaging/core-local/corosync-pristine "$pkg/usr/lib/nex/"', package)

    def test_pristine_corosync_marker_never_adopts_a_modified_conffile(self):
        helper = ROOT / "packaging/core-local/corosync-pristine"
        with tempfile.TemporaryDirectory(prefix="nex-corosync-pristine-") as directory:
            root = Path(directory)
            config = root / "etc/corosync/corosync.conf"
            config.parent.mkdir(parents=True)
            config.write_text("cluster_name: debian\n")
            stock_digest = hashlib.sha256(config.read_bytes()).hexdigest()
            expected = subprocess.check_output(["md5sum", str(config)], text=True).split()[0]
            status = root / "var/lib/dpkg/status"
            status.parent.mkdir(parents=True)
            status.write_text("Package: corosync\nStatus: install ok installed\nArchitecture: amd64\nVersion: 3.1.9-2+deb13u1\nMaintainer: Nex test\nDescription: test package\nConffiles:\n /etc/corosync/corosync.conf " + expected + "\n\n")
            result = subprocess.run(["sh", str(helper), str(root)], capture_output=True, text=True)
            marker = root / "var/lib/nex-core-agent/storage-cluster/corosync-unconfigured"
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(marker.stat().st_mode & 0o777, 0o600)
            self.assertEqual(marker.read_text(), stock_digest + "\n")
            config.write_text("cluster_name: administrator-managed\n")
            result = subprocess.run(["sh", str(helper), str(root)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(marker.read_text(), stock_digest + "\n")

    def test_postinst_preserves_an_administrator_corosync_configuration(self):
        for tool in ("dpkg-divert", "dpkg-query"):
            if not shutil.which(tool):
                self.skipTest(f"{tool} missing: Debian dpkg tools are required for this check")
        with tempfile.TemporaryDirectory(prefix="nex-cluster-postinst-") as directory:
            root = Path(directory)
            for sub in ("usr/lib", "etc/corosync", "var/lib/dpkg", "bin"):
                (root / sub).mkdir(parents=True)
            (root / "usr/lib/os-release").write_text("ID=debian\nVERSION_CODENAME=trixie\n")
            (root / "etc/os-release").symlink_to("../usr/lib/os-release")
            admin = root / "etc/corosync/corosync.conf"
            admin.write_text("totem { version: 2 }\n")
            calls = root / "helper-calls"
            helper = root / "bin/deb-systemd-helper"
            helper.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$NEX_TEST_LOG"\n')
            helper.chmod(0o755)
            (root / "var/lib/dpkg/status").write_text(
                "Package: nex-core-local\nStatus: install ok half-configured\nArchitecture: amd64\nVersion: 0.1.5~alpha1\n\n")
            env = dict(os.environ, DPKG_ROOT=str(root), DPKG_ADMINDIR=str(root / "var/lib/dpkg"),
                       NEX_TEST_LOG=str(calls), PATH=f"{root / 'bin'}:{os.environ['PATH']}")
            subprocess.run(["sh", str(ROOT / "packaging/core-local/postinst"), "configure"],
                           env=env, check=True, capture_output=True, text=True)
            self.assertEqual(admin.read_text(), "totem { version: 2 }\n")
            self.assertNotIn("corosync.service", calls.read_text() if calls.exists() else "")

    def test_storage_lab_installs_discovery_clients(self):
        lab = (ROOT / "tests/integration/bug_ledger_lab.py").read_text()
        self.assertTrue(all(client in lab for client in CLIENTS))

    def test_zfs_stays_an_optional_host_capability(self):
        package = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        installer = (ROOT / "installer/packages.txt").read_text()
        self.assertNotIn("zfs", package.lower())
        self.assertNotIn("zfs", installer.lower())

    def test_agent_sandbox_allows_only_core_owned_storage_recovery_paths(self):
        unit = (ROOT / "packaging/systemd/nex-core-agent.service").read_text()
        for path in ("/var/lib/nex-storage",
                     "/var/lib/nex-core-agent/storage-credentials",
                     "/var/lib/nex-core-agent/storage-cluster",
                     "/etc/systemd/system",
                     "/var/lib/iscsi",
                     "/run/lock/iscsi",
                     "/etc/corosync"):
            self.assertIn("ReadWritePaths=-" + path, unit)
        self.assertNotIn("ReadWritePaths=-/var/lib/nex-core-agent/storage\n", unit)
        for path in ("/etc/corosync/authkey", "/var/lib/corosync", "/var/lib/pacemaker",
                     "/var/log/pacemaker", "/run/corosync", "/run/pacemaker", "/run/dlm_controld"):
            self.assertNotIn("ReadWritePaths=-" + path, unit)

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
        self.assertLess(postinst.index('apparmor_parser -r "$apparmor_profile"'),
                        postinst.index('systemctl try-restart libvirtd.service'))
        self.assertIn('libvirtd-apparmor-policy "${DPKG_ROOT:-}" remove', prerm)
        self.assertIn('apparmor_parser -r "$apparmor_profile"', prerm)
        self.assertIn("AppArmor is active but apparmor_parser is unavailable", postinst)
        self.assertIn("AppArmor is active but apparmor_parser is unavailable", prerm)
        # BUG-0092: installer in-target must not reload AppArmor (chroot + .load gate).
        for script in (postinst, prerm):
            self.assertIn("/proc/1/root", script)
            self.assertIn("[ -e /sys/kernel/security/apparmor/.load ]", script)
            self.assertLess(script.index("/proc/1/root"), script.index("apparmor_parser -r"))
            self.assertIn('apparmor_live=yes', script)
            self.assertIn('[ "$apparmor_live" = yes ]', script)
            self.assertIn('[ "$in_chroot" = no ]', script)

    def test_apparmor_live_gate_skips_reload_in_chroot_or_without_load(self):
        """BUG-0092: Debian Installer in-target must not fail package configure on AppArmor."""
        # Mirrors the postinst/prerm gate with injectable inputs. Do not use chmod to
        # simulate non-writable .load: CI runs as root and root still passes [ -w ].
        gate = r'''
set -eu
root=
in_chroot=$1
enabled=$2
load=$3
profile=$4
parser_bin=$5
apparmor_live=no
if [ -z "$root" ] && [ "$in_chroot" = no ] &&
   [ -r "$enabled" ] &&
   grep -qx 'Y' "$enabled" &&
   [ -f "$profile" ] &&
   [ -e "$load" ]; then
    apparmor_live=yes
fi
if [ "$apparmor_live" = yes ] && ! command -v "$parser_bin" >/dev/null 2>&1; then
    echo "AppArmor is active but apparmor_parser is unavailable" >&2
    exit 1
fi
if [ "$apparmor_live" = yes ]; then
    "$parser_bin" -r "$profile"
fi
echo "live=$apparmor_live"
'''
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            enabled = root / "enabled"
            load = root / "load"
            profile = root / "usr.sbin.libvirtd"
            parser = root / "apparmor_parser"
            enabled.write_text("Y\n")
            load.write_text("")
            profile.write_text("# profile\n")
            parser.write_text("#!/bin/sh\necho parser-ran >>\"$0.log\"\nexit 0\n")
            parser.chmod(0o755)

            def run_gate(in_chroot, load_path, parser_path):
                return subprocess.run(
                    ["sh", "-c", gate, "gate", in_chroot, str(enabled), load_path,
                     str(profile), parser_path],
                    capture_output=True, text=True)

            # Installer-like chroot even when .load exists (bind-mounted sysfs).
            chroot = run_gate("yes", str(load), str(parser))
            self.assertEqual(chroot.returncode, 0, chroot.stderr)
            self.assertIn("live=no", chroot.stdout)
            self.assertFalse(parser.with_suffix(".log").exists())

            # Missing .load interface.
            missing = run_gate("no", str(root / "missing-load"), str(parser))
            self.assertEqual(missing.returncode, 0, missing.stderr)
            self.assertIn("live=no", missing.stdout)

            # Live host: .load present requires parser success.
            live = run_gate("no", str(load), str(parser))
            self.assertEqual(live.returncode, 0, live.stderr)
            self.assertIn("live=yes", live.stdout)
            self.assertTrue(parser.with_suffix(".log").exists())

            # Live host without parser must fail closed.
            missing_parser = run_gate("no", str(load), str(root / "no-parser"))
            self.assertNotEqual(missing_parser.returncode, 0)
            self.assertIn("apparmor_parser is unavailable", missing_parser.stderr)

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
