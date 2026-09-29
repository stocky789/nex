// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type ReactNode } from "react";
import type { API, Data, Host, Operation, Pool, Site } from "./types";
import { vmStateLabel, type VM } from "./managed";
import type { Report } from "./pool-members";
import { formatValue, refreshMS, type StatsHistory } from "./stats";
import { loadHostStats } from "./stats-cache";
import { storageAccess } from "./storage";
import { unfinished } from "./dock";
import { operationLabel, relativeTime } from "./labels";
import { Icon } from "./icons";
import { AttentionList, Badge, Meter, badgeTone, monotone, date } from "./ui";
import { backupPage, type BackupCatalogPage, type BackupRecoveryPoint, type BackupRun, type BackupRunPage } from "./backup-types";
import "./overview.css";

export { monotone };

// Each chart range reads the finest stored tier that covers it and keeps the steps inside its span.
const chartRanges = { "1H": { range: "24h", seconds: 3600 }, "1D": { range: "24h", seconds: 86400 }, "1W": { range: "30d", seconds: 7 * 86400 }, "1M": { range: "30d", seconds: 30 * 86400 } };
type ChartRange = keyof typeof chartRanges;
type Point = number | null;
type Line = { label: string; series: 1 | 2 | 3; values: Point[]; show: (v: number) => string };
const gib = (bytes: number) => Math.round(bytes / 1024 ** 3);
const last = (values: Point[]) => [...values].reverse().find(v => v != null) ?? null;
const vmMiB = (vm: VM) => vm.spec?.memory.size_mib || 0;
const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;
const stopped = (vm: VM) => vm.state === "shut off" || vm.state === "stopped" || vm.state === "shutoff";

export {
  poolUsage, sampleCounts, storageSeries, hostParts, vmParts, trend, attentionItems,
} from "./overview-model";
import { poolUsage, sampleCounts, storageSeries, hostParts, vmParts, trend, attentionItems, type Part } from "./overview-model";
// Which Overview tiles this browser has collapsed. A per-browser convenience only: a blocked or
// cleared store just shows every tile open.
const collapsedKey = "nex.overview.collapsed";
export function loadCollapsed(store?: Pick<Storage, "getItem">): string[] {
  try {
    const value = JSON.parse((store ?? localStorage).getItem(collapsedKey) || "[]");
    return Array.isArray(value) ? value.filter(v => typeof v === "string") : [];
  } catch { return []; }
}
export function saveCollapsed(ids: string[], store?: Pick<Storage, "setItem">) {
  try { (store ?? localStorage).setItem(collapsedKey, JSON.stringify(ids)); } catch {}
}
// Placement shows this many VM chips per host, then links to the host for the rest.
const chipLimit = 12;

