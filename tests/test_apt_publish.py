# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise APT repository publish tooling in disposable directories."""
import gzip
import importlib.util
import io
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("apt_publish", ROOT / "scripts/apt-publish.py")
apt = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(apt)

HAS_DPKG_DEB = shutil.which("dpkg-deb") is not None
HAS_SCAN = shutil.which("dpkg-scanpackages") is not None or Path("/usr/bin/dpkg-scanpackages").is_file()
HAS_GPG = shutil.which("gpg") is not None


def build_minimal_deb(dest_dir, name, version, arch="amd64"):
    work = dest_dir / f"build-{name}"
    debian = work / "DEBIAN"
    debian.mkdir(parents=True)
    (debian / "control").write_text(
        f"Package: {name}\n"
        f"Version: {version}\n"
        f"Architecture: {arch}\n"
        f"Maintainer: Nex Lab <apt@nex.local>\n"
        f"Description: minimal test package for {name}\n"
    )
    (work / "usr" / "share" / "doc" / name).mkdir(parents=True)
    (work / "usr" / "share" / "doc" / name / "README").write_text(f"{name} {version}\n")
    deb = dest_dir / f"{name}_{version}_{arch}.deb"
    subprocess.run(
        ["dpkg-deb", "--root-owner-group", "--build", str(work), str(deb)],
        check=True,
        capture_output=True,
        text=True,
    )
    return deb


