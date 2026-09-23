// SPDX-License-Identifier: AGPL-3.0-only
// The Hosts screen shows only what hosts report: usage from their own history, disk roles from
// their ZFS and md reports, and a stale host's VMs as Unknown. run with: node src/host-list.test.mjs
import { testCacheDir } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { build } from "esbuild";
import { mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

// Bundled like test-helpers' bundleTestModule, with noVNC (reached through the console tab) stubbed:
// it needs a browser DOM the moment it loads.
mkdirSync(testCacheDir, { recursive: true });
const outfile = `${testCacheDir}/host-list.test-bundle.mjs`;
await build({
  stdin: { contents: 'export * from "./host-list.tsx"; export * from "./host-panel.tsx"; export { statsHistory } from "./stats.tsx";', resolveDir: fileURLToPath(new URL(".", import.meta.url)), loader: "ts" },
  bundle: true, format: "esm", jsx: "automatic", target: "es2022", outfile, loader: { ".css": "empty" },
  external: ["react", "react/jsx-runtime", "react-dom", "react-dom/server"],
  plugins: [{ name: "stub-novnc", setup(b) {
    b.onResolve({ filter: /^@novnc\/novnc$/ }, () => ({ path: "novnc", namespace: "stub" }));
    b.onLoad({ filter: /.*/, namespace: "stub" }, () => ({ contents: "export default class RFB {}" }));
  } }],
});
const { HostList, hostStatus, coreVersion, hostUsage, hostSparkRows, sparkRows, ports, diskRoles } = await import(`file://${outfile}`);
assert.equal(hostSparkRows, sparkRows);

const s = (key, group, unit, values) => ({ key, group, label: key, unit, values });
const history = { object: "host", id: "h1", range: "10m", step_seconds: 5, start: "2026-09-23T00:00:00Z", series: [
  s("cpu.total", "cpu", "percent", [10, 20, 30]),
  s("memory.used", "memory", "bytes", [1, 2, 3]),
  s("memory.total", "memory", "bytes", [4, 4, 4]),
  s("disk.sda.read", "disk", "bytes_per_second", [100, null, 300]),
  s("disk.sda.write", "disk", "bytes_per_second", [50, null, null]),
  s("disk.sda.iops", "disk", "iops", [9, 9, 9]),
] };
assert.deepEqual(hostUsage(history), { cpu: 30, memory: 75, cpuValues: [10, 20, 30], memoryValues: [25, 50, 75] });
assert.deepEqual(hostUsage(null), { cpu: null, memory: null, cpuValues: [], memoryValues: [] }, "no history is unknown, not zero");
const [, , disk, network] = hostSparkRows(history);
assert.deepEqual(disk.values, [50, null, 100], "rates add up per sample, keep gaps as gaps and leave IOPS out");
assert.equal(disk.text, "300 B/s");
assert.equal(network.text, "—", "a resource with no series reads as unknown");

assert.deepEqual(hostStatus({ revoked: false, connection: "fresh" }), { value: "online", label: "Online" });
assert.deepEqual(hostStatus({ revoked: false, connection: "stale" }), { value: "stale", label: "Stale" });
assert.deepEqual(hostStatus({ revoked: true, connection: "fresh" }), { value: "revoked", label: "Revoked" });
assert.equal(coreVersion("Nex Core 0.4.4 (Debian 13)"), "0.4.4");
assert.equal(coreVersion("Debian 13"), "Debian 13");

const nics = [{ name: "eno1", kind: "physical", state: "up" }, { name: "br0", kind: "bridge", state: "up" }, { name: "lo", state: "up" }, { name: "eth0", state: "down" }];
assert.deepEqual(ports({ inventory: { nics: nics.slice(0, 3) } }).map(n => n.name), ["eno1"], "only physical ports when Core reports kinds");
assert.deepEqual(ports({ inventory: { nics: nics.filter(n => !n.kind) } }).map(n => n.name), ["eth0"], "an older Core without kinds leaves out only lo");

const zfs = { name: "tank", devices: [{ name: "tank", state: "DEGRADED" }, { name: "mirror-0", parent: "tank", state: "DEGRADED" }, { name: "sda", parent: "mirror-0", state: "ONLINE" }, { name: "sdb1", parent: "mirror-0", state: "FAULTED" }, { name: "sdc", parent: "tank", state: "ONLINE" }] };
const storage = [
  { kind: "zfs", host_id: "h1", access: { zfs_pool: zfs } },
  { kind: "zfs", host_id: "h1", zfs_host_access: [{ host_id: "h2", pool: zfs }] },
  { kind: "nfs", host_id: "h1" },
];
const arrays = [{ level: "raid1", members: [{ name: "nvme0n1p2", device: "/dev/nvme0n1p2", state: "in_sync" }, { name: "nvme1n1p2", device: "/dev/nvme1n1p2", state: "faulty" }] }];
assert.deepEqual(diskRoles("h1", storage, arrays, ["nvme0n1", "nvme1n1", "sda", "sdb", "sdc", "sdd"]), {
  sda: [{ text: "mirror", failed: false }],
  sdb: [{ text: "mirror · faulted", failed: true }],
  sdc: [{ text: "stripe", failed: false }],
  nvme0n1: [{ text: "system mirror", failed: false }],
  nvme1n1: [{ text: "system mirror", failed: true }],
}, "roles come from the host's own reports; sdd is used for nothing reported");
assert.deepEqual(diskRoles("h3", storage, [], ["sda"]), {}, "another host's pool gives this host's disks no role");

const inventory = (name) => ({ observed_at: "2026-09-23T00:00:00Z", hostname: name, cpu_model: "Example CPU", cpu_count: 8, memory_bytes: 8 * 1024 ** 3, virtualization: ["kvm"], os_version: "Nex Core 0.4.4 (Debian 13)", agent_version: "0.4.4",
  nics: [{ name: "eno1", mac: "02:00:5e:00:53:01", kind: "physical", master: "br0", addresses: [], state: "up" }, { name: "br0", mac: "02:00:5e:00:53:01", kind: "bridge", addresses: ["192.0.2.11/24"], state: "up" }],
  disks: [{ name: "sda", size_bytes: 1024 ** 4 }], checks: [] });
const hosts = [
  { id: "h1", pool_id: "p1", hostname: "node-1", revoked: false, connection: "fresh", last_seen: "2026-09-23T00:00:00Z", inventory: inventory("node-1") },
  { id: "h2", pool_id: "p1", hostname: "node-2", revoked: false, connection: "stale", last_seen: "2026-09-23T00:00:00Z", inventory: inventory("node-2") },
];
const data = { sites: [], pools: [{ id: "p1", name: "pool-1", site_id: "s1", master_host_id: "h1" }], hosts, operations: [], audit: [], storage: [], networks: [], media: [],
  vms: [{ id: "v1", name: "app-01", host_id: "h2", pool_id: "p1", state: "shut off", freshness: "stale", spec: { cpu: { count: 2 }, memory: { size_mib: 2048 } } }] };
const render = (selectedID, unavailable = false) => renderToStaticMarkup(createElement(HostList, { data, hosts, selectedID, api: async () => ({}), busy: false, unavailable, loading: false, coreAvailable: "0.4.5",
  openHostConsole: async () => "", onRefreshInventory() {}, onReenroll() {}, onRevoke() {}, onRetry() {} }));

const table = render();
assert.match(table, /192\.0\.2\.11</, "the IP address is shown without its prefix length");
assert.match(table, /0\.4\.4 <span class="badge warn">Update</, "a host behind the available Core is marked");
assert.match(table, /badge warn">Stale</, "a stale host says so in words");
assert.ok(!/\d+%/.test(table), "no usage is shown before a host has reported any");

const stale = render("h2");
assert.match(stale, /VMs on this host are shown as Unknown, not stopped/);
assert.match(stale, /app-01<\/a><\/td><td class="muted">Unknown</, "a VM on a stale host is Unknown, never Stopped");
assert.match(stale, /Member of pool-1/);
assert.match(stale, /<dt>Uptime<\/dt><dd>—/, "uptime is not reported, so it is a dash");
assert.match(stale, /title="eno1 · 02:00:5e:00:53:01 · link up">eno1</, "a port without a reported speed shows its name, never a guessed speed");
hosts[0].inventory.uptime_seconds = 2 * 86400 + 3 * 3600;
hosts[0].inventory.observed_at = new Date().toISOString();
hosts[0].inventory.nics[0].speed_mbps = 25000;
const fresh = render("h1");
assert.match(fresh, /<dt>Uptime<\/dt><dd>2d 3h/, "reported uptime is shown");
assert.match(fresh, /host-port fast"[^>]*>25G</, "a reported 25 GbE link is labelled with its speed");
assert.match(stale, /disabled="" title="The host is not freshly connected/, "the console stays visible and says why it is disabled");
assert.ok(!/Fence it or wait/.test(render("h1")), "a fresh host has no stale explanation");
assert.match(render("gone", true), /Host unavailable/, "a link the failed inventory cannot resolve says so");
assert.match(render("h1", true), /<h2>[^]*?badge neutral">Unknown</, "a failing inventory never reads as Online");
assert.equal(coreVersion(undefined), "Not reported", "an inventory without os_version does not break the screen");
const { statsHistory } = await import(`file://${outfile}`);
assert.deepEqual(hostUsage(statsHistory({ id: "h1" })), { cpu: null, memory: null, cpuValues: [], memoryValues: [] }, "a stats reply without series reads as no data");
console.log("host-list ok");
