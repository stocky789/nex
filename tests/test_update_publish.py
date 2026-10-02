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


def fake_release(directory, label, sequence, roothash="ab" * 32):
    directory.mkdir(parents=True)
    for name, body in (("root.erofs", b"erofs"), ("root.verity", b"verity"), ("roothash", roothash.encode())):
        (directory / name).write_bytes(body)
    (directory / "esp/EFI").mkdir(parents=True)
    (directory / "esp/EFI/loader.efi").write_bytes(b"loader")
    (directory / "manifest.json").write_text(json.dumps({"label": label, "sequence": sequence, "roothash": roothash}))
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
            self.assertEqual(sorted(archive.getnames()), ["esp/EFI/loader.efi", "manifest.json", "root.erofs", "root.verity", "roothash"])

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
        code, _, err = run(*argv)
        self.assertEqual(code, 1)
        self.assertIn("already published with different content", err)
        self.assertEqual({p: p.read_bytes() for p in tracked}, before, "a rejected republish leaves the tree as it was")
        self.assertEqual(sorted(p.name for p in (self.base / "blobs").iterdir()), blobs, "and stores no blob")
        # A flag that changes the signed compat block is also a change.
        (release / "esp/EFI/loader.efi").write_bytes(b"loader")
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


if __name__ == "__main__":
    unittest.main()
