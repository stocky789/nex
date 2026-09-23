// SPDX-License-Identifier: AGPL-3.0-only
// The Overview's pool lines add the hosts' own stats series step by step; run with: node src/overview.test.mjs
import assert from "node:assert/strict";
import { build } from "esbuild";
import { fileURLToPath } from "node:url";
import { testCacheDir } from "./test-helpers.mjs";

// overview.tsx reaches the console screens through the host list; their VNC client needs a real
// browser at load, so it is stubbed like console.test.mjs does. Stylesheets are left out.
const outfile = `${testCacheDir}/overview.test-bundle.mjs`;
await build({
  entryPoints: [fileURLToPath(new URL("./overview.tsx", import.meta.url))], outfile,
  bundle: true, format: "esm", jsx: "automatic", target: "es2022", loader: { ".css": "empty" },
  external: ["react", "react/jsx-runtime", "react-dom", "react-dom/server"],
  plugins: [{ name: "stub-vnc", setup(b) { b.onResolve({ filter: /^@novnc\/novnc$/ }, () => ({ path: "rfb", namespace: "stub" })); b.onLoad({ filter: /.*/, namespace: "stub" }, () => ({ contents: "export default class RFB {}" })); } }],
});
const { poolUsage, sampleCounts, storageSeries, hostParts, vmParts, trend, monotone, runs, bucket, loadCollapsed, saveCollapsed } = await import(`file://${outfile}`);

const start = "2026-09-23T10:00:00Z";
const history = (id, cpu, used, total, disk) => ({ object: "host", id, range: "24h", step_seconds: 60, start, series: [
  { key: "cpu.total", group: "cpu", label: "CPU", unit: "percent", values: cpu },
  { key: "memory.used", group: "memory", label: "Used", unit: "bytes", values: used },
  { key: "memory.total", group: "memory", label: "Total", unit: "bytes", values: total },
  { key: "disk.sda.read_bytes", group: "disk", label: "sda read", unit: "bytes_per_second", values: disk },
  { key: "disk.sda.read_iops", group: "disk", label: "sda read IOPS", unit: "iops", values: [999, 999, 999] },
] });
const hosts = [{ id: "a", connection: "fresh", inventory: { cpu_count: 30 } }, { id: "b", connection: "fresh", inventory: { cpu_count: 10 } }];
const usage = poolUsage([
  history("a", [10, 20, 30], [10, 20, null], [100, 100, 100], [1, 2, 3]),
  // A stale host stopped reporting: its unknown points leave the sums instead of counting as zero.
  history("b", [50, null, null], [50, 60, 70], [100, 100, 100], [4, null, 6]),
], hosts, 180);

assert.deepEqual(usage.cpu, [(10 * 30 + 50 * 10) / 40, 20, 30], "CPU is weighted by each host's CPU count");
assert.deepEqual(usage.memory, [30, 40, 70], "memory is summed used over summed total");
assert.deepEqual(usage.io, [5, 2, 9], "storage I/O sums byte rates and ignores IOPS");
assert.deepEqual(usage.times, [0, 1, 2].map(i => Date.parse(start) + i * 60000));
assert.deepEqual([usage.memoryUsed, usage.memoryTotal], [90, 200], "the memory KPI uses each host's last known reading");
assert.deepEqual(usage.hostCPU, { a: 30, b: 50 });
const staleB = poolUsage([history("a", [10], [10], [100], [1]), history("b", [50], [50], [100], [4])], [hosts[0], { ...hosts[1], connection: "stale" }], 60);
assert.deepEqual([staleB.memoryUsed, staleB.memoryTotal], [10, 100], "a host that is not reporting is left out of the memory KPI");
assert.deepEqual(poolUsage([history("a", [1, 2, 3], [1, 1, 1], [2, 2, 2], [0, 0, 0])], hosts, 120).cpu, [2, 3], "only the last points are kept");
assert.deepEqual(poolUsage([], hosts, 60).cpu, [], "no history draws nothing");
// Storage use is sampled per pool on each reload, so any scope can sum it.
const samples = [];
const pools = [{ id: "p1" }, { id: "p2" }];
sampleCounts({ pools, storage: [{ pool_id: "p1", access: { total_bytes: 100, available_bytes: 75 } }, { pool_id: "p1", tombstone: true, access: { total_bytes: 100, available_bytes: 0 } }] }, samples);
sampleCounts({ pools, storage: [{ pool_id: "p1", access: { total_bytes: 100, available_bytes: 50 } }, { pool_id: "p2", access: { total_bytes: 300, available_bytes: 0 } }] }, samples);
assert.deepEqual(storageSeries(["p1"], samples), [25, 50], "storage used skips removed storage");
assert.deepEqual(storageSeries(["p1", "p2"], samples), [25, 87.5], "a scope sums its pools");
assert.deepEqual(storageSeries(["p3"], samples), [], "a pool with no samples draws nothing");
for (let i = 0; i < 40; i++) sampleCounts({ pools, storage: [] }, samples);
assert.equal(samples.length, 30, "only the last 30 reloads are kept");

