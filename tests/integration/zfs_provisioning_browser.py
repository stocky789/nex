#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""ZFS provisioning wizard against built UI with simulated Core replies.

This checks browser request construction and safety controls only. It never touches a disk.
"""
import argparse
import json
import re
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import threading
import time

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, default=ROOT / ".cache/zfs-provisioning-browser")
    parser.add_argument("--dist", type=Path, default=ROOT / "web/dist", help="built UI to serve; use another build directory to check an unbuilt working tree")
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(("127.0.0.1", 0), partial(SimpleHTTPRequestHandler, directory=str(args.dist)))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    hosts = [
        {"id": "core-a", "pool_id": "pool-qa", "hostname": "core-a", "connection": "fresh", "revoked": False, "capabilities": ["managed-vm-v1", "storage-zfs-provision-v1", "storage-discovery-v1"], "inventory": {"os_version": "Nex Core 0.4.8", "cpu_count": 4, "memory_bytes": 8 * 1024 ** 3}},
        {"id": "core-b", "pool_id": "pool-qa", "hostname": "core-b", "connection": "fresh", "revoked": False, "capabilities": ["managed-vm-v1", "storage-zfs-provision-v1", "storage-discovery-v1"], "inventory": {"os_version": "Nex Core 0.4.8", "cpu_count": 4, "memory_bytes": 8 * 1024 ** 3}},
    ]
    hosts[0]["inventory"]["managed"] = {"zfs_provisions": [{"repository_id": "repo-existing", "pool_id": "pool-qa", "name": "Existing ZFS", "scope": "pool", "host_id": "core-a", "layout": "mirror", "status": "succeeded"}, {"repository_id": "repo-host", "pool_id": "pool-qa", "name": "Host ZFS", "scope": "host", "host_id": "core-a", "layout": "single", "status": "succeeded"}]}
    data = {"session": {"username": "operator", "csrf_token": "fixture-csrf"}, "sites": [{"id": "site-qa", "name": "QA site"}], "pools": [{"id": "pool-qa", "site_id": "site-qa", "name": "QA pool", "master_host_id": "core-a"}], "hosts": hosts, "storage": [], "vms": [], "networks": [], "media": [], "operations": [], "audit": []}
    submissions, errors, fixture_attempts, deferred = [], [], [], []
    discovery_mode = {"value": "success"}

    def api(route):
        request = route.request
        path = request.url.split("/api/v1/", 1)[1].split("?", 1)[0]
        if request.method == "GET":
            # Reads this fixture does not simulate (usage history, pool members) are unavailable.
            route.fulfill(json=data[path]) if path in data else route.fulfill(status=404, json={"error": "not simulated"})
            return
        body = request.post_data_json
        submissions.append((path, body))
        if path == "storage/discover":
            if discovery_mode["value"] == "loading":
                deferred.append((route, body))
                return
            if discovery_mode["value"] == "empty":
                route.fulfill(json={"status": "succeeded", "candidates": []})
                return
            if discovery_mode["value"] == "stale" or (discovery_mode["value"] == "mixed" and body["host_id"] == "core-a"):
                route.fulfill(json={"status": "failed", "error": "Core device observation is stale"})
                return
            candidates = [{"device": {"by_id": f"/dev/disk/by-id/{body['host_id']}-blank", "model": "Fixture SSD", "serial": f"{body['host_id']}-blank", "size_bytes": 64 * 1024 ** 3, "rotational": False, "eligibility": "blank", "fingerprint": "a" * 64}}, {"device": {"by_id": f"/dev/disk/by-id/{body['host_id']}-old", "model": "Fixture HDD", "serial": f"{body['host_id']}-old", "size_bytes": 64 * 1024 ** 3, "rotational": True, "eligibility": "erase_required", "fingerprint": "b" * 64}}, {"device": {"by_id": f"/dev/disk/by-id/{body['host_id']}-system", "model": "System", "serial": f"{body['host_id']}-system", "size_bytes": 64 * 1024 ** 3, "rotational": False, "eligibility": "system disk", "fingerprint": "c" * 64}}]
            route.fulfill(json={"status": "succeeded", "candidates": candidates})
        elif path == "storage/zfs/provision":
            if body["name"] == "Host only":
                deferred.append((route, body))
                return
            if body["name"] == "Fixture ZFS":
                fixture_attempts.append(body)
                if len(fixture_attempts) == 1:
                    route.abort("failed")
                    return
            route.fulfill(status=202, json={"name": body["name"], "pool_id": "pool-qa", "scope": body["scope"], "hosts": body["hosts"], "operation": {"id": "zfs-fixture"}})
        else:
            route.fulfill(status=204)

    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            expect.set_options(timeout=10000)
            page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.route("**/api/v1/**", api)
            page.goto(f"http://127.0.0.1:{server.server_port}/#/pools/pool-qa", wait_until="commit")
            page.get_by_role("navigation", name="Sections", exact=True).get_by_role("link", name="Storage", exact=True).click()
            outcomes = page.locator("section", has=page.get_by_role("heading", name="ZFS storage by host", exact=True))
            expect(outcomes.get_by_text("Existing ZFS", exact=True)).to_be_visible()
            host_scope = outcomes.get_by_role("heading", name="Host ZFS", exact=True).locator("..")
            expect(host_scope.get_by_text("core-a", exact=True)).to_be_visible()
            expect(host_scope.get_by_text("core-b", exact=True)).to_have_count(0)
            expect(host_scope.get_by_role("button", name="Add this host", exact=True)).to_have_count(0)
            outcomes.get_by_role("button", name="Add this host", exact=True).click()
            # The clicked row is the explicit target of a disk-erasing flow: it is carried into the
            # wizard, and a host Core already configured for this repository is not offered again.
            configure = page.get_by_role("dialog", name="Add core-b to Existing ZFS", exact=True)
            expect(configure.get_by_label("Storage name", exact=True)).to_have_value("Existing ZFS")
            expect(configure.get_by_label("Storage name", exact=True)).not_to_be_editable()
            expect(configure.get_by_role("checkbox", name="core-a", exact=True)).to_have_count(0)
            expect(configure.get_by_label("Use /dev/disk/by-id/core-a-blank", exact=True)).to_have_count(0)
            configure.get_by_label("Use /dev/disk/by-id/core-b-blank", exact=True).check()
            configure.get_by_role("button", name="Review and erase →", exact=True).click()
            configure.get_by_role("button", name="Erase disks and create storage", exact=True).click()
            deadline = time.monotonic() + 30
            while not any(path == "storage/zfs/provision" for path, _ in submissions) and time.monotonic() < deadline:
                page.wait_for_timeout(50)
            configured = next(body for path, body in submissions if path == "storage/zfs/provision")
            assert configured["repository_id"] == "repo-existing" and configured["scope"] == "pool" and [host["host_id"] for host in configured["hosts"]] == ["core-b"]
            add = page.get_by_role("button", name="Add ZFS storage", exact=True)
            expect(add).to_be_enabled()
            add.focus()
            add.press("Enter")
            wizard = page.get_by_role("dialog", name="Add ZFS storage", exact=True)
            # Focus moves into the dialog heading, and follows each step.
            heading = wizard.get_by_role("heading", name="Add ZFS storage", exact=True)
            expect(heading).to_be_focused()
            # Disks Core reports as unavailable are never offered as a choice, only listed with its reason.
            expect(wizard.get_by_label("Use /dev/disk/by-id/core-a-blank", exact=True)).to_be_visible()
            expect(wizard.get_by_label("Use /dev/disk/by-id/core-a-system", exact=True)).to_have_count(0)
            wizard.get_by_text("1 unavailable disk", exact=True).first.click()
            expect(wizard.get_by_text("system disk", exact=False).first).to_be_visible()
            wizard.get_by_label("Use /dev/disk/by-id/core-a-old", exact=True).check()
            wizard.get_by_label("Use /dev/disk/by-id/core-b-blank", exact=True).check()
            expect(wizard.get_by_text(re.compile(r"^1 data disk → Single disk,"))).to_have_count(2)
            wizard.get_by_label("Storage name", exact=True).fill("Fixture ZFS")
            page.screenshot(path=str(args.evidence / "disk-selection.png"))
            wizard.get_by_role("button", name="Review and erase →", exact=True).click()
            expect(heading).to_be_focused()
            erase = wizard.get_by_label("Type this by-id device name to erase its old data", exact=True)
            submit = wizard.get_by_role("button", name="Erase disks and create storage", exact=True)
            expect(submit).to_be_disabled()
            erase.fill("/dev/disk/by-id/not-the-device")
            expect(submit).to_be_disabled()
            erase.fill("/dev/disk/by-id/core-a-old")
            page.screenshot(path=str(args.evidence / "confirmation.png"))
            wizard.screenshot(path=str(args.evidence / "confirmation-top.png"))
            submit.click()
            expect(wizard.get_by_role("alert")).to_be_visible()
            wizard.get_by_role("button", name="Close", exact=True).click()
            page.get_by_role("button", name="Add ZFS storage", exact=True).click()
            # A resumed wizard never lands on a pre-armed erase step: discovery is asked again, no
            # disk is ticked, and each erase confirmation must be typed again.
            expect(wizard.get_by_label("Storage name", exact=True)).to_have_value("Fixture ZFS")
            expect(submit).to_have_count(0)
            expect(wizard.get_by_label("Use /dev/disk/by-id/core-a-old", exact=True)).not_to_be_checked()
            expect(wizard.get_by_role("button", name="Review and erase →", exact=True)).to_be_disabled()
            wizard.get_by_label("Use /dev/disk/by-id/core-a-old", exact=True).check()
            wizard.get_by_label("Use /dev/disk/by-id/core-b-blank", exact=True).check()
            wizard.get_by_role("button", name="Review and erase →", exact=True).click()
            expect(erase).to_have_value("")
            expect(submit).to_be_disabled()
            erase.fill("/dev/disk/by-id/core-a-old")
            submit.click()
            expect(wizard).to_have_count(0)
            provision = next(body for path, body in reversed(submissions) if path == "storage/zfs/provision" and body["name"] == "Fixture ZFS")
            assert provision["scope"] == "pool" and {host["host_id"] for host in provision["hosts"]} == {"core-a", "core-b"}
            assert all("layout" not in host for host in provision["hosts"]), "the automatic layout stays Core's decision"
            old = next(device for host in provision["hosts"] for device in host["devices"] if device["by_id"].endswith("core-a-old"))
            assert old["erase_confirmation"] == old["by_id"]
            assert old["fingerprint"] == "b" * 64
            assert len(fixture_attempts) == 2 and fixture_attempts[0]["request_id"] == fixture_attempts[1]["request_id"]
            assert all(not device.get("erase_confirmation") for host in provision["hosts"] for device in host["devices"] if device["by_id"].endswith("blank"))
            # Unticking a host with the keyboard leaves one host: that is a host-only repository.
            page.get_by_role("button", name="Add ZFS storage", exact=True).click()
            core_b = wizard.get_by_role("checkbox", name="core-b", exact=True)
            expect(wizard.get_by_label("Use /dev/disk/by-id/core-b-blank", exact=True)).to_be_visible()
            wizard.get_by_label("Use /dev/disk/by-id/core-b-blank", exact=True).check()
            core_b.focus()
            page.keyboard.press("Space")
            expect(core_b).not_to_be_checked()
            expect(wizard.get_by_label("Use /dev/disk/by-id/core-b-blank", exact=True)).to_have_count(0)
            expect(wizard.get_by_role("button", name="Review and erase →", exact=True)).to_be_disabled()
            wizard.get_by_label("Use /dev/disk/by-id/core-a-blank", exact=True).check()
            wizard.get_by_label("Storage name", exact=True).fill("Host only")
            page.screenshot(path=str(args.evidence / "one-host.png"))
            wizard.get_by_role("button", name="Review and erase →", exact=True).click()
            expect(wizard.get_by_text("Only core-a", exact=True)).to_be_visible()
            deferred.clear()
            submit.click()
            # A native second Escape cannot be cancelled: the dialog must not stay closed mid-submit.
            expect(wizard.get_by_role("button", name="Submitting…", exact=True)).to_be_disabled()
            page.keyboard.press("Escape")
            page.keyboard.press("Escape")
            expect(wizard.get_by_role("button", name="Submitting…", exact=True)).to_be_visible()
            deadline = time.monotonic() + 10
            while not deferred and time.monotonic() < deadline:
                page.wait_for_timeout(50)
            route, body = deferred.pop()
            route.fulfill(status=202, json={"name": body["name"], "pool_id": "pool-qa", "scope": body["scope"], "hosts": body["hosts"], "operation": {"id": "zfs-fixture"}})
            expect(wizard).to_have_count(0)
            host_only = next(body for path, body in reversed(submissions) if path == "storage/zfs/provision")
            assert host_only["scope"] == "host" and [host["host_id"] for host in host_only["hosts"]] == ["core-a"]
            discovery_mode["value"] = "loading"
            deferred.clear()
            page.get_by_role("button", name="Add ZFS storage", exact=True).click()
            expect(wizard.get_by_role("button", name="Loading disks…", exact=True)).to_be_disabled()
            deadline = time.monotonic() + 10
            while len(deferred) < 2 and time.monotonic() < deadline:
                page.wait_for_timeout(50)
            assert len(deferred) == 2
            for route, body in deferred:
                route.fulfill(json={"status": "succeeded", "candidates": [{"device": {"by_id": f"/dev/disk/by-id/{body['host_id']}-blank", "model": "Fixture SSD", "serial": f"{body['host_id']}-blank", "size_bytes": 64 * 1024 ** 3, "rotational": False, "eligibility": "blank", "fingerprint": "a" * 64}}]})
            expect(wizard.get_by_label("Use /dev/disk/by-id/core-a-blank", exact=True)).to_be_visible()
            page.keyboard.press("Escape")
            expect(wizard).to_have_count(0)
            page.evaluate("sessionStorage.clear()")
            discovery_mode["value"] = "empty"
            page.get_by_role("button", name="Add ZFS storage", exact=True).click()
            expect(wizard.get_by_text("No disk on this host can be used for new storage.", exact=False)).to_have_count(2)
            wizard.get_by_role("button", name="Close", exact=True).click()
            page.evaluate("sessionStorage.clear()")
            # One host that cannot report devices leaves the other host selectable.
            discovery_mode["value"] = "mixed"
            page.get_by_role("button", name="Add ZFS storage", exact=True).click()
            expect(wizard.get_by_label("Use /dev/disk/by-id/core-b-blank", exact=True)).to_be_visible()
            expect(wizard.get_by_role("alert")).to_have_count(1)
            page.screenshot(path=str(args.evidence / "partial-discovery.png"))
            wizard.get_by_role("button", name="Close", exact=True).click()
            page.evaluate("sessionStorage.clear()")
            discovery_mode["value"] = "stale"
            page.get_by_role("button", name="Add ZFS storage", exact=True).click()
            expect(wizard.get_by_role("alert")).to_have_count(2)
            expect(wizard.get_by_role("alert").first).to_have_text("Core device observation is stale · This host is left as it is.")
            page.screenshot(path=str(args.evidence / "stale-discovery.png"))
            wizard.get_by_role("button", name="Cancel", exact=True).click()
            expect(wizard).to_have_count(0)
            assert not errors, errors
            (args.evidence / "results.json").write_text(json.dumps({"result": "passed", "mode": "simulated Core replies", "checks": ["Core eligibility is rendered without client inference", "unavailable disks are listed with Core's reason and cannot be ticked", "typed by-id confirmation gates erasure", "add-this-host carries the clicked host and omits already-configured hosts", "add-this-host submits Core repository identity and target host", "host-scoped outcomes list only the selected host", "a resumed wizard re-runs discovery, ticks nothing and re-arms each erase confirmation", "lost reply close/reopen replays the same request identity", "the automatic layout is left to Core", "keyboard opens a fresh wizard and unticks a host; one ticked host submits host scope", "dialog focus follows each step; Escape and Cancel close it, but repeated Escape cannot close it mid-submit", "loading, empty, partial and stale device discovery states are rendered"]}, indent=2) + "\n")
            browser.close()
    finally:
        server.shutdown()


if __name__ == "__main__":
    main()
