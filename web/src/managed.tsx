// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { Guest } from "./console";
import { Icon } from "./icons";
import { VMWizard, adapterNames, busNames, cpuNames, describeNetwork, guestNames, readyHost, secureBootText, tpmText, type DiskRow, type NICRow } from "./vm-create";
import { VMEditor, editBlocker } from "./vm-edit";
import { StorageRepositories, storageOwnerFresh } from "./storage";

export type Registration = { id: string; pool_id: string; host_id: string; name: string; operation_id: string; status: string; error?: string };
export type StorageSource = { nfs?: { server: string; export: string; directory?: string }; zfs?: { pool: string; dataset: string; mount_path: string }; smb?: { server: string; share: string; directory?: string }; iscsi?: { portal: string; target_iqn: string; lun: number; wwid: string; filesystem_uuid: string; exclusive_target: boolean; mode?: "exclusive" | "clustered_gfs2"; cluster_id?: string; lock_table?: string; eligible_host_ids?: string[] } };
// `member_access` is a Core observation from the pool master. It is deliberately
// separate from repository configuration: Controller uses it only to render and
// preflight placement; Core rechecks access and writer safety for the operation.
export type Storage = Registration & { path: string; kind?: string; source?: StorageSource; identity?: Record<string, unknown>; generation?: number; tombstone?: boolean; credential_present?: boolean; capabilities?: { vm_disk?: boolean; iso?: boolean; thin?: boolean; thick?: boolean; shared?: boolean; eligible_host_ids?: string[] }; access?: { status?: string; readable?: boolean; writable?: boolean; available_bytes?: number; total_bytes?: number; observed_at?: string; error?: string }; member_access?: { repository_id: string; host_id: string; generation: number; identity: string; readable: boolean; writable: boolean; qemu_writable: boolean; locking: boolean; observed_at?: string; error?: string }[] };
export type Network = Registration & { kind: string; source: string; uplink?: string; vlan?: number };
export type Media = Registration & { storage_id: string; size_bytes: number; sha256: string };
// Arrays are nullable because Go marshals an empty slice as null; render accordingly.
// boot.secure_boot and boot.tpm are absent from a definition written before those fields
// existed: an unset secure_boot on a UEFI VM is Secure Boot on, and an unset tpm is no TPM.
// drivers_media_id is the ISO in the VM's second CD drive, empty when that drive is empty.
export type CPUFeature = { name: string; policy: "require" | "disable" };
export type VMCPU = { count: number; sockets: number; cores: number; threads: number; model: string; features?: CPUFeature[] | null };
export type VMSpec = { revision?: number; description: string; guest_os: string; cpu: VMCPU; memory: { mode: string; size_mib: number }; disks: DiskRow[] | null; nics: NICRow[] | null; boot: { firmware: string; machine: string; media_boot_order: number; secure_boot?: string; tpm?: string }; media_id: string; drivers_media_id?: string; start_after_create: boolean; autostart: boolean };
export type VMUsage = { observed_at?: string; sample_window_ms?: number; cpu_percent?: number | null; memory_assigned_bytes?: number; memory_maximum_bytes?: number; memory_host_bytes?: number | null; disk_used_bytes?: number | null; disk_capacity_bytes?: number | null };
export type VM = Registration & { spec?: VMSpec; state: string; freshness: string; role: string; protected: boolean; guest?: Guest | null; usage?: VMUsage | null };
type NIC = { name: string; mac: string; kind?: string; master?: string; parent?: string; state: string; addresses?: string[] | null };
// inventory is the host's last stored report, returned even while the host is stale; its vms
// carry the last state the host reported for each VM.
export type Host = { id: string; pool_id: string; hostname: string; connection: string; revoked: boolean; capabilities?: string[]; inventory: { observed_at?: string; cpu_count: number; cpu_models?: string[] | null; cpu_baseline?: { model: string; features: CPUFeature[] | null; machine: string; generation: number; member_count: number; observed_at: string; error?: string } | null; memory_bytes: number; nics?: NIC[] | null; vms?: { id: string; state: string }[] | null } | null };
type API = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Props = { account: string; poolID: string; poolName: string; masterHostID: string; tab: string; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[]; vms: VM[]; api: API; reload: () => Promise<void>; setTab: (tab: string) => void; unavailable: boolean; uploading: boolean; startIsoUpload: <T>(path: string, file: File) => Promise<T>; createVM: boolean; onCreateVMShown: () => void };
const GiB = 1024 ** 3;
const status = (value: string) => <span className={`badge ${value}`}>{value || "unknown"}</span>;
// sentence ends a message with a full stop when it has no terminal punctuation of its own.
export const sentence = (text: string) => /[.!?]$/.test(text) ? text : `${text}.`;
const task = (value: Registration) => <>{status(value.status)}{value.error && <p className="task-error">{value.error}</p>}{value.operation_id && <small><a href="#/tasks">Task {value.operation_id}</a></small>}</>;

