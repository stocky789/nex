// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { API, Data, Host } from "./types";
import type { Storage } from "./managed";
import { HostISOs, vmStateLabel } from "./managed";
import { Console } from "./console";
import { HardwareSections, Stats, formatValue, lastValue, mdArraysView, statsFailure, systemDiskReplaceSupported, HARDWARE_CAPABILITY, type HostHardware, type StatsFailure, type StatsHistory } from "./stats";
import { SystemDiskMirror, mdMemberFailed, type MDArray } from "./md";
import { HostNetwork } from "./host-network";
import { HostRestrictedBanner, fencingText } from "./pool-promotion";
import { maintenanceText, type Report } from "./pool-members";
import { checkLabel, relativeTime } from "./labels";
import { ImageUpdateBadge, imageUpdateNotice } from "./image-updates";
import { Activity, Badge, Banner, Meter, Panel, Sparkline, date } from "./ui";
import { Icon } from "./icons";
import "./hosts.css";

export const addresses = (host: Host) => host.inventory?.nics?.find(n => n.name === "br0")?.addresses?.filter(a => /^\d+\.\d+\.\d+\.\d+\/\d+$/.test(a)).map(a => a.replace(/\/\d+$/, "")) || [];

// Online / Stale / Revoked / Unknown: the host's own connection state in the words the console uses.
// While the inventory cannot be reloaded the last report is not current, so it is Unknown.
export function hostStatus(host: Host, unavailable = false) {
  if (host.revoked) return { value: "revoked", label: "Revoked" };
  if (unavailable) return { value: "unknown", label: "Unknown" };
  if (host.connection === "fresh") return { value: "online", label: "Online" };
  return host.connection === "stale" ? { value: "stale", label: "Stale" } : { value: "unknown", label: "Unknown" };
}

// "Nex Core 0.4.4 (Debian 13)" reads as 0.4.4; any other report is shown as reported.
export const coreVersion = (osVersion?: string) => osVersion?.match(/Nex Core\s+(\S+)/i)?.[1] || osVersion || "Not reported";

// Current CPU and memory use from the host's own 10-minute history; null when it has no recent sample.
export function hostUsage(history?: StatsHistory | null) {
  const values = (key: string) => history?.series.find(s => s.key === key)?.values ?? [];
  const total = values("memory.total");
  const memory = values("memory.used").map((used, i) => used != null && total[i] ? (100 * used) / total[i]! : null);
  const cpu = values("cpu.total");
  return { cpu: lastValue(cpu), memory: lastValue(memory), cpuValues: cpu, memoryValues: memory };
}

// One line per resource for the Performance tab. Disk and network add up every device's bytes/s;
// the sparkline is scaled to its own peak, so the current rate is always given as text beside it.
// Distinct from stats.tsx sparkRows (VM series shape).
export function hostSparkRows(history: StatsHistory) {
  const rate = (group: string) => {
    const series = history.series.filter(s => s.group === group && s.unit === "bytes_per_second");
    const length = Math.max(0, ...series.map(s => s.values.length));
    return Array.from({ length }, (_, i) => series.some(s => s.values[i] != null) ? series.reduce((sum, s) => sum + (s.values[i] ?? 0), 0) : null);
  };
  const usage = hostUsage(history), disk = rate("disk"), network = rate("network");
  const scaled = (values: (number | null)[]) => { const peak = Math.max(1, ...values.map(v => v ?? 0)); return values.map(v => v == null ? null : (100 * v) / peak); };
  return [
    { label: "CPU", series: 1 as const, values: usage.cpuValues, text: formatValue("percent", usage.cpu) },
    { label: "Memory", series: 2 as const, values: usage.memoryValues, text: formatValue("percent", usage.memory) },
    { label: "Disk", series: 3 as const, values: scaled(disk), text: formatValue("bytes_per_second", lastValue(disk)) },
    { label: "Network", series: 4 as const, values: scaled(network), text: formatValue("network_rate", lastValue(network)) },
  ];
}
/** @deprecated use hostSparkRows */
export const sparkRows = hostSparkRows;

