# SPDX-License-Identifier: AGPL-3.0-only
"""Republishing a version must replace its release and move its tag, streaming assets."""
import importlib.machinery
import importlib.util
import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader("gitea_release", str(ROOT / "scripts/gitea-release.py"))
spec = importlib.util.spec_from_loader(loader.name, loader)
gitea = importlib.util.module_from_spec(spec)
loader.exec_module(gitea)


class FakeGitea(BaseHTTPRequestHandler):
    calls = []
    state = {"release": {"id": 5}, "tag_sha": "old"}
    # drop_uploads: attempt numbers that hang up mid-body, as a proxy does on a
    # multi-gigabyte ISO. stored_size: what the server claims it kept.
    drop_uploads = ()
    stored_size = None
    reject_uploads = False
    uploads = 0
    release_payload = None

    def reply(self, status, payload=None):
        body = json.dumps(payload).encode() if payload is not None else b""
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.calls.append(("GET", self.path))
        if self.path.endswith("/assets"):
            # A dropped upload can still leave the attachment registered.
            return self.reply(200, [{"id": 77, "name": "dropped.iso"}] if self.uploads else [])
        if self.path.endswith("/releases/tags/v1.0.0-rc1"):
            return self.reply(200, self.state["release"]) if self.state["release"] else self.reply(404, {})
        if self.path.endswith("/tags/v1.0.0-rc1"):
            return self.reply(200, {"commit": {"sha": self.state["tag_sha"]}}) if self.state["tag_sha"] else self.reply(404, {})
        self.reply(404, {})

    def do_DELETE(self):
        self.calls.append(("DELETE", self.path))
        if "/assets/" in self.path:
            pass
        elif "/releases/" in self.path:
            self.state["release"] = None
        else:
            self.state["tag_sha"] = None
        self.reply(204)

    def do_POST(self):
        if "/assets" in self.path:
            FakeGitea.uploads += 1
            if self.uploads in self.drop_uploads:
                self.rfile.read(16)
                self.close_connection = True
                self.calls.append(("POST", self.path, b"<dropped>", None))
                return
        body = self.rfile.read(int(self.headers["Content-Length"]))
        self.calls.append(("POST", self.path, body, self.headers.get("Content-Type")))
        if self.path.endswith("/releases"):
            payload = json.loads(body)
            self.assertion = payload
            FakeGitea.release_payload = payload
            self.state["release"] = {"id": 9, **payload}
            return self.reply(201, {"id": 9, "html_url": "https://example/releases/tag/v1.0.0-rc1", **payload})
        if self.reject_uploads:
            return self.reply(422, {"message": "asset rejected"})
        size = len(body) if self.stored_size is None else self.stored_size
        self.reply(201, {"id": 100, "size": size})

    def do_PATCH(self):
        if not self.path.endswith("/releases/9"):
            self.calls.append(("PATCH", self.path))
            return self.reply(404, {})
        body = self.rfile.read(int(self.headers["Content-Length"]))
        self.calls.append(("PATCH", self.path, body, self.headers.get("Content-Type")))
        payload = json.loads(body)
        release_payload = FakeGitea.release_payload or {}
        release_payload.update(payload)
        FakeGitea.release_payload = release_payload
        release_state = self.state["release"]
        assert isinstance(release_state, dict)
        release_state.update(payload)
        if payload.get("draft") is False:
            self.state["tag_sha"] = release_payload["target_commitish"]
        self.reply(200, {"id": 9, "html_url": "https://example/releases/tag/v1.0.0-rc1",
                         **release_payload})

    def log_message(self, *args):
        pass