// Overview is the landing screen for the selected pool, or site-wide totals for "All pools".
// Every figure is computed here from the hosts' current reports; nothing is stored.
export function Overview({ data, site, pool, hosts, vms, api, coreAvailable, loading, busy, onCreateSite, onAddHost }: {
  data: Data; site?: Site; pool?: Pool; hosts: Host[]; vms: VM[]; api: API; coreAvailable?: string; loading: boolean; busy: boolean; onCreateSite: () => void; onAddHost: () => void;
}) {
  const [range, setRange] = useState<ChartRange>("1D");
  const [histories, setHistories] = useState<StatsHistory[] | null>(null);
  const [members, setMembers] = useState<Report | null>(null);
  const [catalog, setCatalog] = useState<BackupRecoveryPoint[] | null>(null);
  const [catalogPage, setCatalogPage] = useState<BackupCatalogPage | null>(null);
  const [backupRuns, setBackupRuns] = useState<BackupRun[] | null>(null);
  const [runPage, setRunPage] = useState<BackupRunPage | null>(null);
  const [backupError, setBackupError] = useState(false);
  const [collapsed, setCollapsed] = useState(() => loadCollapsed());
  const hostKey = hosts.map(h => h.id).join(",");
  useEffect(() => {
    if (!hostKey) return;
    let live = true;
    const tier = chartRanges[range].range;
    const ids = hostKey.split(",").filter(Boolean);
    const load = () => loadHostStats(api, ids, tier)
      .then(map => { if (live) setHistories(Object.values(map)); });
    setHistories(null);
    load();
    // The same cadence as a host's Stats tab: live refresh for the 24 h tier, on demand beyond it.
    const timer = refreshMS[tier] ? setInterval(() => { if (document.visibilityState === "visible") load(); }, refreshMS[tier]) : undefined;
    return () => { live = false; clearInterval(timer); };
  }, [hostKey, range]);
  useEffect(() => {
    if (!pool) return;
    let live = true;
    const load = () => api<Report>(`pools/${encodeURIComponent(pool.id)}/members`).then(r => { if (live) setMembers(r); }, () => { if (live) setMembers(null); });
    setMembers(null);
    load();
    const timer = setInterval(() => { if (document.visibilityState === "visible") load(); }, 60000);
    return () => { live = false; clearInterval(timer); };
  }, [pool?.id]);
  useEffect(() => {
    if (!pool) {
      setCatalog(null);
      setCatalogPage(null);
      setBackupRuns(null);
      setRunPage(null);
      return;
    }
    let live = true;
    const load = () => {
      void api<BackupRecoveryPoint[] | BackupCatalogPage>(`pools/${encodeURIComponent(pool.id)}/backup-catalog`)
        .then(response => { if (live) { setCatalog(backupPage(response).items); setCatalogPage(Array.isArray(response) ? null : response); setBackupError(false); } }, () => { if (live) { setCatalog(null); setCatalogPage(null); setBackupError(true); } });
      void api<BackupRun[] | BackupRunPage>(`pools/${encodeURIComponent(pool.id)}/backup-runs`)
        .then(response => { if (live) { setBackupRuns(backupPage(response).items); setRunPage(Array.isArray(response) ? null : response); } }, () => { if (live) { setBackupRuns(null); setRunPage(null); } });
    };
    setCatalog(null);
    setCatalogPage(null);
    setBackupRuns(null);
    setRunPage(null);
    setBackupError(false);
    load();
    const timer = setInterval(() => { if (document.visibilityState === "visible") load(); }, 60000);
    return () => { live = false; clearInterval(timer); };
  }, [pool?.id]);

  if (loading) return <p role="status">Loading…</p>;
  if (!site) return <section className="panel empty"><h2>Start with a site</h2>
    <p>Organize your hosts by location. Create a site, then connect your first host.</p>
    <button type="button" className="primary" disabled={busy} onClick={onCreateSite}>Create site</button></section>;
  if (!hosts.length) return <section className="panel empty"><h2>Add your first host</h2>
    <p>Each Nex Core host you add becomes the master of its own pool. Pools are created automatically; you do not create them by hand.</p>
    <button type="button" className="primary" disabled={busy} onClick={onAddHost}>Add host</button></section>;

  const scopePools = pool ? [pool] : data.pools.filter(p => p.site_id === site.id);
  const hostName = (id: string) => data.hosts.find(h => h.id === id)?.hostname || id;
  const fresh = hosts.filter(h => h.connection === "fresh");
  const hostFresh = (id: string) => fresh.some(h => h.id === id);
  const known = (vm: VM) => vm.freshness === "fresh" && hostFresh(vm.host_id);
  const usage = poolUsage(histories || [], hosts, chartRanges[range].seconds);
  const cpuNow = last(usage.cpu);
  const vcpus = vms.filter(v => v.state === "running").reduce((sum, v) => sum + (v.spec?.cpu.count || 0), 0);
  const cores = hosts.reduce((sum, h) => sum + (h.inventory?.cpu_count || 0), 0);
  const storage = data.storage.filter(s => !s.tombstone && scopePools.some(p => p.id === s.pool_id));
  const capacity = storage.reduce((sum, s) => sum + (s.access?.total_bytes || 0), 0);
  const free = storage.reduce((sum, s) => sum + (s.access?.total_bytes ? s.access.available_bytes || 0 : 0), 0);
  const ops = data.operations.filter(o => hosts.some(h => h.id === o.host_id));
  const ioPeak = Math.max(0, ...usage.io.map(v => v ?? 0));
  const percent = (v: number) => `${Math.round(v)}%`;
  const lines: Line[] = [
    { label: "CPU", series: 1, values: usage.cpu, show: percent },
    { label: "Memory", series: 2, values: usage.memory, show: percent },
    { label: "Storage I/O", series: 3, values: usage.io.map(v => v == null || !ioPeak ? v : 100 * v / ioPeak), show: v => formatValue("bytes_per_second", ioPeak ? v * ioPeak / 100 : v) },
  ];
  // The CPU and memory trends cover the last hour of the loaded history; the week and month views
  // load hourly samples, so there the trends cover the last day instead and say so.
  const daily = range === "1W" || range === "1M";
  const hour = poolUsage(histories || [], hosts, daily ? 86400 : 3600);
  const trendWindow: [string, string] = daily ? ["24h", "over the last day"] : ["1h", "over the last hour"];
  const memoryShare = usage.memoryTotal ? 100 * usage.memoryUsed! / usage.memoryTotal : null;
  const storageShare = capacity ? 100 * (capacity - free) / capacity : null;

  const attention = attentionItems(data, hosts, vms, storage, coreAvailable);
  const failedBackups = runPage?.attention_count ?? backupRuns?.filter(r => ["failed", "missed", "needs_attention"].includes(r.status)).length ?? 0;
  if (failedBackups) attention.push({ tone: "bad", title: `${failedBackups} backup or restore runs need attention`, detail: "Review Core run outcomes and errors.", href: "#/backups" });
  const untestedPoints = catalogPage?.untested_count ?? catalog?.filter(p => p.last_test_result !== "passed").length ?? 0;
  if (untestedPoints) attention.push({ tone: "warn", title: `${untestedPoints} recovery points lack a passed restore test`, detail: "Test a point before relying on it.", href: "#/backups" });

  const sharedStorage = storage.filter(s => s.capabilities?.shared);
  const sharedHealthy = sharedStorage.filter(s => badgeTone(storageAccess(s)) === "ok" && !s.access?.error).length;
  const title = pool ? pool.name : `${site.name}, all pools`;
  const tile = (id: string) => ({ id, collapsed: collapsed.includes(id), onToggle: () => {
    const next = collapsed.includes(id) ? collapsed.filter(c => c !== id) : [...collapsed, id];
    setCollapsed(next);
    saveCollapsed(next);
  } });
  const failures = attention.filter(a => a.tone === "bad").length;
  const running = ops.filter(o => unfinished.has(o.status)).length;
  const staleHosts = hosts.length - fresh.length;
  const memoryNow = last(usage.memory);
  const backupPoints = catalog
    ? [...catalog].sort((a, b) => (b.captured_at || "").localeCompare(a.captured_at || ""))
    : null;
  const lastBackup = backupPoints?.[0] || null;
  const lastRun = runPage ? runPage.last_backup_run || null : backupRuns?.filter(r => ["backup-now", "backup-scheduled", "backup-policy-run-now"].includes(r.kind))
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at))[0] || null;
  const lastRestoreTest = catalogPage ? catalogPage.last_restore_test ? { last_tested_at: catalogPage.last_restore_test.tested_at, last_test_result: catalogPage.last_restore_test.result } : null : backupPoints
    ?.filter(p => p.last_tested_at)
    .sort((a, b) => (b.last_tested_at || "").localeCompare(a.last_tested_at || ""))[0] || null;
  const nights = Array.from({ length: 14 }, (_, i) => {
    const day = new Date();
    day.setUTCHours(0, 0, 0, 0);
    day.setUTCDate(day.getUTCDate() - (13 - i));
    const key = day.toISOString().slice(0, 10);
    return catalogPage ? catalogPage.backup_days_14.includes(key) : backupPoints?.some(p => (p.captured_at || "").slice(0, 10) === key) ?? false;
  });
  const backupsSummary = !pool
    ? "Choose a pool"
    : backupError
      ? "Backup catalog unavailable"
      : catalog == null
        ? "Loading…"
      : lastBackup
        ? `Last ${date(lastBackup.captured_at)}`
        : "No recovery points";

  return <div className="overview">
    <h1 className="sr-only">{title} overview</h1>
    <div className="ov-kpis">
      <Kpi label="Hosts" badge={staleHosts > 0 && <Badge value="stale" label={`${staleHosts} stale`} />}
        value={<>{fresh.length}<small>/{hosts.length}</small></>} unit="online" {...statusBar(hostParts(hosts))} />
      <Kpi label="Virtual machines" value={vms.filter(v => known(v) && v.state === "running").length} unit={`running of ${vms.length}`}
        {...statusBar(vmParts(vms, known))} />
      <Kpi label="CPU" value={cpuNow == null ? "—" : `${Math.round(cpuNow)}%`} unit={`${vcpus} / ${cores} vCPU`}
        bar={cpuNow != null && <Meter percent={cpuNow} label="CPU used" series={1} />}
        {...kpiTrend("Pool CPU", hour.cpu, ...trendWindow)} />
      <Kpi label="Memory" value={memoryShare == null ? "—" : `${Math.round(memoryShare)}%`}
        unit={usage.memoryTotal ? `${gib(usage.memoryUsed!)} / ${gib(usage.memoryTotal)} GiB` : "Not reported"}
        bar={memoryShare != null && <Meter percent={memoryShare} label="Memory used" series={2} />}
        {...kpiTrend("Pool memory", hour.memory, ...trendWindow)} />
      <Kpi label="Storage" value={storageShare == null ? "—" : `${Math.round(storageShare)}%`}
        unit={capacity ? `${formatValue("bytes", capacity - free)} / ${formatValue("bytes", capacity)}` : "Not reported"}
        bar={storageShare != null && <Meter percent={storageShare} label="Storage used" series={3} />}
        {...kpiTrend("Storage used", storageSeries(scopePools.map(p => p.id)), "since opened", "since this page opened")} />
    </div>

    <div className="ov-mid">
      <Tile {...tile("chart")} title={`${title} utilisation`}
        summary={`CPU ${cpuNow == null ? "not reported" : percent(cpuNow)} · memory ${memoryNow == null ? "not reported" : percent(memoryNow)}`}
        extra={<>
          <span className="ov-legend">{lines.map(l => <span key={l.label}><i style={{ background: `var(--s${l.series})` }} />{l.label}</span>)}</span>
          <span className="ov-seg" role="group" aria-label="Range">{(Object.keys(chartRanges) as ChartRange[]).map(r =>
            <button key={r} type="button" aria-pressed={r === range} onClick={() => setRange(r)}>{r}</button>)}</span>
        </>}>
        <UsageChart times={usage.times} lines={lines} range={range} loading={!histories}
          summary={`${title} utilisation over the last ${{ "1H": "hour", "1D": "day", "1W": "week", "1M": "month" }[range]}: CPU ${cpuNow == null ? "not reported" : `${Math.round(cpuNow)}%`}, memory ${memoryNow == null ? "not reported" : percent(memoryNow)} now; storage I/O peak ${formatValue("bytes_per_second", ioPeak)}.`} />
        <p className="ov-foot">Storage I/O is drawn against its peak in this range, {formatValue("bytes_per_second", ioPeak)}. No placement limit reported.</p>
      </Tile>
      <Tile {...tile("attention")} title="Needs attention" note={attention.length > 0 && <Badge value="warning" label={String(attention.length)} />}
        summary={attention.length ? `${count(attention.length, "item")}${failures ? ` · ${count(failures, "failure")}` : ""}` : "Nothing needs attention"}>
        {attention.length ? <AttentionList items={attention} /> : <p className="note">No reported issues in the available inventory. Check Backups for run history.</p>}
      </Tile>
    </div>

    <Tile {...tile("placement")} title="Placement" note={<span className="muted">Where every VM runs right now · block width follows memory</span>}
      summary={`${count(hosts.length, "host")} · ${count(vms.length, "VM")}${staleHosts ? ` · ${count(staleHosts, "stale host")}` : ""}`}>
      <div className="ov-map">{[...hosts].sort((a, b) => Number(scopePools.some(p => p.master_host_id === b.id)) - Number(scopePools.some(p => p.master_host_id === a.id))).map(h => {
        const master = data.pools.some(p => p.master_host_id === h.id);
        const onHost = vms.filter(v => v.host_id === h.id);
        const assigned = onHost.filter(v => !stopped(v)).reduce((sum, v) => sum + vmMiB(v), 0) * 1024 ** 2;
        const memory = h.inventory?.memory_bytes || 0;
        const isFresh = h.connection === "fresh";
        return <div key={h.id} className={`ov-host${isFresh ? "" : " stale"}`}>
          <header><a href={`#/hosts/${h.id}`}><span className={`dot ${isFresh ? "ok" : h.revoked ? "bad" : "warn"}`} aria-hidden="true" />{h.hostname}</a>
            <Badge value={master ? "master" : "member"} label={master ? "Master" : "Member"} /></header>
          <div className="ov-cap" role="meter" aria-label={`${h.hostname} memory assigned`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={memory ? Math.round(100 * assigned / memory) : 0}>
            <i style={{ width: `${memory ? Math.min(100, 100 * assigned / memory) : 0}%` }} /></div>
          <span className="ov-meta">{!pool && `${data.pools.find(p => p.id === h.pool_id)?.name} · `}{gib(assigned)} / {gib(memory)} GiB · {isFresh
            ? (usage.hostCPU[h.id] == null ? "CPU not reported" : `CPU ${Math.round(usage.hostCPU[h.id])}%`)
            : `Stale${h.last_seen ? `, last seen ${relativeTime(h.last_seen)}` : ""}`}</span>
          <div className="ov-vms">{onHost.length ? onHost.slice(0, chipLimit).map(v => {
            const state = !isFresh || v.freshness !== "fresh" ? "unknown" : stopped(v) ? "stopped" : v.state;
            const label = vmStateLabel(v, state !== "unknown");
            return <a key={v.id} href={`#/vms/${v.id}`} className={`ov-vm ${state}`} style={{ flexGrow: Math.max(1, vmMiB(v) / 1024) }} title={`${v.name}: ${label}, ${gib(vmMiB(v) * 1024 ** 2)} GiB`} aria-label={`${v.name}, ${label}`}>
              <span className={`dot ${state === "running" ? "ok" : state === "stopped" ? "" : "warn"}`} aria-hidden="true" />{v.name}</a>;
          }) : <span className="ov-meta">No VMs</span>}
            {onHost.length > chipLimit && <a href={`#/hosts/${h.id}`} className="ov-vm ov-more" aria-label={`${onHost.length - chipLimit} more VMs on ${h.hostname}`}>+{onHost.length - chipLimit} more</a>}</div>
        </div>;
      })}</div>
      <p className="ov-key"><span><span className="dot ok" aria-hidden="true" />Running</span><span><span className="dot warn" aria-hidden="true" />Paused or Unknown (host stale)</span><span><span className="ov-dashed" aria-hidden="true" />Stopped</span></p>
    </Tile>

    <div className="ov-row3">
      <Tile {...tile("tasks")} title="Recent tasks" extra={<a href="#/tasks">History</a>} summary={running ? `${running} running` : "Nothing running"}>
        {ops.length ? <ul className="ov-tasks">{ops.slice(0, 5).map((o: Operation) => <li key={o.id}><a href="#/tasks">
          <span>{unfinished.has(o.status) && <span className="pulse" aria-hidden="true" />}{operationLabel(o.kind)}<small>{hostName(o.host_id)} · {relativeTime(o.updated_at)}</small></span>
          <Badge value={o.status} label={unfinished.has(o.status) ? "Running" : undefined} />
        </a></li>)}</ul> : <p className="note">No tasks yet.</p>}
      </Tile>
      <Tile {...tile("backups")} title="Backups, last 14 nights" extra={<a href="#/backups">Backups</a>} summary={backupsSummary}>
        <div className="ov-nights" aria-hidden="true">{nights.map((on, i) => <i key={i} className={on ? "on" : undefined} />)}</div>
        <span className="sr-only">Completed backups on {nights.filter(Boolean).length} of the last 14 UTC days.</span>
        <dl className="ov-kv">
          <dt>Last run</dt>
          <dd>{!pool ? <span className="muted">Choose a pool</span> : lastRun ? <>{date(lastRun.updated_at)} · <Badge value={lastRun.status} /></> : backupRuns === null ? <span className="muted">Unavailable</span> : <span className="muted">Never</span>}</dd>
          <dt>Last restore test</dt>
          <dd>{!pool ? <span className="muted">Choose a pool</span> : backupError ? <span className="muted">Unavailable</span> : catalog == null ? <span className="muted">Loading…</span> : lastRestoreTest ? <>{date(lastRestoreTest.last_tested_at)}{lastRestoreTest.last_test_result ? ` · ${lastRestoreTest.last_test_result}` : ""}</> : <span className="muted">Never</span>}</dd>
        </dl>
      </Tile>
      <Tile {...tile("resilience")} title="Resilience"
        summary={`${scopePools.length > 1 ? "Masters" : "Master"} ${scopePools.map(p => hostName(p.master_host_id)).join(", ")} · shared storage ${sharedStorage.length ? `${sharedHealthy} of ${sharedStorage.length} healthy` : "none"}`}>
        <dl className="ov-kv">
          <dt>Survives loss of one host</dt><dd className="muted">Available with high availability (Phase 9)</dd>
          <dt>Fencing</dt><dd>{fencing(pool, members)}</dd>
          <dt>Shared storage</dt><dd>{sharedStorage.length ? <Badge value={sharedHealthy === sharedStorage.length ? "healthy" : "degraded"} label={`${sharedHealthy} of ${sharedStorage.length} healthy`} /> : <span className="muted">None registered</span>}</dd>
          <dt>{scopePools.length > 1 ? "Masters" : "Master"}</dt><dd>{scopePools.map((p, i) => <span key={p.id}>{i > 0 && ", "}<a href={`#/hosts/${p.master_host_id}`}>{hostName(p.master_host_id)}</a></span>)}</dd>
        </dl>
      </Tile>
    </div>
  </div>;
}