@unittest.skipUnless(HAS_DPKG_DEB and HAS_SCAN, "dpkg-deb and dpkg-scanpackages required")
class AptPublishTests(unittest.TestCase):
    def test_publish_two_packages_layout_and_updates(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            repo = base / "repo"
            gpg = base / "gnupg"
            gpg.mkdir(mode=0o700)
            controller = build_minimal_deb(base, "nex-controller", "0.1.0-alpha1")
            core = build_minimal_deb(base, "nex-core-local", "0.1.0-alpha1")
            stderr = io.StringIO()
            with redirect_stderr(stderr):
                published = apt.publish(repo, "nex", "main", "amd64",
                                        [controller, core], gpg)
            self.assertIn("warning: no signing key", stderr.getvalue())
            packages = repo / "dists/nex/main/binary-amd64/Packages"
            packages_gz = repo / "dists/nex/main/binary-amd64/Packages.gz"
            release = repo / "dists/nex/Release"
            self.assertTrue(packages.is_file())
            self.assertTrue(packages_gz.is_file())
            self.assertTrue(release.is_file())
            self.assertFalse(packages.is_symlink())
            # Current hosts update through apt, but hosts released before that switch
            # poll updates.json. Freezing it would hide every later release from them,
            # so it stays published until no supported host reads it.
            updates_path = repo / "updates.json"
            self.assertTrue(updates_path.is_file())
            self.assertEqual(
                (repo / "pool/main/n/nex-controller" / controller.name).read_bytes(),
                controller.read_bytes(),
            )
            self.assertEqual(
                (repo / "pool/main/n/nex-core-local" / core.name).read_bytes(),
                core.read_bytes(),
            )
            text = packages.read_text()
            self.assertIn("Package: nex-controller", text)
            self.assertIn("Package: nex-core-local", text)
            self.assertEqual(gzip.decompress(packages_gz.read_bytes()).decode(), text)
            release_text = release.read_text()
            self.assertIn("Origin: Nex", release_text)
            self.assertIn("Label: Nex", release_text)
            self.assertIn("Suite: nex", release_text)
            self.assertIn("Codename: nex", release_text)
            self.assertIn("Architectures: amd64", release_text)
            self.assertIn("Components: main", release_text)
            self.assertIn("MD5Sum:", release_text)
            self.assertIn("SHA256:", release_text)
            payload = json.loads(updates_path.read_text())
            self.assertEqual(payload["repo"], "Nex")
            self.assertEqual(payload["packages"]["nex-controller"]["version"], "0.1.0-alpha1")
            self.assertEqual(payload["packages"]["nex-core-local"]["version"], "0.1.0-alpha1")
            self.assertTrue(payload["packages"]["nex-controller"]["filename"].startswith("pool/"))
            # The legacy index must track the signed one, or old hosts stop seeing releases.
            self.assertEqual(published, payload)
            self.assertFalse((repo / "dists/nex/InRelease").exists())

    def test_development_channel_is_a_separate_repository_with_its_own_retention(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            gpg = base / "gnupg"
            gpg.mkdir(mode=0o700)
            stable, development = base / "repo", base / "repo" / "dev"
            stderr = io.StringIO()
            with redirect_stderr(stderr):
                apt.publish(stable, "nex", "main", "amd64",
                            [build_minimal_deb(base, "nex-core-local", "0.4.18")], gpg)
                # Builds 999 and 1000 compare as numbers; the oldest of three is dropped.
                for build in ("0.4.19~dev.999", "0.4.19~dev.1000", "0.4.19~dev.1001"):
                    (base / build).mkdir()
                    apt.publish(development, "nex", "main", "amd64",
                                [build_minimal_deb(base / build, "nex-core-local", build)], gpg, keep=2)
            pool = development / "pool/main/n/nex-core-local"
            self.assertEqual(sorted(path.name for path in pool.iterdir()),
                             ["nex-core-local_0.4.19~dev.1000_amd64.deb", "nex-core-local_0.4.19~dev.1001_amd64.deb"])
            development_index = (development / "dists/nex/main/binary-amd64/Packages").read_text()
            self.assertIn("Version: 0.4.19~dev.1001", development_index)
            self.assertNotIn("Version: 0.4.18", development_index)
            # The stable index never lists a development build, and keeps every version without --keep.
            stable_index = (stable / "dists/nex/main/binary-amd64/Packages").read_text()
            self.assertIn("Version: 0.4.18", stable_index)
            self.assertNotIn("dev", stable_index)
            self.assertEqual(json.loads((stable / "updates.json").read_text())["packages"]["nex-core-local"]["version"],
                             "0.4.18")

    def test_rejects_invalid_deb_name(self):
        with self.assertRaises(apt.AptPublishError):
            apt.validate_deb_name("../evil_1.0_amd64.deb")
        with self.assertRaises(apt.AptPublishError):
            apt.validate_deb_name("Bad_Name_1.0_amd64.deb")
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            bad = base / "Nex Controller_1.0_amd64.deb"
            bad.write_bytes(b"not-a-real-deb")
            with self.assertRaises(apt.AptPublishError):
                apt.collect_debs([bad])

    @unittest.skipUnless(HAS_GPG, "gpg required")
    def test_init_key_and_signed_inrelease(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            repo = base / "repo"
            gpg = base / "gnupg"
            self.assertEqual(apt.init_key(gpg), 0)
            self.assertTrue(apt.gpg_has_secret(gpg))
            controller = build_minimal_deb(base, "nex-controller", "0.1.0")
            core = build_minimal_deb(base, "nex-core-local", "0.1.0")
            stderr = io.StringIO()
            with redirect_stderr(stderr):
                apt.publish(repo, "nex", "main", "amd64", [controller, core], gpg)
            self.assertNotIn("warning: no signing key", stderr.getvalue())
            self.assertTrue((repo / "dists/nex/InRelease").is_file())
            self.assertTrue((repo / "dists/nex/Release.gpg").is_file())
            self.assertTrue((repo / "pubkey.gpg").is_file())
            self.assertIn("BEGIN PGP SIGNED MESSAGE", (repo / "dists/nex/InRelease").read_text())
            self.assertIn("BEGIN PGP PUBLIC KEY BLOCK", (repo / "pubkey.gpg").read_text())

    def test_cli_rejects_invalid_name(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            bad = base / "../escape_1.0_amd64.deb"
            # Create a local file with an invalid basename instead.
            bad = base / "ESCAPE_1.0_amd64.deb"
            bad.write_bytes(b"x")
            code = apt.main([
                "--repo-root", str(base / "repo"),
                "--gpg-home", str(base / "gnupg"),
                str(bad),
            ])
            self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
