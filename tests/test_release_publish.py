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
    def test_publishes_iso_with_checksum_and_latest_link(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "downloads"
            iso = make_iso(tmp, "0.1.6~alpha1")
            source = make_source(tmp, "0.1.6~alpha1")
            manifest = make_manifest(tmp, "0.1.6~alpha1")
            version, digest, newest, removed = publish.publish(root, iso, source, manifest, keep=3)
            self.assertEqual(version, "0.1.6~alpha1")
            self.assertEqual(newest, "0.1.6~alpha1")
            self.assertEqual(removed, [])
            installed = root / "0.1.6~alpha1" / iso.name
            self.assertTrue(installed.is_file())
            self.assertEqual(installed.read_bytes(), iso.read_bytes())
            sidecar = (root / "0.1.6~alpha1" / (iso.name + ".sha256")).read_text()
            self.assertEqual(sidecar, f"{digest}  {iso.name}\n")
            for asset in (source, manifest):
                self.assertEqual((root / version / asset.name).read_bytes(), asset.read_bytes())
                self.assertTrue((root / version / (asset.name + ".sha256")).is_file())
            self.assertEqual((root / "latest").resolve().name, "0.1.6~alpha1")

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
            for version in ("0.1.4", "0.1.5", "0.1.6~alpha1", "0.1.6", "0.1.7"):
                publish_release(root, tmp, version)
            kept = publish.existing_releases(root)
            self.assertEqual(kept, ["0.1.6~alpha1", "0.1.6", "0.1.7"])
            self.assertEqual((root / "latest").resolve().name, "0.1.7")

    def test_prerelease_sorts_below_its_release(self):
        # Matches dpkg: 0.1.6~alpha1 is older than 0.1.6, so it is pruned first.
        order = sorted(["0.1.6", "0.1.6~alpha1", "0.1.10", "0.1.9"],
                       key=publish.debian_sort_key)
        self.assertEqual(order, ["0.1.6~alpha1", "0.1.6", "0.1.9", "0.1.10"])

    def test_dpkg_agrees_with_the_sort_order(self):
        if not shutil.which("dpkg"):
            self.skipTest("dpkg required to cross-check version ordering")
        versions = ["0.1.6~alpha1", "0.1.6", "0.1.9", "0.1.10"]
        for lower, higher in zip(versions, versions[1:]):
            with self.subTest(lower=lower, higher=higher):
                result = subprocess.run(["dpkg", "--compare-versions", lower, "lt", higher],
                                        check=False)
                self.assertEqual(result.returncode, 0, f"{lower} must sort below {higher}")

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