// The Hosts bar has one segment per host; the VM bar one per state, sized by its count.
assert.deepEqual(hostParts([{ hostname: "a", connection: "fresh" }, { hostname: "b", connection: "stale" }, { hostname: "c", connection: "fresh", revoked: true }, { hostname: "d", connection: "" }])
  .map(p => [p.title, p.tone, p.count]), [["a: Online", "ok", 1], ["b: Stale", "warn", 1], ["c: Revoked", "bad", 1], ["d: Unknown", "unknown", 1]]);
const vm = (state, known = true) => ({ state, known });
const parts = vmParts([vm("shut off"), vm("running"), vm("running", false), vm("paused"), vm("running"), vm("running"), vm("stopped")], v => v.known);
assert.deepEqual(parts.map(p => [p.label, p.tone, p.count]), [["Running", "ok", 3], ["Paused", "warn", 1], ["Stopped", "neutral", 2], ["Unknown", "unknown", 1]],
  "running first, a VM on a stale host is Unknown, never Stopped");
assert.deepEqual(parts.map(p => p.count / 7), [3 / 7, 1 / 7, 2 / 7, 1 / 7], "each state's share of the bar is its share of the VMs");

// KPI change labels say how far the window moved.
assert.equal(trend([40, null, 43, 50]).change, "▲ 10%");
assert.equal(trend([50, 51, 50]).change, "", "no change reads as steady");
assert.equal(trend([30, 28.4]).change, "▼ 2%");
assert.equal(trend([null, 30]), null, "one point has no change");

// The chart's curves: every Bézier control point stays between its segment's end values, so a
// line through 0 and 100 never bends past either, and a flat stretch stays flat.
const spiky = [[0, 100], [10, 0], [20, 100], [30, 100], [40, 50], [50, 0], [60, 0], [70, 30]];
const numbers = monotone(spiky).match(/-?\d+\.\d+/g).map(Number);
assert.deepEqual(numbers.slice(0, 2), [0, 100], "the curve starts at the first point");
for (let k = 0; k < spiky.length - 1; k++) {
  const [c1, c2, end] = [numbers.slice(2 + 6 * k, 4 + 6 * k), numbers.slice(4 + 6 * k, 6 + 6 * k), numbers.slice(6 + 6 * k, 8 + 6 * k)];
  const [lo, hi] = [Math.min(spiky[k][1], spiky[k + 1][1]), Math.max(spiky[k][1], spiky[k + 1][1])];
  assert.deepEqual(end, spiky[k + 1], `segment ${k} ends at its point`);
  for (const [, cy] of [c1, c2]) assert.ok(cy >= lo && cy <= hi, `segment ${k} control ${cy} stays within ${lo}–${hi}`);
}
assert.equal(monotone([[0, 50], [50, 50], [100, 50]]), "M0.00,50.00C16.67,50.00 33.33,50.00 50.00,50.00C66.67,50.00 83.33,50.00 100.00,50.00", "a flat line stays flat");
assert.deepEqual(runs([10, 20, null, null, 30, 40]).map(r => r.length), [2, 2], "a gap splits the line into separate runs");
assert.deepEqual(runs([null, 150, -5]), [[[50, 0], [100, 100]]], "values are clamped to 0–100 and placed by index");

// Week and month ranges average steps into buckets, counted back from the newest.
const b = bucket([1, 2, 3, 4, 5, 6, 7], [[10, 20, 30, null, 50, null, null], [1, 1, 1, 1, 1, 1, 1]], 3);
assert.deepEqual(b.times, [1, 4, 7], "each bucket is stamped with its last step");
assert.deepEqual(b.starts, [1, 2, 5]);
assert.deepEqual(b.series[0], [10, 25, 50], "a bucket averages only its known samples");
assert.deepEqual(b.counts, [1, 3, 3], "the count is the most samples any series had");
assert.deepEqual(bucket([1, 2], [[null, null]], 2).series[0], [null], "an empty bucket stays a gap");

// Collapsed tiles are remembered per browser; a broken or blocked store shows every tile open.
const store = new Map();
const fake = { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
assert.deepEqual(loadCollapsed(fake), [], "nothing collapsed by default");
saveCollapsed(["placement", "tasks"], fake);
assert.deepEqual(loadCollapsed(fake), ["placement", "tasks"]);
store.set("nex.overview.collapsed", "{not json");
assert.deepEqual(loadCollapsed(fake), [], "a corrupt value is ignored");
store.set("nex.overview.collapsed", '["chart", 3]');
assert.deepEqual(loadCollapsed(fake), ["chart"], "only tile names are kept");
const blocked = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("blocked"); } };
assert.deepEqual(loadCollapsed(blocked), []);
saveCollapsed(["chart"], blocked);
console.log("overview ok");
