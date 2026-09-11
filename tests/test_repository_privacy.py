# SPDX-License-Identifier: AGPL-3.0-only
"""Keep personal paths, observed lab IDs, and credentials out of public files."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]
HOME_PATH = re.compile(r"/(?:home|Users)/[A-Za-z0-9_.-]+/")
OBSERVED_ID = re.compile(
    r'"(?:id|[^"\n]*_(?:id|uuid)|uuid|certificate_sha256)"\s*:\s*"'
    r'(?:[0-9a-f]{64}|[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12})"', re.I)
CREDENTIAL = re.compile(
    r"-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----\s+[A-Za-z0-9+/=]{64,}|"
    r"\bgh[pousr]_[A-Za-z0-9]{30,}|\bgithub_pat_[A-Za-z0-9_]{30,}|"
    r"\bAKIA[0-9A-Z]{16}\b|\bxox[baprs]-[0-9A-Za-z-]{20,}"
)


class RepositoryPrivacyTests(unittest.TestCase):
    def test_public_repository_has_no_private_paths_ids_or_credentials(self):
        files = subprocess.check_output(
            ['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z'],
            cwd=ROOT).decode().split('\0')
        findings = []
        for name in sorted(set(files) - {''}):
            path = ROOT / name
            if path.is_symlink():
                data = str(path.readlink()).encode()
            elif path.is_file():
                data = path.read_bytes()
            else:
                continue
            # Images need visual review; public keyrings are binary.
            text = '' if b'\0' in data else data.decode('utf-8', errors='replace')
            for number, line in enumerate([name, *text.splitlines()]):
                if name.startswith('docs/qa/') and OBSERVED_ID.search(line):
                    findings.append(f'{name}:{number}: observed environment identifier')
                if HOME_PATH.search(line):
                    findings.append(f'{name}:{number}: personal home path')
            if CREDENTIAL.search(text):
                findings.append(f'{name}: possible credential')
        self.assertFalse(findings, '\n' + '\n'.join(findings))


if __name__ == '__main__':
    unittest.main()
