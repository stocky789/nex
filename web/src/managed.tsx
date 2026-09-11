// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { Guest } from "./console";
import { Icon } from "./icons";
import { VMWizard, adapterNames, busNames, cpuNames, describeNetwork, guestNames, readyHost, type DiskRow, type NICRow } from "./vm-create";
import { VMEditor, editBlocker } from "./vm-edit";
import { StorageRepositories } from "./storage";

export type Registration = { id: string; pool_id: string; host_id: string; name: string; operation_id: string; status: string; error?: string };
export type StorageSource = { nfs?: { server: string; export: string; directory?: string }; zfs?: { pool: string; dataset: string; mount_path: string }; smb?: { server: string; share: string; directory?: string }; iscsi?: { portal: string; target_iqn: string; lun: number; wwid: string; filesystem_uuid: string; exclusive_target: boolean } };
// `member_access` is a Core observation from the pool master. It is deliberately
// separate from repository configuration: Controller uses it only to render and
// preflight placement; Core rechecks access and writer safety for the operation.
export type Storage = Registration & { path: string; kind?: string; source?: StorageSource; identity?: Record<string, unknown>; generation?: number; tombstone?: boolean; credential_present?: boolean; capabilities?: { vm_disk?: boolean; iso?: boolean; thin?: boolean; thick?: boolean; shared?: boolean; eligible_host_ids?: string[] }; access?: { status?: string; readable?: boolean; writable?: boolean; available_bytes?: number; total_bytes?: number; observed_at?: string; error?: string }; member_access?: { repository_id: string; host_id: string; generation: number; identity: string; readable: boolean; writable: boolean; qemu_writable: boolean; locking: boolean; observed_at?: string; error?: string }[] };
export type Network = Registration & { kind: string; source: string; uplink?: string; vlan?: number };
export type Media = Registration & { storage_id: string; size_bytes: number; sha256: string };
// Arrays are nullable because Go marshals an empty slice as null; render accordingly.
export type VMSpec = { revision?: number; description: string; guest_os: string; cpu: { count: number; sockets: number; cores: number; threads: number; model: string }; memory: { mode: string; size_mib: number }; disks: DiskRow[] | null; nics: NICRow[] | null; boot: { firmware: string; machine: string; media_boot_order: number }; media_id: string; start_after_create: boolean; autostart: boolean };
export type VM = Registration & { spec?: VMSpec; state: string; freshness: string; role: string; protected: boolean; guest?: Guest | null };
type NIC = { name: string; mac: string; kind?: string; master?: string; parent?: string; state: string; addresses?: string[] | null };
export type Host = { id: string; pool_id: string; hostname: string; connection: string; revoked: boolean; capabilities?: string[]; inventory: { cpu_count: number; cpu_models?: string[] | null; memory_bytes: number; nics?: NIC[] | null } | null };
type API = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Props = { account: string; poolID: string; poolName: string; masterHostID: string; tab: string; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[]; vms: VM[]; api: API; reload: () => Promise<void>; setTab: (tab: string) => void; unavailable: boolean; uploading: boolean; startIsoUpload: <T>(path: string, file: File) => Promise<T>; createVM: boolean; onCreateVMShown: () => void };
const GiB = 1024 ** 3;
const status = (value: string) => <span className={`badge ${value}`}>{value || "unknown"}</span>;
const task = (value: Registration) => <>{status(value.status)}{value.error && <p className="task-error">{value.error}</p>}{value.operation_id && <small><a href="#/tasks">Task {value.operation_id}</a></small>}</>;

// Each registration form belongs to the pool tab that opens it; "" means no form is open.
export const formTab = (form: string) => ({ vm: "Virtual machines", network: "Networking", iso: "ISO library" })[form] || "";

type Pending = Record<string, { payload: string; id: string }>;
function pendingRequests(key: string): Pending {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || "{}");
    if (value && typeof value === "object" && Object.values(value).every(v => v && typeof v === "object" && "id" in v && typeof v.id === "string" && "payload" in v && typeof v.payload === "string")) return value;
  } catch {}
  return {};
}

