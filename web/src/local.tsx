// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import { createRoot } from "react-dom/client";
import { Console } from "./console";
import "./local.css";

type Session = { username: string; csrf_token: string };
type Host = { hostname: string; bridge: string; storage_dir: string; defaults: { cpus: number; memory_mib: number; disk_gib: number }; preflight: { ready: boolean; blockers: string[]; cpus: number; memory_mib: number; disk_gib: number } };
type VM = { id: string; name: string; state: string; blocked_reason?: string; managed: boolean; power_allowed: boolean };
type Operation = { id: string; vm_id: string; kind: string; status: string; stage: string; error?: string; controller_url?: string; controller_cert_sha256?: string; progress?: number };
type Draft = { name: string; hostname: string; cpus: number; memory_mib: number; disk_gib: number; network_mode: "dhcp" | "static"; address_cidr: string; gateway: string; dns: string; admin_password: string; site_name: string; bridge: string; storage_dir: string };
type UpdateStatus = { package: string; installed_version: string; available_version?: string; update_available: boolean; repo_url: string; phase?: string; progress?: number; message?: string; error?: string };
const initial: Draft = { name: "nex-controller", hostname: "nex-controller", cpus: 2, memory_mib: 4096, disk_gib: 32, network_mode: "dhcp", address_cidr: "", gateway: "", dns: "", admin_password: "", site_name: "Main site", bridge: "", storage_dir: "" };
function Brand() { return <div className="identity"><span className="brand" role="img" aria-label="Nex" /><span>CORE <small>LOCAL HOST</small></span></div>; }
function Links() { return <footer><span>Nex Core · Independent host access</span><nav aria-label="Resources"><a href="/api/docs/">API reference</a><a href="/source">Source code</a></nav></footer>; }
const fieldNames: Record<string, string> = { name: "VM name", hostname: "Controller hostname", cpus: "Virtual CPUs", memory_mib: "Memory (MiB)", disk_gib: "System disk (GiB)", network_mode: "IP configuration", address_cidr: "IPv4 address / prefix", gateway: "IPv4 gateway", dns: "DNS servers", site_name: "Site name", bridge: "LAN bridge", storage_dir: "Local storage" };
const label = (s: string) => s.replaceAll("_", " ").replaceAll("-", " ");
function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [connecting, setConnecting] = useState(true);
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [host, setHost] = useState<Host | null>(null);
  const [vms, setVMs] = useState<VM[]>([]);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [stale, setStale] = useState(false);
  const [view, setView] = useState<"overview" | "deploy" | "console" | "updates">("overview");
  const [consoleVM, setConsoleVM] = useState<VM | null>(null);
  const [draft, setDraft] = useState<Draft>(initial);
  const [review, setReview] = useState(false);
  const [action, setAction] = useState<{ vm: VM; kind: string } | null>(null);
  const [update, setUpdate] = useState<UpdateStatus | null>(null);
  const [updateNote, setUpdateNote] = useState("");
  const [updatePhase, setUpdatePhase] = useState<"idle" | "checking" | "applying" | "done" | "error">("idle");
  const requestID = useRef("");
  const heading = useRef<HTMLHeadingElement>(null);
  async function api<T>(path: string, method = "GET", body?: unknown): Promise<T> {
    const response = await fetch(`/api/local/v1/${path}`, { method, credentials: "same-origin", headers: { "Content-Type": "application/json", ...(session ? { "X-CSRF-Token": session.csrf_token } : {}) }, body: body === undefined ? undefined : JSON.stringify(body) });
    if (!response.ok) {
      if (response.status === 401) { setSession(null); setDraft(initial); setAction(null); }
      const detail = await response.json().catch(() => ({}));
      throw Object.assign(new Error(detail.error || `Request failed (${response.status}).`), { status: response.status });
    }
    return response.status === 204 ? undefined as T : response.json();
  }
  useEffect(() => { api<Session>("session").then(setSession).catch(e => { if (e.status !== 401) setError(String(e.message)); }).finally(() => setConnecting(false)); }, []);
  async function refresh() {
    try {
      const [h, vs, ops] = await Promise.all([api<Host>("host"), api<VM[]>("vms"), api<Operation[]>("deployments")]);
      setHost(h); setVMs(vs || []); setOperations(ops || []); setStale(false);
    } catch (e) { setStale(true); throw e; }
  }
  useEffect(() => {
    if (!session) return;
    let active = true;
    const poll = () => { if (active) refresh().catch(e => { if (active) setError(e.message); }); };
    poll(); const timer = window.setInterval(poll, 5000);
    return () => { active = false; window.clearInterval(timer); };
  }, [session]);
  useEffect(() => { heading.current?.focus(); }, [view, review, action]);
  async function login(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    try { setSession(await api<Session>("session", "POST", { username: "root", password })); } catch (e) { setError((e as Error).message); }
    finally { setPassword(""); setBusy(false); }
  }
  function beginDeploy() {
    if (!host) return;
    requestID.current = crypto.randomUUID(); setDraft({ ...initial, ...host.defaults, bridge: "", storage_dir: "" }); setReview(false); setView("deploy"); setError("");
  }
  async function deploy() {
    setBusy(true); setError("");
    const payload = { ...draft, address_cidr: draft.network_mode === "static" ? draft.address_cidr : "", gateway: draft.network_mode === "static" ? draft.gateway : "", dns: draft.network_mode === "static" ? draft.dns.split(/[\s,]+/).filter(Boolean) : [], request_id: requestID.current };
    setDraft(d => ({ ...d, admin_password: "" }));
    try { const op = await api<Operation>("deployments", "POST", payload); setOperations(ops => [op, ...ops.filter(o => o.id !== op.id)]); setView("overview"); setReview(false); await refresh(); }
    catch (e) { setError(`${(e as Error).message} Check deployment progress before trying again; this request keeps its identity.`); setReview(false); await refresh().catch(() => {}); }
    finally { setBusy(false); }
  }
  async function retryDeployment(id: string) {
    setBusy(true); setError("");
    try { await api<Operation>(`deployments/${encodeURIComponent(id)}/retry`, "POST", {}); await refresh(); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  async function power(e: FormEvent) {
    e.preventDefault(); if (!action || !host) return;
    setBusy(true); setError("");
    try { await api(`vms/${encodeURIComponent(action.vm.id)}/actions`, "POST", { request_id: requestID.current, action: action.kind }); setAction(null); await refresh(); }
    catch (e) { setError(`${(e as Error).message} Refresh observed state before retrying.`); }
    finally { setBusy(false); }
  }
  async function checkUpdates() {
    setBusy(true); setError(""); setUpdateNote(""); setUpdatePhase("checking");
    try { setUpdate(await api<UpdateStatus>("updates")); setUpdatePhase("idle"); }
    catch (e) { setUpdatePhase("error"); setError((e as Error).message); }
    finally { setBusy(false); }
  }
  async function applyUpdate() {
    setBusy(true); setError(""); setUpdateNote(""); setUpdatePhase("applying");
    try {
      await api("updates/apply", "POST", {});
      for (let i = 0; i < 600; i++) {
        await new Promise(r => setTimeout(r, 1000));
        const st = await api<UpdateStatus>("updates");
        setUpdate(st);
        if (st.phase === "done") {
          setUpdateNote(st.message || "Update complete");
          setUpdatePhase("done");
          return;
        }
        if (st.phase === "failed") {
          setUpdatePhase("error");
          setError(st.error || st.message || "Update failed");
          return;
        }
      }
      setUpdateNote("Still applying…");
      setUpdatePhase("done");
    } catch (e) { setUpdatePhase("error"); setError((e as Error).message); }
    finally { setBusy(false); }
  }
  const alert = error && <div className="alert" role="alert">{error}<button onClick={() => { setError(""); if (session) refresh().catch(e => setError(e.message)); else api<Session>("session").then(setSession).catch(e => { if (e.status !== 401) setError(e.message); }); }}>Retry connection</button></div>;
  if (connecting) return <main className="login"><Brand /><h1>Connecting to Nex Core…</h1><progress aria-label="Connecting" /></main>;
  if (!session) return <main className="login"><Brand /><p className="eyebrow">YOUR HOST, WITHIN REACH</p><h1>Local host access</h1><p className="muted">Deploy Nex Controller and recover local virtual machines, even when central management is unavailable.</p>{alert}<form onSubmit={login}><label>Username<input value="root" readOnly autoComplete="username" /></label><label>Password<input type="password" required value={password} onChange={e => setPassword(e.target.value)} autoComplete="current-password" autoFocus /></label><button className="primary" disabled={busy}>{busy ? "Signing in…" : "Sign in"}</button></form><p className="muted small">Use the root password set during installation.</p><Links /></main>;
  const existing = operations.some(o => o.kind === "deploy");
  return <><header><Brand /><div className="account"><span>{host?.hostname || "Local host"}</span><button disabled={busy} onClick={async () => { setBusy(true); try { await api("session", "DELETE"); setSession(null); setHost(null); setVMs([]); setOperations([]); setDraft(initial); setView("overview"); setAction(null); setUpdate(null); setUpdateNote(""); setError(""); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }}>Sign out</button></div></header><div className="shell"><aside>
      <p className="eyebrow">Host</p>
      <nav className="side-nav" aria-label="Host workspace">
        <button type="button" className={view === "overview" || view === "deploy" || view === "console" ? "selected" : ""} onClick={() => { setView("overview"); setDraft(d => ({ ...d, admin_password: "" })); }}>Overview</button>
        <button type="button" className={view === "updates" ? "selected" : ""} onClick={() => { setView("updates"); setUpdateNote(""); void checkUpdates(); }}>Updates</button>
      </nav>
    </aside><main className="workspace">{alert}{stale && <div role="status" className="warning">Host observations are stale. Power controls and deployment are disabled until the connection recovers.</div>}{!host ? <section><h1>Loading host observations…</h1><progress aria-label="Loading host" /></section> : <>
    <div className="page-title"><div><p className="eyebrow">NEX CORE / {view === "console" ? "CONSOLE" : view === "deploy" ? "CONTROLLER SETUP" : view === "updates" ? "UPDATES" : "OVERVIEW"}</p><h1 ref={heading} tabIndex={-1}>{view === "console" && consoleVM ? consoleVM.name : view === "deploy" ? (review ? "Review your appliance" : "Deploy Nex Controller") : view === "updates" ? "Software updates" : host.hostname}</h1><p className="muted">{view === "console" ? "Graphical console served directly by this host, independent of Nex Controller." : view === "deploy" ? "Set up your Controller on this host." : view === "updates" ? null : "Your virtual machines and local management, in one place."}</p></div></div>
    {view === "updates" ? <section><dl className="defaults"><div><dt>Installed</dt><dd>{update?.installed_version || "—"}</dd></div><div><dt>Available</dt><dd>{!update ? "—" : update.update_available ? (update.available_version || "—") : "Up to date"}</dd></div></dl>{(updatePhase === "checking" || updatePhase === "applying" || updatePhase === "done") && <p className="small" role="status">{updatePhase === "checking" ? "Checking…" : update?.phase === "downloading" ? `Downloading… ${update.progress ?? 0}%` : update?.phase === "installing" ? "Installing…" : update?.phase === "restarting" ? "Restarting…" : updatePhase === "applying" ? "Updating…" : (updateNote || "Done")}</p>}{(updatePhase === "checking" || updatePhase === "applying") && <progress aria-label={updatePhase === "checking" ? "Checking for updates" : "Applying update"} max={100} value={updatePhase === "applying" && typeof update?.progress === "number" ? update.progress : undefined} />}{update?.error && <p className="warning" role="status">{update.error}</p>}{updateNote && updatePhase === "done" && <p className="small" role="status">{updateNote}</p>}<div className="actions"><button disabled={busy} onClick={() => void checkUpdates()}>Check</button><button className="primary" disabled={busy || !update?.update_available} onClick={() => void applyUpdate()}>Update</button></div></section> : view === "console" && consoleVM ? <><Console key={consoleVM.id} name={consoleVM.name} open={async () => `wss://${location.host}/api/local/v1/vms/${encodeURIComponent(consoleVM.id)}/console`} /><div className="actions"><button onClick={() => { setView("overview"); setConsoleVM(null); }}>Back to overview</button></div></> : view === "overview" ? <><section className="hero"><div><p className="eyebrow">YOUR MANAGEMENT PLANE</p><h2>One place to run Nex.</h2><p>Deploy Controller as a virtual machine on this host. Its own Debian system starts automatically with the host.</p><button className="primary" disabled={busy || stale || !host.preflight.ready || existing} onClick={beginDeploy}>Deploy Controller <span aria-hidden="true">→</span></button>{existing && <p className="small">An appliance deployment is already recorded. Review its progress below.</p>}</div><dl className="defaults"><div><dt>Virtual CPUs</dt><dd>{host.defaults.cpus}</dd></div><div><dt>Memory</dt><dd>{host.defaults.memory_mib / 1024} <small>GiB</small></dd></div><div><dt>System disk</dt><dd>{host.defaults.disk_gib} <small>GiB</small></dd></div></dl></section>
    {!host.preflight.ready && <section className="warning"><h2>Deployment needs attention</h2><ul>{host.preflight.blockers.map(b => <li key={b}>{b}</li>)}</ul><p>Resolve the listed checks on this host, then refresh. Deployment requires a trusted appliance bundle, sufficient local storage, and an existing LAN bridge. This page does not change host networking.</p></section>}
    <section><div className="section-title"><div><h2>Virtual machines</h2><p className="muted">Observed on this host. A lost connection never means a VM has stopped.</p></div><button disabled={busy} onClick={() => refresh().catch(e => setError(e.message))}>Refresh</button></div>{vms.length ? <div className="table-scroll"><table><thead><tr><th>Virtual machine</th><th>Observed state</th><th>Local control</th><th>Console</th></tr></thead><tbody>{vms.map(vm => <tr key={vm.id}><td><strong>{vm.name}</strong><small className="muted">{vm.managed ? "Nex managed" : "Existing local VM"}</small></td><td><span className="status">{label(vm.state)}</span></td><td>{vm.power_allowed ? <div className="actions">{["start", "shutdown", "force-stop"].map(kind => <button key={kind} disabled={busy || stale} className={kind === "force-stop" ? "danger" : ""} onClick={() => { requestID.current = crypto.randomUUID(); setAction({ vm, kind }); setError(""); }}>{kind === "start" ? "Start" : kind === "shutdown" ? "Shut down" : "Force stop"}</button>)}</div> : <span className="muted">{vm.blocked_reason || "Local power control is unavailable"}</span>}</td><td>{vm.state === "running" ? <button disabled={busy || stale} onClick={() => { setConsoleVM(vm); setView("console"); setError(""); }}>Open console</button> : <span className="muted">Start the VM first</span>}</td></tr>)}</tbody></table></div> : <div className="empty"><h3>No virtual machines observed</h3><p>Your Controller appliance will appear here after deployment creates it.</p></div>}</section>
    <section><h2>Deployment progress</h2><p className="muted">Saved on the host; closing this page does not cancel the operation.</p>{operations.length ? operations.map(op => <article className="operation" key={op.id}><div className="section-title"><strong>{label(op.stage || op.kind)}</strong><span className="status">{label(op.status)}</span></div><progress aria-label={`Deployment progress ${op.id}`} max={100} value={typeof op.progress === "number" ? op.progress : undefined} /><p className="small mono">{op.id}</p>{op.error && <p className="warning">{op.error} Check this operation before creating or starting another appliance.</p>}{["needs_attention", "failed"].includes(op.status) && <button disabled={busy || stale} onClick={() => retryDeployment(op.id)}>Retry deployment</button>}{op.status === "ready" && op.controller_url?.startsWith("https://") && <div><a className="button primary" href={op.controller_url}>Open Nex Controller ↗</a>{op.controller_cert_sha256 && <p className="small">Before accepting the Controller certificate, compare its SHA-256 fingerprint with:<br /><code className="mono">{op.controller_cert_sha256.match(/.{1,2}/g)?.join(":").toUpperCase()}</code></p>}</div>}</article>) : <p className="empty">No deployment has been requested.</p>}</section></> : <section className="deployment"><ol className="steps" aria-label="Deployment steps"><li aria-current={!review ? "step" : undefined}>1 · Configure</li><li aria-current={review ? "step" : undefined}>2 · Review & deploy</li><li>3 · Follow progress</li></ol>{review ? <><h2>Ready to create your Controller</h2><p>Creates a new VM and disk on <strong>{host.hostname}</strong>. {draft.network_mode === "dhcp" ? "Your network will assign its IP address automatically." : "The chosen address must be unused and reachable from this host and your browser."}</p><dl className="review">{Object.entries(draft).filter(([key]) => key !== "admin_password" && (draft.network_mode === "static" || !["address_cidr", "gateway", "dns"].includes(key))).map(([key, value]) => <div key={key}><dt>{fieldNames[key] || label(key)}</dt><dd>{key === "network_mode" ? (value === "dhcp" ? "Automatic (DHCP)" : "Static IPv4") : value}</dd></div>)}</dl><p className="warning">Stopping this host or the Controller VM interrupts central management. Keep independent recovery access to this host.</p><div className="actions"><button disabled={busy} onClick={() => setReview(false)}>Back</button><button className="primary" disabled={busy || stale || !host.preflight.ready} onClick={deploy}>{busy ? "Submitting…" : "Create VM & install Controller"}</button></div></> : <form onSubmit={e => { e.preventDefault(); setReview(true); }}><fieldset disabled={busy}><legend>Appliance resources</legend><div className="form-grid">{([['name','VM name'],['hostname','Controller hostname'],['cpus','Virtual CPUs'],['memory_mib','Memory (MiB)'],['disk_gib','System disk (GiB)']] as const).map(([key,title]) => <label key={key}>{title}<input required type={typeof draft[key] === "number" ? "number" : "text"} min={key === "cpus" ? 2 : key === "memory_mib" ? 4096 : key === "disk_gib" ? 32 : undefined} value={draft[key]} onChange={e => setDraft({ ...draft, [key]: typeof draft[key] === "number" ? Number(e.target.value) : e.target.value })} /></label>)}<label>Local storage<select aria-label="Local storage" required value={draft.storage_dir} onChange={e => setDraft({ ...draft, storage_dir: e.target.value })}><option value="">Select storage explicitly</option><option value={host.storage_dir}>{host.storage_dir}</option></select></label></div><p className="muted small">Local storage stays on this host. It is not shared storage or a backup.</p></fieldset><fieldset disabled={busy}><legend>Network settings</legend><div className="form-grid"><label>LAN bridge<select aria-label="LAN bridge" required value={draft.bridge} onChange={e => setDraft({ ...draft, bridge: e.target.value })}><option value="">Select an existing bridge</option>{host.bridge && <option value={host.bridge}>{host.bridge}</option>}</select></label><label>IP configuration<select aria-label="IP configuration" value={draft.network_mode} onChange={e => setDraft({ ...draft, network_mode: e.target.value as Draft["network_mode"] })}><option value="dhcp">Automatic (DHCP)</option><option value="static">Static IPv4</option></select></label>{draft.network_mode === "static" && ([['address_cidr','IPv4 address / prefix','192.168.1.20/24'],['gateway','IPv4 gateway','192.168.1.1'],['dns','DNS servers','192.168.1.1']] as const).map(([key,title,placeholder]) => <label key={key}>{title}<input required value={draft[key]} placeholder={placeholder} onChange={e => setDraft({ ...draft, [key]: e.target.value })} /></label>)}</div><p className="muted small">{draft.network_mode === "dhcp" ? "Get an IP address automatically from your network." : "Use an unused IPv4 address. Separate DNS servers with commas."}</p></fieldset><fieldset disabled={busy}><legend>Controller workspace & access</legend><div className="form-grid"><label>Site name<input required value={draft.site_name} onChange={e => setDraft({ ...draft, site_name: e.target.value })} /></label><label>Controller administrator password<input required type="password" autoComplete="new-password" value={draft.admin_password} onChange={e => { const value = e.target.value; e.target.setCustomValidity(new TextEncoder().encode(value).length > 1024 ? "Password is too long." : ""); setDraft({ ...draft, admin_password: value }); }} /></label></div><p className="muted small">The Controller account is <strong>admin</strong>. Same password for the web UI and the appliance console.</p></fieldset><div className="actions"><button type="button" onClick={() => { setDraft(initial); setView("overview"); }}>Cancel</button><button className="primary" disabled={busy || stale || !host.preflight.ready}>Review deployment →</button></div></form>}</section>}
    {action && <section className="confirm" role="region" aria-label="Confirm VM power action"><h2 ref={heading} tabIndex={-1}>{label(action.kind)}: {action.vm.name}</h2><p>{action.kind === "force-stop" ? "Force stop cuts guest power immediately and may lose unwritten data." : action.kind === "shutdown" ? "Requests an orderly guest shutdown. Shutting down Controller interrupts central management." : "Starts this VM only when the host permits local ownership."}</p><form onSubmit={power}><div className="actions"><button type="button" disabled={busy} onClick={() => { setAction(null); }}>Cancel power action</button><button className={action.kind === "force-stop" ? "danger" : "primary"} disabled={busy || stale}>Confirm {label(action.kind)}</button></div></form></section>}
  </>}<Links /></main></div></>;
}
createRoot(document.getElementById("root")!).render(<App />);
