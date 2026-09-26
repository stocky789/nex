// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { API } from "./types";
import type { Host, VM } from "./managed";
import { Badge, date } from "./ui";
import { Icon } from "./icons";

export type PoolSnapshot = {
  id: string;
  vm_id: string;
  name: string;
  description?: string;
  created_at: string;
  spec_revision: number;
};

const rid = (prefix: string) => {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return prefix + Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("").slice(0, 20);
};

type Props = {
  vm: VM;
  host?: Host;
  api: API;
  reload: () => Promise<void>;
  busy: boolean;
  unavailable: boolean;
  onNotice: (text: string, operationID?: string) => void;
};

type Pending = { kind: "revert" | "delete"; snap: PoolSnapshot };

function snapshotBlocker(vm: VM, host: Host | undefined, unavailable: boolean, busy: boolean): string {
  if (vm.protected) return "Controller appliance: use its own panel.";
  if (unavailable) return "Controller inventory could not be loaded.";
  if (busy) return "Another task is already in progress.";
  if (!host || host.revoked || host.connection !== "fresh") return "Needs a freshly connected host.";
  if (vm.freshness !== "fresh") return "Current VM state is unknown.";
  return "";
}

export function VMSnapshots({ vm, host, api, reload, busy, unavailable, onNotice }: Props) {
  const [rows, setRows] = useState<PoolSnapshot[] | null>(null);
  const [error, setError] = useState("");
  const [confirmError, setConfirmError] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [working, setWorking] = useState("");
  const [editID, setEditID] = useState("");
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [pending, setPending] = useState<Pending | null>(null);
  const confirmDialog = useRef<HTMLDialogElement>(null);
  const opener = useRef<HTMLButtonElement | null>(null);
  const requests = useRef<Record<string, string>>({});
  const loadGen = useRef(0);
  const loadAbort = useRef<AbortController | null>(null);
  const blocker = snapshotBlocker(vm, host, unavailable, busy);
  const blocked = Boolean(blocker);
  const stopped = vm.state === "shut off";
  const running = vm.state === "running";
  const [mode, setMode] = useState<"stopped" | "crash" | "quiesced">("stopped");
  const canCreate = !vm.protected && (stopped || running);
  const createWhy = blocker || (!canCreate ? "Guest must be shut off or running." : "");
  const stopWhy = blocker || (!stopped ? "Stop the VM first." : "");

  const load = async (vmID = vm.id) => {
    loadAbort.current?.abort();
    const abort = new AbortController();
    loadAbort.current = abort;
    const gen = ++loadGen.current;
    setError("");
    try {
      const next = await api<PoolSnapshot[]>(`vms/${encodeURIComponent(vmID)}/snapshots`, "GET", undefined, undefined, undefined, abort.signal);
      if (gen !== loadGen.current || abort.signal.aborted) return;
      setRows(next);
    } catch (e) {
      if (gen !== loadGen.current || abort.signal.aborted) return;
      setRows(null);
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  useEffect(() => {
    setRows(null);
    setError("");
    setConfirmError("");
    setName("");
    setDescription("");
    setEditID("");
    setPending(null);
    requests.current = {};
    void load(vm.id);
    return () => { loadAbort.current?.abort(); };
  }, [vm.id]);

  const noticeKind = (key: string) => {
    if (key === "create") return "create requested";
    if (key.startsWith("rename-")) return "rename requested";
    if (key.startsWith("revert-")) return "revert requested";
    if (key.startsWith("delete-")) return "delete requested";
    return "request submitted";
  };

  const requestID = (key: string, prefix: string) => {
    requests.current[key] ??= rid(prefix);
    return requests.current[key];
  };

  const clearRequest = (key: string) => { delete requests.current[key]; };

  const run = async (key: string, prefix: string, path: string, method: string, body: Record<string, unknown>) => {
    setWorking(key);
    setError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(path, method, {
        ...body,
        request_id: requestID(key, prefix),
      });
      clearRequest(key);
      onNotice(`${vm.name}: snapshot ${noticeKind(key)}`, reply.operation?.id);
      await reload();
      await load();
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setWorking("");
    }
  };

  const onCreate = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim() || blocked || !canCreate) return;
    const createMode = stopped ? "stopped" : mode === "stopped" ? "crash" : mode;
    const ok = await run("create", "snap", `vms/${encodeURIComponent(vm.id)}/snapshots`, "POST", {
      name: name.trim(),
      description: description.trim(),
      mode: createMode === "stopped" ? undefined : createMode,
    });
    if (ok) {
      setName("");
      setDescription("");
    }
  };

  const closeConfirm = () => {
    if (working) return;
    confirmDialog.current?.close();
  };

  const openConfirm = (kind: Pending["kind"], snap: PoolSnapshot, button: HTMLButtonElement) => {
    opener.current = button;
    setConfirmError("");
    setPending({ kind, snap });
    if (!confirmDialog.current?.open) confirmDialog.current?.showModal();
  };

  const confirmPending = async () => {
    if (!pending || working) return;
    const { kind, snap } = pending;
    const key = `${kind}-${snap.id}`;
    setWorking(key);
    setConfirmError("");
    try {
      const path = kind === "revert"
        ? `vms/${encodeURIComponent(vm.id)}/snapshots/${encodeURIComponent(snap.id)}/revert`
        : `vms/${encodeURIComponent(vm.id)}/snapshots/${encodeURIComponent(snap.id)}/delete`;
      const reply = await api<{ operation?: { id?: string } }>(path, "POST", {
        request_id: requestID(key, kind === "revert" ? "rev" : "del"),
      });
      clearRequest(key);
      onNotice(`${vm.name}: snapshot ${noticeKind(key)}`, reply.operation?.id);
      confirmDialog.current?.close();
      await reload();
      await load();
    } catch (e) {
      setConfirmError(e instanceof Error ? e.message : String(e));
    } finally {
      setWorking("");
    }
  };

  return <section className="tile" aria-label="Snapshots">
    <h2>Snapshots</h2>
    {error && <p className="why" role="alert">{error}</p>}
    {vm.protected ? <p className="note">Controller appliance: use its own panel.</p> : null}
    <form className="vm-fields" onSubmit={onCreate}>
      <label>Name<input value={name} onChange={e => { setName(e.target.value); clearRequest("create"); }} maxLength={64} required disabled={blocked || !canCreate || Boolean(working)} title={createWhy || undefined} /></label>
      <label>Description<input value={description} onChange={e => { setDescription(e.target.value); clearRequest("create"); }} maxLength={512} disabled={blocked || !canCreate || Boolean(working)} title={createWhy || undefined} /></label>
      {running && !vm.protected ? <label>Mode<select value={mode === "stopped" ? "crash" : mode} onChange={e => { setMode(e.target.value as "crash" | "quiesced"); clearRequest("create"); }} disabled={blocked || Boolean(working)} title={blocker || undefined}>
        <option value="crash">Crash-consistent</option>
        <option value="quiesced">Quiesced</option>
      </select></label> : null}
      <div className="actions">
        <button type="submit" disabled={blocked || !canCreate || !name.trim() || Boolean(working)} title={createWhy || undefined}>
          <Icon kind="snapshot" />{working === "create" ? "Working…" : "Take snapshot"}
        </button>
      </div>
    </form>
    {rows === null && !error ? <p className="muted">Loading…</p> : null}
    {rows && rows.length === 0 ? <p className="muted">No retained snapshots.</p> : null}
    {rows && rows.length > 0 ? <table className="data">
      <thead><tr><th>Name</th><th>Created</th><th><span className="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {rows.map(snap => <tr key={snap.id}>
          <td>
            {editID === snap.id ? <form className="vm-fields" onSubmit={async e => {
              e.preventDefault();
              if (!editName.trim()) return;
              const key = `rename-${snap.id}`;
              const ok = await run(key, "meta", `vms/${encodeURIComponent(vm.id)}/snapshots/${encodeURIComponent(snap.id)}`, "PATCH", {
                name: editName.trim(),
                description: editDescription.trim(),
              });
              if (ok) setEditID("");
            }}>
              <label>Name<input value={editName} onChange={e => { setEditName(e.target.value); clearRequest(`rename-${snap.id}`); }} maxLength={64} required disabled={Boolean(working)} /></label>
              <label>Description<input value={editDescription} onChange={e => { setEditDescription(e.target.value); clearRequest(`rename-${snap.id}`); }} maxLength={512} disabled={Boolean(working)} /></label>
              <div className="actions">
                <button type="submit" disabled={Boolean(working) || blocked} title={blocker || undefined}>Save</button>
                <button type="button" className="sm" disabled={Boolean(working)} onClick={() => { clearRequest(`rename-${snap.id}`); setEditID(""); }}>Cancel</button>
              </div>
            </form> : <>
              <div>{snap.name}</div>
              {snap.description ? <small className="muted">{snap.description}</small> : null}
            </>}
          </td>
          <td><span className="muted">{date(snap.created_at)}</span></td>
          <td className="actions">
            <button type="button" className="sm" disabled={blocked || Boolean(working)} title={blocker || undefined} aria-label={`Rename ${snap.name}`} onClick={() => { setEditID(snap.id); setEditName(snap.name); setEditDescription(snap.description || ""); }}>Rename</button>
            <button type="button" className="sm" disabled={blocked || !stopped || Boolean(working)} title={stopWhy || undefined} aria-label={`Revert ${vm.name} to ${snap.name}`} onClick={e => openConfirm("revert", snap, e.currentTarget)}>Revert</button>
            <button type="button" className="sm danger" disabled={blocked || !stopped || Boolean(working)} title={stopWhy || undefined} aria-label={`Delete snapshot ${snap.name}`} onClick={e => openConfirm("delete", snap, e.currentTarget)}>Delete</button>
          </td>
        </tr>)}
      </tbody>
    </table> : null}
    {working ? <p className="muted" role="status"><Badge value="pending" /> Working…</p> : null}
    <dialog ref={confirmDialog} className="confirm-dialog" aria-labelledby="snap-confirm-title" aria-describedby={pending ? "snap-confirm-desc" : undefined}
      onCancel={e => { if (working) e.preventDefault(); }}
      onClose={() => {
        if (pending) {
          clearRequest(`${pending.kind}-${pending.snap.id}`);
        }
        setPending(null);
        setConfirmError("");
        opener.current?.focus();
      }}>
      {pending ? <form onSubmit={e => { e.preventDefault(); if (!working) void confirmPending(); }}>
        <h2 id="snap-confirm-title">{pending.kind === "revert" ? `Revert ${vm.name} to ${pending.snap.name}?` : `Delete snapshot ${pending.snap.name}?`}</h2>
        <p id="snap-confirm-desc">{pending.kind === "revert" ? "Current disks and firmware for this VM are discarded." : "Only a leaf snapshot can be deleted."}</p>
        {confirmError && <p className="why" role="alert">{confirmError}</p>}
        <div className="actions">
          <button type="button" disabled={Boolean(working)} onClick={closeConfirm}>Cancel</button>
          <button className={pending.kind === "delete" ? "danger" : "primary"} disabled={Boolean(working)}>
            {working ? "Requesting…" : pending.kind === "revert" ? "Revert" : "Delete"}
          </button>
        </div>
      </form> : null}
    </dialog>
  </section>;
}
