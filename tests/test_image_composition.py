# SPDX-License-Identifier: AGPL-3.0-only
"""Image composition: layer package sets, the denylist, and what layer-finalize lets into a layer."""
import importlib.machinery
import importlib.util
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader("layer_finalize", str(ROOT / "scripts/layer-finalize.py"))
spec = importlib.util.spec_from_loader(loader.name, loader)
lf = importlib.util.module_from_spec(spec)
loader.exec_module(lf)

release_loader = importlib.machinery.SourceFileLoader("image_release", str(ROOT / "image/release.py"))
release_spec = importlib.util.spec_from_loader(release_loader.name, release_loader)
image_release = importlib.util.module_from_spec(release_spec)
release_loader.exec_module(image_release)

IMAGE = ROOT / "image"
LAYERS = sorted(p.name for p in (IMAGE / "mkosi.images").iterdir() if (p / "layer.json").is_file())


def package_names(path):
    out = []
    for line in path.read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if line:
            out.append(line.split()[0])
    return out


class Tree:
    """A throwaway base tree and layer tree."""

    def __init__(self, test):
        self.tmp = tempfile.TemporaryDirectory()
        test.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name, "base")
        self.layer = Path(self.tmp.name, "layer")
        self.base.mkdir()
        self.layer.mkdir()

    def put(self, root, rel, text="x\n", mode=None):
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        if mode:
            p.chmod(mode)
        return p

    def finalize(self, **spec):
        spec = {"name": "nex-test", **spec}
        return lf.finalize(self.layer, self.base, spec, "20261001.1", 1790000000, None)


class CompositionTest(unittest.TestCase):
    def test_layers_exist_and_specs_are_consistent(self):
        self.assertEqual(LAYERS, ["nex-cluster", "nex-zfs"])
        for name in LAYERS:
            spec = json.loads((IMAGE / "mkosi.images" / name / "layer.json").read_text())
            self.assertEqual(spec["name"], name)
            self.assertTrue(lf.NAME.match(name))
            self.assertFalse(set(spec["units_enable"]) & set(spec["units_never_enable"]), name)

    def test_layer_packages_are_disjoint_from_base_and_each_other(self):
        base = set(package_names(IMAGE / "base.packages"))
        seen = {}
        for name in LAYERS:
            pkgs = set(package_names(IMAGE / "mkosi.images" / name / "layer.packages"))
            self.assertTrue(pkgs, name)
            self.assertFalse(pkgs & base, f"{name} repeats base packages {sorted(pkgs & base)}")
            for other, theirs in seen.items():
                self.assertFalse(pkgs & theirs, f"{name} and {other} share {sorted(pkgs & theirs)}")
            seen[name] = pkgs

    def test_denylist_never_reaches_base_or_layers(self):
        deny = set(package_names(IMAGE / "denylist.txt"))
        self.assertTrue(deny)
        every = set(package_names(IMAGE / "base.packages"))
        for name in LAYERS:
            every |= set(package_names(IMAGE / "mkosi.images" / name / "layer.packages"))
        self.assertFalse(every & deny, sorted(every & deny))

    def test_zfs_layer_ships_no_build_tooling(self):
        pkgs = set(package_names(IMAGE / "mkosi.images/nex-zfs/layer.packages"))
        self.assertFalse({"zfs-dkms", "dkms", "build-essential", "gcc"} & pkgs)

    def test_zfs_mount_unit_reset_drop_in_is_what_the_finalizer_needs(self):
        # The stock Debian unit orders before local-fs.target; the shipped drop-in must make it pass.
        t = Tree(self)
        t.put(t.layer, "usr/lib/systemd/system/zfs-mount.service",
              "[Unit]\nDefaultDependencies=no\nBefore=local-fs.target\nBefore=umount.target\n[Install]\nWantedBy=zfs.target\n")
        with self.assertRaisesRegex(lf.Failure, "orders before local-fs.target"):
            t.finalize()
        drop = (IMAGE / "mkosi.images/nex-zfs/mkosi.extra/usr/lib/systemd/system/zfs-mount.service.d/10-nex-layer.conf").read_text()
        t.put(t.layer, "usr/lib/systemd/system/zfs-mount.service.d/10-nex-layer.conf", drop)
        t.finalize()

    def test_zfs_pools_mount_before_the_services_that_use_them(self):
        # Resetting Before=local-fs.target must not leave libvirtd and Core free to start on unmounted datasets.
        stock = "[Unit]\nDefaultDependencies=no\nBefore=local-fs.target\nBefore=umount.target\n"
        for name in ("zfs-mount.service", "zfs-mount@.service"):
            with tempfile.TemporaryDirectory() as tmp:
                unit = Path(tmp, name)
                unit.write_text(stock)
                drop = IMAGE / "mkosi.images/nex-zfs/mkosi.extra/usr/lib/systemd/system" / f"{name}.d/10-nex-layer.conf"
                before = set(lf.parse_unit(unit, [drop])[("Unit", "Before")])
            self.assertEqual(before, {"umount.target", "libvirtd.service", "nex-core-local.service"}, name)

    def test_busy_commands_reach_the_metadata_core_reads(self):
        t = Tree(self)
        busy = [["zpool", "list", "-H", "-o", "name"]]
        t.finalize(busy_commands=busy, busy_units=["x.service"])
        meta = json.loads((t.layer / "usr/lib/nex/layers/nex-test.json").read_text())
        self.assertEqual(meta["busy_commands"], busy)
        for name in LAYERS:
            spec = json.loads((IMAGE / "mkosi.images" / name / "layer.json").read_text())
            self.assertEqual(lf_meta_busy(spec), spec.get("busy_commands", []), name)


