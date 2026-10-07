#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Sign and lay out Nex Core image releases in the online update layout.

    <base>/trust.json + trust.sig               root-key-signed keys in force (the latest version)
    <base>/trust.<n>.json + trust.<n>.sig       every version, so a host that missed some walks them in order
    <base>/<channel>/channel.json + channel.sig release-key-signed pointer
    <base>/releases/<sequence>/manifest.json + manifest.sig
    <base>/blobs/sha256-<hex>                   content-addressed; the host fetches with HTTP Range

Wire form is the engine's (internal/nexupdate/trust.go): Ed25519, a raw 64-byte signature over the exact
bytes of the file, raw 32-byte public keys (base64 in trust.json). Keys are PEM files made by openssl; they
never live in the repository. Every signing command fails closed: it needs a key path (option or
environment) and refuses to run without one.

    update-publish.py keygen --out DIR                          throwaway development keys (refuses to overwrite)
    update-publish.py trust --base B --root-key K --release-key K [--version N]
                           [--sign-with OLD_ROOT_KEY] [--revoke ID]... [--drop-key ID]...
    update-publish.py publish --base B --release-dir D --source F --compliance F --channel dev
                              [--layer F]... [--on-demand-layer F]... [--payload vm-image:ID=F]...
                              [--no-mixed-pool] [--controller-first] [--min-controller-api-epoch N]
    update-publish.py prune --base B [--keep 5]                 delete old releases and the blobs only they use
    update-publish.py promote --base B --channel stable --sequence N --release-key K

Trust rotation: run `trust` again in the same base. The previous document's keys are carried over (an old
release key therefore overlaps the new one), --drop-key ends an overlap, --revoke keeps a key listed as
revoked, and --sign-with names the root key the hosts already trust when the root key itself changes. The
new document is refused here when no host could accept it. A channel's min_sequence only goes up: a pointer
keeps the highest floor ever signed for it.

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
import tempfile
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
    """Return an Ed25519 detached signature for the file's exact bytes."""
    sig = subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(key), "-rawin", "-in", str(path)],
                         check=True, capture_output=True).stdout
    if len(sig) != 64:
        raise PublishError("signing produced a malformed signature")
    return sig


def write_signed(key, base_dir, name, document, sig_name):
    """Serialise and sign both files before installing the document, then its detached signature."""
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


def ensure_manifest_signature(base, key, manifest):
    """Keep a usable signature, or sign the immutable bytes before any channel exposes them."""
    signature = manifest.with_name("manifest.sig")
    trust_path = Path(base) / "trust.json"
    if trust_path.is_file():
        keys = json.loads(trust_path.read_text())["keys"]
        usable = [entry["public_key"] for entry in keys if entry["role"] == "release"
                  and not entry.get("revoked") and entry["not_after"] > stamp(now())]
    else:
        usable = [base64.b64encode(public_raw(key)).decode()]
    if signature.is_file():
        with tempfile.TemporaryDirectory() as scratch:
            public = Path(scratch) / "key.der"
            for encoded in usable:
                public.write_bytes(bytes.fromhex("302a300506032b6570032100") + base64.b64decode(encoded))
                if subprocess.run(
                        ["openssl", "pkeyutl", "-verify", "-pubin", "-keyform", "DER", "-inkey", str(public),
                         "-rawin", "-in", str(manifest), "-sigfile", str(signature)], capture_output=True).returncode == 0:
                    return
    if base64.b64encode(public_raw(key)).decode() not in usable:
        raise PublishError("the signing key is not a usable release key of trust.json; cannot repair manifest.sig")
    temporary = signature.with_name("manifest.sig.tmp")
    temporary.write_bytes(sign_file(key, manifest))
    os.replace(temporary, signature)


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


def key_id_of(doc):
    return {key["id"] for key in doc["keys"]}


def signed_by(base, keys):
    """Names of the published channel pointers and manifests whose signature verifies under one of the keys."""
    found = []
    pairs = [(p, p.with_name("channel.sig")) for p in sorted(base.glob("*/channel.json"))]
    pairs += [(p, p.with_name("manifest.sig")) for p in sorted(base.glob("releases/*/manifest.json"))]
    with tempfile.TemporaryDirectory() as scratch:
        for key in keys:
            pub = Path(scratch) / "key.der"
            pub.write_bytes(bytes.fromhex("302a300506032b6570032100") + base64.b64decode(key["public_key"]))
            for document, signature in pairs:
                if signature.is_file() and subprocess.run(
                        ["openssl", "pkeyutl", "-verify", "-pubin", "-keyform", "DER", "-inkey", str(pub), "-rawin",
                         "-in", str(document), "-sigfile", str(signature)], capture_output=True).returncode == 0:
                    found.append(f"{document.relative_to(base)} ({key['id']})")
    return found



