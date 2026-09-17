// SPDX-License-Identifier: AGPL-3.0-only
// Usage history helpers: stacking keeps gaps as gaps, totals leave a stack, units scale.
// run with: node src/stats.test.mjs
import { buildSync } from "esbuild";
import { mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";

const outdir = fileURLToPath(new URL("../node_modules/.cache/nex", import.meta.url));
mkdirSync(outdir, { recursive: true });
const outfile = `${outdir}/stats.test-bundle.mjs`;
buildSync({
  entryPoints: [fileURLToPath(new URL("./stats.tsx", import.meta.url))],
  bundle: true, format: "esm", jsx: "automatic", target: "es2022", outfile,
  external: ["react", "react/jsx-runtime"], loader: { ".css": "empty" },
});
const { formatValue, formatTicks, byteIncrs, enoughLiveSamples, updatedAgo, REALTIME_LIMIT_MS, isolatedIndices, charts, plotValues, stackBands, timestamps, statsFailure } = await import(`file://${outfile}`);

const s = (key, group, unit, values) => ({ key, group, label: key, unit, values });
const history = { object: "vm", id: "vm-1", range: "10m", step_seconds: 5, start: "2026-09-16T00:00:00Z", series: [
  s("cpu.total", "cpu", "percent", [10, null, 30]),
  s("cpu.vcpu.0", "cpu", "percent", [4, null, 10]),
  s("cpu.vcpu.1", "cpu", "percent", [6, 2, 20]),
  s("memory.assigned", "memory", "bytes", [1, 1, 1]),
  s("memory.host", "memory", "bytes", [1, 1, 1]),
] };

assert.deepEqual(timestamps(history), [1789516800, 1789516805, 1789516810]);
const stacked = charts(history, true);
assert.deepEqual(stacked.map(c => c.group), ["cpu", "memory", "network", "disk"]);
assert.deepEqual(stacked[0].series.map(x => x.key), ["cpu.vcpu.0", "cpu.vcpu.1"], "a total leaves the stack");
assert.equal(stacked[1].stacked, false, "memory never stacks");
assert.equal(charts(history, false)[0].series.length, 3, "unstacked keeps the total");
assert.equal(charts({ ...history, object: "host" }, false).at(-1).group, "load");
assert.deepEqual(plotValues(stacked[0].series, true), [[2, null, 5], [5, 1, 15]], "per-vCPU percent is weighted 1/N so the stack top is total CPU; unknown is a gap, not zero");
// UH-8: a series hidden from the legend leaves the running sum, so the visible bands sit on
// each other; it collapses onto the band below instead of vanishing, keeping the bands aligned.
assert.deepEqual(plotValues(stacked[0].series, true, new Set(["cpu.vcpu.0"])), [[0, null, 0], [3, 1, 10]], "hidden vCPU 0 no longer offsets vCPU 1");
assert.deepEqual(plotValues(stacked[0].series, false, new Set(["cpu.vcpu.0"])), [[4, null, 10], [6, 2, 20]], "unstacked ignores hidden");
assert.deepEqual(plotValues(stacked[0].series, false), [[4, null, 10], [6, 2, 20]], "unstacked values stay raw");
// UH-8 bands: uPlot fills nothing for a band whose lower edge is hidden, so each visible series
// bands onto the nearest visible series below it of the same unit.
const three = [s("v0", "cpu", "percent", [1]), s("v1", "cpu", "percent", [1]), s("v2", "cpu", "percent", [1])];
assert.deepEqual(stackBands(three, true), [{ series: [2, 1] }, { series: [3, 2] }]);
assert.deepEqual(stackBands(three, true, new Set(["v1"])), [{ series: [3, 1] }], "hidden middle: vCPU 2 bands onto vCPU 0");
assert.deepEqual(stackBands(three, true, new Set(["v0"])), [{ series: [3, 2] }], "hidden bottom: vCPU 1 fills to zero");
assert.equal(stackBands(three, false), undefined);
assert.deepEqual(stackBands([s("a", "net", "bytes_per_second", [1]), s("b", "net", "iops", [5])], true), [], "units never band together");
assert.deepEqual(plotValues([s("a", "net", "bytes_per_second", [1]), s("b", "net", "iops", [5])], true), [[1], [5]], "units stack separately");

assert.equal(formatValue("percent", 3.25), "3.3%");
assert.equal(formatValue("bytes", 1536), "1.5 KiB");
assert.equal(formatValue("bytes_per_second", 3 * 1024 ** 3), "3.0 GiB/s");
assert.equal(formatValue("bytes", 512), "512 B");
assert.equal(formatValue("iops", 120), "120 IOPS");
assert.equal(formatValue("load", null), "—");
const unique = labels => new Set(labels).size === labels.length;
const tiny = formatTicks("bytes_per_second", [0, 0.5, 1, 1.5], 0.5);
assert.deepEqual(tiny, ["0.0 B/s", "0.5 B/s", "1.0 B/s", "1.5 B/s"], "near-zero rate ticks stay distinct");
assert.deepEqual(formatTicks("bytes_per_second", [0, 512 * 1024, 1024 ** 2, 1.5 * 1024 ** 2], 512 * 1024), ["0.0 MiB/s", "0.5 MiB/s", "1.0 MiB/s", "1.5 MiB/s"]);
assert.deepEqual(formatTicks("bytes", [0, 2 * 1024 ** 3, 4 * 1024 ** 3], 2 * 1024 ** 3), ["0 GiB", "2 GiB", "4 GiB"]);
assert.ok(unique(formatTicks("percent", [0, 0.2, 0.4, 0.6], 0.2)), "small percent steps stay distinct");
assert.deepEqual(formatTicks("bytes_per_second", [0, 0.25, 0.5, 0.75, 1], 0.25), ["0.00 B/s", "0.25 B/s", "0.50 B/s", "0.75 B/s", "1.00 B/s"], "quarter steps are exact, not rounded to repeats");
assert.deepEqual(formatTicks("iops", [0, 100, 200], 100), ["0 IOPS", "100 IOPS", "200 IOPS"]);
for (const incr of byteIncrs.filter(i => i >= 1024)) {
  const ticks = [0, incr, 2 * incr, 3 * incr];
  const labels = formatTicks("bytes", ticks, incr);
  assert.ok(unique(labels) && labels.every(l => /^\d+(\.\d{1,2})? /.test(l)), `byte step \${incr} gives short exact labels: \${labels}`);
}
assert.deepEqual(isolatedIndices([null, 3, null, 4, 5, null, 6]), [1, 6], "a lone sample is drawn, a joined one is a line");
assert.equal(isolatedIndices([1, 2, null]), null);
const live = values => ({ ...history, range: "live", step_seconds: 1, series: [s("cpu.total", "cpu", "percent", values)] });
assert.equal(enoughLiveSamples(null), false);
assert.equal(enoughLiveSamples(live([null, null, 5])), false, "one known sample is still collecting");
assert.equal(enoughLiveSamples(live([null, 4, null, 5])), true, "two known samples draw");
assert.equal(updatedAgo(10_000, 10_400), "Updated just now");
assert.equal(updatedAgo(10_000, 13_000), "Updated 3s ago");
assert.equal(REALTIME_LIMIT_MS, 600_000, "realtime turns off after 10 minutes");
const failure = (message, status) => Object.assign(new Error(message), { status });
assert.deepEqual(statsFailure(failure("the host agent does not keep usage history; upgrade Nex Core", 409)), { kind: "capability", message: "the host agent does not keep usage history; upgrade Nex Core" }, "an older Core gets the upgrade state, not a retry error");
assert.deepEqual(statsFailure(failure("the host is not freshly connected; its usage history cannot be read now", 409)), { kind: "stale", message: "the host is not freshly connected; its usage history cannot be read now" }, "a stale host keeps its distinct state so existing charts can be labelled stale");
assert.equal(statsFailure(failure("the host did not answer", 504)).kind, "error");
console.log("stats helpers ok");
