// SPDX-License-Identifier: AGPL-3.0-only
// A staged management-network edit belongs to the host: it renders, applies, times and rolls
// back its own configuration. This panel submits one durable step per action and shows what the
// host reports. It never computes the outcome, and it never turns "not reporting" into a result.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { heldRequestID } from "./managed";
import { Badge } from "./ui";

type API = <T>(path: string, method?: string, body?: unknown, progress?: (loaded: number, total: number) => void, register?: (xhr: XMLHttpRequest) => void, signal?: AbortSignal) => Promise<T>;
type Connection = "fresh" | "stale" | "unknown" | "revoked";
export type NIC = { name: string; mac?: string; kind?: string; master?: string; parent?: string; addresses?: string[]; state?: string };
export type ManagementNIC = { name: string; mac: string; mode: string; vlan?: number; address?: string; gateway?: string; management?: boolean };
export type Spec = { interfaces: ManagementNIC[]; dns?: string[] };
export type ManagementRecord = {
  revision: number; stage: string; operation_id?: string; spec?: Spec; current?: Spec;
  observed_gateway?: string;
  deadline?: string; window_seconds?: number; seconds_remaining?: number; last_error?: string;
  requested_rollback?: boolean; observed_at?: string;
};
export type LastOperation = { operation_id: string; action?: string; status: string; error?: string; conflict?: boolean };
export type HostNetworkView = {
  host_id: string; hostname: string; connection: Connection; supported: boolean; state: string;
  management_network: ManagementRecord | null; nics?: NIC[];
  check?: { name: string; status: string; detail?: string } | null;
  last_operation?: LastOperation | null;
};

export const MIN_WINDOW_MINUTES = 2;
export const MAX_WINDOW_MINUTES = 30;
export const DEFAULT_WINDOW_MINUTES = 10;

const stageText: Record<string, string> = {
  none: "No change staged",
  staged: "Staged, not applied",
  applied: "Applied, waiting to be confirmed",
  confirmed: "Confirmed",
  cancelled: "Cancelled",
  "rolled-back": "Rolled back automatically",
  unknown: "Unknown",
};
// Text carries the state; the badge colour only repeats it.
const stageBadges: Record<string, string> = { none: "", staged: "queued", applied: "warning", confirmed: "ok", cancelled: "", "rolled-back": "failed", unknown: "unknown" };

// A rollback the operator asked for reached the same stage as an automatic one, but it is the
// outcome they chose: the host reports which it was, and neither the wording nor the badge
// calls a deliberate undo a failure.
export function stageLabel(state: string, requested = false) {
  if (state === "rolled-back" && requested) return { label: "Rolled back at your request", badge: "" };
  return { label: stageText[state] || state, badge: stageBadges[state] ?? "" };
}

export function windowError(minutes: number) {
  if (!Number.isInteger(minutes) || minutes < MIN_WINDOW_MINUTES || minutes > MAX_WINDOW_MINUTES) {
    return `The rollback window must be a whole number of minutes between ${MIN_WINDOW_MINUTES} and ${MAX_WINDOW_MINUTES}.`;
  }
  return "";
}

// ---------------------------------------------------------------- projections

// The interfaces an operator can build a management configuration from: the host's own physical
// and bonded NICs. Bridges, VLAN devices and VM ports are rendered by Core, not chosen here.
export function manageableNICs(nics?: NIC[]) {
  return (nics || []).filter(nic => nic.name !== "lo" && (!nic.kind || nic.kind === "physical" || nic.kind === "bond"));
}

// The configuration the edit form starts from: the host's live on-disk layout when Core reported
// it, otherwise the last change Spec. Spec alone is the staged/applied proposal, which must not
// overwrite the form after a rollback with something that is no longer running.
export function editSeed(record?: ManagementRecord | null): Spec | null | undefined {
  return record?.current ?? record?.spec;
}

export function describeNIC(nic: ManagementNIC) {
  if (nic.mode === "disabled") return "not configured";
  const parts = [nic.mode === "dhcp" ? "DHCP" : nic.address || "no address"];
  if (nic.gateway) parts.push(`via ${nic.gateway}`);
  if (nic.vlan) parts.push(`VLAN ${nic.vlan}`);
  if (nic.management) parts.push("management");
  return parts.join(" · ");
}