// A dashboard tile whose body the operator can collapse to its header and a one-line summary.
function Tile({ id, title, note, extra, summary, collapsed, onToggle, children }: {
  id: string; title: string; note?: ReactNode; extra?: ReactNode; summary: string; collapsed: boolean; onToggle: () => void; children: ReactNode;
}) {
  return <section className={`ov-tile${collapsed ? " collapsed" : ""}`} aria-labelledby={`ov-${id}`}>
    <h2><span id={`ov-${id}`}>{title}</span>{collapsed ? <span className="ov-summary">{summary}</span> : note}
      {!collapsed && extra && <span className="ov-extra">{extra}</span>}
      <button type="button" className="icon-button ov-toggle" aria-expanded={!collapsed} aria-label={`${collapsed ? "Expand" : "Collapse"} ${title}`} title={collapsed ? "Expand" : "Collapse"} onClick={onToggle}>
        <Icon kind="chevron" size={14} /></button></h2>
    {!collapsed && children}
  </section>;
}

function fencing(pool: Pool | undefined, report: Report | null): ReactNode {
  if (!pool) return <span className="muted">Choose a pool to see fencing</span>;
  if (!report) return <span className="muted">Not reported</span>;
  if (!report.authoritative) return <Badge value="unknown" label="Unknown, master not reporting" />;
  const fence = report.fence;
  if (!fence) return <span className="muted">Not reported by this pool master</span>;
  if (fence.total <= 1) return <span className="muted">Not needed with one host</span>;
  return <Badge value={fence.ready === fence.total ? "ready" : "warning"} label={`Ready for ${fence.ready} of ${fence.total} hosts`} />;
}

