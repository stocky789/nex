// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import type { API } from "./types";
import { confirmMatches } from "./managed";
import { backupOpID, rid } from "./backup-types";

export const deletePolicyBody = (requestID: string, typed: string) => ({ request_id: requestID, confirmation: typed });

export function DeleteBackupPolicy({ api, poolID, policy, onClose, onDone }: {
  api: API; poolID: string; policy: { id: string; name: string }; onClose: () => void; onDone: (operationID?: string) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const requestID = useRef("");
  const [typed, setTyped] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // A changed confirmation is a different request; an unchanged retry replays.
  useEffect(() => { requestID.current = rid("bpdelete"); }, [typed]);
  useEffect(() => {
    // Unmounting removes the dialog without closing it, so return focus to the opener ourselves.
    const opener = document.activeElement as HTMLElement | null;
    dialog.current?.showModal();
    return () => opener?.focus();
  }, []);
  const submit = async () => {
    setBusy(true); setError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(
        `pools/${encodeURIComponent(poolID)}/backup-policies/${encodeURIComponent(policy.id)}/delete`, "POST", deletePolicyBody(requestID.current, typed));
      onDone(backupOpID(reply));
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };
  return <dialog ref={dialog} className="confirm-dialog" aria-labelledby="delete-policy-title" onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}>
    <div className="section-heading"><h2 id="delete-policy-title">Delete {policy.name}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p className="warning">Nex deletes this backup job's schedule, VM list and retention rules. Its recovery points and run history stay, and nothing is deleted from the repository. Retention no longer applies to those recovery points, so Nex will not remove them automatically. Restore them or delete them yourself. The job cannot be deleted while one of its backups is running.</p>
    {error && <p className="alert" role="alert">{error}</p>}
    <label>Type the backup job name to confirm<input required autoComplete="off" value={typed} disabled={busy} onChange={e => setTyped(e.target.value)} /><span className="mono">{policy.name}</span></label>
    <div className="actions">
      <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
      <button type="button" className="danger" disabled={busy || !confirmMatches(typed, policy.name)} onClick={() => void submit()}>{busy ? "Deleting…" : "Delete backup job"}</button>
    </div>
  </dialog>;
}
