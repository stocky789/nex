// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import type { API } from "./types";
import type { Host, VM } from "./managed";

// releaseBlocker mirrors what Core refuses; Core still checks the VM, its snapshots and its disk chains.
export function releaseBlocker(vm: VM, host?: Host): string {
  return vm.protected ? "The Controller appliance is managed from its host's local page."
    : !host || host.revoked || host.connection !== "fresh" ? "Release needs a freshly connected host."
    : vm.freshness !== "fresh" ? "Current VM state is unknown; wait for a fresh host report."
    : vm.state !== "shut off" ? "Stop the VM before releasing its retained originals."
    : "";
}

// requestRelease sends the release. A failed attempt keeps its request_id so a retry replays the same
// request; success releases it.
export async function requestRelease(api: API, vmID: string, request: { current: string }, confirmation: string) {
  request.current ||= crypto.randomUUID();
  const result = await api<{ operation: { id: string } }>(`vms/${encodeURIComponent(vmID)}/release-retained`, "POST", { request_id: request.current, confirmation });
  request.current = "";
  return result;
}

// VMReleaseRetained deletes the original disks and vTPM state that restore replace kept beside the VM.
export function VMReleaseRetained({ vm, host, api, reload, busy, unavailable, onNotice }: {
  vm: VM; host?: Host; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string, operationID?: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const request = useRef("");
  const dialog = useRef<HTMLDialogElement>(null);
  const blocker = releaseBlocker(vm, host);
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (open && !element.open) element.showModal();
    else if (!open && element.open) element.close();
  }, [open]);
  async function submit() {
    setWorking(true); setError("");
    try {
      const result = await requestRelease(api, vm.id, request, name);
      setOpen(false);
      await reload();
      onNotice(`Release of retained originals queued for ${vm.name}. Follow its progress in Tasks.`, result.operation.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setWorking(false);
    }
  }
  return <>
    <button type="button" className="sm danger" disabled={busy || working || unavailable || Boolean(blocker)} title={blocker || undefined}
      onClick={() => { setError(""); setName(""); request.current = ""; setOpen(true); }}>Release retained original</button>
    <dialog ref={dialog} className="confirm-dialog" aria-labelledby="vm-release-title" onCancel={e => { if (working) e.preventDefault(); }}
      onClose={() => { setOpen(false); setError(""); setName(""); }}>
      {open && <form onSubmit={e => { e.preventDefault(); void submit(); }}>
        <h2 id="vm-release-title">Release retained original of {vm.name}?</h2>
        <p>Permanently deletes the original disks and vTPM state that restore replace kept for this VM, freeing their storage. The VM's current disks are not touched. Core refuses while snapshots exist, a restore is unfinished, or the files are still in use. Type the VM name to confirm.</p>
        {error && <p className="task-error" role="alert">{error}</p>}
        <label>VM name<input required autoFocus autoComplete="off" value={name} onChange={e => { setName(e.target.value); request.current = ""; }} disabled={working} /></label>
        <div className="actions">
          <button type="button" disabled={working} onClick={() => setOpen(false)}>Cancel</button>
          <button className="danger" disabled={working || name !== vm.name}>{working ? "Requesting…" : "Release retained original"}</button>
        </div>
      </form>}
    </dialog>
  </>;
}
