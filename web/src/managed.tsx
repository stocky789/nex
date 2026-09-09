// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";

export type Registration = { id: string; pool_id: string; host_id: string; name: string; operation_id: string; status: string; error?: string };
export type Storage = Registration & { path: string };
export type Network = Registration & { kind: string; source: string; uplink?: string; vlan?: number };
export type Media = Registration & { storage_id: string; size_bytes: number; sha256: string };
export type VM = Registration & { spec?: { storage_id: string; network_id: string; media_id: string; cpus: number; memory_mib: number; disk_gib: number; firmware: string }; state: string; freshness: string; role: string; protected: boolean };
type NIC = { name: string; mac: string; kind?: string; master?: string; parent?: string; state: string; addresses?: string[] | null };
type Host = { id: string; pool_id: string; hostname: string; connection: string; revoked: boolean; capabilities?: string[]; inventory: { cpu_count: number; memory_bytes: number; nics?: NIC[] | null } | null };
type API = <T>(path: string, method?: string, body?: unknown, progress?: (percent: number) => void) => Promise<T>;
type Props = { account: string; poolID: string; tab: string; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[]; vms: VM[]; api: API; reload: () => Promise<void>; setTab: (tab: string) => void; unavailable: boolean };
const GiB = 1024 ** 3;
const status = (value: string) => <span className={`badge ${value}`}>{value || "unknown"}</span>;
export const describeNetwork = (n: Network) => n.kind === "network" ? `libvirt network ${n.source}` : n.uplink ? (n.vlan ? `VLAN ${n.vlan} on ${n.uplink}` : `Untagged on ${n.uplink}`) : `Existing bridge ${n.source}`;
const task = (value: Registration) => <>{status(value.status)}{value.error && <p className="task-error">{value.error}</p>}{value.operation_id && <small><a href="#/tasks">Task {value.operation_id}</a></small>}</>;

type Pending = Record<string, { payload: string; id: string }>;
function pendingRequests(key: string): Pending {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || "{}");
    if (value && typeof value === "object" && Object.values(value).every(v => v && typeof v === "object" && "id" in v && typeof v.id === "string" && "payload" in v && typeof v.payload === "string")) return value;
  } catch {}
  return {};
}