def lf_meta_busy(spec):
    """busy_commands of a shipped layer after finalizing it into an empty tree."""
    with tempfile.TemporaryDirectory() as tmp:
        base, layer = Path(tmp, "base"), Path(tmp, "layer")
        base.mkdir()
        layer.mkdir()
        lf.finalize(layer, base, {**spec, "depmod": False}, "20261001.1", 1790000000, None)
        return json.loads((layer / "usr/lib/nex/layers" / f"{spec['name']}.json").read_text())["busy_commands"]


class BootChainTest(unittest.TestCase):
    def tree(self, tmp, missing=None):
        files = {"usr/lib/shim/shimx64.efi.signed": "shim", "usr/lib/shim/mmx64.efi.signed": "mok",
                 "usr/lib/systemd/boot/efi/systemd-bootx64.efi.signed": "sdboot",
                 "usr/lib/systemd/boot/efi/systemd-bootx64.efi": "unsigned-sdboot"}
        for rel, text in files.items():
            if rel != missing:
                p = Path(tmp, "tree", rel)
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text(text)
        esp = Path(tmp, "esp")
        for d in ("EFI/BOOT", "EFI/systemd"):
            (esp / d).mkdir(parents=True)
        return Path(tmp, "tree"), esp

    def test_removable_path_starts_shim_which_chains_to_the_signed_systemd_boot(self):
        with tempfile.TemporaryDirectory() as tmp:
            tree, esp = self.tree(tmp)
            image_release.boot_files(str(tree), str(esp))
            got = {str(p.relative_to(esp)): p.read_text() for p in esp.rglob("*") if p.is_file()}
        self.assertEqual(got, {"EFI/BOOT/BOOTX64.EFI": "shim", "EFI/BOOT/grubx64.efi": "sdboot",
                               "EFI/BOOT/mmx64.efi": "mok", "EFI/systemd/systemd-bootx64.efi": "sdboot"})

    def test_an_unsigned_loader_is_never_substituted(self):
        for missing in ("usr/lib/systemd/boot/efi/systemd-bootx64.efi.signed", "usr/lib/shim/shimx64.efi.signed"):
            with tempfile.TemporaryDirectory() as tmp:
                tree, esp = self.tree(tmp, missing)
                with self.assertRaises(FileNotFoundError):
                    image_release.boot_files(str(tree), str(esp))


