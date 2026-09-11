// SPDX-License-Identifier: AGPL-3.0-only
import { useRef, useState, type FormEvent, type InputHTMLAttributes } from "react";
import type { Host, Storage } from "./managed";

type API = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Props = { poolID: string; hosts: Host[]; storage: Storage[]; api: API; reload: () => Promise<void>; unavailable: boolean; onNotice: (message: string) => void };
type Kind = "local" | "nfs" | "zfs" | "smb" | "iscsi";
type Draft = { name: string; host_id: string; kind: Kind; path: string; server: string; export: string; pool: string; dataset: string; share: string; portal: string; target_iqn: string; lun: string; wwid: string; filesystem_uuid: string; exclusive_target: boolean; username: string; password: string };
type Receipt = { receipt: string; pool_id: string; host_id: string; status: string; error?: string; created_at?: string; retire_eligible: boolean };

const blank: Draft = { name: "", host_id: "", kind: "local", path: "", server: "", export: "", pool: "", dataset: "", share: "", portal: "", target_iqn: "", lun: "", wwid: "", filesystem_uuid: "", exclusive_target: false, username: "", password: "" };
const labels: Record<Kind, string> = { local: "Local directory", nfs: "NFSv4 export", zfs: "OpenZFS dataset", smb: "SMB3 share", iscsi: "iSCSI LUN" };
const status = (value: string) => <span className={`badge ${value}`}>{value || "unknown"}</span>;
const needsCredential = (kind: Kind, d?: Draft) => kind === "smb" || (kind === "iscsi" && Boolean(d?.username || d?.password));
export const storageUsable = (s: Storage, hostID: string) => s.host_id === hostID && s.status === "succeeded" && s.access?.readable === true && s.access?.writable === true && s.access?.status === "healthy" && s.capabilities?.vm_disk === true && (!s.capabilities.eligible_host_ids || s.capabilities.eligible_host_ids.includes(hostID));
export function storageSource(s: Storage) {
  const source = s.source;
  if (source?.nfs) return `${source.nfs.server}:${source.nfs.export}`;
  if (source?.smb) return `//${source.smb.server}/${source.smb.share}`;
  if (source?.zfs) return `${source.zfs.pool}/${source.zfs.dataset}`;
  if (source?.iscsi) return `${source.iscsi.target_iqn} · LUN ${source.iscsi.lun}`;
  return s.path || "Source unavailable";
}
export function storageChecked(value?: string) {
  if (!value) return "Not checked";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) || date.getUTCFullYear() <= 1 ? "Not checked" : `Checked ${date.toLocaleString()}`;
}
// A v2 repository exists only after Core assigns an ID/generation.  A queued browser request
// cannot safely be rechecked, updated or detached yet; Core owns the eventual identity.
export const storageLifecycleReady = (s: Storage) => !s.kind ? s.status === "succeeded" : s.status === "succeeded" && (s.generation || 0) > 0;
export const storageAccess = (s: Storage) => s.access?.status || s.status || "unknown";

function source(d: Draft) {
  switch (d.kind) {
    case "local": return undefined;
    case "nfs": return { nfs: { server: d.server, export: d.export } };
    case "zfs": return { zfs: { pool: d.pool, dataset: d.dataset, mount_path: d.path } };
    case "smb": return { smb: { server: d.server, share: d.share } };
    case "iscsi": return { iscsi: { portal: d.portal, target_iqn: d.target_iqn, lun: Number(d.lun), wwid: d.wwid, filesystem_uuid: d.filesystem_uuid, exclusive_target: d.exclusive_target } };
  }
}

