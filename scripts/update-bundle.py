#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Write one published release as an offline bundle in the online layout.

    update-bundle.py --base PUBLISHED --channel stable --out MEDIA --profile minimal|full

The output (trust.json, <channel>/channel.json, releases/<sequence>/, blobs/, SHA256SUMS) goes on media
labelled NEXUPDATE or into /var/lib/nex/image/inbox/ and is verified by the host through the same pipeline as
a download. The bundle's channel.json is signed again with its own `expires` (default 180 days), so a
bundle outlives the online pointer; the release key is needed for that and the command fails closed without it.
`minimal` carries the slot-image only; `full` adds the source, compliance and every on-demand component.
"""
import argparse
import datetime
import hashlib
import importlib.util
import json
import shutil
import sys
from pathlib import Path

_spec = importlib.util.spec_from_file_location("update_publish", Path(__file__).with_name("update-publish.py"))
publish = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(publish)


def bundle(args):
    key = publish.key_path(args.release_key, "NEX_UPDATE_RELEASE_KEY", "release")
    base, out = Path(args.base), Path(args.out)
    channel = json.loads((base / args.channel / "channel.json").read_text())
    sequence = channel["sequence"]
    manifest_path = base / "releases" / str(sequence) / "manifest.json"
    manifest_body = manifest_path.read_bytes()
    if hashlib.sha256(manifest_body).hexdigest() != channel["manifest_sha256"]:
        raise publish.PublishError("the published channel does not match its manifest")
    manifest = json.loads(manifest_body)
    wanted = [c for c in manifest["components"] if args.profile == "full" or c["kind"] == "slot-image"]

    out.mkdir(parents=True, exist_ok=True)
    for name in ("trust.json", "trust.sig"):
        shutil.copyfile(base / name, out / name)
    release = out / "releases" / str(sequence)
    release.mkdir(parents=True, exist_ok=True)
    for name in ("manifest.json", "manifest.sig"):
        shutil.copyfile(base / "releases" / str(sequence) / name, release / name)
    (out / "blobs").mkdir(exist_ok=True)
    for component in wanted:
        shutil.copyfile(base / component["url"], out / component["url"])

    # The same serial and manifest hash with a later expiry: an accepted pointer stays accepted.
    channel["expires"] = publish.stamp(publish.now() + datetime.timedelta(days=args.expires_days))
    publish.write_signed(key, out / args.channel, "channel.json", channel, "channel.sig")

    lines = []
    for path in sorted(p for p in out.rglob("*") if p.is_file() and p.name != "SHA256SUMS"):
        lines.append(f"{publish.sha256_file(path)}  {path.relative_to(out)}")
    (out / "SHA256SUMS").write_text("\n".join(lines) + "\n")
    print(f"bundle of sequence {sequence} ({args.profile}, {len(wanted)} component(s)) written to {out}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--base", required=True, help="published update tree (scripts/update-publish.py)")
    parser.add_argument("--channel", default="stable")
    parser.add_argument("--out", required=True)
    parser.add_argument("--profile", choices=("minimal", "full"), default="minimal")
    parser.add_argument("--release-key")
    parser.add_argument("--expires-days", type=int, default=180)
    args = parser.parse_args(argv)
    try:
        bundle(args)
    except (publish.PublishError, OSError, KeyError) as error:
        print(f"update-bundle: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
