// SPDX-License-Identifier: AGPL-3.0-only
// The VMs screen: honest status for VMs on stale hosts, views, search, saved columns, bulk power
// skips, and the panel's tag editor gate. run with: node src/vm-list.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import { readFileSync } from "node:fs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { VMList, statusOf, needsAttention, views, matches, hiddenColumns, bulkSkip, bulkSnapSkip } = await import(bundleTestModule(new URL("./vm-list.tsx", import.meta.url), "vm-list", true));
assert.match(readFileSync(new URL("./vm-list.tsx", import.meta.url), "utf8"), /request submitted for \$\{total\} VM/);
assert.match(readFileSync(new URL("./vm-list.tsx", import.meta.url), "utf8"), /setBulkSnapIDs/);

const host = { id: "h1", pool_id: "p1", hostname: "node-1", connection: "fresh", revoked: false, capabilities: ["vm-power-v1", "managed-vm-v1", "vm-update-v1", "console-v1"], inventory: null };
const stale = { ...host, id: "h2", hostname: "node-2", connection: "stale" };
const spec = { description: "", guest_os: "linux", cpu: { count: 2, sockets: 0, cores: 0, threads: 0, model: "host-passthrough" }, memory: { mode: "dedicated", size_mib: 2048 }, disks: [], nics: [], boot: { firmware: "bios", machine: "q35", media_boot_order: 0 }, media_id: "", autostart: false, start_after_create: false };
const vm = (id, over) => ({ id, name: id, pool_id: "p1", host_id: "h1", state: "running", freshness: "fresh", status: "succeeded", protected: false, spec, ...over });
const web = vm("web", { spec: { ...spec, tags: ["web"] }, guest: { agent: "connected", clipboard: "ready", resize: false, addresses: [{ interface: "eth0", address: "192.0.2.10" }] } });
const off = vm("off", { state: "shut off" });
const lost = vm("lost", { host_id: "h2", state: "unknown", freshness: "stale" });

// A VM on a stale host is Unknown and needs attention; it is never Stopped.
assert.equal(statusOf(lost), "Unknown (host stale)");
assert.equal(statusOf(off), "Stopped");
assert.ok(needsAttention(lost) && !needsAttention(web) && !needsAttention(off));
assert.ok(needsAttention(vm("x", { status: "failed" })), "a failed creation needs attention");
const count = name => [web, off, lost].filter(views.find(([n]) => n === name)[1]).length;
assert.deepEqual(["All", "Running", "Needs attention", "Stopped"].map(count), [3, 1, 1, 1]);

assert.ok(matches(web, "192.0.2") && matches(web, "WEB") && !matches(off, "web"), "search covers name, IP and tag");
assert.deepEqual(hiddenColumns('["Tags","Nope"]'), ["Tags"]);
assert.deepEqual(hiddenColumns("not json"), [], "an unreadable preference shows every column");

assert.equal(bulkSkip(off, "start", host), "");
assert.match(bulkSkip(web, "start", host), /observed running/);
assert.match(bulkSkip(lost, "shutdown", stale), /freshly connected host/);
assert.match(bulkSkip(vm("ctl", { protected: true }), "shutdown", host), /Controller appliance/);

assert.equal(bulkSnapSkip(off, host), "");
assert.match(bulkSnapSkip(web, host), /stop it first/);
assert.match(bulkSnapSkip(lost, stale), /freshly connected/);
assert.match(bulkSnapSkip(vm("ctl", { protected: true }), host), /Controller appliance/);
assert.match(bulkSnapSkip(vm("stale-vm", { state: "shut off", freshness: "stale" }), host), /current VM state is unknown/);
const snapSrc = readFileSync(new URL("./vm-list.tsx", import.meta.url), "utf8");
assert.match(snapSrc, /requests\.current = \{\}/);
assert.match(snapSrc, /requests\.current\[vm\.id\] \?\?= snapRequestID\(\)/, "bulk snapshot reuses one request_id per VM until success");
assert.match(snapSrc, /delete requests\.current\[vm\.id\]/, "bulk snapshot drops the held request_id only after success");
assert.match(snapSrc, /sentAny\.current = true/, "bulk snapshot records success outside React state before close");
assert.match(snapSrc, /onClose=\{\(\) => onDone\(sentAny\.current\)\}/, "bulk snapshot onDone does not close over a stale sent array");
assert.match(snapSrc, /const nameLocked = sent\.length > 0 \|\| working/, "name stays editable when every request failed");
assert.doesNotMatch(snapSrc, /nameLocked = sent\.length > 0 \|\| Object\.keys\(errors\)/, "errors alone must not lock the snapshot name");

