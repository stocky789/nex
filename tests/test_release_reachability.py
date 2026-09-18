# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise both workflows' public verification without downloading release payloads."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import os
import subprocess
import textwrap
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ReleaseReachabilityTests(unittest.TestCase):
    def test_public_checks_use_headers_and_still_reject_unauthorized(self):
        requests = []
        unauthorized = missing_version = False

        class Handler(BaseHTTPRequestHandler):
            def do_HEAD(self):
                requests.append((self.command, self.path))
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
        try:
            for provider in ('.github', '.gitea'):
                workflow = (ROOT / provider / 'workflows/release.yml').read_text()
                step = workflow.split('      - name: Confirm the release is actually reachable\n', 1)[1]
                script = textwrap.dedent(step.split('        run: |\n', 1)[1])
                for unauthorized, missing_version in ((False, False), (True, False), (False, True)):
                    with self.subTest(provider=provider, unauthorized=unauthorized, missing_version=missing_version):
                        requests.clear()
                        result = subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                            env={**os.environ, 'NEX_VERSION': '0.3.4',
                                 'NEX_PUBLIC_URL': f'http://127.0.0.1:{server.server_port}'},
                            text=True, capture_output=True, timeout=10)
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
                            self.assertIn(('GET', '/apt/dists/nex/main/binary-amd64/Packages'), requests)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
