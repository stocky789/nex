# SPDX-License-Identifier: AGPL-3.0-only
"""Consistency of the image sources: the places that must agree with internal/hostimage and with each other."""
import os
import re
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def read(*parts):
    with open(os.path.join(ROOT, *parts)) as f:
        return f.read()


def names(*parts):
    return {l.split("#", 1)[0].strip() for l in read(*parts).splitlines() if l.split("#", 1)[0].strip()}


class ImageLayoutTest(unittest.TestCase):
    def test_denylist_is_not_requested(self):
        self.assertEqual(names("image", "base.packages") & names("image", "denylist.txt"), set())

    def test_classic_package_not_in_base_is_a_layer_or_dropped(self):
        layer_dir = os.path.join(ROOT, "image", "mkosi.images")
        layered = set()
        for layer in os.listdir(layer_dir) if os.path.isdir(layer_dir) else []:
            path = os.path.join(layer_dir, layer, "layer.packages")
            if os.path.exists(path):
                layered |= names("image", "mkosi.images", layer, "layer.packages")
        classic = names("installer", "packages.txt")
        missing = classic - names("image", "base.packages") - names("image", "denylist.txt") - layered
        # The "smb" group is excluded from the base by the assignment, but no layer ships it yet: listed here so
        # the gap is explicit; remove the entries once a layer ships them.
        missing -= {"cifs-utils", "smbclient"}
        # What neither the base, a layer nor the denylist covers must be an explicit decision, not an accident.
        self.assertEqual(missing, set(), "installer/packages.txt entries the image silently loses")

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
        self.assertIn("After=systemd-bless-boot.service", unit)

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

    def test_emergency_and_rescue_reboot_in_the_initrd(self):
        d = os.path.join("image", "initrd", "usr", "lib", "systemd", "system")
        for unit in ("emergency.service.d", "rescue.service.d"):
            self.assertIn("systemctl --force reboot", read(d, unit, "10-nex-reboot.conf"))

    def test_initrd_assembles_any_nex_data_mirror(self):
        conf = read("image", "initrd", "etc", "mdadm", "mdadm.conf")
        self.assertIn("HOMEHOST <ignore>", conf)
        self.assertFalse(re.search(r"^ARRAY", conf, re.M))

    def test_release_uki_name_matches_the_go_reader(self):
        py, go = read("image", "release.py"), read("internal", "hostimage", "release.go")
        self.assertIn('uki = f"nex-core_{LABEL}_{SEQ}.efi"', py)
        self.assertIn("nex-core_[A-Za-z0-9.~_-]+_[0-9]+", go)

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

    def test_guest_has_every_package_the_single_disk_appliance_installs(self):
        script = read("appliance", "build-inside.sh")
        installed = set(re.search(r"package-service-policy install ([^']+)'", script).group(1).split())
        self.assertLessEqual(installed, names("image", "controller", "controller.packages"))

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