const panelSnapSrc = readFileSync(new URL("./vm-snapshots.tsx", import.meta.url), "utf8");
assert.match(panelSnapSrc, /requests\.current\[key\] \?\?= rid\(prefix\)/, "panel snapshot actions reuse request_id until success");
assert.match(panelSnapSrc, /if \(vm\.protected\) return "Controller appliance/, "protected appliance VMs block snapshot actions");
assert.match(panelSnapSrc, /confirmError/, "confirm dialog keeps its own error, not the create form error");
assert.match(panelSnapSrc, /loadGen\.current/, "snapshot list load ignores stale responses after a VM switch");
assert.match(panelSnapSrc, /if \(ok\) \{\s*setName\(""\);\s*setDescription\(""\);/, "create form clears only after a successful request");
assert.match(panelSnapSrc, /if \(ok\) setEditID\(""\)/, "rename exits edit only after success");

const data = { sites: [], pools: [{ id: "p1", name: "pool-1", site_id: "s", master_host_id: "h1" }], hosts: [host, stale], operations: [], audit: [], vms: [web, off, lost], storage: [], networks: [], media: [] };
const props = { data, vms: data.vms, pool: data.pools[0], api: async () => ({}), reload: async () => {}, busy: false, unavailable: false, loading: false, onNotice() {}, onCreateVM() {},
  mediaControls: () => ({ drives: [], options: [] }), installMedia: () => ({ busy: false, note: "", onEject() {} }) };
const list = renderToStaticMarkup(createElement(VMList, props));
assert.match(list, /<span class="badge warn">Unknown<\/span>/, "the stale VM row says Unknown");
assert.match(list, /Needs attention<span class="count warn">1<\/span>/);
assert.match(list, /<span class="tag" style="--tag-hue:\d+">web<\/span>/);
assert.match(list, /Tags<svg/, "the Tags facet is offered");

// The panel keeps phase-gated actions visible and disabled, and a stale host's VM says why.
const panel = renderToStaticMarkup(createElement(VMList, { ...props, selectedID: "web" }));
assert.match(panel, /disabled="" title="Available with live migration \(Phase 7\)"/);
assert.match(panel, /Last backup<\/dt><dd class="muted">Loading…<\/dd><dt>Last tested restore/);
const stalePanel = renderToStaticMarkup(createElement(VMList, { ...props, selectedID: "lost" }));
assert.match(stalePanel, /class="why" role="status">node-2 is not reporting\. Current execution state is unknown/);
assert.match(stalePanel, /Console<\/button>/);
assert.match(stalePanel, /<button type="button" class="sm primary" disabled=""/, "no console for a VM on a stale host");
// While the inventory cannot load, no VM reads as Running and its console and power say why.
const failing = renderToStaticMarkup(createElement(VMList, { ...props, unavailable: true, selectedID: "web" }));
assert.equal((failing.match(/badge warn">Unknown</g) || []).length, 4, "three rows and the panel badge are Unknown");
assert.match(failing, /class="sm primary" disabled="" title="Controller inventory could not be loaded/);
assert.match(failing, /class="sm tool-start" title="Controller inventory could not be loaded/);

// Above 40 rows the ungrouped table virtualizes: spacer pads and only a window of rows.
const many = Array.from({ length: 50 }, (_, i) => vm(`vm-${i}`));
const virt = renderToStaticMarkup(createElement(VMList, { ...props, vms: many, data: { ...data, vms: many, hosts: [host] } }));
assert.match(virt, /class="virtual-pad"/, "large lists use virtual spacer rows");
assert.ok((virt.match(/<tr>/g) || []).length < 60, "not every VM row is in the markup");

// Group by offers None, Host and Tag; the same tag always gets the same colour.
const markup = renderToStaticMarkup(createElement(VMList, { ...props, vms: [web], data: { ...data, vms: [web], hosts: [host] } }));
assert.match(markup, /aria-label="Group by"[\s\S]*>None<[\s\S]*>Host<[\s\S]*>Tag</);
assert.equal(markup.match(/--tag-hue:(\d+)/)[1], renderToStaticMarkup(createElement(VMList, { ...props, vms: [web], data: { ...data, vms: [web], hosts: [host] } })).match(/--tag-hue:(\d+)/)[1]);

console.log("vm-list ok");
