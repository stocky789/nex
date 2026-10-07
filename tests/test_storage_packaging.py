# SPDX-License-Identifier: AGPL-3.0-only
"""Storage client delivery uses Debian-native clients and OpenZFS DKMS."""
from pathlib import Path
import hashlib
import os
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[1]
HOST_CLIENTS = {"nfs-common", "libnfs-utils", "open-iscsi"}
CLIENTS = HOST_CLIENTS | {"cifs-utils", "smbclient"}
CLUSTERED_GFS2 = {"corosync", "dlm-controld", "gfs2-utils", "pacemaker", "pacemaker-cli-utils", "pcs",
                  "fence-agents-redfish", "fence-agents-ipmilan", "fence-agents-virsh"}
MATCHED_DLM_VERSION = "4.3.0-3+nex1"
OPTIONAL_FENCING = {"sanlock", "python3-sanlock", "libvirt-daemon-plugin-sanlock", "corosync-qdevice"}


class StoragePackagingTests(unittest.TestCase):
    def test_optional_fencing_packages_are_not_deb_dependencies(self):
        core = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        for package in OPTIONAL_FENCING:
            self.assertNotRegex(core, rf"(?m)^(?:Depends|Recommends):.*\b{package}\b")
        # Guest creation goes through the Core API and qnetd is Controller-hosted.
        payload = (ROOT / "appliance/build-controller-payload.sh").read_text()
        self.assertIn("qnetd-runtime-init", payload)
        self.assertIn("corosync-qnetd-runtime.conf", payload)
        self.assertIn("/etc/systemd/system/corosync-qnetd.service.d/nex-runtime.conf", payload)
        policy = (ROOT / "appliance/package-service-policy.sh").read_text()
        self.assertIn("corosync-qdevice.service", policy)

    def test_qnetd_package_path_is_persistently_redirected_to_runtime_nssdb(self):
        helper = ROOT / "appliance/package-service-policy.sh"
        with tempfile.TemporaryDirectory(prefix="nex-qnetd-runtime-") as directory:
            root = Path(directory)
            package_db = root / "etc/corosync/qnetd/nssdb"
            package_db.mkdir(parents=True)
            (package_db / "old-private-ca.key").write_text("discarded package CA")
            env = dict(os.environ, NEX_PACKAGE_ROOT=str(root),
                       NEX_QNETD_OWNER=str(os.getuid()), NEX_QNETD_GROUP=str(os.getgid()))
            subprocess.run(["sh", str(helper), "runtime-qnetd"], env=env, check=True)
            link = root / "etc/corosync/qnetd/nssdb"
            runtime_db = root / "run/nex-witness/nssdb"
            self.assertTrue(link.is_symlink())
            self.assertEqual(link.readlink().as_posix(), "/run/nex-witness/nssdb")
            self.assertTrue(runtime_db.is_dir())
            self.assertFalse((runtime_db / "old-private-ca.key").exists())
            self.assertEqual((root / "etc/tmpfiles.d/nex-qnetd.conf").read_text(),
                             "d /run/nex-witness/nssdb 0770 root coroqnetd -\n")
            subprocess.run(["sh", str(helper), "runtime-qnetd"], env=env, check=True)
            self.assertEqual(link.readlink().as_posix(), "/run/nex-witness/nssdb")

        runtime_init = (ROOT / "appliance/qnetd-runtime-init").read_text()
        self.assertIn("/usr/bin/corosync-qnetd-certutil -i -G", runtime_init)
        self.assertIn('certutil -L -d "sql:$db" -n \'QNet CA\'', runtime_init)
        self.assertIn("chown root:coroqnetd", runtime_init)
        self.assertIn("chmod 0660", runtime_init)
        unit = (ROOT / "appliance/corosync-qnetd-runtime.conf").read_text()
        self.assertIn("ExecStartPre=+/usr/local/lib/nex/qnetd-runtime-init", unit)
        self.assertIn("-S nss_db_dir=/run/nex-witness/nssdb", unit)
        self.assertIn("After=systemd-tmpfiles-setup.service", unit)

    def test_package_service_policy_blocks_fencing_starts_and_restores_site_policy(self):
        helper = ROOT / "appliance/package-service-policy.sh"
        with tempfile.TemporaryDirectory(prefix="nex-service-policy-") as directory:
            root = Path(directory)
            policy = root / "usr/sbin/policy-rc.d"
            policy.parent.mkdir(parents=True)
            site_policy = root / "usr/local/sbin/site-policy"
            site_policy.parent.mkdir(parents=True)
            site_policy.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$POLICY_CALLS"\nexit 42\n')
            site_policy.chmod(0o755)
            policy.symlink_to("../local/sbin/site-policy")
            original = policy.readlink()
            calls = root / "calls"
            env = dict(os.environ, NEX_PACKAGE_ROOT=str(root), POLICY_CALLS=str(calls))
            subprocess.run(["sh", str(helper), "guard"], env=env, check=True)
            for service in ("sanlock", "wdmd", "corosync-qdevice", "corosync-qnetd",
                            "sanlock.service", "corosync-qdevice.service"):
                result = subprocess.run([str(policy), service, "start"], env=env)
                self.assertEqual(result.returncode, 101, service)
            delegated = subprocess.run([str(policy), "chrony", "restart"], env=env)
            self.assertEqual(delegated.returncode, 42)
            self.assertEqual(calls.read_text(), "chrony restart\n")
            subprocess.run(["sh", str(helper), "restore"], env=env, check=True)
            self.assertTrue(policy.is_symlink())
            self.assertEqual(policy.readlink(), original)

    def test_package_service_policy_defaults_to_denial_and_disables_boot_links(self):
        helper = ROOT / "appliance/package-service-policy.sh"
        with tempfile.TemporaryDirectory(prefix="nex-service-policy-") as directory:
            root = Path(directory)
            env = dict(os.environ, NEX_PACKAGE_ROOT=str(root))
            subprocess.run(["sh", str(helper), "guard"], env=env, check=True)
            policy = root / "usr/sbin/policy-rc.d"
            self.assertEqual(subprocess.run([str(policy), "sanlock", "start"]).returncode, 101)
            self.assertEqual(subprocess.run([str(policy), "chrony", "start"]).returncode, 101)
            subprocess.run(["sh", str(helper), "restore"], env=env, check=True)
            self.assertFalse(policy.exists())

            commands = root / "commands"
            commands.mkdir()
            log = root / "service-actions"
            (commands / "deb-systemd-helper").write_text('#!/bin/sh\nprintf "helper %s\\n" "$*" >> "$ACTION_LOG"\n')
            (commands / "systemctl").write_text(
                '#!/bin/sh\nprintf "systemctl %s\\n" "$*" >> "$ACTION_LOG"\n'
                'case $* in *" is-enabled "*) echo disabled; exit 1;; esac\n')
            (commands / "update-rc.d").write_text(
                '#!/bin/sh\nprintf "rc %s\\n" "$*" >> "$ACTION_LOG"\n'
                'for link in "$NEX_PACKAGE_ROOT"/etc/rc[0-6S].d/S[0-9][0-9]"$1"; do rm -f "$link"; done\n')
            for command in commands.iterdir():
                command.chmod(0o755)
            for service in ("sanlock", "wdmd", "corosync-qdevice", "corosync-qnetd"):
                init = root / "etc/init.d" / service
                init.parent.mkdir(parents=True, exist_ok=True)
                init.touch()
                rc = root / "etc/rc2.d" / f"S20{service}"
                rc.parent.mkdir(parents=True, exist_ok=True)
                rc.symlink_to(f"../init.d/{service}")
            disable_env = dict(env, ACTION_LOG=str(log), PATH=f"{commands}:{os.environ['PATH']}")
            subprocess.run(["sh", str(helper), "disable"], env=disable_env, check=True)
            self.assertEqual([p.name for p in (root / "etc").glob("rc[0-6S].d/S[0-9][0-9]*")], ["S20corosync-qnetd"])
            subprocess.run(["sh", str(helper), "disable", "qnetd"], env=disable_env, check=True)
            self.assertNotIn("start", log.read_text())
            self.assertNotIn("stop", log.read_text())
            self.assertFalse(list((root / "etc").glob("rc[0-6S].d/S[0-9][0-9]*")))

    def test_package_disable_does_not_call_dpkg_maintainer_helper(self):
        helper = ROOT / "appliance/package-service-policy.sh"
        with tempfile.TemporaryDirectory(prefix="nex-service-policy-") as directory:
            root = Path(directory)
            unit = root / "usr/lib/systemd/system/corosync-qnetd.service"
            unit.parent.mkdir(parents=True)
            unit.write_text("[Unit]\nDescription=Test qnetd\n[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n")
            link = root / "etc/systemd/system/multi-user.target.wants/corosync-qnetd.service"
            link.parent.mkdir(parents=True)
            link.symlink_to("/usr/lib/systemd/system/corosync-qnetd.service")
            commands = root / "commands"
            commands.mkdir()
            maintainer_helper = commands / "deb-systemd-helper"
            maintainer_helper.write_text("#!/bin/sh\necho 'not called from dpkg' >&2\nexit 17\n")
            maintainer_helper.chmod(0o755)
            env = dict(os.environ, NEX_PACKAGE_ROOT=str(root), PATH=f"{commands}:{os.environ['PATH']}")
            result = subprocess.run(["sh", str(helper), "disable", "qnetd"], env=env,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(link.is_symlink())

    def test_package_install_failure_restores_existing_service_policy(self):
        helper = ROOT / "appliance/package-service-policy.sh"
        with tempfile.TemporaryDirectory(prefix="nex-service-policy-") as directory:
            root = Path(directory)
            policy = root / "usr/sbin/policy-rc.d"
            policy.parent.mkdir(parents=True)
            policy.write_text("#!/bin/sh\nexit 42\n")
            policy.chmod(0o755)
            commands = root / "commands"
            commands.mkdir()
            apt = commands / "apt-get"
            apt.write_text("#!/bin/sh\nexit 17\n")
            apt.chmod(0o755)
            env = dict(os.environ, NEX_PACKAGE_ROOT=str(root), PATH=f"{commands}:{os.environ['PATH']}")
            result = subprocess.run(["sh", str(helper), "install", "corosync-qnetd"], env=env)
            self.assertEqual(result.returncode, 17)
            self.assertEqual(policy.read_text(), "#!/bin/sh\nexit 42\n")
            self.assertFalse((root / "usr/sbin/.policy-rc.d.nex-package-save").exists())

    def test_package_install_refusal_preserves_policy_and_stale_marker(self):
        helper = ROOT / "appliance/package-service-policy.sh"
        with tempfile.TemporaryDirectory(prefix="nex-service-policy-") as directory:
            root = Path(directory)
            policy = root / "usr/sbin/policy-rc.d"
            policy.parent.mkdir(parents=True)
            policy.write_text("#!/bin/sh\nexit 42\n")
            marker = root / "run/.nex-package-no-policy"
            marker.parent.mkdir()
            marker.touch()
            env = dict(os.environ, NEX_PACKAGE_ROOT=str(root))
            result = subprocess.run(["sh", str(helper), "install", "corosync-qnetd"], env=env,
                                    capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("stale package service policy state exists", result.stderr)
            self.assertEqual(policy.read_text(), "#!/bin/sh\nexit 42\n")
            self.assertTrue(marker.exists())

    def test_package_install_guard_failure_restores_policy(self):
        helper = ROOT / "appliance/package-service-policy.sh"
        with tempfile.TemporaryDirectory(prefix="nex-service-policy-") as directory:
            root = Path(directory)
            policy = root / "usr/sbin/policy-rc.d"
            policy.parent.mkdir(parents=True)
            policy.write_text("#!/bin/sh\nexit 42\n")
            commands = root / "commands"
            commands.mkdir()
            mv = commands / "mv"
            mv.write_text('#!/bin/sh\ncase $2 in *.nex-[0-9]*) exit 17;; esac\nexec /bin/mv "$@"\n')
            mv.chmod(0o755)
            env = dict(os.environ, NEX_PACKAGE_ROOT=str(root), PATH=f"{commands}:{os.environ['PATH']}")
            result = subprocess.run(["sh", str(helper), "install", "corosync-qnetd"], env=env,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 17, result.stderr)
            self.assertEqual(policy.read_text(), "#!/bin/sh\nexit 42\n")
            self.assertFalse((root / "usr/sbin/.policy-rc.d.nex-package-save").exists())

    def test_depends_drop_every_layer_package_and_keep_host_tools(self):
        build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        start = build.index('cat > "$pkg/DEBIAN/control"')
        end = build.index('chmod -R a+rX "$pkg/usr"')
        with tempfile.TemporaryDirectory(prefix="nex-image-depends-") as directory:
            pkg = Path(directory)
            (pkg / "DEBIAN").mkdir()
            script = f'set -eu\npkg={pkg}\nNEX_VERSION=1\n' + build[start:end]
            subprocess.run(["bash", "-c", script], check=True)
            fields = dict(line.split(": ", 1) for line in (pkg / "DEBIAN/control").read_text().splitlines()
                          if not line.startswith(" ") and ": " in line)
        depends = {item.strip() for item in fields["Depends"].split(",")}
        layers = CLUSTERED_GFS2 | {"cifs-utils", "smbclient", "sbd", "zfs-dkms", "zfsutils-linux", "linux-headers-amd64"}
        self.assertFalse(layers & depends, sorted(layers & depends))
        self.assertNotIn("Recommends", fields)
        self.assertIn("ipmitool", depends)
        self.assertTrue(HOST_CLIENTS <= depends)

    def test_clustered_iscsi_ocf_probe_is_packaged_and_read_only(self):
        wrapper = ROOT / "packaging/cluster-storage/cluster-iscsi-session"
        text = wrapper.read_text()
        build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        self.assertIn('SPDX-License-Identifier: AGPL-3.0-only', text)
        self.assertIn('readonly core=/usr/lib/nex/nex-core-local', text)
        self.assertIn('readonly timeout=/usr/bin/timeout', text)
        self.assertIn("/usr/lib/ocf/resource.d/nex/cluster-iscsi-session", build)
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

    def test_release_rebuilds_dlm_against_the_pinned_pacemaker_fencing_abi(self):
        recipe = (ROOT / "packaging/cluster-storage/build-matched-dlm.sh").read_text()
        dockerfile = (ROOT / "packaging/cluster-storage/Dockerfile").read_text()
        build = (ROOT / "scripts/build-release-packages.sh").read_text()
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
        self.assertIn("libstonithd56 (= 3.0.0-2)", build)
        # BuildKit-only syntax breaks a release build on a Docker install without buildx, and
        # only after the appliance and Core package have already been rebuilt.
        # Only repository Dockerfiles: untracked scratch trees (old extracted sources under
        # .cache, build output, vendored modules) are not what an ISO build uses (BUG-0194).
        skip = {'.cache', '.git', 'dist', 'vendor'}
        for name in sorted(p.relative_to(ROOT).as_posix() for p in ROOT.glob('**/Dockerfile*')
                           if 'node_modules' not in p.parts and p.relative_to(ROOT).parts[0] not in skip):
            self.assertNotRegex((ROOT / name).read_text(), r'(?m)^(COPY|ADD|RUN)\s+--(chmod|chown|link|mount)',
                                f'{name} needs BuildKit; the legacy builder cannot run it')

    def test_local_core_includes_native_storage_clients(self):
        package = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        self.assertTrue(all(client in package for client in HOST_CLIENTS))

    def test_iscsi_tcp_is_loaded_at_boot_because_the_sandboxed_agent_cannot_load_it(self):
        # BUG-1933: iscsiadm under ProtectKernelModules=yes fails target discovery unless iscsi_tcp is already loaded.
        unit = (ROOT / "packaging/core-local/nex-core-local.service").read_text()
        self.assertIn("ProtectKernelModules=yes", unit)
        modules = [line.strip() for line in (ROOT / "packaging/core-local/nex-iscsi.conf").read_text().splitlines()
                   if line.strip() and not line.startswith("#")]
        self.assertEqual(modules, ["iscsi_tcp"])
        build = (ROOT / "packaging/core-local/build-inside.sh").read_text()
        self.assertIn('install -m644 packaging/core-local/nex-iscsi.conf "$pkg/etc/modules-load.d/nex-iscsi.conf"', build)
        self.assertIn("/etc/modules-load.d/nex-iscsi.conf\\n' > \"$pkg/DEBIAN/conffiles\"", build)

    def test_lab_pristine_corosync_marker_never_adopts_a_modified_conffile(self):
        helper = ROOT / "tests/integration/fixtures/corosync-pristine"
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

    def test_storage_lab_installs_discovery_clients(self):
        lab = (ROOT / "tests/integration/bug_ledger_lab.py").read_text()
        self.assertTrue(all(client in lab for client in CLIENTS))

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


if __name__ == "__main__":
    unittest.main()
