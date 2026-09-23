// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import "./stats.css";
import { SystemDiskMirror, SYSTEM_DISK_CAPABILITY, type MDArray } from "./md";

type API = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
export type StatsSeries = { key: string; group: string; label: string; unit: string; values: (number | null)[] };
export type StatsHistory = { object: string; id: string; host_id?: string; range: string; step_seconds: number; start: string; series: StatsSeries[] };

export const ranges = [["10m", "Last 10 minutes"], ["24h", "Last 24 hours"], ["30d", "Last 30 days"], ["1y", "Last year"]] as const;
// Live refresh only where new points arrive often enough to matter; longer ranges refresh on demand.
export const refreshMS: Record<string, number> = { "10m": 5000, "24h": 60000 };
const titles: Record<string, string> = { cpu: "CPU usage", memory: "Memory usage", network: "Network throughput", disk: "Disk throughput", load: "Load average", temperature: "Temperature" };
const stackable = new Set(["cpu", "network", "disk"]);
const PALETTE_SIZE = 8;

export function formatValue(unit: string, v: number | null | undefined): string {
  if (v == null || !Number.isFinite(v)) return "—";
  if (unit === "percent") return `${v.toFixed(v < 10 ? 1 : 0)}%`;
  if (unit === "iops") return `${v.toFixed(v < 10 ? 1 : 0)} IOPS`;
  if (unit === "load") return v.toFixed(2);
  if (unit === "celsius") return `${v.toFixed(0)} °C`;
  const suffix = unit === "bytes_per_second" ? "/s" : "";
  const names = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0, n = Math.abs(v);
  while (n >= 1024 && i < names.length - 1) { n /= 1024; i++; }
  return `${(Math.sign(v) * n).toFixed(i && n < 10 ? 1 : 0)} ${names[i]}${suffix}`;
}

// Axis labels share one unit and enough decimals for the tick step, so a near-zero rate axis reads
// "0.5 B/s, 1.0 B/s" instead of repeating a rounded "1 B/s".
export function formatTicks(unit: string, ticks: number[], incr: number): string[] {
  const suffix = { percent: "%", iops: " IOPS", bytes: " B", bytes_per_second: " B/s", celsius: " °C" }[unit] ?? "";
  let divisor = 1, name = suffix;
  if (unit === "bytes" || unit === "bytes_per_second") {
    const names = ["B", "KiB", "MiB", "GiB", "TiB"];
    const top = Math.max(0, ...ticks.map(Math.abs));
    const i = Math.min(names.length - 1, Math.max(0, Math.floor(Math.log(top || 1) / Math.log(1024))));
    divisor = 1024 ** i;
    name = ` ${names[i]}${unit === "bytes_per_second" ? "/s" : ""}`;
  }
  const step = incr / divisor;
  // The fewest decimals (at most 3) that represent the step exactly, so 0.25 steps read 0.25, not 0.3.
  let decimals = 0;
  while (decimals < 3 && step > 0 && Number.isFinite(step) && Math.abs(Math.round(step * 10 ** decimals) - step * 10 ** decimals) > 1e-6 * 10 ** decimals) decimals++;
  return ticks.map(t => t == null ? "" : `${(t / divisor).toFixed(decimals)}${name}`);
}

// Tick steps for byte axes are powers of two of a binary unit, so every label is exact in KiB/MiB/GiB.
export const byteIncrs = [0.25, 0.5, ...Array.from({ length: 50 }, (_, i) => 2 ** i)];

// A known sample with unknown neighbours has no line segment to draw, so it is drawn as a point.
export function isolatedIndices(values: (number | null)[]): number[] | null {
  const out = values.flatMap((v, i) => v != null && values[i - 1] == null && values[i + 1] == null ? [i] : []);
  return out.length ? out : null;
}

// A "total" series duplicates the per-vCPU/per-core ones, so it is left out of a stack.
const isTotal = (s: StatsSeries) => s.key.split(".").pop() === "total";

export type Chart = { group: string; title: string; series: StatsSeries[]; stacked: boolean };
export function charts(history: StatsHistory, stacked: boolean): Chart[] {
  const groups = history.object === "host" ? ["cpu", "memory", "network", "disk", "load", "temperature"] : ["cpu", "memory", "network", "disk"];
  return groups.map(group => {
    const stack = stacked && stackable.has(group);
    const series = history.series.filter(s => s.group === group && !(stack && isTotal(s)));
    return { group, title: titles[group], series, stacked: stack };
  });
}

// Stacked values are running sums per unit; an unknown point stays a gap rather than counting as zero.
// Each percent series is one vCPU or core at 0–100%, so the stack of N fully busy ones reads N×100%.
// A series hidden from the legend stays in the data (uPlot only stops drawing it) but leaves the
// running sum, collapsing onto the band below, so the visible bands stack on each other.
export function plotValues(series: StatsSeries[], stacked: boolean, hidden: Set<string> = new Set()): (number | null)[][] {
  if (!stacked) return series.map(s => s.values);
  const sums = new Map<string, number[]>();
  return series.map(s => {
    const acc = sums.get(s.unit) || [];
    sums.set(s.unit, acc);
    const add = hidden.has(s.key) ? 0 : 1;
    return s.values.map((v, i) => v == null ? null : (acc[i] = (acc[i] || 0) + v * add));
  });
}