// Uptime counts forward from when the host measured it; unknown when the observation time is missing.
export function uptime(host: Host) {
  const inv = host.inventory;
  if (inv?.uptime_seconds == null) return null;
  const observed = inv.observed_at && !inv.observed_at.startsWith("0001-") ? Date.parse(inv.observed_at) : NaN;
  if (!Number.isFinite(observed)) return null;
  const seconds = inv.uptime_seconds + Math.max(0, (Date.now() - observed) / 1000);
  if (!Number.isFinite(seconds)) return null;
  const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), minutes = Math.floor(seconds % 3600 / 60);
  return days ? `${days}d ${hours}h` : hours ? `${hours}h ${minutes}m` : `${minutes}m`;
}
const speedLabel = (mbps: number) => mbps >= 1000 ? `${mbps / 1000}G` : `${mbps}M`;
// The interfaces that are ports on the machine. A report with no kind leaves only lo out.
export const ports = (host: Host) => (host.inventory?.nics || []).filter(n => n.kind ? n.kind === "physical" : n.name !== "lo");

// What each disk is used for, from what the host reports: its ZFS pools' vdev layout and its md
// system-disk arrays. A disk the host reports no use for gets no role rather than a guessed one.
export function diskRoles(hostID: string, storage: Storage[], arrays: MDArray[], disks: string[]) {
  const roles: Record<string, { text: string; failed: boolean }[]> = {};
  const add = (device: string, text: string, failed: boolean) => {
    const disk = disks.find(d => new RegExp(`^${d}(p?\\d+)?$`).test(device));
    if (disk) (roles[disk] ||= []).push({ text, failed });
  };
  for (const s of storage) {
    if (s.kind !== "zfs") continue;
    const pool = s.zfs_host_access?.find(a => a.host_id === hostID)?.pool ?? (s.host_id === hostID ? s.access?.zfs_pool : undefined);
    const devices = pool?.devices || [];
    for (const d of devices) {
      if (!d.parent || devices.some(child => child.parent === d.name)) continue;
      const vdev = d.parent === pool?.name ? "stripe" : d.parent.replace(/-\d+$/, "");
      add(d.name, d.state === "ONLINE" ? vdev : `${vdev} · ${d.state.toLowerCase()}`, d.state !== "ONLINE");
    }
  }
  for (const a of arrays) for (const m of a.members) {
    add(m.device.replace(/^\/dev\//, "") || m.name, `system ${a.level === "raid1" ? "mirror" : a.level}`, mdMemberFailed(m));
  }
  return roles;
}

const tabs = ["Overview", "Hardware", "Performance", "Networking", "Storage", "Console", "Activity"] as const;

// The selected host's slide-over. Hardware and the pool's member report are read from the hosts
// when it opens; nothing here is kept after it closes.
export function HostPanel({ data, host, history, api, busy, unavailable, consoleBlocker, openHostConsole, onRefreshInventory, onReenroll, onRevoke, onClose }: {
  data: Data; host: Host; history?: StatsHistory; api: API; busy: boolean; unavailable: boolean; consoleBlocker: string;
  openHostConsole: (id: string) => Promise<string>; onRefreshInventory: () => void; onReenroll: () => void; onRevoke: () => void; onClose: () => void;
}) {
  const [tab, setTab] = useState<string>("Overview");
  const [hardware, setHardware] = useState<HostHardware | null>(null);
  const [hardwareFailure, setHardwareFailure] = useState<StatsFailure | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [report, setReport] = useState<Report | null>(null);
  const pool = data.pools.find(p => p.id === host.pool_id);
  const master = pool?.master_host_id === host.id;
  const status = hostStatus(host, unavailable);
  const ip = addresses(host)[0];
  const hardwareSupported = !!host.capabilities?.includes(HARDWARE_CAPABILITY);
  const inventoryRefresh = data.operations.filter(o => o.host_id === host.id && o.kind === "refresh-inventory" && o.status === "succeeded").map(o => o.id).join(",");

  // Membership refreshes while the panel is open; callers also key this panel by host.id.
  useEffect(() => {
    let stopped = false, inFlight = false;
    if (!host.pool_id) { setReport(null); return; }
    const load = async () => {
      if (inFlight || stopped) return;
      inFlight = true;
      try {
        const next = await api<Report>(`pools/${encodeURIComponent(host.pool_id)}/members`);
        if (!stopped) setReport(next);
      } catch { if (!stopped) setReport(null); }
      finally { inFlight = false; }
    };
    const refresh = () => { if (document.visibilityState === "visible") void load(); };
    void load();
    const timer = setInterval(refresh, 60_000);
    document.addEventListener("visibilitychange", refresh);
    window.addEventListener("online", refresh);
    return () => {
      stopped = true;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", refresh);
      window.removeEventListener("online", refresh);
    };
  }, [host.pool_id]);
  useEffect(() => {
    if (!hardwareSupported) return;
    let stopped = false, inFlight = false;
    // A failed reread keeps the last report on screen, marked as such.
    const load = async () => {
      if (inFlight || stopped) return;
      inFlight = true;
      try {
        const hw = await api<HostHardware>(`hosts/${encodeURIComponent(host.id)}/hardware`);
        if (!stopped) { setHardware(hw); setHardwareFailure(null); }
      } catch (e) { if (!stopped) setHardwareFailure(statsFailure(e)); }
      finally { inFlight = false; }
    };
    const refresh = () => { if (document.visibilityState === "visible") void load(); };
    void load();
    const timer = setInterval(refresh, 60_000);
    document.addEventListener("visibilitychange", refresh);
    window.addEventListener("online", refresh);
    return () => {
      stopped = true;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", refresh);
      window.removeEventListener("online", refresh);
    };
  }, [host.id, hardwareSupported, attempt, inventoryRefresh]);

  // Stable, so the system-disk panel's rebuild polling survives the list's usage refresh.
  const reread = useCallback(() => setAttempt(a => a + 1), []);
  const arrays = hardware?.md_arrays?.arrays ?? [];
  const inventoryDisks = host.inventory?.disks || [];
  const roles = diskRoles(host.id, data.storage, arrays, inventoryDisks.map(d => d.name));
  const disks = <div className="host-disks">{inventoryDisks.map(d => {
    const hw = hardware?.disks?.devices?.find(x => x.name === d.name);
    const kind = hw ? hw.transport === "nvme" ? "NVMe" : hw.rotational ? "HDD" : "SSD" : "";
    return <div className="host-disk" key={d.name} title={hw?.model}>
      <b>{d.name}</b>{formatValue("bytes", d.size_bytes)} {kind}
      {hardware?.system_disk?.booted_disk === `/dev/${d.name}` && <span className="ok">running system</span>}
      {(roles[d.name] || []).map(r => <span key={r.text} className={r.failed ? "bad" : "ok"}>{r.text}</span>)}
    </div>;
  })}</div>;
  const mdView = hardware && mdArraysView(hardware.md_arrays);
  const mirror = mdView === "unavailable" ? <section className="panel"><div className="section-heading"><h2>System disk</h2></div><p className="empty">{hardware?.md_arrays?.reason || "This section is not available on this host."}</p></section>
    : mdView && <SystemDiskMirror arrays={arrays} disks={hardware.disks?.devices ?? []} hostID={host.id} poolID={host.pool_id} api={api}
      supported={systemDiskReplaceSupported(host.capabilities)} reported={mdView !== "unreported"} systemDisk={hardware.system_disk} onRefresh={reread} />;
  const hardwareNote = !hardwareSupported ? "This host does not report hardware details. Upgrade Nex Core on it."
    : hardwareFailure ? hardwareFailure.kind === "stale" ? "The host is not reporting, so its hardware cannot be read." : `Could not read the hardware: ${hardwareFailure.message}`
    : hardware ? "" : "Reading hardware from the host…";

  return <Panel icon="host" title={host.hostname || host.id} badge={<Badge value={status.value} label={status.label} />} onClose={onClose}
    tabs={tabs} tab={tab} onTab={setTab} wide={tab !== "Overview" && tab !== "Activity"}
    sub={<>{master ? "Master" : "Member"} of {pool?.name || "an unknown pool"}{ip && <> · <span className="mono">{ip}</span></>}</>}
    actions={<>
      <button type="button" className="sm primary" disabled={busy || host.revoked} onClick={onRefreshInventory}>Refresh inventory</button>
      <button type="button" className="sm" disabled={Boolean(consoleBlocker)} title={consoleBlocker || undefined} onClick={() => setTab("Console")}>Console</button>
      <button type="button" className="sm" disabled={!host.pool_id} title="Maintenance is set in the pool's Members settings." onClick={() => { location.hash = `/pools/${encodeURIComponent(host.pool_id)}/members`; }}>Maintenance</button>
      <button type="button" className="sm" disabled={busy || !host.revoked} title={host.revoked ? undefined : "Only a host whose access was revoked can be re-enrolled."} onClick={onReenroll}>Re-enroll</button>
      <button type="button" className="sm danger" disabled={busy || host.revoked} onClick={onRevoke}>Revoke access</button>
    </>}>
    <div className="host-panel">
      {tab === "Overview" && <Overview data={data} host={host} master={master} poolName={pool?.name} history={history} hardware={hardware} hardwareNote={hardwareNote}
        report={report} disks={disks} api={api} onNetworking={() => setTab("Networking")} />}
      {tab === "Hardware" && <HardwareReport hw={hardware} note={hardwareNote} onRetry={hardwareSupported ? reread : undefined} />}
      {tab === "Performance" && <>
        <section className="host-card">
          <h3>Last 10 minutes</h3>
          {history ? hostSparkRows(history).map(row => <div className="host-spark" key={row.label}>
            <span className="muted">{row.label}</span>
            <Sparkline values={row.values.filter((v): v is number => v != null)} series={row.series} width={200} height={30} />
            <span>{row.text}</span>
          </div>) : <p className="muted">{host.connection === "fresh" ? "Reading usage from the host…" : "No current usage: the host is not reporting."}</p>}
        </section>
        <Stats key={host.id} kind="host" id={host.id} api={api} capabilities={host.capabilities} poolID={host.pool_id} hardwarePanel={false} initialHistory={history} hardware={hardware} skipHardwareFetch />
      </>}
      {tab === "Networking" && <><HostNetwork key={host.id} hostID={host.id} api={api} unavailable={unavailable} /><Interfaces host={host} /></>}
      {tab === "Storage" && <>
        <section className="panel">
          <div className="section-heading"><h2>Disks</h2></div>
          {inventoryDisks.length ? <div className="host-card-body">{disks}</div> : <p className="empty">{host.inventory ? "No disks reported." : "Waiting for the first inventory report."}</p>}
          <p className="panel-note">Observed device capacity. Pool membership does not make disks shared.</p>
        </section>
        {mirror}
        <HostISOs host={host} media={data.media} storage={data.storage} unavailable={unavailable} />
      </>}
      {tab === "Console" && <Console key={host.id} name={host.hostname || host.id} popout={`#/host-console/${encodeURIComponent(host.id)}`} disabled={consoleBlocker} open={() => openHostConsole(host.id)} />}
      {tab === "Activity" && <Activity entries={data.audit.filter(a => a.resource_id === host.id || a.resource_id.startsWith(host.id + "/"))} data={data} />}
    </div>
  </Panel>;
}

function KeyValues({ rows }: { rows: [string, ReactNode][] }) {
  return <dl className="host-kv">{rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v ?? "—"}</dd></div>)}</dl>;
}