def trust(args):
    """Write the next trust document. With no trust.json in --base this is version 1 (root-1 and release-1).
    Otherwise the previous document's keys are carried forward (so an old release key overlaps the new one
    until it is dropped or revoked), the new keys are added, and the document is signed by a root key hosts
    already trust. Every version is also published as trust.<n>.json and trust.<n>.sig: a host that missed
    versions walks them in order."""
    base = Path(args.base)
    root = key_path(args.root_key, "NEX_UPDATE_ROOT_KEY", "root")
    release = key_path(args.release_key, "NEX_UPDATE_RELEASE_KEY", "release")
    signer = key_path(args.sign_with, "NEX_UPDATE_ROOT_KEY", "root") if args.sign_with else root
    latest = base / "trust.json"
    previous = json.loads(latest.read_text()) if latest.is_file() else None
    expected = previous["version"] + 1 if previous else 1
    version = args.version if args.version is not None else expected
    if version != expected:
        # Hosts walk every version from the one they hold (trust.<n>.json): a gap is trust_gap for all of them.
        raise PublishError(f"trust version {version} must be {expected}: hosts refuse a replay and cannot walk past a gap")
    keys = [dict(key) for key in previous["keys"]] if previous else []
    removed = [key for key in keys if key["id"] in args.drop_key + args.revoke and key["role"] == "release"]
    if removed and not args.allow_orphaned_signatures:
        orphaned = signed_by(base, removed)
        if orphaned:
            raise PublishError(f"the release key being removed still signs published metadata ({', '.join(orphaned[:5])}"
                               f"{', ...' if len(orphaned) > 5 else ''}): hosts would refuse it once they adopt this trust. "
                               "Publish and promote releases under the replacement key first, or pass --allow-orphaned-signatures")
    for key_id in args.drop_key + args.revoke:
        if key_id not in key_id_of({"keys": keys}):
            raise PublishError(f"the previous trust.json has no key {key_id}")
    keys = [key for key in keys if key["id"] not in args.drop_key]
    for key in keys:
        if key["id"] in args.revoke:
            key["revoked"] = True
    not_after = stamp(now() + datetime.timedelta(days=args.days))
    for role, path in (("root", root), ("release", release)):
        public = base64.b64encode(public_raw(path)).decode()
        known = [key for key in keys if key["role"] == role and key["public_key"] == public]
        if known:
            if known[0].get("revoked"):
                raise PublishError(f"the {role} key {known[0]['id']} is revoked; it cannot be named again")
            continue
        key_id = f"{role}-{version}"
        if key_id in key_id_of({"keys": keys}):
            raise PublishError(f"key id {key_id} is already used by another key")
        keys.append({"id": key_id, "role": role, "public_key": public, "not_after": not_after})
    doc = {"format": FORMAT, "version": version, "keys": keys}
    # Hosts accept a document only when a usable root key of the one they hold signed it, and only when it
    # still names a usable root and release key; refuse here what every host would refuse.
    stamp_now = stamp(now())
    signer_public = base64.b64encode(public_raw(signer)).decode()
    holders = (previous or doc)["keys"]
    if not any(key["role"] == "root" and not key.get("revoked") and key["not_after"] > stamp_now and key["public_key"] == signer_public
               for key in holders):
        raise PublishError("--sign-with is not a usable root key of the trust document hosts already hold" if previous
                           else "the signing key is not the root key of the new trust document")
    for role in ("root", "release"):
        if not any(key["role"] == role and not key.get("revoked") and key["not_after"] > stamp_now for key in keys):
            raise PublishError(f"the new trust document would name no usable {role} key")
    if previous and not (base / f"trust.{previous['version']}.json").is_file():
        # A tree published before versioned documents existed: keep the document hosts hold as its own version.
        shutil.copyfile(latest, base / f"trust.{previous['version']}.json")
        shutil.copyfile(base / "trust.sig", base / f"trust.{previous['version']}.sig")
    # The numbered document goes first: whatever trust.json a host reads, the link it names is already there.
    body = write_signed(signer, base, f"trust.{version}.json", doc, f"trust.{version}.sig")
    sig = (base / f"trust.{version}.sig").read_bytes()
    for name, data in (("trust.json", body), ("trust.sig", sig)):
        tmp = base / (name + ".tmp")
        tmp.write_bytes(data)
        os.replace(tmp, base / name)
    print(f"trust.json version {version} written to {args.base}")


def debian_label(label):
    """The Debian form of a tag-style label: 0.4.19-dev.7 is 0.4.19~~dev.7 and 0.4.19-rc1 is 0.4.19~rc1, both below 0.4.19."""
    match = re.fullmatch(r"([0-9][0-9.]*)-(.+)", label)
    if not match:
        return label
    return f"{match[1]}{'~~' if match[2].startswith('dev.') else '~'}{match[2]}"