export function ManagedVMs({ account, poolID, tab, hosts: allHosts, storage: allStorage, networks: allNetworks, media: allMedia, vms: allVMs, api, reload, setTab, unavailable }: Props) {
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
  const [step, setStep] = useState(0);
  const [draft, setDraft] = useState<{ name: string; host_id: string; storage_id: string; network_id: string; media_id: string; cpus: number; memory_mib: number; disk_gib: number; firmware: string }>(() => ({ name: "", host_id: "", storage_id: "", network_id: "", media_id: "", cpus: 2, memory_mib: 2048, disk_gib: 20, firmware: "uefi", ...restored("vm") }));
  const [file, setFile] = useState<File | null>(null);
  const [progress, setProgress] = useState<number | null>(null);
  const [uploadStorage, setUploadStorage] = useState<string>(restored("iso").storage_id || "");
  const [networkHost, setNetworkHost] = useState<string>(restored("network").host_id || "");
  const [connection, setConnection] = useState<string>(() => { const r = restored("network"); return r.kind === "vlan" ? `vlan:${r.uplink}` : r.kind ? "custom" : ""; });
  const lock = useRef(false);
  const mounted = useRef(true);
  useEffect(() => () => { mounted.current = false; }, []);
  const input = useRef<HTMLInputElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const selectedHost = hosts.find(h => h.id === draft.host_id);
  const hostName = (id: string) => hosts.find(h => h.id === id)?.hostname || id;
  const readyHost = (h: Host) => !h.revoked && h.connection === "fresh" && Boolean(h.capabilities?.includes("managed-vm-v1"));
  function persist() {
    try { sessionStorage.setItem(persistenceKey, JSON.stringify(requests.current)); }
    catch { setNotice("Browser storage is unavailable. Keep this page open until the task is confirmed so a retry can use the same request."); }
  }
  const availableStorage = storage.filter(s => s.host_id === draft.host_id && s.status === "succeeded" && (!draft.media_id || s.id === media.find(m => m.id === draft.media_id)?.storage_id));
  const readyNetworks = (id: string) => networks.filter(n => n.host_id === id && n.status === "succeeded").sort((a, b) => (a.vlan || 0) - (b.vlan || 0) || a.name.localeCompare(b.name));
  const availableNetworks = readyNetworks(draft.host_id);
  const nics = hosts.find(h => h.id === networkHost)?.inventory?.nics || [];
  const uplinks = nics.filter(n => !n.kind || n.kind === "physical" || n.kind === "bond");
  const bridges = nics.filter(n => n.kind === "bridge");
  const availableMedia = media.filter(m => m.host_id === draft.host_id && m.status === "succeeded");
  function requestID(scope: string, payload: unknown) {
    const encoded = JSON.stringify(payload);
    if (requests.current[scope] && requests.current[scope].payload !== encoded) throw new Error("An earlier submission has an uncertain outcome. Restore its original entries and retry to reconcile that request, or inspect Task history before starting another request.");
    requests.current[scope] ??= { payload: encoded, id: crypto.randomUUID() };
    persist();
    return requests.current[scope].id;
  }
  async function run(scope: string, action: () => Promise<void>) {
    if (lock.current) return;
    lock.current = true;
    setBusy(true); setError(""); setNotice("");
    try { await action(); } catch (e) {
      const code = (e as Error & { status?: number }).status;
      if ([400, 401, 403, 404, 413, 507].includes(code || 0)) { delete requests.current[scope]; persist(); }
      if (code === 409) {
        try {
          const current = await api<{ request_id: string }[]>("operations");
          if (current.length < 1000 && !current.some(op => op.request_id === requests.current[scope]?.id)) { delete requests.current[scope]; persist(); }
        } catch { /* Keep the original identity while the outcome is uncertain. */ }
      }
      setError((e as Error).message);
    }
    finally { lock.current = false; setBusy(false); }
  }
  function open(name: string) { setForm(name); setError(""); setNotice(""); }
  function chooseFile(value: File | null) {
    if (busy) return;
    setFile(null); setError(""); setProgress(null);
    if (!value) return;
    if (!/\.iso$/i.test(value.name) || value.size < 32774 || value.size > 8 * GiB) {
      setError("Choose a non-empty ISO image ending in .iso, between 32 KiB and 8 GiB."); return;
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
      delete requests.current[kind]; persist();
      setForm(""); await reload(); setNotice(`Registration ${result.status}. Follow host validation in Task history; registration does not make local storage shared.`);
    });
  }
  function next() { setStep(s => s + 1); setTimeout(() => heading.current?.focus(), 0); }
  async function create() {
    const payload = { ...draft, pool_id: poolID };
    await run("vm", async () => {
      const result = await api<{ vm_id: string }>("vms", "POST", { ...payload, request_id: requestID("vm", payload) });
      delete requests.current.vm; persist();
      await reload(); if (mounted.current) { setForm(""); location.hash = `/vms/${result.vm_id}`; }
    });
  }
  const vmTab = tab === "Virtual machines";
  const relevant = ["Virtual machines", "Storage", "Networking", "ISO library"].includes(tab);
  return <div hidden={!relevant}>
    {error && <p className="alert" role="alert">{error} Your entries are retained. Review Task history before retrying an uncertain result.</p>}
    {notice && <p className="notice" role="status">{notice} <a href="#/tasks">Task history</a></p>}
    {unavailable && <p className="warning">Controller inventory may be out of date. Refresh the view before submitting changes.</p>}
    <div className="managed-actions actions">
      {vmTab && <button className="primary" disabled={busy || unavailable} onClick={() => open("vm")}>Create VM</button>}
      {tab === "Storage" && <button disabled={busy || unavailable} onClick={() => open("storage")}>Register local storage</button>}
      {tab === "Networking" && <button disabled={busy || unavailable} onClick={() => open("network")}>Register network</button>}
      {tab === "ISO library" && <button disabled={busy || unavailable} onClick={() => open("iso")}>Upload ISO</button>}
    </div>
    {(form === "storage" || form === "network") && <section className="panel form-panel" aria-label={form === "storage" ? "Register local storage" : "Register network"}>
      <div className="section-heading"><h2>{form === "storage" ? "Register local storage" : "Register guest network"}</h2><button disabled={busy} onClick={() => setForm("")}>Close</button></div>
      {form === "storage" && <p>Select an existing directory on one host. Nex checks access and free space; no disk is formatted. Other hosts cannot use this local directory.</p>}
      <form onSubmit={e => register(e, form as "storage" | "network")}>
        <label>Name<input name="name" required maxLength={100} autoFocus defaultValue={restored(form).name || ""} /></label>
        <label>Host<select name="host_id" required defaultValue={restored(form).host_id || ""} onChange={e => { setNetworkHost(e.target.value); setConnection(""); }}><option value="">Select host</option>{hosts.map(h => <option key={h.id} value={h.id} disabled={!readyHost(h)}>{h.hostname} · {h.connection}{!h.capabilities?.includes("managed-vm-v1") ? " · agent upgrade required" : ""}</option>)}</select></label>
        {form === "storage" ? <label>Existing directory<input name="path" required defaultValue={restored("storage").path || ""} pattern="/.*" placeholder="/var/lib/libvirt/images" /><small>Use an absolute directory path on the selected host.</small></label> : <>
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
        </>}
        {!hosts.some(readyHost) && <p className="warning">A freshly connected host with managed-VM support is required. Enroll, reconnect or upgrade the host agent first.</p>}
        <button className="primary" disabled={busy || unavailable || !hosts.some(readyHost)}>{busy ? "Submitting…" : "Register and check access"}</button>
      </form>
    </section>}
    {form === "iso" && <section className="panel form-panel" aria-label="Upload ISO">
      <div className="section-heading"><h2>Upload guest ISO</h2><button disabled={busy} onClick={() => setForm("")}>Close</button></div>
      <form onSubmit={e => { e.preventDefault(); if (!file) { setError("Choose an ISO file first."); return; } void run("iso", async () => {
        const payload = { storage_id: uploadStorage, name: file.name, size: file.size, modified: file.lastModified };
        const query = new URLSearchParams({ storage_id: uploadStorage, name: file.name, request_id: requestID("iso", payload) });
        setProgress(0);
        const result = await api<Media>(`media?${query}`, "POST", file, setProgress);
        delete requests.current.iso; persist();
        setProgress(null); setFile(null); if (input.current) input.current.value = "";
        await reload(); setNotice(`ISO received by Controller; host transfer ${result.status}. Wait for a succeeded result before creating a VM.`);
      }); }}>
        <label>Destination storage<select required value={uploadStorage} onChange={e => setUploadStorage(e.target.value)} disabled={busy}><option value="">Select validated local storage</option>{storage.filter(s => s.status === "succeeded").map(s => <option key={s.id} value={s.id}>{s.name} · {hostName(s.host_id)}</option>)}</select></label>
        <div className="iso-drop" onDragOver={e => e.preventDefault()} onDrop={e => { e.preventDefault(); if (e.dataTransfer.files.length !== 1) { setError("Drop one ISO image at a time."); return; } chooseFile(e.dataTransfer.files[0]); }}>
          <label>ISO file<input ref={input} type="file" accept=".iso,application/x-iso9660-image" disabled={busy} onChange={e => chooseFile(e.target.files?.[0] || null)} /></label>
          <p className="help">Drop one .iso image here or use the file picker. Maximum 8 GiB; the Controller and host validate its content and available space.</p>
          {file && <p className="iso-filename">{file.name} · {(file.size / GiB).toFixed(2)} GiB</p>}
        </div>
        {busy && <div role="status"><label>Upload to Controller<progress max={100} value={progress ?? undefined} /></label>{progress === 100 ? "Upload sent; waiting for Controller validation…" : `${progress ?? 0}% sent`}</div>}
        {!storage.some(s => s.status === "succeeded") && <p className="help">Register local storage and wait for its access check to succeed first.</p>}
        <button className="primary" disabled={busy || unavailable || !file || !uploadStorage}>{busy ? "Uploading…" : "Upload and transfer ISO"}</button>
      </form>
    </section>}
    {form === "vm" && <section className="panel form-panel" aria-label="Create VM">
      <div className="section-heading"><h2 ref={heading} tabIndex={-1}>Create VM · {step + 1} of 4: {["Name and placement", "Install source and resources", "Storage and network", "Review"][step]}</h2><button disabled={busy} onClick={() => setForm("")}>Close</button></div>
      <form onSubmit={e => { e.preventDefault(); if (step < 3) next(); else void create(); }}>
        {step === 0 && <>
          <label>VM name<input required maxLength={63} pattern={"[a-z0-9]([a-z0-9\\-]{0,61}[a-z0-9])?"} title="Use 1–63 lowercase letters, digits or hyphens; start and end with a letter or digit." value={draft.name} autoFocus onChange={e => setDraft({ ...draft, name: e.target.value })} /></label>
          <label>Placement host<select required value={draft.host_id} onChange={e => { const ready = readyNetworks(e.target.value); setDraft({ ...draft, host_id: e.target.value, storage_id: "", network_id: ready.length === 1 ? ready[0].id : "", media_id: "" }); }}><option value="">Select host</option>{hosts.map(h => <option key={h.id} value={h.id} disabled={!readyHost(h)}>{h.hostname} · {h.connection}{!h.capabilities?.includes("managed-vm-v1") ? " · agent upgrade required" : ""}</option>)}</select></label>
          <p className="help">The VM belongs to this pool and is assigned to the selected host. Local storage and ISO access must be validated for that host.</p>
        </>}
        {step === 1 && <>
          <label>Install ISO<select required value={draft.media_id} onChange={e => setDraft({ ...draft, media_id: e.target.value, storage_id: media.find(m => m.id === e.target.value)?.storage_id || "" })}><option value="">Select a transferred ISO</option>{availableMedia.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>
          {!availableMedia.length && <p className="help">No transferred ISO is available on this host. <button type="button" onClick={() => setTab("ISO library")}>Open ISO library</button></p>}
          <div className="vm-fields"><label>Virtual CPUs<input type="number" required min={1} max={Math.min(selectedHost?.inventory?.cpu_count || 64, 64)} step={1} value={Number.isNaN(draft.cpus) ? "" : draft.cpus} onChange={e => setDraft({ ...draft, cpus: e.target.valueAsNumber })} /></label><label>Memory (MiB)<input type="number" required min={512} max={Math.min(selectedHost?.inventory ? Math.floor(selectedHost.inventory.memory_bytes / 1024 ** 2) : 262144, 262144)} step={1} value={Number.isNaN(draft.memory_mib) ? "" : draft.memory_mib} onChange={e => setDraft({ ...draft, memory_mib: e.target.valueAsNumber })} /></label></div>
          <p className="help">Host-reported capacity: {selectedHost?.inventory ? `${selectedHost.inventory.cpu_count} CPUs · ${Math.floor(selectedHost.inventory.memory_bytes / 1024 ** 2)} MiB total memory` : "unknown"}. Nex checks current availability before creation.</p>
        </>}
        {step === 2 && <>
          <label>VM storage<select required value={draft.storage_id} onChange={e => setDraft({ ...draft, storage_id: e.target.value })}><option value="">Select validated local storage</option>{availableStorage.map(s => <option key={s.id} value={s.id}>{s.name} · {s.path}</option>)}</select></label>
          <label>Disk size (GiB)<input type="number" required min={1} max={2048} step={1} value={Number.isNaN(draft.disk_gib) ? "" : draft.disk_gib} onChange={e => setDraft({ ...draft, disk_gib: e.target.valueAsNumber })} /></label>
          <label>Guest network<select required value={draft.network_id} onChange={e => setDraft({ ...draft, network_id: e.target.value })}><option value="">Select validated network</option>{availableNetworks.map(n => <option key={n.id} value={n.id}>{n.name} · {describeNetwork(n)}</option>)}</select></label>
          {!availableNetworks.length && <p className="help">No verified network on this host. <button type="button" onClick={() => setTab("Networking")}>Open Networking</button></p>}
          <label>Firmware<select value={draft.firmware} onChange={e => setDraft({ ...draft, firmware: e.target.value })}><option value="uefi">UEFI</option><option value="bios">BIOS</option></select></label>
          <p className="help">The disk uses the same local storage as the selected ISO. A new VirtIO disk and network adapter will be created. Existing disks and host networking are preserved.</p>
        </>}
        {step === 3 && <>
          <dl className="details"><dt>VM name</dt><dd>{draft.name}</dd><dt>Assigned host</dt><dd>{hostName(draft.host_id)}</dd><dt>Install ISO</dt><dd>{media.find(m => m.id === draft.media_id)?.name || "Unavailable"}</dd><dt>CPU / memory</dt><dd>{draft.cpus} vCPUs · {draft.memory_mib} MiB</dd><dt>Storage / new disk</dt><dd>{storage.find(s => s.id === draft.storage_id)?.name || "Unavailable"} · {draft.disk_gib} GiB</dd><dt>Guest network</dt><dd>{(n => n ? `${n.name} · ${describeNetwork(n)}` : "Unavailable")(networks.find(n => n.id === draft.network_id))}</dd><dt>Firmware</dt><dd>{draft.firmware.toUpperCase()}</dd></dl>
          <p>The VM will be created stopped with the ISO attached. Follow the durable task to see validation and creation results.</p>
        </>}
        {selectedHost && !readyHost(selectedHost) && <p className="warning">The selected host is unavailable or lacks managed-VM support. Reconnect or upgrade the agent before creating the VM.</p>}
        <div className="actions">{step > 0 && <button type="button" disabled={busy} onClick={() => { setStep(s => s - 1); setTimeout(() => heading.current?.focus(), 0); }}>Back</button>}<button className="primary" disabled={busy || unavailable || (step === 3 && (!selectedHost || !readyHost(selectedHost)))}>{busy ? "Submitting…" : step === 3 ? "Create stopped VM" : "Next"}</button></div>
      </form>
    </section>}
    {vmTab && <section className="panel"><div className="section-heading"><h2>Virtual machines</h2><span className="muted">{vms.length} in pool</span></div>{vms.length ? <div className="table-scroll"><table><thead><tr><th>Name</th><th>Assigned host</th><th>Observed state</th><th>Freshness</th><th>Creation task</th></tr></thead><tbody>{vms.map(v => <tr key={v.id}><td><a href={`#/vms/${v.id}`}>{v.name}</a>{v.protected && <small>Protected Controller appliance</small>}</td><td>{hostName(v.host_id)}</td><td>{v.state || "unknown"}</td><td>{status(v.freshness)}</td><td>{v.operation_id ? task(v) : "—"}</td></tr>)}</tbody></table></div> : <p className="empty">No virtual machines in this pool. Register storage and a network, upload an ISO, then create your first VM.</p>}<p className="panel-note">A stale or unreachable host does not mean its VMs have stopped. Creation results and current observations are separate.</p></section>}
    {tab === "Storage" && <section className="panel"><div className="section-heading"><h2>Pool local storage</h2></div>{storage.length ? <div className="table-scroll"><table><thead><tr><th>Name / directory</th><th>Accessible host</th><th>Access check</th></tr></thead><tbody>{storage.map(s => <tr key={s.id}><td>{s.name}<small>{s.path}</small></td><td>{hostName(s.host_id)}<small>Local only</small></td><td>{task(s)}</td></tr>)}</tbody></table></div> : <p className="empty">No local storage registered. Register an existing host directory to store guest disks and ISO files.</p>}</section>}
    {tab === "Networking" && <section className="panel"><div className="section-heading"><h2>Pool guest networks</h2></div>{networks.length ? <div className="table-scroll"><table><thead><tr><th>Name</th><th>Host</th><th>Connection</th><th>VMs</th><th>Host check</th></tr></thead><tbody>{[...networks].sort((a, b) => (a.vlan || 0) - (b.vlan || 0) || a.name.localeCompare(b.name)).map(n => <tr key={n.id}><td>{n.name}</td><td>{hostName(n.host_id)}</td><td>{describeNetwork(n)}<small className="mono">{n.source}</small></td><td>{vms.filter(v => v.spec?.network_id === n.id).length}</td><td>{task(n)}</td></tr>)}</tbody></table></div> : <p className="empty">No networks reported yet.</p>}</section>}
    {tab === "ISO library" && <section className="panel"><div className="section-heading"><h2>Guest ISO library</h2></div>{media.length ? <div className="table-scroll"><table><thead><tr><th>ISO</th><th>Host / storage</th><th>Size</th><th>Transfer task</th></tr></thead><tbody>{media.map(m => <tr key={m.id}><td>{m.name}<small className="mono">SHA-256: {m.sha256}</small></td><td>{hostName(m.host_id)}<small>{storage.find(s => s.id === m.storage_id)?.name || m.storage_id}</small></td><td>{(m.size_bytes / GiB).toFixed(2)} GiB</td><td>{task(m)}</td></tr>)}</tbody></table></div> : <p className="empty">No ISO images uploaded. Select validated storage and upload installation media.</p>}</section>}
  </div>;
}