// Every KPI tile has the same rows (label, value, bar, legend) so they line up across the row.
function Kpi({ label, badge, value, unit, bar, foot }: { label: string; badge?: ReactNode; value: ReactNode; unit: string; bar?: ReactNode; foot?: ReactNode }) {
  return <div className="ov-kpi">
    <div className="ov-kpi-label">{label}{badge}</div>
    <div className="ov-kpi-value">{value} <small>{unit}</small></div>
    <div className="ov-kpi-bar">{bar}</div>
    <div className="ov-kpi-foot">{foot}</div>
  </div>;
}

// Segments sized by count, with a legend of the counts so colour never carries the state alone.
function statusBar(parts: Part[]) {
  const counts = new Map<string, { tone: string; count: number }>();
  for (const p of parts) counts.set(p.label, { tone: p.tone, count: (counts.get(p.label)?.count || 0) + p.count });
  return {
    bar: <div className="ov-status" role="img" aria-label={parts.map(p => p.title).join(", ") || "None"}>
      {parts.map((p, i) => <i key={i} className={p.tone} style={{ flexGrow: p.count }} title={p.title} />)}
    </div>,
    foot: <div className="ov-kpi-legend">{[...counts].map(([label, c]) => <span key={label}><span className={`dot ${c.tone}`} aria-hidden="true" />{c.count} {label.toLowerCase()}</span>)}</div>,
  };
}