def dpkg_gt(left, right):
    result = subprocess.run(["dpkg", "--compare-versions", debian_label(left), "gt", debian_label(right)], capture_output=True)
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


UKI_RE = re.compile(r"^nex-core_[A-Za-z0-9.~_-]+_[0-9]+\.efi$")


def check_release_dir(release_dir, built):
    """Refuse a release directory that the host would refuse at stage: the sequence is signed and immutable
    once published, so the only remedy for a broken one would be burning a new sequence. Mirrors
    hostimage.ReadRelease: every file the manifest lists must exist with its size and SHA-256, the three
    image files and exactly one UKI named for the label and sequence must be listed, the roothash file must
    agree with the manifest, and nothing unlisted may ride along in the archive."""
    label, sequence, roothash = built["label"], int(built["sequence"]), built["roothash"]
    if not isinstance(roothash, str) or not HEX64_RE.match(roothash):
        raise PublishError("the release manifest's roothash is not 64 lowercase hex characters")
    files = built.get("files")
    if not isinstance(files, dict) or not files:
        raise PublishError("the release manifest lists no files (rebuild with image/release.py)")
    for need in ("root.erofs", "root.verity", "roothash"):
        if need not in files:
            raise PublishError(f"the release manifest lists no {need}")
    ukis = [name for name in files if UKI_RE.match(name)]
    if ukis != [f"nex-core_{label}_{sequence}.efi"]:
        raise PublishError(f"the release needs exactly one UKI named nex-core_{label}_{sequence}.efi, the manifest lists {ukis or 'none'}")
    for name, entry in files.items():
        parts = name.split("/")
        if name == "manifest.json" or name.startswith("/") or any(part in ("", ".", "..") for part in parts):
            raise PublishError(f"the release manifest names an invalid file {name!r}")
        # make_slot_image tars what rglob finds and rglob does not descend into a linked directory: a listed
        # file behind a link would hash fine here and be missing from the signed archive.
        walked = release_dir
        for part in parts:
            walked = walked / part
            if walked.is_symlink():
                raise PublishError(f"the release manifest lists {name}, which goes through the link {walked}")
        path = release_dir / name
        if not path.is_file():
            raise PublishError(f"the release manifest lists {name}, which is not a file in {release_dir}")
        if not isinstance(entry, dict) or path.stat().st_size != entry.get("size") or sha256_file(path) != entry.get("sha256"):
            raise PublishError(f"{name} does not match its size and sha256 in the release manifest; rebuild the release")
    if (release_dir / "roothash").read_text().strip() != roothash:
        raise PublishError("the roothash file does not match the release manifest")
    unlisted = sorted(rel for rel in (str(p.relative_to(release_dir)) for p in release_dir.rglob("*") if p.is_file() or p.is_symlink())
                      if rel not in files and rel != "manifest.json")
    if unlisted:
        raise PublishError(f"files not listed in the release manifest would ship unauthenticated: {', '.join(unlisted)}")


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


def channel_state(args, sequence):
    """The channel's current serial and the floor its next pointer carries; refuses a floor above the sequence."""
    serial, min_sequence = 0, args.min_sequence
    previous = Path(args.base) / args.channel / "channel.json"
    if previous.is_file():
        before = json.loads(previous.read_text())
        serial = before["channel_serial"]
        # The floor only goes up: a host that has not yet seen the higher one learns it from this pointer.
        min_sequence = max(min_sequence, before.get("min_sequence", 0))
    if min_sequence > sequence:
        raise PublishError(f"sequence {sequence} is below the channel's min_sequence {min_sequence}; hosts refuse such a pointer")
    return serial, min_sequence


