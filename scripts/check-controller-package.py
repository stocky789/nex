#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Smoke-start an extracted nex-controller Debian package in Debian 13."""
import argparse
import hashlib
import http.client
import json
from http.cookies import SimpleCookie
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import time


def request(port, context, method, path, body=None, headers=None):
    connection = http.client.HTTPSConnection("localhost", port, context=context, timeout=5)
    try:
        connection.request(method, path, body=body, headers=headers or {})
        response = connection.getresponse()
        data = response.read()
        return response.status, dict(response.getheaders()), data
    finally:
        connection.close()


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("package", type=Path, help="nex-controller .deb to check")
    parser.add_argument("--timeout", type=float, default=60, help="startup timeout in seconds")
    args = parser.parse_args()
    package = args.package.resolve()
    if not package.is_file():
        parser.error("package must be a regular file")

    with tempfile.TemporaryDirectory(prefix="nex-controller-package-") as directory:
        work = Path(directory)
        extracted = work / "package"
        subprocess.run(["dpkg-deb", "-x", str(package), str(extracted)], check=True)
        binary = extracted / "usr/local/lib/nex/nex-controller"
        ui = extracted / "usr/local/share/nex/dist"
        source = extracted / "usr/local/share/nex/nex-source.tar.gz"
        docs = extracted / "usr/local/share/nex/api"
        for path in (binary, ui / "index.html", source, docs / "openapi.json"):
            if not path.is_file():
                raise RuntimeError(f"package is missing {path.relative_to(extracted)}")

        cert, key, password = work / "tls.crt", work / "tls.key", work / "password"
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
            "-nodes", "-keyout", str(key), "-out", str(cert), "-days", "1", "-subj", "/CN=localhost",
            "-addext", "subjectAltName=DNS:localhost",
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        password.write_text("Disposable-package-smoke-8572!\n")
        password.chmod(0o600)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]

        log = work / "controller.log"
        command = [
            str(binary), "-data-dir", str(work / "data"), "-listen", f"127.0.0.1:{port}",
            "-tls-cert", str(cert), "-tls-key", str(key), "-admin-password-file", str(password),
            "-ui-dir", str(ui), "-docs-dir", str(docs), "-source-archive", str(source),
        ]
        with log.open("w") as output:
            process = subprocess.Popen(command, stdout=output, stderr=subprocess.STDOUT)
            try:
                context = ssl.create_default_context(cafile=str(cert))
                deadline = time.monotonic() + args.timeout
                while True:
                    if process.poll() is not None:
                        raise RuntimeError("Controller exited before startup")
                    try:
                        status, _, _ = request(port, context, "GET", "/api/v1/session")
                        if status == 401:
                            break
                    except (ConnectionError, OSError, ssl.SSLError, http.client.HTTPException):
                        pass
                    if time.monotonic() >= deadline:
                        raise RuntimeError("Controller did not become ready")
                    time.sleep(0.15)

                status, _, page = request(port, context, "GET", "/")
                if status != 200 or not page:
                    raise RuntimeError(f"packaged UI returned {status}")
                status, _, served_source = request(port, context, "GET", "/source")
                if status != 200 or hashlib.sha256(served_source).digest() != hashlib.sha256(source.read_bytes()).digest():
                    raise RuntimeError(f"packaged source returned {status} or did not match")
                body = json.dumps({"username": "root", "password": password.read_text().strip()})
                status, headers, session = request(port, context, "POST", "/api/v1/session", body, {"Content-Type": "application/json"})
                if status != 200:
                    raise RuntimeError(f"login returned {status}: {session.decode(errors='replace')}")
                cookie = SimpleCookie(headers.get("Set-Cookie", "")).get("__Host-nex_session")
                if cookie is None:
                    raise RuntimeError("login did not return the administrator session cookie")
                status, _, session = request(port, context, "GET", "/api/v1/session", headers={"Cookie": f"{cookie.key}={cookie.value}"})
                if status != 200 or json.loads(session).get("username") != "root":
                    raise RuntimeError(f"authenticated session returned {status}")
                if not (work / "data/controller.db").is_file():
                    raise RuntimeError("Controller did not create its SQLite database")
            except Exception as error:
                stop(process)
                raise RuntimeError(f"controller package smoke failed: {error}; log follows:\n" + log.read_text()) from None
            finally:
                stop(process)
        print("PASS: packaged Controller SQLite startup, UI, matching source and authenticated session")


if __name__ == "__main__":
    main()
