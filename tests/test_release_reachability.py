# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise both workflows' public verification without downloading release payloads."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import os
import subprocess
import textwrap
import threading
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ReleaseReachabilityTests(unittest.TestCase):
    def test_public_checks_use_headers_and_still_reject_unauthorized(self):
        requests = []
        unauthorized = False
        settling = 0  # HEAD responses still answered 503 right after a publish (run 4319)

        class Handler(BaseHTTPRequestHandler):
            def do_HEAD(self):
                nonlocal settling
                requests.append((self.command, self.path))
                if settling:
                    settling -= 1
                    self.send_response(503)
                    self.end_headers()
                    return
                self.send_response(401 if unauthorized else 200)
                self.send_header('Content-Length', '2937505747')
                self.end_headers()

            def do_GET(self):
                requests.append((self.command, self.path))
                # A full download cannot complete within the verification budget.
                self.send_response(504)
                self.end_headers()

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        temporary = tempfile.TemporaryDirectory()
        payloads = Path(temporary.name) / 'dist/netinst'
        payloads.mkdir(parents=True)
        payload = payloads / ('nex-core-local-' + 'a' * 64 + '.deb')
        payload.write_bytes(b'fixture')
        try:
            for provider in ('.github', '.gitea'):
                workflow = (ROOT / provider / 'workflows/release.yml').read_text()
                step = workflow.split('      - name: Confirm the release is actually reachable\n', 1)[1]
                script = textwrap.dedent(step.split('        run: |\n', 1)[1].split('      - name:', 1)[0])
                for downloads_path in ('downloads', 'downloads/dev'):
                    for unauthorized, settling in ((False, 0), (True, 0), (False, 1)):
                        with self.subTest(provider=provider, downloads=downloads_path, unauthorized=unauthorized, settling=settling):
                            requests.clear()
                            result = subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                                env={**os.environ, 'NEX_PUBLIC_VERSION': '0.3.4',
                                     'NEX_DOWNLOADS_PATH': downloads_path,
                                     'NEX_PUBLIC_URL': f'http://127.0.0.1:{server.server_port}'},
                                text=True, capture_output=True, timeout=30, cwd=temporary.name)
                            if unauthorized:
                                self.assertNotEqual(result.returncode, 0)
                                self.assertIn('returned 401', result.stderr)
                            else:
                                self.assertEqual(result.returncode, 0, result.stderr)
                                self.assertTrue(any(method == 'HEAD' and path.endswith('.tar.gz') for method, path in requests))
                                self.assertFalse(any(method == 'GET' and path.endswith('.tar.gz') for method, path in requests))
                                # Each channel is verified at its own URLs, never the other's.
                                self.assertIn(('HEAD', f'/{downloads_path}/latest.json'), requests)
                                self.assertIn(('HEAD', f'/{downloads_path}/0.3.4/' + payload.name), requests)
        finally:
            temporary.cleanup()
            server.shutdown()
            server.server_close()
            thread.join()