class PublishTests(unittest.TestCase):
    def setUp(self):
        FakeGitea.calls = []
        FakeGitea.state = {"release": {"id": 5}, "tag_sha": "old"}
        FakeGitea.drop_uploads, FakeGitea.stored_size, FakeGitea.uploads = (), None, 0
        FakeGitea.reject_uploads = False
        FakeGitea.release_payload = None
        previous, gitea.RETRY_SECONDS = gitea.RETRY_SECONDS, 0
        self.addCleanup(setattr, gitea, "RETRY_SECONDS", previous)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeGitea)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.repo = gitea.Repo(f"http://127.0.0.1:{self.server.server_port}/api/v1", "o/r", "t")

    def test_replaces_release_moves_tag_and_streams_assets(self):
        with tempfile.TemporaryDirectory() as temporary:
            iso = Path(temporary) / "nex-core-1.0.0~rc1-amd64.iso"
            iso.write_bytes(b"\x00\xffiso-bytes" * 1000)
            release = gitea.publish(self.repo, "v1.0.0-rc1", "new", "Nex Core", "notes\n", [iso])
        self.assertEqual(release["id"], 9)
        self.assertEqual([c[:2] for c in FakeGitea.calls], [
            ("GET", "/api/v1/repos/o/r/releases/tags/v1.0.0-rc1"),
            ("DELETE", "/api/v1/repos/o/r/releases/5"),
            ("GET", "/api/v1/repos/o/r/tags/v1.0.0-rc1"),
            ("DELETE", "/api/v1/repos/o/r/tags/v1.0.0-rc1"),
            ("POST", "/api/v1/repos/o/r/releases"),
            ("POST", "/api/v1/repos/o/r/releases/9/assets?name=nex-core-1.0.0~rc1-amd64.iso"),
            ("PATCH", "/api/v1/repos/o/r/releases/9"),
        ])
        created = json.loads(FakeGitea.calls[4][2])
        self.assertEqual(created["target_commitish"], "new")
        self.assertTrue(created["prerelease"])
        self.assertTrue(created["draft"])
        self.assertEqual(FakeGitea.calls[5][2], b"\x00\xffiso-bytes" * 1000)
        self.assertEqual(FakeGitea.calls[5][3], "application/octet-stream")
        self.assertEqual(json.loads(FakeGitea.calls[6][2]), {"draft": False})
        self.assertFalse(release["draft"])
        self.assertEqual(FakeGitea.state["tag_sha"], "new")

    def test_same_commit_keeps_the_tag(self):
        FakeGitea.state = {"release": None, "tag_sha": "same"}
        with tempfile.TemporaryDirectory() as temporary:
            deb = Path(temporary) / "x.deb"
            deb.write_bytes(b"deb")
            gitea.publish(self.repo, "v1.0.0-rc1", "same", "Nex Core", "", [deb])
        self.assertNotIn("DELETE", [c[0] for c in FakeGitea.calls])

    def test_dropped_upload_is_resent_whole_after_discarding_the_stale_asset(self):
        FakeGitea.drop_uploads = (1,)
        with tempfile.TemporaryDirectory() as temporary:
            iso = Path(temporary) / "dropped.iso"
            iso.write_bytes(b"iso" * 5000)
            gitea.publish(self.repo, "v1.0.0-rc1", "new", "Nex Core", "", [iso])
        uploads = [c for c in FakeGitea.calls if "/assets?name=" in c[1]]
        self.assertEqual([c[2] for c in uploads], [b"<dropped>", b"iso" * 5000])
        self.assertIn(("DELETE", "/api/v1/repos/o/r/releases/9/assets/77"),
                      [c[:2] for c in FakeGitea.calls])

    def test_truncated_upload_fails_instead_of_publishing_a_short_asset(self):
        FakeGitea.stored_size = 3
        with tempfile.TemporaryDirectory() as temporary:
            iso = Path(temporary) / "short.iso"
            iso.write_bytes(b"iso" * 5000)
            with self.assertRaises(gitea.GiteaError):
                gitea.publish(self.repo, "v1.0.0-rc1", "new", "Nex Core", "", [iso])
        self.assertEqual(FakeGitea.uploads, gitea.UPLOAD_ATTEMPTS)
        self.assertIsNone(FakeGitea.state["release"])
        self.assertIsNone(FakeGitea.state["tag_sha"])
        self.assertNotIn("PATCH", [call[0] for call in FakeGitea.calls])
        self.assertIn(("DELETE", "/api/v1/repos/o/r/releases/9"),
                      [call[:2] for call in FakeGitea.calls])

    def test_a_rejected_upload_is_not_retried(self):
        FakeGitea.reject_uploads = True
        with tempfile.TemporaryDirectory() as temporary:
            bad = Path(temporary) / "x.deb"
            bad.write_bytes(b"deb")
            with self.assertRaises(gitea.GiteaError):
                gitea.publish(self.repo, "v1.0.0-rc1", "new", "Nex Core", "", [bad])
        self.assertEqual(FakeGitea.uploads, 1)
        self.assertIsNone(FakeGitea.state["release"])

    def test_api_failure_is_reported_not_swallowed(self):
        FakeGitea.state = {"release": None, "tag_sha": None}
        with self.assertRaises(gitea.GiteaError):
            self.repo.request("PATCH", "/nowhere")


if __name__ == "__main__":
    unittest.main()
