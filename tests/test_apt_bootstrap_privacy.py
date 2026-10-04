# SPDX-License-Identifier: AGPL-3.0-only
"""Check APT bootstrap does not embed a publisher identity."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "packaging/apt/bootstrap.sh"


class AptBootstrapPrivacyTests(unittest.TestCase):
    def bootstrap(self, base, user=None):
        paths = {
            "REPO_ROOT": base / "repo",
            "DOWNLOADS_ROOT": base / "downloads",
            "GPG_HOME": base / "gnupg",
            "PUBLISH": base / "bin/nex-apt-publish",
            "NGINX_CONF": base / "nginx.conf",
            "NGINX_INC": base / "nginx-apt.inc",
            "NGINX_DOWNLOADS_INC": base / "nginx-downloads.inc",
            "SUDOERS": base / "sudoers",
        }
        text = SOURCE.read_text()
        for name, path in paths.items():
            if name == "SUDOERS":
                continue
            text = text.replace(f"{name}=" + {
                "REPO_ROOT": "/srv/nex-apt",
                "DOWNLOADS_ROOT": "/srv/nex-downloads",
                "GPG_HOME": "/etc/nex-apt/gnupg",
                "PUBLISH": "/usr/local/sbin/nex-apt-publish",
                "NGINX_CONF": "/etc/nex-website/nginx.conf",
                "NGINX_INC": "/etc/nex-website/nginx-apt.inc",
                "NGINX_DOWNLOADS_INC": "/etc/nex-website/nginx-downloads.inc",
            }[name], f"{name}={path}")
        text = text.replace("SUDOERS=/etc/sudoers.d/nex-apt-publish", f"SUDOERS={paths['SUDOERS']}")
        text = text.replace("/etc/nex-apt", str(base / "nex-apt-config"))
        text = text.replace('ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)', f"ROOT={ROOT}")
        script = base / "bootstrap.sh"
        script.write_text(text)
        script.chmod(0o755)
        stub = base / "stub"
        stub.mkdir()
        (stub / "id").write_text("#!/bin/sh\necho 0\n")
        (stub / "dpkg").write_text("#!/bin/sh\nexit 0\n")
        # Reports a secret key and writes whatever -o names, as the publisher expects of a real signer.
        (stub / "gpg").write_text("#!/bin/bash\nargs=(\"$@\")\nfor i in \"${!args[@]}\"; do [[ ${args[i]} == -o ]] && : > \"${args[i+1]}\"; done\nprintf 'sec:\\n'\n")
        (stub / "visudo").write_text("#!/bin/sh\nexit 0\n")
        (stub / "install").write_text("#!/bin/bash\nargs=(\"$@\")\nif [[ $1 == -d ]]; then mkdir -p \"${args[-1]}\"; else mkdir -p \"$(dirname \"${args[-1]}\")\"; cp \"${args[-2]}\" \"${args[-1]}\"; fi\n")
        for command in stub.iterdir():
            command.chmod(0o755)
        env = {**os.environ, "PATH": f"{stub}:{os.environ['PATH']}"}
        env.pop("NEX_PUBLISH_USER", None)
        if user is not None:
            env["NEX_PUBLISH_USER"] = user
        return subprocess.run([str(script)], text=True, capture_output=True, env=env), paths

    def test_missing_or_invalid_publisher_stops_before_paths_are_created(self):
        for user in (None, "bad user"):
            with self.subTest(user=user), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                result, paths = self.bootstrap(base, user)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("NEX_PUBLISH_USER", result.stderr)
                self.assertFalse(any(path.exists() for path in paths.values()))

    def test_configured_publisher_controls_rendered_sudoers(self):
        with tempfile.TemporaryDirectory() as directory:
            result, paths = self.bootstrap(Path(directory), "publisher")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(paths["SUDOERS"].read_text().splitlines()[-1],
                             "publisher ALL=(root) NOPASSWD: /usr/local/sbin/nex-apt-publish")


if __name__ == "__main__":
    unittest.main()
