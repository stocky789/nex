# SPDX-License-Identifier: AGPL-3.0-only
"""Keep known private deployment details and credentials out of website source."""
import json
from pathlib import Path
import re
import subprocess
import unittest
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]


class WebsitePublicFiles(unittest.TestCase):
    def test_publishable_files(self):
        files = subprocess.check_output(
            ['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z', 'website'],
            cwd=ROOT).decode().split('\0')
        credential = re.compile(
            r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|'
            r'\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}|'
            r'\bAKIA[0-9A-Z]{16}|_authToken\s*[:=]\s*["\']?[A-Za-z0-9_-]{12,}')
        for name in sorted(set(files) - {''}):
            path = ROOT / name
            with self.subTest(file=name):
                self.assertFalse(path.is_symlink(), 'Public website input must not link outside the tree')
                self.assertFalse(any(part in {'node_modules', '.svelte-kit', 'build'} for part in Path(name).parts))
                self.assertFalse(path.name.startswith('.env') and path.name != '.env.example')
                self.assertNotIn(path.name, {'.npmrc', 'id_rsa', 'id_ed25519'})
                self.assertNotIn(path.suffix, {'.key', '.pem'})
                if path.suffix == '.png':
                    continue  # Visual review and the neutral Controller capture guard cover images.
                text = path.read_text()
                self.assertFalse(bool(credential.search(text)), 'Possible credential in public website input')
                self.assertFalse(bool(re.search(r'/home/[^/\s]+/|\b(?:10(?:\.\d{1,3}){3}|192\.168(?:\.\d{1,3}){2}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2})\b', text.lower())),
                                    'Private workstation/deployment detail in website input')

    def test_public_dependency_sources(self):
        lock = json.loads((ROOT / 'website/package-lock.json').read_text())
        for name, package in lock['packages'].items():
            if 'resolved' in package:
                with self.subTest(package=name):
                    url = urlsplit(package['resolved'])
                    self.assertEqual((url.scheme, url.netloc), ('https', 'registry.npmjs.org'))
                    self.assertFalse(url.query or url.fragment)


if __name__ == '__main__':
    unittest.main()
