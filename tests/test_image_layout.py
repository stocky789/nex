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


if __name__ == "__main__":
    unittest.main()