// The review diff: what the host reports now against what would be sent. "Now" is the host's own
// NIC inventory, because that is the configuration that is actually carrying traffic.
export function diffRows(nics: NIC[] | undefined, spec: Spec) {
  const observed = new Map(manageableNICs(nics).map(nic => [nic.name, nic] as const));
  const rows = spec.interfaces.map(nic => {
    const nowNIC = observed.get(nic.name);
    const now = nowNIC ? (nowNIC.addresses?.length ? nowNIC.addresses.join(", ") : "no address reported") : "not reported by this host";
    const proposed = describeNIC(nic);
    return { name: nic.name, now, proposed, changed: now !== proposed };
  });
  for (const [name, nic] of observed) {
    if (!spec.interfaces.some(i => i.name === name)) rows.push({ name, now: nic.addresses?.join(", ") || "no address reported", proposed: "left out of this configuration", changed: true });
  }
  return rows;
}

// What a staged change will give the operator once it is applied: the host's own recorded
// window, or an honest "not reported" for a host whose Core is older than that field.
export function stagedWindowText(windowSeconds?: number) {
  if (!windowSeconds) return `Once applied, the host restores its previous configuration by itself unless you confirm it within the rollback window it recorded (${MIN_WINDOW_MINUTES}\u2013${MAX_WINDOW_MINUTES} minutes); this host did not report which.`;
  const minutes = Math.max(1, Math.round(windowSeconds / 60));
  return `Once applied, the host restores its previous configuration by itself after ${minutes} minute${minutes === 1 ? "" : "s"} unless you confirm it.`;
}

export function countdownText(seconds: number | null) {
  if (seconds === null) return "";
  if (seconds <= 0) return "The rollback deadline has passed. The host restores its previous configuration by itself; this page shows it once the host reports again.";
  const minutes = Math.floor(seconds / 60);
  return `Rolls back in ${minutes}:${String(seconds % 60).padStart(2, "0")} unless you confirm it.`;
}

// ---------------------------------------------------------------- the edit form

type Row = { name: string; mac: string; mode: string; vlan: string; address: string; gateway: string };

export function rowsFromHost(nics: NIC[] | undefined, spec?: Spec | null): Row[] {
  return manageableNICs(nics).map(nic => {
    const planned = (spec?.interfaces || []).find(i => i.name === nic.name);
    return {
      name: nic.name,
      mac: nic.mac || planned?.mac || "",
      mode: planned?.mode || "disabled",
      vlan: planned?.vlan ? String(planned.vlan) : "",
      address: planned?.address || "",
      gateway: planned?.gateway || "",
    };
  });
}

export function specFromRows(rows: Row[], management: string, dns: string): Spec {
  return {
    interfaces: rows.map(row => ({
      name: row.name,
      mac: row.mac,
      mode: row.mode,
      ...(row.vlan.trim() ? { vlan: Number(row.vlan.trim()) } : {}),
      ...(row.mode === "static" && row.address.trim() ? { address: row.address.trim() } : {}),
      ...(row.mode === "static" && row.gateway.trim() ? { gateway: row.gateway.trim() } : {}),
      ...(row.name === management ? { management: true } : {}),
    })),
    dns: dns.split(/[\s,]+/).filter(Boolean),
  };
}

