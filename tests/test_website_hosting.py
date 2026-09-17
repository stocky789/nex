# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the nginx website policy against a real nginx process."""
import http.client
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TEMPLATE = ROOT / 'packaging/website/nginx.conf.template'


class WebsiteHosting(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.nginx = shutil.which('nginx')
        if not cls.nginx:
            raise RuntimeError('nginx is required; CI installs nginx-core')
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.base = Path(cls.temp.name)
        release = cls.base / 'release'
        release.mkdir()
        cls.release = cls.base / 'current'
        cls.release.symlink_to(release, target_is_directory=True)
        (release / 'index.html').write_text('<!doctype html><title>Nex</title>')
        (release / 'gallery').mkdir()
        (release / 'gallery' / 'index.html').write_text('<!doctype html><title>Gallery</title>')
        (release / '_app').mkdir()
        (release / 'leak').symlink_to('/etc/passwd')
        (release / '.secret').write_text('not served')
        cls.log_dir = cls.base / 'log'
        cls.log_dir.mkdir()
        cls.port = cls.free_port()
        config = TEMPLATE.read_text()
        config = config.replace('@NEX_WEBSITE_LISTEN@', f'127.0.0.1:{cls.port}')
        config = config.replace('/srv/nex-website/current', str(cls.release))
        config = config.replace('/run/nex-website', str(cls.base / 'runtime'))
        (cls.base / 'runtime').mkdir()
        config = config.replace('/var/log/nex-website', str(cls.log_dir))
        cls.config = cls.base / 'nginx.conf'
        cls.config.write_text(config)
        # tempfile gives 0700, but an nginx master started as root drops its workers to the
        # build's unprivileged user, which then cannot traverse the fixture. try_files treats
        # that EACCES as "not found" and falls through to =404, so every served path would
        # answer 404 and the policy under test would never run. Production serves a root-owned
        # /srv/nex-website/current to that same unprivileged worker, so a traversable fixture
        # is the faithful one.
        for path in [cls.base, *cls.base.rglob('*')]:
            if not path.is_symlink():
                path.chmod(0o755 if path.is_dir() else 0o644)
        subprocess.run([cls.nginx, '-t', '-c', str(cls.config)], check=True, capture_output=True, text=True)
        cls.process = subprocess.Popen([cls.nginx, '-c', str(cls.config), '-g', 'daemon off;'])
        cls.addClassCleanup(cls.stop_nginx)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if cls.process.poll() is not None:
                break
            try:
                cls.request('/')
                return
            except (ConnectionRefusedError, TimeoutError):
                time.sleep(.05)
        raise RuntimeError(f'nginx did not become ready: {(cls.log_dir / "error.log").read_text()}')

    @classmethod
    def stop_nginx(cls):
        cls.process.terminate()
        cls.process.wait(timeout=5)

    @classmethod
    def free_port(cls):
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            return listener.getsockname()[1]

    @classmethod
    def request(cls, path, method='GET'):
        connection = http.client.HTTPConnection('127.0.0.1', cls.port, timeout=2)
        try:
            connection.request(method, path)
            response = connection.getresponse()
            body = response.read()
            return response.status, dict(response.getheaders()), body
        finally:
            connection.close()

    def assert_headers(self, headers):
        self.assertEqual(headers['Content-Security-Policy'], "default-src 'self'; base-uri 'none'; connect-src 'none'; font-src 'self'; form-action 'none'; frame-ancestors 'none'; img-src 'self'; object-src 'none'; script-src 'none'; style-src 'self'")
        self.assertEqual(headers['Strict-Transport-Security'], 'max-age=31536000')
        self.assertEqual(headers['X-Content-Type-Options'], 'nosniff')
        self.assertEqual(headers['X-Frame-Options'], 'DENY')
        self.assertEqual(headers['Referrer-Policy'], 'no-referrer')

    def test_serving_policy_and_headers(self):
        for path, expected in [('/', 200), ('/gallery/', 200), ('/_app/', 403), ('/.secret', 403), ('/missing', 404)]:
            with self.subTest(path=path):
                status, headers, _ = self.request(path)
                self.assertEqual(status, expected)
                self.assert_headers(headers)
        status, headers, _ = self.request('/leak')
        self.assertIn(status, (403, 404))
        self.assert_headers(headers)
        status, headers, _ = self.request('/gallery')
        self.assertEqual(status, 301)
        self.assertEqual(headers['Location'], '/gallery/')
        self.assert_headers(headers)
        status, headers, body = self.request('/', 'HEAD')
        self.assertEqual(status, 200)
        self.assertEqual(body, b'')
        self.assert_headers(headers)
        status, headers, _ = self.request('/', 'POST')
        self.assertEqual(status, 403)
        self.assert_headers(headers)

    def test_service_runs_nginx_as_the_dedicated_account(self):
        unit = (ROOT / 'packaging/website/nex-website.service').read_text()
        self.assertIn('User=nex-website', unit)
        self.assertIn("ExecStart=/usr/sbin/nginx -c /etc/nex-website/nginx.conf -g 'daemon off;'", unit)


class HostingStartupRegression(unittest.TestCase):
    def test_http_timeout_during_startup_is_retried(self):
        class Fixture(WebsiteHosting):
            pass

        self.addCleanup(Fixture.doClassCleanups)
        request = Fixture.request
        attempts = 0

        def delayed_request(path):
            nonlocal attempts
            attempts += 1
            if attempts == 1:
                raise TimeoutError('worker not ready')
            return request(path)

        with mock.patch.object(Fixture, 'request', side_effect=delayed_request):
            Fixture.setUpClass()
        self.assertGreater(attempts, 1)
        self.assertEqual(Fixture.request('/')[0], 200)
        Fixture.doClassCleanups()
        self.assertIsNotNone(Fixture.process.poll())
        self.assertFalse(Fixture.base.exists())

    def test_failed_request_closes_the_connection(self):
        with mock.patch.object(WebsiteHosting, 'port', 12345, create=True), \
                mock.patch.object(http.client, 'HTTPConnection') as connection:
            connection.return_value.getresponse.side_effect = TimeoutError('worker not ready')
            with self.assertRaises(TimeoutError):
                WebsiteHosting.request('/')
            connection.return_value.close.assert_called_once()

    def test_startup_deadline_fails_and_cleans_up(self):
        class Fixture(WebsiteHosting):
            pass

        result = unittest.TestResult()
        with mock.patch.object(Fixture, 'request', side_effect=TimeoutError('worker not ready')), \
                mock.patch.object(time, 'monotonic', side_effect=[0, 0, 11]):
            unittest.TestSuite([Fixture('test_serving_policy_and_headers')]).run(result)
        self.assertEqual(len(result.errors), 1)
        self.assertIn('nginx did not become ready', result.errors[0][1])
        self.assertEqual(result.testsRun, 0)
        self.assertIsNotNone(Fixture.process.poll())
        self.assertFalse(Fixture.base.exists())


class PythonPreviewRegression(unittest.TestCase):
    """The temporary Python server reproduces the gaps this host policy closes."""
    def test_python_preview_lists_directories_without_security_headers(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '_app').mkdir()
            with socket.socket() as listener:
                listener.bind(('127.0.0.1', 0))
                port = listener.getsockname()[1]
            process = subprocess.Popen(['python3', '-m', 'http.server', str(port), '--bind', '127.0.0.1', '--directory', str(root)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                for _ in range(50):
                    try:
                        connection = http.client.HTTPConnection('127.0.0.1', port, timeout=2)
                        connection.request('GET', '/_app/')
                        response = connection.getresponse()
                        body = response.read()
                        headers = dict(response.getheaders())
                        connection.close()
                        break
                    except ConnectionRefusedError:
                        time.sleep(.05)
                else:
                    self.fail('Python preview did not start')
                self.assertEqual(response.status, 200)
                self.assertIn(b'Directory listing for', body)
                self.assertNotIn('Content-Security-Policy', headers)
            finally:
                process.terminate()
                process.wait(timeout=5)


if __name__ == '__main__':
    unittest.main()