function Overview({ data, host, master, poolName, history, hardware, hardwareNote, report, disks, api, onNetworking }: {
  data: Data; host: Host; master: boolean; poolName?: string; history?: StatsHistory; hardware: HostHardware | null; hardwareNote: string;
  report: Report | null; disks: ReactNode; api: API; onNetworking: () => void;
}) {
  const inv = host.inventory;
  const memory = hostUsage(host.connection === "fresh" ? history : null).memory;
  const member = report?.members?.find(m => m.host_id === host.id);
  const fencing = member && fencingText(member, report!.authoritative);
  const maintenance = member && maintenanceText(member, report!.authoritative);
  const model = [hardware?.system?.vendor, hardware?.system?.model].filter(Boolean).join(" ");
  const vms = data.vms.filter(v => v.host_id === host.id);
  return <>
    {host.pool_id && <HostRestrictedBanner key={host.id} poolID={host.pool_id} hostID={host.id} api={api} report={report as never} />}
    {host.pool_id && !report && <p className="muted" role="status">Pool membership has not been read. Fencing and maintenance status are unknown.</p>}
    <section className="host-card host-model">
      <span className="host-glyph"><Icon kind="host" size={22} /></span>
      <div><b>{model || "Model not reported"}</b><span className="muted">{model ? `${master ? "Master" : "Member"} of ${poolName || "an unknown pool"}` : hardwareNote || "The host reported no system model."}</span></div>
    </section>
    {host.connection !== "fresh" && <Banner tone="warn" role="status">{host.revoked
      ? "Access revoked. Re-enroll this host to resume reporting."
      : `${host.last_seen ? `Last report ${relativeTime(host.last_seen)}` : "No current report"}. VMs on this host are shown as Unknown, not stopped. Fence it or wait for it to reconnect before restarting them elsewhere. Values below are from its last report.`}</Banner>}
    {!inv ? <p className="empty">Waiting for the first inventory report.</p> : <>
      <section className="host-card">
        <h3>Network interfaces</h3>
        {ports(host).length ? <div className="host-ports">{ports(host).map(n => {
          const up = n.state === "up";
          return <span key={n.name} className={`host-port ${!up ? "down" : (n.speed_mbps ?? 0) >= 10000 ? "fast" : "up"}`}
            title={`${n.name} · ${n.mac} · ${up ? `link up${n.speed_mbps ? ` at ${speedLabel(n.speed_mbps)}b/s` : ""}` : "no link"}`}>{up && n.speed_mbps ? speedLabel(n.speed_mbps) : n.name}</span>;
        })}</div> : <p className="muted">No physical interfaces reported.</p>}
        <div className="host-legend"><span><i className="fast" />10 GbE or faster</span><span><i className="up" />Below 10 GbE or speed not reported</span><span><i className="down" />No link</span></div>
        <div className="host-links"><button type="button" onClick={onNetworking}>Interfaces &amp; VLANs</button></div>
      </section>
      <section className="host-card"><h3>Disks</h3>{inv.disks?.length ? disks : <p className="muted">No disks reported.</p>}</section>
      <section className="host-card">
        <h3>Memory <span className="muted">{formatValue("percent", memory)} of {formatValue("bytes", inv.memory_bytes)}</span></h3>
        {memory != null && <Meter percent={memory} label="Memory in use" series={2} />}
      </section>
      <section className="host-card">
        <h3>Virtual machines <span className="muted">{vms.length}</span></h3>
        {vms.length ? <table className="host-vms"><tbody>{vms.map(v => {
          const known = v.freshness === "fresh";
          return <tr key={v.id}>
            <td><span className={`dot ${!known ? "warn" : v.state === "running" ? "ok" : "off"}`} aria-hidden="true" /><a href={`#/vms/${encodeURIComponent(v.id)}`}>{v.name}</a></td>
            <td className="muted">{vmStateLabel(v)}</td>
            <td>{v.spec?.cpu?.count ?? "—"} vCPU</td>
            <td>{v.spec?.memory?.size_mib ? formatValue("bytes", v.spec.memory.size_mib * 1024 ** 2) : "—"}</td>
          </tr>;
        })}</tbody></table> : <p className="muted">No VMs on this host.</p>}
      </section>
      <section className="host-card">
        <KeyValues rows={[
          ["IP address", addresses(host)[0] ? <span className="mono">{addresses(host)[0]}</span> : null],
          ["CPU", `${inv.cpu_model} · ${inv.cpu_count} logical CPUs`],
          ["Nex Core", coreVersion(inv.os_version)],
          ...(imageUpdateNotice(host) ? [["Image update", <ImageUpdateBadge host={host} />] as [string, ReactNode]] : []),
          ["Uptime", uptime(host)],
          ["Fencing", fencing ? <Badge value={fencing.badge} label={fencing.label} /> : null],
          ["Maintenance", maintenance ? <Badge value={maintenance.badge} label={maintenance.label} /> : null],
          ["Virtualization", inv.virtualization?.join(", ") || "None reported"],
          ["Host identity", <span className="mono">{host.id}</span>],
          ["Last connection", date(host.last_seen)],
          ["Inventory observed", date(inv.observed_at)],
        ]} />
      </section>
      <section className="panel">
        <div className="section-heading"><h2>Health checks</h2><span className="muted">Last observed results</span></div>
        {inv.checks?.length ? <div className="check-list">{inv.checks.map(c => <div className="check" key={c.name}>
          <div>
            <strong>{checkLabel(c.name)}</strong>
            {checkLabel(c.name) !== c.name && <small className="mono">{c.name}</small>}
            <p>{c.error || "No error reported"}</p>
            <small>Observed {date(c.observed_at)} · Last success {date(c.last_success)}</small>
          </div>
          <Badge value={c.freshness === "fresh" ? c.status : c.freshness || "unknown"} />
        </div>)}</div> : <p className="empty">No health checks reported.</p>}
      </section>
    </>}
  </>;
}