function EditForm({ view, busy, submitReady, fresh, onStage }: { view: HostNetworkView; busy: boolean; submitReady: boolean; fresh: boolean; onStage: (spec: Spec, windowSeconds: number) => void }) {
  const seed = editSeed(view.management_network);
  const [rows, setRows] = useState<Row[]>(() => rowsFromHost(view.nics, seed));
  const [management, setManagement] = useState(() => (seed?.interfaces || []).find(i => i.management)?.name || manageableNICs(view.nics)[0]?.name || "");
  const [dns, setDNS] = useState(() => (seed?.dns || []).join(" "));
  const [minutes, setMinutes] = useState(String(DEFAULT_WINDOW_MINUTES));
  const [showWindowError, setShowWindowError] = useState(false);
  const edit = (name: string, patch: Partial<Row>) => setRows(current => current.map(row => (row.name === name ? { ...row, ...patch } : row)));
  const spec = specFromRows(rows, management, dns);
  const windowProblem = windowError(Number(minutes));
  const liveDHCP = view.management_network?.current?.interfaces.find(nic => nic.management && nic.mode === "dhcp");
  const liveAddress = view.nics?.find(nic => nic.name === "br0")?.addresses?.find(address => /^\d+\.\d+\.\d+\.\d+\/\d+$/.test(address)) || "";
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setShowWindowError(true);
    if (windowProblem) return;
    onStage(spec, Number(minutes) * 60);
  }
  if (!rows.length) return <p className="empty">This host has reported no physical interfaces, so there is nothing to configure here yet.</p>;
  return <form className="host-network-form" onSubmit={submit}>
    <fieldset disabled={busy}>
      <legend>Interfaces</legend>
      <div className="table-scroll"><table>
        <thead><tr><th>Interface</th><th>Management</th><th>Addressing</th><th>VLAN</th><th>Address</th><th>Gateway</th></tr></thead>
        <tbody>{rows.map(row => {
          const showDHCP = fresh && row.mode === "dhcp" && liveDHCP?.name === row.name;
          return <tr key={row.name}>
          <th scope="row"><span className="host-network-nic"><b>{row.name}</b><small className="mono">{row.mac || "no MAC reported"}</small></span></th>
          <td className="host-network-mgmt"><label><input type="radio" name="management-nic" value={row.name} checked={management === row.name} onChange={() => setManagement(row.name)} /> <span className="sr-only">Use {row.name} for management</span></label></td>
          <td>
            <label className="sr-only" htmlFor={`mode-${row.name}`}>Addressing for {row.name}</label>
            <select id={`mode-${row.name}`} value={row.mode} onChange={event => edit(row.name, { mode: event.target.value })}>
              <option value="disabled">Not configured</option>
              <option value="dhcp">DHCP</option>
              <option value="static">Static</option>
            </select>
          </td>
          <td>
            <label className="sr-only" htmlFor={`vlan-${row.name}`}>VLAN tag for {row.name}</label>
            <input id={`vlan-${row.name}`} inputMode="numeric" autoComplete="off" placeholder="none" value={row.vlan} onChange={event => edit(row.name, { vlan: event.target.value })} />
          </td>
          <td>
            <label className="sr-only" htmlFor={`address-${row.name}`}>IPv4 address and prefix for {row.name}</label>
            <input id={`address-${row.name}`} inputMode="text" autoComplete="off" placeholder={row.mode === "dhcp" ? "Not reported" : "192.0.2.10/24"} disabled={row.mode !== "static"} value={showDHCP ? liveAddress : row.address} onChange={event => edit(row.name, { address: event.target.value })} />
          </td>
          <td>
            <label className="sr-only" htmlFor={`gateway-${row.name}`}>Default gateway for {row.name}</label>
            <input id={`gateway-${row.name}`} inputMode="text" autoComplete="off" placeholder={row.mode === "dhcp" ? "Not reported" : "192.0.2.1"} disabled={row.mode !== "static" || management !== row.name} value={showDHCP ? view.management_network?.observed_gateway || "" : row.gateway} onChange={event => edit(row.name, { gateway: event.target.value })} />
          </td>
        </tr>})}</tbody>
      </table></div>
      <p className="help">Exactly one interface carries management. Only that interface may have a gateway, and a static address is an IPv4 address with its prefix, for example 192.0.2.10/24.</p>
    </fieldset>
    <fieldset disabled={busy}>
      <legend>Name servers and rollback</legend>
      <label htmlFor="host-network-dns">DNS servers</label>
      <input id="host-network-dns" autoComplete="off" placeholder="192.0.2.53 192.0.2.54" aria-describedby="host-network-dns-help" value={dns} onChange={event => setDNS(event.target.value)} />
      <p className="help" id="host-network-dns-help">IPv4 addresses, separated by spaces or commas. At most eight.</p>
      <label htmlFor="host-network-window">Rollback window (minutes)</label>
      <input id="host-network-window" type="number" min={MIN_WINDOW_MINUTES} max={MAX_WINDOW_MINUTES} step={1} aria-describedby="host-network-window-help" value={minutes} onChange={event => setMinutes(event.target.value)} />
      <p className="help" id="host-network-window-help">After the change is applied the host restores the previous configuration by itself unless you confirm it within this window. {MIN_WINDOW_MINUTES} to {MAX_WINDOW_MINUTES} minutes; the host's own default is {DEFAULT_WINDOW_MINUTES}.</p>
    </fieldset>
    {showWindowError && windowProblem && <p className="alert" role="alert">{windowProblem}</p>}
    <div className="actions">
      <button className="primary" disabled={busy || !submitReady}>{busy ? "Submitting…" : "Stage this configuration"}</button>
    </div>
  </form>;
}

// ---------------------------------------------------------------- apply confirmation

