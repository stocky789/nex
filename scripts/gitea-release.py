#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Create or replace the Gitea release for one version and attach its artifacts.

A release is keyed by its tag. An existing release with that tag is deleted first,
and a tag that points at another commit is moved, so every push of the same
CHANGELOG version republishes that version instead of failing or piling up
duplicates. Assets stream from disk: a multi-gigabyte ISO is never read into memory.

Reads GITHUB_API_URL, GITHUB_REPOSITORY and GITHUB_TOKEN as the runner sets them.
"""
import argparse
import http.client
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

# A multi-gigabyte upload crosses a reverse proxy that can drop it mid-body.
UPLOAD_ATTEMPTS = 3
RETRY_SECONDS = 5


class GiteaError(RuntimeError):
    def __init__(self, message, status=0):
        super().__init__(message)
        self.status = status


class Repo:
    def __init__(self, api_url, repository, token):
        self.base = f"{api_url.rstrip('/')}/repos/{repository}"
        self.token = token

    def request(self, method, path, body=None, headers=None):
        merged = {"Authorization": f"token {self.token}", "Accept": "application/json"}
        merged.update(headers or {})
        request = urllib.request.Request(self.base + path, data=body, method=method, headers=merged)
        try:
            with urllib.request.urlopen(request) as response:
                status, text = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, text = error.code, error.read()
        if status == 404 and method == "GET":
            return None
        if status not in (200, 201, 204):
            raise GiteaError(f"{method} {path}: HTTP {status} {text[:300].decode(errors='replace')}", status)
        return json.loads(text) if text else None

    def json(self, method, path, payload):
        return self.request(method, path, json.dumps(payload).encode(),
                            {"Content-Type": "application/json"})


def upload(repo, release_id, asset):
    """Attach one asset, resending it when the connection or the proxy drops it.

    A dropped multi-gigabyte ISO used to fail the whole release: rebuilding the
    ISO for half an hour to reattach one file is not a recovery path. Each retry
    resends from the start after discarding whatever the failed attempt left
    registered under that name, and a short upload the server stored truncated
    counts as a failure rather than a published release with a broken download.
    """
    path = f"/releases/{release_id}/assets?name={urllib.parse.quote(asset.name)}"
    size = asset.stat().st_size
    for attempt in range(1, UPLOAD_ATTEMPTS + 1):
        try:
            with asset.open("rb") as stream:
                created = repo.request("POST", path, stream, {
                    "Content-Type": "application/octet-stream", "Content-Length": str(size)})
            stored = (created or {}).get("size")
            if stored is not None and stored != size:
                raise GiteaError(f"{asset.name}: stored {stored} of {size} bytes", 500)
            return created
        except (OSError, http.client.HTTPException, GiteaError) as error:
            # 4xx is a request this retry would repeat verbatim; only transport
            # and server-side failures are worth sending the bytes again.
            if attempt == UPLOAD_ATTEMPTS or getattr(error, "status", 500) < 500:
                raise
            print(f"gitea-release: {asset.name} attempt {attempt} failed: {error}", file=sys.stderr)
            time.sleep(RETRY_SECONDS * attempt)
            for existing in repo.request("GET", f"/releases/{release_id}/assets") or []:
                if existing.get("name") == asset.name:
                    repo.request("DELETE", f"/releases/{release_id}/assets/{existing['id']}")


def publish(repo, tag, target, name, notes, assets):
    existing = repo.request("GET", f"/releases/tags/{tag}")
    if existing:
        repo.request("DELETE", f"/releases/{existing['id']}")
    tag_info = repo.request("GET", f"/tags/{tag}")
    if tag_info and tag_info["commit"]["sha"] != target:
        repo.request("DELETE", f"/tags/{tag}")
    # Keep the release private until every asset has survived server-side size
    # verification. A terminal upload failure can then leave only a draft, not a
    # public release serving a known-truncated artifact.
    release = repo.json("POST", "/releases", {
        "tag_name": tag, "target_commitish": target, "name": name, "body": notes,
        "draft": True, "prerelease": "-" in tag})
    try:
        for asset in assets:
            upload(repo, release["id"], asset)
        return repo.json("PATCH", f"/releases/{release['id']}", {"draft": False})
    except (OSError, http.client.HTTPException, GiteaError):
        # Do not retain a failed draft either: older forges have exposed draft
        # attachments through direct download URLs.
        repo.request("DELETE", f"/releases/{release['id']}")
        raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True, help="release tag, e.g. v0.1.6-alpha1")
    parser.add_argument("--target", required=True, help="commit SHA the tag must point at")
    parser.add_argument("--name", required=True)
    parser.add_argument("--notes", type=Path, required=True, help="Markdown file used as the release body")
    parser.add_argument("--api-url", default=os.environ.get("GITHUB_API_URL"))
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY"))
    parser.add_argument("assets", nargs="+", type=Path)
    args = parser.parse_args(argv)
    token = os.environ.get("GITHUB_TOKEN")
    if not (args.api_url and args.repository and token):
        print("gitea-release: GITHUB_API_URL, GITHUB_REPOSITORY and GITHUB_TOKEN are required", file=sys.stderr)
        return 1
    for asset in args.assets:
        if not asset.is_file():
            print(f"gitea-release: missing asset {asset}", file=sys.stderr)
            return 1
    try:
        release = publish(Repo(args.api_url, args.repository, token), args.tag, args.target,
                          args.name, args.notes.read_text(), args.assets)
    except (GiteaError, OSError, http.client.HTTPException) as error:
        print(f"gitea-release: {error}", file=sys.stderr)
        return 1
    print(release.get("html_url") or release["id"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