// Each registration form belongs to the pool tab that opens it; "" means no form is open.
export const formTab = (form: string) => ({ vm: "Virtual machines", network: "Networking", iso: "ISO library" })[form] || "";

// Last path segment of a direct download URL, for prefilling the ISO name field.
export function isoNameFromURL(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  let base = "";
  try {
    base = decodeURIComponent(new URL(trimmed).pathname.split("/").filter(Boolean).pop() || "");
  } catch {
    // Incomplete while typing: still pick up a trailing .iso segment when present.
    const match = trimmed.match(/\/([^/?#]+\.iso)(?:[?#]|$)/i);
    if (match) {
      try { base = decodeURIComponent(match[1]); } catch { base = match[1]; }
    }
  }
  if (!base || base === "." || base === ".." || !/\.iso$/i.test(base)) return "";
  return base.slice(0, 100);
}

// isoTransferNotice describes what is happening to an ISO the operator just uploaded or asked
// the host to download, from the record the host reports now. The status the submission
// returned is only its first word: a banner that keeps saying "queued. Wait for a succeeded
// result" while the same page shows the iso-import task succeeded is wrong (BUG-0204).
// `status` is undefined until the ISO library lists the record.
export function isoTransferNotice(name: string, status?: string, source: "upload" | "download" = "upload"): string {
  const work = source === "download" ? "host download" : "host transfer";
  switch (status) {
    case "succeeded":
      return `${name} is on the host and can be used as install media or a driver disc.`;
    case "failed":
      return `The ${work} of ${name} failed, so it cannot be used by a VM. Read the reason in Task history.`;
    case "needs_attention":
      return `The ${work} of ${name} needs attention: the host did not confirm it, so whether the file is usable is unknown. Check it in Task history before using it or sending it again.`;
    case undefined:
      return `${name} has no ${work} record yet. It is not usable by a VM until one reports succeeded.`;
    default:
      return `The ${work} of ${name} is ${status}. Wait for a succeeded result before using it in a VM.`;
  }
}

// Only imports that never became host-held media can be discarded. Core owns a succeeded ISO,
// while queued and dispatched imports still belong to their host task (BUG-0203).
export const mediaDiscardable = (media: Media) => media.status === "failed" || media.status === "needs_attention";

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
  // The ISO whose host transfer this view is following, so its banner can report the record the
  // host reports now instead of the status the submission happened to return.
  const [transfer, setTransfer] = useState<{ id: string; name: string; source: "upload" | "download" } | null>(null);
  const [uploadStorage, setUploadStorage] = useState<string>(restored("iso").storage_id || "");
  const [isoSource, setIsoSource] = useState(restored("iso").url ? "url" : "file");
  const [isoURL, setIsoURL] = useState<string>(restored("iso").url || "");
  const [isoName, setIsoName] = useState<string>(restored("iso").url ? restored("iso").name : "");
  const downloadHost = hosts.find(h => h.id === storage.find(s => s.id === uploadStorage)?.host_id);
  const canDownload = Boolean(downloadHost && readyHost(downloadHost) && downloadHost.capabilities?.includes("iso-download-v1"));
  const isoReadyStorage = storage.filter(s => s.status === "succeeded" && s.access?.status === "healthy" && s.access?.writable && s.capabilities?.iso && storageOwnerFresh(s, hosts));
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
      const result = await api<{ vm_id: string; operation: { id: string } }>("vms", "POST", { ...payload, request_id: requestID("vm", payload) });
      clearRequest("vm");
      // The creation operation rides along in the route, so a create that fails after this 202
      // still names its task once the VM listing no longer includes it.
      await reload(); if (mounted.current) { setForm(""); location.hash = `/vms/${result.vm_id}/${result.operation.id}`; }
    });
  }
  function discardMedia(media: Media) {
    if (!confirm(`Discard ${media.name}? This removes the failed import record and any Controller staging bytes. The task and audit history are kept.`)) return;
    void run("media-discard", async () => {
      try {
        await api(`media/${media.id}`, "DELETE");
      } catch (error) {
        let current: Media[];
        try { current = await api<Media[]>("media"); } catch { throw error; }
        if (current.some(entry => entry.id === media.id)) throw error;
      }
      await reload();
      setNotice(`Discarded the failed import for ${media.name}. Submit the download again to retry.`);
    });
  }
  const vmTab = tab === "Virtual machines";
  const relevant = ["Virtual machines", "Storage", "Networking", "ISO library"].includes(tab);
  // An open form stays mounted across tab switches so restored entries and wizard progress survive
  // a trip to another tab (the wizard sends the operator to Storage, Networking and ISO library),
  // but it is only shown on the tab that opened it instead of sitting on top of an unrelated one.
  const showForm = formTab(form) === tab;
  // The banner for an ISO the operator just sent follows that record's own status.
  const transferStatus = transfer && media.find(m => m.id === transfer.id)?.status;
  return <div hidden={!relevant}>
    {error && <p className="alert" role="alert">{sentence(error)} Your entries are retained. Review Task history before retrying an uncertain result.</p>}
    {notice && <p className="notice" role="status">{notice} <a href="#/tasks">Task history</a></p>}
    {transfer && <p className={transferStatus === "failed" ? "alert" : transferStatus === "needs_attention" ? "warning" : "notice"} role="status">
      {isoTransferNotice(transfer.name, transferStatus || undefined, transfer.source)} <a href="#/tasks">Task history</a>
      <button type="button" onClick={() => setTransfer(null)}>Dismiss</button>
    </p>}
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
            setTransfer({ id: result.id, name: payload.name, source: "download" });
            await reload();
          });
          return;
        }
        if (!file) { setError("Choose an ISO file first."); return; } const image = file; void run("iso", async () => {
        const payload = { storage_id: uploadStorage, name: image.name, size: image.size, modified: image.lastModified };
        const query = new URLSearchParams({ storage_id: uploadStorage, name: image.name, request_id: requestID("iso", payload) });
        const result = await startIsoUpload<Media>(`media?${query}`, image);
        clearRequest("iso");
        if (mounted.current) { setFile(null); if (input.current) input.current.value = ""; setTransfer({ id: result.id, name: image.name, source: "upload" }); }
        await reload();
      }, false); }}>
        <label>Destination storage<select required value={uploadStorage} onChange={e => setUploadStorage(e.target.value)} disabled={busy || uploading}><option value="">Select ISO-ready storage</option>{isoReadyStorage.map(s => <option key={s.id} value={s.id}>{s.name} · {hostName(s.host_id)}</option>)}</select></label>
        <label>ISO source<select value={isoSource} disabled={busy || uploading} onChange={e => { setIsoSource(e.target.value); setError(""); }}><option value="file">Upload from computer</option><option value="url">Download from public URL</option></select></label>
        {isoSource === "url" ? <>
          <label>Public ISO URL<input type="url" required maxLength={4096} value={isoURL} disabled={busy} onChange={e => {
            const url = e.target.value;
            const suggested = isoNameFromURL(url);
            setIsoURL(url);
            setIsoName(name => (!name.trim() || name === isoNameFromURL(isoURL)) ? (suggested || name) : name);
          }} placeholder="https://downloads.example.org/installer.iso" /></label>
          <label>ISO name<input required maxLength={100} pattern=".*[.][iI][sS][oO]" value={isoName} disabled={busy} onChange={e => setIsoName(e.target.value)} placeholder="installer.iso" /></label>
          <p className="help">Use a direct public HTTP or HTTPS download link. The name is filled from the URL path; edit it if you want. Nex Core downloads and validates the ISO on the selected host; the download continues if you close this page.</p>
          {uploadStorage && !canDownload && <p className="warning">URL downloads need a freshly connected host with download support. Reconnect or upgrade Nex Core.</p>}
        </> : <div className="iso-drop" onDragOver={e => e.preventDefault()} onDrop={e => { e.preventDefault(); if (e.dataTransfer.files.length !== 1) { setError("Drop one ISO image at a time."); return; } chooseFile(e.dataTransfer.files[0]); }}>
          <label>ISO file<input ref={input} type="file" accept=".iso,application/x-iso9660-image" disabled={busy || uploading} onChange={e => chooseFile(e.target.files?.[0] || null)} /></label>
          <p className="help">Drop one .iso image here or use the file picker. The Controller and host validate its content and available space.</p>
          {file && <p className="iso-filename">{file.name} · {(file.size / GiB).toFixed(2)} GiB</p>}
        </div>}
        {uploading && <p className="help" role="status">Upload running · see Recent tasks</p>}
        {!isoReadyStorage.length && <p className="help">Register storage and wait for its assigned Core to report current ISO access first.</p>}
        <button className="primary" disabled={busy || uploading || unavailable || !isoReadyStorage.some(s => s.id === uploadStorage) || (isoSource === "url" ? !canDownload || !isoURL.trim() || !isoName.trim() : !file)}>{busy ? "Submitting…" : uploading ? "Uploading…" : isoSource === "url" ? "Download ISO" : "Upload and transfer ISO"}</button>
      </form>
    </section>}
    {form === "vm" && <div hidden={!showForm}><VMWizard poolID={poolID} poolName={poolName} hosts={hosts} storage={storage} networks={networks} media={media} restored={() => restored("vm")} create={create} busy={busy} unavailable={unavailable} setTab={setTab} onClose={() => setForm("")} /></div>}
    {vmTab && <section className="panel"><div className="section-heading"><h2>Virtual machines</h2><span className="muted">{vms.length} in pool</span></div>{vms.length ? <div className="table-scroll"><table><thead><tr><th>Name</th><th>Assigned host</th><th>Observed state</th><th>Freshness</th><th>Creation task</th></tr></thead><tbody>{vms.map(v => <tr key={v.id}><td><a href={`#/vms/${v.id}`}>{v.name}</a>{v.protected && <small>Protected Controller appliance</small>}</td><td>{hostName(v.host_id)}</td><td>{v.state || "unknown"}</td><td>{status(v.freshness)}</td><td>{v.operation_id ? task(v) : "—"}</td></tr>)}</tbody></table></div> : <p className="empty">No virtual machines in this pool. Register storage and a network, upload an ISO, then create your first VM.</p>}<p className="panel-note">A stale or unreachable host does not mean its VMs have stopped. Creation results and current observations are separate.</p></section>}
    {tab === "Storage" && <StorageRepositories poolID={poolID} masterHostID={masterHostID} hosts={hosts} storage={storage} api={api} reload={reload} unavailable={unavailable} onNotice={setNotice} />}
    {tab === "Networking" && <section className="panel"><div className="section-heading"><h2>Pool guest networks</h2></div>{networks.length ? <div className="table-scroll"><table><thead><tr><th>Name</th><th>Host</th><th>Connection</th><th>VMs</th><th>Host check</th></tr></thead><tbody>{[...networks].sort((a, b) => (a.vlan || 0) - (b.vlan || 0) || a.name.localeCompare(b.name)).map(n => <tr key={n.id || `op:${n.operation_id}`}><td>{n.name}</td><td>{hostName(n.host_id)}</td><td>{describeNetwork(n)}<small className="mono">{n.source}</small></td><td>{vms.filter(v => v.spec?.nics?.some(i => i.network_id === n.id)).length}</td><td>{task(n)}</td></tr>)}</tbody></table></div> : <p className="empty">No networks reported yet.</p>}</section>}
    {tab === "ISO library" && <section className="panel"><div className="section-heading"><h2>Guest ISO library</h2></div>{media.length ? <div className="table-scroll"><table><thead><tr><th>ISO</th><th>Host / storage</th><th>Size</th><th>Transfer task</th><th>Actions</th></tr></thead><tbody>{media.map(m => <tr key={m.id || `op:${m.operation_id}`}><td>{m.name}<small className="mono">SHA-256: {m.sha256 || "Pending verification"}</small></td><td>{hostName(m.host_id)}<small>{storage.find(s => s.id === m.storage_id)?.name || m.storage_id}</small></td><td>{m.size_bytes > 0 ? `${(m.size_bytes / GiB).toFixed(2)} GiB` : "Unknown until downloaded"}</td><td>{task(m)}</td><td>{mediaDiscardable(m) && <button type="button" disabled={busy || unavailable} onClick={() => discardMedia(m)}>Discard failed import</button>}</td></tr>)}</tbody></table></div> : <p className="empty">No ISO images uploaded. Select validated storage and upload installation media.</p>}</section>}
  </div>;
}