export function ApplyDialog({ view, spec, minutes, busy, submitError, submitReady = true, readError = "", onConfirm, onClose }: {
  view: HostNetworkView; spec: Spec; minutes: number | null; busy: boolean; submitError: string; submitReady?: boolean; readError?: string;
  onConfirm: (confirmation: string) => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [typed, setTyped] = useState("");
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const name = view.hostname || view.host_id;
  const dns = spec.dns || [];
  const management = spec.interfaces.find(nic => nic.management);
  const matches = typed.trim().toLowerCase() === name.trim().toLowerCase();
  return <dialog ref={dialog} className="confirm-dialog host-network-dialog" aria-labelledby="apply-network-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="apply-network-title" ref={heading} tabIndex={-1}>Apply the management network change on {name}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p className="warning"><strong>{name} may become unreachable.</strong> Applying this change reconfigures the host's management addressing in place. If the new configuration does not work, this Controller and your browser lose contact with the host.</p>
    <p>The host restores the previous configuration by itself after {minutes === null ? `the rollback window recorded when this change was staged (${MIN_WINDOW_MINUTES}–${MAX_WINDOW_MINUTES} minutes)` : `${minutes} minutes`} unless you confirm the change.</p>
    <p className="help">If rollback fails, use the host console or BMC to restore management addressing.</p>
    <div className="table-scroll"><table>
      <caption>What will be applied</caption>
      <thead><tr><th>Interface</th><th>Now (reported by the host)</th><th>Proposed</th></tr></thead>
      <tbody>{diffRows(view.nics, spec).map(row => <tr key={row.name}>
        <th scope="row">{row.name}</th>
        <td>{row.now}</td>
        <td>{row.changed ? <strong>{row.proposed}</strong> : row.proposed}{row.changed ? <small>changed</small> : null}</td>
      </tr>)}</tbody>
    </table></div>
    <p>DNS servers: {dns.length ? dns.join(", ") : management?.mode === "dhcp" ? "no override; DHCP supplies DNS" : "none configured"}</p>
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    {readError && <p className="alert" role="alert">The current host report could not be read: {readError}. Retry the report before submitting.</p>}
    <form onSubmit={event => { event.preventDefault(); if (matches && !busy) onConfirm(typed.trim()); }}>
      <label htmlFor="apply-network-name">Type the host name <strong>{name}</strong> to apply</label>
      <input id="apply-network-name" autoComplete="off" autoCapitalize="none" spellCheck={false} disabled={busy} value={typed} onChange={event => setTyped(event.target.value)} />
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="primary" disabled={busy || !submitReady || !matches}>{busy ? "Submitting…" : "Apply and start the countdown"}</button>
      </div>
    </form>
  </dialog>;
}

// ---------------------------------------------------------------- the panel

export function HostNetwork({ hostID, api, unavailable }: { hostID: string; api: API; unavailable: boolean }) {
  const [view, setView] = useState<HostNetworkView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const [notice, setNotice] = useState("");
  const [applying, setApplying] = useState(false);
  const [remaining, setRemaining] = useState<number | null>(null);
  const held = useRef({ id: "", signature: "" });
  const reportRequest = useRef(0);
  const reportPending = useRef(0);

  async function refresh(force = false) {
    if (reportPending.current && !force) return;
    const request = ++reportRequest.current;
    reportPending.current = request;
    setLoading(true);
    try {
      const next = await api<HostNetworkView>(`hosts/${encodeURIComponent(hostID)}/network`);
      if (request !== reportRequest.current) return;
      setView(next);
      setError("");
      // The countdown is re-synced from the host's own report on every poll, and is not run at
      // all when the host is not reporting: an invented countdown would be a guess about a host
      // that may already have rolled back.
      setRemaining(next.state === "applied" && next.connection === "fresh" ? next.management_network?.seconds_remaining ?? 0 : null);
    } catch (e) { if (request === reportRequest.current) setError((e as Error).message); }
    finally {
      if (request === reportRequest.current) {
        reportPending.current = 0;
        setLoading(false);
      }
    }
  }
  useEffect(() => {
    const catchUp = () => { if (document.visibilityState === "visible") void refresh(); };
    catchUp();
    const timer = window.setInterval(catchUp, 10_000);
    document.addEventListener("visibilitychange", catchUp);
    window.addEventListener("online", catchUp);
    return () => {
      ++reportRequest.current;
      reportPending.current = 0;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", catchUp);
      window.removeEventListener("online", catchUp);
    };
  }, [hostID]);
  const ticking = remaining !== null;
  useEffect(() => {
    if (!ticking) return;
    const timer = window.setInterval(() => setRemaining(value => (value === null ? null : Math.max(0, value - 1))), 1000);
    return () => window.clearInterval(timer);
  }, [ticking]);

  async function step(action: string, extra: Record<string, unknown>, done: string) {
    if ((unavailable || error) && action !== "cancel") return;
    setBusy(true); setSubmitError("");
    const record = view?.management_network;
    const request_id = heldRequestID(held.current, `${hostID}\n${action}\n${record?.revision ?? 0}\n${record?.operation_id || ""}\n${JSON.stringify(extra)}`);
    // Every step after staging names the change it acts on — the one this page loaded from
    // the host's own report. The Controller checks it against what the host reports now and
    // refuses if the host has moved on, so a stale tab cannot act on a change it never saw.
    const change_id = action === "stage" ? undefined : record?.operation_id;
    try {
      await api(`hosts/${encodeURIComponent(hostID)}/network`, "POST", { request_id, action, ...(change_id ? { change_id } : {}), ...extra });
      setApplying(false);
      setNotice(done);
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setSubmitError(`${(e as Error).message} Submitting again repeats the same request; it never starts a second change.`);
    } finally { setBusy(false); }
  }

  return <HostNetworkPanel view={view} loading={loading} error={error} busy={busy} submitError={submitError} notice={notice}
    remaining={remaining} unavailable={unavailable} applying={applying}
    onRefresh={() => void refresh(true)} onStep={(action, extra, done) => void step(action, extra, done)}
    onApplyOpen={() => setApplying(true)} onApplyClose={() => { if (!busy) setApplying(false); }} />;
}

// The panel is a pure projection of one GET plus the in-flight request: every branch below is
// decided by what the host reported, which is also what makes it testable without a browser.
export function HostNetworkPanel({ view, loading, error, busy, submitError, notice, remaining, unavailable, applying, onRefresh, onStep, onApplyOpen, onApplyClose }: {
  view: HostNetworkView | null; loading: boolean; error: string; busy: boolean; submitError: string;
  notice: string; remaining: number | null; unavailable: boolean; applying: boolean;
  onRefresh: () => void; onStep: (action: string, extra: Record<string, unknown>, done: string) => void;
  onApplyOpen: () => void; onApplyClose: () => void;
}) {
  const state = error ? "unknown" : view?.state || "unknown";
  const record = view?.management_network || null;
  const fresh = !error && view?.connection === "fresh";
  const submitReady = !unavailable && !error;
  const stage = stageLabel(state, !!record?.requested_rollback);
  const last = view?.last_operation;
  // The step the record is in. "state" is "unknown" whenever the host is not reporting, so the
  // controls are decided by the record's own stage and each one is gated on freshness separately.
  const flow = state === "unknown" ? record?.stage || "none" : state;
  const staged = flow === "staged" && record?.spec;
  const applied = flow === "applied";
  // The host reports the window the operator chose when the change was staged, so the apply
  // dialog names the real one after a reload instead of repeating the default as if it were it.
  const windowMinutes = record?.window_seconds ? Math.max(1, Math.round(record.window_seconds / 60)) : null;

  return <section className="panel host-network-panel" aria-label="Management network">
    <div className="section-heading">
      <h2>Management network</h2>
      <span className="host-network-meta">
        <Badge value={stage.badge} label={stage.label} />
        {view && <span className="muted">revision {state === "unknown" ? "unknown" : record?.revision ?? 0}</span>}
        <button type="button" className="sm" disabled={loading} onClick={onRefresh}>{loading ? "Refreshing…" : "Refresh"}</button>
      </span>
    </div>
    {notice && <p className="notice" role="status">{notice}</p>}
    {error && <p className="alert" role="alert">This host could not be asked for its management network: {error}</p>}
    {error && view && <p className="warning" role="status">This is the last reported configuration. Its current state and rollback countdown are unknown until a fresh report arrives.</p>}
    {view && !view.supported && <p className="warning" role="status">This host does not support staged management network edits. They need management-network-v1 on this host; upgrade Nex Core. Its reported interfaces are still shown under Network interfaces.</p>}
    {view && view.supported && !fresh && !error && <p className="warning" role="status">
      The host is {view.connection}, so its management network state is unknown. {record?.deadline
        ? `Its last report said a change was ${record.stage} with a rollback deadline of ${record.deadline}. Whether it has already rolled back cannot be known until the host reports again.`
        : "Nothing here is shown as current; the values from its last report are not repeated as if they still applied."}
    </p>}
    {view && view.supported && <>
      {last?.conflict && <p className="alert" role="alert">{last.error} This host changed since you loaded it — reload.</p>}
      {!last?.conflict && last?.status === "failed" && <p className="alert" role="alert">Nex Core refused the last {last.action || "change"}: {last.error || "no message reported."}</p>}
      {fresh && flow === "rolled-back" && (record?.requested_rollback
        ? <p className="notice" role="status">The host restored its previous configuration because you asked it to. Nothing failed; stage the change again when you are ready.</p>
        : <p className="warning" role="status">The host restored its previous configuration by itself. Nex Core's reason: {record?.last_error || view.check?.detail || "none reported."}</p>)}
      {fresh && view.check && view.check.status !== "healthy" && <p className="task-error">Host check {view.check.name}: {view.check.status}{view.check.detail ? ` · ${view.check.detail}` : ""}</p>}
      {submitError && <p className="alert" role="alert">{submitError}</p>}

      {record?.spec && (flow === "applied" || flow === "confirmed") && <div className="table-scroll"><table>
        <caption>Management configuration the host reports</caption>
        <thead><tr><th>Interface</th><th>Configuration</th></tr></thead>
        <tbody>{record.spec.interfaces.map(nic => <tr key={nic.name}><th scope="row">{nic.name}</th><td>{describeNIC(nic)}</td></tr>)}</tbody>
      </table></div>}

      {applied && <div className="host-network-countdown">
        {error
          ? <p className="warning" role="status">The latest host report could not be read. Its rollback deadline cannot be confirmed; retry the report before acting.</p>
          : fresh
          ? <p className="warning" role="status">{countdownText(remaining)} If you do nothing the host restores its previous configuration; it also restores it if it reboots before you confirm.</p>
          : <p className="warning" role="status">Waiting for the host to reconnect over the new configuration. It cannot be confirmed until it reaches this Controller again; until then it is still on course to roll back at {record?.deadline || "its recorded deadline"}.</p>}
        <div className="actions">
          <button type="button" className="primary" disabled={busy || !submitReady || !fresh} onClick={() => onStep("confirm", {}, "Nex Core kept the new configuration. The rollback timer is stopped.")}>Keep this configuration</button>
          <button type="button" disabled={busy} onClick={() => onStep("cancel", {}, "Nex Core was asked to restore the previous configuration. The host performs it and reports the result.")}>Roll back now</button>
        </div>
      </div>}

      {staged && record?.spec && <div className="host-network-staged">
        <div className="table-scroll"><table>
          <caption>Staged change — review before applying. Nothing on the host has changed yet.</caption>
          <thead><tr><th>Interface</th><th>Now (reported by the host)</th><th>Proposed</th></tr></thead>
          <tbody>{diffRows(view.nics, record.spec).map(row => <tr key={row.name}>
            <th scope="row">{row.name}</th>
            <td>{row.now}</td>
            <td>{row.changed ? <strong>{row.proposed}</strong> : row.proposed}{row.changed ? <small>changed</small> : null}</td>
          </tr>)}</tbody>
        </table></div>
        <p>DNS servers: {(record.spec.dns || []).join(", ") || "none"}</p>
        <p className="help">{stagedWindowText(record.window_seconds)}</p>
        <div className="actions">
          <button type="button" className="primary" disabled={busy || !submitReady || !fresh} onClick={onApplyOpen}>Apply…</button>
          <button type="button" disabled={busy} onClick={() => onStep("cancel", {}, "The staged change was discarded. Nothing on the host was changed.")}>Discard staged change</button>
        </div>
        {!fresh && <p className="help">Applying needs a freshly connected host. Discarding does not: it is a durable command the host runs when it next reports.</p>}
      </div>}

      {view.connection === "fresh" && !staged && !applied && <EditForm key={`${view.host_id}:${record?.revision ?? 0}:${flow}`} view={view} busy={busy} submitReady={submitReady} fresh={fresh} onStage={(spec, windowSeconds) => onStep("stage", { spec, window_seconds: windowSeconds, expected_revision: record?.revision || 0 }, "The change is staged on the host. Review it below, then apply it.")} />}
    </>}
    {loading && !view && <p className="empty" role="status">Asking the host for its management network…</p>}
    {applying && view && record?.spec && <ApplyDialog view={view} spec={record.spec} minutes={windowMinutes} busy={busy} submitError={submitError} submitReady={submitReady} readError={error}
      onConfirm={confirmation => onStep("apply", { confirmation }, "The change is applied and the rollback countdown has started on the host. Confirm it once the host reaches this Controller again.")}
      onClose={onApplyClose} />}
  </section>;
}
