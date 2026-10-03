// SPDX-License-Identifier: AGPL-3.0-only
// The VMs screen: honest stale status, views, search, saved columns, bulk skips, and bounded
// flat-list row construction with complete tag grouping. Run with: node src/vm-list.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { VMList, statusOf, needsAttention, views, matches, hiddenColumns, bulkSkip, bulkSnapSkip } = await import(bundleTestModule(new URL("./vm-list.tsx", import.meta.url), "vm-list", true));

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

// Reading memory happens while constructing a row, even if that JSX is later discarded.
// Both untagged and multi-tag inventories must construct only the displayed virtual window.
const rowIDs = markup => [...markup.matchAll(/<tr\b[^>]*data-vm="([^"]+)"/g)].map(match => match[1]);
for (const tags of [[], ["web", "blue"]]) {
  let constructions = 0;
  const many = Array.from({ length: 1000 }, (_, i) => vm(`vm-${i}`, {
    spec: { ...spec, tags, memory: { mode: "dedicated", get size_mib() { constructions++; return 2048; } } },
  }));
  const virtualList = renderToStaticMarkup(createElement(VMList, { ...props, vms: many, data: { ...data, vms: many, hosts: [host] } }));
  const visible = rowIDs(virtualList);
  assert.match(virtualList, /class="virtual-pad"/, "large flat lists preserve space for offscreen VMs");
  assert.ok(visible.length > 0 && visible.length < many.length / 10, "the visible window is bounded independently of inventory size");
  assert.deepEqual(visible, many.slice(0, visible.length).map(v => v.id), "the window starts with consecutive inventory rows");
  assert.equal(constructions, visible.length, `flat ${tags.length ? "multi-tag" : "untagged"} inventory must not construct offscreen rows`);
}

// Exercise the actual group and search handlers; React's server renderer applies their state
// updates before producing the final markup, without a DOM or replacing component source.
const descendants = node => Array.isArray(node) ? node.flatMap(descendants) : node?.props ? [node, ...descendants(node.props.children)] : [];
function TagGroupedList({ listProps, query }) {
  const tree = VMList(listProps);
  const elements = descendants(tree);
  const tag = elements.find(element => element.type === "button" && element.props.children === "Tag");
  assert.ok(tag, "the Tag grouping action is available");
  if (!tag.props["aria-pressed"]) tag.props.onClick();
  const search = elements.find(element => element.type === "input" && element.props.type === "search");
  if (search.props.value !== query) search.props.onChange({ target: { value: query } });
  return tree;
}
const groupedVMs = Array.from({ length: 1000 }, (_, i) => vm(`matching-${i}`, {
  spec: { ...spec, tags: i % 3 === 0 ? [] : i % 3 === 1 ? ["web"] : ["web", "blue"] },
}));
const inventory = [...groupedVMs, vm("outside", { spec: { ...spec, tags: ["unmatched"] } })];
const grouped = renderToStaticMarkup(createElement(TagGroupedList, {
  listProps: { ...props, vms: inventory, data: { ...data, vms: inventory, hosts: [host] } }, query: "matching",
}));
const tbody = grouped.match(/<tbody>([\s\S]*?)<\/tbody>/)[1];
const groups = [...tbody.matchAll(/<tr class="group">([\s\S]*?)<\/tr>([\s\S]*?)(?=<tr class="group">|$)/g)];
assert.equal(groups.length, 3, "matching VMs appear in both tag groups and the untagged group");
for (const [index, name] of ["blue", "web", "No tags"].entries()) {
  const members = groupedVMs.filter(v => name === "No tags" ? !v.spec.tags.length : v.spec.tags.includes(name));
  assert.ok(groups[index][1].includes(name), "tag groups stay sorted with untagged VMs last");
  assert.deepEqual(rowIDs(groups[index][2]), members.map(v => v.id), `${name} includes every matching VM, including rows outside the flat virtual window`);
  assert.ok(groups[index][1].includes(`(${members.length})`), "each header reports its full matching count");
}
assert.doesNotMatch(tbody, /data-vm="outside"|unmatched|class="virtual-pad"/, "grouping honors search and does not virtualize its tree");
assert.equal(groups[1][1].match(/--tag-hue:(\d+)/)[1], list.match(/--tag-hue:(\d+)/)[1], "group headers preserve the tag's row colour");

console.log("vm-list ok");
