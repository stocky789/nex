# SPDX-License-Identifier: AGPL-3.0-only
import importlib.util
import hashlib
import inspect
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


RUNNER = Path(__file__).parent / "integration" / "libvirt_recovery_ci.py"
SPEC = importlib.util.spec_from_file_location("libvirt_recovery_ci", RUNNER)
CI = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CI)


def passed_report():
    targets = {"vda", "vdb"}
    domain_uuid = "12345678-1234-1234-1234-123456789abc"
    profile = f"libvirt-{domain_uuid} (enforce)"
    package_lines = [f"{package} {CI.SOURCE_VERSION}"
                     for package in CI.SYSTEM_CANDIDATE_PACKAGES]
    commits = {}
    for target in targets:
        interrupted = target == "vda"
        commits[target] = {
            "argv": ["blockcommit", "probe", target, "--top", f"/images/a-{target}.qcow2",
                     "--base", f"/images/d-{target}.qcow2", "--wait", "--verbose"],
            "returncode": 1 if interrupted else 0,
            "stdout": "" if interrupted else "Block Commit: [100.00 %]\nCommit complete",
            "stderr": "error: Disconnected from qemu:///system due to end of file\n" if interrupted else "",
            "jobs_after": {"clear": True},
            "observations": [{"returncode": 0, "stderr": "No current block job"}] if interrupted else [],
            "qemu_pid_before": 42 if interrupted else None,
            "qemu_pid_after": 42 if interrupted else None,
            "daemon_before": {"pid": 10} if interrupted else {},
            "daemon_after": {"pid": 11} if interrupted else {},
            "apparmor_during_commit": {
                "before": {"pid": 42, "domain_uuid": domain_uuid, "context": profile},
                "after": {"pid": 42, "domain_uuid": domain_uuid, "context": profile},
            },
        }
    rows = {label: {"ok": True} for label in (
        "point-b-saved", "point-b-post", "point-c-saved", "point-c-post", "current")}
    case = {
        "case": "public-no-delete-2disk",
        "status": "passed",
        "mode": "midcommit",
        "domain": "probe",
        "uuid": domain_uuid,
        "qemu_pid_before": 42,
        "midcommit_boundary": {
            "command": commits["vda"]["argv"],
            "domain_uuid": domain_uuid,
            "qemu_pid": 42,
            "daemon_before": {"pid": 10},
            "daemon_after": {"pid": 11},
            "qmp_block_jobs": {"ok": True, "return": [{
                "type": "commit", "device": "commit-vda-format", "ready": False,
                "offset": 25, "len": 100,
            }]},
            "boundary": {"final_pre_kill": {
                "qmp_block_jobs": {"ok": True, "return": [{
                    "type": "commit", "device": "commit-vda-format", "ready": False,
                    "offset": 30, "len": 100,
                }]},
                "qemu_pid": 42, "domain_state": "paused",
            }},
            "apparmor_before_restart": {"pid": 42, "domain_uuid": domain_uuid, "context": profile},
            "apparmor_after_restart": {"pid": 42, "domain_uuid": domain_uuid, "context": profile},
        },
        "commits": commits,
        "point_b_redefine": {
            "xml": "<domainsnapshot/>",
            "snapshot_docs": {"labels": ["point-a", "point-b", "point-c"], "current": "point-c"},
        },
        "metadata_delete": {
            "argv": ["snapshot-delete", "probe", "point-a", "--metadata"], "returncode": 0,
        },
        "pre_unlink_reference_scan": {
            "passed": True,
            "paths": ["/images/a-vda.qcow2", "/images/a-vdb.qcow2"],
            "sources_scanned": ["snapshot XML", "inactive domain XML", "QMP block and job graphs",
                                "current and saved-point backing chains"],
        },
        "final_stopped": {
            "inactive_xml": "<domain/>",
            "snapshot_docs": {"labels": ["point-b", "point-c"]},
            "chains": {target: [f"/images/{target}.qcow2"] for target in targets},
            "current_chains": {target: [f"/images/{target}.qcow2"] for target in targets},
            "unlinked_a": [{"path": f"/images/a-{target}.qcow2", "absent_after_unlink": True}
                           for target in targets],
        },
        "final_data": {target: dict(rows) for target in targets},
        "reopen": {
            "state": "running",
            "uuid": domain_uuid,
            "qmp": {name: {"ok": True} for name in (
                "query-block", "query-named-block-nodes", "query-block-jobs", "query-jobs")},
        },
        "reverts": [
            {"label": label, "reads": {target: {"ok": True} for target in targets}}
            for label in ("point-c", "point-b")
        ],
    }
    return {
        "exit_code": 0,
        "uri": "qemu:///system",
        "domain_type": "qemu",
        "scope": CI.system_probe_scope("midcommit"),
        "apparmor": {"kernel_enabled": "Y"},
        "packages": package_lines,
        "cases": [case],
    }