// An all-zero topology is the documented default of one socket, Count cores and one thread.
const topology = (c: VMSpec["cpu"]) => c.sockets || c.cores || c.threads ? `${c.sockets} × ${c.cores} × ${c.threads} (sockets × cores × threads)` : `1 × ${c.count} × 1 (sockets × cores × threads)`;

// ejectInstallOffer reports whether to offer ejecting this VM's install ISO. After an install
// finishes, the disc stays in the drive ahead of every bootable disk, so each start shows
// "Press any key to boot from CD or DVD" and a key press in the console restarts the installer
// (BUG-0202). It is offered, never applied for the operator: an installer may still be running,
// and Nex does not guess that an install finished and rewrite the definition by itself.
// It needs a running VM with a fresh report, because that is the only state Nex can observe
// the VM in; a host that cannot take a media request is not offered the action at all.
export function ejectInstallOffer(vm: VM, host?: Host): boolean {
  const spec = vm.spec;
  if (!spec?.media_id || vm.protected) return false;
  const media = spec.boot.media_boot_order;
  const disks = (spec.disks || []).map(d => d.boot_order).filter(order => order > 0);
  // Nothing to fall back to, or the firmware already tries a disk first: no prompt to remove.
  if (media <= 0 || !disks.length || disks.some(order => order < media)) return false;
  if (vm.freshness !== "fresh" || vm.state !== "running") return false;
  // The guest opened its own tray, so the drive the firmware would boot from is already empty.
  // An older Core reports no observation at all; the recorded media then stands on its own.
  if (vm.guest?.media === "empty" || vm.guest?.media === "no-drive") return false;
  return Boolean(host && !host.revoked && host.connection === "fresh" && host.capabilities?.includes("vm-media-v1"));
}

