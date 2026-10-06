# SPDX-License-Identifier: AGPL-3.0-only
"""ISO publishing into the website download tree, without touching a real host."""
import importlib.machinery
import importlib.util
import json
import subprocess
import shutil
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader(
    "release_publish", str(ROOT / "scripts/release-publish.py"))
spec = importlib.util.spec_from_loader(loader.name, loader)
publish = importlib.util.module_from_spec(spec)
loader.exec_module(publish)


def make_iso(directory, version, content=b"iso-bytes"):
    path = Path(directory) / f"nex-core-{version}-amd64.iso"
    path.write_bytes(content + version.encode())
    return path


def make_source(directory, version, content=b"source-bytes"):
    path = Path(directory) / f"nex-core-{version}-source.tar.gz"
    path.write_bytes(content + version.encode())
    return path


def make_manifest(directory, version):
    path = Path(directory) / f"nex-core-{version}-compliance-manifest.json"
    iso = Path(directory) / f"nex-core-{version}-amd64.iso"
    source = Path(directory) / f"nex-core-{version}-source.tar.gz"
    path.write_text(json.dumps({
        "version": version,
        "iso": {"file": iso.name, "sha256": publish.sha256(iso) if iso.is_file() else "missing"},
        "source_bundle": {"file": source.name, "sha256": publish.sha256(source) if source.is_file() else "missing"},
    }) + "\n")
    return path


def publish_release(root, directory, version, keep=3, content=b"iso-bytes"):
    return publish.publish(root, make_iso(directory, version, content),
                           make_source(directory, version, content),
                           make_manifest(directory, version), keep)


