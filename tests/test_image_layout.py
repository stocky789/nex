# SPDX-License-Identifier: AGPL-3.0-only
"""Consistency of the image sources: the places that must agree with internal/hostimage and with each other."""
import importlib.util
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def read(*parts):
    with open(os.path.join(ROOT, *parts)) as f:
        return f.read()


def load_release(label="dev", sequence="0"):
    """image/release.py imported fresh with the build's label and sequence in the environment."""
    old = {k: os.environ.get(k) for k in ("NEX_LABEL", "NEX_SEQUENCE")}
    os.environ.update(NEX_LABEL=label, NEX_SEQUENCE=sequence)
    try:
        spec = importlib.util.spec_from_file_location("nex_release_under_test", os.path.join(ROOT, "image", "release.py"))
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        return mod
    finally:
        for k, v in old.items():
            os.environ.pop(k, None) if v is None else os.environ.__setitem__(k, v)


def go_const(text, name):
    return re.search(rf"\b{name}\s*=\s*([^\s/]+)", text).group(1)


def names(*parts):
    return {l.split("#", 1)[0].strip() for l in read(*parts).splitlines() if l.split("#", 1)[0].strip()}


class ImageLayoutTest(unittest.TestCase):
    def test_denylist_is_not_requested(self):
        self.assertEqual(names("image", "base.packages") & names("image", "denylist.txt"), set())

    def test_data_label_agrees_across_sources(self):
        go = read("internal", "hostimage", "layout.go")
        label = re.search(r'DataLabel\s*=\s*"([^"]+)"', go).group(1)
        self.assertIn(f"LABEL={label} /var ", read("image", "mkosi.extra", "etc", "fstab"))

    def test_overlay_unit_uses_the_upper_the_installer_seeds(self):
        go = read("internal", "hostimage", "install.go")
        upper = re.search(r'etcUpper\s*=\s*"([^"]+)"', go).group(1)
        work = re.search(r'etcWork\s*=\s*"([^"]+)"', go).group(1)
        unit = read("image", "initrd", "usr", "lib", "systemd", "system", "nex-etc-overlay.service")
        self.assertIn(f"upperdir=/sysroot/var/{upper},workdir=/sysroot/var/{work}", unit)
        for forbidden in ("index=on", "metacopy=on", "redirect_dir=on", "xino=on"):
            self.assertNotIn(forbidden, unit)

    def test_esp_sync_unit_runs_a_command_the_cli_has(self):
        unit = read("image", "mkosi.extra", "usr", "lib", "systemd", "system", "nex-esp-sync.service")
        cmd = re.search(r"ExecStart=/usr/bin/nex-hostimage (\S+)", unit).group(1)
        self.assertIn(f'case "{cmd}"', read("cmd", "nex-hostimage", "main.go"))
        self.assertIn("After=nex-bless-boot.service", unit)

    def test_no_host_keys_or_machine_id_in_sources(self):
        for base, _, files in os.walk(os.path.join(ROOT, "image", "mkosi.extra")):
            for f in files:
                self.assertFalse(f.startswith("ssh_host_"), f)
        self.assertFalse(os.path.exists(os.path.join(ROOT, "image", "mkosi.extra", "etc", "machine-id")))
        post = read("image", "mkosi.postinst.chroot")
        self.assertIn(": > /etc/machine-id", post)
        self.assertIn("rm -f /etc/ssh/ssh_host_*_key", post)

    def test_firstboot_generates_keys_only_when_missing(self):
        unit = read("image", "mkosi.extra", "usr", "lib", "systemd", "system", "nex-firstboot.service")
        self.assertIn("ConditionPathExists=!/etc/ssh/ssh_host_ed25519_key", unit)
        self.assertIn("Before=ssh.service", unit)

    def test_initrd_failure_ends_in_a_reboot_never_a_locked_shell(self):
        # The initrd root account is locked and SYSTEMD_SULOGIN_FORCE is not set, so emergency/rescue would end at a
        # console that refuses the login. Every failure reboots after a pause; an armed entry spent its try in the menu.
        d = os.path.join("image", "initrd", "usr", "lib", "systemd", "system")
        paced = read(d, "nex-initrd-reboot-paced.service")
        self.assertIn("ExecStart=/usr/bin/systemctl --force reboot", paced)
        self.assertRegex(paced, r"(?m)^ExecStartPre=/usr/bin/sleep [1-9]\d*$")
        self.assertNotIn("Condition", paced)
        for target in ("emergency.service.d", "rescue.service.d"):
            drop = read(d, target, "10-nex-reboot.conf")
            self.assertRegex(drop, r"(?m)^Wants=nex-initrd-reboot-paced\.service$")
            self.assertRegex(drop, r"(?m)^After=nex-initrd-reboot-paced\.service$")
            self.assertNotIn("ExecStart", drop)

    def test_boot_check_ends_before_its_unit_timeout(self):
        script = read("image", "mkosi.extra", "usr", "libexec", "nex", "nex-boot-check")
        unit = read("image", "mkosi.extra", "usr", "lib", "systemd", "system", "nex-boot-check.service")
        budget = int(re.search(r"NEX_BOOT_CHECK_BUDGET:-(\d+)", script).group(1))
        limit = int(re.search(r"^TimeoutStartSec=(\d+)$", unit, re.M).group(1))
        # budget + one command timeout (30 s) + the bad mark (60 s) must leave the script, not systemd, the last word.
        self.assertGreaterEqual(limit, budget + 30 + 60 + 120)

    def test_boot_check_gives_up_when_its_time_budget_is_used_up(self):
        if os.geteuid() == 0:
            self.skipTest("the script writes below /var and /etc; run it unprivileged")
        script = os.path.join(ROOT, "image", "mkosi.extra", "usr", "libexec", "nex", "nex-boot-check")
        with tempfile.TemporaryDirectory() as stubs:
            for name, body in (("systemctl", "exit 1"), ("findmnt", "echo ext4")):
                with open(os.path.join(stubs, name), "w") as f:
                    f.write("#!/bin/sh\n" + body + "\n")
                os.chmod(os.path.join(stubs, name), 0o755)
            env = dict(os.environ, PATH=stubs + os.pathsep + os.environ["PATH"], NEX_BOOT_CHECK_TRIES="1000",
                       NEX_BOOT_CHECK_WAIT="1", NEX_BOOT_CHECK_BUDGET="2")
            started = time.monotonic()
            run = subprocess.run(["sh", script], env=env, capture_output=True, text=True, timeout=60)
        # 1000 tries of 1 s would take far longer: only the elapsed-time budget ends the loop.
        self.assertLess(time.monotonic() - started, 30)
        self.assertEqual(run.returncode, 1)
        self.assertIn("nex-boot-check:", run.stderr)

    def test_root_home_is_a_link_to_the_directory_tmpfiles_creates(self):
        post = read("image", "mkosi.postinst.chroot")
        target = re.search(r"^ln -s (\S+) /root$", post, re.M).group(1)
        self.assertRegex(read("image", "mkosi.extra", "usr", "lib", "tmpfiles.d", "nex.conf"),
                         rf"(?m)^d /{target} 0700 root root ")

    def test_initrd_assembles_any_nex_data_mirror(self):
        conf = read("image", "initrd", "etc", "mdadm", "mdadm.conf")
        self.assertIn("HOMEHOST <ignore>", conf)
        self.assertFalse(re.search(r"^ARRAY", conf, re.M))

    def test_release_uki_name_matches_the_go_reader(self):
        py, go = read("image", "release.py"), read("internal", "hostimage", "release.go")
        self.assertIn('uki = f"nex-core_{LABEL}_{SEQ}.efi"', py)
        self.assertIn("nex-core_[A-Za-z0-9.~_-]+_[0-9]+", go)

    def test_release_label_is_one_the_go_reader_accepts(self):
        rel = load_release()
        pattern = re.search(r"nex-core_(\[[^\]]+\]\+)_", read("internal", "hostimage", "release.go")).group(1)
        self.assertEqual(rel.LABEL_RE, pattern)
        for bad in ("a+b", "a b", "a/b", "", "é"):
            with self.assertRaises(SystemExit, msg=bad):
                load_release(bad)
        with self.assertRaises(SystemExit):
            load_release("dev", "-1")
        load_release("2026.10.2-1~rc_1", "7")

    def test_uki_sorts_by_sequence_whatever_the_label(self):
        # sd-boot orders one IMAGE_ID by IMAGE_VERSION (VERSION when absent) as a version string, newest first. The
        # sequence alone, zero padded, makes that the numeric order BootOrder models; a label in it would not.
        entries = []
        for seq, label in ((2, "2026.10.2"), (3, "2026.10.2-1"), (10, "2026.9"), (9, "2026.10.2.1")):
            fields = dict(l.split("=", 1) for l in load_release(label, str(seq)).uki_os_release("Nex Core", "nex-core").splitlines())
            self.assertEqual(fields["VERSION"], fields["IMAGE_VERSION"])
            entries.append((fields["IMAGE_VERSION"].strip('"'), seq))
        self.assertEqual([seq for _, seq in sorted(entries)], sorted(seq for _, seq in entries))

    def test_data_partition_number_agrees_with_the_installer(self):
        go = read("internal", "hostimage", "install.go")
        index = go_const(go, "DataIndex")
        installer = read("installer", "nex-image-install.sh")
        self.assertEqual(set(re.findall(r'partition "\$disk" (\d+)', installer)), {index})
        self.assertIn('Planned{DataIndex, DataLabel,', read("internal", "hostimage", "layout.go"))

    def test_installer_finds_the_data_volume_by_the_label_the_plan_writes(self):
        label = go_const(read("internal", "hostimage", "layout.go"), "DataLabel").strip('"')
        installer = read("installer", "nex-image-install.sh")
        for probe in (f"E:ID_PART_ENTRY_NAME={label}", f"E:ID_FS_LABEL={label}", f"$DEV/md/{label}"):
            self.assertIn(probe, installer)

    def test_partition_names_in_the_installer_and_labs_exist_in_the_plan(self):
        layout = read("internal", "hostimage", "layout.go")
        planned = set(re.findall(r'\{"(nex-[a-z0-9-]+)", type', layout))
        planned |= {go_const(layout, name).strip('"') for name in ("BIOSLabel", "ESPLabel", "DataLabel")}
        self.assertEqual(len(planned), 7)
        used = set()
        for path in (("installer", "nex-image-install.sh"), ("tests", "integration", "image_install_lab.py"),
                     ("tests", "integration", "image_sb_lab.py")):
            used |= set(re.findall(r"\bnex-(?:esp|data|(?:root|verity)-[a-z0-9]+)\b", read(*path)))
        self.assertTrue(used)
        self.assertLessEqual(used, planned)

    def test_root_can_sign_in_at_the_console_and_over_ssh(self):
        # Found in the first installed-host boots of the lab ISO: login(1) is Priority required but not Essential, so
        # the base tree had no /usr/bin/login and agetty ended the serial login before the password prompt; and sshd's
        # default PermitRootLogin refuses a password, so the root password the installer was given worked nowhere.
        self.assertIn("login", names("image", "base.packages"))
        self.assertIn("login", names("image", "controller", "controller.packages"))
        self.assertIn("login", {l.split("\t")[0] for l in read("image", "packages.lock").splitlines()})
        conf = {l.split()[0]: l.split()[1] for l in read("image", "mkosi.extra", "etc", "ssh", "sshd_config.d", "00-nex-core.conf").splitlines()
                if l.strip() and not l.startswith("#")}
        self.assertEqual(conf, {"PermitRootLogin": "yes", "PasswordAuthentication": "yes"})