type DetailProps = { vm: VM; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[]; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string) => void; installMedia?: { busy: boolean; note: string; onEject: () => void } };

// VMDetails shows the definition and observed state; Edit turns the same panel into the
// editor, so a VM is read and changed in one place.
export function VMDetails({ vm, hosts, storage, networks, media, api, reload, busy, unavailable, onNotice, installMedia }: DetailProps) {
  const spec = vm.spec;
  const host = hosts.find(h => h.id === vm.host_id);
  const [editing, setEditing] = useState(false);
  const blocker = editBlocker(vm, host);
  const offerEject = !editing && Boolean(installMedia) && ejectInstallOffer(vm, host);
  // The VM listing reports "unknown" while the host is not fresh; the host's stored inventory
  // still holds the last state it reported.
  const lastReport = host?.inventory?.vms?.find(v => v.id === vm.id);
  const reportedAt = host?.inventory?.observed_at ? new Date(host.inventory.observed_at) : null;
  return <><section className="panel"><div className="section-heading"><h2>VM details</h2><span className="actions">{status(vm.freshness)}{!editing && <button type="button" disabled={busy || unavailable || Boolean(blocker)} title={blocker || undefined} onClick={() => setEditing(true)}>Edit</button>}{!editing && <VMDelete vm={vm} host={host} api={api} reload={reload} busy={busy} unavailable={unavailable} onNotice={onNotice} />}</span></div>
    {vm.freshness !== "fresh" && <p className="warning">Current execution state is unknown. Last reported state: {lastReport?.state || "unknown"}{lastReport && reportedAt && !isNaN(reportedAt.getTime()) ? ` at ${reportedAt.toLocaleString()}` : ""}. An unreachable host does not prove that this VM stopped.</p>}
    {offerEject && installMedia && <div className="warning install-media-offer" role="status">
      <p><strong>The install ISO is still in the CD drive, ahead of the disks in the boot order.</strong> Every start boots the ISO first: the installer starts again, or waits at a prompt such as “Press any key to boot from CD or DVD” where a key press starts it again. Eject it once the guest is installed; the VM then boots from its disk. The ISO stays in the library and can be inserted again from the Console tab.</p>
      <p className="help">Do not eject while an installer is still running in this VM.</p>
      {installMedia.note && <p className="task-error" role="alert">{installMedia.note}</p>}
      <div className="actions"><button type="button" disabled={busy || unavailable || installMedia.busy} onClick={installMedia.onEject}>{installMedia.busy ? "Ejecting…" : "Eject install media"}</button></div>
    </div>}
    {editing && spec ? <VMEditor vm={vm} host={host} poolName={host?.hostname || ""} storage={storage} networks={networks} media={media} api={api} reload={reload} unavailable={unavailable} onNotice={onNotice} onClose={() => setEditing(false)} /> : <dl className="details"><dt>VM identity</dt><dd>{vm.id}</dd><dt>Assigned host</dt><dd><a href={`#/hosts/${vm.host_id}`}>{hosts.find(h => h.id === vm.host_id)?.hostname || vm.host_id}</a></dd><dt>Observed state</dt><dd>{vm.state || "unknown"}</dd><dt>Role</dt><dd>{vm.protected ? "Protected Controller appliance" : "Guest VM"}</dd>{spec && <>{spec.description && <><dt>Description</dt><dd>{spec.description}</dd></>}<dt>Guest OS</dt><dd>{guestNames[spec.guest_os] || "Not recorded"}</dd><dt>Virtual CPUs</dt><dd>{spec.cpu.count}<small>{topology(spec.cpu)} · {cpuNames[spec.cpu.model] || spec.cpu.model}</small></dd><dt>Memory</dt><dd>{spec.memory.size_mib} MiB<small>{spec.memory.mode === "dedicated" ? "Dedicated · the host locks this much RAM while the VM runs, and keeps the capacity committed to this VM while it is stopped" : "Maximum · a ceiling only; free pages return to the host when the guest reports them"}</small></dd><dt>Disks</dt><dd>{spec.disks?.length ? <ul className="plain">{spec.disks.map((d, i) => <li key={i}>{d.name} · {d.size_gib} GiB · {busNames[d.bus] || d.bus} · {d.thin ? "Thin" : "Thick"}{d.boot_order > 0 ? ` · boot ${d.boot_order}` : ""}<small>{storage.find(s => s.id === d.storage_id)?.name || d.storage_id}</small></li>)}</ul> : "None recorded"}</dd><dt>Networks</dt><dd>{spec.nics?.length ? <ul className="plain">{spec.nics.map((n, i) => <li key={i}>{networks.find(x => x.id === n.network_id)?.name || n.network_id} · {adapterNames[n.model] || n.model}{n.mtu ? ` · MTU ${n.mtu}` : ""}<small className="mono">{n.mac || "MAC derived from the VM identity"}</small></li>)}</ul> : "None"}</dd><dt>Install ISO</dt><dd>{spec.media_id ? media.find(m => m.id === spec.media_id)?.name || spec.media_id : "None · CD drive empty"}{spec.boot.media_boot_order > 0 && <small>CD drive boot order {spec.boot.media_boot_order}</small>}</dd><dt>Driver disc</dt><dd>{spec.drivers_media_id ? media.find(m => m.id === spec.drivers_media_id)?.name || spec.drivers_media_id : "None · second CD drive empty"}</dd><dt>Firmware</dt><dd>{spec.boot.firmware?.toUpperCase()}<small>Machine type {spec.boot.machine || "q35"}</small></dd><dt>Secure Boot</dt><dd>{secureBootText(spec.boot)}{spec.boot.firmware === "uefi" && !spec.boot.secure_boot && <small>Not recorded in this definition: a UEFI VM defined before the choice existed boots with the enrolled-keys firmware. Edit and save the VM to record it.</small>}</dd><dt>TPM</dt><dd>{tpmText(spec.boot)}</dd><dt>Autostart</dt><dd>{spec.autostart ? "Starts when the host boots" : "Does not start when the host boots"}</dd></>}{vm.operation_id && <><dt>Creation task</dt><dd>{task(vm)}</dd></>}</dl>}
    {!vm.protected && !editing && <p className="panel-note">To install a guest, open the Console tab and use Start VM on its toolbar so the console is attached in time for the boot prompt. Select Edit to change the VM; changes a running guest can take apply while it runs.</p>}
  </section>
  <GuestTools vm={vm} />
  <VMUsagePanel vm={vm} />
  </>;
}

