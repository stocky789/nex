#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Storage repository browser workflow against the built Controller UI.

This default mode serves the real built UI and uses deterministic API fixtures.  It exercises
only presentation and request construction; it does not claim native NFS, ZFS, SMB or iSCSI
access.  The optional disposable-Core lab mode is added by the storage lab owner.
"""
import argparse
import json
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import threading

from playwright.sync_api import expect, sync_playwright


ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, default=ROOT / ".cache/storage-browser")
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(("127.0.0.1", 0), partial(SimpleHTTPRequestHandler, directory=str(ROOT / "web/dist")))
    threading.Thread(target=server.serve_forever, daemon=True).start()

    host = {"id": "core-qa", "pool_id": "pool-qa", "hostname": "core-qa", "connection": "fresh", "revoked": False,
            "capabilities": ["managed-vm-v1", "storage-repositories-v1", "storage-discovery-v1"], "inventory": {"cpu_count": 4, "memory_bytes": 8 * 1024 ** 3}}
    healthy = {"id": "core-assigned-repository", "pool_id": "pool-qa", "host_id": "core-qa", "name": "VM share", "kind": "smb",
               "source": {"smb": {"server": "files.example.test", "share": "vms"}}, "identity": "files.example.test/vms",
               "generation": 1, "status": "succeeded", "operation_id": "storage-register-core", "credential_present": True,
               "capabilities": {"vm_disk": True, "iso": True, "thin": True, "thick": False, "shared": False},
               "access": {"status": "healthy", "readable": True, "writable": True, "available_bytes": 32 * 1024 ** 3, "observed_at": "2026-09-11T00:00:00Z"}}
    unsuitable = {"id": "read-only-repository", "pool_id": "pool-qa", "host_id": "core-qa", "name": "Read-only NFS", "kind": "nfs",
                  "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/readonly"}}, "generation": 1,
                  "status": "succeeded", "operation_id": "storage-register-readonly", "capabilities": {"vm_disk": True, "iso": True},
                  "access": {"status": "degraded", "readable": True, "writable": False, "error": "export is read-only"}}
    data = {"session": {"username": "operator", "csrf_token": "fixture-csrf"}, "sites": [{"id": "site-qa", "name": "QA site"}],
            "pools": [{"id": "pool-qa", "site_id": "site-qa", "name": "QA pool", "master_host_id": "core-qa"}], "hosts": [host],
            "storage": [], "vms": [], "networks": [], "media": [], "operations": [], "audit": []}
    receipts = [{"receipt": "core-unused-receipt", "host_id": "core-qa", "status": "ready", "created_at": "2026-09-11T00:00:00Z", "retire_eligible": True}]
    submissions, errors, checks = [], [], []
    mode = {"fail_recheck": True, "unauthorized": False, "fail_retire": True, "fail_discovery": False,
            "browser_empty": False, "browser_error": False, "browser_failed": False, "browser_deferred": False}
    deferred_browser_replies = []

    def browser_candidates(kind, path):
        if kind == "nfs":
            if path == "":
                return [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports"}}, "path": "/exports", "browsable": True}]
            if path == "/exports/vms":
                return [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/vms"}}, "path": "/exports/vms", "browsable": True}]
            if path == "/exports":
                return [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports"}}, "path": "/exports/team", "browsable": True}]
            if path == "/exports/team":
                return [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports"}}, "path": "/exports/team/vms", "browsable": True}]
        if kind == "smb":
            if path == "":
                return [{"kind": "smb", "source": {"smb": {"server": "files.example.test", "share": "team-share"}}, "path": "/team-share", "browsable": True}]
            if path == "/team-share":
                return [{"kind": "smb", "source": {"smb": {"server": "files.example.test", "share": "team-share"}}, "path": "/team-share/vms", "browsable": True}]
        return []

    def api(route):
        request = route.request
        path = request.url.split("/api/v1/", 1)[1].split("?", 1)[0]
        if path == "session" and request.method == "GET":
            route.fulfill(json=data["session"])
            return
        if request.method == "GET":
            if path == "storage/credentials":
                route.fulfill(json=receipts)
                return
            route.fulfill(json=data.get(path, []))
            return
        body = request.post_data_json
        submissions.append((path, body))
        if path == "storage/discover":
            if mode["fail_discovery"]:
                route.fulfill(status=504, json={"error": "Core discovery became stale"})
            elif body["kind"] in ("nfs", "smb") and mode["browser_deferred"]:
                deferred_browser_replies.append((route, {"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "succeeded", "candidates": browser_candidates(body["kind"], body.get("path", ""))}))
            elif body["kind"] in ("nfs", "smb") and mode["browser_failed"]:
                route.fulfill(json={"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "failed", "error": "Core refused this folder listing", "candidates": []})
            elif body["kind"] in ("nfs", "smb") and (mode["browser_empty"] or mode["browser_error"]):
                if mode["browser_error"]:
                    route.fulfill(status=504, json={"error": "Core folder listing became stale"})
                else:
                    route.fulfill(json={"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "succeeded", "candidates": []})
            elif body["kind"] in ("nfs", "smb"):
                route.fulfill(json={"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "succeeded", "candidates": browser_candidates(body["kind"], body.get("path", ""))})
            elif body["kind"] == "iscsi":
                route.fulfill(json={"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "succeeded", "candidates": [{"kind": "iscsi", "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:vm-storage"}}, "path": "iqn.2026-01.example.test:vm-storage", "browsable": False}]})
            else:
                route.fulfill(json={"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "succeeded", "candidates": [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/vms"}}, "path": "/exports/vms", "browsable": True}]})
        elif path == "storage/credentials":
            # A credential endpoint returns only Core's opaque receipt.  The test also verifies
            # that no later durable request repeats the secret.
            route.fulfill(status=201, json={"receipt": "core-only-receipt"})
        elif path == "storage":
            data["storage"] = [{**healthy, "name": body["name"], "status": "queued", "operation_id": "storage-register-core"}]
            data["operations"] = [{"id": "storage-register-core", "host_id": "core-qa", "kind": "storage-register", "request_id": body["request_id"], "status": "queued", "created_at": "2026-09-11T00:00:00Z", "updated_at": "2026-09-11T00:00:00Z", "actor": "operator"}]
            route.fulfill(status=202, json=data["storage"][0])
        elif path.endswith("/recheck"):
            if mode["unauthorized"]:
                route.fulfill(status=401, json={"error": "Session expired"})
                return
            if mode["fail_recheck"]:
                route.fulfill(status=503, json={"error": "Simulated Core access check unavailable"})
            else:
                data["storage"][0] = {**data["storage"][0], "status": "queued", "operation_id": "storage-recheck-core"}
                route.fulfill(status=202, json=data["storage"][0])
        elif path.endswith("/detach"):
            data["storage"] = []
            route.fulfill(status=202, json={"operation_id": "storage-detach-core"})
        elif path.startswith("storage/credentials/") and path.endswith("/retire"):
            if mode["fail_retire"]:
                route.fulfill(status=409, json={"error": "Credential is still in use"})
            else:
                receipts.clear()
                route.fulfill(status=202, json={"operation_id": "credential-retire-core"})
        else:
            route.fulfill(status=204)

    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            page.set_default_timeout(2500)
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.route("**/api/v1/**", api)
            url = f"http://127.0.0.1:{server.server_port}/#/pools/pool-qa"
            page.goto(url)
            page.wait_for_load_state("networkidle")
            expect(page.get_by_role("heading", name="QA pool", exact=True)).to_be_visible()
            page.get_by_role("navigation", name="Resource views").get_by_role("button", name="Storage", exact=True).click()
            expect(page.get_by_text("No storage repositories reported.", exact=False)).to_be_visible()
            page.screenshot(path=str(args.evidence / "empty.png"), full_page=True)
            checks.append("Empty repository state renders in the built UI")

            host["connection"] = "stale"
            page.get_by_role("button", name="Refresh view", exact=True).click()
            page.get_by_role("button", name="Register storage", exact=True).click()
            stale_form = page.get_by_role("region", name="Register storage", exact=True)
            expect(stale_form.get_by_label("Host", exact=True).locator("option")).to_have_count(1)
            expect(stale_form.get_by_role("button", name="Register and check access", exact=True)).to_be_disabled()
            stale_form.get_by_role("button", name="Close", exact=True).click()
            host["connection"] = "fresh"
            page.get_by_role("button", name="Refresh view", exact=True).click()
            checks.append("A stale Core is excluded from registration placement")

            page.get_by_role("button", name="Register storage", exact=True).click()
            form = page.get_by_role("region", name="Register storage", exact=True)
            expect(form.get_by_label("Name", exact=True)).to_be_focused()
            # Required native fields block keyboard submission until the selected backend is complete.
            form.get_by_label("Repository type").select_option("nfs")
            form.get_by_label("Name", exact=True).fill("NFS fixtures")
            page.screenshot(path=str(args.evidence / "nfs-form.png"), full_page=True)
            form.get_by_label("Host", exact=True).select_option("core-qa")
            form.get_by_role("button", name="Register and check access", exact=True).focus()
            page.keyboard.press("Enter")
            assert not submissions, submissions
            form.get_by_label("NFS server").fill("nfs.example.test")
            # Folder browsing is a transient Core observation: it begins at root, even when a
            # manual draft already names a source, and only Select folder changes that draft.
            form.get_by_label("Export path").fill("/ignored/draft")
            form.get_by_label("Existing directory within export").fill("old/draft")
            mode["browser_deferred"] = True
            form.get_by_role("button", name="Browse exports", exact=True).click()
            nfs_browser = page.get_by_role("dialog", name="Browse exports", exact=True)
            expect(nfs_browser.get_by_role("status")).to_contain_text("Loading folders")
            expect(nfs_browser.get_by_role("button", name="Select folder", exact=True)).to_be_disabled()
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            expect(nfs_browser).to_be_hidden()
            delayed_route, delayed_reply = deferred_browser_replies.pop()
            mode["browser_deferred"] = False
            delayed_route.fulfill(json=delayed_reply)
            page.wait_for_timeout(100)
            expect(nfs_browser).to_be_hidden()
            expect(form.get_by_label("Export path")).to_have_value("/ignored/draft")
            expect(form.get_by_label("Existing directory within export")).to_have_value("old/draft")
            form.get_by_role("button", name="Browse exports", exact=True).click()
            nfs_browser = page.get_by_role("dialog", name="Browse exports", exact=True)
            expect(nfs_browser).to_be_visible()
            nfs_root = submissions[-1][1]
            assert nfs_root["kind"] == "nfs" and nfs_root["server"] == "nfs.example.test" and "path" not in nfs_root, nfs_root
            expect(nfs_browser.get_by_role("button", name="Up", exact=True)).to_be_disabled()
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            expect(form.get_by_label("Export path")).to_have_value("/ignored/draft")
            expect(form.get_by_label("Existing directory within export")).to_have_value("old/draft")
            form.get_by_role("button", name="Browse exports", exact=True).click()
            page.keyboard.press("Escape")
            expect(nfs_browser).to_be_hidden()
            expect(form.get_by_label("Export path")).to_have_value("/ignored/draft")
            form.get_by_role("button", name="Browse exports", exact=True).click()
            nfs_browser.get_by_role("button", name="Open /exports", exact=True).dblclick()
            expect(nfs_browser.get_by_role("button", name="Up", exact=True)).to_be_enabled()
            nfs_browser.get_by_role("button", name="Refresh", exact=True).click()
            assert submissions[-1][1]["path"] == "/exports", submissions[-1]
            nfs_browser.get_by_role("button", name="Open /exports/team", exact=True).dblclick()
            nfs_browser.get_by_role("button", name="Up", exact=True).click()
            assert submissions[-1][1]["path"] == "/exports", submissions[-1]
            nfs_browser.get_by_role("button", name="Open /exports/team", exact=True).dblclick()
            team_folder = nfs_browser.get_by_role("button", name="Open /exports/team/vms", exact=True)
            team_folder.focus()
            page.keyboard.press("Space")
            expect(team_folder).to_have_attribute("aria-pressed", "true")
            page.screenshot(path=str(args.evidence / "nfs-browser.png"), full_page=True)
            nfs_browser.get_by_role("button", name="Select folder", exact=True).click()
            expect(form.get_by_label("Export path")).to_have_value("/exports")
            expect(form.get_by_label("Existing directory within export")).to_have_value("team/vms")
            mode["browser_empty"] = True
            form.get_by_role("button", name="Browse exports", exact=True).click()
            expect(nfs_browser.get_by_text("No folders were found.", exact=False)).to_be_visible()
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            mode["browser_empty"] = False
            mode["browser_error"] = True
            form.get_by_role("button", name="Browse exports", exact=True).click()
            expect(nfs_browser.get_by_role("alert")).to_contain_text("Core folder listing became stale")
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            mode["browser_error"] = False
            mode["browser_failed"] = True
            form.get_by_role("button", name="Browse exports", exact=True).click()
            expect(nfs_browser.get_by_role("alert")).to_contain_text("Core refused this folder listing")
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            mode["browser_failed"] = False
            checks.append("NFS root browsing omits stale draft paths; loading, cancellation and late replies preserve form fields; keyboard navigation, refresh, empty and Core-error states stay actionable until a selected folder is applied")
            # All backend field sets exist and use native constraints before a request leaves the browser.
            for kind, labels in [("zfs", ("Imported pool", "Existing dataset", "Reported mount point")),
                                 ("smb", ("SMB server", "Share", "Account", "Password")),
                                 ("iscsi", ("Target portal", "Target IQN", "LUN", "LUN WWID", "Existing filesystem UUID"))]:
                form.get_by_label("Repository type").select_option(kind)
                for label in labels:
                    expect(form.get_by_label(label, exact=True)).to_be_visible()
                if kind == "iscsi":
                    expect(form.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).to_be_visible()
            form.get_by_label("Target portal", exact=True).fill("storage.example.test:3260")
            discover_targets = form.get_by_role("button", name="Discover targets", exact=True)
            expect(discover_targets).to_be_enabled()
            discover_targets.click()
            form.get_by_role("button", name="Use iqn.2026-01.example.test:vm-storage", exact=True).click()
            expect(form.get_by_label("Target IQN", exact=True)).to_have_value("iqn.2026-01.example.test:vm-storage")
            form.get_by_label("Repository type").select_option("smb")
            form.get_by_label("Name", exact=True).fill("VM share")
            form.get_by_label("SMB server").fill("files.example.test")
            form.get_by_label("Account", exact=True).fill("nex-vm")
            secret = "not-for-display-qa-secret"
            password = form.get_by_label("Password", exact=True)
            expect(password).to_have_attribute("type", "password")
            password.fill(secret)
            # SMB browsing needs only the Core, server and transient credentials; a manual
            # share is optional until selection and is never smuggled into the root request.
            expect(form.get_by_role("button", name="Browse shared folders", exact=True)).to_be_enabled()
            form.get_by_label("Share", exact=True).fill("old-share")
            form.get_by_label("Existing directory within share").fill("old/draft")
            form.get_by_role("button", name="Browse shared folders", exact=True).click()
            smb_browser = page.get_by_role("dialog", name="Browse shared folders", exact=True)
            expect(smb_browser).to_be_visible()
            smb_root = submissions[-1][1]
            assert smb_root["kind"] == "smb" and smb_root["credentials"] == {"username": "nex-vm", "secret": secret} and "path" not in smb_root, smb_root
            smb_browser.get_by_role("button", name="Open /team-share", exact=True).dblclick()
            share_folder = smb_browser.get_by_role("button", name="Open /team-share/vms", exact=True)
            share_folder.click()
            expect(share_folder).to_have_attribute("aria-pressed", "true")
            page.screenshot(path=str(args.evidence / "smb-browser.png"), full_page=True)
            smb_browser.get_by_role("button", name="Select folder", exact=True).click()
            expect(form.get_by_label("Share", exact=True)).to_have_value("team-share")
            expect(form.get_by_label("Existing directory within share")).to_have_value("vms")
            checks.append("SMB browsing works before a manual share exists and applies the selected share plus relative directory only after confirmation")
            page.get_by_role("button", name="Register and check access", exact=True).click()
            expect(page.get_by_text("Storage registration queued", exact=False)).to_be_visible()
            expect(page.get_by_text("core-assigned-repository", exact=True)).to_have_count(0)
            assert "core-assigned-repository" == data["storage"][0]["id"]
            credential = next(body for path, body in submissions if path == "storage/credentials")
            register = next(body for path, body in submissions if path == "storage")
            assert credential["credentials"] == {"username": "nex-vm", "secret": secret}, credential
            assert register["source"] == {"smb": {"server": "files.example.test", "share": "team-share", "directory": "vms"}}, register
            assert register["credential_receipt"] == "core-only-receipt" and secret not in json.dumps(register), register
            assert credential["registration_request_id"] == register["request_id"] == register["credential_request_id"], (credential, register)
            assert secret not in page.content() and secret not in page.evaluate("Object.values(sessionStorage).join(' ')")
            expect(page.get_by_text("VM share", exact=True)).to_be_visible()
            expect(page.get_by_text("queued", exact=True)).to_be_visible()
            pending_row = page.get_by_role("row").filter(has_text="VM share")
            for action in ("Recheck", "Update / rotate credentials", "Detach"):
                expect(pending_row.get_by_role("button", name=action, exact=True)).to_be_disabled()
            page.screenshot(path=str(args.evidence / "pending-core-id.png"), full_page=True)
            # Core's next report projects its allocated ID/generation before lifecycle controls open.
            data["storage"] = [healthy]
            page.get_by_role("button", name="Refresh view", exact=True).click()
            expect(page.get_by_role("row").filter(has_text="VM share").get_by_role("button", name="Recheck", exact=True)).to_be_enabled()
            checks.append("NFS, ZFS, SMB and iSCSI forms expose their typed fields; SMB secrets stay in the transient handoff; pending Core-assignment rows cannot run lifecycle actions")

            page.get_by_role("button", name="Recheck", exact=True).click()
            expect(page.get_by_role("alert")).to_contain_text("Simulated Core access check unavailable")
            page.screenshot(path=str(args.evidence / "error.png"), full_page=True)
            mode["fail_recheck"] = False
            page.get_by_role("button", name="Recheck", exact=True).click()
            expect(page.get_by_text("Access recheck queued", exact=False)).to_be_visible()
            expect(page.get_by_role("button", name="Detach", exact=True)).to_be_disabled()
            data["storage"] = [healthy]
            page.get_by_role("button", name="Refresh view", exact=True).click()
            expect(page.get_by_role("button", name="Detach", exact=True)).to_be_enabled()
            checks.append("A Core recheck error remains visible; retry queues a new Core operation and waits for Core confirmation before another lifecycle action")

            page.get_by_role("button", name="Detach", exact=True).click()
            dialog = page.get_by_role("dialog")
            expect(dialog.get_by_role("heading", name="Detach VM share?", exact=True)).to_be_visible()
            expect(dialog.get_by_label("Repository name", exact=True)).to_be_focused()
            dialog.get_by_label("Repository name", exact=True).fill("wrong")
            dialog.get_by_role("button", name="Detach storage", exact=True).click()
            assert not any(path.endswith("/detach") for path, _ in submissions)
            dialog.get_by_label("Repository name", exact=True).fill("VM share")
            dialog.get_by_role("button", name="Detach storage", exact=True).click()
            expect(page.get_by_text("No storage repositories reported.", exact=False)).to_be_visible()
            checks.append("Detach requires a focused typed-name confirmation and does not erase data")

            cleanup = page.get_by_role("region", name="Unused storage credentials", exact=True)
            cleanup.get_by_label("Host", exact=True).select_option("core-qa")
            cleanup.get_by_role("button", name="Find unused credentials", exact=True).click()
            receipt_row = cleanup.get_by_role("row").filter(has_text="core-unused-receipt")
            expect(receipt_row).to_be_visible()
            receipt_row.get_by_role("button", name="Retire unused credentials", exact=True).click()
            retire = page.get_by_role("dialog")
            expect(retire.get_by_role("heading", name="Retire unused credentials?", exact=True)).to_be_visible()
            retire.get_by_role("button", name="Retire unused credentials", exact=True).click()
            expect(page.get_by_role("alert")).to_contain_text("Credential is still in use")
            expect(retire).to_be_visible()
            mode["fail_retire"] = False
            retire.get_by_role("button", name="Retire unused credentials", exact=True).click()
            expect(page.get_by_text("Credential retirement queued", exact=False)).to_be_visible()
            expect(cleanup.get_by_text("core-unused-receipt", exact=True)).to_have_count(0)
            checks.append("Core-only unused credential receipts can be found and retired with a confirmation; an in-use refusal stays actionable")

            # VM placement only receives repositories Core currently reports writable and capable.
            synthetic = {**healthy, "id": "synthetic-core-id", "name": "Synthetic repository", "generation": 0}
            data["storage"] = [healthy, unsuitable, synthetic]
            page.get_by_role("button", name="Refresh view", exact=True).click()
            expect(page.get_by_text("Read-only NFS", exact=True)).to_be_visible()
            synthetic_row = page.get_by_role("row").filter(has_text="Synthetic repository")
            for action in ("Recheck", "Update / rotate credentials", "Detach"):
                expect(synthetic_row.get_by_role("button", name=action, exact=True)).to_be_disabled()
            page.get_by_role("navigation", name="Resource views").get_by_role("button", name="Virtual machines", exact=True).click()
            page.get_by_role("button", name="Create VM", exact=True).click()
            expect(page.get_by_role("heading", name="Create VM · 1 of 6:", exact=False)).to_be_visible()
            page.get_by_label("VM name", exact=True).fill("storage-picker")
            page.get_by_role("region", name="Create VM", exact=True).locator("select").nth(1).select_option("core-qa")
            page.get_by_role("button", name="Next", exact=True).click()
            page.get_by_role("button", name="Next", exact=True).click()
            disk = page.get_by_role("group", name="Disk 1", exact=True).get_by_label("Storage")
            expect(disk.locator("option")).to_contain_text(["VM share"])
            expect(disk.locator("option")).not_to_contain_text(["Read-only NFS"])
            page.set_viewport_size({"width": 390, "height": 844})
            assert page.evaluate("document.documentElement.scrollWidth <= innerWidth"), "storage UI narrow-page overflow"
            page.screenshot(path=str(args.evidence / "vm-picker-mobile.png"), full_page=True)
            checks.append("VM disk picker excludes read-only or unhealthy repositories; narrow layout has no overflow")

            page.set_viewport_size({"width": 1440, "height": 1000})
            page.get_by_role("navigation", name="Resource views").get_by_role("button", name="Storage", exact=True).click()
            mode["unauthorized"] = True
            page.get_by_role("row").filter(has_text="VM share").get_by_role("button", name="Recheck", exact=True).click()
            expect(page.get_by_label("Username", exact=True)).to_be_visible()
            page.screenshot(path=str(args.evidence / "unauthorized.png"), full_page=True)
            checks.append("An expired Controller session returns to the keyboard-accessible sign-in screen")
            assert not errors, errors
            browser.close()
        (args.evidence / "results.json").write_text(json.dumps({"mode": "real built Controller UI with deterministic mocked API; no Core or native storage execution", "checks": checks, "page_errors": errors}, indent=2) + "\n")
        print(json.dumps(checks, indent=2))
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