// A stacked percent axis ends at 100% per visible vCPU/core, so four busy vCPUs fill a 400% chart.
export function stackedPercentMax(series: StatsSeries[], hidden: Set<string> = new Set()): number {
  return 100 * Math.max(1, series.filter(s => s.unit === "percent" && !hidden.has(s.key)).length);
}

// uPlot draws no fill for a band whose lower edge is hidden, so each visible series bands onto the
// nearest visible series below it with the same unit; the lowest visible one fills to zero.
export function stackBands(series: StatsSeries[], stacked: boolean, hidden: Set<string> = new Set()): uPlot.Band[] | undefined {
  if (!stacked) return undefined;
  const bands: uPlot.Band[] = [];
  const below = new Map<string, number>();
  series.forEach((s, i) => {
    if (hidden.has(s.key)) return;
    const lower = below.get(s.unit);
    if (lower != null) bands.push({ series: [i + 1, lower + 1] });
    below.set(s.unit, i);
  });
  return bands;
}

export function timestamps(history: StatsHistory): number[] {
  const start = Date.parse(history.start) / 1000;
  const length = Math.max(0, ...history.series.map(s => s.values.length));
  return Array.from({ length }, (_, i) => start + i * history.step_seconds);
}

// Realtime turns itself off after this long so a forgotten tab does not keep a host sampling every second.
export const REALTIME_LIMIT_MS = 10 * 60 * 1000;

// Realtime charts wait for two known points: one sample draws nothing useful and reads as broken.
export function enoughLiveSamples(history: StatsHistory | null): boolean {
  return !!history && history.series.some(s => s.values.filter(v => v != null).length >= 2);
}

export function updatedAgo(at: number, now: number): string {
  const seconds = Math.max(0, Math.round((now - at) / 1000));
  return seconds < 1 ? "Updated just now" : `Updated ${seconds}s ago`;
}

// Gauge thresholds come from the host's hardware report, keyed by series key; a sensor the host
// reports no max/crit for is drawn as a plain value with no band.
export type Thresholds = Record<string, { max?: number; crit?: number }>;
export type GaugeState = "normal" | "warm" | "critical";
export type Gauge = { key: string; label: string; unit: string; value: number | null; scale: number | null; state: GaugeState };

// Gauges read the history the tab already polls, so they cost no request. A value older than three
// steps is no longer current: showing the frozen number would read as a live one.
export function lastValue(values: (number | null)[], within = 3): number | null {
  for (let i = values.length - 1; i >= 0 && i > values.length - 1 - within; i--) if (values[i] != null) return values[i];
  return null;
}

export function gaugeState(value: number | null, t?: { max?: number; crit?: number }): GaugeState {
  if (value == null) return "normal";
  if (t?.crit != null && value >= t.crit) return "critical";
  if (t?.max != null && value >= t.max) return "warm";
  return "normal";
}

export function gauges(history: StatsHistory, thresholds: Thresholds = {}): Gauge[] {
  if (history.object !== "host") return [];
  const value = (key: string) => {
    const s = history.series.find(s => s.key === key);
    return s ? lastValue(s.values) : null;
  };
  const used = value("memory.used"), total = value("memory.total");
  const out: Gauge[] = [
    { key: "cpu.total", label: "CPU", unit: "percent", value: value("cpu.total"), scale: 100, state: "normal" },
    { key: "memory", label: "Memory", unit: "percent", value: used != null && total ? (100 * used) / total : null, scale: 100, state: "normal" },
  ];
  for (const s of history.series.filter(s => s.group === "temperature")) {
    const t = thresholds[s.key];
    const reading = lastValue(s.values);
    out.push({ key: s.key, label: s.label, unit: s.unit, value: reading, scale: t?.crit ?? t?.max ?? null, state: gaugeState(reading, t) });
  }
  return out;
}

// Fold Warm/Critical into the meter's valuetext: the coloured state word is presentational and
// the bar is aria-hidden, so assistive tech only hears the announced value.
export function gaugeValueText(gauge: Pick<Gauge, "unit" | "value" | "state">): string {
  const text = formatValue(gauge.unit, gauge.value);
  if (gauge.state === "normal") return text;
  return `${text} ${gauge.state === "critical" ? "Critical" : "Warm"}`;
}

function GaugeTile({ gauge }: { gauge: Gauge }) {
  const text = formatValue(gauge.unit, gauge.value);
  const announced = gaugeValueText(gauge);
  const percent = gauge.value != null && gauge.scale ? Math.max(0, Math.min(100, (100 * gauge.value) / gauge.scale)) : null;
  return <div className={`stats-gauge stats-gauge-${gauge.state}`}>
    <div className="stats-gauge-label">{gauge.label}</div>
    <div className="stats-gauge-value" {...(percent != null ? { role: "meter", "aria-valuenow": gauge.value!, "aria-valuemin": 0, "aria-valuemax": Math.max(gauge.scale!, gauge.value!), "aria-valuetext": announced, "aria-label": gauge.label } : {})}>
      {text}{gauge.state !== "normal" && <span className="stats-gauge-state" aria-hidden="true">{gauge.state === "critical" ? "Critical" : "Warm"}</span>}
    </div>
    {percent != null && <div className="stats-gauge-bar" aria-hidden="true"><span style={{ width: `${percent}%` }} /></div>}
  </div>;
}