export function ManagedVMs({ account, poolID, poolName, masterHostID, tab, hosts: allHosts, storage: allStorage, networks: allNetworks, media: allMedia, vms: allVMs, api, reload, setTab, unavailable, uploading, startIsoUpload, createVM, onCreateVMShown }: Props) {
  const hosts = allHosts.filter(h => h.pool_id === poolID);
  const storage = allStorage.filter(s => s.pool_id === poolID);
  const networks = allNetworks.filter(n => n.pool_id === poolID);
  const media = allMedia.filter(m => m.pool_id === poolID);
  const vms = allVMs.filter(v => v.pool_id === poolID);
  const persistenceKey = `nex-pending/${account}/${poolID}`;
  const requests = useRef<Pending>(pendingRequests(persistenceKey));
  const restored = (scope: string) => { try { return JSON.parse(requests.current[scope]?.payload || "{}"); } catch { return {}; } };
  const [form, setForm] = useState(requests.current.vm ? "vm" : requests.current.iso ? "iso" : requests.current.storage ? "storage" : requests.current.network ? "network" : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [uploadStorage, setUploadStorage] = useState<string>(restored("iso").storage_id || "");
  const [isoSource, setIsoSource] = useState(restored("iso").url ? "url" : "file");
  const [isoURL, setIsoURL] = useState<string>(restored("iso").url || "");
  const [isoName, setIsoName] = useState<string>(restored("iso").url ? restored("iso").name : "");
  const downloadHost = hosts.find(h => h.id === storage.find(s => s.id === uploadStorage)?.host_id);
  const canDownload = Boolean(downloadHost && readyHost(downloadHost) && downloadHost.capabilities?.includes("iso-download-v1"));
  const [networkHost, setNetworkHost] = useState<string>(restored("network").host_id || "");
  const [connection, setConnection] = useState<string>(() => { const r = restored("network"); return r.kind === "vlan" ? `vlan:${r.uplink}` : r.kind ? "custom" : ""; });
  const lock = useRef(false);
  const mounted = useRef(true);
  useEffect(() => () => { mounted.current = false; }, []);
  const input = useRef<HTMLInputElement>(null);
  const hostName = (id: string) => hosts.find(h => h.id === id)?.hostname || id;
  function persist() {
    try { sessionStorage.setItem(persistenceKey, JSON.stringify(requests.current)); }
    catch { setNotice("Browser storage is unavailable. Keep this page open until the task is confirmed so a retry can use the same request."); }
  }
  // An ISO upload can finish after this view unmounts, so re-read the stored map before clearing
  // an entry rather than persisting a snapshot another view has since replaced.
  function clearRequest(scope: string) {
    delete requests.current[scope];
    const stored = pendingRequests(persistenceKey);
    delete stored[scope];
    try { sessionStorage.setItem(persistenceKey, JSON.stringify(stored)); } catch {}
  }
  const nics = hosts.find(h => h.id === networkHost)?.inventory?.nics || [];
  const uplinks = nics.filter(n => !n.kind || n.kind === "physical" || n.kind === "bond");
  const bridges = nics.filter(n => n.kind === "bridge");
  function requestID(scope: string, payload: unknown) {
    const encoded = JSON.stringify(payload);
    if (requests.current[scope] && requests.current[scope].payload !== encoded) throw new Error("An earlier submission has an uncertain outcome. Restore its original entries and retry to reconcile that request, or inspect Task history before starting another request.");
    requests.current[scope] ??= { payload: encoded, id: crypto.randomUUID() };
    persist();
    return requests.current[scope].id;
  }
  // An ISO upload runs unblocking: it must not disable Close or the rest of the pool view while
  // the file streams to the Controller. Its own guard is the shell's single-upload state.
  async function run(scope: string, action: () => Promise<void>, blocking = true) {
    if (blocking) {
      if (lock.current) return;
      lock.current = true;
      setBusy(true);
    }
    setError(""); setNotice("");
    try { await action(); } catch (e) {
      const code = (e as Error & { status?: number }).status;
      // An aborted request never reached the Controller, so its identity can be released.
      if ((e as Error & { cancelled?: boolean }).cancelled) { clearRequest(scope); return; }
      if ([400, 401, 403, 404, 413, 507].includes(code || 0)) clearRequest(scope);
      if (code === 409) {
        try {
          const current = await api<{ request_id: string }[]>("operations");
          if (current.length < 1000 && !current.some(op => op.request_id === requests.current[scope]?.id)) clearRequest(scope);
        } catch { /* Keep the original identity while the outcome is uncertain. */ }
      }
      setError((e as Error).message);
    }
    finally { if (blocking) { lock.current = false; setBusy(false); } }
  }
  function open(name: string) { setForm(name); setError(""); setNotice(""); }
  // The toolbar's New VM lands here after navigating to the pool; show the wizard once.
  useEffect(() => { if (createVM) { setTab("Virtual machines"); open("vm"); onCreateVMShown(); } }, [createVM]);
  function chooseFile(value: File | null) {
    if (busy || uploading) return;
    setFile(null); setError("");
    if (!value) return;
    if (!/\.iso$/i.test(value.name) || value.size < 32774) {
      setError("Choose an ISO image ending in .iso of at least 32 KiB."); return;
    }
    setFile(value);
  }
  function register(e: FormEvent<HTMLFormElement>, kind: "storage" | "network") {
    e.preventDefault();
    const values: Record<string, string> = {};
    new FormData(e.currentTarget).forEach((value, key) => { values[key] = String(value).trim(); });
    if (kind === "network") {
      // One "Connection" choice becomes the API's kind/uplink/vlan or kind/source fields.
      const [type, device] = (values.connection || "").split(":");
      delete values.connection;
      if (!["vlan", "bridge", "custom"].includes(type)) { setError("Choose a connection that the selected host currently reports, or Close to discard the restored entries."); return; }
      if (type === "vlan") { values.kind = "vlan"; values.uplink = device; delete values.source; }
      else if (type === "bridge") { values.kind = "bridge"; values.source = device; delete values.vlan; }
      else delete values.vlan;
    }
    const payload: Record<string, unknown> = { ...values, pool_id: poolID };
    if (typeof values.vlan === "string") payload.vlan = Number(values.vlan);
    void run(kind, async () => {
      const result = await api<Registration>(kind === "storage" ? "storage" : "networks", "POST", { ...payload, request_id: requestID(kind, payload) });
      clearRequest(kind);
      setForm(""); await reload(); setNotice(`Registration ${result.status}. Follow host validation in Task history; registration does not make local storage shared.`);
    });
  }
  async function create(payload: Record<string, unknown>) {
    await run("vm", async () => {
      const result = await api<{ vm_id: string }>("vms", "POST", { ...payload, request_id: requestID("vm", payload) });
      clearRequest("vm");
      await reload(); if (mounted.current) { setForm(""); location.hash = `/vms/${result.vm_id}`; }
    });
  }
  const vmTab = tab === "Virtual machines";
  const relevant = ["Virtual machines", "Storage", "Networking", "ISO library"].includes(tab);
  // An open form stays mounted across tab switches so restored entries and wizard progress survive
  // a trip to another tab (the wizard sends the operator to Storage, Networking and ISO library),
  // but it is only shown on the tab that opened it instead of sitting on top of an unrelated one.
  const showForm = formTab(form) === tab;
  return <div hidden={!relevant}>
    {error && <p className="alert" role="alert">{error} Your entries are retained. Review Task history before retrying an uncertain result.</p>}
    {notice && <p className="notice" role="status">{notice} <a href="#/tasks">Task history</a></p>}
    {unavailable && <p className="warning">Controller inventory may be out of date. Refresh the view before submitting changes.</p>}
    <div className="managed-actions actions">
      {vmTab && <button className="primary" disabled={busy || unavailable} onClick={() => open("vm")}>Create VM</button>}
      {tab === "Networking" && <button disabled={busy || unavailable} onClick={() => open("network")}>Register network</button>}
      {tab === "ISO library" && <button disabled={busy || unavailable} onClick={() => open("iso")}>Upload ISO</button>}
    </div>
    {form === "network" && <section className="panel form-panel" aria-label="Register network" hidden={!showForm}>
      <div className="section-heading"><h2>Register guest network</h2><button disabled={busy} onClick={() => setForm("")}>Close</button></div>
      <form onSubmit={e => register(e, "network")}>
        <label>Name<input name="name" required maxLength={100} autoFocus defaultValue={restored(form).name || ""} /></label>
        <label>Host<select name="host_id" required defaultValue={restored(form).host_id || ""} onChange={e => { setNetworkHost(e.target.value); setConnection(""); }}><option value="">Select host</option>{hosts.map(h => <option key={h.id} value={h.id} disabled={!readyHost(h)}>{h.hostname} · {h.connection}{!h.capabilities?.includes("managed-vm-v1") ? " · agent upgrade required" : ""}</option>)}</select></label>
        <>
          <label>Connection<select name="connection" required value={connection} onChange={e => setConnection(e.target.value)}>
            <option value="">{networkHost ? "Select a NIC or bridge" : "Select a host first"}</option>
            {uplinks.map(n => <option key={n.name} value={`vlan:${n.name}`}>{n.name} · {n.kind || "NIC"} · {n.state}{n.mac ? ` · ${n.mac}` : ""}</option>)}
            {bridges.filter(b => !networks.some(n => n.host_id === networkHost && n.source === b.name)).map(n => <option key={n.name} value={`bridge:${n.name}`}>Existing bridge {n.name} · untagged</option>)}
            <option value="custom">Other: existing bridge or libvirt network by name</option>
          </select></label>
          {connection.startsWith("vlan:") && <label>VLAN ID<input name="vlan" type="number" required min={0} max={4094} step={1} defaultValue={restored("network").vlan ?? ""} placeholder="100" /><small>0 = untagged</small></label>}
          {connection === "custom" && <>
            <label>Network type<select name="kind" defaultValue={restored("network").kind === "network" ? "network" : "bridge"}><option value="bridge">Existing bridge</option><option value="network">Existing libvirt network</option></select></label>
            <label>Existing network or bridge<input name="source" required defaultValue={restored("network").source || ""} maxLength={63} placeholder="br0" /></label>
          </>}
          {networkHost && !nics.length && <p className="help">This host has not reported its interfaces yet. Wait for a fresh inventory or use the "Other" option.</p>}
        </>
        {!hosts.some(readyHost) && <p className="warning">A freshly connected host with managed-VM support is required. Enroll, reconnect or upgrade the host agent first.</p>}
        <button className="primary" disabled={busy || unavailable || !hosts.some(readyHost)}>{busy ? "Submitting…" : "Register and check access"}</button>
      </form>
    </section>}
    {form === "iso" && <section className="panel form-panel" aria-label="Upload ISO" hidden={!showForm}>
      <div className="section-heading"><h2>Upload guest ISO</h2><button onClick={() => setForm("")}>Close</button></div>
      <form onSubmit={e => { e.preventDefault();
        if (isoSource === "url") {
          if (!canDownload || unavailable) return;
          const payload = { storage_id: uploadStorage, name: isoName.trim(), url: isoURL.trim() };
          void run("iso", async () => {
            const result = await api<Media>("media/download", "POST", { ...payload, request_id: requestID("iso", payload) });
            clearRequest("iso");
            await reload(); setNotice(`ISO download ${result.status}. Nex Core downloads to the selected host storage; follow Task history and wait for a succeeded result before use.`);
          });
          return;
        }
        if (!file) { setError("Choose an ISO file first."); return; } const image = file; void run("iso", async () => {
        const payload = { storage_id: uploadStorage, name: image.name, size: image.size, modified: image.lastModified };
        const query = new URLSearchParams({ storage_id: uploadStorage, name: image.name, request_id: requestID("iso", payload) });
        const result = await startIsoUpload<Media>(`media?${query}`, image);
        clearRequest("iso");
        if (mounted.current) { setFile(null); if (input.current) input.current.value = ""; }
        await reload(); if (mounted.current) setNotice(`ISO received by Controller; host transfer ${result.status}. Wait for a succeeded result before creating a VM.`);
      }, false); }}>
        <label>Destination storage<select required value={uploadStorage} onChange={e => setUploadStorage(e.target.value)} disabled={busy || uploading}><option value="">Select ISO-ready storage</option>{storage.filter(s => s.status === "succeeded" && s.access?.status === "healthy" && s.access?.writable && s.capabilities?.iso).map(s => <option key={s.id} value={s.id}>{s.name} · {hostName(s.host_id)}</option>)}</select></label>
        <label>ISO source<select value={isoSource} disabled={busy || uploading} onChange={e => { setIsoSource(e.target.value); setError(""); }}><option value="file">Upload from computer</option><option value="url">Download from public URL</option></select></label>
        {isoSource === "url" ? <>
          <label>Public ISO URL<input type="url" required maxLength={4096} value={isoURL} disabled={busy} onChange={e => setIsoURL(e.target.value)} placeholder="https://downloads.example.org/installer.iso" /></label>
          <label>ISO name<input required maxLength={100} pattern=".*[.][iI][sS][oO]" value={isoName} disabled={busy} onChange={e => setIsoName(e.target.value)} placeholder="installer.iso" /></label>
          <p className="help">Use a direct public HTTP or HTTPS download link. Nex Core downloads and validates the ISO on the selected host; the download continues if you close this page.</p>
          {uploadStorage && !canDownload && <p className="warning">URL downloads need a freshly connected host with download support. Reconnect or upgrade Nex Core.</p>}
        </> : <div className="iso-drop" onDragOver={e => e.preventDefault()} onDrop={e => { e.preventDefault(); if (e.dataTransfer.files.length !== 1) { setError("Drop one ISO image at a time."); return; } chooseFile(e.dataTransfer.files[0]); }}>
          <label>ISO file<input ref={input} type="file" accept=".iso,application/x-iso9660-image" disabled={busy || uploading} onChange={e => chooseFile(e.target.files?.[0] || null)} /></label>
          <p className="help">Drop one .iso image here or use the file picker. The Controller and host validate its content and available space.</p>
          {file && <p className="iso-filename">{file.name} · {(file.size / GiB).toFixed(2)} GiB</p>}
        </div>}
        {uploading && <p className="help" role="status">Upload running · see Recent tasks</p>}
        {!storage.some(s => s.status === "succeeded" && s.access?.status === "healthy" && s.access?.writable && s.capabilities?.iso) && <p className="help">Register storage and wait for Core to report ISO access first.</p>}
        <button className="primary" disabled={busy || uploading || unavailable || !uploadStorage || (isoSource === "url" ? !canDownload || !isoURL.trim() || !isoName.trim() : !file)}>{busy ? "Submitting…" : uploading ? "Uploading…" : isoSource === "url" ? "Download ISO" : "Upload and transfer ISO"}</button>
      </form>
    </section>}
    {form === "vm" && <div hidden={!showForm}><VMWizard poolID={poolID} poolName={poolName} hosts={hosts} storage={storage} networks={networks} media={media} restored={() => restored("vm")} create={create} busy={busy} unavailable={unavailable} setTab={setTab} onClose={() => setForm("")} /></div>}
    {vmTab && <section className="panel"><div className="section-heading"><h2>Virtual machines</h2><span className="muted">{vms.length} in pool</span></div>{vms.length ? <div className="table-scroll"><table><thead><tr><th>Name</th><th>Assigned host</th><th>Observed state</th><th>Freshness</th><th>Creation task</th></tr></thead><tbody>{vms.map(v => <tr key={v.id}><td><a href={`#/vms/${v.id}`}>{v.name}</a>{v.protected && <small>Protected Controller appliance</small>}</td><td>{hostName(v.host_id)}</td><td>{v.state || "unknown"}</td><td>{status(v.freshness)}</td><td>{v.operation_id ? task(v) : "—"}</td></tr>)}</tbody></table></div> : <p className="empty">No virtual machines in this pool. Register storage and a network, upload an ISO, then create your first VM.</p>}<p className="panel-note">A stale or unreachable host does not mean its VMs have stopped. Creation results and current observations are separate.</p></section>}
    {tab === "Storage" && <StorageRepositories poolID={poolID} masterHostID={masterHostID} hosts={hosts} storage={storage} api={api} reload={reload} unavailable={unavailable} onNotice={setNotice} />}
    {tab === "Networking" && <section className="panel"><div className="section-heading"><h2>Pool guest networks</h2></div>{networks.length ? <div className="table-scroll"><table><thead><tr><th>Name</th><th>Host</th><th>Connection</th><th>VMs</th><th>Host check</th></tr></thead><tbody>{[...networks].sort((a, b) => (a.vlan || 0) - (b.vlan || 0) || a.name.localeCompare(b.name)).map(n => <tr key={n.id}><td>{n.name}</td><td>{hostName(n.host_id)}</td><td>{describeNetwork(n)}<small className="mono">{n.source}</small></td><td>{vms.filter(v => v.spec?.nics?.some(i => i.network_id === n.id)).length}</td><td>{task(n)}</td></tr>)}</tbody></table></div> : <p className="empty">No networks reported yet.</p>}</section>}
    {tab === "ISO library" && <section className="panel"><div className="section-heading"><h2>Guest ISO library</h2></div>{media.length ? <div className="table-scroll"><table><thead><tr><th>ISO</th><th>Host / storage</th><th>Size</th><th>Transfer task</th></tr></thead><tbody>{media.map(m => <tr key={m.id}><td>{m.name}<small className="mono">SHA-256: {m.sha256 || "Pending verification"}</small></td><td>{hostName(m.host_id)}<small>{storage.find(s => s.id === m.storage_id)?.name || m.storage_id}</small></td><td>{m.size_bytes > 0 ? `${(m.size_bytes / GiB).toFixed(2)} GiB` : "Unknown until downloaded"}</td><td>{task(m)}</td></tr>)}</tbody></table></div> : <p className="empty">No ISO images uploaded. Select validated storage and upload installation media.</p>}</section>}
  </div>;
}

// An all-zero topology is the documented default of one socket, Count cores and one thread.
const topology = (c: VMSpec["cpu"]) => c.sockets || c.cores || c.threads ? `${c.sockets} × ${c.cores} × ${c.threads} (sockets × cores × threads)` : `1 × ${c.count} × 1 (sockets × cores × threads)`;

type DetailProps = { vm: VM; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[]; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string) => void };

