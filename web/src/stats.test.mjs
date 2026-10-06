// SPDX-License-Identifier: AGPL-3.0-only
// Usage history helpers: stacking keeps gaps as gaps, totals leave a stack, units scale.
// run with: node src/stats.test.mjs
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const moduleURL = bundleTestModule(new URL("./stats.tsx", import.meta.url), "stats", true);
const { formatValue, formatTicks, byteIncrs, enoughLiveSamples, updatedAgo, REALTIME_LIMIT_MS, isolatedIndices, charts, plotValues, stackBands, stackedPercentMax, timestamps, statsFailure, lastValue, gaugeState, gauges, gaugeValueText, hardwareSummary, sectionNote, sensorThresholds, mdArraysView, systemDiskReplaceSupported } = await import(moduleURL);

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
assert.equal(charts({ ...history, object: "host" }, false).map(c => c.group).includes("load"), true);
assert.deepEqual(plotValues(stacked[0].series, true), [[4, null, 10], [10, 2, 30]], "per-vCPU percent stacks raw so N busy vCPUs read N×100%; unknown is a gap, not zero");
assert.equal(stackedPercentMax(stacked[0].series), 200, "two vCPUs give a 200% axis");
assert.equal(stackedPercentMax(stacked[0].series, new Set(["cpu.vcpu.0"])), 100, "a hidden vCPU leaves the axis");
assert.equal(stackedPercentMax([s("a", "net", "bytes_per_second", [1])]), 100, "never a zero-height axis");
// UH-8: a series hidden from the legend leaves the running sum, so the visible bands sit on
// each other; it collapses onto the band below instead of vanishing, keeping the bands aligned.
assert.deepEqual(plotValues(stacked[0].series, true, new Set(["cpu.vcpu.0"])), [[0, null, 0], [6, 2, 20]], "hidden vCPU 0 no longer offsets vCPU 1");
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
assert.equal(formatValue("network_rate", 125_000), "1.0 Mbps");
assert.equal(formatValue("network_rate", 125_000_000), "1.0 Gbps");
assert.equal(formatValue("bytes", 512), "512 B");
assert.equal(formatValue("iops", 120), "120 IOPS");
assert.equal(formatValue("load", null), "—");
const unique = labels => new Set(labels).size === labels.length;
const tiny = formatTicks("bytes_per_second", [0, 0.5, 1, 1.5], 0.5);
assert.deepEqual(tiny, ["0.0 B/s", "0.5 B/s", "1.0 B/s", "1.5 B/s"], "near-zero rate ticks stay distinct");
assert.deepEqual(formatTicks("bytes_per_second", [0, 512 * 1024, 1024 ** 2, 1.5 * 1024 ** 2], 512 * 1024), ["0.0 MiB/s", "0.5 MiB/s", "1.0 MiB/s", "1.5 MiB/s"]);
assert.deepEqual(formatTicks("network_rate", [0, 62_500, 125_000], 62_500), ["0.0 Mbps", "0.5 Mbps", "1.0 Mbps"]);
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
assert.deepEqual(statsFailure(failure("the host agent does not keep usage history; upgrade Nex Core", 409)), { kind: "capability", message: "the host agent does not keep usage history; upgrade Nex Core" }, "a Core without usage history gets the upgrade state, not a retry error");
assert.deepEqual(statsFailure(failure("the host is not freshly connected; its usage history cannot be read now", 409)), { kind: "stale", message: "the host is not freshly connected; its usage history cannot be read now" }, "a stale host keeps its distinct state so existing charts can be labelled stale");
assert.equal(statsFailure(failure("the host did not answer", 504)).kind, "error");

// Temperatures: a host-only sixth chart that never stacks, and gauges read from the same history.
assert.equal(formatValue("celsius", 47.4), "47 °C");
assert.equal(formatValue("celsius", null), "—");
assert.deepEqual(formatTicks("celsius", [0, 40, 80], 40), ["0 °C", "40 °C", "80 °C"]);
const host = { ...history, object: "host", series: [
  s("cpu.total", "cpu", "percent", [10, 20, 30, null]),
  s("memory.used", "memory", "bytes", [50, 50, 50, 50]),
  s("memory.total", "memory", "bytes", [200, 200, 200, 200]),
  s("temp.coretemp-0000:00:18.3.1", "temperature", "celsius", [40, 41, 42, 42]),
  s("temp.nvme-0000:01:00.0.1", "temperature", "celsius", [60, null, null, null]),
] };
const hostCharts = charts(host, true);
assert.deepEqual(hostCharts.map(c => c.group), ["cpu", "memory", "network", "disk", "load", "temperature"], "temperature is the sixth host chart");
assert.equal(hostCharts.at(-1).stacked, false, "temperatures never stack");
assert.deepEqual(hostCharts.at(-1).series.map(x => x.key), ["temp.coretemp-0000:00:18.3.1", "temp.nvme-0000:01:00.0.1"]);
assert.deepEqual(charts({ ...host, object: "vm" }, false).map(c => c.group), ["cpu", "memory", "network", "disk"], "a VM has no temperature chart");

