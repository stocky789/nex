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
        unauthorized = missing_version = False
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
                version = '0.3.3' if missing_version else '0.3.4'
                body = f'Package: nex-controller\nVersion: {version}\n'.encode() + b'Description: fixture entry\n' * 20000
                # A full source GET cannot complete within the verification budget.
                status = 401 if unauthorized else 504 if self.path.endswith('.tar.gz') else 200
                self.send_response(status)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

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
                for apt_path, downloads_path in (('apt', 'downloads'), ('apt/dev', 'downloads/dev')):
                    for unauthorized, missing_version, settling in ((False, False, 0), (True, False, 0), (False, True, 0), (False, False, 1)):
                        with self.subTest(provider=provider, apt=apt_path, unauthorized=unauthorized, missing_version=missing_version, settling=settling):
                            requests.clear()
                            result = subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                                env={**os.environ, 'NEX_VERSION': '0.3.4', 'NEX_APT_PATH': apt_path,
                                     'NEX_DOWNLOADS_PATH': downloads_path,
                                     'NEX_PUBLIC_URL': f'http://127.0.0.1:{server.server_port}'},
                                text=True, capture_output=True, timeout=30, cwd=temporary.name)
                            if unauthorized:
                                self.assertNotEqual(result.returncode, 0)
                                self.assertIn('returned 401', result.stderr)
                            elif missing_version:
                                self.assertNotEqual(result.returncode, 0)
                                self.assertIn('published index does not list 0.3.4', result.stderr)
                            else:
                                self.assertEqual(result.returncode, 0, result.stderr)
                                self.assertTrue(any(method == 'HEAD' and path.endswith('.tar.gz') for method, path in requests))
                                self.assertFalse(any(method == 'GET' and path.endswith('.tar.gz') for method, path in requests))
                                # Each channel is verified at its own URLs, never the other's.
                                self.assertIn(('GET', f'/{apt_path}/dists/nex/main/binary-amd64/Packages'), requests)
                                self.assertIn(('HEAD', f'/{downloads_path}/0.3.4/' + payload.name), requests)
        finally:
            temporary.cleanup()
            server.shutdown()
            server.server_close()
            thread.join()