const usageBytes = (value?: number | null) => typeof value === "number" && Number.isFinite(value)
  ? `${(value / GiB).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB`
  : "Not reported";
function VMUsagePanel({ vm }: { vm: VM }) {
  const usage = vm.usage;
  return <section className="panel vm-usage" aria-label="VM usage">
    <div className="section-heading"><h2>VM usage</h2>{status(vm.freshness)}</div>
    {!usage ? <p className="empty">No usage sample reported. This is not zero usage: the VM may be stopped, the host may not support usage observation, or the host could not sample it without waiting for another operation.</p>
      : <dl className="details">
        <dt>CPU</dt><dd>{usage.cpu_percent == null ? "Not reported" : `${usage.cpu_percent}% of allocated vCPUs`}{usage.cpu_percent != null && usage.sample_window_ms ? <small>Average over {(usage.sample_window_ms / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })} seconds</small> : <small>A rate needs two host samples; it is not reported as zero before then.</small>}</dd>
        <dt>Memory allocation</dt><dd>{usageBytes(usage.memory_assigned_bytes)} assigned · {usageBytes(usage.memory_maximum_bytes)} maximum<small>Allocation, not memory used inside the guest.</small></dd>
        <dt>Host memory cost</dt><dd>{usageBytes(usage.memory_host_bytes)}<small>Resident memory used by this VM's host process, not guest-used memory.</small></dd>
        <dt>Disk used</dt><dd>{usageBytes(usage.disk_used_bytes)}<small>Repository space occupied by this VM's disk images.</small></dd>
        <dt>Disk capacity</dt><dd>{usageBytes(usage.disk_capacity_bytes)}<small>Virtual disk size visible to the guest.</small></dd>
      </dl>}
    <p className="panel-note">Usage is a host observation, not a guest-health check. A stale VM status means the host's current execution state is unknown; wait for a fresh report before acting on these numbers.</p>
  </section>;
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
  waiting: "Waiting for guest tools (qemu-guest-agent and spice-vdagent) in the guest · Type into VM still works",
  "no-channel": "Channel missing · this VM was created before console clipboard support; recreate it to add the channel",
};
// A resize needs both halves: a VirtIO display in the VM and the guest's vdagent to act on the
// request. Core now reports the pair rather than the device model alone (BUG-0201), and the
// Controller sees only that verdict — no display model is in the definition it mirrors. It can
// still name the missing half: the vdagent is the channel the clipboard reads "ready" for, so a
// guest whose clipboard is ready and still cannot resize is one without a VirtIO display.
// Blaming the display for every false answer would send an operator to recreate a perfectly
// good VM when installing the guest tools is all that is needed.
export function guestResizeText(g: Guest): string {
  if (g.resize) return "Available · the console can resize the guest to the browser window";
  if (g.clipboard === "ready") return "Not available · the guest is running its tools, so this VM's display is not VirtIO. Only a VM recreated with a VirtIO display can be resized.";
  if (g.clipboard === "no-channel") return "Not available · this VM was created before the console agent channel existed, so nothing in the guest can act on a resize. Recreate it, then install the guest tools.";
  return "Not available · the guest is not running both qemu-guest-agent and the vdagent that changes its resolution. Install the guest tools; Console clipboard reads Ready exactly when resize becomes available. A VM whose display is not VirtIO cannot resize even then.";
}
export function GuestTools({ vm }: { vm: VM }) {
  const g = vm.guest;
  const running = vm.freshness === "fresh" && vm.state === "running";
  return <section className="panel" aria-label="Guest tools">
    <div className="section-heading"><h2>Guest tools</h2>{g ? <span className={`badge ${g.agent === "connected" ? "fresh" : g.agent === "unresponsive" || g.agent === "disconnected" ? "stale" : "unknown"}`}>{g.agent || "unknown"}</span> : status("unknown")}</div>
    {!g ? <p className="empty">{running ? "No guest observation yet. The host reports guest tools for running VMs on Nex Core builds with guest observation; wait for the next report or upgrade Nex Core." : "Guest tools are observed while the VM is running."}</p>
      : <dl className="details">
        <dt>Guest agent</dt><dd>{guestAgentText[g.agent] || g.agent}</dd>
        {g.os && <><dt>Operating system</dt><dd>{g.os}</dd></>}
        {g.hostname && <><dt>Guest hostname</dt><dd>{g.hostname}</dd></>}
        <dt>IP addresses</dt><dd>{g.addresses?.length ? <ul className="plain">{g.addresses.map(a => <li key={a.interface + a.address} className="mono">{a.address} <small>{a.interface}</small></li>)}</ul> : g.agent === "connected" ? "None reported" : "Unknown until the guest agent answers"}</dd>
        <dt>Console clipboard</dt><dd>{guestClipboardText[g.clipboard] || g.clipboard}</dd>
        <dt>Display resize</dt><dd>{guestResizeText(g)}</dd>
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

export const powerActions = [
  { action: "start", label: "Start", icon: "start", when: (state: string) => state === "shut off", confirm: "" },
  { action: "shutdown", label: "Shut down", icon: "shutdown", when: (state: string) => state === "running", confirm: "Asks the guest to shut down (ACPI). A guest without ACPI support ignores it; use Force stop if it stays running." },
  { action: "reboot", label: "Reboot", icon: "reboot", when: (state: string) => state === "running", confirm: "Asks the guest to reboot (ACPI). A guest without ACPI support ignores it." },
  { action: "force-stop", label: "Force stop", icon: "stop", when: (state: string) => state === "running", confirm: "Cuts power immediately, like pulling the plug. Unsaved guest data is lost." },
  { action: "force-reboot", label: "Force reboot", icon: "reboot", when: (state: string) => state === "running", confirm: "Hard-resets the guest immediately (like the reset button). Unsaved guest data is lost." },
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

// powerConfirmText is the confirmation a power action needs on this VM; empty means it is sent
// straight away. Start has no confirmation of its own, but a protected Controller appliance that
// this Controller sees shut off is not this Controller, so starting it could run two Controllers.
export function powerConfirmText(item: typeof powerActions[number], vm: VM): string {
  if (!vm.protected) return item.confirm;
  return item.action === "start" ? "This is a Controller appliance. If another Controller now manages this host, starting it runs two Controllers; never do that. Recover it from the host-local page instead."
    : `${item.confirm} This is the Controller appliance: central management stops until it runs again; recover it from the host-local page.`;
}

// VMPower is the toolbar's power group: Start, Shut down, Reboot, Force stop and Force reboot for
// the selected VM, greyed out with a reason when nothing is selected or the host cannot take the request.
// Requests are durable host tasks; the observed state, refreshed by host reports, tells what
// actually happened. Confirmations use a native modal dialog so they work from the toolbar.
export function VMPower({ vm, host, api, reload, busy, unavailable, onNotice, describedBy }: { vm?: VM; host?: Host; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string, operationID?: string) => void; describedBy?: string }) {
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
      const res = await api<{ operation?: { id?: string } }>(`vms/${encodeURIComponent(vm.id)}/power`, "POST", { request_id: request.current[item.action], action: item.action });
      delete request.current[item.action];
      onNotice(`${item.label} queued for ${vm.name}. Follow its progress in Recent tasks.`, res?.operation?.id);
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
      onClick={e => { opener.current = e.currentTarget; setError(""); if (vm && powerConfirmText(item, vm)) setPending(item); else void submit(item); }}><Icon kind={item.icon} />{item.label}</button>)}
    <dialog ref={dialog} className="confirm-dialog" aria-labelledby="power-confirm-title" onCancel={e => { if (working) e.preventDefault(); }} onClose={() => { setPending(null); setError(""); opener.current?.focus(); }}>
      {pending && vm && <form aria-label={`Confirm ${pending.label.toLowerCase()}`} onSubmit={e => { e.preventDefault(); void submit(pending); }}>
        <h2 id="power-confirm-title">{pending.label} {vm.name}?</h2>
        {powerConfirmText(pending, vm) && <p>{powerConfirmText(pending, vm)}</p>}
        {error && <p className="task-error" role="alert">{error}</p>}
        <div className="actions"><button type="button" disabled={working} onClick={() => setPending(null)}>Cancel</button><button className={pending.action === "force-stop" || pending.action === "force-reboot" ? "danger" : "primary"} disabled={working}>{working ? "Requesting…" : `Confirm ${pending.label.toLowerCase()}`}</button></div>
      </form>}
    </dialog>
  </div>;
}