export type StatsFailure = { kind: "capability" | "stale" | "error"; message: string };

// The stats relay uses 409 for both a stale host and an older Core. Keep the distinction the
// operator needs without adding a second API contract solely for presentation.
export function statsFailure(error: unknown): StatsFailure {
  const message = error instanceof Error ? error.message : String(error);
  const status = (error as { status?: number })?.status;
  if (status === 409 && /does not keep usage history|upgrade nex core/i.test(message)) return { kind: "capability", message };
  if (status === 409 && /not freshly connected/i.test(message)) return { kind: "stale", message };
  return { kind: "error", message };
}

// The host reads its hardware live on request; nothing is cached here. Every section may come back
// unavailable on its own, so a missing tool costs one section rather than the whole report.
export const HARDWARE_CAPABILITY = "host-hardware-v1";
export type Unavailable = { unavailable?: boolean; reason?: string };
export type MemoryModule = { slot: string; size_bytes: number; type?: string; speed_mts?: number; manufacturer?: string; part?: string };
export type HardwareDisk = { name: string; model?: string; serial?: string; size_bytes: number; rotational: boolean; transport?: string };
export type PCIDevice = { address: string; class: string; class_id: string; vendor: string; vendor_id: string; device: string; device_id: string };
export type TemperatureSensor = { key: string; chip: string; label: string; max_celsius?: number; crit_celsius?: number };
export type HostHardware = {
  host_id?: string;
  observed_at: string;
  system: Unavailable & { vendor?: string; model?: string; version?: string; serial?: string; board_vendor?: string; board_name?: string; board_version?: string; board_serial?: string; chassis_vendor?: string; chassis_type?: string; bios_vendor?: string; bios_version?: string; bios_date?: string };
  cpu: Unavailable & { model?: string; sockets?: number; cores?: number; threads?: number };
  memory: Unavailable & { total_bytes?: number; modules?: MemoryModule[] };
  disks: Unavailable & { devices?: HardwareDisk[] };
  pci: Unavailable & { devices?: PCIDevice[] };
  sensors: Unavailable & { temperatures?: TemperatureSensor[] };
  md_arrays?: Unavailable & { arrays?: MDArray[] | null };
};

const join = (...parts: (string | undefined)[]) => parts.filter(Boolean).join(" ");

// One line answering "what is this server": vendor and model, CPU, memory, BIOS version.
export function hardwareSummary(hw: HostHardware): string[] {
  const out = [join(hw.system?.vendor, hw.system?.model)];
  const cpu = hw.cpu ?? {};
  if (cpu.model) out.push(cpu.sockets && cpu.sockets > 1 ? `${cpu.sockets} × ${cpu.model}` : cpu.model);
  if (cpu.cores) out.push(`${cpu.cores} cores / ${cpu.threads ?? cpu.cores} threads`);
  if (hw.memory?.total_bytes) out.push(`${formatValue("bytes", hw.memory.total_bytes)} memory`);
  if (hw.system?.bios_version) out.push(`BIOS ${hw.system.bios_version}`);
  return out.filter(Boolean);
}

// What a section shows instead of its table: the host's reason when it could not be read, a plain
// note when it read nothing, and null when there are rows to draw.
export function sectionNote(section: Unavailable | undefined, rows: number): string | null {
  if (!section) return "Not reported by this host.";
  if (section.unavailable) return section.reason || "This section is not available on this host.";
  return rows ? null : "None reported.";
}

// System-disk panel states derived from md_arrays. The Controller always remarsals the field, so
// an older Core that never sent it arrives as arrays:null; a current Core that looked and found
// none sends arrays:[]. Keep "Single system disk" only for the latter.
export type MDArraysView = "unreported" | "unavailable" | "empty" | "arrays";
export function mdArraysView(section: HostHardware["md_arrays"]): MDArraysView {
  if (section?.unavailable) return "unavailable";
  if (!Array.isArray(section?.arrays)) return "unreported";
  return section.arrays.length ? "arrays" : "empty";
}

// Replace disk… opens storage/discover first; gate on the same discovery/provision caps that call
// needs, not only host-system-disk-replace-v1.
export function systemDiskReplaceSupported(capabilities?: string[]): boolean {
  return !!capabilities?.includes(SYSTEM_DISK_CAPABILITY)
    && !!capabilities?.includes("storage-discovery-v1")
    && !!capabilities?.includes("storage-zfs-provision-v1");
}

// Gauge scale and warning band come from the host's own sensor thresholds, joined to the history by
// series key, so no number is invented in the UI.
export function sensorThresholds(hw: HostHardware | null): Thresholds {
  const out: Thresholds = {};
  for (const s of hw?.sensors?.temperatures ?? []) out[s.key] = { max: s.max_celsius, crit: s.crit_celsius };
  return out;
}

const id = (v?: string) => v ? <span className="stats-id">{v}</span> : null;