// VMDetails shows the definition and observed state; Edit turns the same panel into the
// editor, so a VM is read and changed in one place.
export function VMDetails({ vm, hosts, storage, networks, media, api, reload, busy, unavailable, onNotice }: DetailProps) {
  const spec = vm.spec;
  const host = hosts.find(h => h.id === vm.host_id);
  const [editing, setEditing] = useState(false);
  const blocker = editBlocker(vm, host);
  return <><section className="panel"><div className="section-heading"><h2>VM details</h2><span className="actions">{status(vm.freshness)}{!editing && <button type="button" disabled={busy || unavailable || Boolean(blocker)} title={blocker || undefined} onClick={() => setEditing(true)}>Edit</button>}</span></div>
    {vm.freshness !== "fresh" && <p className="warning">Current execution state is unknown. Last reported state: {vm.state || "unknown"}. An unreachable host does not prove that this VM stopped.</p>}
    {editing && spec ? <VMEditor vm={vm} host={host} poolName={host?.hostname || ""} storage={storage} networks={networks} media={media} api={api} reload={reload} unavailable={unavailable} onNotice={onNotice} onClose={() => setEditing(false)} /> : <dl className="details"><dt>VM identity</dt><dd>{vm.id}</dd><dt>Assigned host</dt><dd><a href={`#/hosts/${vm.host_id}`}>{hosts.find(h => h.id === vm.host_id)?.hostname || vm.host_id}</a></dd><dt>Observed state</dt><dd>{vm.state || "unknown"}</dd><dt>Role</dt><dd>{vm.protected ? "Protected Controller appliance" : "Guest VM"}</dd>{spec && <>{spec.description && <><dt>Description</dt><dd>{spec.description}</dd></>}<dt>Guest OS</dt><dd>{guestNames[spec.guest_os] || "Not recorded"}</dd><dt>Virtual CPUs</dt><dd>{spec.cpu.count}<small>{topology(spec.cpu)} · {cpuNames[spec.cpu.model] || spec.cpu.model}</small></dd><dt>Memory</dt><dd>{spec.memory.size_mib} MiB<small>{spec.memory.mode === "dedicated" ? "Dedicated configured · locks host RAM while running on updated Nex Core. Existing VMs: upgrade Core, stop, then Edit → Save changes. Capacity stays committed when stopped." : "Maximum · a ceiling only; free pages return to the host when the guest reports them"}</small></dd><dt>Disks</dt><dd>{spec.disks?.length ? <ul className="plain">{spec.disks.map((d, i) => <li key={i}>{d.name} · {d.size_gib} GiB · {busNames[d.bus] || d.bus} · {d.thin ? "Thin" : "Thick"}{d.boot_order > 0 ? ` · boot ${d.boot_order}` : ""}<small>{storage.find(s => s.id === d.storage_id)?.name || d.storage_id}</small></li>)}</ul> : "None recorded"}</dd><dt>Networks</dt><dd>{spec.nics?.length ? <ul className="plain">{spec.nics.map((n, i) => <li key={i}>{networks.find(x => x.id === n.network_id)?.name || n.network_id} · {adapterNames[n.model] || n.model}{n.mtu ? ` · MTU ${n.mtu}` : ""}<small className="mono">{n.mac || "MAC derived from the VM identity"}</small></li>)}</ul> : "None"}</dd><dt>Install ISO</dt><dd>{spec.media_id ? media.find(m => m.id === spec.media_id)?.name || spec.media_id : "None · CD drive empty"}{spec.boot.media_boot_order > 0 && <small>CD drive boot order {spec.boot.media_boot_order}</small>}</dd><dt>Firmware</dt><dd>{spec.boot.firmware?.toUpperCase()}<small>Machine type {spec.boot.machine || "q35"}</small></dd><dt>Autostart</dt><dd>{spec.autostart ? "Starts when the host boots" : "Does not start when the host boots"}</dd></>}{vm.operation_id && <><dt>Creation task</dt><dd>{task(vm)}</dd></>}</dl>}
    {!vm.protected && !editing && <p className="panel-note">Creation provisions a stopped VM. Use Start in the toolbar to start it, then open the Console tab. Select Edit to change the VM; changes a running guest can take apply while it runs.</p>}
  </section>
  <GuestTools vm={vm} />
  </>;
}