function SourceFields({ draft, setDraft, disabled }: { draft: Draft; setDraft: (patch: Partial<Draft>) => void; disabled: boolean }) {
  const field = (key: Exclude<keyof Draft, "exclusive_target">, title: string, placeholder: string, extra: Partial<InputHTMLAttributes<HTMLInputElement>> = {}) => <label>{title}<input required value={draft[key] as string} disabled={disabled} placeholder={placeholder} onChange={e => setDraft({ [key]: e.target.value } as Partial<Draft>)} {...extra} /></label>;
  if (draft.kind === "local") return <>{field("path", "Existing directory", "/var/lib/libvirt/images", { pattern: "/.*" })}</>;
  if (draft.kind === "nfs") return <>{field("server", "NFS server", "nfs.example.test")}{field("export", "Export path", "/exports/vms", { pattern: "/.*" })}</>;
  if (draft.kind === "zfs") return <>{field("pool", "Imported pool", "tank")}{field("dataset", "Existing dataset", "vms")}{field("path", "Reported mount point", "/tank/vms", { pattern: "/.*" })}<p className="help">Nex uses an already imported and mounted filesystem dataset. It never creates, imports, upgrades or changes ZFS pool properties.</p></>;
  if (draft.kind === "smb") return <>{field("server", "SMB server", "files.example.test")}{field("share", "Share", "vms")}{field("username", "Account", "nex-vm")}{field("password", "Password", "", { type: "password", autoComplete: "new-password" })}</>;
  return <>{field("portal", "Target portal", "storage.example.test:3260")}{field("target_iqn", "Target IQN", "iqn.2026-01.example.test:vm-storage")}{field("lun", "LUN", "0", { inputMode: "numeric" })}{field("wwid", "LUN WWID", "360000000000000000000000000000001")}{field("filesystem_uuid", "Existing filesystem UUID", "00000000-0000-0000-0000-000000000000")}<label className="console-option"><input required type="checkbox" style={{ width: "auto" }} checked={draft.exclusive_target} disabled={disabled} onChange={e => setDraft({ exclusive_target: e.target.checked })} /> This LUN is restricted to this Core initiator only</label><label>CHAP account<input value={draft.username} disabled={disabled} placeholder="Optional" onChange={e => setDraft({ username: e.target.value })} /></label><label>CHAP secret<input type="password" autoComplete="new-password" value={draft.password} disabled={disabled} onChange={e => setDraft({ password: e.target.value })} /></label><p className="help">Nex Core selects its managed mount point after allocating the repository ID. The LUN must already have one ext4 or XFS filesystem and be restricted to this Core initiator only; Nex does not format it or enable multiple writers.</p></>;
}