class OsReleaseStampTest(unittest.TestCase):
    """nex-update reads the compatibility record of the running image from os-release; the build must stamp all of it."""

    def stamped(self, **env):
        script = (IMAGE / "mkosi.postinst.chroot").read_text()
        start = script.index("label=")
        end = script.index("\nEOT\n", start)
        # Only the variable assignments and the os-release heredoc: the rest of the script edits the tree it runs in.
        body = script[start:end].replace("cat > /usr/lib/os-release <<EOT", "cat <<EOT") + "\nEOT\n"
        out = subprocess.run(["sh", "-c", body], env={"PATH": os.environ["PATH"], **env}, capture_output=True, text=True, check=True).stdout
        return dict(line.split("=", 1) for line in out.splitlines() if line.startswith("NEX_"))

    def test_every_compatibility_key_the_update_engine_reads_is_stamped(self):
        self.assertEqual(self.stamped(), {"NEX_STATE_FORMAT": "3", "NEX_MIN_STATE_FORMAT": "0", "NEX_MD_GENERATION": "0",
                                          "NEX_MD_FALLBACK_SAFE": "0"})
        got = self.stamped(NEX_STATE_FORMAT="4", NEX_MIN_STATE_FORMAT="2", NEX_MD_GENERATION="5", NEX_MD_FALLBACK_SAFE="1")
        self.assertEqual(got, {"NEX_STATE_FORMAT": "4", "NEX_MIN_STATE_FORMAT": "2", "NEX_MD_GENERATION": "5",
                               "NEX_MD_FALLBACK_SAFE": "1"})


