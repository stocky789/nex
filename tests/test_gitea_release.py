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

    def reply(self, status, payload=None):
        body = json.dumps(payload).encode() if payload is not None else b""
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.calls.append(("GET", self.path))
        if self.path.endswith("/releases/tags/v1.0.0-rc1"):
            return self.reply(200, self.state["release"]) if self.state["release"] else self.reply(404, {})
        if self.path.endswith("/tags/v1.0.0-rc1"):
            return self.reply(200, {"commit": {"sha": self.state["tag_sha"]}}) if self.state["tag_sha"] else self.reply(404, {})
        self.reply(404, {})

    def do_DELETE(self):
        self.calls.append(("DELETE", self.path))
        if "/releases/" in self.path:
            self.state["release"] = None
        else:
            self.state["tag_sha"] = None
        self.reply(204)

    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        self.calls.append(("POST", self.path, body, self.headers.get("Content-Type")))
        if self.path.endswith("/releases"):
            payload = json.loads(body)
            self.assertion = payload
            self.state["tag_sha"] = payload["target_commitish"]
            return self.reply(201, {"id": 9, "html_url": "https://example/releases/tag/v1.0.0-rc1"})
        self.reply(201, {"id": 100})

    def log_message(self, *args):
        pass


class PublishTests(unittest.TestCase):
    def setUp(self):
        FakeGitea.calls = []
        FakeGitea.state = {"release": {"id": 5}, "tag_sha": "old"}
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeGitea)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
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
        ])
        created = json.loads(FakeGitea.calls[4][2])
        self.assertEqual(created["target_commitish"], "new")
        self.assertTrue(created["prerelease"])
        self.assertEqual(FakeGitea.calls[5][2], b"\x00\xffiso-bytes" * 1000)
        self.assertEqual(FakeGitea.calls[5][3], "application/octet-stream")

    def test_same_commit_keeps_the_tag(self):
        FakeGitea.state = {"release": None, "tag_sha": "same"}
        with tempfile.TemporaryDirectory() as temporary:
            deb = Path(temporary) / "x.deb"
            deb.write_bytes(b"deb")
            gitea.publish(self.repo, "v1.0.0-rc1", "same", "Nex Core", "", [deb])
        self.assertNotIn("DELETE", [c[0] for c in FakeGitea.calls])

    def test_api_failure_is_reported_not_swallowed(self):
        FakeGitea.state = {"release": None, "tag_sha": None}
        with self.assertRaises(gitea.GiteaError):
            self.repo.request("PATCH", "/nowhere")


if __name__ == "__main__":
    unittest.main()