function KeyValues({ rows }: { rows: [string, ReactNode][] }) {
  return <table className="stats-hw-table"><tbody>
    {rows.filter(([, v]) => v != null && v !== "").map(([k, v]) => <tr key={k}><th scope="row">{k}</th><td>{v}</td></tr>)}
  </tbody></table>;
}

function Section({ title, note, count, children }: { title: string; note: string | null; count?: number; children: ReactNode }) {
  return <details className="stats-hw-section">
    <summary>{title}{count != null && !note && <span className="stats-hw-count">{count}</span>}</summary>
    {note ? <p className="empty">{note}</p> : children}
  </details>;
}

function HardwareSections({ hw }: { hw: HostHardware }) {
  const sys = hw.system ?? {}, cpu = hw.cpu ?? {};
  const modules = hw.memory?.modules ?? [], disks = hw.disks?.devices ?? [], pci = hw.pci?.devices ?? [], sensors = hw.sensors?.temperatures ?? [];
  return <>
    <Section title="System" note={sectionNote(hw.system, Object.values(sys).filter(v => typeof v === "string" && v).length)}>
      <KeyValues rows={[
        ["Vendor", sys.vendor],
        ["Model", join(sys.model, sys.version)],
        ["Serial", id(sys.serial)],
        ["Board", join(sys.board_vendor, sys.board_name, sys.board_version)],
        ["Board serial", id(sys.board_serial)],
        ["Chassis", join(sys.chassis_vendor, sys.chassis_type)],
        ["BIOS", sys.bios_version ? <>{id(sys.bios_version)} {join(sys.bios_vendor, sys.bios_date)}</> : null],
      ]} />
    </Section>
    <Section title="CPU" note={sectionNote(hw.cpu, cpu.model ? 1 : 0)}>
      <KeyValues rows={[["Model", cpu.model], ["Sockets", cpu.sockets], ["Cores", cpu.cores], ["Threads", cpu.threads]]} />
    </Section>
    <Section title="Memory" note={sectionNote(hw.memory, modules.length)} count={modules.length}>
      <table className="stats-hw-table">
        <thead><tr><th>Slot</th><th>Size</th><th>Type</th><th>Speed</th><th>Manufacturer</th><th>Part</th></tr></thead>
        <tbody>{modules.map(m => <tr key={m.slot}>
          <td>{m.slot}</td><td>{m.size_bytes ? formatValue("bytes", m.size_bytes) : "Empty"}</td><td>{m.type}</td>
          <td>{m.speed_mts ? `${m.speed_mts} MT/s` : ""}</td><td>{m.manufacturer}</td><td>{id(m.part)}</td>
        </tr>)}</tbody>
      </table>
    </Section>
    <Section title="Disks" note={sectionNote(hw.disks, disks.length)} count={disks.length}>
      <table className="stats-hw-table">
        <thead><tr><th>Device</th><th>Model</th><th>Size</th><th>Type</th><th>Transport</th><th>Serial</th></tr></thead>
        <tbody>{disks.map(d => <tr key={d.name}>
          <td>{d.name}</td><td>{d.model}</td><td>{formatValue("bytes", d.size_bytes)}</td>
          <td>{d.rotational ? "Rotational" : "Solid state"}</td><td>{d.transport}</td><td>{id(d.serial)}</td>
        </tr>)}</tbody>
      </table>
    </Section>
    <Section title="PCI devices" note={sectionNote(hw.pci, pci.length)} count={pci.length}>
      <table className="stats-hw-table">
        <thead><tr><th>Address</th><th>Class</th><th>Device</th><th>IDs</th></tr></thead>
        <tbody>{pci.map(d => <tr key={d.address}>
          <td>{d.address}</td><td>{d.class}</td><td>{join(d.vendor, d.device)}</td><td>{id(`${d.vendor_id}:${d.device_id}`)}</td>
        </tr>)}</tbody>
      </table>
    </Section>
    <Section title="Temperature sensors" note={sectionNote(hw.sensors, sensors.length)} count={sensors.length}>
      <table className="stats-hw-table">
        <thead><tr><th>Sensor</th><th>Chip</th><th>Warns at</th><th>Critical at</th><th>Series</th></tr></thead>
        <tbody>{sensors.map(s => <tr key={s.key}>
          <td>{s.label}</td><td>{s.chip}</td><td>{formatValue("celsius", s.max_celsius)}</td>
          <td>{formatValue("celsius", s.crit_celsius)}</td><td>{id(s.key)}</td>
        </tr>)}</tbody>
      </table>
    </Section>
  </>;
}