export function VMDetails({ vm, hosts, storage, networks, media }: { vm: VM; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[] }) {
  const spec = vm.spec;
  return <section className="panel"><div className="section-heading"><h2>VM details</h2>{status(vm.freshness)}</div>
    {vm.freshness !== "fresh" && <p className="warning">Current execution state is unknown. Last reported state: {vm.state || "unknown"}. An unreachable host does not prove that this VM stopped.</p>}
    <dl className="details"><dt>VM identity</dt><dd>{vm.id}</dd><dt>Assigned host</dt><dd><a href={`#/hosts/${vm.host_id}`}>{hosts.find(h => h.id === vm.host_id)?.hostname || vm.host_id}</a></dd><dt>Observed state</dt><dd>{vm.state || "unknown"}</dd><dt>Role</dt><dd>{vm.protected ? "Protected Controller appliance" : "Guest VM"}</dd>{spec && <><dt>Virtual CPUs</dt><dd>{spec.cpus}</dd><dt>Memory</dt><dd>{spec.memory_mib} MiB</dd><dt>Disk</dt><dd>{spec.disk_gib} GiB · VirtIO</dd><dt>Storage</dt><dd>{storage.find(s => s.id === spec.storage_id)?.name || spec.storage_id}</dd><dt>Network</dt><dd>{networks.find(n => n.id === spec.network_id)?.name || spec.network_id}</dd><dt>Install ISO</dt><dd>{media.find(m => m.id === spec.media_id)?.name || spec.media_id}</dd><dt>Firmware</dt><dd>{spec.firmware?.toUpperCase()}</dd></>}{vm.operation_id && <><dt>Creation task</dt><dd>{task(vm)}</dd></>}</dl>
    {!vm.protected && <p className="panel-note">Creation provisions a stopped VM. Use the Power controls to start it, then open the Console tab.</p>}
  </section>;
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
            : "Transferred · last verified";
        }
        return <tr key={image.id}><td>{image.name}</td><td>{destination?.name || image.storage_id}{destination && <small>{destination.path}</small>}</td><td>{(image.size_bytes / GiB).toLocaleString(undefined, { maximumFractionDigits: 2 })} GiB<small>{image.size_bytes.toLocaleString()} bytes</small></td><td>{availability}</td><td>{task(image)}</td></tr>;
      })}</tbody>
    </table></div> : <p className="empty">{unavailable ? "ISO records could not be loaded. Retry when the Controller is reachable." : "No ISO transfers recorded for this host. Upload an ISO to this host's storage from the pool's ISO library."}</p>}
    <p className="panel-note">Availability is based on completed transfer records, validated storage and host connectivity. Files are not continuously rescanned; VM creation checks the selected ISO again.</p>
  </section>;
}