export function StorageRepositories({ poolID, hosts, storage, api, reload, unavailable, onNotice }: Props) {
  const [form, setForm] = useState(false);
  const [draft, setDraft] = useState<Draft>(blank);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [edit, setEdit] = useState<Storage | null>(null);
  const detach = useRef<HTMLDialogElement>(null);
  const [remove, setRemove] = useState<Storage | null>(null);
  const [receiptHost, setReceiptHost] = useState("");
  const [receipts, setReceipts] = useState<Receipt[] | null>(null);
  const [retire, setRetire] = useState<Receipt | null>(null);
  const retireDialog = useRef<HTMLDialogElement>(null);
  const patch = (value: Partial<Draft>) => setDraft(d => ({ ...d, ...value }));
  const activeHosts = hosts.filter(h => !h.revoked && h.connection === "fresh" && h.capabilities?.includes("managed-vm-v1"));
  function close() { setForm(false); setEdit(null); setDraft(blank); setError(""); }
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const request_id = crypto.randomUUID();
      const registration = { name: draft.name.trim(), kind: draft.kind, ...(draft.kind === "local" ? { path: draft.path } : { source: source(draft) }) };
      const credentials = needsCredential(draft.kind, draft) ? { username: draft.username, secret: draft.password } : undefined;
      // Passwords deliberately never enter session storage, notices, task payloads, or a GET response.
      if (edit) {
        let credential_receipt = "";
        if (credentials) credential_receipt = (await api<{ receipt: string }>("storage/credentials", "POST", { request_id, pool_id: poolID, host_id: draft.host_id, repository_id: edit.id, credentials })).receipt;
        await api(`storage/${encodeURIComponent(edit.id)}/update`, "POST", { request_id, pool_id: poolID, host_id: draft.host_id, expected_generation: edit.generation, registration, ...(credential_receipt ? { credential_receipt } : {}) });
        onNotice(`Storage update queued for ${registration.name}. Wait for Nex Core to report the new access check.`);
      } else {
        // New remote credentials are handed off before the durable registration. The Controller
        // allocates no repository state; Core binds the receipt to this request.
        let credential_receipt = "";
        if (credentials) credential_receipt = (await api<{ receipt: string }>("storage/credentials", "POST", { request_id: crypto.randomUUID(), registration_request_id: request_id, pool_id: poolID, host_id: draft.host_id, credentials })).receipt;
        await api("storage", "POST", { request_id, pool_id: poolID, host_id: draft.host_id, ...registration, ...(credential_receipt ? { credential_receipt, credential_request_id: request_id } : {}) });
        onNotice(`Storage registration queued for ${registration.name}. The repository appears after Nex Core confirms it.`);
      }
      await reload(); close();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  function beginEdit(s: Storage) { const kind = (Object.hasOwn(labels, s.kind || "") ? s.kind : "local") as Kind; const values: Partial<Draft> = kind === "nfs" ? s.source?.nfs || {} : kind === "zfs" ? { ...s.source?.zfs, path: s.path || s.source?.zfs?.mount_path } : kind === "smb" ? s.source?.smb || {} : kind === "iscsi" ? { ...s.source?.iscsi, lun: String(s.source?.iscsi?.lun ?? "") } : { path: s.path }; setEdit(s); setForm(true); setError(""); setDraft({ ...blank, name: s.name, host_id: s.host_id, kind, ...values }); }
  async function recheck(s: Storage) { setBusy(true); setError(""); try { await api(`storage/${encodeURIComponent(s.id)}/recheck`, "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: s.host_id, expected_generation: s.generation }); await reload(); onNotice(`Access recheck queued for ${s.name}.`); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function detachStorage() { if (!remove) return; setBusy(true); setError(""); try { await api(`storage/${encodeURIComponent(remove.id)}/detach`, "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: remove.host_id, expected_generation: remove.generation }); await reload(); onNotice(`Detach queued for ${remove.name}. Nex never erases storage data.`); detach.current?.close(); setRemove(null); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function findReceipts() { if (!receiptHost) return; setBusy(true); setError(""); try { setReceipts(await api<Receipt[]>(`storage/credentials?${new URLSearchParams({ pool_id: poolID, host_id: receiptHost })}`)); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function retireReceipt() { if (!retire) return; setBusy(true); setError(""); try { await api(`storage/credentials/${encodeURIComponent(retire.receipt)}/retire`, "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: retire.host_id }); onNotice(`Credential retirement queued for ${retire.receipt}. Nex Core confirms whether it is still unreferenced.`); retireDialog.current?.close(); setReceipts(items => items?.filter(item => item.receipt !== retire.receipt) || []); setRetire(null); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  return <>
    {error && <p className="alert" role="alert">{error}</p>}
    <div className="managed-actions actions"><button className="primary" disabled={busy || unavailable} onClick={() => { setForm(true); setEdit(null); setDraft(blank); }}>Register storage</button></div>
    {form && <section className="panel form-panel storage-form" aria-label={edit ? "Update storage" : "Register storage"}><div className="section-heading"><h2>{edit ? `Update ${edit.name}` : "Register VM storage"}</h2><button type="button" disabled={busy} onClick={close}>Close</button></div><p>Register an existing storage source on one Core host. Nex verifies Core and QEMU access before it can hold VM disks or ISO images.</p><form onSubmit={submit}><label>Name<input required maxLength={100} value={draft.name} autoFocus disabled={busy} onChange={e => patch({ name: e.target.value })} /></label><label htmlFor="storage-host">Host</label><select id="storage-host" required value={draft.host_id} disabled={busy || Boolean(edit)} onChange={e => patch({ host_id: e.target.value })}><option value="">Select host</option>{activeHosts.map(h => <option key={h.id} value={h.id}>{h.hostname}</option>)}</select><label>Repository type<select value={draft.kind} disabled={busy || Boolean(edit)} onChange={e => patch({ kind: e.target.value as Kind, password: "" })}>{(Object.keys(labels) as Kind[]).map(k => <option key={k} value={k}>{labels[k]}</option>)}</select></label><SourceFields draft={draft} setDraft={patch} disabled={busy} /><p className="help">A repository remains assigned to this one Core host. Registration never formats storage, creates a ZFS pool, changes an export, or makes a filesystem shareable.</p><button className="primary" disabled={busy || unavailable || !activeHosts.length}>{busy ? "Submitting…" : edit ? "Update and check access" : "Register and check access"}</button></form></section>}
    <section className="panel" aria-label="Unused storage credentials"><div className="section-heading"><h2>Unused storage credentials</h2></div><div className="credential-cleanup"><label htmlFor="credential-host">Host</label><select id="credential-host" value={receiptHost} disabled={busy || unavailable} onChange={e => { setReceiptHost(e.target.value); setReceipts(null); }}><option value="">Select host</option>{activeHosts.map(h => <option key={h.id} value={h.id}>{h.hostname}</option>)}</select><button type="button" disabled={busy || unavailable || !receiptHost} onClick={() => void findReceipts()}>Find unused credentials</button></div>{receipts && (receipts.length ? <div className="table-scroll"><table><thead><tr><th>Core receipt</th><th>Observed status</th><th>Created</th><th>Action</th></tr></thead><tbody>{receipts.map(receipt => <tr key={receipt.receipt}><td className="mono">{receipt.receipt}</td><td>{status(receipt.status)}{receipt.error && <small>{receipt.error}</small>}</td><td>{receipt.created_at ? new Date(receipt.created_at).toLocaleString() : "Unknown"}</td><td><button type="button" className="danger" disabled={busy || !receipt.retire_eligible} onClick={() => { setRetire(receipt); retireDialog.current?.showModal(); }}>Retire unused credentials</button></td></tr>)}</tbody></table></div> : <p className="empty">Nex Core reports no unused credential receipts on this host.</p>)}<p className="panel-note">Only Core-calculated unused receipts appear. Nex never displays credentials; Core rejects retirement if a receipt is still attached to storage.</p></section>
    <section className="panel"><div className="section-heading"><h2>Pool storage repositories</h2><span className="muted">{storage.length} reported</span></div>{storage.length ? <div className="table-scroll"><table><thead><tr><th>Name / backend</th><th>Accessible host</th><th>Core access</th><th>Capacity / verification</th><th>Actions</th></tr></thead><tbody>{storage.map(s => <tr key={s.id}><td><strong>{s.name}</strong><small>{labels[(s.kind || "local") as Kind] || s.kind || "Local directory"} · {storageSource(s)}</small></td><td>{hosts.find(h => h.id === s.host_id)?.hostname || s.host_id}<small>Single-host assignment</small></td><td><span className={`badge ${storageAccess(s)}`}>{storageAccess(s)}</span><small>{s.access?.readable ? "Readable" : "Not readable"} · {s.access?.writable ? "Writable" : "Not writable"}{s.access?.error ? ` · ${s.access.error}` : ""}</small></td><td>{s.access?.available_bytes !== undefined ? `${(s.access.available_bytes / 1024 ** 3).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB available` : "Unknown"}<small>{storageChecked(s.access?.observed_at)}</small></td><td><div className="actions"><button type="button" title={storageLifecycleReady(s) ? undefined : "Wait for Nex Core to assign and confirm this repository."} disabled={busy || unavailable || !storageLifecycleReady(s)} onClick={() => void recheck(s)}>Recheck</button><button type="button" title={storageLifecycleReady(s) ? undefined : "Wait for Nex Core to assign and confirm this repository."} disabled={busy || unavailable || s.tombstone || !storageLifecycleReady(s)} onClick={() => beginEdit(s)}>Update{needsCredential((s.kind || "local") as Kind) ? " / rotate credentials" : ""}</button><button type="button" className="danger" title={storageLifecycleReady(s) ? undefined : "Wait for Nex Core to assign and confirm this repository."} disabled={busy || unavailable || s.tombstone || !storageLifecycleReady(s)} onClick={() => { setRemove(s); detach.current?.showModal(); }}>Detach</button></div></td></tr>)}</tbody></table></div> : <p className="empty">No storage repositories reported. Register an existing local directory, NFS export, ZFS dataset, SMB share or iSCSI filesystem.</p>}<p className="panel-note">Core checks identity and access again before VM disk, media and power operations. A successful check does not permit multi-host writers, live migration or snapshots.</p></section>
    <dialog ref={detach} className="confirm-dialog" aria-labelledby="detach-storage-title" onClose={() => setRemove(null)}>{remove && <form onSubmit={e => { e.preventDefault(); void detachStorage(); }}><h2 id="detach-storage-title">Detach {remove.name}?</h2><p>Confirm the repository name to remove Nex’s configuration. This does not delete data.</p><label>Repository name<input required autoFocus pattern={remove.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")} /></label><div className="actions"><button type="button" disabled={busy} onClick={() => detach.current?.close()}>Cancel</button><button className="danger" disabled={busy}>{busy ? "Detaching…" : "Detach storage"}</button></div></form>}</dialog>
    <dialog ref={retireDialog} className="confirm-dialog" aria-labelledby="retire-credential-title" onClose={() => setRetire(null)}>{retire && <form onSubmit={e => { e.preventDefault(); void retireReceipt(); }}><h2 id="retire-credential-title">Retire unused credentials?</h2><p>Core will remove this opaque receipt only if it is still unused. No repository data is deleted.</p><p className="mono">{retire.receipt}</p><div className="actions"><button type="button" disabled={busy} onClick={() => retireDialog.current?.close()}>Cancel</button><button className="danger" disabled={busy}>{busy ? "Retiring…" : "Retire unused credentials"}</button></div></form>}</dialog>
  </>;
}