function HardwarePanel({ hardware, loading, failure, onRetry, id, poolID, api, replaceSupported }: { hardware: HostHardware | null; loading: boolean; failure: StatsFailure | null; onRetry: () => void; id: string; poolID: string; api: API; replaceSupported: boolean }) {
  // Hardware is read once when the tab opens, so Retry is the only way to reread it — including
  // after a report loaded and the host has since changed or gone stale.
  const mdView = hardware ? mdArraysView(hardware.md_arrays) : null;
  return <section className="panel stats" aria-label="Hardware">
    <div className="section-heading"><h2>Hardware</h2>
      <div className="stats-controls"><button type="button" disabled={loading} onClick={onRetry}>Retry</button></div>
    </div>
    {hardware && <p className="stats-hw-summary">{hardwareSummary(hardware).join(" · ")}</p>}
    {failure?.kind === "stale" && <div className="stats-stale" role="status">{hardware ? "The host connection is stale. Showing the last loaded hardware; it may be out of date." : "The host connection is stale. Reconnect Nex Core, then retry."}</div>}
    {failure?.kind === "error" && <div className="alert" role="alert">{hardware ? `Could not reread the hardware; showing the last loaded report. ${failure.message}` : failure.message}</div>}
    {!hardware && !failure && <p className="empty" role="status">Reading hardware from the host…</p>}
    {hardware && mdView === "unavailable" && <div className="md-arrays">
      <h3>System disk</h3>
      <p className="empty">{hardware.md_arrays?.reason || "This section is not available on this host."}</p>
    </div>}
    {hardware && mdView && mdView !== "unavailable" && <SystemDiskMirror arrays={hardware.md_arrays?.arrays ?? []} disks={hardware.disks?.devices ?? []}
      hostID={id} poolID={poolID} api={api} supported={replaceSupported} reported={mdView !== "unreported"} onReplaced={onRetry} />}
    {hardware && <div className="stats-hw-sections"><HardwareSections hw={hardware} /></div>}
  </section>;
}

export function RealtimeStats({ kind, id, api, stacked, limitMS = REALTIME_LIMIT_MS }: { kind: "vm" | "host"; id: string; api: API; stacked?: boolean; limitMS?: number }) {
  const [open, setOpen] = useState(false);
  const [localStacked, setLocalStacked] = useState(false);
  const [on, setOn] = useState(false);
  const [history, setHistory] = useState<StatsHistory | null>(null);
  const [updated, setUpdated] = useState(0);
  const [now, setNow] = useState(Date.now());
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const [visible, setVisible] = useState(document.visibilityState === "visible");
  const hidden = useRef(new Set<string>());
  const plots = useRef(new Set<uPlot>());
  // Set only when the operator turns realtime on, so a Retry never extends the 10-minute limit.
  const startedAt = useRef(0);

  useEffect(() => {
    const change = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", change);
    return () => document.removeEventListener("visibilitychange", change);
  }, []);

  useEffect(() => {
    // The clock keeps "Updated Ns ago" moving and enforces the limit even while an error has stopped polling.
    if (!on) return;
    const clock = () => {
      setNow(Date.now());
      if (Date.now() - startedAt.current >= limitMS) {
        setOn(false);
        setError("");
        setNote("Realtime paused after 10 minutes to save host resources.");
      }
    };
    const timer = setInterval(clock, 1000);
    return () => clearInterval(timer);
  }, [on, limitMS]);

  useEffect(() => {
    // Polling stops the moment this is unchecked or unmounted; the host's lease then lapses on its own.
    if (!on || error) return;
    let stopped = false, pending = false;
    async function tick() {
      if (Date.now() - startedAt.current >= limitMS) return;
      if (pending || document.visibilityState !== "visible") return;
      pending = true;
      try {
        const next = await api<StatsHistory>(`${kind}s/${encodeURIComponent(id)}/stats?range=live`);
        if (!stopped) { setHistory(next); setUpdated(Date.now()); }
      } catch (e) {
        if (stopped) return;
        const message = e instanceof Error ? e.message : String(e);
        if ((e as { status?: number }).status === 429) { setOn(false); setNote(message); }
        else setError(message);
      } finally {
        pending = false;
      }
    }
    void tick();
    const timer = setInterval(() => void tick(), 1000);
    return () => { stopped = true; clearInterval(timer); };
  }, [on, error, kind, id, limitMS]);

  function toggle(checked: boolean) {
    if (checked) startedAt.current = Date.now();
    setOn(checked);
    setNote("");
    setError("");
    if (!checked) setHistory(null);
  }

  const ready = enoughLiveSamples(history);
  // Announced only when the state changes; the per-second "Updated" counter stays out of the live region.
  const announcement = !on ? (open ? "Realtime off" : "") : error ? "Realtime update failed" : !visible ? "Realtime paused while this page is hidden" : ready ? "Realtime live" : "Collecting realtime samples";
  return <div className="stats stats-realtime">
    <button type="button" className="stats-drawer" aria-expanded={open} onClick={() => { if (open) toggle(false); setOpen(!open); }}>
      <span className="stats-chevron" aria-hidden="true">{open ? "▾" : "▸"}</span>Realtime
      {on && !error && <span className="stats-live"><span className="stats-live-dot" aria-hidden="true" />Live</span>}
    </button>
    {open && <div className="stats-drawer-body">
      <div className="stats-realtime-bar">
        <label title="While on, the host samples this object every second. Turning it off stops requests at once; the host stops sampling by itself within 2 minutes.">
          <input type="checkbox" checked={on} onChange={e => toggle(e.target.checked)} /> Show realtime stats (1-second samples)
        </label>
        {stacked === undefined && <label><input type="checkbox" role="switch" checked={localStacked} onChange={e => setLocalStacked(e.target.checked)} /> Stacked</label>}
        {on && <span className="muted">{!visible ? "Paused while this page is hidden" : updated ? updatedAgo(updated, now) : "Connecting…"}</span>}
        <span className="sr-only" role="status">{announcement}</span>
      </div>
      {note && <div className="stats-note" role="status">{note} {!on && note.startsWith("Realtime paused") && <button type="button" onClick={() => toggle(true)}>Resume</button>}</div>}
      {error && <div className="alert" role="alert">{history ? `Realtime update failed; showing the last samples. ${error}` : error} <button type="button" onClick={() => setError("")}>Retry</button></div>}
      {on && !ready && !error && <p className="empty">Collecting realtime samples…</p>}
      {history && ready && (on || error) && <div className="stats-grid stats-grid-live">
        {charts(history, stacked ?? localStacked).map(c => <StatsChart key={c.group} chart={c} history={history} syncKey={`live-${kind}-${id}`} hidden={hidden.current} plots={plots.current} height={kind === "vm" ? 100 : 120} zoom={false} />)}
      </div>}
      <p className="panel-note">Realtime is off until you turn it on. Closing this drawer or leaving the tab turns realtime off, and it turns itself off after 10 minutes. It pauses while this page is hidden.</p>
    </div>}
  </div>;
}