// A change label in the tile's header, once there are two points; the utilisation chart below
// draws the trend itself.
function kpiTrend(what: string, values: Point[], window: string, windowText: string) {
  const t = trend(values);
  if (!t) return {};
  const summary = `${what} ${windowText}: ${Math.round(t.first)}% to ${Math.round(t.now)}%.`;
  return { badge: <span className="ov-change" title={summary}>{t.change ? `${t.change} ${window}` : "— steady"}</span> };
}

// The known runs of a series as points in the chart's 0–100 box: a gap in the data is a gap in
// the line, never a drop to zero.
export function runs(values: Point[]): [number, number][][] {
  const x = (i: number) => (100 * i) / (values.length - 1);
  const y = (v: number) => 100 - Math.min(100, Math.max(0, v));
  return values.reduce<[number, number][][]>((out, v, i) => {
    if (v != null) (values[i - 1] != null ? out[out.length - 1] : out[out.push([]) - 1]).push([x(i), y(v)]);
    return out;
  }, []);
}

// Averages consecutive steps in buckets of `size`, counted back from the newest so the last point
// is still "now". A bucket with no samples stays a gap; `counts` is the most samples any series had.
export function bucket(times: number[], series: Point[][], size: number) {
  const out = { starts: [] as number[], times: [] as number[], series: series.map(() => [] as Point[]), counts: [] as number[] };
  for (let end = times.length; end > 0; end -= size) {
    const start = Math.max(0, end - size);
    let most = 0;
    series.forEach((values, s) => {
      const known = values.slice(start, end).filter((v): v is number => v != null);
      most = Math.max(most, known.length);
      out.series[s].unshift(known.length ? known.reduce((a, b) => a + b, 0) / known.length : null);
    });
    out.starts.unshift(times[start]);
    out.times.unshift(times[end - 1]);
    out.counts.unshift(most);
  }
  return out;
}