def write_channel(args, key, sequence, manifest_body):
    channel_dir = Path(args.base) / args.channel
    serial, min_sequence = channel_state(args, sequence)
    doc = {"format": FORMAT, "channel": args.channel, "channel_serial": serial + 1, "sequence": sequence,
           "manifest_sha256": hashlib.sha256(manifest_body).hexdigest(),
           "expires": stamp(now() + datetime.timedelta(days=args.expires_days)), "min_sequence": min_sequence}
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
    try:
        built = json.loads((release_dir / "manifest.json").read_text())
        label, sequence, roothash = built["label"], int(built["sequence"]), built["roothash"]
    except (OSError, ValueError, KeyError, TypeError) as error:
        raise PublishError(f"{release_dir} has no readable release manifest.json with label, sequence and roothash") from error
    if sequence < 1:
        raise PublishError("a published release needs sequence >= 1 (build with NEX_SEQUENCE)")
    check_release_dir(release_dir, built)
    check_order(args.base, label, sequence)
    channel_state(args, sequence)

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
        if args.min_controller_api_epoch < 0:
            raise PublishError("--min-controller-api-epoch cannot be negative")
        if args.controller_first and not any(component["kind"] == "vm-image" for _, component in sources):
            raise PublishError("--controller-first needs the Controller image: add --payload vm-image:ID=FILE")
        manifest = {"format": FORMAT, "sequence": sequence, "label": label, "architecture": ARCHITECTURE,
                    "created": stamp(now()), "min_from_sequence": args.min_from_sequence,
                    "compat": {"update_engine": 1, "state_format": args.state_format, "min_state_format": args.min_state_format,
                               "api_epoch": args.api_epoch, "min_controller_api_epoch": args.min_controller_api_epoch,
                               "mixed_pool": args.mixed_pool, "controller_first": args.controller_first},
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
            ensure_manifest_signature(args.base, key, published)
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
        if (current["sequence"] == sequence and current["manifest_sha256"] == hashlib.sha256(body).hexdigest()
                and current.get("min_sequence", 0) == channel_state(args, sequence)[1]):
            print(f"{args.channel}/channel.json already points at sequence {sequence}")
            return
    write_channel(args, key, sequence, body)


def promote(args):
    key = key_path(args.release_key, "NEX_UPDATE_RELEASE_KEY", "release")
    manifest = Path(args.base) / "releases" / str(args.sequence) / "manifest.json"
    if not manifest.is_file():
        raise PublishError(f"sequence {args.sequence} is not published in {args.base}")
    ensure_manifest_signature(args.base, key, manifest)
    write_channel(args, key, args.sequence, manifest.read_bytes())


def prune(args):
    """Drop every release except the newest --keep and the ones a channel points at, then the blobs none of them uses."""
    base = Path(args.base)
    sequences = sorted(int(p.name) for p in (base / "releases").glob("*") if p.name.isdigit())
    keep = set(sequences[-args.keep:]) | {json.loads(p.read_text())["sequence"] for p in base.glob("*/channel.json")}
    live = set()
    for sequence in sequences:
        release = base / "releases" / str(sequence)
        if sequence in keep:
            live |= {c["url"] for c in json.loads((release / "manifest.json").read_text())["components"]}
        else:
            shutil.rmtree(release)
    for blob in (base / "blobs").glob("*"):
        if f"blobs/{blob.name}" not in live:
            blob.unlink()
    print(f"kept releases {sorted(keep)}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("keygen", help="write throwaway development keys")
    p.add_argument("--out", required=True)
    p.set_defaults(run=keygen)

    p = sub.add_parser("trust", help="sign trust.json with a root key (publishes trust.<version>.json/.sig beside it)")
    p.add_argument("--base", required=True)
    p.add_argument("--root-key", help="the root key this document names (and signs with unless --sign-with is given)")
    p.add_argument("--release-key", help="the release key this document names")
    p.add_argument("--sign-with", help="root key that signs the document: a root key of the trust.json hosts already hold "
                   "(root rotation: sign with the old root key, name the new one with --root-key)")
    p.add_argument("--revoke", action="append", default=[], metavar="KEY_ID", help="mark a key of the previous document revoked (kept in the list)")
    p.add_argument("--drop-key", action="append", default=[], metavar="KEY_ID", help="leave a key of the previous document out (end of its overlap)")
    p.add_argument("--version", type=int, help="must equal one more than the trust.json in --base, else 1 (the default)")
    p.add_argument("--allow-orphaned-signatures", action="store_true",
                   help="revoke or drop a release key although published channel.json/manifest.json signatures still verify under it")
    p.add_argument("--days", type=int, default=DEFAULT_TRUST_DAYS, help="validity of the keys this run adds")
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
    p.add_argument("--min-controller-api-epoch", type=int, default=0,
                   help="the oldest Controller api_epoch this release runs with (0: no minimum)")
    p.add_argument("--mixed-pool", action=argparse.BooleanOptionalAction, default=True,
                   help="the release may run beside the previous one in a pool; --no-mixed-pool makes a rollout need running_vms shutdown")
    p.add_argument("--controller-first", action="store_true",
                   help="a pool rollout replaces the Controller image first (needs a --payload vm-image:...)")
    p.add_argument("--min-from-sequence", type=int, default=0)
    p.add_argument("--md-generation", type=int, default=0)
    p.add_argument("--md-fallback-safe", action="store_true")
    p.set_defaults(run=publish)

    p = sub.add_parser("promote", help="re-sign only channel.json to point at a published release")
    release_options(p)
    p.add_argument("--sequence", type=int, required=True)
    p.set_defaults(run=promote)

    p = sub.add_parser("prune", help="delete old releases and the blobs only they use")
    p.add_argument("--base", required=True)
    p.add_argument("--keep", type=int, default=5, help="newest releases to keep besides those a channel points at")
    p.set_defaults(run=prune)

    args = parser.parse_args(argv)
    try:
        args.run(args)
    except PublishError as error:
        print(f"update-publish: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