assert.equal(lastValue([1, null, null]), 1, "two steps back is still current");
assert.equal(lastValue([1, null, null, null]), null, "older than three steps is unknown, not a frozen number");
assert.equal(lastValue([]), null);
const band = { max: 80, crit: 100 };
assert.equal(gaugeState(42, band), "normal");
assert.equal(gaugeState(80, band), "warm", "at max is already warm");
assert.equal(gaugeState(100, band), "critical");
assert.equal(gaugeState(120, {}), "normal", "no thresholds means no band");
assert.equal(gaugeState(null, band), "normal");

const row = gauges(host, { "temp.coretemp-0000:00:18.3.1": band });
assert.deepEqual(row.map(g => g.key), ["cpu.total", "memory", "temp.coretemp-0000:00:18.3.1", "temp.nvme-0000:01:00.0.1"], "CPU, memory, then each sensor in series order");
assert.deepEqual(row[0], { key: "cpu.total", label: "CPU", unit: "percent", value: 30, scale: 100, state: "normal" });
assert.equal(row[1].value, 25, "memory is used over total");
assert.deepEqual([row[2].scale, row[2].state], [100, "normal"], "a sensor scales to its crit");
assert.deepEqual([row[3].value, row[3].scale, row[3].state], [null, null, "normal"], "a stale sensor without thresholds shows a plain unknown");
assert.deepEqual(gauges({ ...host, object: "vm" }), [], "a VM has no hardware to gauge");
// SR-085: Warm/Critical must be in aria-valuetext; the visible state span is presentational.
assert.equal(gaugeValueText({ unit: "celsius", value: 96, state: "critical" }), "96 °C Critical");
assert.equal(gaugeValueText({ unit: "celsius", value: 82, state: "warm" }), "82 °C Warm");
assert.equal(gaugeValueText({ unit: "percent", value: 40, state: "normal" }), "40%");
{
  const statsSrc = readFileSync(new URL("./stats.tsx", import.meta.url), "utf8");
  assert.match(statsSrc, /aria-valuetext": announced/);
  assert.match(statsSrc, /stats-gauge-state" aria-hidden="true"/);
}

// Hardware panel: the summary line, what a section shows instead of a table, and the thresholds the
// gauges above join to the history by series key. Every value here is a fictional fixture.
const hw = {
  observed_at: "2026-09-22T10:00:00Z",
  system: { vendor: "Example Systems", model: "EX-2200", bios_version: "2.1.4" },
  cpu: { model: "Example CPU E-16", sockets: 2, cores: 32, threads: 64 },
  memory: { total_bytes: 8 * 1024 ** 3, modules: [{ slot: "DIMM_A1", size_bytes: 8 * 1024 ** 3 }] },
  disks: { devices: [] },
  pci: { unavailable: true, reason: "lspci is not installed" },
  sensors: { temperatures: [
    { key: "temp.coretemp-0000:00:18.3.1", chip: "coretemp", label: "CPU 0 package", max_celsius: 80, crit_celsius: 100 },
    { key: "temp.acpitz-0.1", chip: "acpitz", label: "acpitz" },
  ] },
};
assert.deepEqual(hardwareSummary(hw), ["Example Systems EX-2200", "2 × Example CPU E-16", "32 cores / 64 threads", "8.0 GiB memory", "BIOS 2.1.4"]);
assert.deepEqual(hardwareSummary({ system: {}, cpu: { model: "Example CPU E-4", cores: 4 }, memory: {} }), ["Example CPU E-4", "4 cores / 4 threads"], "a report without dmidecode still names the CPU");

assert.equal(sectionNote(hw.system, 3), null, "a section with rows draws its table");
assert.equal(sectionNote(hw.pci, 0), "lspci is not installed", "an unavailable section shows the host's reason");
assert.equal(sectionNote({ unavailable: true }, 0), "This section is not available on this host.");
assert.equal(sectionNote(hw.disks, 0), "None reported.", "available but empty is not an error");
assert.equal(sectionNote(undefined, 0), "Not reported by this host.", "a reply missing a section says so");

// SR-086: arrays:null is a Core that sent no section; arrays:[] is a Core with no mirror; unavailable
// must surface the host's reason rather than "Single system disk".
assert.equal(mdArraysView(undefined), "unreported");
assert.equal(mdArraysView({ arrays: null }), "unreported", "Controller remarsal of a missing section");
assert.equal(mdArraysView({ arrays: [] }), "empty", "current Core looked and found no arrays");
assert.equal(mdArraysView({ unavailable: true, reason: "the kernel exposes no block devices for this host", arrays: [] }), "unavailable");
assert.equal(mdArraysView({ arrays: [{ name: "md0" }] }), "arrays");
assert.equal(mdArraysView({ unavailable: true, arrays: [{ name: "md0" }] }), "unavailable", "unavailable wins over a stale list");

// Replace needs discovery + provision, not only the system-disk capability.
assert.equal(systemDiskReplaceSupported(["host-system-disk-replace-v1"]), false);
assert.equal(systemDiskReplaceSupported(["host-system-disk-replace-v1", "storage-discovery-v1"]), false);
assert.equal(systemDiskReplaceSupported(["host-system-disk-replace-v1", "storage-discovery-v1", "storage-zfs-provision-v1"]), true);
assert.equal(systemDiskReplaceSupported(undefined), false);

assert.deepEqual(sensorThresholds(hw), {
  "temp.coretemp-0000:00:18.3.1": { max: 80, crit: 100 },
  "temp.acpitz-0.1": { max: undefined, crit: undefined },
}, "thresholds are keyed by the series key, so a gauge scales from the host's own sensor");
assert.deepEqual(sensorThresholds(null), {}, "no hardware report means no invented bands");
assert.deepEqual(gauges(host, sensorThresholds(hw))[2].scale, 100, "the CPU sensor gauge ends at its crit");
assert.equal(statsFailure(failure("the host agent does not report hardware; upgrade Nex Core", 409)).kind, "capability", "the hardware relay's 409 hides the panel rather than offering a retry");
// Hardware Retry must keep a stable callback identity across Stats re-renders so the system-disk
// mirror's rebuild interval is not cleared every 5s by a fresh inline arrow.
{
  const statsSrc = readFileSync(new URL("./stats.tsx", import.meta.url), "utf8");
  assert.match(statsSrc, /const rereadHardware = useCallback\(\(\) => setHardwareAttempt\(a => a \+ 1\), \[\]\);/);
  assert.match(statsSrc, /onRetry=\{rereadHardware\}/);
  assert.doesNotMatch(statsSrc, /onRetry=\{\(\) => setHardwareAttempt/);
}
// WCAG 1.4.11: a plotted line is non-text content, so every series colour needs 3:1 against the
// panel it is drawn on. Series and status colours now live in tokens.css (Task 2); stats.css
// references them and carries no literal colour of its own (tokens.test.mjs enforces that).
{
  const css = readFileSync(new URL("./stats.css", import.meta.url), "utf8");
  const tokens = readFileSync(new URL("./tokens.css", import.meta.url), "utf8");
  const token = name => tokens.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`))[1];
  const rgb = hex => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16));
  const luminance = hex => rgb(hex).map(v => v / 255).map(v => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
    .reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i], 0);
  const contrast = (a, b) => { const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x); return (hi + 0.05) / (lo + 0.05); };
  const panel = token("panel");
  for (let i = 1; i <= 8; i++) {
    const colour = token(`series-${i}`);
    assert.ok(contrast(colour, panel) >= 3, `--series-${i} ${colour} is ${contrast(colour, panel).toFixed(2)}:1 on the panel, below 3:1`);
  }
  // SR-088: only the last body row drops its rule, so thead keeps its underline.
  assert.match(css, /\.stats-hw-table tbody tr:last-child > \* \{ border-bottom: 0; \}/);
  assert.doesNotMatch(css, /\.stats-hw-table tr:last-child > \*/);
  // SR-089 / SR-090: system-disk block is a card; Healthy text meets 4.5:1 on the stats panel canvas.
  assert.match(css, /\.md-arrays \{[^}]*background: var\(--panel\)/);
  const healthy = token("ok");
  const sunken = token("sunken");
  assert.ok(contrast(healthy, sunken) >= 4.5, `.md-healthy ${healthy} is ${contrast(healthy, sunken).toFixed(2)}:1 on the stats panel canvas, below 4.5:1`);
}

console.log("stats helpers ok");

// The VM panel's spark rows: one series per resource, byte rates summed, IOPS never added to bytes.
{
  const { sparkRows } = await import(moduleURL);
  const rows = sparkRows({ object: "vm", id: "vm-1", range: "10m", step_seconds: 5, start: "2026-09-16T00:00:00Z", series: [
    s("cpu.total", "cpu", "percent", [10, null, 30]), s("cpu.vcpu.0", "cpu", "percent", [4, null, 10]),
    s("memory.assigned", "memory", "bytes", [8, 8, 8]), s("memory.host", "memory", "bytes", [2, 4, 4]),
    s("disk.vda.read_bytes", "disk", "bytes_per_second", [1024, 0, 1024]), s("disk.vda.write_bytes", "disk", "bytes_per_second", [1024, 0, 1024]),
    s("disk.vda.read_iops", "disk", "iops", [500, 500, 500]),
    s("net.nic0.rx", "network", "bytes_per_second", [125_000, 0, 125_000]),
  ] });
  assert.deepEqual(rows.map(r => r.text), ["30%", "4 B", "2.0 KiB/s", "1.0 Mbps"]);
  assert.deepEqual(rows[0].values, [10, 30], "a gap is left out, not drawn as zero");
  assert.equal(rows[1].detail, "memory.host", "host resident, not assigned plus host");
  assert.deepEqual(rows[2].values, [100, 0, 100]);
  console.log("stats spark rows ok");
}