class PublishTests(unittest.TestCase):
    def test_hash_pinned_release_payload_is_the_hash_then_the_file_name(self):
        for name in ('root.erofs', 'manifest.json', 'nex-core_dev_0.efi', 'controller.qcow2', 'loader.conf'):
            with self.subTest(name=name):
                self.assertRegex('a' * 64 + '-' + name, publish.PAYLOAD_NAME)
        self.assertIsNone(publish.PAYLOAD_NAME.fullmatch('a' * 63 + '-root.erofs'))
        self.assertIsNone(publish.PAYLOAD_NAME.fullmatch('root.erofs-' + 'a' * 64))
        self.assertIsNone(publish.PAYLOAD_NAME.fullmatch('a' * 64 + '-../root.erofs'))

    def test_publishes_iso_with_checksum_and_latest_link(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            iso = make_iso(tmp, "0.1.6-alpha1")
            source = make_source(tmp, "0.1.6-alpha1")
            manifest = make_manifest(tmp, "0.1.6-alpha1")
            version, digest, newest, removed = publish.publish(root, iso, source, manifest, keep=3)
            self.assertEqual(version, "0.1.6-alpha1")
            self.assertEqual(newest, "0.1.6-alpha1")
            self.assertEqual(removed, [])
            installed = root / "0.1.6-alpha1" / iso.name
            self.assertTrue(installed.is_file())
            self.assertEqual(installed.read_bytes(), iso.read_bytes())
            sidecar = (root / "0.1.6-alpha1" / (iso.name + ".sha256")).read_text()
            self.assertEqual(sidecar, f"{digest}  {iso.name}\n")
            for asset in (source, manifest):
                self.assertEqual((root / version / asset.name).read_bytes(), asset.read_bytes())
                self.assertTrue((root / version / (asset.name + ".sha256")).is_file())
            self.assertEqual((root / "latest").resolve().name, "0.1.6-alpha1")

    def test_sidecar_verifies_with_sha256sum(self):
        if not shutil.which("sha256sum"):
            self.skipTest("sha256sum required")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            publish_release(root, tmp, "0.1.6")
            release = root / "0.1.6"
            result = subprocess.run(["sha256sum", "-c", "nex-core-0.1.6-amd64.iso.sha256"],
                                    cwd=release, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_retention_keeps_the_newest_and_prunes_the_rest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            for version in ("0.1.4", "0.1.5", "0.1.6-alpha1", "0.1.6", "0.1.7"):
                publish_release(root, tmp, version)
            kept = publish.existing_releases(root)
            self.assertEqual(kept, ["0.1.6-alpha1", "0.1.6", "0.1.7"])
            self.assertEqual((root / "latest").resolve().name, "0.1.7")

    def test_retention_orders_development_and_prerelease_numbers_with_dpkg(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            versions = ["0.1.6", "0.1.7-dev.999", "0.1.7-dev.1000",
                        "0.1.7-alpha1", "0.1.7-rc9", "0.1.7-rc10", "0.1.7", "0.1.10"]
            for version in reversed(versions):
                publish_release(root, tmp, version, keep=len(versions))
            self.assertEqual(publish.existing_releases(root), versions)
            self.assertEqual((root / "latest").resolve().name, "0.1.10")
            publish_release(root, tmp, "0.1.7-dev.999", keep=3)
            self.assertEqual(publish.existing_releases(root), ["0.1.7-rc10", "0.1.7", "0.1.10"])
            self.assertEqual((root / "latest").resolve().name, "0.1.10")
            self.assertFalse((root / "0.1.7-dev.999").exists())

    def test_the_development_root_keeps_its_own_latest_and_retention(self):
        with tempfile.TemporaryDirectory() as tmp:
            stable, development = Path(tmp) / "downloads", Path(tmp) / "downloads" / "dev"
            publish_release(stable, tmp, "0.1.6")
            for build in (1, 2, 3):
                publish_release(development, tmp, f"0.1.7-dev.{build}", keep=2)
            # Two builds survive, `latest` is the newest build, and the stable root is untouched
            # even though the development tree sits inside it.
            self.assertEqual(publish.existing_releases(development), ["0.1.7-dev.2", "0.1.7-dev.3"])
            self.assertEqual((development / "latest").resolve().name, "0.1.7-dev.3")
            self.assertEqual(publish.existing_releases(stable), ["0.1.6"])
            self.assertEqual((stable / "latest").resolve().name, "0.1.6")
            page = (development / "0.1.7-dev.3" / "index.html").read_text()
            self.assertIn("Development channel build of the next release", page)
            self.assertNotIn("Development channel", (stable / "0.1.6" / "index.html").read_text())

    def test_republishing_the_same_version_replaces_it_cleanly(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            publish_release(root, tmp, "0.1.6", content=b"first")
            second = make_iso(tmp, "0.1.6", b"second")
            _, digest, _, _ = publish.publish(
                root, second, make_source(tmp, "0.1.6", b"second"),
                make_manifest(tmp, "0.1.6"), keep=3)
            installed = root / "0.1.6" / second.name
            self.assertEqual(installed.read_bytes(), second.read_bytes())
            self.assertEqual(publish.sha256(installed), digest)
            self.assertEqual(publish.existing_releases(root), ["0.1.6"])

    def test_latest_manifest_follows_the_newest_release_per_channel(self):
        with tempfile.TemporaryDirectory() as tmp:
            stable, development = Path(tmp) / "downloads", Path(tmp) / "downloads" / "dev"
            publish_release(stable, tmp, "0.1.6")
            publish_release(development, tmp, "0.1.7-dev.4")
            publish_release(development, tmp, "0.1.7-dev.5")
            self.assertEqual(json.loads((stable / "latest.json").read_text())["version"], "0.1.6")
            manifest = json.loads((development / "latest.json").read_text())
            self.assertEqual(manifest["version"], "0.1.7-dev.5")
            iso = development / "0.1.7-dev.5" / "nex-core-0.1.7-dev.5-amd64.iso"
            self.assertEqual(manifest["files"]["iso"], {
                "path": "0.1.7-dev.5/" + iso.name,
                "size": iso.stat().st_size,
                "sha256": publish.sha256(iso)})
            self.assertNotIn("netinst", manifest["files"])
            self.assertRegex(manifest["published"], r"^\d{4}-\d{2}-\d{2}$")

    def test_latest_manifest_is_not_replaced_by_a_failed_publish(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            publish_release(root, tmp, "0.1.6")
            bad = make_iso(tmp, "0.1.7")
            with self.assertRaises(publish.ReleasePublishError):
                publish.publish(root, bad, make_source(tmp, "0.1.7"),
                                make_manifest(tmp, "0.1.6"), keep=3)
            self.assertEqual(json.loads((root / "latest.json").read_text())["version"], "0.1.6")

    def test_new_publication_adds_download_pages_to_retained_releases(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            publish_release(root, tmp, "0.1.6")
            (root / "0.1.6" / "index.html").unlink(missing_ok=True)
            publish_release(root, tmp, "0.1.7")
            page = (root / "0.1.6" / "index.html").read_text()
            self.assertIn("nex-core-0.1.6-amd64.iso", page)
            self.assertIn("nex-core-0.1.6-source.tar.gz", page)

    def test_channel_indexes_only_link_retained_releases(self):
        with tempfile.TemporaryDirectory() as tmp:
            stable = Path(tmp) / "downloads"
            development = stable / "dev"
            for version in ("0.1.4", "0.1.5", "0.1.6"):
                publish_release(stable, tmp, version, keep=2)
            publish_release(development, tmp, "0.1.7-dev.1")
            for root, versions in ((stable, ("0.1.6", "0.1.5")),
                                   (development, ("0.1.7-dev.1",))):
                page = (root / "index.html").read_text()
                self.assertNotIn("latest.json", page)
                self.assertNotIn("0.1.4/", page)
                self.assertIn('href="latest/"', page)
                self.assertIn('href="/downloads/dev/"', page)
                for version in versions:
                    self.assertIn(f'href="{version}/"', page)
                    self.assertTrue((root / version / "index.html").is_file())
                if len(versions) == 2:
                    self.assertLess(page.index(versions[0]), page.index(versions[1]))
                self.assertEqual(json.loads((root / "latest.json").read_text())["version"],
                                 versions[0])

    def test_rejects_unexpected_iso_filename(self):
        with tempfile.TemporaryDirectory() as tmp:
            bad = Path(tmp) / "something-else.iso"
            bad.write_bytes(b"x")
            with self.assertRaisesRegex(publish.ReleasePublishError, "unexpected ISO filename"):
                publish.publish(Path(tmp) / "downloads", bad, make_source(tmp, "0.1.6"),
                                make_manifest(tmp, "0.1.6"), keep=3)

    def test_rejects_symlinked_iso(self):
        with tempfile.TemporaryDirectory() as tmp:
            real = make_iso(tmp, "0.1.6")
            link = Path(tmp) / "nex-core-0.1.7-amd64.iso"
            link.symlink_to(real)
            with self.assertRaisesRegex(publish.ReleasePublishError, "regular file"):
                publish.publish(Path(tmp) / "downloads", link, make_source(tmp, "0.1.7"),
                                make_manifest(tmp, "0.1.7"), keep=3)

    def test_keep_below_one_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            publish_release(root, tmp, "0.1.6")
            with self.assertRaisesRegex(publish.ReleasePublishError, "at least 1"):
                publish.prune(root, 0)

    def test_failed_publish_leaves_no_partial_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            publish_release(root, tmp, "0.1.6")
            missing = Path(tmp) / "nex-core-0.1.7-amd64.iso"
            with self.assertRaises(publish.ReleasePublishError):
                publish.publish(root, missing, make_source(tmp, "0.1.7"),
                                make_manifest(tmp, "0.1.7"), keep=3)
            self.assertEqual(publish.existing_releases(root), ["0.1.6"])
            self.assertFalse(any(p.name.startswith(".") and p.is_dir() for p in root.iterdir()))

    def test_rejects_assets_for_a_different_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(publish.ReleasePublishError, "source bundle must be named"):
                publish.publish(Path(tmp) / "downloads", make_iso(tmp, "0.1.6"),
                                make_source(tmp, "0.1.7"), make_manifest(tmp, "0.1.6"), keep=3)

    def test_rejects_manifest_without_matching_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            manifest = make_manifest(tmp, "0.1.6")
            manifest.write_text('{"version":"0.1.7"}\n')
            with self.assertRaisesRegex(publish.ReleasePublishError, "compliance manifest version"):
                publish.publish(Path(tmp) / "downloads", make_iso(tmp, "0.1.6"),
                                make_source(tmp, "0.1.6"), manifest, keep=3)

    def test_rejects_manifest_with_mismatched_artifact_hash(self):
        with tempfile.TemporaryDirectory() as tmp:
            iso = make_iso(tmp, "0.1.6")
            source = make_source(tmp, "0.1.6")
            manifest = make_manifest(tmp, "0.1.6")
            source.write_bytes(b"tampered")
            with self.assertRaisesRegex(publish.ReleasePublishError, "source_bundle binding"):
                publish.publish(Path(tmp) / "downloads", iso, source, manifest, keep=3)

    def test_publishes_iso_with_component_manifest(self):
        with tempfile.TemporaryDirectory() as tmp:
            iso = make_iso(tmp, "0.1.6")
            source = make_source(tmp, "0.1.6")
            manifest = make_manifest(tmp, "0.1.6")
            data = json.loads(manifest.read_text())
            data["components"] = [{"package": "bridge-utils"}]
            manifest.write_text(json.dumps(data) + "\n")
            version, _, _, _ = publish.publish(
                Path(tmp) / "downloads", iso, source, manifest, keep=3)
            self.assertEqual(version, "0.1.6")
            self.assertTrue((Path(tmp) / "downloads" / "0.1.6" / iso.name).is_file())


if __name__ == "__main__":
    unittest.main()