export function Stats({ kind, id, api, capabilities, poolID = "", realtimeLimitMS = REALTIME_LIMIT_MS }: { kind: "vm" | "host"; id: string; api: API; capabilities?: string[]; poolID?: string; realtimeLimitMS?: number }) {
  const [range, setRange] = useState("10m");
  const [stacked, setStacked] = useState(false);
  const [history, setHistory] = useState<StatsHistory | null>(null);
  const [failure, setFailure] = useState<StatsFailure | null>(null);
  const [loading, setLoading] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const hidden = useRef(new Set<string>());
  const plots = useRef(new Set<uPlot>());
  const shown = useRef("");
  // Hardware is static, so it is read once when the tab opens and on an explicit Retry. An older
  // Core that does not report the capability is not asked at all.
  const hardwareSupported = kind === "host" && !!capabilities?.includes(HARDWARE_CAPABILITY);
  const [hardware, setHardware] = useState<HostHardware | null>(null);
  const [hardwareFailure, setHardwareFailure] = useState<StatsFailure | null>(null);
  const [hardwareLoading, setHardwareLoading] = useState(false);
  const [hardwareAttempt, setHardwareAttempt] = useState(0);
  // Stable identity so SystemDiskMirror's rebuild/task intervals are not torn down on every
  // Stats history re-render (default 10m range polls every 5s).
  const rereadHardware = useCallback(() => setHardwareAttempt(a => a + 1), []);

  useEffect(() => {
    if (!hardwareSupported) return;
    let stopped = false;
    setHardwareLoading(true);
    api<HostHardware>(`hosts/${encodeURIComponent(id)}/hardware`)
      .then(next => { if (!stopped) { setHardware(next); setHardwareFailure(null); } })
      // The last loaded report stays on screen, marked stale, rather than blanking the panel.
      .catch(e => { if (!stopped) setHardwareFailure(statsFailure(e)); })
      .finally(() => { if (!stopped) setHardwareLoading(false); });
    return () => { stopped = true; };
  }, [id, hardwareSupported, hardwareAttempt]);

  useEffect(() => {
    let stopped = false, inFlight = false;
    // Refresh and Retry keep the charts and zoom; only a different object or range starts over.
    if (shown.current !== `${kind}|${id}|${range}`) {
      shown.current = `${kind}|${id}|${range}`;
      setHistory(null);
      setFailure(null);
    }
    async function load() {
      if (inFlight || stopped) return;
      inFlight = true;
      setLoading(true);
      try {
        const next = await api<StatsHistory>(`${kind}s/${encodeURIComponent(id)}/stats?range=${range}`);
        if (!stopped) { setHistory(next); setFailure(null); }
      } catch (e) {
        if (!stopped) {
          const next = statsFailure(e);
          setFailure(next);
          // An older Core cannot serve this feature at all, so do not present an old chart as
          // current usage after it has told us the capability is absent.
          if (next.kind === "capability") setHistory(null);
        }
      } finally {
        inFlight = false;
        if (!stopped) setLoading(false);
      }
    }
    void load();
    const every = refreshMS[range];
    const timer = every ? setInterval(() => { if (document.visibilityState === "visible") void load(); }, every) : undefined;
    return () => { stopped = true; clearInterval(timer); };
  }, [kind, id, range, attempt]);

  const hasData = history?.series.some(s => s.values.some(v => v != null));
  return <>
    {hardwareSupported && hardwareFailure?.kind !== "capability" &&
      <HardwarePanel hardware={hardware} loading={hardwareLoading} failure={hardwareFailure} onRetry={rereadHardware}
        id={id} poolID={poolID} api={api} replaceSupported={systemDiskReplaceSupported(capabilities)} />}
    <section className="panel stats" aria-label="Usage history">
    <div className="section-heading">
      <h2>Usage history</h2>
      <div className="stats-controls">
        <label className="stats-toggle"><input type="checkbox" role="switch" checked={stacked} onChange={e => setStacked(e.target.checked)} /> Stacked</label>
        <label>Range <select value={range} onChange={e => setRange(e.target.value)}>{ranges.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        {!refreshMS[range] && <button type="button" disabled={loading} onClick={() => setAttempt(a => a + 1)}>Refresh</button>}
      </div>
    </div>
    {/* Gauges read the 10-minute history only. A longer range stores averages of up to a day,
        which would be painted as the current reading, band and all. */}
    {kind === "host" && range === "10m" && history && <div className="stats-gauges">{gauges(history, sensorThresholds(hardware)).map(g => <GaugeTile key={g.key} gauge={g} />)}</div>}
    {kind === "host" && <RealtimeStats kind={kind} id={id} api={api} stacked={stacked} limitMS={realtimeLimitMS} />}
    {failure?.kind === "capability" && <p className="stats-state" role="status">Upgrade Nex Core to see usage history.</p>}
    {failure?.kind === "stale" && <div className="stats-stale" role="status">{history ? "The host connection is stale. Showing last loaded usage history; it may be out of date." : "The host connection is stale. Reconnect Nex Core, then retry."} <button type="button" disabled={loading} onClick={() => setAttempt(a => a + 1)}>Retry</button></div>}
    {failure?.kind === "error" && <div className="alert" role="alert">{history ? `Could not refresh; showing the last loaded history. ${failure.message}` : failure.message} <button type="button" disabled={loading} onClick={() => setAttempt(a => a + 1)}>Retry</button></div>}
    {!history && !failure && <p className="empty" role="status">Connecting to host… the first load can take up to 30 seconds.</p>}
    {history && !hasData && <p className="empty">No history recorded yet. Usage is recorded while {kind === "vm" ? "the VM is running" : "the host agent runs"}.</p>}
    {history && hasData && <div className="stats-grid">
      {charts(history, stacked).map(c => <StatsChart key={c.group} chart={c} history={history} syncKey={`stats-${kind}-${id}`} hidden={hidden.current} plots={plots.current} />)}
    </div>}
    <p className="panel-note">Gaps mean no sample: the {kind === "vm" ? "VM was stopped" : "agent was not running"} or the host could not read the value. Hover a chart to read its values. Drag across a chart to zoom; double-click to reset. Select a series below a chart to hide or show it.</p>
    </section>
  </>;
}

let syncingScale = false;

// The hover readout for the chart under the pointer. Synced charts move their crosshair but keep
// their tooltip hidden, so only one readout is on screen at a time.
function tooltip(raw: { current: StatsSeries[] }, color: (i: number) => string): uPlot.Plugin {
  const tip = document.createElement("div");
  tip.className = "stats-tooltip";
  tip.hidden = true;
  let pointer = false;
  return {
    hooks: {
      init: u => {
        u.over.appendChild(tip);
        u.over.addEventListener("mouseenter", () => { pointer = true; });
        u.over.addEventListener("mouseleave", () => { pointer = false; tip.hidden = true; });
      },
      setCursor: u => {
        const { idx, left, top } = u.cursor;
        if (!pointer || idx == null || left == null || top == null || left < 0) { tip.hidden = true; return; }
        tip.replaceChildren();
        const time = document.createElement("div");
        time.className = "stats-tooltip-time";
        time.textContent = new Date(u.data[0][idx] * 1000).toLocaleString();
        tip.appendChild(time);
        raw.current.forEach((s, i) => {
          if (!u.series[i + 1]?.show) return;
          const row = document.createElement("div");
          row.className = "stats-tooltip-row";
          const swatch = document.createElement("span");
          swatch.className = "stats-swatch";
          swatch.style.background = color(i);
          const label = document.createElement("span");
          label.textContent = s.label;
          const value = document.createElement("strong");
          const v = s.values[idx];
          value.textContent = v == null ? "no sample" : formatValue(s.unit, v);
          row.append(swatch, label, value);
          tip.appendChild(row);
        });
        tip.hidden = false;
        const gap = 12, width = tip.offsetWidth, height = tip.offsetHeight;
        const x = left + gap + width > u.over.clientWidth ? left - gap - width : left + gap;
        const y = Math.min(Math.max(0, top - height / 2), Math.max(0, u.over.clientHeight - height));
        tip.style.transform = `translate(${Math.max(0, x)}px, ${y}px)`;
      },
    },
  };
}

function StatsChart({ chart, history, syncKey, hidden, plots, height = 220, zoom = true }: { chart: Chart; history: StatsHistory; syncKey: string; hidden: Set<string>; plots: Set<uPlot>; height?: number; zoom?: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const [, redraw] = useState(0);
  const shape = useRef("");
  const raw = useRef<StatsSeries[]>(chart.series);
  raw.current = chart.series;
  const units = [...new Set(chart.series.map(s => s.unit))];
  const x = timestamps(history);
  const data = [x, ...plotValues(chart.series, chart.stacked, hidden)] as uPlot.AlignedData;
  const hiddenKeys = chart.series.filter(s => hidden.has(s.key)).map(s => s.key).join(",");

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    if (!chart.series.length) {
      if (plot.current) { plots.delete(plot.current); plot.current.destroy(); plot.current = null; }
      shape.current = "";
      return;
    }
    const signature = `${chart.stacked}|${chart.series.map(s => s.key).join(",")}`;
    const u = plot.current;
    if (u && shape.current === signature) {
      // Keep an operator's zoom across a live refresh; follow new data otherwise.
      const sx = u.scales.x, zoomed = sx.min != null && sx.max != null && x.length > 0 && (sx.min > u.data[0][0] + 1 || sx.max < u.data[0][u.data[0].length - 1] - 1);
      const bands = stackBands(chart.series, chart.stacked, hidden);
      if (bands) { u.delBand(); bands.forEach(b => u.addBand(b)); }
      u.setData(data, !zoomed);
      return;
    }
    u?.destroy();
    if (u) plots.delete(u);
    shape.current = signature;
    const style = getComputedStyle(el);
    const color = (i: number) => style.getPropertyValue(`--series-${i % PALETTE_SIZE + 1}`).trim() || "#2456d9";
    const axis = (unit: string, side: 1 | 3): uPlot.Axis => ({ scale: unit, side, size: 70, stroke: style.getPropertyValue("--muted").trim(), grid: { show: side === 3, stroke: style.getPropertyValue("--line-soft").trim() }, values: (_u, ticks, _axis, _space, incr) => formatTicks(unit, ticks, incr), ...(unit.startsWith("bytes") ? { incrs: byteIncrs } : {}) });
    const opts: uPlot.Options = {
      width: el.clientWidth || 400,
      height,
      legend: { show: false },
      plugins: [tooltip(raw, color)],
      cursor: { sync: { key: syncKey }, drag: { x: zoom, y: false, setScale: zoom } },
      scales: Object.fromEntries(units.map(unit => [unit, { range: (_u: uPlot, min: number, max: number): uPlot.Range.MinMax =>
        chart.stacked && unit === "percent" ? [0, stackedPercentMax(raw.current, hidden)] : [unit === "percent" || chart.stacked ? 0 : Math.min(0, min), max > 0 ? max * 1.1 : 1] }])),
      // Compact realtime charts label seconds on one line; uPlot's default two-line date labels overlap at that width.
      axes: [{ stroke: style.getPropertyValue("--muted").trim(), grid: { stroke: style.getPropertyValue("--line-soft").trim() }, ...(zoom ? {} : { space: 60, values: (_u: uPlot, ticks: number[]) => ticks.map(t => new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })) }) }, // Compact charts drop the right-hand axis: its width left no room for time labels. A second unit
      // (IOPS beside bytes/s) keeps its own scale and is still read from the tooltip.
      ...units.slice(0, zoom ? 2 : 1).map((unit, i) => axis(unit, i ? 1 : 3))],
      series: [{}, ...chart.series.map((s, i): uPlot.Series => ({
        label: s.label,
        scale: s.unit,
        show: !hidden.has(s.key),
        stroke: color(i),
        width: 2,
        // Only the left-axis unit fills, so a second-axis series (IOPS beside bytes/s) never muddies the stack.
        fill: (chart.stacked && s.unit === units[0]) || chart.series.length === 1 ? color(i) + "40" : undefined,
        spanGaps: false,
        points: { show: false, size: 6, fill: color(i), filter: (u, si) => isolatedIndices(u.data[si] as (number | null)[]) },
      }))],
      bands: stackBands(chart.series, chart.stacked, hidden),
      hooks: {
        setSeries: [(u, si) => {
          const s = si == null ? undefined : raw.current[si - 1];
          if (!s || si == null) return;
          u.series[si].show ? hidden.delete(s.key) : hidden.add(s.key);
          redraw(n => n + 1);
        }],
        setScale: [(u, key) => {
          if (key !== "x" || syncingScale) return;
          syncingScale = true;
          for (const other of plots) if (other !== u) other.setScale("x", { min: u.scales.x.min!, max: u.scales.x.max! });
          syncingScale = false;
        }],
      },
    };
    const next = new uPlot(opts, data, el);
    plot.current = next;
    plots.add(next);
  }, [history, chart.stacked, hiddenKeys]);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const observer = new ResizeObserver(() => plot.current?.setSize({ width: el.clientWidth, height }));
    observer.observe(el);
    return () => { observer.disconnect(); if (plot.current) { plots.delete(plot.current); plot.current.destroy(); plot.current = null; } };
  }, []);

  return <figure className={`stats-chart stats-chart-${chart.group}`}>
    <figcaption>{chart.title}</figcaption>
    <div ref={box} hidden={!chart.series.length} />
    {chart.series.length > 0 && <div className="stats-legend">
      {chart.series.map((s, i) => <button key={s.key} type="button" className="stats-chip" aria-pressed={!hidden.has(s.key)}
        title={hidden.has(s.key) ? `Show ${s.label}` : `Hide ${s.label}`}
        onClick={() => plot.current?.setSeries(i + 1, { show: hidden.has(s.key) })}>
        <span className="stats-swatch" style={{ background: `var(--series-${i % PALETTE_SIZE + 1})` }} />{s.label}
      </button>)}
    </div>}
    {!chart.series.length && <p className="empty">{chart.group === "temperature" ? "No temperature sensors detected on this host." : "No samples reported for this chart."}</p>}
  </figure>;
}