// GuestTools shows what the host observed inside a running guest: agent facts and the
// console channels behind clipboard and display resize. Absence is shown as unknown.
const guestAgentText: Record<string, string> = {
  connected: "Connected · guest agent answering",
  unresponsive: "Attached but not answering · the guest agent may be starting or hung",
  disconnected: "Not running in the guest",
  "no-channel": "Channel missing · this VM was created before guest tools support; recreate it to add the channel",
};
const guestClipboardText: Record<string, string> = {
  ready: "Ready · copy and paste work both ways in the Console tab",
  waiting: "Waiting for spice-vdagent in the guest · Type into VM still works",
  "no-channel": "Channel missing · this VM was created before console clipboard support; recreate it to add the channel",
};
export function GuestTools({ vm }: { vm: VM }) {
  const g = vm.guest;
  const running = vm.freshness === "fresh" && vm.state === "running";
  return <section className="panel" aria-label="Guest tools">
    <div className="section-heading"><h2>Guest tools</h2>{g ? status(g.agent === "connected" ? "fresh" : g.agent === "unresponsive" || g.agent === "disconnected" ? "stale" : "unknown") : status("unknown")}</div>
    {!g ? <p className="empty">{running ? "No guest observation yet. The host reports guest tools for running VMs on Nex Core builds with guest observation; wait for the next report or upgrade Nex Core." : "Guest tools are observed while the VM is running."}</p>
      : <dl className="details">
        <dt>Guest agent</dt><dd>{guestAgentText[g.agent] || g.agent}</dd>
        {g.os && <><dt>Operating system</dt><dd>{g.os}</dd></>}
        {g.hostname && <><dt>Guest hostname</dt><dd>{g.hostname}</dd></>}
        <dt>IP addresses</dt><dd>{g.addresses?.length ? <ul className="plain">{g.addresses.map(a => <li key={a.interface + a.address} className="mono">{a.address} <small>{a.interface}</small></li>)}</ul> : g.agent === "connected" ? "None reported" : "Unknown until the guest agent answers"}</dd>
        <dt>Console clipboard</dt><dd>{guestClipboardText[g.clipboard] || g.clipboard}</dd>
        <dt>Display resize</dt><dd>{g.resize ? "Available · VirtIO display; the console can resize the guest to the browser window" : "Not available · no VirtIO display in this VM's definition"}</dd>
      </dl>}
    {g && running && (g.agent === "disconnected" || g.agent === "unresponsive") && <GuestToolsAdvisory />}
    {g && g.agent === "no-channel" && <p className="warning">This VM was created before guest tools support, so it has no guest agent channel. Recreate it to add the channel, then install the tools below. In the meantime the console's <strong>Type into VM</strong> still works.</p>}
    <p className="panel-note">A connected agent reports facts and enables graceful operations; it is not proof that the guest's applications are healthy.</p>
  </section>;
}

// GuestToolsAdvisory tells an operator how to install the guest tools in a running VM whose
// agent has not connected, for both Linux and Windows guests.
export function GuestToolsAdvisory() {
  return <div className="warning guest-advisory">
    <p><strong>Guest tools are not responding.</strong> Install them inside the VM to see its OS, hostname and IP addresses here and to use the console clipboard. They are standard packages; use the <strong>Console</strong> tab to run these if the VM has no network yet.</p>
    <div className="guest-advisory-grid">
      <div>
        <h3>Linux</h3>
        <p className="help">Debian and Ubuntu:</p>
        <pre>sudo apt update{"\n"}sudo apt install qemu-guest-agent spice-vdagent</pre>
        <p className="help">Other distributions: install their <code>qemu-guest-agent</code> and <code>spice-vdagent</code> packages, then <code>systemctl enable --now qemu-guest-agent spice-vdagentd</code>. Both start on their own; no reboot is needed. The clipboard needs a graphical desktop session.</p>
      </div>
      <div>
        <h3>Windows</h3>
        <p className="help">Run the virtio-win guest tools installer, which bundles the QEMU guest agent and the SPICE vdagent:</p>
        <pre>virtio-win-guest-tools.exe</pre>
        <p className="help">Download it from the <a href="https://github.com/virtio-win/virtio-win-pkg-scripts" target="_blank" rel="noopener">virtio-win project</a>. It installs both as Windows services that start automatically.</p>
      </div>
    </div>
  </div>;
}

export function HostISOs({ host, media, storage, unavailable }: { host: Host; media: Media[]; storage: Storage[]; unavailable: boolean }) {
  const images = media.filter(image => image.host_id === host.id && image.pool_id === host.pool_id);
  return <section className="panel" aria-label="Host ISO images">
    <div className="section-heading"><h2>Host ISO images</h2><span className="muted">{images.length} transfer records</span></div>
    {images.length ? <div className="table-scroll"><table>
      <thead><tr><th>ISO</th><th>Storage</th><th>Size</th><th>Transfer / access</th><th>Transfer task</th></tr></thead>
      <tbody>{images.map(image => {
        const destination = storage.find(item => item.id === image.storage_id && item.host_id === host.id && item.pool_id === host.pool_id);
        let availability = ({ queued: "Pending transfer", dispatched: "Dispatched · awaiting result", failed: "Transfer failed", needs_attention: "Needs attention" } as Record<string, string>)[image.status] || "Not available";
        if (image.status === "succeeded") {
          availability = unavailable ? "Availability unknown · Controller data may be stale"
            : host.revoked ? "Unavailable · host access revoked"
            : host.connection !== "fresh" ? "Availability unknown · host is not freshly connected"
            : !host.capabilities?.includes("managed-vm-v1") ? "Unavailable · host agent upgrade required"
            : destination?.status !== "succeeded" ? "Availability unknown · storage access unverified"
            : "Transfer succeeded · not rescanned";
        }
        return <tr key={image.id}><td>{image.name}</td><td>{destination?.name || image.storage_id}{destination && <small>{destination.path}</small>}</td><td>{image.size_bytes > 0 ? <>{(image.size_bytes / GiB).toLocaleString(undefined, { maximumFractionDigits: 2 })} GiB<small>{image.size_bytes.toLocaleString()} bytes</small></> : "Unknown until downloaded"}</td><td>{availability}</td><td>{task(image)}</td></tr>;
      })}</tbody>
    </table></div> : <p className="empty">{unavailable ? "ISO records could not be loaded. Retry when the Controller is reachable." : "No ISO transfers recorded for this host. Upload an ISO to this host's storage from the pool's ISO library."}</p>}
    <p className="panel-note">Availability is based on completed transfer records, validated storage and host connectivity. Files are not continuously rescanned; VM creation checks the selected ISO again.</p>
  </section>;
}

const powerActions = [
  { action: "start", label: "Start", icon: "start", when: (state: string) => state === "shut off", confirm: "" },
  { action: "shutdown", label: "Shut down", icon: "shutdown", when: (state: string) => state === "running", confirm: "Asks the guest to shut down (ACPI). A guest without ACPI support ignores it; use Force stop if it stays running." },
  { action: "reboot", label: "Reboot", icon: "reboot", when: (state: string) => state === "running", confirm: "Asks the guest to reboot (ACPI). A guest without ACPI support ignores it." },
  { action: "force-stop", label: "Force stop", icon: "stop", when: (state: string) => state === "running", confirm: "Cuts power immediately, like pulling the plug. Unsaved guest data is lost." },
] as const;

// powerBlocker explains why central power requests are unavailable for a VM now; an empty
// result means they can be submitted.
export function powerBlocker(vm: VM, host?: Host): string {
  return !host || host.revoked || host.connection !== "fresh" ? "Power controls need a freshly connected host."
    : !host.capabilities?.includes("vm-power-v1") ? "The host agent does not support central power requests; upgrade Nex Core."
    : vm.freshness !== "fresh" ? "Current VM state is unknown; wait for a fresh host report."
    : vm.state !== "running" && vm.state !== "shut off" ? `The VM is ${vm.state || "unknown"}; resolve that state on its host before a power request.`
    : "";
}

// VMPower is the toolbar's power group: Start, Shut down, Reboot and Force stop for the selected
// VM, greyed out with a reason when nothing is selected or the host cannot take the request.
// Requests are durable host tasks; the observed state, refreshed by host reports, tells what
// actually happened. Confirmations use a native modal dialog so they work from the toolbar.
export function VMPower({ vm, host, api, reload, busy, unavailable, onNotice, describedBy }: { vm?: VM; host?: Host; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string) => void; describedBy?: string }) {
  const [pending, setPending] = useState<typeof powerActions[number] | null>(null);
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const request = useRef<Record<string, string>>({});
  const dialog = useRef<HTMLDialogElement>(null);
  // The opener is disabled while its request runs, so the dialog cannot restore focus by itself.
  const opener = useRef<HTMLButtonElement | null>(null);
  const blocker = vm ? powerBlocker(vm, host) : "Select a virtual machine in Resources.";
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (pending && !element.open) element.showModal();
    else if (!pending && element.open) element.close();
  }, [pending]);
  async function submit(item: typeof powerActions[number]) {
    if (!vm) return;
    setWorking(true); setError("");
    request.current[item.action] ??= crypto.randomUUID();
    try {
      await api(`vms/${encodeURIComponent(vm.id)}/power`, "POST", { request_id: request.current[item.action], action: item.action });
      delete request.current[item.action];
      onNotice(`${item.label} queued for ${vm.name}. Follow its progress in Recent tasks.`);
      await reload();
      // Close only once the button can take focus back, so the dialog returns focus to it.
      setPending(null);
    } catch (e) {
      // The dialog is the error surface for every action: it stays (or opens, for Start) with the
      // message, and its confirm button retries with the same request identity.
      setError((e as Error).message);
      setPending(item);
    }
    finally { setWorking(false); }
  }
  return <div className="toolbar-group vm-power" role="group" aria-label="VM power">
    {powerActions.map(item => <button key={item.action} type="button" className={`tool-${item.action}`} title={blocker || undefined} aria-describedby={describedBy}
      disabled={busy || working || unavailable || !vm || Boolean(blocker) || !item.when(vm.state)}
      onClick={e => { opener.current = e.currentTarget; setError(""); if (item.confirm) setPending(item); else void submit(item); }}><Icon kind={item.icon} />{item.label}</button>)}
    <dialog ref={dialog} className="confirm-dialog" aria-labelledby="power-confirm-title" onCancel={e => { if (working) e.preventDefault(); }} onClose={() => { setPending(null); setError(""); opener.current?.focus(); }}>
      {pending && vm && <form aria-label={`Confirm ${pending.label.toLowerCase()}`} onSubmit={e => { e.preventDefault(); void submit(pending); }}>
        <h2 id="power-confirm-title">{pending.label} {vm.name}?</h2>
        {(pending.confirm || vm.protected) && <p>{pending.confirm}{vm.protected && " This is the Controller appliance: central management stops until it runs again; recover it from the host-local page."}</p>}
        {error && <p className="task-error" role="alert">{error}</p>}
        <div className="actions"><button type="button" disabled={working} onClick={() => setPending(null)}>Cancel</button><button className={pending.action === "force-stop" ? "danger" : "primary"} disabled={working}>{working ? "Requesting…" : `Confirm ${pending.label.toLowerCase()}`}</button></div>
      </form>}
    </dialog>
  </div>;
}