class ControllerProfileTest(unittest.TestCase):
    """The Controller guest image (image/controller) against the two-disk contract of internal/vmhost and the single-disk build."""

    def test_data_disk_is_the_second_virtio_disk_the_domain_defines(self):
        go = read("internal", "vmhost", "engine.go")
        self.assertIn('Source: sourceXML{File: j.Disk}, Target: targetXML{Dev: "vdb", Bus: "virtio"}', go)
        fstab = read("image", "controller", "mkosi.extra", "etc", "fstab")
        # Made on a blank disk only (x-systemd.makefs) and mounted in the initrd, where the /etc overlay needs it.
        self.assertRegex(fstab, r"(?m)^/dev/vdb /var ext4 [^ ]*x-initrd\.mount[^ ]*x-systemd\.makefs ")

    def test_guest_has_no_apt_update_helper(self):
        payload = read("appliance", "build-controller-payload.sh") + read("image", "controller", "mkosi.postinst.chroot")
        packages = names("image", "controller", "controller.packages")
        for forbidden in ("apply-update", "nex-controller-update", "updates.conf", "nex.sources"):
            self.assertNotIn(forbidden, payload)
        self.assertEqual({p for p in packages if p in ("apt", "dpkg", "unattended-upgrades")}, set())

    def test_initrd_overlay_parts_exist_and_the_wants_entry_stays_a_link(self):
        release = read("image", "release.py")
        parts = re.findall(r'"(usr/lib/systemd/system/[^"]+)"', release[release.index("def controller"):])
        self.assertTrue(parts)
        for part in parts:
            self.assertTrue(os.path.lexists(os.path.join(ROOT, "image", "initrd", part)), part)
        # systemd ignores a wants entry that is a copy, which left /etc read-only in the first boot of the guest.
        link = os.path.join(ROOT, "image", "initrd", "usr", "lib", "systemd", "system", "initrd-fs.target.wants", "nex-etc-overlay.service")
        self.assertTrue(os.path.islink(link))
        self.assertIn("follow_symlinks=False", release)

    def test_readiness_marker_path_is_writable_on_the_data_disk(self):
        # The root is read-only: the marker the host probes is a link into /var/lib/nex-controller, written through by firstboot.
        post = read("image", "controller", "mkosi.postinst.chroot")
        self.assertIn("ln -s /var/lib/nex-controller/appliance-ready.json /usr/local/share/nex/dist/appliance-ready.json", post)
        self.assertIn(".resolve()", read("appliance", "firstboot.py"))


if __name__ == "__main__":
    unittest.main()
