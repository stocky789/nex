import json
import signal
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from tests.integration import backup_repository_destinations_lab as lab_smoke


class SelectedLabTest(unittest.TestCase):
    def selected_fixture(self, cache):
        lab = cache / "nex-cluster-ha-lab.selected"
        lab.mkdir()
        (lab / "lab.json").write_text(json.dumps(lab_smoke.FIXTURE))
        (lab / "ready.json").write_text("{}")
        ports = {"storage": 44001, "core1": 44002, "core2": 44003}
        (lab / "ports.json").write_text(json.dumps(ports))
        for name, port in ports.items():
            (lab / f"{name}.qcow2").touch()
            command = ["qemu-system-x86_64",
                       f"file={lab / (name + '.qcow2')},format=qcow2,if=virtio",
                       f"unix:{lab / (name + '-qmp.sock')},server=on,wait=off",
                       "-netdev", f"user,id=nat,hostfwd=tcp:127.0.0.1:{port}-:22"]
            (lab / f"{name}-command.json").write_text(json.dumps(command))
            (lab / f"{name}-qemu.process.json").write_text(json.dumps(
                {"pid": port, "starttime": 1, "command": command}))
        return lab

    def test_requires_exact_ready_disposable_fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = Path(directory)
            with mock.patch.object(lab_smoke, "CACHE", cache):
                lab = cache / "nex-cluster-ha-lab.selected"
                lab.mkdir()
                (lab / "lab.json").write_text(json.dumps(lab_smoke.FIXTURE))
                with self.assertRaises(ValueError):
                    lab_smoke.selected_lab(lab)
                (lab / "ready.json").write_text("{}")
                self.assertEqual(lab_smoke.selected_lab(lab), lab)
                alias = cache / "nex-cluster-ha-lab.alias"
                alias.symlink_to(lab)
                with self.assertRaises(ValueError):
                    lab_smoke.selected_lab(alias)
                (lab / "lab.json").write_text("{}")
                with self.assertRaises(ValueError):
                    lab_smoke.selected_lab(lab)

    def test_mount_identity_must_match_selected_private_service(self):
        lab_smoke.require_mount("198.18.90.10:/srv/nex-backup-smoke/nfs nfs4", "nfs")
        lab_smoke.require_mount("//198.18.90.10/nexbackupsmoke cifs", "smb")
        with self.assertRaises(RuntimeError):
            lab_smoke.require_mount("198.18.90.20:/srv/nex-backup-smoke/nfs nfs4", "nfs")

    def test_stale_guest_blocks_remote_before_ssh(self):
        with tempfile.TemporaryDirectory() as directory:
            with mock.patch.object(lab_smoke, "CACHE", Path(directory)):
                lab = self.selected_fixture(Path(directory))
                with mock.patch.object(lab_smoke.fixture, "owned_process", return_value=False), \
                        mock.patch.object(lab_smoke.subprocess, "run") as run:
                    with self.assertRaises(RuntimeError):
                        lab_smoke.remote(lab, "core1", "true")
                    run.assert_not_called()

    def test_immediate_kill_requires_owned_selected_storage_process(self):
        with tempfile.TemporaryDirectory() as directory:
            with mock.patch.object(lab_smoke, "CACHE", Path(directory)):
                lab = self.selected_fixture(Path(directory))
                with mock.patch.object(lab_smoke.fixture, "owned_process", return_value=True), \
                        mock.patch.object(lab_smoke.fixture, "process_exists", return_value=False), \
                        mock.patch.object(lab_smoke.os, "pidfd_open", return_value=9), \
                        mock.patch.object(lab_smoke.os, "close"), \
                        mock.patch.object(lab_smoke.signal, "pidfd_send_signal") as send:
                    lab_smoke.kill_owned_storage_now(lab)
                    send.assert_called_once_with(9, signal.SIGKILL)
                    command = json.loads((lab / "storage-command.json").read_text())
                    command[1] = "file=/tmp/foreign.qcow2,format=qcow2,if=virtio"
                    (lab / "storage-command.json").write_text(json.dumps(command))
                    with self.assertRaises(RuntimeError):
                        lab_smoke.kill_owned_storage_now(lab)
                    send.assert_called_once()

    def test_mismatched_forwarded_port_blocks_remote(self):
        with tempfile.TemporaryDirectory() as directory:
            with mock.patch.object(lab_smoke, "CACHE", Path(directory)):
                lab = self.selected_fixture(Path(directory))
                (lab / "ports.json").write_text(json.dumps(
                    {"storage": 44001, "core1": 44999, "core2": 44003}))
                with mock.patch.object(lab_smoke.fixture, "owned_process", return_value=True), \
                        mock.patch.object(lab_smoke.subprocess, "run") as run:
                    with self.assertRaises(RuntimeError):
                        lab_smoke.remote(lab, "core1", "true")
                    run.assert_not_called()

    def test_partial_minio_fault_attempts_idempotent_restore(self):
        for metadata in (False, True):
            with self.subTest(metadata=metadata), tempfile.TemporaryDirectory() as directory:
                calls = []

                def remote(_lab, _host, script, _stdin=""):
                    calls.append(script)
                    if len(calls) == 1:
                        return "first"
                    if len(calls) == 2:
                        return "upload-id"
                    if len(calls) == 3:
                        raise RuntimeError("SSH lost after partial fault mutation")
                    return ""

                with mock.patch.object(lab_smoke, "remote", side_effect=remote):
                    with self.assertRaisesRegex(RuntimeError, "partial fault"):
                        lab_smoke.run_minio_degraded_profile(Path(directory), metadata=metadata)
                self.assertEqual(len(calls), 4)
                restore = calls[3]
                self.assertIn("systemctl stop nex-backup-minio.service", restore)
                if metadata:
                    self.assertIn('if test -e "$point" || test -e "$multipart"', restore)
                else:
                    self.assertIn("if test -e /srv/nex-backup-smoke/minio-data.paused", restore)

    def test_minio_multipart_permissions_are_bucket_scoped(self):
        bucket, objects = lab_smoke.MINIO_POLICY["Statement"]
        self.assertEqual(bucket["Resource"], ["arn:aws:s3:::nex-backup-smoke"])
        self.assertEqual(objects["Resource"], ["arn:aws:s3:::nex-backup-smoke/*"])
        self.assertIn("s3:ListBucketMultipartUploads", bucket["Action"])
        self.assertIn("s3:AbortMultipartUpload", objects["Action"])
        self.assertIn("s3:ListMultipartUploadParts", objects["Action"])
        self.assertTrue(all(not action.startswith("admin:") for statement in
                            (bucket, objects) for action in statement["Action"]))


if __name__ == "__main__":
    unittest.main()
