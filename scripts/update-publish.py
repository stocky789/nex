#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Sign and lay out Nex Core image releases in the online update layout.

    <base>/trust.json + trust.sig               root-key-signed keys in force
    <base>/<channel>/channel.json + channel.sig release-key-signed pointer
    <base>/releases/<sequence>/manifest.json + manifest.sig
    <base>/blobs/sha256-<hex>                   content-addressed; the host fetches with HTTP Range

Wire form is the engine's (internal/nexupdate/trust.go): Ed25519, a raw 64-byte signature over the exact
bytes of the file, raw 32-byte public keys (base64 in trust.json). Keys are PEM files made by openssl; they
never live in the repository. Every signing command fails closed: it needs a key path (option or
environment) and refuses to run without one.

    update-publish.py keygen --out DIR                          throwaway development keys (refuses to overwrite)
    update-publish.py trust --base B --root-key K --release-key K [--version N]
    update-publish.py publish --base B --release-dir D --source F --compliance F --channel dev
                              [--layer F]... [--on-demand-layer F]... [--payload vm-image:ID=F]...
    update-publish.py promote --base B --channel stable --sequence N --release-key K

A release's order is its integer `sequence`. The label is display text, but the publisher asserts that the
label order equals `dpkg --compare-versions` order against every release already in the tree, so the two can
never disagree (BUG-0416's one-engine rule).
"""
import argparse
import base64
import datetime
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
from pathlib import Path

FORMAT = 1
ARCHITECTURE = "amd64"
DEFAULT_TRUST_DAYS = 365 * 2
CHANNEL_RE = re.compile(r"^[a-z][a-z0-9-]{0,31}$")
SLOT_FILES = ("root.erofs", "root.verity", "roothash", "manifest.json")
SLOT_ENCODING = "tar+zstd"
SLOT_ZSTD_LEVEL = 10
LAYER_ID_RE = re.compile(r"^nex-[a-z0-9]+(-[a-z0-9]+)*$")
COMPONENT_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
PAYLOAD_NAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
PAYLOAD_KINDS = ("vm-image", "iso")
HEX64_RE = re.compile(r"^[0-9a-f]{64}$")


class PublishError(RuntimeError):
    pass


def now():
    return datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)


def stamp(moment):
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def sha256_file(path, chunk=1 << 20):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(chunk), b""):
            digest.update(block)
    return digest.hexdigest()


def key_path(option, env, what):
    """The signing key: the option, else the environment. No key, no signature."""
    value = option or os.environ.get(env)
    if not value:
        raise PublishError(f"refusing to sign without the {what} key: pass its option or set {env}")
    path = Path(value)
    if not path.is_file():
        raise PublishError(f"the {what} key does not exist: {path}")
    return path


def public_raw(key):
    der = subprocess.run(["openssl", "pkey", "-in", str(key), "-pubout", "-outform", "DER"],
                         check=True, capture_output=True).stdout
    if not der.startswith(bytes.fromhex("302a300506032b6570032100")) or len(der) != 44:
        raise PublishError(f"an Ed25519 key is required: {key}")
    return der[-32:]


def sign_file(key, path):
    """Write <path stem>.sig beside a metadata file; returns the signature bytes."""
    sig = subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(key), "-rawin", "-in", str(path)],
                         check=True, capture_output=True).stdout
    if len(sig) != 64:
        raise PublishError("signing produced a malformed signature")
    return sig


def write_signed(key, base_dir, name, document, sig_name):
    """Serialise a document with sorted keys, write it and its detached signature (document first would
    expose a file with no signature, so both are renamed into place, signature last)."""
    body = (json.dumps(document, indent=1, sort_keys=True) + "\n").encode()
    base_dir.mkdir(parents=True, exist_ok=True)
    target = base_dir / name
    tmp = target.with_name(name + ".tmp")
    tmp.write_bytes(body)
    sig = sign_file(key, tmp)
    sig_target = base_dir / sig_name
    sig_tmp = sig_target.with_name(sig_name + ".tmp")
    sig_tmp.write_bytes(sig)
    os.replace(tmp, target)
    os.replace(sig_tmp, sig_target)
    return body


def keygen(args):
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True, mode=0o700)
    for role in ("root", "release"):
        key = out / f"{role}.key"
        if key.exists():
            raise PublishError(f"refusing to overwrite {key}")
        subprocess.run(["openssl", "genpkey", "-algorithm", "ed25519", "-out", str(key)], check=True)
        key.chmod(0o600)
    print(f"development keys written to {out} (never commit them)")


def trust(args):
    root = key_path(args.root_key, "NEX_UPDATE_ROOT_KEY", "root")
    release = key_path(args.release_key, "NEX_UPDATE_RELEASE_KEY", "release")
    not_after = stamp(now() + datetime.timedelta(days=args.days))
    doc = {"format": FORMAT, "version": args.version, "keys": [
        {"id": "root-1", "role": "root", "public_key": base64.b64encode(public_raw(root)).decode(), "not_after": not_after},
        {"id": "release-1", "role": "release", "public_key": base64.b64encode(public_raw(release)).decode(), "not_after": not_after},
    ]}
    write_signed(root, Path(args.base), "trust.json", doc, "trust.sig")
    print(f"trust.json version {args.version} written to {args.base}")


def dpkg_gt(left, right):
    result = subprocess.run(["dpkg", "--compare-versions", left, "gt", right], capture_output=True)
    if result.returncode not in (0, 1):
        raise PublishError(f"dpkg --compare-versions could not compare {left!r} and {right!r}")
    return result.returncode == 0


def check_order(base, label, sequence):
    """The new release's sequence order against every hosted release equals its label order."""
    releases = Path(base) / "releases"
    if not releases.is_dir():
        return
    for child in sorted(releases.iterdir()):
        manifest = child / "manifest.json"
        if not manifest.is_file():
            continue
        other = json.loads(manifest.read_text())
        if other["sequence"] == sequence and other["label"] == label:
            continue  # republishing the identical release is idempotent
        if other["sequence"] == sequence:
            raise PublishError(f"sequence {sequence} is already published as {other['label']}; a rebuilt label needs a new sequence")
        if other["label"] == label:
            raise PublishError(f"label {label} is already published as sequence {other['sequence']}")
        newer_by_sequence = sequence > other["sequence"]
        if newer_by_sequence != dpkg_gt(label, other["label"]):
            raise PublishError(f"sequence order disagrees with dpkg label order: {label} (sequence {sequence}) "
                               f"versus {other['label']} (sequence {other['sequence']})")


def make_slot_image(release_dir, out_path):
    """The slot-image blob: a zstd-compressed tar of the release directory with fixed ownership and times, so
    the same release gives the same blob. The tar is streamed through zstd (one thread, fixed level): the
    component hash covers the compressed bytes the host downloads and resumes."""
    release_dir = Path(release_dir)
    for name in SLOT_FILES:
        if not (release_dir / name).is_file():
            raise PublishError(f"not a release directory (missing {name}): {release_dir}")
    files = sorted(str(p.relative_to(release_dir)) for p in release_dir.rglob("*") if p.is_file())
    try:
        with open(out_path, "wb") as out:
            compressor = subprocess.Popen(["zstd", "-q", "-T1", f"-{SLOT_ZSTD_LEVEL}", "-c"], stdin=subprocess.PIPE, stdout=out)
            try:
                with tarfile.open(fileobj=compressor.stdin, mode="w|", format=tarfile.PAX_FORMAT) as archive:
                    for name in files:
                        info = archive.gettarinfo(str(release_dir / name), arcname=name)
                        info.uid = info.gid = 0
                        info.uname = info.gname = ""
                        info.mtime = 0
                        info.mode = 0o644
                        with open(release_dir / name, "rb") as handle:
                            archive.addfile(info, handle)
            finally:
                compressor.stdin.close()
                status = compressor.wait()
    except FileNotFoundError as error:
        raise PublishError("zstd is required to build the slot-image component") from error
    if status != 0:
        raise PublishError(f"zstd failed with status {status}")


def describe_blob(source, component_id, kind, required, on_demand, extra=None):
    """Describe a file as a manifest component; nothing is written yet."""
    digest = sha256_file(source)
    component = {"id": component_id, "kind": kind, "sha256": digest, "bytes": os.path.getsize(source),
                 "url": f"blobs/sha256-{digest}", "required": required}
    if on_demand:
        component["on_demand"] = True
    component.update(extra or {})
    return component


def layer_component(path, on_demand, label, sequence):
    """A layer component from `<name>.raw` and the `<name>.raw.json` that image/mkosi.images/build-layer.sh
    writes beside it. The layer must have been built for exactly this release's base (BUILD_ID is
    <label>-<sequence>): a release that carried a layer of another base would be refused by every host."""
    path = Path(path)
    descriptor = Path(str(path) + ".json")
    if not path.is_file() or not descriptor.is_file():
        raise PublishError(f"a layer needs the file and its descriptor: {path} and {descriptor}")
    meta = json.loads(descriptor.read_text())
    if meta.get("kind") != "layer" or not LAYER_ID_RE.match(str(meta.get("id", ""))):
        raise PublishError(f"{descriptor} does not describe a layer")
    if meta.get("sha256") != sha256_file(path) or meta.get("bytes") != os.path.getsize(path):
        raise PublishError(f"{path} does not match its descriptor; rebuild the layer")
    build = f"{label}-{sequence}"
    if meta.get("requires_base") != build:
        raise PublishError(f"layer {meta['id']} was built for base {meta.get('requires_base')}, not for {build}; "
                           "every release rebuilds all layers against its base")
    extra = {"requires_base": build}
    if meta.get("verity_root_hash"):
        if not HEX64_RE.match(meta["verity_root_hash"]):
            raise PublishError(f"layer {meta['id']} has a malformed verity_root_hash")
        extra["verity_root_hash"] = meta["verity_root_hash"]
    return describe_blob(path, meta["id"], "layer", not on_demand, on_demand, extra)


def payload_component(spec):
    """KIND:ID=FILE, an on_demand payload placed on hosts at /var/lib/nex/payload/<sha256>/<file name>."""
    head, _, file = spec.partition("=")
    kind, _, component_id = head.partition(":")
    path = Path(file)
    if kind not in PAYLOAD_KINDS or not COMPONENT_ID_RE.match(component_id) or not path.is_file() \
            or not PAYLOAD_NAME_RE.match(path.name):
        raise PublishError(f"--payload takes KIND:ID=FILE with KIND in {PAYLOAD_KINDS}, a plain file name and an existing file: {spec!r}")
    return describe_blob(path, component_id, kind, False, True, {"name": path.name})


def store_blob(base, source, component):
    """Store a described file content-addressed under blobs/. A blob that already exists must hold the same bytes."""
    blobs = Path(base) / "blobs"
    blobs.mkdir(parents=True, exist_ok=True)
    target = blobs / f"sha256-{component['sha256']}"
    if target.exists():
        if sha256_file(target) != component["sha256"]:
            raise PublishError(f"{target} exists with other content; refusing to replace a published blob")
        return
    tmp = target.with_name(target.name + ".tmp")
    shutil.copyfile(source, tmp)
    os.replace(tmp, target)


def without_created(manifest):
    return {key: value for key, value in manifest.items() if key != "created"}


def write_channel(args, key, sequence, manifest_body):
    channel_dir = Path(args.base) / args.channel
    serial = 0
    previous = channel_dir / "channel.json"
    if previous.is_file():
        serial = json.loads(previous.read_text())["channel_serial"]
    doc = {"format": FORMAT, "channel": args.channel, "channel_serial": serial + 1, "sequence": sequence,
           "manifest_sha256": hashlib.sha256(manifest_body).hexdigest(),
           "expires": stamp(now() + datetime.timedelta(days=args.expires_days)), "min_sequence": args.min_sequence}
    write_signed(key, channel_dir, "channel.json", doc, "channel.sig")
    print(f"{args.channel}/channel.json serial {serial + 1} -> sequence {sequence}")


def publish(args):
    key = key_path(args.release_key, "NEX_UPDATE_RELEASE_KEY", "release")
    if not CHANNEL_RE.match(args.channel):
        raise PublishError("the channel name is invalid")
    # The licence gate: no signature without the corresponding source and the compliance manifest, hosted
    # with the hashes the manifest carries.
    for option, path in (("--source", args.source), ("--compliance", args.compliance)):
        if not path or not Path(path).is_file():
            raise PublishError(f"refusing to sign without {option}: the release must ship its source and compliance manifest")
    release_dir = Path(args.release_dir)
    built = json.loads((release_dir / "manifest.json").read_text())
    label, sequence, roothash = built["label"], int(built["sequence"]), built["roothash"]
    if sequence < 1:
        raise PublishError("a published release needs sequence >= 1 (build with NEX_SEQUENCE)")
    check_order(args.base, label, sequence)

    work = Path(args.base) / ".work"
    work.mkdir(parents=True, exist_ok=True)
    try:
        slot_blob = work / "slot-image.tar.zst"
        make_slot_image(release_dir, slot_blob)
        sources = [
            (slot_blob, describe_blob(slot_blob, "slot-image", "slot-image", True, False,
                                      {"verity_root_hash": roothash, "encoding": SLOT_ENCODING})),
            (Path(args.source), describe_blob(args.source, "source", "source", False, True)),
            (Path(args.compliance), describe_blob(args.compliance, "compliance", "compliance", False, True)),
        ]
        sources += [(Path(layer), layer_component(layer, False, label, sequence)) for layer in args.layer]
        sources += [(Path(layer), layer_component(layer, True, label, sequence)) for layer in args.on_demand_layer]
        sources += [(Path(spec.partition("=")[2]), payload_component(spec)) for spec in args.payload]
        ids = [component["id"] for _, component in sources]
        if len(ids) != len(set(ids)):
            raise PublishError("component ids must be unique across the slot image, layers and payloads")
        manifest = {"format": FORMAT, "sequence": sequence, "label": label, "architecture": ARCHITECTURE,
                    "created": stamp(now()), "min_from_sequence": args.min_from_sequence,
                    "compat": {"update_engine": 1, "state_format": args.state_format, "min_state_format": args.min_state_format,
                               "api_epoch": args.api_epoch, "min_controller_api_epoch": 0, "mixed_pool": True,
                               "controller_first": False},
                    "components": [component for _, component in sources]}
        if args.md_generation:
            manifest["compat"]["md_generation"] = args.md_generation
            manifest["compat"]["md_fallback_safe"] = args.md_fallback_safe
        release_path = Path(args.base) / "releases" / str(sequence)
        published = release_path / "manifest.json"
        if published.is_file():
            # A published sequence is immutable: the identical release is a no-op (only `created` differs
            # between two runs), anything else needs a new sequence.
            if without_created(json.loads(published.read_text())) != without_created(manifest):
                raise PublishError(f"sequence {sequence} is already published with different content; "
                                   "a rebuilt release needs a new sequence")
            for source, component in sources:
                store_blob(args.base, source, component)
            body = published.read_bytes()
            print(f"release {label} (sequence {sequence}) is already published unchanged")
        else:
            for source, component in sources:
                store_blob(args.base, source, component)
            body = write_signed(key, release_path, "manifest.json", manifest, "manifest.sig")
            print(f"release {label} (sequence {sequence}, roothash {roothash[:16]}...) published to {args.base}")
    finally:
        shutil.rmtree(work, ignore_errors=True)
    pointer = Path(args.base) / args.channel / "channel.json"
    if pointer.is_file():
        current = json.loads(pointer.read_text())
        if current["sequence"] == sequence and current["manifest_sha256"] == hashlib.sha256(body).hexdigest():
            print(f"{args.channel}/channel.json already points at sequence {sequence}")
            return
    write_channel(args, key, sequence, body)


def promote(args):
    key = key_path(args.release_key, "NEX_UPDATE_RELEASE_KEY", "release")
    manifest = Path(args.base) / "releases" / str(args.sequence) / "manifest.json"
    if not manifest.is_file():
        raise PublishError(f"sequence {args.sequence} is not published in {args.base}")
    write_channel(args, key, args.sequence, manifest.read_bytes())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("keygen", help="write throwaway development keys")
    p.add_argument("--out", required=True)
    p.set_defaults(run=keygen)

    p = sub.add_parser("trust", help="sign trust.json with the root key")
    p.add_argument("--base", required=True)
    p.add_argument("--root-key")
    p.add_argument("--release-key")
    p.add_argument("--version", type=int, default=1)
    p.add_argument("--days", type=int, default=DEFAULT_TRUST_DAYS)
    p.set_defaults(run=trust)

    def release_options(p):
        p.add_argument("--base", required=True)
        p.add_argument("--channel", default="dev")
        p.add_argument("--release-key")
        p.add_argument("--expires-days", type=int, default=14)
        p.add_argument("--min-sequence", type=int, default=0)

    p = sub.add_parser("publish", help="store a release set and point a channel at it")
    release_options(p)
    p.add_argument("--release-dir", required=True, help="image/build.sh release output (manifest.json, root.erofs, ...)")
    p.add_argument("--source", help="matching source archive (hosted as the source component)")
    p.add_argument("--compliance", help="component and licence manifest (hosted as the compliance component)")
    p.add_argument("--layer", action="append", default=[], metavar="FILE", help="a layer built for this release's base (FILE.json beside it); staged with every update")
    p.add_argument("--on-demand-layer", action="append", default=[], metavar="FILE", help="like --layer, but staged only on hosts that enabled it")
    p.add_argument("--payload", action="append", default=[], metavar="KIND:ID=FILE", help="an on_demand payload (vm-image or iso) fetched on request")
    p.add_argument("--state-format", type=int, default=3)
    p.add_argument("--min-state-format", type=int, default=0)
    p.add_argument("--api-epoch", type=int, default=1)
    p.add_argument("--min-from-sequence", type=int, default=0)
    p.add_argument("--md-generation", type=int, default=0)
    p.add_argument("--md-fallback-safe", action="store_true")
    p.set_defaults(run=publish)

    p = sub.add_parser("promote", help="re-sign only channel.json to point at a published release")
    release_options(p)
    p.add_argument("--sequence", type=int, required=True)
    p.set_defaults(run=promote)

    args = parser.parse_args(argv)
    try:
        args.run(args)
    except PublishError as error:
        print(f"update-publish: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