def passed_deep32_report():
    targets = {"vda", "vdb"}
    labels = [f"point-{index:02d}" for index in range(32)]
    domain_uuid = "abcdefab-1234-1234-1234-abcdefabcdef"
    profile = f"libvirt-{domain_uuid} (enforce)"
    base_paths = {target: f"/images/base-{target}.qcow2" for target in targets}
    point_paths = {
        label: {target: f"/images/{label}-{target}.qcow2" for target in targets}
        for label in labels
    }
    oldest = point_paths[labels[0]]
    xml_for = lambda index: (
        f"<domainsnapshot><name>{labels[index]}</name>"
        + (f"<parent><name>{labels[index - 1]}</name></parent>" if index else "")
        + "</domainsnapshot>"
    )
    initial_docs = {"labels": labels, "current": labels[-1],
                    "xml": {label: xml_for(index) for index, label in enumerate(labels)}}
    retained_labels = labels[1:]
    def final_snapshot_xml(index):
        parent = f"<parent><name>{labels[index - 1]}</name></parent>" if index > 1 else ""
        disks = "".join(
            f"<disk device='disk'><source file='{base_paths[target] if index == 1 else point_paths[labels[index - 1]][target]}'/>"
            f"<target dev='{target}'/></disk>" for target in sorted(targets))
        return (f"<domainsnapshot><name>{labels[index]}</name>{parent}<domain><devices>{disks}"
                "</devices></domain></domainsnapshot>")

    final_docs = {"labels": retained_labels, "current": labels[-1],
                  "xml": {label: final_snapshot_xml(index)
                          for index, label in enumerate(labels) if index > 0}}
    commits = {}
    for target in targets:
        commits[target] = {
            "argv": ["blockcommit", "probe", target, "--top", oldest[target],
                     "--base", base_paths[target], "--wait", "--verbose"],
            "returncode": 0,
            "stdout": "Commit complete\n",
            "jobs_after": {"clear": True},
            "apparmor_during_commit": {
                "before": {"pid": 42, "domain_uuid": domain_uuid, "context": profile},
                "after": {"pid": 42, "domain_uuid": domain_uuid, "context": profile},
            },
        }
    final_chains, final_data = {}, {}
    for target in targets:
        chain_map = {base_paths[target]: [base_paths[target]]}
        prefix = []
        for index, label in enumerate(retained_labels, start=1):
            prefix.insert(0, point_paths[label][target])
            chain_map[point_paths[label][target]] = [*prefix, base_paths[target]]
        final_chains[target] = chain_map
        saved, post = {}, {}
        for index, label in enumerate(retained_labels, start=1):
            saved[label] = {
                "marker_ok": True, "payload_ok": True, "marker": 0x20 + index - 1,
                "payload": {"pattern": 0x60 + index - 1,
                            "offset": (8 + index - 1) << 20, "length": 128 << 10},
            }
            post[label] = {
                "marker_ok": True, "payload_ok": True, "marker": 0x20 + index,
                "payload": {"pattern": 0x60 + index,
                            "offset": (8 + index) << 20, "length": 128 << 10},
            }
        final_data[target] = {"saved": saved, "post": post,
                              "current": {"marker_ok": True, "payload_ok": True,
                                          "marker": 0x3F,
                                          "payload": {"pattern": 0x7F, "offset": 39 << 20,
                                                      "length": 128 << 10}}}
    parent_graph = CI.expected_redefined_parent_graph(labels)
    paths = sorted(oldest.values())
    def qmp_block(depth):
        rows = []
        for target in targets:
            image = {"filename": point_paths[labels[-1]][target]}
            cursor = image
            for index in range(depth - 1):
                cursor["backing-image"] = {"filename": f"/images/backing-{index}-{target}.qcow2"}
                cursor = cursor["backing-image"]
            rows.append({"inserted": {"image": image}})
        return {"ok": True, "return": rows}
    case = {
        "case": "public-no-delete-deep32", "status": "passed", "domain": "probe",
        "uuid": domain_uuid, "point_count": 32, "labels": labels,
        "base_paths": base_paths, "point_paths": point_paths, "oldest_post_paths": oldest,
        "initial": {"uuid": domain_uuid, "snapshot_docs": initial_docs,
                    "qmp_query_block": qmp_block(33),
                    "qmp_query_block_depth": {target: 33 for target in targets}},
        "commits": commits,
        "point_01_redefine": {
            "rewritten_sources": base_paths, "base_paths": base_paths,
            "removed_parent": labels[0], "snapshot_docs": initial_docs,
            "parent_graph": parent_graph,
        },
        "metadata_delete": {"argv": ["snapshot-delete", "probe", labels[0], "--metadata"],
                            "returncode": 0},
        "pre_unlink_reference_scan": {
            "passed": True, "paths": paths,
            "sources_scanned": [{"owner": owner} for owner in
                                 ["inactive-domain", *(f"snapshot-{label}" for label in retained_labels)]],
            "foreign_domains": [],
        },
        "final_stopped": {"state": "shut off", "snapshot_docs": final_docs,
                          "chains": final_chains, "data": final_data,
                          "unlinked_oldest": [{"path": path, "absent_after_unlink": True}
                                               for path in paths]},
        "retained_data_before_unlink": final_data,
        "retained_chains_before_unlink": final_chains,
        "current_heads_before_unlink": {target: point_paths[labels[-1]][target]
                                         for target in targets},
        "reopen": {"uuid": domain_uuid, "state": "running",
                   "qmp": {**{name: {"ok": True} for name in
                              ("query-named-block-nodes", "query-block-jobs", "query-jobs")},
                           "query-block": qmp_block(32)},
                   "qmp_query_block_depth": {target: 32 for target in targets},
                   "apparmor": {"context": profile}},
    }
    return {
        "exit_code": 0, "uri": "qemu:///system", "domain_type": "qemu",
        "scope": CI.DEEP32_SCOPE,
        "apparmor": {"kernel_enabled": "Y"},
        "candidate": {"fixture": "selected-candidate"},
        "packages": [f"{package} {CI.SOURCE_VERSION}"
                     for package in CI.SYSTEM_CANDIDATE_PACKAGES],
        "case": case,
    }
