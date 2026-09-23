// SPDX-License-Identifier: AGPL-3.0-only
import { Fragment, useEffect, useRef, useState } from "react";
import type { API, Data, Pool } from "./main";
import { powerActions, powerBlocker, sentence, stateText, vmStateLabel, type VM } from "./managed";
import type { ConsoleMedia } from "./console";
import { guestNames } from "./vm-create";
import { VMPanel, TagList, stateBadge } from "./vm-panel";
import { readPreference, writePreference } from "./dock";
import { Badge, MiniBar, MissingPanel, Panel } from "./ui";
import { Icon } from "./icons";
import { formatValue } from "./stats";

export { consoleBlocker } from "./vm-panel";

const dotTone = (vm: VM) => vm.freshness !== "fresh" ? "warn" : vm.state === "running" ? "ok" : "off";
const fresh = (vm: VM) => vm.freshness === "fresh";
export function statusOf(vm: VM): string {
  return fresh(vm) ? vmStateLabel(vm) : "Unknown (host stale)";
}
// A VM needs attention when its state is unknown or unusual, or its creation did not succeed.
export const needsAttention = (vm: VM) => !fresh(vm) || !["running", "shut off"].includes(vm.state) || vm.status === "failed" || vm.status === "needs_attention";
export const views = [
  ["All", () => true],
  ["Running", (vm: VM) => fresh(vm) && vm.state === "running"],
  ["Needs attention", needsAttention],
  ["Stopped", (vm: VM) => fresh(vm) && vm.state === "shut off"],
] as const;

export const columns = ["State", "Pool", "Host", "IP address", "Guest OS", "vCPU", "Memory", "CPU", "Tags"] as const;
type Column = typeof columns[number];
const columnsKey = "nex.vm-columns";
// The hidden columns are this browser's choice; anything unreadable shows every column.
export function hiddenColumns(saved: string): Column[] {
  try { const value = JSON.parse(saved); return Array.isArray(value) ? columns.filter(c => value.includes(c)) : []; } catch { return []; }
}

// matches is the sidebar's search: name, any reported IP address or a tag.
export const matches = (vm: VM, query: string) => {
  const q = query.trim().toLowerCase();
  return !q || [vm.name, ...(vm.guest?.addresses || []).map(a => a.address), ...(vm.spec?.tags ?? [])].some(text => text.toLowerCase().includes(q));
};

type Bulk = { action: "start" | "shutdown"; ids: string[] };