const powerActions = [
  { action: "start", label: "Start", when: (state: string) => state === "shut off", confirm: "" },
  { action: "shutdown", label: "Shut down", when: (state: string) => state === "running", confirm: "Asks the guest to shut down (ACPI). A guest without ACPI support ignores it; use Force stop if it stays running." },
  { action: "reboot", label: "Reboot", when: (state: string) => state === "running", confirm: "Asks the guest to reboot (ACPI). A guest without ACPI support ignores it." },
  { action: "force-stop", label: "Force stop", when: (state: string) => state === "running", confirm: "Cuts power immediately, like pulling the plug. Unsaved guest data is lost." },
] as const;

// VMPower shows central power controls for one VM. Requests are durable host tasks; the
// observed state, refreshed by host reports, tells what actually happened.
export function VMPower({ vm, host, api, reload, busy, unavailable, onNotice }: { vm: VM; host?: Host; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string) => void }) {
  const [pending, setPending] = useState<typeof powerActions[number] | null>(null);
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const request = useRef<Record<string, string>>({});
  const blocker = !host || host.revoked || host.connection !== "fresh" ? "Power controls need a freshly connected host."
    : !host.capabilities?.includes("vm-power-v1") ? "The host agent does not support central power requests; upgrade Nex Core."
    : vm.freshness !== "fresh" ? "Current VM state is unknown; wait for a fresh host report."
    : vm.state !== "running" && vm.state !== "shut off" ? `The VM is ${vm.state || "unknown"}; resolve that state on its host before a power request.`
    : "";
  async function submit(item: typeof powerActions[number]) {
    setWorking(true); setError("");
    request.current[item.action] ??= crypto.randomUUID();
    try {
      await api(`vms/${encodeURIComponent(vm.id)}/power`, "POST", { request_id: request.current[item.action], action: item.action });
      delete request.current[item.action];
      setPending(null);
      onNotice(`${item.label} requested for ${vm.name}. Follow the task; the observed state updates with the next host report.`);
      await reload();
    } catch (e) { setError((e as Error).message); }
    finally { setWorking(false); }
  }
  return <section className="panel vm-power" aria-label={`Power controls for ${vm.name}`}>
    <div className="section-heading"><h2>Power</h2><span className="muted">Observed: {vm.state || "unknown"} · {status(vm.freshness)}</span></div>
    <div className="actions vm-power-actions">
      {powerActions.map(item => <button key={item.action} className={item.action === "force-stop" ? "danger" : item.action === "start" ? "primary" : ""} aria-describedby={blocker ? `power-blocker-${vm.id}` : undefined} disabled={busy || working || unavailable || Boolean(blocker) || !item.when(vm.state)} onClick={() => { setError(""); if (item.confirm) setPending(item); else void submit(item); }}>{item.label}</button>)}
      {blocker && <span className="help" id={`power-blocker-${vm.id}`}>{blocker}</span>}
    </div>
    {error && <p className="task-error" role="alert">{error}</p>}
    {pending && <form className="confirm-power" aria-label={`Confirm ${pending.label.toLowerCase()}`} onSubmit={e => { e.preventDefault(); void submit(pending); }}>
      <p><strong>{pending.label} {vm.name}?</strong> {pending.confirm}{vm.protected && " This is the Controller appliance: central management stops until it runs again; recover it from the host-local page."}</p>
      <div className="actions"><button type="button" autoFocus disabled={working} onClick={() => setPending(null)}>Cancel</button><button className={pending.action === "force-stop" ? "danger" : "primary"} disabled={working}>{working ? "Requesting…" : `Confirm ${pending.label.toLowerCase()}`}</button></div>
    </form>}
  </section>;
}
