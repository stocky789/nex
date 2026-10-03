// SPDX-License-Identifier: AGPL-3.0-only
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import type { API, Data, Pool } from "./types";
import { powerActions, powerBlocker, sentence, stateText, vmStateLabel, type VM } from "./managed";
import type { ConsoleMedia } from "./console";
import { guestNames } from "./vm-create";
import { GuestOS } from "./guest-os";
import { VMPanel, TagList, stateBadge } from "./vm-panel";
import { tagStyle } from "./vm-edit";
import { readPreference, writePreference } from "./dock";
import { Badge, MiniBar, MissingPanel, Panel } from "./ui";
import { Icon } from "./icons";
import { formatValue } from "./stats";
import { VirtualPad, useVirtualWindow } from "./virtual-list";
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
  const [groupBy, setGroupBy] = useState<"none" | "host" | "tag">("none");
  // Networks links its VM count here with that network already chosen in the Network facet (by id).
  const [filters, setFilters] = useState<Record<string, string[]>>({});
  useEffect(() => {
    if (network) setFilters(f => ({ ...f, Network: [network] }));
  }, [network]);
  const [view, setView] = useState<string>("All");
  const [checked, setChecked] = useState<string[]>([]);
  const [hidden, setHidden] = useState(() => hiddenColumns(readPreference(columnsKey)));
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [bulk, setBulk] = useState<Bulk | null>(null);
  const [bulkSnapIDs, setBulkSnapIDs] = useState<string[] | null>(null);
  const hostNames = useMemo(() => new Map(data.hosts.map(h => [h.id, h.hostname || h.id])), [data.hosts]);
  const poolNames = useMemo(() => new Map(data.pools.map(p => [p.id, p.name || p.id])), [data.pools]);
  const networkNames = useMemo(() => new Map(data.networks.map(n => [n.id, n.name || n.id])), [data.networks]);
  const hostName = (id: string) => hostNames.get(id) || id;
  const poolName = (id: string) => poolNames.get(id) || id;
  const networkLabel = (id: string) => networkNames.get(id) || id;
  const checkedSet = useMemo(() => new Set(checked), [checked]);
  const viewTest = views.find(([name]) => name === view)?.[1] ?? views[0][1];
  const rows = useMemo(() => {
    const facetFns: [string, (vm: VM) => string[]][] = [
      ["Status", vm => [statusOf(vm)]],
      ["Pool", vm => [vm.pool_id]],
      ["Host", vm => [vm.host_id]],
      ["Guest OS", vm => [vm.spec ? guestNames[vm.spec.guest_os] || "Not recorded" : "Not recorded"]],
      ["Network", vm => (vm.spec?.nics ?? []).map(n => n.network_id)],
      ["Tags", vm => vm.spec?.tags ?? []],
    ];
    return vms.filter(vm => viewTest(vm) && matches(vm, query) && facetFns.every(([facet, values]) => !filters[facet]?.length || values(vm).some(v => filters[facet].includes(v))));
  }, [vms, view, query, filters, hostNames, poolNames, networkNames]);
  const targets = rows.filter(vm => checkedSet.has(vm.id));
  const facetDefs: [string, (vm: VM) => string[], (value: string) => string][] = [
    ["Status", vm => [statusOf(vm)], v => v],
    ["Pool", vm => [vm.pool_id], poolName],
    ["Host", vm => [vm.host_id], hostName],
    ["Guest OS", vm => [vm.spec ? guestNames[vm.spec.guest_os] || "Not recorded" : "Not recorded"], v => v],
    ["Network", vm => (vm.spec?.nics ?? []).map(n => n.network_id), networkLabel],
    ["Tags", vm => vm.spec?.tags ?? [], v => v],
  ];
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
  const scopedVM = vms.find(v => v.id === selectedID);
  const vm = scopedVM || data.vms.find(v => v.id === selectedID);
  const outOfScope = Boolean(selectedID && vm && !scopedVM);
  // A VM opened on its creation route shows that task, not "not found" or VM details with Edit
  // and Delete, until the create has succeeded.
  const createOperation = operationID && (!vm || (vm.status !== "succeeded" && vm.operation_id === operationID))
    ? data.operations.find(o => o.kind === "vm-create" && o.id === operationID)
    : undefined;
  const createBlocker = !pool ? "Choose a pool in the scope menu first." : unavailable ? "Controller inventory may be out of date. Use Retry above to reload it before creating a VM." : "";
  const row = (v: VM) => <tr key={v.id} data-vm={v.id} className={v.id === selectedID ? "sel" : undefined} onClick={() => { location.hash = `/vms/${encodeURIComponent(v.id)}`; }}>
    <td onClick={e => e.stopPropagation()}><input type="checkbox" aria-label={`Select ${v.name}`} checked={checked.includes(v.id)} onChange={e => setChecked(e.target.checked ? [...checked, v.id] : checked.filter(id => id !== v.id))} /></td>
    <td><span className={`dot ${unavailable ? "warn" : dotTone(v)}`} aria-hidden="true" /><a className="row-lead" href={`#/vms/${encodeURIComponent(v.id)}`}>{v.name}</a></td>
    {show("State") && <td>{fresh(v) && !unavailable ? statusOf(v) : stateBadge(v, unavailable)}</td>}
    {show("Pool") && <td>{poolName(v.pool_id)}</td>}
    {show("Host") && <td><a href={`#/hosts/${encodeURIComponent(v.host_id)}`} onClick={e => e.stopPropagation()}>{hostName(v.host_id)}</a></td>}
    {show("IP address") && <td className="mono">{v.guest?.addresses?.[0]?.address || "—"}</td>}
    {show("Guest OS") && <td><GuestOS vm={v} /></td>}
    {show("vCPU") && <td>{v.spec?.cpu.count ?? "—"}</td>}
    {show("Memory") && <td>{v.spec ? formatValue("bytes", v.spec.memory.size_mib * 1024 ** 2) : "—"}</td>}
    {show("CPU") && <td><MiniBar percent={fresh(v) ? v.usage?.cpu_percent : null} /></td>}
    {show("Tags") && <td><TagList tags={v.spec?.tags} /></td>}
  </tr>;
  const span = 2 + columns.filter(show).length;
  const groups = data.hosts.filter(h => rows.some(v => v.host_id === h.id));
  // A VM whose host is not in the inventory still gets a group, so grouping never hides it.
  const unplaced = rows.filter(v => !groups.some(h => h.id === v.host_id));
  // Flat lists above this size only paint the visible rows; grouping keeps the full tree.
  const virtualize = groupBy === "none" && rows.length > 40;
  const virt = useVirtualWindow(virtualize ? rows.length : 0, 36);
  // A VM with several tags appears under each of them; untagged VMs close the list.
  const tagNames = groupBy === "tag" ? [...new Set(rows.flatMap(v => v.spec?.tags ?? []))].sort() : [];
  const untagged = groupBy === "tag" ? rows.filter(v => !v.spec?.tags?.length) : [];
  const body = groupBy === "tag" ? <>
    {tagNames.map(t => <Fragment key={t}>
      <tr className="group"><td colSpan={span}><span className="tag" style={tagStyle(t)}>{t}</span> <span className="muted">({rows.filter(v => v.spec?.tags?.includes(t)).length})</span></td></tr>
      {rows.filter(v => v.spec?.tags?.includes(t)).map(v => <Fragment key={v.id}>{row(v)}</Fragment>)}
    </Fragment>)}
    {untagged.length > 0 && <>
      <tr className="group"><td colSpan={span}>No tags <span className="muted">({untagged.length})</span></td></tr>
      {untagged.map(row)}
    </>}
  </> : groupBy === "host" ? groups.map(h => <Fragment key={h.id}>
    <tr className="group"><td colSpan={span}>{poolName(h.pool_id)} › {h.hostname || h.id} {h.connection !== "fresh" && <Badge value="stale" />}</td></tr>
    {rows.filter(v => v.host_id === h.id).map(row)}
  </Fragment>).concat(unplaced.length ? [<Fragment key="unknown-host">
    <tr className="group"><td colSpan={span}>Unknown host <Badge value="unknown" /></td></tr>
    {unplaced.map(row)}
  </Fragment>] : []) : virtualize ? <>
    <VirtualPad height={virt.topPad} colSpan={span} />
    {rows.slice(virt.start, virt.end).map(row)}
    <VirtualPad height={virt.bottomPad} colSpan={span} />
  </> : rows.map(row);
  return <div className="vms">
    <h1 className="sr-only">Virtual machines</h1>
    <aside className={`vm-filters${filtersOpen ? " open" : ""}`} aria-label="Filters">
      <label className="search"><Icon kind="search" /><input type="search" placeholder="Search name, IP, tag" aria-label="Search name, IP address or tag" value={query} onChange={e => setQuery(e.target.value)} /></label>
      <div className="group-by" role="group" aria-label="Group by">Group by
        <span className="ov-seg">{(["none", "host", "tag"] as const).map(g => <button key={g} type="button" aria-pressed={groupBy === g} onClick={() => setGroupBy(g)}>{g === "none" ? "None" : g === "host" ? "Host" : "Tag"}</button>)}</span>
      </div>
      {facetDefs.map(([facet, values, label]) => {
        const counts = new Map<string, number>();
        vms.forEach(v => values(v).forEach(value => counts.set(value, (counts.get(value) || 0) + 1)));
        return <details key={facet} className="facet" open>
          <summary>{facet}<Icon kind="chevron" size={14} /></summary>
          {[...counts].sort(([a], [b]) => label(a).localeCompare(label(b))).map(([value, count]) => <label key={value}>
            <input type="checkbox" checked={filters[facet]?.includes(value) || false} onChange={e => toggleFilter(facet, value, e.target.checked)} />{label(value)} <span className="muted">({count})</span>
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
        <button type="button" className="sm" disabled={busy || unavailable} onClick={() => setBulkSnapIDs(targets.map(v => v.id))}><Icon kind="snapshot" />Snapshot</button>
        <button type="button" className="sm" disabled title="Available with live migration (Phase 7)"><Icon kind="migrate" />Migrate</button>
        <button type="button" className="link" onClick={() => setChecked([])}>Clear</button>
      </div>}
      {vms.length ? <div className="table-scroll" ref={el => { virt.scroller.current = el; }}><table className="list">
        <thead><tr>
          <th><input type="checkbox" aria-label="Select all shown" checked={rows.length > 0 && targets.length === rows.length} ref={el => { if (el) el.indeterminate = targets.length > 0 && targets.length < rows.length; }} onChange={e => setChecked(e.target.checked ? rows.map(v => v.id) : [])} /></th>
          <th>Name</th>{columns.filter(show).map(c => <th key={c}>{c}</th>)}
        </tr></thead>
        <tbody>{body}
          {!rows.length && <tr className="group"><td colSpan={span}>No VMs match this view and these filters.</td></tr>}
        </tbody>
      </table></div> : <p className="empty">{loading ? "Loading…" : "No virtual machines in this scope."}</p>}
      <div className="pager">
        <span>{rows.length ? `1–${rows.length}` : "0"} of {rows.length} virtual machines{rows.length < vms.length ? ` (${vms.length} in scope)` : ""}</span>
        <button type="button" className="primary" disabled={busy || Boolean(createBlocker)} title={createBlocker || undefined} onClick={onCreateVM}><Icon kind="vm" />Create VM</button>
      </div>
      <p className="note">A stale or unreachable host does not mean its VMs have stopped.</p>
    </div>
    {bulk && <BulkPower bulk={bulk} data={data} api={api} reload={reload} onNotice={onNotice} onDone={sent => { setBulk(null); if (sent) setChecked([]); }} />}
    {bulkSnapIDs && <BulkSnapshot ids={bulkSnapIDs} data={data} api={api} reload={reload} onNotice={onNotice} onDone={sent => { setBulkSnapIDs(null); if (sent) setChecked([]); }} />}
    {createOperation ? <Panel icon="vm" title="New VM" badge={<Badge value={createOperation.status} />} onClose={close}>
      <section className="tile" aria-label="Creation task">
        <h2>Creation {createOperation.status}</h2>
        <p className={createOperation.error ? "task-error" : undefined}>
          {createOperation.error ? sentence(createOperation.error) : "This VM appears here after its host reports it."}
        </p>
        <a href="#/tasks">Tasks</a>
      </section>
    </Panel> : selectedID && (outOfScope ? <Panel icon="vm" title={vm!.name} onClose={close}>
      <p className="empty">This VM is outside the current site or pool scope. Choose its site or pool in the scope menu to open it.</p>
    </Panel> : scopedVM ? <VMPanel key={scopedVM.id} vm={scopedVM} data={data} api={api} reload={reload} busy={busy} unavailable={unavailable} onNotice={onNotice} media={mediaControls(scopedVM)} eject={installMedia(scopedVM)} onClose={close} />
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
    if (total) onNotice(`${item.label} request submitted for ${total} VM${total === 1 ? "" : "s"}. Follow each task in Tasks.`);
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

const snapRequestID = () => {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return "snap" + Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("").slice(0, 20);
};

export function bulkSnapSkip(vm: VM, host?: Data["hosts"][number]): string {
  if (vm.protected) return "Controller appliance: use its own panel.";
  if (!host || host.revoked || host.connection !== "fresh") return "Skipped: the host is not freshly connected.";
  if (vm.freshness !== "fresh") return "Skipped: current VM state is unknown.";
  if (vm.state !== "shut off") return `Skipped: the VM is observed ${stateText(vm.state)}; stop it first.`;
  return "";
}

function BulkSnapshot({ ids, data, api, reload, onNotice, onDone }: {
  ids: string[]; data: Data; api: API; reload: () => Promise<void>;
  onNotice: (text: string) => void; onDone: (sent: boolean) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const requests = useRef<Record<string, string>>({});
  const sentAny = useRef(false);
  const [name, setName] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [sent, setSent] = useState<string[]>([]);
  const [working, setWorking] = useState(false);
  useEffect(() => { dialog.current?.showModal(); }, []);
  const targets = ids.flatMap(id => data.vms.filter(v => v.id === id)).map(vm => ({
    vm, skip: bulkSnapSkip(vm, data.hosts.find(h => h.id === vm.host_id)),
  }));
  const pending = targets.filter(t => !t.skip && !sent.includes(t.vm.id));
  const nameLocked = sent.length > 0 || working;
  async function submit() {
    const trimmed = name.trim();
    if (!trimmed || !pending.length) return;
    setWorking(true);
    const failed: Record<string, string> = {};
    const done: string[] = [];
    await Promise.all(pending.map(async ({ vm }) => {
      requests.current[vm.id] ??= snapRequestID();
      try {
        await api(`vms/${encodeURIComponent(vm.id)}/snapshots`, "POST", {
          request_id: requests.current[vm.id], name: trimmed,
        });
        delete requests.current[vm.id];
        done.push(vm.id);
      } catch (e) { failed[vm.id] = (e as Error).message; }
    }));
    if (done.length) sentAny.current = true;
    const total = sent.length + done.length;
    setSent([...sent, ...done]); setErrors(failed); setWorking(false);
    if (total) onNotice(`Snapshot request submitted for ${total} VM${total === 1 ? "" : "s"}. Follow each task in Tasks.`);
    await reload();
    if (!Object.keys(failed).length) dialog.current?.close();
  }
  return <dialog ref={dialog} className="confirm-dialog" aria-labelledby="bulk-snap-title" onCancel={e => { if (working) e.preventDefault(); }} onClose={() => onDone(sentAny.current)}>
    <form onSubmit={e => { e.preventDefault(); if (!working && pending.length && name.trim()) void submit(); }}>
      <h2 id="bulk-snap-title">Snapshot {targets.length} VM{targets.length === 1 ? "" : "s"}?</h2>
      <label>Name<input value={name} onChange={e => { setName(e.target.value); requests.current = {}; }} maxLength={64} required disabled={nameLocked} autoFocus /></label>
      <ul className="bulk-targets">{targets.map(({ vm, skip }) => <li key={vm.id}>
        <strong>{vm.name}</strong>
        <small className={errors[vm.id] ? "task-error" : undefined}>{errors[vm.id] || skip || (sent.includes(vm.id) ? "Requested" : "Will be requested")}</small>
      </li>)}</ul>
      <div className="actions">
        <button type="button" disabled={working} onClick={() => dialog.current?.close()}>Cancel</button>
        <button className="primary" disabled={working || !pending.length || !name.trim()}>{working ? "Requesting…" : `${Object.keys(errors).length ? "Retry" : "Snapshot"} ${pending.length} VM${pending.length === 1 ? "" : "s"}`}</button>
      </div>
    </form>
  </dialog>;
}
