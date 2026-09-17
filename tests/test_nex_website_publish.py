# SPDX-License-Identifier: AGPL-3.0-only
"""Tests for the root website publish wrapper."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "nex_website_publish", ROOT / "scripts/nex-website-publish.py"
)
publish = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(publish)
RELEASE_SPEC = importlib.util.spec_from_file_location(
    "website_release", ROOT / "scripts/website-release.py"
)
release = importlib.util.module_from_spec(RELEASE_SPEC)
RELEASE_SPEC.loader.exec_module(release)


class NexWebsitePublishTests(unittest.TestCase):
    def test_load_health_env_reads_url_and_expected(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "health.env"
            path.write_text(
                "NEX_WEBSITE_URL=http://192.0.2.10:8080/\n"
                "NEX_WEBSITE_EXPECTED=Nex\n",
                encoding="utf-8",
            )
            url, expected = publish.load_health_env(path)
            self.assertEqual(url, "http://192.0.2.10:8080/")
            self.assertEqual(expected, "Nex")

    def test_load_health_env_requires_url(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "health.env"
            path.write_text("NEX_WEBSITE_EXPECTED=Nex\n", encoding="utf-8")
            with self.assertRaisesRegex(publish.PublishError, "NEX_WEBSITE_URL"):
                publish.load_health_env(path)

    def test_executable_artifact_is_rejected_without_mode_mutation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            artifact = root / "build"
            artifact.mkdir()
            for name in ("index.html", "LICENSE.txt", "THIRD-PARTY-NOTICES.txt"):
                (artifact / name).write_text("Nex\n", encoding="utf-8")
            path = artifact / "app.js"
            path.write_text("console.log(1)\n", encoding="utf-8")
            path.chmod(0o755)
            health = root / "health.env"
            health.write_text("NEX_WEBSITE_URL=http://192.0.2.10:8080/\n", encoding="utf-8")
            tool = root / "website-release.py"
            tool.write_text("#!/usr/bin/env python3\n", encoding="utf-8")
            argv = ["nex-website-publish.py", "--root", str(root / "site"),
                    "publish", str(artifact), "20260917-test"]

            def validate_release(command, check):
                try:
                    release.validate_artifact(Path(command[-2]))
                except release.ReleaseError:
                    return Mock(returncode=1)
                return Mock(returncode=0)

            with patch.object(publish, "HEALTH_ENV", health), \
                    patch.object(publish, "RELEASE_TOOL", tool), \
                    patch.object(publish.os, "geteuid", return_value=0), \
                    patch.object(publish.sys, "argv", argv), \
                    patch.object(publish.subprocess, "run", side_effect=validate_release):
                self.assertEqual(publish.main(), 1)
            self.assertTrue(path.stat().st_mode & 0o111)
            with self.assertRaisesRegex(release.ReleaseError, "executable"):
                release.validate_artifact(artifact)

    def test_prune_releases_keeps_newest_and_current(self):
        with tempfile.TemporaryDirectory() as tmp:
            site = Path(tmp) / "site"
            releases = site / "releases"
            releases.mkdir(parents=True)
            names = [f"rel-{i:02d}" for i in range(5)]
            for index, name in enumerate(names):
                path = releases / name
                path.mkdir()
                os.utime(path, (index, index))
            publish.prune_releases(site, keep=2, current_id="rel-00")
            remaining = sorted(p.name for p in releases.iterdir())
            self.assertEqual(remaining, ["rel-00", "rel-03", "rel-04"])

    def test_main_publish_invokes_release_tool(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            artifact = base / "build"
            artifact.mkdir()
            (artifact / "index.html").write_text("<title>Nex</title>", encoding="utf-8")
            health = base / "health.env"
            health.write_text("NEX_WEBSITE_URL=http://192.0.2.10:8080/\n", encoding="utf-8")
            tool = base / "website-release.py"
            tool.write_text("#!/usr/bin/env python3\n", encoding="utf-8")
            site = base / "site"
            site.mkdir()
            argv = [
                "nex-website-publish.py",
                "--root", str(site),
                "--keep", "3",
                "publish", str(artifact), "20260917-test",
            ]
            with patch.object(publish, "HEALTH_ENV", health), \
                    patch.object(publish, "RELEASE_TOOL", tool), \
                    patch.object(publish.os, "geteuid", return_value=0), \
                    patch.object(publish.sys, "argv", argv), \
                    patch.object(publish.subprocess, "run") as run:
                run.return_value.returncode = 0
                self.assertEqual(publish.main(), 0)
            self.assertEqual(run.call_count, 1)
            called = run.call_args.args[0]
            self.assertIn("--health-url", called)
            self.assertIn("http://192.0.2.10:8080/", called)
            self.assertIn("publish", called)
            self.assertIn("20260917-test", called)


if __name__ == "__main__":
    unittest.main()