export function deleteBlocker(vm: VM, host?: Host): string {
  return vm.protected ? "The Controller appliance is managed from its host's local page, not deleted here."
    : !host || host.revoked || host.connection !== "fresh" ? "Delete needs a freshly connected host."
    : !host.capabilities?.includes("vm-delete-v1") ? "The host agent does not support deleting VMs; upgrade Nex Core."
    : vm.freshness !== "fresh" ? "Current VM state is unknown; wait for a fresh host report."
    : vm.state !== "shut off" ? "Stop the VM before deleting it."
    : "";
}

// VMDelete confirms by typing the VM name and optionally wiping disk files on the host.
// openSignal seeds open so a context-menu mount does not notify closed and unmount before the dialog shows.
export function VMDelete({ vm, host, api, reload, busy, unavailable, onNotice, openSignal, onOpenChange, showButton = true }: {
  vm: VM; host?: Host; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean;
  onNotice: (text: string) => void; openSignal?: number; onOpenChange?: (open: boolean) => void; showButton?: boolean;
}) {
  const [open, setOpen] = useState(() => (openSignal ?? 0) > 0);
  const [wipe, setWipe] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const request = useRef<Record<string, string>>({});
  const dialog = useRef<HTMLDialogElement>(null);
  const prevOpen = useRef<boolean | null>(null);
  const blocker = deleteBlocker(vm, host);
  useEffect(() => {
    if (openSignal && openSignal > 0) {
      setError(""); setWipe(false); setName(""); setOpen(true);
    }
  }, [openSignal]);
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (open && !element.open) element.showModal();
    else if (!open && element.open) element.close();
  }, [open]);
  useEffect(() => {
    if (prevOpen.current === null) {
      prevOpen.current = open;
      return;
    }
    if (prevOpen.current === open) return;
    prevOpen.current = open;
    onOpenChange?.(open);
  }, [open, onOpenChange]);
  async function submit() {
    setWorking(true); setError("");
    // One identity per payload: a retry replays it, a changed wipe choice is a new request.
    request.current[String(wipe)] ||= crypto.randomUUID();
    try {
      await api(`vms/${encodeURIComponent(vm.id)}`, "DELETE", { request_id: request.current[String(wipe)], delete_disks: wipe });
      request.current = {};
      setOpen(false);
      if (location.hash === `#/vms/${vm.id}` || location.hash.startsWith(`#/vms/${vm.id}/`) || location.hash.startsWith(`#/console/${vm.id}`)) {
        location.hash = `#/hosts/${vm.host_id}`;
      }
      // Navigating clears page notices on hashchange; the awaited reload lets that run first.
      await reload();
      onNotice(`Delete queued for ${vm.name}. Follow its progress in Recent tasks.`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setWorking(false);
    }
  }
  return <>
    {showButton && <button type="button" className="danger" disabled={busy || working || unavailable || Boolean(blocker)} title={blocker || undefined}
      onClick={() => { setError(""); setWipe(false); setName(""); setOpen(true); }}>Delete VM</button>}
    <dialog ref={dialog} className="confirm-dialog" aria-labelledby="vm-delete-title" onCancel={e => { if (working) e.preventDefault(); }}
      onClose={() => { setOpen(false); setError(""); setWipe(false); setName(""); }}>
      {open && <form onSubmit={e => { e.preventDefault(); void submit(); }}>
        <h2 id="vm-delete-title">Delete {vm.name}?</h2>
        <p>Removes this VM from Nex and libvirt. Type the VM name to confirm.</p>
        <label className="disk-wipe"><input type="checkbox" checked={wipe} onChange={e => setWipe(e.target.checked)} disabled={working} /> Permanently delete disks from storage</label>
        <p className="help">{wipe ? "Disk files will be permanently removed from the host storage after the VM is undefined." : "Disk files stay on the host storage and are not deleted."}</p>
        {error && <p className="task-error" role="alert">{error}</p>}
        <label>VM name<input required autoFocus value={name} onChange={e => setName(e.target.value)} disabled={working}
          pattern={vm.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")} /></label>
        <div className="actions">
          <button type="button" disabled={working} onClick={() => setOpen(false)}>Cancel</button>
          <button className="danger" disabled={working || name !== vm.name}>{working ? "Requesting…" : wipe ? "Delete VM and disks" : "Delete VM"}</button>
        </div>
      </form>}
    </dialog>
  </>;
}