class AppArmorEvidenceTests(unittest.TestCase):
    def assert_rejected(self, report):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "result.json"
            path.write_text(json.dumps(report))
            with self.assertRaises(RuntimeError):
                CI.check_apparmor_result(path)

    def test_deep32_public_consolidation_result_is_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "deep32-result.json"
            report = passed_deep32_report()
            path.write_text(json.dumps(report))
            CI.check_deep32_result(path, report["candidate"])

    def test_deep32_requires_exact_chain_depth_metadata_and_candidate(self):
        report = passed_deep32_report()
        report["case"]["initial"]["qmp_query_block_depth"]["vdb"] = 32
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["case"]["initial"].pop("qmp_query_block")
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["case"]["point_01_redefine"]["parent_graph"]["point-02"] = None
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["case"]["pre_unlink_reference_scan"]["paths"] = ["/images/unrelated.qcow2"]
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["case"]["pre_unlink_reference_scan"]["foreign_domains"] = [
            {"domain": "foreign", "state": "running",
             "definitions": {"default": [], "inactive": []}, "snapshots": {}, "sources": []},
        ]
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["case"]["pre_unlink_reference_scan"]["foreign_domains"] = [
            {"domain": "foreign", "state": "shut off",
             "definitions": {"inactive": []}, "snapshots": {}, "sources": []},
        ]
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["candidate"]["fixture"] = "different-candidate"
        self.assert_deep32_rejected(report, expected_candidate={"fixture": "selected-candidate"})

    def assert_deep32_rejected(self, report, expected_candidate=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "deep32-result.json"
            path.write_text(json.dumps(report))
            expected_candidate = expected_candidate or report["candidate"]
            with self.assertRaises(RuntimeError):
                CI.check_deep32_result(path, expected_candidate)

    def test_public_no_delete_consolidation_requires_enforcing_commits_and_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "result.json"
            path.write_text(json.dumps(passed_report()))
            CI.check_apparmor_result(path)

    def test_system_probe_scope_matches_the_validator_contract(self):
        report = passed_report()
        self.assertEqual(report["scope"], CI.system_probe_scope("midcommit"))
        report["scope"] = "paused BIOS external qcow2 interrupted public blockcommit; no integrated data-delete"
        self.assert_rejected(report)

    def test_integrated_delete_or_missing_references_is_rejected(self):
        report = passed_report()
        report["cases"][0]["commits"]["vda"]["argv"].append("--delete")
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["pre_unlink_reference_scan"]["passed"] = False
        self.assert_rejected(report)

    def test_missing_metadata_only_delete_reopen_or_data_is_rejected(self):
        report = passed_report()
        report["cases"][0]["metadata_delete"]["argv"].remove("--metadata")
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["reopen"].pop("qmp")
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["reopen"]["qmp"].pop("query-named-block-nodes")
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["final_stopped"]["unlinked_a"][0]["path"] = "/images/unrelated.qcow2"
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["final_data"]["vdb"]["point-c-post"]["ok"] = False
        self.assert_rejected(report)

    def test_deep32_validates_saved_snapshot_source_mapping_and_preunlink_reads(self):
        report = passed_deep32_report()
        report["case"]["final_stopped"]["snapshot_docs"]["xml"]["point-01"] = (
            report["case"]["final_stopped"]["snapshot_docs"]["xml"]["point-02"])
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["case"]["current_heads_before_unlink"]["vda"] = "/images/deleted-point.qcow2"
        self.assert_deep32_rejected(report)

        report = passed_deep32_report()
        report["case"].pop("retained_data_before_unlink")
        self.assert_deep32_rejected(report)

    def test_interrupted_commit_requires_nonterminal_job_same_qemu_and_new_daemon(self):
        report = passed_report()
        report["cases"][0]["midcommit_boundary"]["qmp_block_jobs"]["return"][0]["ready"] = True
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["midcommit_boundary"]["boundary"]["final_pre_kill"][
            "qmp_block_jobs"]["return"][0]["offset"] = 100
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["midcommit_boundary"]["daemon_after"]["pid"] = 10
        self.assert_rejected(report)

        report = passed_report()
        report["cases"][0]["midcommit_boundary"]["apparmor_after_restart"]["context"] = \
            "unconfined"
        self.assert_rejected(report)


class CandidatePackageTests(unittest.TestCase):
    def test_recipe_and_candidate_use_five_patch_cohort(self):
        root = Path(__file__).parents[1]
        recipe = (root / "packaging" / "libvirt" / "build-matched-libvirt.sh").read_text()
        self.assertEqual(CI.SOURCE_VERSION, "11.3.0-3+deb13u3+nex4")
        self.assertEqual(CI.PATCH_FILES, (
            "readonly-disk-apparmor.patch",
            "unique-revert-overlay.patch",
            "partial-backing-chain.patch",
            "qmp-json-depth.patch",
            "preserve-readonly-revert-media.patch",
        ))
        for patch in CI.PATCH_FILES:
            self.assertIn(patch, recipe)
        self.assertNotIn("serialized-snapshot-delete.patch", recipe)
        self.assertNotIn("snapshot-delete-recovery.patch", recipe)
        delivery_files = (
            root / "installer" / "build.sh",
            root / ".github" / "workflows" / "release.yml",
            root / ".gitea" / "workflows" / "release.yml",
        )
        for path in delivery_files:
            self.assertIn(CI.SOURCE_VERSION, path.read_text(), path)
        validation = '[ "$(dpkg-deb -f "$payload" Version)" = '
        version_checks = []
        for path in delivery_files[1:]:
            matching = [line.strip() for line in path.read_text().splitlines()
                        if validation in line and CI.SOURCE_VERSION in line]
            self.assertEqual(len(matching), 1, path)
            version_checks.append(matching[0])
        self.assertEqual(version_checks[0], version_checks[1])

    def test_runtime_result_requires_readonly_media_revert_regression(self):
        steps = [
            "revert-older-point", "revert-newer-then-repeat-older",
            "repeated-revert-after-new-point", "delete-point-c-after-revert",
            "delete-point-b", "delete-point-a", "start-after-delete",
        ]
        report = {
            "status": "passed", "uri": "qemu:///session", "domain_type": "qemu",
            "packages": [f"{package} {CI.SOURCE_VERSION}" for package in CI.CANDIDATE_PACKAGES] + [
                "qemu-system-x86 1:10.0.13+ds-0+deb13u1",
                "qemu-utils 1:10.0.13+ds-0+deb13u1",
            ],
            "cases": [{"status": "passed", "mode": "rapid", "disk_count": count,
                       "steps": [{"name": step} for step in steps],
                       "uuid_overlays": {"vda": "123e4567-e89b-12d3-a456-426614174000"},
                       "second_observation": {"uuid_overlays": {
                           "vda": "123e4567-e89b-12d3-a456-426614174001"}},
                       "third_observation": {"uuid_overlays": {
                           "vda": "123e4567-e89b-12d3-a456-426614174002"}},
                       "fourth_observation": {"uuid_overlays": {
                           "vda": "123e4567-e89b-12d3-a456-426614174003"}}}
                      for count in (1, 2)],
        }
        report["readonly_media_revert"] = {
            "status": "passed", "snapshot_media_policy": "no",
            "restored_original_source": True, "media_overlay_created": False,
            "writable_disk_head_valid": True,
        }
        with tempfile.TemporaryDirectory() as directory:
            result = Path(directory) / "result.json"
            result.write_text(json.dumps(report))
            CI.check_runtime_result(result)
            report["readonly_media_revert"]["media_overlay_created"] = True
            result.write_text(json.dumps(report))
            with self.assertRaises(RuntimeError):
                CI.check_runtime_result(result)

    def test_domain_xml_helpers_avoid_nested_elementtree_predicates(self):
        xml = """<domain type='qemu'>
  <devices>
    <disk type='file' device='disk'>
      <source file='/tmp/root.qcow2'/><target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <source file='/tmp/installer-a.iso'/><target dev='hdc' bus='ide'/><readonly/>
    </disk>
  </devices>
</domain>"""
        self.assertEqual(CI.disk_source_from_domain_xml(xml, "hdc"), "/tmp/installer-a.iso")
        self.assertIsNotNone(CI.disk_readonly_from_domain_xml(xml, "hdc"))
        self.assertIsNone(CI.disk_readonly_from_domain_xml(xml, "vda"))
        with self.assertRaises(Exception) as raised:
            from xml.etree import ElementTree
            ElementTree.fromstring(xml).find("./devices/disk[target[@dev='hdc']]/readonly")
        self.assertIn("predicate", str(raised.exception).lower())

    def test_system_package_closure_accepts_exact_amd64_and_all_debs(self):
        all_packages = {"libvirt-daemon-config-network", "libvirt-daemon-config-nwfilter"}
        with tempfile.TemporaryDirectory() as directory:
            candidate = Path(directory)
            expected = {}
            for package in CI.SYSTEM_CANDIDATE_PACKAGES:
                architecture = "all" if package in all_packages else "amd64"
                path = candidate / f"{package}_{CI.SOURCE_VERSION}_{architecture}.deb"
                path.touch()
                expected[path] = (package, CI.SOURCE_VERSION, architecture)

            def metadata(command, **kwargs):
                path = Path(command[2])
                package, version, architecture = expected[path]
                stdout = (f"Package: {package}\nVersion: {version}\n"
                          f"Architecture: {architecture}\n")
                return subprocess.CompletedProcess(command, 0, stdout, "")

            with mock.patch.object(CI.subprocess, "run", side_effect=metadata):
                selected = CI.check_packages(candidate, CI.SYSTEM_CANDIDATE_PACKAGES)

        self.assertEqual(set(selected), set(expected))
        self.assertEqual(
            {path.name.rsplit("_", 1)[-1] for path in selected},
            {"all.deb", "amd64.deb"},
        )

    def test_system_package_metadata_must_match_filename_architecture(self):
        package = CI.SYSTEM_CANDIDATE_PACKAGES[0]
        with tempfile.TemporaryDirectory() as directory:
            candidate = Path(directory)
            path = candidate / f"{package}_{CI.SOURCE_VERSION}_amd64.deb"
            path.touch()
            result = subprocess.CompletedProcess(
                [], 0,
                f"Package: {package}\nVersion: {CI.SOURCE_VERSION}\nArchitecture: all\n",
                "",
            )
            with mock.patch.object(CI.subprocess, "run", return_value=result):
                with self.assertRaises(RuntimeError):
                    CI.check_packages(candidate, [package])

    def test_candidate_provenance_binds_manifest_recipe_and_selected_debs(self):
        with tempfile.TemporaryDirectory() as directory:
            candidate = Path(directory)
            manifest_bytes = b"candidate checksums\n"
            (candidate / "SHA256SUMS").write_bytes(manifest_bytes)
            (candidate / "build-inputs.txt").write_text(
                "local_version=" + CI.SOURCE_VERSION + "\n" +
                "recipe_sha256=" + "a" * 64 + "\n")
            deb = candidate / f"libvirt0_{CI.SOURCE_VERSION}_amd64.deb"
            deb.write_bytes(b"selected candidate package")

            receipt = CI.candidate_provenance(candidate, [deb])

        self.assertEqual(receipt["source_version"], CI.SOURCE_VERSION)
        self.assertEqual(receipt["sha256sums_sha256"], hashlib.sha256(manifest_bytes).hexdigest())
        self.assertEqual(receipt["recipe_sha256"], "a" * 64)
        self.assertEqual(receipt["packages"], [{
            "filename": deb.name,
            "sha256": hashlib.sha256(b"selected candidate package").hexdigest(),
        }])

    def test_candidate_provenance_requires_checksum_manifest_and_build_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            candidate = Path(directory)
            deb = candidate / f"libvirt0_{CI.SOURCE_VERSION}_amd64.deb"
            deb.write_bytes(b"selected candidate package")
            with self.assertRaisesRegex(RuntimeError, "checksum manifest or build-input"):
                CI.candidate_provenance(candidate, [deb])
            (candidate / "SHA256SUMS").write_text("x\n")
            with self.assertRaisesRegex(RuntimeError, "checksum manifest or build-input"):
                CI.candidate_provenance(candidate, [deb])

    def test_system_guest_stages_sources_from_the_loaded_runner_directory(self):
        sources = CI.system_guest_source_files()
        self.assertEqual([path.name for path in sources], [
            "recovery_s0.py", "recovery_s0_consolidate.py", "libvirt_recovery_ci.py",
        ])
        self.assertTrue(all(path.is_file() for path in sources))
        self.assertTrue(all(path.parent == RUNNER.parent.resolve() for path in sources))

    def test_deep32_payload_compiles_and_refuses_running_foreign_domains(self):
        source = RUNNER.with_name("recovery_s0_consolidate.py")
        spec = importlib.util.spec_from_file_location("recovery_s0_consolidate", source)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        payload = CI.build_deep32_payload(
            module.native_helper_source(),
            {"image_root": "/tmp/root", "work": "/tmp/root/domain", "domain": "domain",
             "sentinel": "sentinel", "candidate": {}},
            "def verify_candidate():\n    return {}\n",
        )
        self.assertIn(CI.DEEP32_FOREIGN_GUARD, payload)
        namespace = {}
        exec(CI.DEEP32_FOREIGN_GUARD, namespace)
        self.assertEqual(namespace["require_stopped_foreign_domain"]("stopped", "shut off"),
                         "shut off")
        with self.assertRaises(RuntimeError):
            namespace["require_stopped_foreign_domain"]("running", "running")
        self.assertEqual(CI.expected_redefined_parent_graph(["point-00", "point-01", "point-02"]),
                         {"point-00": None, "point-01": None, "point-02": "point-01"})

        namespace = {}
        exec(inspect.getsource(CI.snapshot_source_paths), namespace)
        docs = {"point-01": (
            "<domainsnapshot><domain><devices>"
            "<disk device='disk'><source file='/images/base-vda.qcow2'/><target dev='vda'/></disk>"
            "<disk device='disk'><source file='/images/base-vdb.qcow2'/><target dev='vdb'/></disk>"
            "</devices></domain></domainsnapshot>")}
        self.assertEqual(namespace["snapshot_source_paths"](docs, ["point-01"], ["vda", "vdb"]), {
            "point-01": {"vda": "/images/base-vda.qcow2", "vdb": "/images/base-vdb.qcow2"},
        })
        reads = []
        saved_namespace = {"read_state": lambda path, marker, payload:
                           reads.append((path, marker, payload["pattern"])) or {"ok": True}}
        exec(inspect.getsource(CI.retained_saved_point_reads), saved_namespace)
        saved_paths = {
            "point-01": {target: f"/images/base-{target}.qcow2" for target in ("vda", "vdb")},
            "point-02": {target: f"/images/point-01-{target}.qcow2" for target in ("vda", "vdb")},
        }
        payloads = {target: {label: {"pattern": 0x60 + index}
                             for index, label in enumerate(("point-00", "point-01"))}
                    for target in ("vda", "vdb")}
        saved_namespace["retained_saved_point_reads"](
            ["point-00", "point-01", "point-02"], ["vda", "vdb"], saved_paths, payloads)
        self.assertEqual(reads, [
            ("/images/base-vda.qcow2", 0x20, 0x60),
            ("/images/point-01-vda.qcow2", 0x21, 0x61),
            ("/images/base-vdb.qcow2", 0x20, 0x60),
            ("/images/point-01-vdb.qcow2", 0x21, 0x61),
        ])
        output_namespace = {"json": json}
        exec(inspect.getsource(CI.emit_deep32_status), output_namespace)
        with mock.patch("builtins.print") as printed:
            output_namespace["emit_deep32_status"]({"status": "passed"})
        printed.assert_called_once_with('{"status": "passed"}', flush=True)

        data_namespace = {}
        exec(inspect.getsource(CI.deep32_data_by_target), data_namespace)
        data = data_namespace["deep32_data_by_target"](
            ["vda", "vdb"],
            {target: {"point-01": {"marker_ok": True}} for target in ("vda", "vdb")},
            {target: {"point-01": {"marker_ok": True}} for target in ("vda", "vdb")},
            {target: {"marker_ok": True} for target in ("vda", "vdb")},
        )
        self.assertEqual(set(data), {"vda", "vdb"})
        self.assertEqual(set(data["vda"]), {"saved", "post", "current"})

    def test_runtime_system_guest_results_bind_candidate_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory)
            candidate_dir = evidence / "candidate"
            candidate_dir.mkdir()
            candidate = {"manifest": "retained"}
            selected = [candidate_dir / "libvirt.deb"]
            with (mock.patch.object(CI, "apparmor_guest_run") as guest,
                  mock.patch.object(CI, "check_apparmor_result") as apparmor_check,
                  mock.patch.object(CI, "check_packages", return_value=selected),
                  mock.patch.object(CI, "candidate_provenance", return_value=candidate) as provenance,
                  mock.patch.object(CI, "check_deep32_result") as deep32_check,
                  mock.patch.object(CI, "check_branch_result") as branch_check):
                CI.validate_system_guest_results(evidence, candidate_dir)
                guest.assert_called_once_with(evidence, candidate_dir, system_mode="midcommit")
                apparmor_check.assert_called_once_with(evidence / "result.json")
                branch_check.assert_not_called()
                provenance.assert_not_called()
                deep32_check.assert_not_called()

                guest.reset_mock()
                apparmor_check.reset_mock()
                CI.validate_system_guest_results(evidence, candidate_dir, system_mode="all")
            guest.assert_called_once_with(evidence, candidate_dir, system_mode="all")
            apparmor_check.assert_called_once_with(evidence / "result.json")
            branch_check.assert_called_once_with(evidence / "branch-result.json")
            provenance.assert_called_once_with(candidate_dir, selected)
            deep32_check.assert_called_once_with(evidence / "deep32-result.json", candidate)


if __name__ == "__main__":
    unittest.main()