class InstallationStampTest(unittest.TestCase):
    """nex-core-local's preinst refuses a fresh install on a tree without the system stamp; mkosi installs the package
    into a fresh tree built from image/mkosi.skeleton, so the skeleton must carry the stamp."""

    def preinst(self, root):
        return subprocess.run(["sh", str(ROOT / "packaging/core-local/preinst"), "install"],
                              env={**os.environ, "DPKG_ROOT": str(root)}, capture_output=True, text=True)

    def test_a_tree_built_from_the_image_skeleton_accepts_the_package(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertNotEqual(self.preinst(tmp).returncode, 0)
            shutil.copytree(IMAGE / "mkosi.skeleton", tmp, dirs_exist_ok=True)
            result = self.preinst(tmp)
        self.assertEqual(result.returncode, 0, result.stderr)


class FinalizeTest(unittest.TestCase):
    def test_etc_moves_to_factory_with_tmpfiles_rule_and_identity_is_written(self):
        t = Tree(self)
        t.put(t.layer, "etc/zfs/zed.d/zed.rc", "ZED=1\n")
        t.put(t.layer, "usr/bin/tool")
        t.put(t.layer, "usr/lib/extension-release.d/extension-release.other", "ID=debian\n")
        r = t.finalize(units_enable=["a.service"], provides=["a"])
        self.assertFalse((t.layer / "etc").exists())
        self.assertEqual((t.layer / "usr/share/factory/etc/zfs/zed.d/zed.rc").read_text(), "ZED=1\n")
        self.assertIn("C+ /etc/zfs - - - -", (t.layer / "usr/lib/tmpfiles.d/nex-test-etc.conf").read_text())
        self.assertEqual(r["etc_to_factory"], ["/etc/zfs"])
        rel = t.layer / "usr/lib/extension-release.d"
        self.assertEqual([p.name for p in rel.iterdir()], ["extension-release.nex-test"])
        self.assertEqual((rel / "extension-release.nex-test").read_text(), "ID=nex-core\nSYSEXT_LEVEL=20261001.1\n")
        meta = json.loads((t.layer / "usr/lib/nex/layers/nex-test.json").read_text())
        self.assertEqual((meta["name"], meta["requires_base"], meta["units_enable"]), ("nex-test", "20261001.1", ["a.service"]))
        self.assertEqual(os.stat(t.layer / "usr/bin/tool").st_mtime, 1790000000)

    def test_new_accounts_become_sysusers_with_their_exact_ids(self):
        t = Tree(self)
        t.put(t.base, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\n")
        t.put(t.base, "etc/group", "root:x:0:\n")
        t.put(t.layer, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\nhacluster:x:105:107:HA cluster:/var/lib/pacemaker:/usr/sbin/nologin\n")
        t.put(t.layer, "etc/group", "root:x:0:\nhaclient:x:107:hacluster\n")
        r = t.finalize()
        text = (t.layer / "usr/lib/sysusers.d/nex-test.conf").read_text()
        self.assertIn("g haclient 107", text)
        self.assertIn('u hacluster 105:haclient "HA cluster" /var/lib/pacemaker', text)
        self.assertIn("m hacluster haclient", text)
        self.assertTrue(r["sysusers"])
        self.assertFalse((t.layer / "etc").exists())

    def test_account_id_that_differs_from_the_base_fails(self):
        t = Tree(self)
        t.put(t.base, "etc/passwd", "svc:x:100:100::/:/bin/false\n")
        t.put(t.layer, "etc/passwd", "svc:x:101:100::/:/bin/false\n")
        with self.assertRaisesRegex(lf.Failure, "uid 101 differs"):
            t.finalize()

    def test_enablement_links_are_dropped_but_real_overrides_fail(self):
        t = Tree(self)
        t.put(t.layer, "usr/lib/systemd/system/pacemaker.service", "[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n")
        wants = t.layer / "etc/systemd/system/multi-user.target.wants"
        wants.mkdir(parents=True)
        (wants / "pacemaker.service").symlink_to("/usr/lib/systemd/system/pacemaker.service")
        r = t.finalize()
        self.assertFalse((t.layer / "etc").exists())
        self.assertIn("etc/systemd/system/multi-user.target.wants/pacemaker.service", r["etc_dropped"])
        t2 = Tree(self)
        t2.put(t2.layer, "etc/systemd/system/x.service", "[Service]\n")
        with self.assertRaisesRegex(lf.Failure, "etc/systemd/system"):
            t2.finalize()

    def test_unclassified_var_and_files_outside_usr_fail_unless_allowed(self):
        t = Tree(self)
        t.put(t.layer, "var/lib/pacemaker/state")
        t.put(t.layer, "usr/bin/tool")
        with self.assertRaisesRegex(lf.Failure, "var/lib/pacemaker/state"):
            t.finalize()
        t.finalize(allow_var=["var/lib/pacemaker/*"])

    def test_dpkg_state_is_exported_then_removed(self):
        t = Tree(self)
        status = "Package: corosync\nStatus: install ok installed\nArchitecture: amd64\nVersion: 3.1\n\nPackage: bash\nStatus: install ok installed\nArchitecture: amd64\nVersion: 5\n"
        t.put(t.base, "var/lib/dpkg/status", "Package: bash\nStatus: install ok installed\nArchitecture: amd64\nVersion: 5\n")
        t.put(t.layer, "var/lib/dpkg/status", status)
        t.put(t.layer, "usr/bin/tool")
        r = t.finalize()
        self.assertEqual(r["packages"], ["corosync"])
        self.assertEqual((t.layer / "usr/share/nex/layers/nex-test.packages").read_text(), "corosync 3.1 amd64\n")
        self.assertFalse((t.layer / "var").exists())

    def test_package_names_come_from_copyright_files_without_a_dpkg_database(self):
        t = Tree(self)
        t.put(t.layer, "usr/share/doc/zfs-zed/copyright")
        t.put(t.layer, "usr/share/doc/zfsutils-linux/copyright")
        t.put(t.layer, "usr/share/doc/stray/README")
        r = t.finalize()
        self.assertEqual(r["packages"], ["zfs-zed", "zfsutils-linux"])

    @unittest.skipUnless(shutil.which("systemd-tmpfiles"), "needs systemd-tmpfiles")
    def test_package_defaults_reach_a_shared_etc_directory_without_replacing_operator_files(self):
        # tmpfiles.d(5): a plain C rule skips the whole copy when the destination directory exists and is not empty,
        # which is what /etc/default, /etc/logrotate.d and the like are on every host. Run the real consumer.
        t = Tree(self)
        t.put(t.layer, "etc/default/zfs", "package default\n")
        t.put(t.layer, "etc/default/shared", "package version\n")
        t.put(t.layer, "etc/zfs/zed.d/zed.rc", "ZED=1\n")
        t.finalize()
        host = Path(t.tmp.name, "host")
        shutil.copytree(t.layer / "usr", host / "usr")
        t.put(host, "etc/default/other", "operator file\n")
        t.put(host, "etc/default/shared", "operator edit\n")
        subprocess.run(["systemd-tmpfiles", "--root", str(host), "--create"], check=True, capture_output=True)
        self.assertEqual((host / "etc/default/zfs").read_text(), "package default\n")
        self.assertEqual((host / "etc/default/shared").read_text(), "operator edit\n")
        self.assertEqual((host / "etc/default/other").read_text(), "operator file\n")
        self.assertEqual((host / "etc/zfs/zed.d/zed.rc").read_text(), "ZED=1\n")

    def test_host_specific_etc_files_are_not_shared_through_factory(self):
        t = Tree(self)
        t.put(t.layer, "etc/hostid", "abcd")
        t.put(t.layer, "etc/zfs/zed.d/zed.rc")
        t.finalize()
        self.assertFalse((t.layer / "usr/share/factory/etc/hostid").exists())

    def test_a_file_that_shadows_the_base_fails(self):
        t = Tree(self)
        t.put(t.base, "usr/bin/ls")
        t.put(t.layer, "usr/bin/ls")
        with self.assertRaisesRegex(lf.Failure, "usr/bin/ls"):
            t.finalize()
        t.finalize(allow_collisions=["usr/bin/ls"])

    def test_mount_units_and_early_wanted_by_fail(self):
        t = Tree(self)
        t.put(t.layer, "usr/lib/systemd/system/data.mount", "[Mount]\nWhat=x\n")
        with self.assertRaisesRegex(lf.Failure, "mount units"):
            t.finalize()
        t2 = Tree(self)
        t2.put(t2.layer, "usr/lib/systemd/system/early.service", "[Unit]\n[Install]\nWantedBy=sysinit.target\n")
        with self.assertRaisesRegex(lf.Failure, "sysinit.target"):
            t2.finalize()

    def test_units_hung_off_early_targets_by_wants_or_requires_directories_fail(self):
        for target, suffix in (("sysinit.target", "wants"), ("sysinit.target", "requires"), ("local-fs.target", "requires"),
                               ("basic.target", "requires"), ("basic.target", "upholds")):
            with self.subTest(directory=f"{target}.{suffix}"):
                t = Tree(self)
                t.put(t.layer, "usr/lib/systemd/system/early.service", "[Service]\nExecStart=/bin/true\n")
                link = t.layer / f"usr/lib/systemd/system/{target}.{suffix}/early.service"
                link.parent.mkdir(parents=True)
                link.symlink_to("../early.service")
                with self.assertRaisesRegex(lf.Failure, f"units hang off {re.escape(target)}"):
                    t.finalize()
        t = Tree(self)
        t.put(t.layer, "usr/lib/systemd/system/late.service", "[Service]\nExecStart=/bin/true\n")
        link = t.layer / "usr/lib/systemd/system/multi-user.target.requires/late.service"
        link.parent.mkdir(parents=True)
        link.symlink_to("../late.service")
        t.finalize()

    def test_modules_for_a_kernel_the_base_lacks_fail(self):
        t = Tree(self)
        t.put(t.layer, "usr/lib/modules/6.12.1/updates/x.ko")
        t.put(t.base, "usr/lib/modules/6.12.2/modules.dep")
        with self.assertRaisesRegex(lf.Failure, "base does not carry"):
            t.finalize(depmod=True)


class ReproducibilityTest(unittest.TestCase):
    PAYLOAD = [
        ("etc/zfs/zed.d/zed.rc", "ZED=1\n"),
        ("etc/passwd", "root:x:0:0:root:/root:/bin/bash\nsvc:x:105:107::/var/lib/svc:/usr/sbin/nologin\nalso:x:106:107::/:/usr/sbin/nologin\n"),
        ("etc/group", "root:x:0:\nsvcgrp:x:107:svc,also\n"),
        ("usr/bin/tool", "#!/bin/sh\n"),
        ("usr/lib/systemd/system/svc.service", "[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n"),
        ("usr/share/doc/svc/copyright", "x\n"),
        ("usr/share/doc/also/copyright", "x\n"),
    ]

    def build(self, order, mtime):
        t = Tree(self)
        t.put(t.base, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\n")
        t.put(t.base, "etc/group", "root:x:0:\n")
        for rel, text in order:
            os.utime(t.put(t.layer, rel, text), (mtime, mtime))
        wants = t.layer / "etc/systemd/system/multi-user.target.wants"
        wants.mkdir(parents=True)
        (wants / "svc.service").symlink_to("/usr/lib/systemd/system/svc.service")
        report = t.finalize(units_enable=["svc.service"], provides=["svc"])
        return t.layer, report

    @staticmethod
    def entries(root):
        out = {}
        for base, dirs, files in os.walk(root):
            for n in dirs + files:
                p = Path(base, n)
                if p.is_symlink():
                    content = os.readlink(p)
                else:
                    content = None if p.is_dir() else p.read_bytes()
                out[str(p.relative_to(root))] = (p.is_symlink(), p.is_dir(), content, p.lstat().st_mode, int(p.lstat().st_mtime))
        return out

    def test_finalized_layer_does_not_depend_on_creation_order_or_mtimes(self):
        first, report1 = self.build(self.PAYLOAD, 1500000000)
        second, report2 = self.build(list(reversed(self.PAYLOAD)), 1700000000)
        self.assertEqual(self.entries(first), self.entries(second))
        self.assertEqual(report1, report2)
        self.assertEqual({v[4] for v in self.entries(first).values()}, {1790000000})


    def test_cluster_layer_declares_the_state_folders_and_accounts_the_packages_made_in_their_scripts(self):
        # An overlay build keeps neither the folders nor the accounts that the Debian maintainer scripts create, and a
        # layer may not ship /var: corosync stopped at start without /var/lib/corosync and pacemaker without hacluster
        # (pool lab 3). Core runs systemd-sysusers and systemd-tmpfiles after the merge, so the layer declares both.
        extra = IMAGE / "mkosi.images/nex-cluster/mkosi.extra/usr/lib"
        tmpfiles = (extra / "tmpfiles.d/nex-cluster-state.conf").read_text()
        for folder in ("/var/lib/corosync", "/var/lib/pacemaker/cib", "/var/log/pacemaker", "/var/lib/sanlock"):
            self.assertRegex(tmpfiles, r"(?m)^d " + re.escape(folder) + r" ")
        sysusers = (extra / "sysusers.d/nex-cluster-accounts.conf").read_text()
        for line in (r"^u hacluster 189:haclient ", r"^u sanlock 179:sanlock "):
            self.assertRegex(sysusers, "(?m)" + line)
        spec = json.loads((IMAGE / "mkosi.images/nex-cluster/layer.json").read_text())
        self.assertEqual(spec["allow_var"], [])


class ControllerImageTest(unittest.TestCase):
    @unittest.skipUnless(shutil.which("systemd-sysusers"), "needs systemd-sysusers")
    def test_the_controller_account_gets_fixed_ids_while_other_system_ids_are_taken_dynamically(self):
        # firstboot gives /etc/nex-controller and /var/lib/nex-controller to this account on the data disk, which outlives
        # every system image; a uid taken from whatever system id was free at build time would not survive a swap.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "usr/lib/sysusers.d").mkdir(parents=True)
            shutil.copy(IMAGE / "controller/mkosi.extra/usr/lib/sysusers.d/nex-controller.conf", root / "usr/lib/sysusers.d")
            (root / "etc").mkdir()
            (root / "etc/passwd").write_text("root:x:0:0:root:/root:/bin/bash\nsvc:x:999:999::/:/usr/sbin/nologin\nsvc2:x:998:998::/:/usr/sbin/nologin\n")
            (root / "etc/group").write_text("root:x:0:\nsvc:x:999:\nsvc2:x:998:\n")
            subprocess.run(["systemd-sysusers", "--root", tmp, "nex-controller.conf"], check=True, capture_output=True)
            entry = next(line for line in (root / "etc/passwd").read_text().splitlines() if line.startswith("nex-controller:"))
            self.assertEqual(entry.split(":")[2:], ["280", "280", "Nex Controller", "/var/lib/nex-controller", "/usr/sbin/nologin"])
            self.assertIn("nex-controller:x:280:", (root / "etc/group").read_text())


class ZfsModuleBuildTest(unittest.TestCase):
    """scripts/build-zfs-modules.sh in a copy of the tree with a fake docker: only the sources the builder is given matter."""

    def sources(self, relative_paths=False, **env):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "scripts").mkdir()
            shutil.copy(ROOT / "scripts/build-zfs-modules.sh", root / "scripts")
            (root / "image/mkosi.images/nex-zfs/zfs-builder").mkdir(parents=True)
            (root / "image/mkosi.images/nex-zfs/zfs-builder/Dockerfile").write_text("FROM scratch\n")
            (root / "image/snapshot").write_text("20300101T000000Z\n")
            (root / "bin").mkdir()
            fake = root / "bin/docker"
            fake.write_text(
                '#!/usr/bin/env python3\n'
                'import pathlib, sys\n'
                'if sys.argv[1] == "run":\n'
                '    args = sys.argv[2:]\n'
                '    mounts = {}\n'
                '    for i, arg in enumerate(args):\n'
                '        if arg == "-v":\n'
                '            source, target, *_ = args[i + 1].split(":")\n'
                '            path = pathlib.Path(source)\n'
                '            if not path.is_absolute() or not path.exists():\n'
                '                sys.exit("invalid Docker bind-mount source: " + source)\n'
                '            mounts[target] = path\n'
                '    (mounts["/out"] / "signed-module-test").write_bytes(\n'
                '        mounts["/keys/module.key"].read_bytes() + mounts["/keys/module.crt"].read_bytes())\n'
                '    sys.stdin.buffer.read()\n')
            fake.chmod(0o755)
            (root / "module.key").write_text("key\n")
            (root / "module.crt").write_text("cert\n")
            environ = {k: v for k, v in os.environ.items() if not k.startswith("NEX_") and k != "ZFS_DKMS_VERSION"}
            environ.update(PATH=f"{root / 'bin'}:{os.environ['PATH']}",
                           NEX_MODULE_KEY="module.key" if relative_paths else str(root / "module.key"),
                           NEX_MODULE_CERT="module.crt" if relative_paths else str(root / "module.crt"), **env)
            subprocess.run(["sh", str(root / "scripts/build-zfs-modules.sh"), "--base-tree", str(root / "base"), "--kver", "6.12.0-test",
                            "--out", "out" if relative_paths else str(root / "out")],
                           cwd=root, env=environ, check=True, capture_output=True)
            return ((root / ".cache/image-builder/zfs/sources.list").read_text(),
                    (root / "out/signed-module-test").read_bytes())

    def test_relative_paths_supply_the_selected_signing_material_and_output(self):
        _, material = self.sources(relative_paths=True)
        self.assertEqual(material, b"key\ncert\n")

    def test_modules_are_built_from_the_snapshot_the_image_is_pinned_to(self):
        # The layer's zfsutils-linux and zfs-zed come from image/snapshot; modules from another snapshot would be another OpenZFS.
        text, _ = self.sources()
        self.assertIn("snapshot.debian.org/archive/debian/20300101T000000Z/", text)
        self.assertIn("snapshot.debian.org/archive/debian-security/20300101T000000Z/", text)

    def test_an_explicit_snapshot_still_overrides_the_pin(self):
        text, _ = self.sources(NEX_SNAPSHOT="20310202T000000Z")
        self.assertIn("archive/debian/20310202T000000Z/", text)
        self.assertNotIn("20300101T000000Z", text)


if __name__ == "__main__":
    unittest.main()
