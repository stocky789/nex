#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Built UI check for Core-reported clustered iSCSI safety states."""
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import threading
from storage_browser_support import take_deferred

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[2]


def main():
    server = ThreadingHTTPServer(("127.0.0.1", 0), partial(SimpleHTTPRequestHandler, directory=str(ROOT / "web/dist")))
    server.daemon_threads = True
    server.block_on_close = False
    threading.Thread(target=server.serve_forever, daemon=True).start()
    hosts = [{"id": f"core-{n}", "pool_id": "pool-qa", "hostname": f"core-{n}", "connection": "fresh", "revoked": False,
              "capabilities": ["managed-vm-v1", "storage-clustered-iscsi-v2", "storage-repositories-v1", "storage-discovery-v1"], "inventory": {"os_version": "Nex Core 0.4.8", "cpu_count": 4, "memory_bytes": 8 * 1024 ** 3}} for n in range(1, 4)]
    cluster = {"pool_id": "pool-qa", "host_id": "core-1", "status": "needs_attention", "operations": [{
        "request_id": "cluster-apply-1", "membership_generation": 2, "status": "awaiting_fencing", "updated_at": "2026-09-12T00:00:00Z",
        "config": {"repository_id": "gfs2-lun", "cluster_id": "qa-cluster", "generation": 4, "lock_table": "qa_lock",
                   "device_path": "/dev/disk/by-id/scsi-qa", "mount_path": "/var/lib/nex-storage/gfs2-qa",
                   "nodes": [{"host_id": "core-1", "corosync_node_id": 1, "corosync_address": "192.0.2.10", "fence": {"kind": "virsh", "endpoint": "lab.example.test", "target_id": "00000000-0000-0000-0000-000000000001", "virsh_uri": "qemu+ssh://lab/system"}}]},
        "members": []
    }]}
    # A stable snapshot needs a matching browser clock: real storage access
    # expires after 30s, but unrelated test steps may take longer on a busy runner.
    observed_now = "2026-09-18T00:00:00Z"
    data = {"session": {"username": "operator", "csrf_token": "fixture-csrf"}, "sites": [{"id": "site-qa", "name": "QA site"}],
            "pools": [{"id": "pool-qa", "site_id": "site-qa", "name": "QA pool", "master_host_id": "core-1"}], "hosts": hosts,
            "storage": [
                {"id": "gfs2-lun", "pool_id": "pool-qa", "host_id": "core-1", "name": "Clustered GFS2", "kind": "iscsi", "generation": 4, "status": "succeeded",
                 "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:gfs2", "lun": 0, "wwid": "3600gfs2", "filesystem_uuid": "00000000-0000-0000-0000-000000000000", "exclusive_target": False, "mode": "clustered_gfs2", "cluster_id": "qa-cluster", "lock_table": "qa-cluster:gfs2-lun", "eligible_host_ids": ["core-1", "core-2", "core-3"]}},
                 "capabilities": {"vm_disk": True, "eligible_host_ids": ["core-1", "core-2", "core-3"]}, "access": {"status": "healthy", "readable": True, "writable": True},
                 "member_access": [{"repository_id": "gfs2-lun", "host_id": "core-2", "generation": 4, "identity": "gfs2-core-2", "readable": True, "writable": True, "qemu_writable": True, "locking": True, "observed_at": observed_now}]},
                {"id": "exclusive-lun", "pool_id": "pool-qa", "host_id": "core-1", "name": "Exclusive iSCSI", "kind": "iscsi", "generation": 4, "status": "succeeded",
                 "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:exclusive", "lun": 1, "wwid": "3600exclusive", "filesystem_uuid": "00000000-0000-0000-0000-000000000001", "exclusive_target": True, "mode": "exclusive"}},
                 "capabilities": {"vm_disk": True}, "access": {"status": "healthy", "readable": True, "writable": True}},
                {"id": "stale-gfs2", "pool_id": "pool-qa", "host_id": "core-1", "name": "Stale GFS2", "kind": "iscsi", "generation": 4, "status": "succeeded",
                 "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:stale", "lun": 2, "wwid": "3600stale", "filesystem_uuid": "00000000-0000-0000-0000-000000000002", "exclusive_target": False, "mode": "clustered_gfs2", "cluster_id": "qa-cluster", "lock_table": "qa-cluster:stale", "eligible_host_ids": ["core-1", "core-2", "core-3"]}},
                 "capabilities": {"vm_disk": True}, "access": {"status": "healthy", "readable": True, "writable": True},
                 "member_access": [{"repository_id": "stale-gfs2", "host_id": "core-2", "generation": 4, "identity": "stale-core-2", "readable": True, "writable": True, "qemu_writable": True, "locking": True, "observed_at": "2026-09-12T00:00:00Z"}]},
                {"id": "error-gfs2", "pool_id": "pool-qa", "host_id": "core-1", "name": "Errored GFS2", "kind": "iscsi", "generation": 4, "status": "succeeded",
                 "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:error", "lun": 3, "wwid": "3600error", "filesystem_uuid": "00000000-0000-0000-0000-000000000003", "exclusive_target": False, "mode": "clustered_gfs2", "cluster_id": "qa-cluster", "lock_table": "qa-cluster:error", "eligible_host_ids": ["core-1", "core-2", "core-3"]}},
                 "capabilities": {"vm_disk": True}, "access": {"status": "healthy", "readable": True, "writable": True},
                 "member_access": [{"repository_id": "error-gfs2", "host_id": "core-2", "generation": 4, "identity": "error-core-2", "readable": True, "writable": True, "qemu_writable": True, "locking": True, "observed_at": "2099-09-12T00:00:00Z", "error": "Core access check failed"}]},
            ], "vms": [], "networks": [], "media": [], "operations": [], "audit": []}
    errors, requests, credentials, fences, applies, deferred_discovery, deferred_browser, deferred_credentials = [], [], [], [], [], [], [], []
    mode = {"fence_timeout": False, "fence_abort": False, "discovery": "success", "browser_deferred": False, "credential_deferred": False}

    def api(route):
        request = route.request
        path = request.url.split("/api/v1/", 1)[1]
        requests.append(path)
        if path == "session": route.fulfill(json=data["session"])
        elif path.startswith("storage/cluster?"): route.fulfill(json=cluster)
        elif path == "storage/cluster/credentials":
            credentials.append(request.post_data_json)
            route.fulfill(status=202, json={"request_id": request.post_data_json["request_id"], "status": "accepted"})
        elif path == "storage/credentials":
            body = request.post_data_json
            reply = {"receipt": "core-only-race-receipt"}
            if mode["credential_deferred"]: deferred_credentials.append((route, reply))
            else: route.fulfill(status=201, json=reply)
        elif path == "storage/cluster/apply":
            applies.append(request.post_data_json)
            route.fulfill(status=202, json={"status": "accepted"})
        elif path == "storage/discover":
            body = request.post_data_json
            assert body["pool_id"] == "pool-qa" and body["host_id"] == "core-1", body
            if body["kind"] == "nfs":
                reply = {"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-1", "status": "succeeded", "candidates": [{"kind": "nfs", "source": {"nfs": {"server": body["server"], "export": "/exports/late"}}, "path": "/exports/late", "browsable": True}]}
                if mode["browser_deferred"]: deferred_browser.append((route, reply))
                else: route.fulfill(json=reply)
                return
            assert body["kind"] == "iscsi", body
            reply = {"request_id": body["request_id"], "pool_id": "pool-qa", "host_id": "core-1", "status": "succeeded", "candidates": [{"kind": "iscsi", "source": {"iscsi": {"portal": body["server"], "target_iqn": "iqn.2026-01.example.test:discovered-gfs2"}}, "browsable": False}]}
            if mode["discovery"] == "deferred": deferred_discovery.append((route, reply))
            elif mode["discovery"] == "stale": route.fulfill(status=504, json={"error": "Core target discovery became stale"})
            else: route.fulfill(json=reply)
        elif path == "storage/cluster/verify-fence":
            fences.append(request.post_data_json)
            if mode["fence_timeout"]: route.fulfill(status=504, json={"error": "Core fence result is unknown"})
            elif mode["fence_abort"]: route.abort("connectionreset")
            else:
                cluster["operations"][0]["config"]["fence_proofs"] = [{"request_id": request.post_data_json["request_id"], "host_id": "core-1", "status": "unverified", "target_id": "00000000-0000-0000-0000-000000000001", "exclusion_verified": False, "observed_at": "2026-09-12T00:00:01Z"}]
                route.fulfill(status=202, json={"status": "accepted"})
        # Reads this fixture does not simulate (usage history, pool members) are unavailable.
        elif request.method == "GET" and path.split("?", 1)[0] in data: route.fulfill(json=data[path.split("?", 1)[0]])
        elif request.method == "GET": route.fulfill(status=404, json={"error": "not simulated"})
        else: route.fulfill(status=204)

    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            # Freeze Date only; polling, cancellation and simulated-delay timers run.
            page.clock.set_fixed_time(observed_now)
            # Functional checks share the 30s action budget on busy CI runners;
            # this suite does not impose a five-second UI performance requirement.
            expect.set_options(timeout=30000)
            # Match the returning signed-in browser whose session the API fixture supplies.
            page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.route("**/api/v1/**", api)
            page.goto(f"http://127.0.0.1:{server.server_port}/#/pools/pool-qa")
            page.wait_for_load_state("networkidle")
            rail = page.get_by_role("navigation", name="Sections", exact=True)

            def refresh():
                # The console has no manual refresh; wait for its next automatic inventory poll.
                with page.expect_response(lambda r: r.url.endswith("/api/v1/storage") and r.request.method == "GET", timeout=15000):
                    pass
            rail.get_by_role("link", name="Storage", exact=True).click()
            # A cancelled picker must ignore a late successful Core response instead
            # of recreating its native dialog with stale draft data.
            page.get_by_role("button", name="Register storage", exact=True).click()
            registration = page.get_by_role("region", name="Register storage", exact=True)
            registration.get_by_label("Repository type").select_option("nfs")
            registration.get_by_label("NFS server", exact=True).fill("nfs.example.test")
            mode["browser_deferred"] = True
            # Simulate one renderer stall during input dispatch (not slow discovery).
            # Shared CI runners can pause after actionability checks have passed.
            registration.get_by_role("button", name="Browse exports", exact=True).evaluate("""button => {
                button.addEventListener('pointerdown', () => {
                    const end = performance.now() + 3500;
                    while (performance.now() < end) {}
                }, { once: true });
            }""")
            registration.get_by_role("button", name="Browse exports", exact=True).click()
            browser_dialog = page.get_by_role("dialog", name="Browse exports", exact=True)
            expect(browser_dialog).to_be_visible()
            pending_browser, _ = take_deferred(page, deferred_browser, "NFS export discovery")
            browser_dialog.get_by_role("button", name="Cancel", exact=True).click()
            expect(browser_dialog).to_be_hidden()
            pending_browser.fulfill(json={"status": "succeeded", "candidates": [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/late"}}, "path": "/exports/late", "browsable": True}]})
            expect(browser_dialog).to_be_hidden()
            expect(page.get_by_role("button", name="Open /exports/late", exact=True)).to_have_count(0)
            registration.get_by_role("button", name="Browse exports", exact=True).click()
            expect(browser_dialog).to_be_visible()
            pending_browser, _ = take_deferred(page, deferred_browser, "NFS export discovery")
            page.keyboard.press("Escape")
            expect(browser_dialog).to_be_hidden()
            pending_browser.fulfill(json={"status": "succeeded", "candidates": [{"kind": "nfs", "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/late"}}, "path": "/exports/late", "browsable": True}]})
            expect(browser_dialog).to_be_hidden()
            expect(page.get_by_role("button", name="Open /exports/late", exact=True)).to_have_count(0)
            mode["browser_deferred"] = False
            registration.get_by_role("button", name="Close", exact=True).click()
            panel = page.get_by_role("region", name="Clustered iSCSI storage", exact=True)
            expect(panel).to_contain_text("no Core fence proof or member observation reported.")
            expect(panel).to_contain_text("health not yet reported")
            fence_action = panel.get_by_role("button", name="Verify power fence", exact=True)
            fence_action.click()
            dialog = page.get_by_role("dialog", name="Verify power fence for core-1?", exact=True)
            expect(dialog).to_be_visible()
            bounds = dialog.bounding_box()
            assert bounds and bounds["x"] >= 0 and bounds["y"] >= 0 and bounds["x"] + bounds["width"] <= 1440 and bounds["y"] + bounds["height"] <= 1000, bounds
            assert dialog.evaluate("element => element.matches(':modal')")
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-fence-dialog.png"), full_page=False)
            register = page.get_by_role("button", name="Register storage", exact=True)
            register_bounds = register.bounding_box()
            assert register_bounds, register_bounds
            page.mouse.click(register_bounds["x"] + register_bounds["width"] / 2, register_bounds["y"] + register_bounds["height"] / 2)
            expect(dialog).to_be_visible()
            expect(page.get_by_role("region", name="Register storage", exact=True)).to_be_hidden()
            page.keyboard.press("Escape")
            expect(dialog).to_be_hidden()
            expect(fence_action).to_be_focused()
            assert not fences, fences
            panel.get_by_role("button", name="Verify power fence", exact=True).click()
            dialog.get_by_label("Confirmation", exact=True).fill("FENCE wrong-host")
            expect(dialog.get_by_role("button", name="Verify power fence", exact=True)).to_be_disabled()
            dialog.get_by_role("button", name="Cancel", exact=True).click()
            mode["fence_timeout"] = True
            panel.get_by_role("button", name="Verify power fence", exact=True).click()
            dialog.get_by_label("Confirmation", exact=True).fill("FENCE core-1")
            dialog.get_by_role("button", name="Verify power fence", exact=True).click()
            expect(panel.get_by_role("status").filter(has_text="Fence verification response was not received")).to_be_visible()
            assert len(fences) == 1, fences
            expect(dialog).to_be_hidden()
            fence_button = panel.get_by_role("button", name="Verify power fence", exact=True)
            expect(fence_button).to_be_disabled()
            assert fence_button.get_attribute("title") == "Fence outcome is unknown. Refresh Core status before considering another verification."
            page.get_by_role("button", name="Refresh Core status", exact=True).click()
            expect(fence_button).to_be_enabled()
            mode["fence_timeout"] = False
            mode["fence_abort"] = True
            fence_button.click()
            dialog.get_by_label("Confirmation", exact=True).fill("FENCE core-1")
            dialog.get_by_role("button", name="Verify power fence", exact=True).click()
            expect(panel.get_by_role("status").filter(has_text="Fence verification response was not received")).to_be_visible()
            assert len(fences) == 2, fences
            expect(dialog).to_be_hidden()
            expect(fence_button).to_be_disabled()
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-fence-unknown-mock.png"), full_page=True)
            page.get_by_role("button", name="Refresh Core status", exact=True).click()
            expect(fence_button).to_be_enabled()
            mode["fence_timeout"] = False
            mode["fence_abort"] = False
            fence_button.click()
            dialog.get_by_label("Confirmation", exact=True).fill("FENCE core-1")
            dialog.get_by_role("button", name="Verify power fence", exact=True).click()
            expect(panel).to_contain_text("Fence proof for core-1")
            expect(panel).to_contain_text("Core is reconciling this requested proof; it is not safe to retry.")
            assert len(fences) == 3 and fences[2]["host_id"] == "core-1" and fences[2]["repository_id"] == "gfs2-lun" and fences[2]["fence_confirmation"] == "FENCE core-1", fences
            fence_button = panel.get_by_role("button", name="Verify power fence", exact=True)
            expect(fence_button).to_be_disabled()
            assert fence_button.get_attribute("title") == "Core is reconciling the existing fence request; Nex will not retry it automatically."
            cluster["operations"][0]["config"]["fence_proofs"][0].update({"status": "passed", "exclusion_verified": True, "observed_at": "2026-09-12T00:00:02Z"})
            page.get_by_role("button", name="Refresh Core status", exact=True).click()
            expect(panel.get_by_text("Passed", exact=True)).to_be_visible()
            expect(panel).to_contain_text("Core recorded the target as excluded.")
            reverify = panel.get_by_role("button", name="Reverify power fence", exact=True)
            expect(reverify).to_be_enabled()
            # Core rejects a proof until all derived bootstrap metadata is present.
            # A partial status must therefore leave the destructive action unavailable.
            del cluster["operations"][0]["config"]["mount_path"]
            page.get_by_role("button", name="Refresh Core status", exact=True).click()
            expect(reverify).to_be_disabled()
            expect(reverify).to_have_attribute("title", "Wait for Core to derive cluster metadata before destructive fencing.")
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-browser.png"), full_page=True)
            expect(panel.get_by_text("Passed", exact=True)).to_be_visible()
            cluster["operations"][0]["config"]["mount_path"] = "/var/lib/nex-storage/gfs2-qa"
            cluster["operations"][0]["config"]["fence_proofs"][0].update({"status": "uncertain", "exclusion_verified": False, "error": "survivor transport needs reconciliation"})
            page.get_by_role("button", name="Refresh Core status", exact=True).click()
            expect(panel.get_by_text("Uncertain", exact=True)).to_be_visible()
            expect(reverify).to_be_enabled()
            reverify.click()
            reverify_dialog = page.get_by_role("dialog", name="Reverify power fence for core-1?", exact=True)
            expect(reverify_dialog).to_contain_text("Core previously reported uncertain")
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-fence-reverify-mock.png"), full_page=False)
            assert len(fences) == 3, fences
            reverify_dialog.get_by_label("Confirmation", exact=True).fill("FENCE wrong-host")
            expect(reverify_dialog.get_by_role("button", name="Reverify power fence", exact=True)).to_be_disabled()
            reverify_dialog.get_by_role("button", name="Cancel", exact=True).click()
            assert len(fences) == 3, fences
            reverify.click()
            reverify_dialog.get_by_label("Confirmation", exact=True).fill("FENCE core-1")
            reverify_dialog.get_by_role("button", name="Reverify power fence", exact=True).click()
            expect(panel).to_contain_text("Core is reconciling this requested proof; it is not safe to retry.")
            assert len(fences) == 4 and fences[3]["host_id"] == "core-1" and fences[3]["repository_id"] == "gfs2-lun", fences
            expect(panel).to_contain_text("Core is reconciling this requested proof; it is not safe to retry.")
            config = cluster["operations"][0]["config"]
            config["fence_proofs"][0].update({"status": "passed", "exclusion_verified": True, "error": "", "observed_at": "2026-09-12T00:00:03Z"})
            config["nodes"].extend([
                {"host_id": f"core-{n}", "corosync_node_id": n, "corosync_address": f"192.0.2.{9+n}", "fence": {"kind": "virsh", "endpoint": "lab.example.test", "target_id": f"00000000-0000-0000-0000-00000000000{n}", "virsh_uri": "qemu+ssh://lab/system"}}
                for n in (2, 3)
            ])
            # This is the post-fix public member-report contract. Its order is deliberately
            # different from config.nodes: the UI must join reports by host_id.
            cluster["operations"][0]["members"] = [{
                "host_id": f"core-{n}", "generation": 4, "membership_generation": 2,
                "cluster_id": "qa-cluster", "config_digest": "a" * 64, "stage": "health",
                "updated_at": f"2026-09-12T00:00:0{n}Z",
                "status": {"repository_id": "gfs2-lun", "cluster_id": "qa-cluster", "status": "healthy", "healthy": True, "quorate": True, "mounted": True, "fencing_verified": True,
                           "fences": [{"host_id": f"core-{n}", "status": "passed", "exclusion_verified": True, "observed_at": f"2026-09-12T00:00:0{n}Z"}]},
            } for n in (3, 1, 2)]
            cluster["operations"][0]["status"] = "succeeded"
            page.get_by_role("button", name="Refresh Core status", exact=True).click()
            for n in (1, 2, 3):
                report = panel.locator("small").filter(has_text=f"core-{n} · node {n}")
                expect(report).to_have_count(1)
                expect(report).to_contain_text("health: healthy, quorum confirmed, GFS2 mounted, Checked")
            setup = panel.locator("form.cluster-setup")
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-setup-top.png"), full_page=True)
            setup.get_by_label("Target portal", exact=True).fill("storage.example.test:3260")
            mode["discovery"] = "deferred"
            setup.get_by_role("button", name="Discover targets", exact=True).click()
            expect(setup.get_by_role("button", name="Discovering targets…", exact=True)).to_be_disabled()
            pending_discovery, _ = take_deferred(page, deferred_discovery, "iSCSI target discovery")
            setup.get_by_role("button", name="Cancel discovery", exact=True).click()
            expect(setup.get_by_label("Target IQN", exact=True)).to_have_value("")
            pending_discovery.fulfill(json={"status": "succeeded", "candidates": [{"kind": "iscsi", "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:late"}}}]})
            expect(setup.get_by_role("button", name="Use iqn.2026-01.example.test:late", exact=True)).to_have_count(0)
            # Returning to the original portal must not revive its aborted request.
            setup.get_by_role("button", name="Discover targets", exact=True).click()
            pending_discovery, _ = take_deferred(page, deferred_discovery, "iSCSI target discovery")
            setup.get_by_label("Target portal", exact=True).fill("other-storage.example.test:3260")
            setup.get_by_label("Target portal", exact=True).fill("storage.example.test:3260")
            pending_discovery.fulfill(json={"status": "succeeded", "candidates": [{"kind": "iscsi", "source": {"iscsi": {"portal": "storage.example.test:3260", "target_iqn": "iqn.2026-01.example.test:aborted-a"}}}]})
            expect(setup.get_by_role("button", name="Use iqn.2026-01.example.test:aborted-a", exact=True)).to_have_count(0)
            mode["discovery"] = "stale"
            setup.get_by_role("button", name="Discover targets", exact=True).click()
            expect(setup.get_by_role("alert")).to_contain_text("Core target discovery became stale")
            mode["discovery"] = "success"
            setup.get_by_role("button", name="Discover targets", exact=True).click()
            discovered = setup.get_by_role("button", name="Use iqn.2026-01.example.test:discovered-gfs2", exact=True)
            expect(discovered).to_be_visible()
            discovered.click()
            expect(setup.get_by_label("Target IQN", exact=True)).to_have_value("iqn.2026-01.example.test:discovered-gfs2")
            assert any(path == "storage/discover" for path in requests), requests
            for label, value in [("Repository name", "Shared GFS2"), ("LUN", "0"), ("LUN WWID", "3600qa"), ("Filesystem UUID", "00000000-0000-0000-0000-000000000000")]: setup.get_by_label(label, exact=True).fill(value)
            for n in range(1, 4):
                row = setup.get_by_role("group", name=f"core-{n} · Corosync node {n}", exact=True)
                for label, value in [("Corosync address", f"192.0.2.{9+n}"), ("Fence SSH host", "lab.example.test"), ("SSH login", "operator"), ("SSH port", "22"), ("Libvirt domain UUID", f"00000000-0000-0000-0000-00000000000{n}"), ("Virsh URI", "qemu+ssh://lab/system"), ("Pinned SSH host key", "lab.example.test ssh-ed25519 AAAA"), ("SSH private key", "private-key")]: row.get_by_label(label, exact=True).fill(value)
            setup.get_by_role("button", name="Submit Core setup", exact=True).click()
            expect(setup.get_by_text("Core accepted setup.", exact=False)).to_be_visible()
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-setup-browser.png"), full_page=True)
            assert len(credentials) == 3 and {item["target_host_id"] for item in credentials} == {"core-1", "core-2", "core-3"}, credentials
            assert all(item["kind"] == "fence_ssh" and "ssh_private_key" in item["credentials"] for item in credentials), credentials
            assert applies[-1]["registration"]["source"]["iscsi"]["target_iqn"] == "iqn.2026-01.example.test:discovered-gfs2", applies
            assert any(path == "storage/cluster/apply" for path in requests), requests
            assert any(path.startswith("storage/cluster?pool_id=pool-qa") for path in requests), requests
            assert not errors, errors
            # Core owns coordinated clustered lifecycle. The repository can still be
            # rechecked, but the generic update/detach paths must not imply safety.
            clustered_row = page.get_by_role("row").filter(has_text="Clustered GFS2")
            expect(clustered_row.get_by_role("button", name="Recheck", exact=True)).to_be_enabled()
            lifecycle_reason = "Clustered GFS2 lifecycle changes are not coordinated yet. Recheck or refresh Core status instead."
            for action in ("Update", "Detach"):
                button = clustered_row.get_by_role("button", name=action, exact=True)
                expect(button).to_be_disabled()
                expect(button).to_have_attribute("title", lifecycle_reason)
            rail.get_by_role("link", name="VMs", exact=True).click()
            page.get_by_role("button", name="Create VM", exact=True).click()
            wizard = page.get_by_role("region", name="Create VM", exact=True)
            page.get_by_label("VM name", exact=True).fill("clustered-member-vm")
            wizard.get_by_role("combobox", name="Placement host", exact=True).select_option("core-2")
            page.get_by_role("button", name="Next", exact=True).click()
            page.get_by_role("button", name="Next", exact=True).click()
            disk_storage = page.get_by_role("region", name="Create VM", exact=True).get_by_role("combobox", name="Storage", exact=True)
            expect(disk_storage.locator('option[value="gfs2-lun"]')).to_have_count(1)
            disk_options = disk_storage.locator("option").all_text_contents()
            assert any("Clustered GFS2" in option for option in disk_options), disk_options
            assert not any("Exclusive iSCSI" in option or "Stale GFS2" in option or "Errored GFS2" in option for option in disk_options), disk_options
            # Exercise the actual expiry guard with controlled time, independent
            # of how long the previous UI checks took on this runner.
            page.clock.set_fixed_time("2026-09-18T00:00:31Z")
            refresh()
            expect(disk_storage.locator('option[value="gfs2-lun"]')).to_have_count(0)
            page.clock.set_fixed_time(observed_now)
            refresh()
            expect(disk_storage.locator('option[value="gfs2-lun"]')).to_have_count(1)
            disk_storage.select_option("gfs2-lun")
            expect(disk_storage).to_have_value("gfs2-lun")
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-vm-selection-mock.png"), full_page=True)

            # A cached healthy report is not current access once its assigned Core disconnects.
            # Keep both common shared backends here: this used to leave their lifecycle actions
            # and ISO destination choices available merely because access was last healthy.
            data["storage"].extend([
                # A retained Core record can have the same visible identity.  Durable
                # row IDs select the current registration even when this older record
                # renders first with the same name, backend and source.
                {"id": "cached-nfs-historical", "pool_id": "pool-qa", "host_id": "core-1", "name": "Cached NFS", "kind": "nfs", "generation": 4, "status": "failed",
                 "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/vms"}}, "capabilities": {"vm_disk": True, "iso": True},
                 "access": {"status": "healthy", "readable": True, "writable": True, "available_bytes": 32 * 1024 ** 3, "observed_at": "2026-09-12T00:00:00Z"}},
                {"id": "cached-nfs", "pool_id": "pool-qa", "host_id": "core-1", "name": "Cached NFS", "kind": "nfs", "generation": 5, "status": "succeeded",
                 "source": {"nfs": {"server": "nfs.example.test", "export": "/exports/vms"}}, "capabilities": {"vm_disk": True, "iso": True},
                 "access": {"status": "healthy", "readable": True, "writable": True, "available_bytes": 32 * 1024 ** 3, "observed_at": "2026-09-12T00:00:00Z"}},
                {"id": "cached-smb", "pool_id": "pool-qa", "host_id": "core-1", "name": "Cached SMB", "kind": "smb", "generation": 5, "status": "succeeded", "credential_present": True,
                 "source": {"smb": {"server": "files.example.test", "share": "vms"}}, "capabilities": {"vm_disk": True, "iso": True},
                 "access": {"status": "healthy", "readable": True, "writable": True, "available_bytes": 32 * 1024 ** 3, "observed_at": "2026-09-12T00:00:00Z"}},
            ])
            data["media"] = [
                {"id": "cached-nfs-iso", "pool_id": "pool-qa", "host_id": "core-1", "storage_id": "cached-nfs", "name": "cached-nfs.iso", "size_bytes": 65536, "sha256": "a" * 64, "operation_id": "media-nfs", "status": "succeeded"},
                {"id": "cached-smb-iso", "pool_id": "pool-qa", "host_id": "core-1", "storage_id": "cached-smb", "name": "cached-smb.iso", "size_bytes": 65536, "sha256": "b" * 64, "operation_id": "media-smb", "status": "succeeded"},
            ]
            for host in hosts: host["connection"] = "stale"
            rail.get_by_role("link", name="Storage", exact=True).click()
            refresh()
            stale_actions = len(requests)
            stale_reason = "Assigned Core is not freshly connected. Reconnect it before changing this repository."
            # core-2 reported this GFS2 member access moments ago, but its Core is now stale;
            # show it strictly as the last report instead of present access.
            clustered_last_member = page.get_by_role("row").filter(has_text="Clustered GFS2")
            # Its Host access light is the hollow Unknown one, never Accessible.
            expect(clustered_last_member.get_by_role("link", name="core-2: Unknown. The host is not reporting, so its access is not current.")).to_be_visible()
            expect(clustered_last_member.get_by_role("link", name="core-2: Accessible", exact=False)).to_have_count(0)
            cached_nfs_row = page.locator('tr[data-storage-id="cached-nfs"]')
            historical_nfs_row = page.locator('tr[data-storage-id="cached-nfs-historical"]')
            expect(cached_nfs_row).to_have_count(1)
            expect(historical_nfs_row).to_have_count(1)
            # The backend type and its source now sit in separate cells of the row.
            for row in (cached_nfs_row, historical_nfs_row):
                expect(row).to_contain_text("NFSv4 export")
                expect(row).to_contain_text("nfs.example.test:/exports/vms")
            for storage_id, update in (("cached-nfs", "Update"), ("cached-smb", "Update / rotate credentials")):
                row = page.locator(f'tr[data-storage-id="{storage_id}"]')
                # Health reads Unknown over the last report, never the cached Healthy.
                expect(row.locator(".badge", has_text="Unknown")).to_be_visible()
                expect(row.get_by_text("Healthy", exact=True)).to_have_count(0)
                expect(row.get_by_text("Assigned Core is not reporting; showing its last report.", exact=True)).to_be_visible()
                for action in ("Recheck", update, "Detach"):
                    button = row.get_by_role("button", name=action, exact=True)
                    expect(button).to_be_disabled()
                    expect(button).to_have_attribute("title", stale_reason)
                    button.click(force=True)
            assert not any("/recheck" in request or "/detach" in request for request in requests[stale_actions:]), requests[stale_actions:]
            page.screenshot(path=str(ROOT / ".cache/cluster-storage-owner-stale-browser.png"), full_page=True)
            # The ISO library sits below the repositories on Storage.
            page.get_by_role("button", name="Upload ISO", exact=True).click()
            upload = page.get_by_role("region", name="Upload ISO", exact=True)
            destination = upload.get_by_role("combobox", name="Destination storage", exact=True)
            expect(destination).to_be_visible()
            stale_destinations = destination.locator("option").all_text_contents()
            assert stale_destinations == ["Select ISO-ready storage"], stale_destinations
            upload.get_by_role("button", name="Close", exact=True).click()

            hosts[0]["connection"] = "fresh"
            refresh()
            rail.get_by_role("link", name="Storage", exact=True).click()
            for storage_id, update in (("cached-nfs", "Update"), ("cached-smb", "Update / rotate credentials")):
                row = page.locator(f'tr[data-storage-id="{storage_id}"]')
                expect(row.get_by_text("Healthy", exact=True)).to_be_visible()
                expect(row.get_by_role("button", name="Recheck", exact=True)).to_be_enabled()
                expect(row.get_by_role("button", name=update, exact=True)).to_be_enabled()
                expect(row.get_by_role("button", name="Detach", exact=True)).to_be_enabled()

            # The view may go stale between opening a lifecycle form and its final submit.
            # Refresh keeps the form/dialog open, so the operation handler must repeat the
            # owner-fresh check rather than trusting the previously enabled action.
            cached_nfs = page.locator('tr[data-storage-id="cached-nfs"]')
            cached_nfs.get_by_role("button", name="Update", exact=True).click()
            update_form = page.get_by_role("region", name="Update storage", exact=True)
            expect(update_form).to_be_visible()
            hosts[0]["connection"] = "stale"
            race_start = len(requests)
            refresh()
            expect(cached_nfs.get_by_text("Assigned Core is not reporting; showing its last report.", exact=True)).to_be_visible()
            # Refresh correctly disables the visible submit control. Submit the form directly
            # to exercise the handler's second guard for a just-staled Core.
            update_form.locator("form").evaluate("form => form.requestSubmit()")
            expect(page.get_by_role("alert")).to_contain_text(stale_reason)
            assert not any("storage/cached-nfs/update" in request for request in requests[race_start:]), requests[race_start:]
            update_form.get_by_role("button", name="Close", exact=True).click()

            hosts[0]["connection"] = "fresh"
            refresh()
            cached_smb = page.locator('tr[data-storage-id="cached-smb"]')
            cached_smb.get_by_role("button", name="Update / rotate credentials", exact=True).click()
            smb_update = page.get_by_role("region", name="Update storage", exact=True)
            smb_update.get_by_label("Account", exact=True).fill("nex-vm")
            smb_update.get_by_label("Password", exact=True).fill("not-for-display-race-secret")
            mode["credential_deferred"] = True
            # Reproduce request dispatch lag after React renders its busy state.
            # Only this fixture request is delayed; the actual UI stays unchanged.
            page.evaluate("""() => {
                const originalFetch = window.fetch;
                window.fetch = async (...args) => {
                    if (String(args[0]).endsWith('/api/v1/storage/credentials')) {
                        window.fetch = originalFetch;
                        await new Promise(resolve => setTimeout(resolve, 1000));
                    }
                    return originalFetch(...args);
                };
            }""")
            race_start = len(requests)
            smb_update.locator("form").evaluate("form => form.requestSubmit()")
            expect(smb_update.get_by_role("button", name="Submitting…", exact=True)).to_be_disabled()
            pending_credentials, _ = take_deferred(page, deferred_credentials, "SMB credential handoff")
            hosts[0]["connection"] = "stale"
            refresh()
            expect(cached_smb.get_by_text("Assigned Core is not reporting; showing its last report.", exact=True)).to_be_visible()
            pending_credentials.fulfill(json={"receipt": "core-only-race-receipt"})
            expect(page.get_by_role("alert")).to_contain_text(stale_reason)
            assert not any("storage/cached-smb/update" in request for request in requests[race_start:]), requests[race_start:]
            smb_update.get_by_role("button", name="Close", exact=True).click()
            mode["credential_deferred"] = False

            hosts[0]["connection"] = "fresh"
            refresh()
            cached_nfs = page.locator('tr[data-storage-id="cached-nfs"]')
            cached_nfs.get_by_role("button", name="Detach", exact=True).click()
            detach_dialog = page.get_by_role("dialog", name="Detach Cached NFS?", exact=True)
            expect(detach_dialog).to_be_visible()
            hosts[0]["connection"] = "stale"
            race_start = len(requests)
            # A modal correctly blocks page clicks; wait for the normal inventory poll to
            # deliver the disconnect while the confirmation remains open.
            page.wait_for_timeout(10500)
            expect(cached_nfs.get_by_text("Assigned Core is not reporting; showing its last report.", exact=True)).to_be_visible()
            detach_dialog.get_by_label("Repository name", exact=True).fill("Cached NFS")
            detach_dialog.get_by_role("button", name="Detach storage", exact=True).click()
            expect(detach_dialog).to_contain_text(stale_reason)
            expect(detach_dialog).to_be_visible()
            assert not any("storage/cached-nfs/detach" in request for request in requests[race_start:]), requests[race_start:]
            detach_dialog.get_by_role("button", name="Cancel", exact=True).click()

            hosts[0]["connection"] = "fresh"
            refresh()
            # The ISO library sits below the repositories on Storage.
            page.get_by_role("button", name="Upload ISO", exact=True).click()
            upload = page.get_by_role("region", name="Upload ISO", exact=True)
            destination = upload.get_by_role("combobox", name="Destination storage", exact=True).locator("option").all_text_contents()
            assert any("Cached NFS" in option for option in destination) and any("Cached SMB" in option for option in destination), destination
            upload.get_by_role("button", name="Close", exact=True).click()
            browser.close()
    finally:
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