// VMList is the VMs screen: a filter sidebar, view tabs, the table with bulk power actions, and
// the selected VM's slide-over panel. Filtering happens here, on the inventory already loaded.
export function VMList({ data, vms, pool, network, selectedID, operationID, api, reload, busy, unavailable, loading, onNotice, onCreateVM, mediaControls, installMedia }: {
  data: Data; vms: VM[]; pool?: Pool; network?: string; selectedID?: string; operationID?: string; api: API; reload: () => Promise<void>;
  busy: boolean; unavailable: boolean; loading: boolean; onNotice: (text: string, operationID?: string) => void; onCreateVM: () => void;
  mediaControls: (vm: VM) => ConsoleMedia; installMedia: (vm: VM) => { busy: boolean; note: string; onEject: () => void };
}) {
  const [query, setQuery] = useState("");
  const [group, setGroup] = useState(false);
  // Networks links its VM count here with that network already chosen in the Network facet.
  const [filters, setFilters] = useState<Record<string, string[]>>(network ? { Network: [network] } : {});
  const [view, setView] = useState<string>("All");
  const [checked, setChecked] = useState<string[]>([]);
  const [hidden, setHidden] = useState(() => hiddenColumns(readPreference(columnsKey)));
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [bulk, setBulk] = useState<Bulk | null>(null);
  const hostName = (id: string) => data.hosts.find(h => h.id === id)?.hostname || id;
  const poolName = (id: string) => data.pools.find(p => p.id === id)?.name || id;
  const facets: [string, (vm: VM) => string[]][] = [
    ["Status", vm => [statusOf(vm)]],
    ["Pool", vm => [poolName(vm.pool_id)]],
    ["Host", vm => [hostName(vm.host_id)]],
    ["Guest OS", vm => [vm.spec ? guestNames[vm.spec.guest_os] || "Not recorded" : "Not recorded"]],
    ["Network", vm => (vm.spec?.nics ?? []).map(n => data.networks.find(x => x.id === n.network_id)?.name || n.network_id)],
    ["Tags", vm => vm.spec?.tags ?? []],
  ];
  const viewTest = views.find(([name]) => name === view)?.[1] ?? views[0][1];
  const rows = vms.filter(vm => viewTest(vm) && matches(vm, query) && facets.every(([facet, values]) => !filters[facet]?.length || values(vm).some(v => filters[facet].includes(v))));
  const targets = rows.filter(vm => checked.includes(vm.id));
  const show = (column: Column) => !hidden.includes(column);
  const filtered = Boolean(query || Object.values(filters).some(v => v.length));

  function toggleFilter(facet: string, value: string, on: boolean) {
    setFilters({ ...filters, [facet]: on ? [...(filters[facet] || []), value] : (filters[facet] || []).filter(v => v !== value) });
  }
  function toggleColumn(column: Column, on: boolean) {
    const next = on ? hidden.filter(c => c !== column) : [...hidden, column];
    setHidden(next);
    writePreference(columnsKey, JSON.stringify(next));
  }
  // Closing returns focus to the row the panel belonged to, so the list keeps the keyboard.
  const close = () => {
    const id = selectedID;
    location.hash = "/vms";
    requestAnimationFrame(() => document.querySelector<HTMLElement>(`tr[data-vm="${CSS.escape(id || "")}"] a`)?.focus());
  };
  const vm = data.vms.find(v => v.id === selectedID);
  // A VM opened on its creation route shows that task, not "not found" or VM details with Edit
  // and Delete, until the create has succeeded.
  const createOperation = operationID && (!vm || (vm.status !== "succeeded" && vm.operation_id === operationID))
    ? data.operations.find(o => o.kind === "vm-create" && o.id === operationID)
    : undefined;
  const createBlocker = !pool ? "Choose a pool in the scope menu first." : unavailable ? "Controller inventory may be out of date. Use Retry above to reload it before creating a VM." : "";
  const row = (v: VM) => <tr key={v.id} data-vm={v.id} className={v.id === selectedID ? "sel" : undefined} onClick={() => { location.hash = `/vms/${v.id}`; }}>
    <td onClick={e => e.stopPropagation()}><input type="checkbox" aria-label={`Select ${v.name}`} checked={checked.includes(v.id)} onChange={e => setChecked(e.target.checked ? [...checked, v.id] : checked.filter(id => id !== v.id))} /></td>
    <td><span className={`dot ${unavailable ? "warn" : dotTone(v)}`} aria-hidden="true" /><a className="row-lead" href={`#/vms/${v.id}`}>{v.name}</a></td>
    {show("State") && <td>{fresh(v) && !unavailable ? statusOf(v) : stateBadge(v, unavailable)}</td>}
    {show("Pool") && <td>{poolName(v.pool_id)}</td>}
    {show("Host") && <td><a href={`#/hosts/${v.host_id}`} onClick={e => e.stopPropagation()}>{hostName(v.host_id)}</a></td>}
    {show("IP address") && <td className="mono">{v.guest?.addresses?.[0]?.address || "—"}</td>}
    {show("Guest OS") && <td>{v.guest?.os || (v.spec ? guestNames[v.spec.guest_os] || "Not recorded" : "—")}</td>}
    {show("vCPU") && <td>{v.spec?.cpu.count ?? "—"}</td>}
    {show("Memory") && <td>{v.spec ? formatValue("bytes", v.spec.memory.size_mib * 1024 ** 2) : "—"}</td>}
    {show("CPU") && <td><MiniBar percent={fresh(v) ? v.usage?.cpu_percent : null} /></td>}
    {show("Tags") && <td><TagList tags={v.spec?.tags} /></td>}
  </tr>;
  const span = 2 + columns.filter(show).length;
  const groups = data.hosts.filter(h => rows.some(v => v.host_id === h.id));
  // A VM whose host is not in the inventory still gets a group, so grouping never hides it.
  const unplaced = rows.filter(v => !groups.some(h => h.id === v.host_id));

  return <div className="vms">
    <h1 className="sr-only">Virtual machines</h1>
    <aside className={`vm-filters${filtersOpen ? " open" : ""}`} aria-label="Filters">
      <label className="search"><Icon kind="search" /><input type="search" placeholder="Search name, IP, tag" aria-label="Search name, IP address or tag" value={query} onChange={e => setQuery(e.target.value)} /></label>
      <label className="toggle-row">Group by host<input type="checkbox" role="switch" className="switch" checked={group} onChange={e => setGroup(e.target.checked)} /></label>
      {facets.map(([facet, values]) => {
        const counts = new Map<string, number>();
        vms.forEach(v => values(v).forEach(value => counts.set(value, (counts.get(value) || 0) + 1)));
        return <details key={facet} className="facet" open>
          <summary>{facet}<Icon kind="chevron" size={14} /></summary>
          {[...counts].sort(([a], [b]) => a.localeCompare(b)).map(([value, count]) => <label key={value}>
            <input type="checkbox" checked={filters[facet]?.includes(value) || false} onChange={e => toggleFilter(facet, value, e.target.checked)} />{value} <span className="muted">({count})</span>
          </label>)}
          {!counts.size && <p className="help">None in this scope</p>}
        </details>;
      })}
      <div className="foot">
        <button type="button" className="link" disabled={!filtered} onClick={() => { setQuery(""); setFilters({}); }}>Clear filters</button>
        <details className="columns">
          <summary className="link">Customize columns</summary>
          {columns.map(c => <label key={c}><input type="checkbox" checked={show(c)} onChange={e => toggleColumn(c, e.target.checked)} />{c}</label>)}
        </details>
      </div>
    </aside>
    <div className="vm-main">
      <div className="views" role="group" aria-label="Views">
        {views.map(([name, test]) => { const count = vms.filter(test).length; return <button key={name} type="button" aria-pressed={view === name} onClick={() => setView(name)}>
          {name}<span className={`count${name === "Needs attention" && count ? " warn" : ""}`}>{count}</span>
        </button>; })}
        <button type="button" className="filters-toggle" aria-expanded={filtersOpen} onClick={() => setFiltersOpen(!filtersOpen)}>Filters</button>
      </div>
      {targets.length > 0 && <div className="bulk" role="group" aria-label="Selected VMs">
        <b>{targets.length} selected</b>
        <button type="button" className="sm" disabled={busy || unavailable} onClick={() => setBulk({ action: "start", ids: targets.map(v => v.id) })}><Icon kind="start" />Start</button>
        <button type="button" className="sm" disabled={busy || unavailable} onClick={() => setBulk({ action: "shutdown", ids: targets.map(v => v.id) })}><Icon kind="shutdown" />Shut down</button>
        <button type="button" className="sm" disabled title="Available with snapshots (Phase 5)"><Icon kind="snapshot" />Snapshot</button>
        <button type="button" className="sm" disabled title="Available with live migration (Phase 7)"><Icon kind="migrate" />Migrate</button>
        <button type="button" className="link" onClick={() => setChecked([])}>Clear</button>
      </div>}
      {vms.length ? <div className="table-scroll"><table className="list">
        <thead><tr>
          <th><input type="checkbox" aria-label="Select all shown" checked={rows.length > 0 && targets.length === rows.length} ref={el => { if (el) el.indeterminate = targets.length > 0 && targets.length < rows.length; }} onChange={e => setChecked(e.target.checked ? rows.map(v => v.id) : [])} /></th>
          <th>Name</th>{columns.filter(show).map(c => <th key={c}>{c}</th>)}
        </tr></thead>
        <tbody>{group ? groups.map(h => <Fragment key={h.id}>
          <tr className="group"><td colSpan={span}>{poolName(h.pool_id)} › {h.hostname || h.id} {h.connection !== "fresh" && <Badge value="stale" />}</td></tr>
          {rows.filter(v => v.host_id === h.id).map(row)}
        </Fragment>).concat(unplaced.length ? [<Fragment key="unknown-host">
          <tr className="group"><td colSpan={span}>Unknown host <Badge value="unknown" /></td></tr>
          {unplaced.map(row)}
        </Fragment>] : []) : rows.map(row)}
          {!rows.length && <tr className="group"><td colSpan={span}>No VMs match this view and these filters.</td></tr>}
        </tbody>
      </table></div> : <p className="empty">{loading ? "Loading…" : "No virtual machines in this scope. Register storage and a network, upload an ISO, then create your first VM."}</p>}
      <div className="pager">
        <span>{rows.length ? `1–${rows.length}` : "0"} of {rows.length} virtual machines{rows.length < vms.length ? ` (${vms.length} in scope)` : ""}</span>
        <button type="button" className="primary" disabled={busy || Boolean(createBlocker)} title={createBlocker || undefined} onClick={onCreateVM}><Icon kind="vm" />Create VM</button>
      </div>
      <p className="note">A stale or unreachable host does not mean its VMs have stopped.</p>
    </div>
    {bulk && <BulkPower bulk={bulk} data={data} api={api} reload={reload} onNotice={onNotice} onDone={sent => { setBulk(null); if (sent) setChecked([]); }} />}
    {createOperation ? <Panel icon="vm" title="New VM" badge={<Badge value={createOperation.status} />} onClose={close}>
      <section className="tile" aria-label="Creation task">
        <h2>Creation {createOperation.status}</h2>
        <p className={createOperation.error ? "task-error" : undefined}>
          {createOperation.error ? sentence(createOperation.error) : "This VM appears here after its host reports it."}
        </p>
        <a href="#/tasks">Tasks</a>
      </section>
    </Panel> : selectedID && (vm ? <VMPanel vm={vm} data={data} api={api} reload={reload} busy={busy} unavailable={unavailable} onNotice={onNotice} media={mediaControls(vm)} eject={installMedia(vm)} onClose={close} />
      : <MissingPanel icon="vm" noun="VM" unavailable={unavailable} loading={loading} onClose={close} />)}
  </div>;
}

