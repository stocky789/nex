# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the smoke fixture when an installed host retains multiple kernels."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class ISOSmokeTests(unittest.TestCase):
    def test_nested_guest_uses_running_host_kernel(self):
        spec = importlib.util.spec_from_file_location(
            "iso_smoke", Path(__file__).parent / "integration/iso-smoke.py")
        smoke = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(smoke)
        copy_kernel = next(line.strip() for line in smoke.CHECK.splitlines()
                           if line.strip().startswith("cp ") and '"$work/vmlinuz"' in line)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            boot, work = root / "boot", root / "work"
            boot.mkdir()
            work.mkdir()
            (boot / "vmlinuz-running").write_bytes(b"running kernel")
            (boot / "vmlinuz-older").write_bytes(b"older kernel")
            command = "uname() { printf '%s\\n' running; }; " + copy_kernel.replace("/boot/", str(boot) + "/")
            subprocess.run(["sh", "-ec", command],
                           env={**os.environ, "work": str(work)}, check=True)
            self.assertEqual((work / "vmlinuz").read_bytes(), b"running kernel")
