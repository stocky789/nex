#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the package gate with real working and CGO-disabled Controller binaries.

The Debian packages are disposable test fixtures, not release artifacts. No package
is installed; no host, pool, or existing Controller is contacted.
"""
import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parents[2]


def main():
    with tempfile.TemporaryDirectory(prefix="nex-package-regression-") as directory:
        work = Path(directory)
        tree = work / "package"
        binary = tree / "usr/local/lib/nex/nex-controller"
        share = tree / "usr/local/share/nex"
        binary.parent.mkdir(parents=True)
        (tree / "DEBIAN").mkdir()
        (tree / "DEBIAN/control").write_text(
            "Package: nex-controller\nVersion: 0.0.0\nArchitecture: amd64\n"
            "Maintainer: Nex contributors\nDescription: Disposable smoke regression fixture\n")
        (share / "dist").mkdir(parents=True)
        (share / "dist/index.html").write_text("<!doctype html><title>Package smoke fixture</title>")
        (share / "api").mkdir()
        (share / "api/openapi.json").write_text("{}\n")
        with tarfile.open(share / "nex-source.tar.gz", "w:gz") as archive:
            data = b"Disposable regression fixture; not release corresponding source.\n"
            entry = tarfile.TarInfo("fixture.txt")
            entry.size = len(data)
            archive.addfile(entry, io.BytesIO(data))

        for cgo in ("1", "0"):
            subprocess.run(
                [os.environ.get("GO", "go"), "build", "-o", str(binary), "./cmd/nex-controller"],
                cwd=ROOT, env={**os.environ, "CGO_ENABLED": cgo}, check=True)
            package = work / f"controller-cgo-{cgo}.deb"
            subprocess.run(["dpkg-deb", "--root-owner-group", "--build", str(tree), str(package)], check=True)
            result = subprocess.run(
                [sys.executable, str(ROOT / "scripts/check-controller-package.py"), str(package)],
                capture_output=True, text=True, timeout=90)
            output = result.stdout + result.stderr
            if cgo == "1":
                if result.returncode != 0:
                    raise RuntimeError("working package rejected:\n" + output)
                print("PASS: working packaged Controller initializes SQLite and authenticates")
            else:
                if result.returncode == 0 or "go-sqlite3 requires cgo" not in output:
                    raise RuntimeError("CGO-disabled package was not rejected for its startup failure:\n" + output)
                print("PASS: CGO-disabled packaged Controller rejected with startup diagnostics")


if __name__ == "__main__":
    main()