// bulkSkip says why a bulk power request is not sent to this VM; empty means it is sent. The
// Controller appliance is left to its own panel, whose confirmation explains the risk.
export function bulkSkip(vm: VM, action: Bulk["action"], host?: Data["hosts"][number]): string {
  const item = powerActions.find(p => p.action === action)!;
  if (vm.protected) return "Controller appliance: use its own panel.";
  return powerBlocker(vm, host) || (item.when(vm.state) ? "" : `Skipped: the VM is observed ${stateText(vm.state)}.`);
}

// BulkPower confirms one power action for every selected VM in a single dialog. Each VM gets its
// own request identity, kept until that request succeeds, so Confirm again after a failure retries
// only the failed ones and replays their original requests.
function BulkPower({ bulk, data, api, reload, onNotice, onDone }: { bulk: Bulk; data: Data; api: API; reload: () => Promise<void>; onNotice: (text: string) => void; onDone: (sent: boolean) => void }) {
  const item = powerActions.find(p => p.action === bulk.action)!;
  const dialog = useRef<HTMLDialogElement>(null);
  const requests = useRef<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [sent, setSent] = useState<string[]>([]);
  const [working, setWorking] = useState(false);
  useEffect(() => { dialog.current?.showModal(); }, []);
  const targets = bulk.ids.flatMap(id => data.vms.filter(v => v.id === id)).map(vm => ({ vm, skip: bulkSkip(vm, bulk.action, data.hosts.find(h => h.id === vm.host_id)) }));
  const pending = targets.filter(t => !t.skip && !sent.includes(t.vm.id));
  async function submit() {
    setWorking(true);
    const failed: Record<string, string> = {};
    const done: string[] = [];
    await Promise.all(pending.map(async ({ vm }) => {
      requests.current[vm.id] ??= crypto.randomUUID();
      try {
        await api(`vms/${encodeURIComponent(vm.id)}/power`, "POST", { request_id: requests.current[vm.id], action: bulk.action });
        delete requests.current[vm.id];
        done.push(vm.id);
      } catch (e) { failed[vm.id] = (e as Error).message; }
    }));
    const total = sent.length + done.length;
    setSent([...sent, ...done]); setErrors(failed); setWorking(false);
    if (total) onNotice(`${item.label} queued for ${total} VM${total === 1 ? "" : "s"}. Follow the progress in Tasks.`);
    await reload();
    if (!Object.keys(failed).length) dialog.current?.close();
  }
  return <dialog ref={dialog} className="confirm-dialog" aria-labelledby="bulk-power-title" onCancel={e => { if (working) e.preventDefault(); }} onClose={() => onDone(sent.length > 0)}>
    <form onSubmit={e => { e.preventDefault(); if (!working && pending.length) void submit(); }}>
      <h2 id="bulk-power-title">{item.label} {targets.length} VM{targets.length === 1 ? "" : "s"}?</h2>
      {item.confirm && <p>{item.confirm}</p>}
      <ul className="bulk-targets">{targets.map(({ vm, skip }) => <li key={vm.id}>
        <strong>{vm.name}</strong>
        <small className={errors[vm.id] ? "task-error" : undefined}>{errors[vm.id] || skip || (sent.includes(vm.id) ? "Requested" : "Will be requested")}</small>
      </li>)}</ul>
      <div className="actions">
        <button type="button" disabled={working} onClick={() => dialog.current?.close()}>Cancel</button>
        <button className="primary" disabled={working || !pending.length}>{working ? "Requesting…" : `${Object.keys(errors).length ? "Retry" : item.label} ${pending.length} VM${pending.length === 1 ? "" : "s"}`}</button>
      </div>
    </form>
  </dialog>;
}
