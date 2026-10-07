import contextlib
import hashlib
import importlib.util
import io
import json
import shutil
import subprocess
import tarfile
import tempfile
import unittest
import unittest.mock
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("update_publish", ROOT / "scripts/update-publish.py")
update_publish = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update_publish)

NEEDS_TOOLS = shutil.which("dpkg") and shutil.which("openssl") and shutil.which("zstd")


def run(*argv):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = update_publish.main(list(argv))
    return code, out.getvalue(), err.getvalue()


def seal(directory):
    """List every file of a release directory in its manifest.json with size and SHA-256, as image/release.py does."""
    manifest = json.loads((directory / "manifest.json").read_text())
    manifest["files"] = {str(path.relative_to(directory)): {"sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                                                           "size": path.stat().st_size}
                         for path in sorted(directory.rglob("*")) if path.is_file() and path.name != "manifest.json"}
    (directory / "manifest.json").write_text(json.dumps(manifest))


def fake_release(directory, label, sequence, roothash="ab" * 32):
    directory.mkdir(parents=True)
    for name, body in (("root.erofs", b"erofs"), ("root.verity", b"verity"), ("roothash", roothash.encode()),
                       (f"nex-core_{label}_{sequence}.efi", b"uki")):
        (directory / name).write_bytes(body)
    (directory / "esp/EFI").mkdir(parents=True)
    (directory / "esp/EFI/loader.efi").write_bytes(b"loader")
    (directory / "manifest.json").write_text(json.dumps({"label": label, "sequence": sequence, "roothash": roothash}))
    seal(directory)
    return directory


def fake_layer(directory, name, built_for, data=None):
    """A layer file and the descriptor image/mkosi.images/build-layer.sh writes beside it."""
    directory.mkdir(parents=True, exist_ok=True)
    data = data or f"layer {name} for {built_for}".encode()
    path = directory / f"{name}.raw"
    path.write_bytes(data)
    Path(str(path) + ".json").write_text(json.dumps({"id": name, "kind": "layer", "sha256": hashlib.sha256(data).hexdigest(),
                                                    "bytes": len(data), "requires_base": built_for}))
    return path


@unittest.skipUnless(NEEDS_TOOLS, "dpkg and openssl are required")
class UpdatePublishTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="nex-update-publish-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.keys, self.base = self.tmp / "keys", self.tmp / "pub"
        self.assertEqual(run("keygen", "--out", str(self.keys))[0], 0)
        self.source = self.tmp / "source.tar.gz"
        self.source.write_bytes(b"source")
        self.compliance = self.tmp / "compliance.json"
        self.compliance.write_bytes(b"{}")

    def publish(self, label, sequence, **extra):
        release = fake_release(self.tmp / f"rel-{sequence}-{label}", label, sequence)
        argv = ["publish", "--base", str(self.base), "--release-dir", str(release), "--release-key", str(self.keys / "release.key"),
                "--source", str(self.source), "--compliance", str(self.compliance), "--channel", extra.get("channel", "dev")]
        return run(*argv)

    def verify(self, key, document, signature):
        pub = self.tmp / "pub.pem"
        subprocess.run(["openssl", "pkey", "-in", str(key), "-pubout", "-out", str(pub)], check=True)
        return subprocess.run(["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(pub), "-rawin", "-in", str(document),
                               "-sigfile", str(signature)], capture_output=True).returncode == 0

    def test_sequence_order_must_equal_dpkg_label_order(self):
        # BUG-0416: the label is display text, but it may never disagree with the signed sequence.
        self.assertEqual(self.publish("1.9", 1)[0], 0)
        self.assertEqual(self.publish("1.10", 2)[0], 0, "1.10 is newer than 1.9 for dpkg, not for a lexical compare")
        code, _, err = self.publish("1.10.1", 3)
        self.assertEqual(code, 0, err)
        code, _, err = self.publish("1.9~rc1", 4)
        self.assertEqual(code, 1)
        self.assertIn("disagrees with dpkg", err)
        self.assertFalse((self.base / "releases/4").exists(), "a refused release leaves nothing behind")
        self.assertEqual(self.publish("1.11", 3)[0], 1, "a sequence is never reused for another label")
        self.assertEqual(self.publish("1.10", 9)[0], 1, "a label is never published under a second sequence")

    def test_signs_with_the_release_key_and_ratchets_the_channel(self):
        self.assertEqual(run("trust", "--base", str(self.base), "--root-key", str(self.keys / "root.key"),
                             "--release-key", str(self.keys / "release.key"))[0], 0)
        self.assertTrue(self.verify(self.keys / "root.key", self.base / "trust.json", self.base / "trust.sig"))
        self.assertFalse(self.verify(self.keys / "release.key", self.base / "trust.json", self.base / "trust.sig"))
        self.assertEqual(self.publish("1.0", 1)[0], 0)
        self.assertEqual(self.publish("1.1", 2)[0], 0)
        manifest = self.base / "releases/2/manifest.json"
        channel = self.base / "dev/channel.json"
        self.assertTrue(self.verify(self.keys / "release.key", manifest, self.base / "releases/2/manifest.sig"))
        self.assertTrue(self.verify(self.keys / "release.key", channel, self.base / "dev/channel.sig"))
        self.assertFalse(self.verify(self.keys / "root.key", channel, self.base / "dev/channel.sig"))
        pointer = json.loads(channel.read_text())
        self.assertEqual((pointer["channel_serial"], pointer["sequence"]), (2, 2))
        self.assertEqual(pointer["manifest_sha256"], hashlib.sha256(manifest.read_bytes()).hexdigest())
        components = {c["id"]: c for c in json.loads(manifest.read_text())["components"]}
        for component in components.values():
            blob = self.base / component["url"]
            self.assertEqual(hashlib.sha256(blob.read_bytes()).hexdigest(), component["sha256"])
            self.assertEqual(blob.stat().st_size, component["bytes"])
        self.assertEqual(components["slot-image"]["verity_root_hash"], "ab" * 32)
        slot = components["slot-image"]
        self.assertEqual(slot["encoding"], "tar+zstd")
        raw = subprocess.run(["zstd", "-dc"], input=(self.base / slot["url"]).read_bytes(), capture_output=True, check=True).stdout
        with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
            self.assertEqual(sorted(archive.getnames()), ["esp/EFI/loader.efi", "manifest.json", "nex-core_1.1_2.efi", "root.erofs",
                                                          "root.verity", "roothash"])

    def test_a_development_label_sorts_below_its_release(self):
        self.assertEqual(self.publish("0.4.33-dev.294", 294)[0], 0)
        self.assertEqual(self.publish("0.4.33", 300)[0], 0)
        self.assertEqual(self.publish("0.4.34-dev.310", 310)[0], 0)
        self.assertEqual(self.publish("0.4.33-dev.311", 311)[0], 1)

    def test_prune_keeps_the_newest_releases_and_what_they_use(self):
        for sequence in (1, 2, 3):
            self.assertEqual(self.publish(f"1.{sequence}", sequence)[0], 0)
        self.assertEqual(run("prune", "--base", str(self.base), "--keep", "1")[0], 0)
        self.assertEqual(sorted(p.name for p in (self.base / "releases").iterdir()), ["3"])
        wanted = {c["url"] for c in json.loads((self.base / "releases/3/manifest.json").read_text())["components"]}
        self.assertEqual({f"blobs/{p.name}" for p in (self.base / "blobs").iterdir()}, wanted)

    def test_a_published_sequence_is_immutable(self):
        key = ["--release-key", str(self.keys / "release.key")]
        release = fake_release(self.tmp / "rel", "1.0", 1)
        argv = ["publish", "--base", str(self.base), "--release-dir", str(release), *key, "--source", str(self.source),
                "--compliance", str(self.compliance), "--channel", "dev"]
        self.assertEqual(run(*argv)[0], 0)
        tracked = [self.base / "releases/1/manifest.json", self.base / "releases/1/manifest.sig",
                   self.base / "dev/channel.json", self.base / "dev/channel.sig"]
        before = {p: p.read_bytes() for p in tracked}
        blobs = sorted(p.name for p in (self.base / "blobs").iterdir())
        code, out, err = run(*argv)
        self.assertEqual(code, 0, err)
        self.assertIn("unchanged", out)
        self.assertEqual({p: p.read_bytes() for p in tracked}, before, "an identical republish rewrites nothing")
        # Changed content under the same sequence and label (the UKI differs, the roothash does not).
        (release / "esp/EFI/loader.efi").write_bytes(b"another loader")
        seal(release)
        code, _, err = run(*argv)
        self.assertEqual(code, 1)
        self.assertIn("already published with different content", err)
        self.assertEqual({p: p.read_bytes() for p in tracked}, before, "a rejected republish leaves the tree as it was")
        self.assertEqual(sorted(p.name for p in (self.base / "blobs").iterdir()), blobs, "and stores no blob")
        # A flag that changes the signed compat block is also a change.
        (release / "esp/EFI/loader.efi").write_bytes(b"loader")
        seal(release)
        code, _, err = run(*argv, "--state-format", "4")
        self.assertEqual(code, 1)
        self.assertEqual({p: p.read_bytes() for p in tracked}, before)

    def test_an_interrupted_publish_is_completed_by_repeating_it(self):
        release = fake_release(self.tmp / "rel", "1.0", 1)
        argv = ["publish", "--base", str(self.base), "--release-dir", str(release), "--release-key", str(self.keys / "release.key"),
                "--source", str(self.source), "--compliance", str(self.compliance), "--channel", "dev"]
        self.assertEqual(run(*argv)[0], 0)
        manifest = (self.base / "releases/1/manifest.json").read_bytes()
        shutil.rmtree(self.base / "dev")  # the crash came before the channel pointer was written
        self.assertEqual(run(*argv)[0], 0)
        pointer = json.loads((self.base / "dev/channel.json").read_text())
        self.assertEqual(pointer["manifest_sha256"], hashlib.sha256(manifest).hexdigest())
        self.assertEqual((self.base / "releases/1/manifest.json").read_bytes(), manifest)

    def test_retry_restores_manifest_signature_after_document_rename_without_rewriting_it(self):
        release = fake_release(self.tmp / "rel", "1.0", 1)
        argv = ["publish", "--base", str(self.base), "--release-dir", str(release),
                "--release-key", str(self.keys / "release.key"), "--source", str(self.source),
                "--compliance", str(self.compliance), "--channel", "dev"]
        manifest = self.base / "releases/1/manifest.json"
        signature = manifest.with_name("manifest.sig")
        replace = update_publish.os.replace

        def interrupt_signature(source, target):
            if Path(target) == signature:
                raise OSError("interrupted before signature rename")
            return replace(source, target)

        with unittest.mock.patch.object(update_publish.os, "replace", side_effect=interrupt_signature):
            with self.assertRaisesRegex(OSError, "interrupted before signature rename"):
                run(*argv)
        original = manifest.read_bytes()
        self.assertFalse(signature.exists())
        self.assertFalse((self.base / "dev/channel.json").exists())
        later = update_publish.now() + update_publish.datetime.timedelta(days=1)
        with unittest.mock.patch.object(update_publish, "now", return_value=later):
            code, _, err = run(*argv)
        self.assertEqual(code, 0, err)
        self.assertEqual(manifest.read_bytes(), original)
        self.assertTrue(self.verify(self.keys / "release.key", manifest, signature))
        pointer = json.loads((self.base / "dev/channel.json").read_text())
        self.assertEqual(pointer["manifest_sha256"], hashlib.sha256(original).hexdigest())

    def test_retry_repairs_invalid_signature_without_changing_committed_manifest_or_pointer(self):
        self.assertEqual(self.publish_with("1.0", 1)[0], 0)
        manifest = self.base / "releases/1/manifest.json"
        signature = manifest.with_name("manifest.sig")
        pointer = self.base / "dev/channel.json"
        original, channel = manifest.read_bytes(), pointer.read_bytes()
        signature.write_bytes(b"not a signature")
        code, _, err = self.publish_with("1.0", 1)
        self.assertEqual(code, 0, err)
        self.assertEqual(manifest.read_bytes(), original)
        self.assertEqual(pointer.read_bytes(), channel)
        self.assertTrue(self.verify(self.keys / "release.key", manifest, signature))

    def test_retry_preserves_signature_from_usable_overlapping_release_key(self):
        self.assertEqual(self.trust()[0], 0)
        self.assertEqual(self.publish_with("1.0", 1)[0], 0)
        manifest = self.base / "releases/1/manifest.json"
        signature = manifest.with_name("manifest.sig")
        original = signature.read_bytes()
        new_keys = self.tmp / "keys2"
        self.assertEqual(run("keygen", "--out", str(new_keys))[0], 0)
        self.assertEqual(self.trust("--sign-with", str(self.keys / "root.key"), keys=new_keys)[0], 0)
        code, _, err = run("publish", "--base", str(self.base), "--release-dir", str(self.tmp / "rel-1-1.0"),
                           "--release-key", str(new_keys / "release.key"), "--source", str(self.source),
                           "--compliance", str(self.compliance), "--channel", "dev")
        self.assertEqual(code, 0, err)
        self.assertEqual(signature.read_bytes(), original)
        self.assertTrue(self.verify(self.keys / "release.key", manifest, signature))

    def test_missing_signature_cannot_be_repaired_by_an_untrusted_release_key(self):
        self.assertEqual(self.trust()[0], 0)
        self.assertEqual(self.publish_with("1.0", 1)[0], 0)
        manifest = self.base / "releases/1/manifest.json"
        original = manifest.read_bytes()
        manifest.with_name("manifest.sig").unlink()
        shutil.rmtree(self.base / "dev")
        new_keys = self.tmp / "keys2"
        self.assertEqual(run("keygen", "--out", str(new_keys))[0], 0)
        code, _, err = run("publish", "--base", str(self.base), "--release-dir", str(self.tmp / "rel-1-1.0"),
                           "--release-key", str(new_keys / "release.key"), "--source", str(self.source),
                           "--compliance", str(self.compliance), "--channel", "dev")
        self.assertEqual(code, 1)
        self.assertIn("usable release key", err)
        self.assertEqual(manifest.read_bytes(), original)
        self.assertFalse(manifest.with_name("manifest.sig").exists())
        self.assertFalse((self.base / "dev/channel.json").exists())

    def test_promote_restores_missing_manifest_signature_before_exposing_channel(self):
        self.assertEqual(self.publish_with("1.0", 1)[0], 0)
        manifest = self.base / "releases/1/manifest.json"
        signature = manifest.with_name("manifest.sig")
        original = manifest.read_bytes()
        signature.unlink()
        code, _, err = run("promote", "--base", str(self.base), "--channel", "stable", "--sequence", "1",
                           "--release-key", str(self.keys / "release.key"))
        self.assertEqual(code, 0, err)
        self.assertEqual(manifest.read_bytes(), original)
        self.assertTrue(self.verify(self.keys / "release.key", manifest, signature))
        pointer = json.loads((self.base / "stable/channel.json").read_text())
        self.assertEqual(pointer["manifest_sha256"], hashlib.sha256(original).hexdigest())

    def test_promote_re_signs_only_the_channel(self):
        self.assertEqual(self.publish("1.0", 1)[0], 0)
        self.assertEqual(self.publish("1.1", 2)[0], 0)
        before = (self.base / "releases/1/manifest.json").read_bytes()
        code, _, err = run("promote", "--base", str(self.base), "--channel", "stable", "--sequence", "1",
                           "--release-key", str(self.keys / "release.key"))
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads((self.base / "stable/channel.json").read_text())["sequence"], 1)
        self.assertEqual((self.base / "releases/1/manifest.json").read_bytes(), before)
        self.assertEqual(run("promote", "--base", str(self.base), "--channel", "stable", "--sequence", "7",
                             "--release-key", str(self.keys / "release.key"))[0], 1)

    def test_fails_closed_without_a_key_or_the_licence_components(self):
        release = fake_release(self.tmp / "rel", "1.0", 1)
        common = ["--base", str(self.base), "--release-dir", str(release), "--source", str(self.source), "--compliance", str(self.compliance)]
        with unittest.mock.patch.dict("os.environ", {}, clear=False) as env:
            env.pop("NEX_UPDATE_RELEASE_KEY", None)
            code, _, err = run("publish", *common)
        self.assertEqual(code, 1)
        self.assertIn("without the release key", err)
        self.assertFalse(self.base.exists(), "nothing is written without a key")
        key = ["--release-key", str(self.keys / "release.key")]
        code, _, err = run("publish", "--base", str(self.base), "--release-dir", str(release), "--compliance", str(self.compliance), *key)
        self.assertEqual(code, 1)
        self.assertIn("--source", err)
        code, _, err = run("publish", "--base", str(self.base), "--release-dir", str(release), "--source", str(self.source), *key)
        self.assertEqual(code, 1)
        self.assertIn("--compliance", err)
        shutil.rmtree(release / "esp")
        (release / "root.verity").unlink()
        self.assertEqual(run("publish", *common, *key)[0], 1, "an incomplete release directory is refused")
        self.assertEqual(run("trust", "--base", str(self.base), "--root-key", str(self.tmp / "nokey"),
                             "--release-key", str(self.keys / "release.key"))[0], 1)

    def test_bundle_carries_the_profile_and_its_own_signed_expiry(self):
        bundle_spec = importlib.util.spec_from_file_location("update_bundle", ROOT / "scripts/update-bundle.py")
        update_bundle = importlib.util.module_from_spec(bundle_spec)
        bundle_spec.loader.exec_module(update_bundle)
        key = str(self.keys / "release.key")
        self.assertEqual(run("trust", "--base", str(self.base), "--root-key", str(self.keys / "root.key"), "--release-key", key)[0], 0)
        self.assertEqual(self.publish("1.0", 1)[0], 0)
        out = self.tmp / "media"
        self.assertEqual(update_bundle.main(["--base", str(self.base), "--channel", "dev", "--out", str(out), "--release-key", key]), 0)
        manifest = json.loads((out / "releases/1/manifest.json").read_text())
        slot = next(c for c in manifest["components"] if c["kind"] == "slot-image")
        self.assertEqual([p.name for p in (out / "blobs").iterdir()], [f"sha256-{slot['sha256']}"], "minimal carries the slot image only")
        self.assertTrue(self.verify(self.keys / "release.key", out / "dev/channel.json", out / "dev/channel.sig"))
        online, offline = (json.loads((d / "dev/channel.json").read_text()) for d in (self.base, out))
        self.assertGreater(offline["expires"], online["expires"], "a bundle's pointer outlives the online one")
        self.assertEqual((offline["channel_serial"], offline["manifest_sha256"]), (online["channel_serial"], online["manifest_sha256"]))
        sums = dict(line.split("  ", 1)[::-1] for line in (out / "SHA256SUMS").read_text().splitlines())
        self.assertEqual(sums[f"blobs/sha256-{slot['sha256']}"], slot["sha256"])
        self.assertEqual(update_bundle.main(["--base", str(self.base), "--channel", "dev", "--out", str(self.tmp / "m2"), "--profile", "full", "--release-key", key]), 0)
        self.assertEqual(len(list((self.tmp / "m2/blobs").iterdir())), 3)
        with unittest.mock.patch.dict("os.environ", {}, clear=False) as env:
            env.pop("NEX_UPDATE_RELEASE_KEY", None)
            self.assertEqual(update_bundle.main(["--base", str(self.base), "--channel", "dev", "--out", str(self.tmp / "m3")]), 1)
        self.assertFalse((self.tmp / "m3").exists())

    def publish_with(self, label, sequence, *extra):
        release = self.tmp / f"rel-{sequence}-{label}"
        if not release.exists():
            fake_release(release, label, sequence)
        return run("publish", "--base", str(self.base), "--release-dir", str(release), "--release-key", str(self.keys / "release.key"),
                   "--source", str(self.source), "--compliance", str(self.compliance), "--channel", "dev", *extra)

    def test_layers_and_payloads_are_signed_components_and_a_minimal_bundle_carries_the_layers(self):
        bundle_spec = importlib.util.spec_from_file_location("update_bundle", ROOT / "scripts/update-bundle.py")
        update_bundle = importlib.util.module_from_spec(bundle_spec)
        bundle_spec.loader.exec_module(update_bundle)
        key = str(self.keys / "release.key")
        self.assertEqual(run("trust", "--base", str(self.base), "--root-key", str(self.keys / "root.key"), "--release-key", key)[0], 0)
        layers = self.tmp / "layers"
        zfs, cluster = fake_layer(layers, "nex-zfs", "1.0-1"), fake_layer(layers, "nex-cluster", "1.0-1")
        controller = self.tmp / "controller.qcow2"
        controller.write_bytes(b"appliance")
        code, _, err = self.publish_with("1.0", 1, "--layer", str(zfs), "--on-demand-layer", str(cluster),
                                         "--payload", f"vm-image:controller={controller}")
        self.assertEqual(code, 0, err)
        components = {c["id"]: c for c in json.loads((self.base / "releases/1/manifest.json").read_text())["components"]}
        self.assertEqual({k: components["nex-zfs"][k] for k in ("kind", "requires_base", "required")},
                         {"kind": "layer", "requires_base": "1.0-1", "required": True})
        self.assertNotIn("on_demand", components["nex-zfs"])
        self.assertEqual({k: components["nex-cluster"][k] for k in ("kind", "requires_base", "required", "on_demand")},
                         {"kind": "layer", "requires_base": "1.0-1", "required": False, "on_demand": True})
        self.assertEqual({k: components["controller"][k] for k in ("kind", "name", "on_demand")},
                         {"kind": "vm-image", "name": "controller.qcow2", "on_demand": True})
        for component in components.values():
            self.assertEqual(hashlib.sha256((self.base / component["url"]).read_bytes()).hexdigest(), component["sha256"])
        self.assertTrue(self.verify(self.keys / "release.key", self.base / "releases/1/manifest.json", self.base / "releases/1/manifest.sig"))

        out = self.tmp / "media"
        self.assertEqual(update_bundle.main(["--base", str(self.base), "--channel", "dev", "--out", str(out), "--release-key", key]), 0)
        self.assertEqual(sorted(p.name for p in (out / "blobs").iterdir()),
                         sorted(f"sha256-{components[i]['sha256']}" for i in ("slot-image", "nex-zfs")),
                         "minimal carries the slot image and the layers every update stages, nothing on demand")
        full = self.tmp / "full"
        self.assertEqual(update_bundle.main(["--base", str(self.base), "--channel", "dev", "--out", str(full), "--profile", "full", "--release-key", key]), 0)
        self.assertEqual(len(list((full / "blobs").iterdir())), len(components))

    def test_a_layer_for_another_base_or_with_the_wrong_bytes_is_never_signed(self):
        layers = self.tmp / "layers"
        wrong_base = fake_layer(layers, "nex-zfs", "1.0-2")
        code, _, err = self.publish_with("1.0", 1, "--layer", str(wrong_base))
        self.assertEqual(code, 1)
        self.assertIn("built for base 1.0-2, not for 1.0-1", err)
        self.assertFalse((self.base / "releases/1").exists(), "a refused release leaves nothing behind")
        tampered = fake_layer(layers, "nex-cluster", "1.0-1")
        tampered.write_bytes(b"not what was built")
        code, _, err = self.publish_with("1.0", 1, "--layer", str(tampered))
        self.assertEqual(code, 1)
        self.assertIn("does not match its descriptor", err)
        good = fake_layer(layers, "nex-zfs", "1.0-1")
        self.assertEqual(self.publish_with("1.0", 1, "--layer", str(good), "--on-demand-layer", str(good))[0], 1, "one id, one component")
        self.assertEqual(self.publish_with("1.0", 1, "--layer", str(layers / "missing.raw"))[0], 1)
        controller = self.tmp / "controller.qcow2"
        controller.write_bytes(b"appliance")
        for spec in (f"floppy:controller={controller}", f"vm-image:../x={controller}", f"vm-image:controller={self.tmp}/none"):
            self.assertEqual(self.publish_with("1.0", 1, "--payload", spec)[0], 1, spec)
        self.assertFalse((self.base / "releases/1").exists())

    def test_keygen_never_overwrites_a_key(self):
        before = (self.keys / "root.key").read_bytes()
        self.assertEqual(run("keygen", "--out", str(self.keys))[0], 1)
        self.assertEqual((self.keys / "root.key").read_bytes(), before)

    def trust(self, *extra, keys=None, base=None):
        keys = keys or self.keys
        return run("trust", "--base", str(base or self.base), "--root-key", str(keys / "root.key"),
                   "--release-key", str(keys / "release.key"), *extra)

    def trust_doc(self, name="trust.json"):
        return json.loads((self.base / name).read_text())

    def test_trust_rotation_is_signed_by_the_old_root_key_and_keeps_the_overlap(self):
        self.assertEqual(self.trust()[0], 0)
        new_keys = self.tmp / "keys2"
        self.assertEqual(run("keygen", "--out", str(new_keys))[0], 0)
        v1 = (self.base / "trust.json").read_bytes()
        # The new root key is not one the hosts trust yet, so it cannot sign its own introduction.
        code, _, err = self.trust(keys=new_keys)
        self.assertEqual(code, 1)
        self.assertIn("--sign-with", err)
        code, _, err = self.trust("--sign-with", str(self.keys / "root.key"), keys=new_keys)
        self.assertEqual(code, 0, err)
        self.assertTrue(self.verify(self.keys / "root.key", self.base / "trust.json", self.base / "trust.sig"))
        self.assertFalse(self.verify(new_keys / "root.key", self.base / "trust.json", self.base / "trust.sig"))
        v2 = self.trust_doc()
        self.assertEqual(v2["version"], 2)
        self.assertEqual({k["id"] for k in v2["keys"]}, {"root-1", "release-1", "root-2", "release-2"},
                         "the old keys stay listed so manifests signed with the old release key still verify")
        # Every version is published under its own name, and the version 1 document of an older tree is kept.
        self.assertEqual((self.base / "trust.2.json").read_bytes(), (self.base / "trust.json").read_bytes())
        self.assertEqual((self.base / "trust.2.sig").read_bytes(), (self.base / "trust.sig").read_bytes())
        self.assertEqual((self.base / "trust.1.json").read_bytes(), v1)
        self.assertTrue(self.verify(self.keys / "root.key", self.base / "trust.1.json", self.base / "trust.1.sig"))
        # A replay is refused, and the next version is signed by the new root key and revokes the old keys.
        self.assertEqual(self.trust("--version", "2", "--sign-with", str(self.keys / "root.key"), keys=new_keys)[0], 1)
        code, _, err = self.trust("--revoke", "release-1", "--revoke", "root-1", keys=new_keys)
        self.assertEqual(code, 0, err)
        v3 = self.trust_doc()
        self.assertEqual(v3["version"], 3)
        self.assertEqual({k["id"]: k.get("revoked", False) for k in v3["keys"]},
                         {"root-1": True, "release-1": True, "root-2": False, "release-2": False})
        self.assertTrue(self.verify(new_keys / "root.key", self.base / "trust.json", self.base / "trust.sig"))
        self.assertTrue(self.verify(self.keys / "root.key", self.base / "trust.2.json", self.base / "trust.2.sig"))
        # A revoked key cannot be named again, and an id that is not in the document is an error.
        self.assertEqual(self.trust(keys=self.keys, base=self.base)[0], 1)
        self.assertEqual(self.trust("--revoke", "nope", keys=new_keys)[0], 1)
        # Dropping ends an overlap: the next version no longer lists the revoked keys at all.
        self.assertEqual(self.trust("--drop-key", "root-1", "--drop-key", "release-1", keys=new_keys)[0], 0)
        self.assertEqual({k["id"] for k in self.trust_doc()["keys"]}, {"root-2", "release-2"})

    def test_bundle_carries_every_trust_version(self):
        bundle_spec = importlib.util.spec_from_file_location("update_bundle", ROOT / "scripts/update-bundle.py")
        update_bundle = importlib.util.module_from_spec(bundle_spec)
        bundle_spec.loader.exec_module(update_bundle)
        key = str(self.keys / "release.key")
        self.assertEqual(self.trust()[0], 0)
        self.assertEqual(self.trust()[0], 0)  # version 2, same keys
        self.assertEqual(self.publish("1.0", 1)[0], 0)
        out = self.tmp / "media"
        self.assertEqual(update_bundle.main(["--base", str(self.base), "--channel", "dev", "--out", str(out), "--release-key", key]), 0)
        for name in ("trust.json", "trust.sig", "trust.1.json", "trust.1.sig", "trust.2.json", "trust.2.sig"):
            self.assertEqual((out / name).read_bytes(), (self.base / name).read_bytes(), name)

    def test_compat_flags_reach_the_signed_manifest(self):
        controller = self.tmp / "controller.qcow2"
        controller.write_bytes(b"appliance")
        code, _, err = self.publish_with("1.0", 1, "--controller-first", "--no-mixed-pool", "--min-controller-api-epoch", "2",
                                         "--payload", f"vm-image:controller={controller}")
        self.assertEqual(code, 0, err)
        compat = json.loads((self.base / "releases/1/manifest.json").read_text())["compat"]
        self.assertEqual((compat["controller_first"], compat["mixed_pool"], compat["min_controller_api_epoch"]), (True, False, 2))
        code, _, err = self.publish_with("1.1", 2)
        self.assertEqual(code, 0, err)
        compat = json.loads((self.base / "releases/2/manifest.json").read_text())["compat"]
        self.assertEqual((compat["controller_first"], compat["mixed_pool"], compat["min_controller_api_epoch"]), (False, True, 0))
        code, _, err = self.publish_with("1.2", 3, "--controller-first")
        self.assertEqual(code, 1)
        self.assertIn("--controller-first needs the Controller image", err)
        self.assertFalse((self.base / "releases/3").exists())

    def test_the_channel_floor_only_goes_up(self):
        self.assertEqual(self.publish("1.0", 1)[0], 0)
        self.assertEqual(self.publish_with("1.1", 2, "--min-sequence", "2")[0], 0)
        self.assertEqual(self.publish("1.2", 3)[0], 0, "a later publish without the flag")
        self.assertEqual(json.loads((self.base / "dev/channel.json").read_text())["min_sequence"], 2)
        key = str(self.keys / "release.key")
        self.assertEqual(run("promote", "--base", str(self.base), "--channel", "dev", "--sequence", "2", "--release-key", key)[0], 0)
        self.assertEqual(json.loads((self.base / "dev/channel.json").read_text())["min_sequence"], 2)
        code, _, err = run("promote", "--base", str(self.base), "--channel", "dev", "--sequence", "1", "--release-key", key)
        self.assertEqual(code, 1)
        self.assertIn("below the channel's min_sequence", err)
        self.assertEqual(json.loads((self.base / "dev/channel.json").read_text())["sequence"], 2)

    def test_a_repeat_publish_with_a_higher_floor_re_signs_and_a_bad_floor_stores_nothing(self):
        self.assertEqual(self.publish("1.0", 1)[0], 0)
        channel = self.base / "dev/channel.json"
        self.assertEqual(json.loads(channel.read_text())["min_sequence"], 0)
        self.assertEqual(self.publish_with("1.0", 1, "--min-sequence", "1")[0], 0)
        after = json.loads(channel.read_text())
        self.assertEqual((after["min_sequence"], after["channel_serial"]), (1, 2), "the cutoff is not dropped by the idempotent path")
        self.assertTrue(self.verify(self.keys / "release.key", channel, self.base / "dev/channel.sig"))
        blobs = sorted(p.name for p in (self.base / "blobs").iterdir())
        code, _, err = self.publish_with("1.1", 2, "--min-sequence", "3")
        self.assertEqual(code, 1)
        self.assertIn("below the channel's min_sequence", err)
        self.assertFalse((self.base / "releases/2").exists())
        self.assertEqual(sorted(p.name for p in (self.base / "blobs").iterdir()), blobs, "no blob is stored for a refused floor")

    def test_trust_version_must_be_the_next_one_and_the_signer_must_be_unexpired(self):
        for version in ("0", "-1", "5"):
            self.assertEqual(self.trust("--version", version)[0], 1, version)
        self.assertFalse((self.base / "trust.json").exists())
        self.assertEqual(self.trust("--version", "1")[0], 0)
        code, _, err = self.trust("--version", "3")
        self.assertEqual(code, 1)
        self.assertIn("must be 2", err)
        self.assertEqual(self.trust_doc()["version"], 1)

        long_ago = update_publish.now() - update_publish.datetime.timedelta(days=3 * 365)
        expired = self.tmp / "expired-base"
        with unittest.mock.patch.object(update_publish, "now", return_value=long_ago):
            self.assertEqual(self.trust(base=expired)[0], 0)
        code, _, err = self.trust(base=expired)
        self.assertEqual(code, 1)
        self.assertIn("not a usable root key", err)
        self.assertEqual(json.loads((expired / "trust.json").read_text())["version"], 1)

    def test_a_release_key_that_still_signs_published_metadata_is_not_dropped_without_the_flag(self):
        self.assertEqual(self.trust()[0], 0)
        new_keys = self.tmp / "keys2"
        self.assertEqual(run("keygen", "--out", str(new_keys))[0], 0)
        self.assertEqual(self.trust("--sign-with", str(self.keys / "root.key"), keys=new_keys)[0], 0)
        self.assertEqual(self.publish("1.0", 1)[0], 0)
        for flag in ("--revoke", "--drop-key"):
            code, _, err = self.trust(flag, "release-1", "--sign-with", str(new_keys / "root.key"), keys=new_keys)
            self.assertEqual(code, 1, flag)
            self.assertIn("still signs published metadata", err)
            self.assertIn("releases/1/manifest.json", err)
            self.assertEqual(self.trust_doc()["version"], 2)
        # The root key of the old pair is not a release signer; dropping it is not held back.
        self.assertEqual(self.trust("--drop-key", "root-1", "--sign-with", str(new_keys / "root.key"), keys=new_keys)[0], 0)
        self.assertEqual(self.trust("--drop-key", "release-1", "--allow-orphaned-signatures", "--sign-with", str(new_keys / "root.key"),
                                    keys=new_keys)[0], 0)
        self.assertEqual({k["id"] for k in self.trust_doc()["keys"]}, {"root-2", "release-2"})

    def test_a_release_directory_that_disagrees_with_its_manifest_is_never_signed(self):
        def tamper_image(release):
            (release / "root.erofs").write_bytes(b"other erofs")

        def drop_uki(release):
            manifest = json.loads((release / "manifest.json").read_text())
            del manifest["files"]["nex-core_1.0_1.efi"]
            (release / "manifest.json").write_text(json.dumps(manifest))

        def wrong_roothash_file(release):
            (release / "roothash").write_text("cd" * 32)
            seal(release)

        def unlisted_file(release):
            (release / "extra.bin").write_bytes(b"not in the manifest")

        def no_file_list(release):
            manifest = json.loads((release / "manifest.json").read_text())
            del manifest["files"]
            (release / "manifest.json").write_text(json.dumps(manifest))

        def missing_file(release):
            (release / "esp/EFI/loader.efi").unlink()

        def nested_manifest(release):
            (release / "sub").mkdir()
            (release / "sub/manifest.json").write_text("{}")

        def linked_directory(release):
            moved = self.tmp / "moved-esp"
            shutil.move(str(release / "esp"), str(moved))
            (release / "esp").symlink_to(moved)

        for name, damage, message in (
                ("a changed image", tamper_image, "does not match its size and sha256"),
                ("no UKI", drop_uki, "exactly one UKI"),
                ("a roothash file that disagrees", wrong_roothash_file, "roothash file does not match"),
                ("an unlisted file", unlisted_file, "not listed in the release manifest"),
                ("an unlisted manifest.json in a subdirectory", nested_manifest, "sub/manifest.json"),
                ("a listed file behind a linked directory", linked_directory, "goes through the link"),
                ("no file list", no_file_list, "lists no files"),
                ("a listed file that is gone", missing_file, "not a file")):
            with self.subTest(name):
                release = fake_release(self.tmp / f"rel-{abs(hash(name))}", "1.0", 1)
                damage(release)
                code, _, err = run("publish", "--base", str(self.base), "--release-dir", str(release), "--release-key",
                                   str(self.keys / "release.key"), "--source", str(self.source), "--compliance", str(self.compliance))
                self.assertEqual(code, 1)
                self.assertIn(message, err)
                self.assertFalse((self.base / "releases/1").exists(), "nothing is signed for a broken release")


if __name__ == "__main__":
    unittest.main()
