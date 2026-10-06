#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Storage repository browser workflow against the built Controller UI.

This default mode serves the real built UI and uses deterministic API fixtures.  It exercises
only presentation and request construction; it does not claim native NFS, ZFS, SMB or iSCSI
access.  The optional disposable-Core lab mode is added by the storage lab owner.
"""
import argparse
import json
import re
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import threading

from playwright.sync_api import expect, sync_playwright

from storage_browser_support import take_deferred


ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, default=ROOT / ".cache/storage-browser")
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(("127.0.0.1", 0), partial(SimpleHTTPRequestHandler, directory=str(ROOT / "web/dist")))
    threading.Thread(target=server.serve_forever, daemon=True).start()

    host = {"id": "core-qa", "pool_id": "pool-qa", "hostname": "core-qa", "connection": "fresh", "revoked": False,
            "capabilities": ["managed-vm-v1", "storage-repositories-v1", "storage-discovery-v1", "storage-discovery-purpose-v1", "storage-iscsi-inspect-v1"], "inventory": {"os_version": "Nex Core 0.4.8", "cpu_count": 4, "memory_bytes": 8 * 1024 ** 3}}
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
    submissions, errors, checks = [], [], []
    mode = {"fail_recheck": True, "unauthorized": False, "fail_discovery": False,
            "browser_empty": False, "browser_error": False, "browser_failed": False, "browser_deferred": False}
    deferred_browser_replies = []
    deferred_inspection = []
    mode["inspection"] = "single"
    inspected_lun = {"lun": 0, "wwid": "3600-inspected-qa", "filesystem_uuid": "00000000-0000-0000-0000-000000000123", "filesystem_type": "ext4", "size_bytes": 1024 ** 3}

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
                return [{"kind": "smb", "source": {"smb": {"server": "files.example.test", "share": "team-share"}}, "path": "/team-share/vms", "browsable": True, "existing": True}]
        return []

    def api(route):
        request = route.request
        path = request.url.split("/api/v1/", 1)[1].split("?", 1)[0]
        if path == "session" and request.method == "GET":
            route.fulfill(json=data["session"])
            return
        if request.method == "GET":
            # Reads this fixture does not simulate (usage history, pool members) are unavailable.
            route.fulfill(json=data[path]) if path in data else route.fulfill(status=404, json={"error": "not simulated"})
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
            elif body["kind"] == "iscsi" and body.get("target_iqn"):
                reply = {"request_id": body["request_id"], "pool_id": body["pool_id"], "host_id": body["host_id"], "status": "succeeded", "iscsi_luns": [inspected_lun]}
                if mode["inspection"] == "multiple":
                    reply["iscsi_luns"] = [inspected_lun, {**inspected_lun, "lun": 7, "wwid": "3600-second-qa", "filesystem_uuid": "00000000-0000-0000-0000-000000000456", "filesystem_type": "xfs"}]
                elif mode["inspection"] == "empty": reply.pop("iscsi_luns")
                elif mode["inspection"] == "blank": reply["iscsi_luns"] = [{"lun": 0, "wwid": "3600-blank-qa", "error": "No filesystem signature detected."}]
                elif mode["inspection"] == "unsupported": reply["iscsi_luns"] = [{**inspected_lun, "filesystem_type": "ntfs", "error": "Filesystem ntfs is unsupported."}]
                elif mode["inspection"] == "failed": reply.update(status="failed", iscsi_luns=[], error="Core could not inspect this selected target.")
                if mode["inspection"] == "deferred": deferred_inspection.append((route, reply))
                else: route.fulfill(json=reply)
            elif body["kind"] == "iscsi":
                route.fulfill(json={"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "succeeded", "candidates": [{"kind": "iscsi", "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:vm-storage", "lun": 0}}, "browsable": False}]})
            else:
                route.fulfill(json={"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-qa", "status": "succeeded", "candidates": [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/vms"}}, "path": "/exports/vms", "browsable": True}]})
        elif path == "storage/credentials":
            # A credential endpoint returns only Core's opaque receipt.  The test also verifies
            # that no later durable request repeats the secret.
            if mode.get("fail_credentials"): route.fulfill(status=503, json={"error": "Simulated credential handoff failure"})
            else: route.fulfill(status=201, json={"receipt": "core-only-receipt"})
        elif path == "storage":
            if mode.get("fail_registration"):
                route.fulfill(status=503, json={"error": "Simulated registration failure"})
                return
            data["storage"] = [{**healthy, "name": body["name"], "status": "queued", "operation_id": "storage-register-core"}]
            data["operations"] = [{"id": "storage-register-core", "host_id": "core-qa", "kind": "storage-register", "request_id": body["request_id"], "status": "queued", "created_at": "2026-09-11T00:00:00Z", "updated_at": "2026-09-11T00:00:00Z", "actor": "operator"}]
            route.fulfill(status=202, json=data["storage"][0])
        elif path.endswith("/update"):
            data["storage"] = [{**data["storage"][0], **body["registration"], "generation": data["storage"][0]["generation"] + 1}]
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
        else:
            route.fulfill(status=204)

    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            # Functional checks share the 30s action budget on busy CI runners;
            # this suite does not impose a five-second UI performance requirement.
            expect.set_options(timeout=30000)
            # The UI probes the session only for a browser that signed in before (BUG-0208).
            page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.route("**/api/v1/**", api)
            url = f"http://127.0.0.1:{server.server_port}/#/pools/pool-qa"
            page.goto(url)
            page.wait_for_load_state("networkidle")
            # The pool link chooses that pool as the scope; the rail then opens its Storage screen.
            expect(page.get_by_label(re.compile(r"^Scope: .*, QA pool\. Change scope$"))).to_be_visible()
            rail = page.get_by_role("navigation", name="Sections", exact=True)

            def refresh():
                # The console has no manual refresh; wait for its next automatic inventory poll.
                with page.expect_response(lambda r: r.url.endswith("/api/v1/storage") and r.request.method == "GET", timeout=60000):
                    pass
            rail.get_by_role("link", name="Storage", exact=True).click()
            expect(page.get_by_text("No storage repositories reported.", exact=False)).to_be_visible()
            page.screenshot(path=str(args.evidence / "empty.png"), full_page=True)
            checks.append("Empty repository state renders in the built UI")

            host["connection"] = "stale"
            refresh()
            page.get_by_role("button", name="Register storage", exact=True).click()
            stale_form = page.get_by_role("region", name="Register storage", exact=True)
            expect(stale_form.get_by_label("Host", exact=True).locator("option")).to_have_count(1)
            expect(stale_form.get_by_role("button", name="Register and check access", exact=True)).to_be_disabled()
            stale_form.get_by_role("button", name="Close", exact=True).click()
            host["connection"] = "fresh"
            refresh()
            checks.append("A stale Core is excluded from registration placement")

            page.get_by_role("button", name="Register storage", exact=True).click()
            form = page.get_by_role("region", name="Register storage", exact=True)
            expect(form.get_by_label("Name", exact=True)).to_be_focused()
            # Required native fields block keyboard submission until the selected backend is complete.
            form.get_by_label("Repository type").select_option("nfs")
            form.get_by_label("Name", exact=True).fill("NFS fixtures")
            page.screenshot(path=str(args.evidence / "nfs-form.png"), full_page=True)
            # Shared storage is recorded on the pool master; the form offers no host choice.
            expect(form.get_by_label("Host", exact=True)).to_have_count(0)
            expect(form.get_by_text("Pool master:", exact=False)).to_be_visible()
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
            delayed_route, delayed_reply = take_deferred(page, deferred_browser_replies, "NFS export discovery")
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            expect(nfs_browser).to_be_hidden()
            mode["browser_deferred"] = False
            delayed_route.fulfill(json=delayed_reply)
            page.wait_for_timeout(100)
            expect(nfs_browser).to_be_hidden()
            expect(form.get_by_label("Export path")).to_have_value("/ignored/draft")
            expect(form.get_by_label("Existing directory within export")).to_have_value("old/draft")
            with page.expect_request("**/api/v1/storage/discover") as root_request:
                form.get_by_role("button", name="Browse exports", exact=True).click()
            nfs_browser = page.get_by_role("dialog", name="Browse exports", exact=True)
            expect(nfs_browser).to_be_visible()
            nfs_root = root_request.value.post_data_json
            assert nfs_root["kind"] == "nfs" and nfs_root["server"] == "nfs.example.test" and "path" not in nfs_root, nfs_root
            expect(nfs_browser.get_by_role("button", name="Up", exact=True)).to_be_disabled()
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            expect(form.get_by_label("Export path")).to_have_value("/ignored/draft")
            expect(form.get_by_label("Existing directory within export")).to_have_value("old/draft")
            # Deliver the pending discovery response during Escape, before the native
            # dialog close event (whose delivery is delayed by 100ms here). A reply
            # must not reopen a cancelled picker. Drain the previous close first.
            page.evaluate("() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))")
            page.evaluate("""() => {
                const fetch = window.fetch;
                const dialog = document.querySelector('dialog.storage-browser');
                document.addEventListener('close', event => {
                    event.stopImmediatePropagation();
                    setTimeout(() => {
                        dialog.dispatchEvent(new Event('close'));
                        window.discoveryCloseDelivered = true;
                    }, 100);
                }, {capture: true, once: true});
                window.discoveryReadyForEscape = false;
                window.fetch = async (...args) => {
                    if (args[0] !== '/api/v1/storage/discover') return fetch(...args);
                    window.fetch = fetch;
                    const response = await fetch(...args);
                    return new Promise(resolve => {
                        document.querySelector('dialog.storage-browser').addEventListener(
                            'cancel', () => resolve(response), {once: true});
                        window.discoveryReadyForEscape = true;
                    });
                };
            }""")
            form.get_by_role("button", name="Browse exports", exact=True).click()
            page.wait_for_function("window.discoveryReadyForEscape")
            page.keyboard.press("Escape")
            page.wait_for_function("window.discoveryCloseDelivered")
            expect(page.locator("dialog.storage-browser")).not_to_have_attribute("open", "")
            expect(nfs_browser).to_be_hidden()
            expect(form.get_by_label("Export path")).to_have_value("/ignored/draft")
            form.get_by_role("button", name="Browse exports", exact=True).click()
            expect(nfs_browser.get_by_role("button", name="Select /exports", exact=True)).to_be_visible()
            # A close notification queued for the previous opening must not clear a
            # newly opened picker; browsers deliver close events asynchronously.
            page.evaluate("""() => {
                document.addEventListener('close', event => {
                    event.stopImmediatePropagation();
                    window.deliverOldBrowserClose = () => event.target.dispatchEvent(new Event('close'));
                }, {capture: true, once: true});
            }""")
            nfs_browser.get_by_role("button", name="Cancel", exact=True).click()
            page.wait_for_function("window.deliverOldBrowserClose")
            # A busy runner can deliver a valid listing after the default 5s
            # assertion deadline. Keep that scheduling gap in the regression.
            page.evaluate("""() => {
                const originalFetch = window.fetch;
                window.fetch = async (...args) => {
                    if (args[0] !== '/api/v1/storage/discover') return originalFetch(...args);
                    window.fetch = originalFetch;
                    const response = await originalFetch(...args);
                    await new Promise(resolve => setTimeout(resolve, 5500));
                    return response;
                };
            }""")
            form.get_by_role("button", name="Browse exports", exact=True).click()
            expect(nfs_browser.get_by_role("button", name="Select /exports", exact=True)).to_be_visible()
            page.evaluate("window.deliverOldBrowserClose()")
            expect(nfs_browser.get_by_role("button", name="Select /exports", exact=True)).to_be_visible()
            page.screenshot(path=str(args.evidence / "nfs-browser-reopened.png"), full_page=True)
            checks.append("Escape cancels discovery before its reply; delayed close events cannot empty a reopened modal")
            nfs_browser.get_by_role("button", name="Select /exports", exact=True).dblclick()
            expect(nfs_browser.get_by_role("button", name="Up", exact=True)).to_be_enabled()
            with page.expect_request("**/api/v1/storage/discover") as refresh_request:
                nfs_browser.get_by_role("button", name="Refresh", exact=True).click()
            assert refresh_request.value.post_data_json["path"] == "/exports", refresh_request.value.post_data_json
            nfs_browser.get_by_role("button", name="Select /exports/team", exact=True).dblclick()
            with page.expect_request("**/api/v1/storage/discover") as up_request:
                nfs_browser.get_by_role("button", name="Up", exact=True).click()
            assert up_request.value.post_data_json["path"] == "/exports", up_request.value.post_data_json
            nfs_browser.get_by_role("button", name="Select /exports/team", exact=True).dblclick()
            team_folder = nfs_browser.get_by_role("button", name="Select /exports/team/vms", exact=True)
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
                                 ("smb", ("SMB server", "Share", "Account", "Domain", "Password")),
                                 ("iscsi", ("Target portal", "Target IQN", "LUN", "LUN WWID", "Existing filesystem UUID"))]:
                form.get_by_label("Repository type").select_option(kind)
                for label in labels:
                    expect(form.get_by_label(label, exact=True)).to_be_visible()
                if kind == "iscsi":
                    expect(form.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).to_be_visible()
            iscsi_label = "This LUN is restricted to this Core initiator only"
            inspect_button = form.get_by_role("button", name="Inspect LUNs", exact=True)

            def last_iscsi_source():
                return next(body for path, body in reversed(submissions) if path == "storage" and body.get("kind") == "iscsi")["source"]["iscsi"]

            def submit_failed_registration():
                # The simulated failure keeps the draft while still recording the exact request body.
                mode["fail_registration"] = True
                with page.expect_response(lambda r: r.url.endswith("/api/v1/storage") and r.request.method == "POST"):
                    form.get_by_role("button", name="Register and check access", exact=True).click()
                expect(page.get_by_role("alert")).to_contain_text("Simulated registration failure")
                mode["fail_registration"] = False

            def start_inspection(iqn):
                form.get_by_label("Target IQN", exact=True).fill(iqn)
                inspect_button.click()
            form.get_by_label("Target portal", exact=True).fill("storage.example.test:3260")
            discover_targets = form.get_by_role("button", name="Discover targets", exact=True)
            expect(discover_targets).to_be_enabled()
            discover_targets.click()
            form.get_by_role("button", name="Use iqn.2026-01.example.test:vm-storage", exact=True).click()
            expect(form.get_by_label("Target IQN", exact=True)).to_have_value("iqn.2026-01.example.test:vm-storage")
            expect(form.get_by_label("LUN", exact=True)).to_have_value("0")
            expect(form.get_by_label("LUN WWID", exact=True)).to_have_value(inspected_lun["wwid"])
            expect(form.get_by_label("Existing filesystem UUID", exact=True)).to_have_value(inspected_lun["filesystem_uuid"])
            expect(form.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).not_to_be_checked()
            page.screenshot(path=str(args.evidence / "iscsi-single-lun.png"), full_page=True)
            # Registration carries exactly the detected identity; exclusivity is an explicit operator act.
            form.get_by_label("Name", exact=True).fill("Autofilled iSCSI")
            # The exclusivity acknowledgement is a required field: the browser refuses to submit without it.
            assert form.get_by_label(iscsi_label, exact=True).evaluate("e => !e.checkValidity()"), "an unacknowledged LUN must not be submittable"
            form.get_by_label(iscsi_label, exact=True).check()
            submit_failed_registration()
            assert last_iscsi_source() == {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:vm-storage", "lun": 0, "wwid": inspected_lun["wwid"], "filesystem_uuid": inspected_lun["filesystem_uuid"], "exclusive_target": True, "mode": "exclusive"}, last_iscsi_source()
            form.get_by_label(iscsi_label, exact=True).uncheck()
            # A failed submission must leave a cancelled/manual source draft intact.
            form.get_by_label("Name", exact=True).fill("Manual iSCSI")
            form.get_by_label("LUN", exact=True).fill("19")
            form.get_by_label("LUN WWID", exact=True).fill("manual-registration-wwid")
            manual_uuid = "00000000-0000-0000-0000-000000000019"
            form.get_by_label("Existing filesystem UUID", exact=True).fill(manual_uuid)
            form.get_by_label("This LUN is restricted to this Core initiator only", exact=True).check()
            mode["fail_registration"] = True
            before = len(submissions)
            with page.expect_response(lambda r: r.url.endswith("/api/v1/storage") and r.request.method == "POST"):
                form.get_by_role("button", name="Register and check access", exact=True).click()
            expect(page.get_by_role("alert")).to_contain_text("Simulated registration failure")
            expect(form.get_by_role("button", name="Register and check access", exact=True)).to_be_enabled()
            expect(form.get_by_label("LUN", exact=True)).to_have_value("19")
            expect(form.get_by_label("LUN WWID", exact=True)).to_have_value("manual-registration-wwid")
            expect(form.get_by_label("Existing filesystem UUID", exact=True)).to_have_value(manual_uuid)
            expect(form.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).to_be_checked()
            assert not any(path == "storage/discover" and body.get("target_iqn") for path, body in submissions[before:])
            mode["fail_registration"] = False
            # Selecting the same target retries after either manual cancellation or an error.
            for outcome in ("failed", "single"):
                mode["inspection"] = outcome
                form.get_by_role("button", name="Discover targets", exact=True).click()
                with page.expect_response(lambda r: r.url.endswith("/api/v1/storage/discover") and r.request.post_data_json.get("target_iqn") == "iqn.2026-01.example.test:vm-storage"):
                    form.get_by_role("button", name="Use iqn.2026-01.example.test:vm-storage", exact=True).click()
                if outcome == "failed":
                    expect(form.get_by_text("Core could not inspect this selected target.", exact=False)).to_be_visible()
                    expect(form.get_by_label("LUN", exact=True)).to_have_value("19")
                    expect(form.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).to_be_checked()
                else:
                    expect(form.get_by_label("LUN", exact=True)).to_have_value("0")
                    expect(form.get_by_label("LUN WWID", exact=True)).to_have_value(inspected_lun["wwid"])
                    # A newly detected LUN identity must be acknowledged again.
                    expect(form.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).not_to_be_checked()
            checks.append("Failed submissions preserve cancelled manual identities and exclusivity without reinspection; selecting the same target retries cancelled and failed inspections")

            def inspect_mode(value):
                # Typing the IQN never inspects; the Inspect LUNs button is the explicit trigger.
                mode["inspection"] = value
                iqn = f"iqn.2026-01.example.test:{value}"
                before = len(submissions)
                form.get_by_label("Target IQN", exact=True).fill(iqn)
                assert not any(path == "storage/discover" for path, _ in submissions[before:]), "typing the IQN must not inspect"
                with page.expect_request(lambda r: r.url.endswith("/api/v1/storage/discover") and r.post_data_json.get("target_iqn") == iqn):
                    inspect_button.click()

            inspect_mode("multiple")
            picker = form.get_by_role("combobox", name="Detected LUN", exact=True)
            expect(picker).to_be_visible()
            expect(picker).to_have_value("")
            expect(form.get_by_label("LUN", exact=True)).to_have_value("")
            expect(form.get_by_label("LUN WWID", exact=True)).to_have_value("")
            picker.focus()
            page.keyboard.press("Space")
            page.keyboard.press("End")
            page.keyboard.press("Enter")
            expect(form.get_by_label("LUN", exact=True)).to_have_value("7")
            expect(form.get_by_label("LUN WWID", exact=True)).to_have_value("3600-second-qa")
            expect(form.get_by_label("Existing filesystem UUID", exact=True)).to_have_value("00000000-0000-0000-0000-000000000456")
            page.screenshot(path=str(args.evidence / "iscsi-multiple-luns.png"), full_page=True)
            # An explicit multi-LUN choice registers exactly the chosen LUN's identity (the IQN edit cleared exclusivity,
            # so it is acknowledged again before the browser lets the form submit).
            form.get_by_label(iscsi_label, exact=True).check()
            submit_failed_registration()
            assert last_iscsi_source() == {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:multiple", "lun": 7, "wwid": "3600-second-qa", "filesystem_uuid": "00000000-0000-0000-0000-000000000456", "exclusive_target": True, "mode": "exclusive"}, last_iscsi_source()
            for value, message in [("empty", "Core reported no LUNs"), ("blank", "No filesystem signature detected."), ("unsupported", "not supported for ordinary iSCSI"), ("failed", "Core could not inspect this selected target.")]:
                inspect_mode(value)
                expect(form.get_by_text(message, exact=False)).to_be_visible()
                if value in ("empty", "failed"):
                    expect(form.get_by_label("LUN", exact=True)).to_have_value("")
                if value == "blank":
                    expect(form.get_by_label("LUN", exact=True)).to_have_value("0")
                    expect(form.get_by_label("Existing filesystem UUID", exact=True)).to_have_value("")
                expect(form.get_by_label("LUN WWID", exact=True)).to_be_editable()
            checks.append("Selected targets autofill actual LUN zero and Core identity; multiple LUNs require keyboard-accessible explicit selection; blank, unsupported, empty and failed observations remain honest and editable")

            mode["inspection"] = "single"
            before = len(submissions)
            form.get_by_label("CHAP account", exact=True).fill("fixture-chap")
            with page.expect_request(lambda r: r.url.endswith("/api/v1/storage/discover") and r.post_data_json.get("target_iqn")) as account_only:
                inspect_button.click()
            assert "credentials" not in account_only.value.post_data_json, account_only.value.post_data_json
            chap_secret = "fixture-transient-chap-secret"
            before = len(submissions)
            form.get_by_label("CHAP secret", exact=True).fill(chap_secret)
            assert not any(path == "storage/discover" for path, _ in submissions[before:]), "typing CHAP must not inspect"
            with page.expect_request(lambda r: r.url.endswith("/api/v1/storage/discover") and r.post_data_json.get("target_iqn") and r.post_data_json.get("credentials", {}).get("secret") == chap_secret) as chap:
                inspect_button.click()
            assert chap.value.post_data_json["credentials"] == {"username": "fixture-chap", "secret": chap_secret}
            expect(form.get_by_label("LUN", exact=True)).to_have_value("0")
            # Editing CHAP credentials keeps the detected identity; only kind/host/portal/IQN changes clear it.
            form.get_by_label("CHAP account", exact=True).fill("fixture-chap-edited")
            expect(form.get_by_label("LUN", exact=True)).to_have_value("0")
            expect(form.get_by_label("LUN WWID", exact=True)).to_have_value(inspected_lun["wwid"])
            form.get_by_label("CHAP account", exact=True).fill("fixture-chap")
            assert chap_secret not in page.locator("body").inner_text()
            assert chap_secret not in page.evaluate("JSON.stringify({local: {...localStorage}, session: {...sessionStorage}})")
            for index, field in enumerate(("Target portal", "Target IQN", "CHAP account", "CHAP secret")):
                mode["inspection"] = "deferred"
                start_inspection(f"iqn.2026-01.example.test:pending-{index}")
                pending, reply = take_deferred(page, deferred_inspection, "selected-target LUN inspection")
                form.get_by_label(field, exact=True).fill("" if field == "Target IQN" else f"changed-{index}")
                # Editing the portal, IQN, CHAP account or CHAP secret ends a running inspection by itself; no manual cancel is left.
                expect(form.get_by_role("button", name="Cancel LUN inspection", exact=True)).to_have_count(0)
                if field in ("CHAP account", "CHAP secret"):
                    expect(form.get_by_text("Inspection interrupted, select the target again.", exact=True)).to_be_visible()
                form.get_by_label("LUN", exact=True).fill("23")
                form.get_by_label("LUN WWID", exact=True).fill("manual-wwid")
                pending.fulfill(json=reply)
                expect(form.get_by_label("LUN", exact=True)).to_have_value("23")
                expect(form.get_by_label("LUN WWID", exact=True)).to_have_value("manual-wwid")
                expect(form.get_by_label("Existing filesystem UUID", exact=True)).to_have_value("")
                deferred_inspection.clear()
            start_inspection("iqn.2026-01.example.test:manual")
            pending, reply = take_deferred(page, deferred_inspection, "manual LUN entry during inspection")
            form.get_by_label("LUN", exact=True).fill("25")
            pending.fulfill(json=reply)
            expect(form.get_by_label("LUN", exact=True)).to_have_value("25")
            start_inspection("iqn.2026-01.example.test:host")
            pending, reply = take_deferred(page, deferred_inspection, "changing inspection host")
            form.get_by_label("Host", exact=True).select_option("")
            form.get_by_label("LUN", exact=True).fill("26")
            pending.fulfill(json=reply)
            expect(form.get_by_label("LUN", exact=True)).to_have_value("26")
            form.get_by_label("Target IQN", exact=True).fill("")
            form.get_by_label("Host", exact=True).select_option("core-qa")
            start_inspection("iqn.2026-01.example.test:cancel")
            pending, reply = take_deferred(page, deferred_inspection, "cancelled LUN inspection")
            form.get_by_role("button", name="Cancel LUN inspection", exact=True).click()
            form.get_by_label("LUN", exact=True).fill("24")
            pending.fulfill(json=reply)
            expect(form.get_by_label("LUN", exact=True)).to_have_value("24")
            start_inspection("iqn.2026-01.example.test:close")
            pending, reply = take_deferred(page, deferred_inspection, "closing LUN inspection")
            form.get_by_role("button", name="Close", exact=True).click()
            pending.fulfill(json=reply)
            expect(form).to_have_count(0)
            page.get_by_role("button", name="Register storage", exact=True).click()
            form.get_by_label("Repository type").select_option("iscsi")
            expect(form.get_by_label("LUN", exact=True)).to_have_value("")
            # Removing the inspection capability models a Core without it, without manufacturing an empty success.
            host["capabilities"].remove("storage-iscsi-inspect-v1")
            refresh()
            form.get_by_label("Host", exact=True).select_option("core-qa")
            form.get_by_label("Target portal", exact=True).fill("storage.example.test:3260")
            before = len(submissions)
            form.get_by_label("Target IQN", exact=True).fill("iqn.2026-01.example.test:manual")
            expect(form.get_by_text("This Core does not support read-only LUN inspection.", exact=False)).to_be_visible()
            assert not any(path == "storage/discover" and body.get("target_iqn") for path, body in submissions[before:])
            form.get_by_label("LUN", exact=True).fill("9")
            expect(form.get_by_label("LUN", exact=True)).to_have_value("9")
            # The submit-only CHAP scrub must not behave like an operator credential edit.
            form.get_by_label("Name", exact=True).fill("Manual iSCSI")
            form.get_by_label("CHAP account", exact=True).fill("manual-chap")
            form.get_by_label("CHAP secret", exact=True).fill("fixture-manual-chap-secret")
            form.get_by_label("LUN", exact=True).fill("9")
            form.get_by_label("LUN WWID", exact=True).fill("manual-fixture-wwid")
            form.get_by_label("Existing filesystem UUID", exact=True).fill(manual_uuid)
            form.get_by_label("This LUN is restricted to this Core initiator only", exact=True).check()
            mode["fail_credentials"] = True
            with page.expect_response(lambda r: r.url.endswith("/api/v1/storage/credentials") and r.request.method == "POST"):
                form.get_by_role("button", name="Register and check access", exact=True).click()
            expect(page.get_by_role("alert")).to_contain_text("Simulated credential handoff failure")
            expect(form.get_by_label("CHAP secret", exact=True)).to_have_value("")
            expect(form.get_by_label("LUN", exact=True)).to_have_value("9")
            expect(form.get_by_label("LUN WWID", exact=True)).to_have_value("manual-fixture-wwid")
            expect(form.get_by_label("Existing filesystem UUID", exact=True)).to_have_value(manual_uuid)
            expect(form.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).to_be_checked()
            mode["fail_credentials"] = False
            form.get_by_role("button", name="Close", exact=True).click()
            saved_source = {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:saved", "lun": 12, "wwid": "3600-saved-qa", "filesystem_uuid": manual_uuid, "exclusive_target": True, "mode": "exclusive"}}
            data["storage"] = [{**healthy, "id": "saved-iscsi-qa", "name": "Saved iSCSI", "kind": "iscsi", "source": saved_source}]
            host["capabilities"].append("storage-iscsi-inspect-v1")
            mode["inspection"] = "multiple"
            refresh()
            before = len(submissions)
            page.get_by_role("row").filter(has_text="Saved iSCSI").get_by_role("button", name="Update", exact=True).click()
            editor = page.get_by_role("region", name="Update storage", exact=True)
            expect(editor.get_by_label("LUN", exact=True)).to_have_value("12")
            expect(editor.get_by_label("LUN WWID", exact=True)).to_have_value(saved_source["iscsi"]["wwid"])
            expect(editor.get_by_label("Existing filesystem UUID", exact=True)).to_have_value(manual_uuid)
            expect(editor.get_by_label("This LUN is restricted to this Core initiator only", exact=True)).to_be_checked()
            assert not any(path == "storage/discover" and body.get("target_iqn") for path, body in submissions[before:])
            editor.get_by_label("Name", exact=True).fill("Renamed iSCSI")
            with page.expect_response(lambda r: r.url.endswith("/api/v1/storage/saved-iscsi-qa/update") and r.request.method == "POST") as updated:
                editor.get_by_role("button", name="Update and check access", exact=True).click()
            assert updated.value.request.post_data_json["registration"]["source"] == saved_source
            expect(editor).to_have_count(0)
            expect(page.get_by_text("Storage update request submitted for Renamed iSCSI", exact=False)).to_be_visible()
            data["storage"] = []
            refresh()
            host["capabilities"].remove("storage-iscsi-inspect-v1")
            page.get_by_role("button", name="Register storage", exact=True).click()
            checks.append("Failed CHAP handoff scrubs only the secret, retaining old-Core manual identity and exclusivity; existing iSCSI name-only updates preserve source fields without inspection")
            host["capabilities"].append("storage-iscsi-inspect-v1")
            mode["inspection"] = "single"
            checks.append("CHAP travels only in transient inspection POST bodies; target/portal/host/CHAP changes, manual edits, cancel and close reject late field writes; a Core without inspection keeps a manual workflow without selected-target requests")
            form.get_by_label("Repository type").select_option("smb")
            form.get_by_label("Name", exact=True).fill("VM share")
            form.get_by_label("SMB server").fill("files.example.test")
            form.get_by_label("Account", exact=True).fill("nex-vm")
            form.get_by_label("Domain", exact=True).fill("truenas")
            secret = "not-for-display-qa-secret"
            password = form.get_by_label("Password", exact=True)
            expect(password).to_have_attribute("type", "password")
            password.fill(secret)
            # SMB browsing needs only the Core, server and transient credentials; a manual
            # share is optional until selection and is never smuggled into the root request.
            expect(form.get_by_role("button", name="Browse shared folders", exact=True)).to_be_enabled()
            form.get_by_label("Share", exact=True).fill("old-share")
            expect(form.get_by_label("Existing directory within share")).to_have_count(0)
            with page.expect_request("**/api/v1/storage/discover") as smb_request:
                form.get_by_role("button", name="Browse shared folders", exact=True).click()
            smb_browser = page.get_by_role("dialog", name="Browse shared folders", exact=True)
            expect(smb_browser).to_be_visible()
            smb_root = smb_request.value.post_data_json
            assert smb_root["kind"] == "smb" and smb_root["credentials"] == {"username": "nex-vm", "secret": secret, "domain": "truenas"} and "path" not in smb_root, smb_root
            smb_browser.get_by_role("button", name="Select /team-share", exact=True).dblclick()
            share_folder = smb_browser.get_by_role("button", name="Select /team-share/vms, a backup repository was found here", exact=True)
            share_folder.click()
            expect(share_folder).to_have_attribute("aria-pressed", "true")
            expect(share_folder.get_by_text("E", exact=True)).to_be_visible()
            page.screenshot(path=str(args.evidence / "smb-browser.png"), full_page=True)
            smb_browser.get_by_role("button", name="Select folder", exact=True).click()
            expect(form.get_by_label("Share", exact=True)).to_have_value("team-share")
            expect(form.locator(".storage-selected-folder")).to_have_text("vms")
            checks.append("SMB browsing works before a manual share exists and applies the selected share plus relative directory only after confirmation")
            with page.expect_response(lambda reply: reply.url.endswith("/api/v1/storage") and reply.request.method == "POST"):
                page.get_by_role("button", name="Register and check access", exact=True).click()
            expect(page.get_by_text("core-assigned-repository", exact=True)).to_have_count(0)
            credential = next(body for path, body in submissions if path == "storage/credentials" and body["credentials"].get("secret") == secret)
            register = next(body for path, body in submissions if path == "storage" and body.get("kind") == "smb")
            assert credential["credentials"] == {"username": "nex-vm", "secret": secret, "domain": "truenas"}, credential
            assert register["source"] == {"smb": {"server": "files.example.test", "share": "team-share", "directory": "vms"}}, register
            assert register["credential_receipt"] == "core-only-receipt" and secret not in json.dumps(register), register
            assert credential["registration_request_id"] == register["request_id"] == register["credential_request_id"], (credential, register)
            # Wait for close() after reload; asserting while the form is still mounted races the password input.
            expect(page.get_by_role("region", name="Register storage")).to_have_count(0)
            expect(page.get_by_text("Storage registration request submitted for VM share", exact=False)).to_be_visible()
            assert secret not in page.content() and secret not in page.evaluate("Object.values(sessionStorage).join(' ')")
            expect(page.get_by_text("VM share", exact=True)).to_be_visible()
            # The queued Core registration task is listed on Tasks (the old bottom task strip is gone).
            rail.get_by_role("link", name="Tasks", exact=True).click()
            expect(page.get_by_role("row").filter(has_text="storage-register-core")).to_contain_text("Waiting to be sent")
            rail.get_by_role("link", name="Storage", exact=True).click()
            pending_row = page.get_by_role("row").filter(has_text="VM share")
            for action in ("Recheck", "Update / rotate credentials", "Detach"):
                expect(pending_row.get_by_role("button", name=action, exact=True)).to_be_disabled()
            page.screenshot(path=str(args.evidence / "pending-core-id.png"), full_page=True)
            # Core's next report projects its allocated ID/generation before lifecycle controls open.
            data["storage"] = [healthy]
            refresh()
            expect(page.get_by_role("row").filter(has_text="VM share").get_by_role("button", name="Recheck", exact=True)).to_be_enabled()
            checks.append("NFS, ZFS, SMB and iSCSI forms expose their typed fields; SMB secrets stay in the transient handoff; pending Core-assignment rows cannot run lifecycle actions")

            page.get_by_role("button", name="Recheck", exact=True).click()
            expect(page.get_by_role("alert")).to_contain_text("Simulated Core access check unavailable")
            page.screenshot(path=str(args.evidence / "error.png"), full_page=True)
            mode["fail_recheck"] = False
            with page.expect_response(lambda r: r.url.endswith("/recheck") and r.request.method == "POST"):
                page.get_by_role("button", name="Recheck", exact=True).click()
            expect(page.get_by_text("Access recheck submitted for VM share", exact=False)).to_be_visible()
            expect(page.get_by_role("button", name="Detach", exact=True)).to_be_disabled()
            data["storage"] = [healthy]
            refresh()
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

            # VM placement only receives repositories Core currently reports writable and capable.
            synthetic = {**healthy, "id": "synthetic-core-id", "name": "Synthetic repository", "generation": 0}
            data["storage"] = [healthy, unsuitable, synthetic]
            refresh()
            expect(page.get_by_text("Read-only NFS", exact=True)).to_be_visible()
            synthetic_row = page.get_by_role("row").filter(has_text="Synthetic repository")
            for action in ("Recheck", "Update / rotate credentials", "Detach"):
                expect(synthetic_row.get_by_role("button", name=action, exact=True)).to_be_disabled()
            rail.get_by_role("link", name="VMs", exact=True).click()
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
            rail.get_by_role("link", name="Storage", exact=True).click()
            # UI-1: queued and failed registrations have no repository id yet (id ""). When the
            # queued one becomes healthy with a real id, its old row must not stay behind.
            # Core has not checked access for a registration it has not accepted yet.
            pending = lambda record, **fields: {**{k: v for k, v in record.items() if k != "access"}, "id": "", **fields}
            failed = [pending(unsuitable, name=f"Failed repository {n}", status="failed", operation_id=f"storage-register-failed-{n}", error="export not found") for n in (1, 2)]
            data["storage"] = [pending(healthy, status="queued"), *failed]
            refresh()
            storage_rows = page.locator("tr[data-storage-id]")
            expect(storage_rows).to_have_count(3)
            expect(storage_rows.filter(has_text="VM share")).to_contain_text(re.compile("queued", re.I))
            data["storage"] = [healthy, *failed]
            refresh()
            expect(page.locator('tr[data-storage-id="core-assigned-repository"]')).to_be_visible()
            expect(storage_rows).to_have_count(3)
            expect(page.locator("tr[data-storage-id] .badge").filter(has_text="queued")).to_have_count(0)
            page.screenshot(path=str(args.evidence / "pending-row-replaced.png"), full_page=True)
            checks.append("Two failed and one queued registration without ids render three rows; the queued row is replaced, not duplicated, once Core reports its id")
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
