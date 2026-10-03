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
import unittest.mock
from contextlib import redirect_stderr


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("apt_publish", ROOT / "scripts/apt-publish.py")
apt = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(apt)

HAS_DPKG_DEB = shutil.which("dpkg-deb") is not None
HAS_SCAN = shutil.which("dpkg-scanpackages") is not None or Path("/usr/bin/dpkg-scanpackages").is_file()
HAS_GPG = shutil.which("gpg") is not None


def build_minimal_deb(dest_dir, name, version, arch="amd64", *, payload=None):
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
    (work / "usr" / "share" / "doc" / name / "README").write_text(
        payload if payload is not None else f"{name} {version}\n")
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
                (repo / published["packages"]["nex-controller"]["filename"]).read_bytes(),
                controller.read_bytes(),
            )
            self.assertEqual(
                (repo / published["packages"]["nex-core-local"]["filename"]).read_bytes(),
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
                for build in ("0.4.19~~dev.999", "0.4.19~~dev.1000", "0.4.19~~dev.1001"):
                    (base / build).mkdir()
                    apt.publish(development, "nex", "main", "amd64",
                                [build_minimal_deb(base / build, "nex-core-local", build)], gpg, keep=2)
            pool = development / "pool/main/n/nex-core-local"
            self.assertEqual(sorted(path.name for path in pool.rglob("*.deb")),
                             ["nex-core-local_0.4.19~~dev.1000_amd64.deb", "nex-core-local_0.4.19~~dev.1001_amd64.deb"])
            development_index = (development / "dists/nex/main/binary-amd64/Packages").read_text()
            self.assertIn("Version: 0.4.19~~dev.1001", development_index)
            self.assertNotIn("Version: 0.4.18", development_index)
            # The stable index never lists a development build, and keeps every version without --keep.
            stable_index = (stable / "dists/nex/main/binary-amd64/Packages").read_text()
            self.assertIn("Version: 0.4.18", stable_index)
            self.assertNotIn("dev", stable_index)
            self.assertEqual(json.loads((stable / "updates.json").read_text())["packages"]["nex-core-local"]["version"],
                             "0.4.18")

    def assert_failed_publish_preserves_committed_archive(self, failure):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            repo, gpg = base / "repo", base / "gnupg"
            self.assertEqual(apt.init_key(gpg), 0)
            old_dir, new_dir, rebuilt_dir = base / "old", base / "new", base / "rebuilt"
            for directory in (old_dir, new_dir, rebuilt_dir):
                directory.mkdir()
            old = build_minimal_deb(old_dir, "nex-core-local", "1.0")
            new = build_minimal_deb(new_dir, "nex-core-local", "2.0")
            rebuilt = build_minimal_deb(rebuilt_dir, "nex-core-local", "1.0", payload="recreated package bytes\n")
            apt.publish(repo, "nex", "main", "amd64", [old], gpg, keep=1)
            metadata = list((repo / "dists").rglob("*"))
            metadata += [repo / "updates.json", repo / "pubkey.gpg"]
            committed = {path: path.read_bytes() for path in metadata if path.is_file()}
            packages = repo / "dists/nex/main/binary-amd64/Packages"
            referenced = {repo / record["Filename"]: (repo / record["Filename"]).read_bytes()
                          for record in apt.parse_packages(packages.read_text())}
            native_run = subprocess.run

            def fail_command(argv, *args, **kwargs):
                indexing = Path(argv[0]).name == "dpkg-scanpackages"
                signing = argv[0] == "gpg" and failure in argv
                if (failure == "indexing" and indexing) or signing:
                    return subprocess.CompletedProcess(argv, 1, stdout="", stderr=f"injected {failure} failure")
                return native_run(argv, *args, **kwargs)

            with unittest.mock.patch.object(apt.subprocess, "run", side_effect=fail_command):
                message = "signing key" if failure == "--list-secret-keys" else "injected"
                with self.assertRaisesRegex(apt.AptPublishError, message):
                    apt.publish(repo, "nex", "main", "amd64", [rebuilt, new], gpg, keep=1)
            self.assertEqual({path: path.read_bytes() for path in committed}, committed)
            self.assertEqual({path: path.read_bytes() for path in referenced}, referenced)
            self.assertEqual(gzip.decompress(packages.with_name("Packages.gz").read_bytes()),
                             packages.read_bytes())
            for record in apt.parse_packages(packages.read_text()):
                package = repo / record["Filename"]
                self.assertEqual(apt.hashlib.sha256(package.read_bytes()).hexdigest(), record["SHA256"])
                self.assertEqual(package.stat().st_size, int(record["Size"]))
            for signature, document in (("InRelease", None), ("Release.gpg", "Release")):
                command = ["gpg", "--homedir", str(gpg), "--batch", "--verify",
                           str(repo / "dists/nex" / signature)]
                if document:
                    command.append(str(repo / "dists/nex" / document))
                result = subprocess.run(command, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
            recovered = apt.publish(repo, "nex", "main", "amd64", [rebuilt, new], gpg, keep=1)
            self.assertTrue(all(not path.exists() for path in referenced))
            self.assertEqual((repo / recovered["packages"]["nex-core-local"]["filename"]).read_bytes(), new.read_bytes())
            self.assertEqual([record["Version"] for record in apt.parse_packages(packages.read_text())], ["2.0"])

    @unittest.skipUnless(HAS_GPG, "gpg required")
    def test_indexing_failure_preserves_committed_indexes_and_referenced_packages(self):
        self.assert_failed_publish_preserves_committed_archive("indexing")

    @unittest.skipUnless(HAS_GPG, "gpg required")
    def test_signing_failure_preserves_committed_indexes_and_referenced_packages(self):
        for failure in ("--detach-sign", "--clearsign", "--list-secret-keys"):
            with self.subTest(failure=failure):
                self.assert_failed_publish_preserves_committed_archive(failure)

    def assert_interrupted_rebuild_preserves_references(self, boundary):
        class InterruptedPublish(BaseException):
            pass

        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            repo, gpg = base / "repo", base / "gnupg"
            self.assertEqual(apt.init_key(gpg), 0)
            old_dir, rebuilt_dir = base / "old", base / "rebuilt"
            old_dir.mkdir()
            rebuilt_dir.mkdir()
            old = build_minimal_deb(old_dir, "nex-core-local", "1.0", payload="original package\n")
            rebuilt = build_minimal_deb(rebuilt_dir, "nex-core-local", "1.0", payload="rebuilt package\n")
            apt.publish(repo, "nex", "main", "amd64", [old], gpg, keep=1)
            packages = repo / "dists/nex/main/binary-amd64/Packages"
            records = apt.parse_packages(packages.read_text())
            referenced = {repo / record["Filename"]: (repo / record["Filename"]).read_bytes()
                          for record in records}
            metadata = [path for path in (repo / "dists").rglob("*") if path.is_file()]
            metadata += [repo / "updates.json", repo / "pubkey.gpg"]
            committed = {path: path.read_bytes() for path in metadata}
            cutover = packages if boundary == "Packages" else repo / "dists/nex/InRelease"
            replace = apt.os.replace

            def interrupt_commit(source, target):
                if Path(target) == cutover:
                    raise InterruptedPublish()
                return replace(source, target)

            with unittest.mock.patch.object(apt.os, "replace", side_effect=interrupt_commit):
                with self.assertRaises(InterruptedPublish):
                    apt.publish(repo, "nex", "main", "amd64", [rebuilt], gpg, keep=1)
            if boundary == "Packages":
                self.assertEqual({path: path.read_bytes() for path in committed}, committed)
            else:
                self.assertEqual(cutover.read_bytes(), committed[cutover])
                self.assertNotEqual(packages.read_bytes(), committed[packages])
            self.assertEqual({path: path.read_bytes() for path in referenced}, referenced)
            for record in records:
                package = repo / record["Filename"]
                self.assertEqual(apt.hashlib.sha256(package.read_bytes()).hexdigest(), record["SHA256"])
                self.assertEqual(package.stat().st_size, int(record["Size"]))
            installed = list((repo / "pool").rglob(rebuilt.name))
            self.assertEqual({path.read_bytes() for path in installed}, {old.read_bytes(), rebuilt.read_bytes()})
            verified = subprocess.run(
                ["gpg", "--homedir", str(gpg), "--batch", "--verify", str(repo / "dists/nex/InRelease")],
                capture_output=True, text=True)
            self.assertEqual(verified.returncode, 0, verified.stderr)
            recovered = apt.publish(repo, "nex", "main", "amd64", [rebuilt], gpg, keep=1)
            record = apt.parse_packages(packages.read_text())[0]
            package = repo / record["Filename"]
            self.assertEqual(package.read_bytes(), rebuilt.read_bytes())
            self.assertEqual(apt.hashlib.sha256(package.read_bytes()).hexdigest(), record["SHA256"])
            self.assertEqual(record["Version"], "1.0", "rebuilding a version remains supported")
            self.assertEqual(recovered["packages"]["nex-core-local"]["filename"], record["Filename"])
            self.assertTrue(all(not path.exists() for path in referenced))

    @unittest.skipUnless(HAS_GPG, "gpg required")
    def test_interruption_after_rebuilt_package_install_keeps_committed_package_bytes_usable(self):
        for boundary in ("Packages", "InRelease"):
            with self.subTest(boundary=boundary):
                self.assert_interrupted_rebuild_preserves_references(boundary)

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
