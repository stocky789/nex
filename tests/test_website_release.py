# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise immutable website release selection in a disposable directory."""
import importlib.util
import io
import multiprocessing
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("website_release", ROOT / "scripts/website-release.py")
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


def artifact(path, title="Nex"):
    path.mkdir()
    (path / "index.html").write_text(f"<title>{title}</title>")
    (path / "LICENSE.txt").write_text("license")
    (path / "THIRD-PARTY-NOTICES.txt").write_text("notices")


def take_lock(root, result):
    with release.website_lock(root):
        result.put("locked")


class WebsiteReleaseTests(unittest.TestCase):
    def setUp(self):
        self.chown = patch.object(release.os, "chown")
        self.chown.start()
        self.addCleanup(self.chown.stop)

    def test_publish_switches_atomically_and_retains_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            artifact(base / "one")
            target = release.publish(base / "one", "2026.09.09", base / "site", lambda: True)
            self.assertEqual((base / "site/current").resolve(), target)
            self.assertEqual((target / "index.html").read_text(), "<title>Nex</title>")
            self.assertFalse((target / "index.html").stat().st_mode & 0o022)

    def test_failed_publish_restores_previous_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            artifact(base / "one", "Nex one")
            artifact(base / "two", "Nex two")
            release.publish(base / "one", "one", base / "site", lambda: True)
            with self.assertRaisesRegex(release.ReleaseError, "restored"):
                release.publish(base / "two", "two", base / "site", lambda: False)
            self.assertEqual((base / "site/current").resolve().name, "one")
            self.assertTrue((base / "site/releases/two").is_dir())

    def test_rollback_requires_retained_real_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            artifact(base / "one")
            release.publish(base / "one", "one", base / "site")
            (base / "site/releases/bad").symlink_to(base / "one")
            with self.assertRaises(release.ReleaseError):
                release.rollback("bad", base / "site")

    def test_rejects_symlinked_artifact_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            artifact(base / "one")
            (base / "one/escape.css").symlink_to("/etc/passwd")
            with self.assertRaises(release.ReleaseError):
                release.publish(base / "one", "one", base / "site")

    def test_health_fails_when_curl_returns_an_error_after_body(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            fake_curl = base / "curl"
            fake_curl.write_text("#!/bin/sh\nprintf 'Nex'\nexit 22\n")
            fake_curl.chmod(0o755)
            environment = os.environ | {
                "NEX_WEBSITE_URL": "http://example.invalid/",
                "PATH": f"{base}:{os.environ['PATH']}",
            }
            result = subprocess.run(["sh", ROOT / "scripts/website-health.sh"],
                                    env=environment, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)

    def test_release_lock_serializes_administrators(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "site"
            context = multiprocessing.get_context("spawn")
            result = context.Queue()
            with release.website_lock(root):
                worker = context.Process(target=take_lock, args=(root, result))
                worker.start()
                with self.assertRaises(multiprocessing.queues.Empty):
                    result.get(timeout=.2)
            self.assertEqual(result.get(timeout=5), "locked")
            worker.join(timeout=5)
            self.assertEqual(worker.exitcode, 0)

    def test_no_health_rejects_a_second_current_selector(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            artifact(base / "artifact")
            site = base / "site"
            site.mkdir()
            (site / "current").symlink_to("releases/old")
            stderr = io.StringIO()
            arguments = ["website-release.py", "--root", str(site), "--no-health",
                         "publish", str(base / "artifact"), "new"]
            with patch.object(release.os, "geteuid", return_value=0), \
                    patch.object(sys, "argv", arguments), redirect_stderr(stderr):
                self.assertEqual(release.main(), 1)
            self.assertIn("first publish", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