// The host's own hardware report, read live. Each section can be unavailable on its own.
function HardwareReport({ hw, note, onRetry }: { hw: HostHardware | null; note: string; onRetry?: () => void }) {
  return <>
    <div className="host-actions">{onRetry && <button type="button" className="sm" onClick={onRetry}>Read again</button>}{hw && <span className="muted">Read {date(hw.observed_at)}</span>}</div>
    {!hw ? <p className={note.startsWith("Reading") ? "empty" : "warning"} role="status">{note}</p> : <>
      {note && <p className="warning" role="status">{note} Showing the last report read.</p>}
      <HardwareSections hw={hw} />
    </>}
  </>;
}

// Every interface the host reports: uplinks with what they carry, then bridges, VLAN devices and VM ports.
function Interfaces({ host }: { host: Host }) {
  const nics = host.inventory?.nics || [];
  const uplinks = nics.filter(n => !n.kind || n.kind === "physical" || n.kind === "bond");
  const carried = (name: string) => [
    ...nics.filter(b => b.kind === "bridge" && nics.some(p => p.name === name && p.master === b.name)).map(b => `untagged → ${b.name}${b.addresses?.length ? ` (${b.addresses.join(", ")})` : ""}`),
    ...nics.filter(v => v.kind === "vlan" && v.parent === name).map(v => `VLAN ${v.name.slice(name.length + 1)}${v.master ? ` → ${v.master}` : ""}`),
  ];
  return <section className="panel host-network">
    <div className="section-heading"><h2>Network interfaces</h2></div>
    {nics.length ? <>
      {uplinks.map(n => <div className="resource" key={n.name}>
        <strong>{n.name}</strong><Badge value={n.state} />
        <p>{carried(n.name).join(" · ") || (n.addresses?.length ? n.addresses.join(", ") : "Not used by any network")}</p>
        <small className="mono">{n.mac}</small>
      </div>)}
      <details>
        <summary>All interfaces ({nics.length}) · bridges, bonds, VLAN devices and VM ports</summary>
        {nics.map(n => <div className="resource" key={n.name}>
          <strong>{n.name}</strong><Badge value={n.state} />
          <p>{n.kind ? `${n.kind} · ` : ""}{n.master ? `port of ${n.master} · ` : ""}{n.parent ? `on ${n.parent} · ` : ""}{n.addresses?.join(", ") || "No addresses reported"}</p>
          <small className="mono">{n.mac}</small>
        </div>)}
      </details>
    </> : <p className="empty">{host.inventory ? "No interfaces reported." : "Waiting for the first inventory report."}</p>}
  </section>;
}