// The chart draws in a stretched 0–100 box; its labels, end dots, crosshair and tooltip are HTML
// placed by percentage so text keeps its size at any width. Over a day, a week or a month the steps
// are averaged into about one point per 14 px, so the curve shows the trend rather than sample
// noise; the tooltip says it is an average and the footnote still gives the true peak.
function UsageChart({ times, lines, range, loading, summary }: { times: number[]; lines: Line[]; range: ChartRange; loading: boolean; summary: string }) {
  const [width, setWidth] = useState(0);
  const [at, setAt] = useState<number | null>(null);
  const plot = useRef<HTMLDivElement>(null);
  const hasData = lines.some(l => l.values.some(v => v != null));
  useEffect(() => {
    const el = plot.current;
    if (!el) return;
    const observer = new ResizeObserver(() => setWidth(el.clientWidth));
    observer.observe(el);
    return () => observer.disconnect();
  }, [loading, hasData]);
  if (loading) return <div className="ov-chart ov-chart-empty" role="status">Loading usage history…</div>;
  if (!hasData) return <div className="ov-chart ov-chart-empty">No usage history reported by the hosts in scope.</div>;

  const size = range !== "1H" && width ? Math.ceil(times.length / Math.max(1, Math.floor(width / 14))) : 1;
  const buckets = size > 1 ? bucket(times, lines.map(l => l.values), size) : null;
  const shown = buckets ? buckets.times : times;
  const series = buckets ? lines.map((l, s) => ({ ...l, values: buckets.series[s] })) : lines;
  const n = shown.length;
  const x = (i: number) => (100 * i) / (n - 1);
  const y = (v: number) => 100 - Math.min(100, Math.max(0, v));
  const ends = series.map(l => { const i = l.values.map(v => v != null).lastIndexOf(true); return { ...l, i, v: l.values[i] }; });
  const short = range === "1H" || range === "1D";
  const time = (t: number) => new Date(t).toLocaleString([], short ? { hour: "2-digit", minute: "2-digit" } : { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  const tick = (k: number) => {
    if (k === 4) return "Now";
    const t = new Date(shown[Math.round((k / 4) * (n - 1))]);
    return short ? t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : t.toLocaleDateString([], { month: "short", day: "numeric" });
  };
  const i = at == null ? null : Math.min(at, n - 1);
  // On a narrow chart the tooltip cannot sit beside the crosshair, so it pins to the far side.
  const tipPinned = width > 0 && width < 480;
  const tip = i == null ? null : {
    when: buckets && buckets.counts[i] > 1 ? `${time(buckets.starts[i])} – ${time(shown[i])}` : time(shown[i]),
    average: buckets && buckets.counts[i] > 1 ? `Average of ${buckets.counts[i]} samples` : "",
    values: series.map(l => ({ ...l, text: l.values[i] == null ? "Not reported" : l.show(l.values[i]!) })),
  };
  const pick = (e: React.PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    setAt(Math.round(Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)) * (n - 1)));
  };
  const key = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") setAt(null);
    else if (e.key === "ArrowLeft" || e.key === "ArrowRight") setAt(Math.min(n - 1, Math.max(0, i == null ? n - 1 : i + (e.key === "ArrowLeft" ? -1 : 1))));
    else return;
    e.preventDefault();
  };
  return <div className="ov-chart">
    <div className="ov-plot" ref={plot} tabIndex={0} role="img" aria-label={`${summary} Left and right arrow keys read each point.`}
      onPointerMove={pick} onPointerDown={pick} onPointerLeave={e => { if (e.pointerType === "mouse") setAt(null); }} onKeyDown={key} onBlur={() => setAt(null)}>
      {[0, 25, 50, 75, 100].map(v => <span key={v} className="ov-grid" style={{ top: `${y(v)}%` }}><b>{v}%</b></span>)}
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
        <path d={runs(series[0].values).map(r => `M${r[0][0]},100${monotone(r).replace("M", "L")}L${r[r.length - 1][0]},100Z`).join("")} fill={`var(--s${series[0].series})`} opacity={0.12} />
        {series.map(l => <path key={l.label} d={runs(l.values).map(monotone).join("")} fill="none" stroke={`var(--s${l.series})`} strokeWidth={1.5} strokeLinejoin="round" vectorEffect="non-scaling-stroke" />)}
      </svg>
      {ends.filter(e => e.v != null).map(e => <i key={e.label} className="ov-end" style={{ left: `${x(e.i)}%`, top: `${y(e.v!)}%`, background: `var(--s${e.series})` }} />)}
      {tip && <>
        <span className="ov-cross" style={{ left: `${x(i!)}%` }} aria-hidden="true" />
        {series.filter(l => l.values[i!] != null).map(l => <i key={l.label} className="ov-end" style={{ left: `${x(i!)}%`, top: `${y(l.values[i!]!)}%`, background: `var(--s${l.series})` }} />)}
        <div className={`ov-tip${tipPinned ? " pinned" : x(i!) > 55 ? " flip" : ""}`} style={tipPinned ? (x(i!) > 50 ? { left: 4 } : { right: 4 }) : { left: `${x(i!)}%` }} aria-hidden="true">
          <b>{tip.when}</b>
          {tip.values.map(l => <span key={l.label}><i style={{ background: `var(--s${l.series})` }} />{l.label}<em>{l.text}</em></span>)}
          {tip.average && <small>{tip.average}</small>}
        </div>
      </>}
    </div>
    <p className="sr-only" aria-live="polite">{tip && `${tip.when}: ${tip.values.map(l => `${l.label} ${l.text}`).join(", ")}${tip.average ? `. ${tip.average}` : ""}`}</p>
    <div className="ov-axis" aria-hidden="true">{[0, 1, 2, 3, 4].map(k => <span key={k}>{tick(k)}</span>)}</div>
  </div>;
}
