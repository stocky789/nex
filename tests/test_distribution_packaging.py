# SPDX-License-Identifier: AGPL-3.0-only
"""Packaging build properties checked without building anything on this workstation."""
from pathlib import Path

import unittest

ROOT = Path(__file__).resolve().parents[1]


class ControllerCgoBuildTests(unittest.TestCase):
    """The Controller links go-sqlite3; a CGO_ENABLED=0 build cannot open its database."""

    def test_controller_builders_have_a_c_toolchain_and_enable_cgo(self):
        packages = (ROOT / "appliance/Dockerfile").read_text().split()
        self.assertIn("gcc", packages)
        self.assertIn("libc6-dev", packages)
        lines = (ROOT / "appliance/build-controller-payload.sh").read_text().splitlines()
        export = lines.index("export CGO_ENABLED=1")
        go_build = next(i for i, line in enumerate(lines) if line.startswith("go build"))
        self.assertLess(export, go_build)


class VirtioWinReleaseInclusionTests(unittest.TestCase):
    """Public ISO builds stage virtio-win only with retained Fedora corresponding source."""

    def test_build_stages_virtio_win_with_source_gate(self):
        build = (ROOT / "installer/build.sh").read_text()
        self.assertIn("stage_virtio_win", build)
        self.assertIn("third-party/virtio-win", build)
        self.assertIn("virtio-win-0.1.302-1.src.rpm", build)
        self.assertIn("303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d", build)
        self.assertIn("25561bba120e67bfae842429d5e25764c38e74b92f78d9ef90c20ec41d2ad75d", build)
        self.assertIn("source_retained", build)
        self.assertIn("virtio-win corresponding source RPM missing", build)
        self.assertNotIn(
            "refusing to publish virtio-win.iso without complete corresponding-source and legal accounting",
            build,
        )


if __name__ == "__main__":
    unittest.main()
