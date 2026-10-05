# SPDX-License-Identifier: AGPL-3.0-only
"""Builder containers on a container-hosted runner must mount the real workspace."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WRAPPER = ROOT / "scripts/ci-docker.sh"


class CiDockerTests(unittest.TestCase):
    def run_wrapper(self, *args, **extra_env):
        with tempfile.TemporaryDirectory() as fake:
            docker = Path(fake) / "docker"
            docker.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
            docker.chmod(0o755)
            env = dict(os.environ, PATH=f"{fake}:{os.environ['PATH']}",
                       GITHUB_WORKSPACE="/workspace/o/r", NEX_HOST_WORKSPACE="/var/lib/docker/volumes/v/_data",
                       **extra_env)
            return subprocess.run([str(WRAPPER), *args], env=env, capture_output=True, text=True)

    def test_source_archives_come_from_the_named_host_directory_when_set(self):
        result = self.run_wrapper("run", "image", NEX_SOURCE_CACHE="/mnt/share/sources")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("/mnt/share/sources:/cache/downloads/sources", result.stdout.split("\n"))
        self.assertNotIn(":/cache/downloads/sources", self.run_wrapper("run", "image").stdout)

    def test_workspace_mounts_are_rewritten_to_the_host_path(self):
        result = self.run_wrapper("run", "--rm", "-v", "/workspace/o/r/.cache/x:/output",
                                  "--volume", "/workspace/o/r:/work:ro", "-v", "named:/data",
                                  "-e", "A=/workspace/o/r/keep", "image")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.split("\n")[:-1], [
            "run", "-v", "nex-build-cache:/cache", "-e", "GOCACHE=/cache/go-build",
            "-e", "npm_config_cache=/cache/npm", "-e", "NEX_DOWNLOADS=/cache/downloads",
            "--rm", "-v", "/var/lib/docker/volumes/v/_data/.cache/x:/output",
            "-v", "/var/lib/docker/volumes/v/_data:/work:ro", "-v", "named:/data",
            "-e", "A=/workspace/o/r/keep", "image"])

    def test_paths_outside_the_workspace_are_refused_not_mounted_empty(self):
        result = self.run_wrapper("run", "-v", "/tmp/nex-input-1:/input:ro", "image")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("refusing to mount /tmp/nex-input-1", result.stderr)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
