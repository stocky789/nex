// SPDX-License-Identifier: AGPL-3.0-only
// S5 "Planned master change and member eject". Everything here is a projection of what the
// hosts reported: the Controller relays one command to the pool master and keeps no stage,
// no timer and no generation of its own, so this file never invents progress either.
import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { confirmMatches } from "./managed";

export type Transfer = {
  operation_id: string; action: string; role: string; target_host_id: string; stage: string;
  status: "pending" | "succeeded" | "failed" | "needs_attention"; error?: string;
  authority_generation?: number; frozen?: boolean; observed_at?: string;
  // Reported by the host that owns the transfer when its Core is new enough to say. Until
  // then the stage inference below is all there is, and Core decides again either way.
  cancellable?: boolean;
  // The source's own answer to "have I signed this pool's authority away?". Absent means an
  // older Core that cannot say, which is never treated as false.
  handed_off?: boolean;
};
export type Authority = { generation: number; digest?: string; observed_at?: string };
// The newest pool-transfer operation this Controller holds: how an asynchronous Core
// refusal becomes visible where the operator acted.
// `reason` is Core's sentence for an operation that did what was asked — a performed cancel
// carries it with `error` empty — so the message is read from `reason` first and `error` only
// as the fallback a refusal uses.
export type LastTransfer = { operation_id: string; action: string; status: string; error?: string; reason?: string; conflict?: boolean; retryable?: boolean };
// Structural shapes only, so pool-members.tsx can pass its own richer types without this
// file importing back from it.
export type AuthorityMember = {
  host_id: string; name?: string; role: "master" | "member"; status?: string; connection?: string;
  peer_origin?: string; replica?: { generation: number; acknowledged_generation: number } | null;
  transfer?: Transfer | null;
  // This member's own advertised pool-authority-transfer-v1, so a master change whose target
  // could not perform one is not offered at all.
  transfer_supported?: boolean;
};
export type AuthorityReport = {
  pool_id: string; pool_name: string; master_host_id: string; master_connection: string;
  generation: number; authoritative: boolean; transfer_supported?: boolean;
  authority?: Authority | null; transfer?: Transfer | null; last_transfer?: LastTransfer | null;
  master_conflict?: string[]; members?: AuthorityMember[];
};

// Core's own stage names, said as what is happening. An unknown stage is printed as Core
// sent it rather than guessed at.
const transferStages: Record<string, string> = {
  preflight: "checking the pool",
  freeze: "pausing pool authority writes",
  replicate: "copying the pool records to the new master",
  activate: "handing the pool authority to the new master",
  repoint: "pointing every member at the new master",
  remove: "removing the host from the membership",
  leave: "telling the host to leave the pool",
  done: "finished",
  aborted: "cancelled",
};
const transferActions: Record<string, string> = {
  promote: "master change",
  eject: "eject",
  "master-change": "master change",
  "transfer-cancel": "cancel of a master change",
};
// Whether a cancel can still succeed is Core's own answer, reported as `cancellable` by the
// host that owns the transfer and computed from the same predicate the cancel route enforces.
// These stage lists are only a fallback for a host too old to report it, and they mirror Core's
// rule: a master change is cancellable until the pool-CA-signed hand-off (so through `activate`,
// whose hand-off window the target's own never-activate record decides), and an eject until the
// membership without that member is written (so through `remove`, which names the step still to
// perform). `preflight` is a promotion stage; no master change or eject ever reaches it.
export function transferActionText(action?: string) {
  return transferActions[action || ""] || action || "pool change";
}
export function transferStageText(transfer?: Transfer | null) {
  if (!transfer) return "unknown";
  return transferStages[transfer.stage] || transfer.stage || "unknown";
}
export function transferInProgress(transfer?: Transfer | null) {
  return Boolean(transfer) && transfer!.status === "pending";
}
// Takes the smallest shape that can answer, so every pool view can reuse it.
export function hostName(report: { members?: { host_id: string; name?: string }[] }, hostID: string) {
  return (report.members || []).find(member => member.host_id === hostID)?.name || hostID;
}
// Cancel is offered only while the contract says it can still succeed: the master's own
// record is before the hand-off and no participant reports a later stage. After that the
// change can only go forward, and the button is replaced by that sentence.
export function transferCancellable(report: AuthorityReport) {
  const transfer = report.transfer;
  if (!report.authoritative || !transferInProgress(transfer) || transfer!.role !== "source") return false;
  return transfer!.cancellable === true;
}

// The one way out of a hand-off whose target never answered: Nex Core powers that target off,
// verifies it, and resumes the pool on the host that still has it. The gate is structural,
// from Core's own reported fields — the source says it has handed the authority over
// (`handed_off`), the transfer is unfinished, and the target's own row is not fresh, so
// nothing on the reachable side can still complete it. Core's sentence is displayed, never
// matched on. A Core too old to report `handed_off` offers nothing: an unsaid flag is unknown,
// not false. `cancellable` is deliberately not part of this: Core keeps a handed-off master
// change cancellable, because the plain cancel still succeeds if the target answers that it
// never activated. So both are offered, the plain cancel first, and this is what is left when
// the target does not answer at all.
export function transferResumePrompt(report: AuthorityReport) {
  const transfer = report.transfer;
  if (!report.authoritative || !transfer || transfer.role !== "source" || !transfer.target_host_id) return "";
  if (typeof transfer.handed_off !== "boolean" || !transfer.handed_off) return "";
  if (transfer.status !== "pending" && transfer.status !== "needs_attention") return "";
  const target = (report.members || []).find(member => member.host_id === transfer.target_host_id);
  if (target?.connection === "fresh") return "";
  // Core's own words when it has said any; otherwise what the reported fields mean, because
  // the offer does not depend on a sentence having arrived.
  const last = report.last_transfer;
  const prompt = (last && last.action === "transfer-cancel" && last.status !== "succeeded" && lastTransferMessage(last)) || transfer.error;
  return prompt || `This pool's authority was handed to ${hostName(report, transfer.target_host_id)}, and that host is not reporting. The authority stays with it until it is powered off.`;
}

// Core's message for an operation: a refusal puts it in `error`, and an operation that did
// what was asked puts it in `reason` with `error` empty, so a performed cancel is not read as
// a silent one.
function lastTransferMessage(last: LastTransfer) {
  return last.reason || last.error || "";
}

// The result of the command the operator submitted, which is where Core's asynchronous
// refusals arrive. The frozen sentence is the one an operator retries unchanged.
export function lastTransferText(last?: LastTransfer | null) {
  if (!last) return "";
  const action = transferActionText(last.action);
  const reported = lastTransferMessage(last);
  const message = reported || "Nex Core reported no message.";
  // A cancel that succeeded did what was asked, and Core's reason says how — including the
  // fence-backed resume, where the reason is that the target was powered off. It is a result,
  // not a failure, and it is the one succeeded operation worth a sentence.
  if (last.status === "succeeded") {
    if (last.action !== "transfer-cancel") return "";
    return reported ? `The master change was cancelled: ${message}` : "The master change was cancelled.";
  }
  if (last.retryable) return `${message} The pool is changing master; retry when it completes.`;
  // A refused cancel is Core saying the change has passed the point it could be undone. It is
  // neither retryable nor a stale-read conflict, so it is said as what it is, with Core's own
  // sentence first. The action is what identifies it; the sentence is never matched on.
  if (last.action === "transfer-cancel" && last.conflict) return `${message} Nothing was cancelled, and retrying will not cancel it either; the change completes on the hosts.`;
  if (last.conflict) return `${message} The pool changed since you loaded it — reload and look again before retrying.`;
  if (last.status === "failed") return `The ${action} failed: ${message}`;
  if (last.status === "needs_attention") return `Needs attention: ${message} The hosts carry on without this Controller; this shows their own reports.`;
  return `The ${action} was submitted. The pool master runs it; this shows what the hosts report.`;
}

// The only absolute generation in the pool is the master's own report of it. Without it
// nothing here says how current a member's copy is.
export function authorityText(report: AuthorityReport) {
  if (!report.authoritative || !report.authority) return "The pool master is not reporting an authority generation, so how far behind a copy is cannot be said here.";
  return `The pool authority is at generation ${report.authority.generation}.`;
}
// How far behind one member's copy is, in plain numbers. Both reports are used and the
// lower one wins, because either side can be the older report.
export function behindText(member: AuthorityMember, report: AuthorityReport) {
  if (!report.authoritative || !report.authority || member.role === "master" || !member.replica) return "";
  const held = Math.min(member.replica.generation, member.replica.acknowledged_generation);
  const behind = report.authority.generation - held;
  if (behind <= 0) return "";
  return `${behind} generation${behind === 1 ? "" : "s"} behind`;
}
// Two hosts claiming one pool is never resolved here: the Controller keeps the recorded
// master and names the hosts that disagree.
export function masterConflictText(report: AuthorityReport) {
  const claimants = report.master_conflict || [];
  if (!claimants.length) return "";
  const names = claimants.map(id => hostName(report, id)).join(", ");
  return `${names} report${claimants.length === 1 ? "s" : ""} being the pool master of this pool, at no newer generation than ${hostName(report, report.master_host_id)}, which this Controller still treats as the master. Nothing is changed while they disagree: check both hosts before you act.`;
}

function upgradeHost(report: AuthorityReport) {
  return hostName(report, report.master_host_id);
}
function notCurrent(report: AuthorityReport) {
  return `The pool master is ${report.master_connection}, so this membership is not current; reload when it reports again.`;
}
// Every reason below mirrors a refusal the route would answer with 409. Core decides again
// and its answer is shown verbatim; this only avoids offering an action that cannot work.
export function masterChangeBlock(report: AuthorityReport, member: AuthorityMember) {
  if (member.role === "master") return "This host is already the pool master.";
  if (!report.authoritative) return notCurrent(report);
  if (report.transfer_supported === false) return `Changing the pool master needs pool-authority-transfer-v1 on the pool master, the selected host and every ready member; upgrade ${upgradeHost(report)}.`;
  // The selected host's own advertised capability. An absent field is an older projection,
  // not a "no", so only an explicit false withdraws the action.
  if (member.transfer_supported === false) return `Changing the pool master needs pool-authority-transfer-v1 on ${member.name || member.host_id}; upgrade it before making it the pool master.`;
  if (transferInProgress(report.transfer)) return "Another pool authority transfer is already in progress on this pool.";
  if (member.status !== "ready" || !member.peer_origin) return "Only a ready member with a peer endpoint can become the pool master.";
  if (member.connection !== "fresh") return "The selected host must be fresh and active to become the pool master.";
  return "";
}
// An unreachable member can be ejected: Core ejects it and the host learns of it when it
// returns, so freshness is deliberately not a blocker here.
export function ejectBlock(report: AuthorityReport, member: AuthorityMember) {
  if (member.role === "master") return "The pool master cannot be ejected; change the pool master first.";
  if (!report.authoritative) return notCurrent(report);
  if (report.transfer_supported === false) return `Ejecting a member needs pool-authority-transfer-v1 on the pool master and every ready member; upgrade ${upgradeHost(report)}.`;
  if (transferInProgress(report.transfer)) return "Another pool authority transfer is already in progress on this pool.";
  return "";
}

// One compact disclosure per row instead of two more columns: the members table is already
// the widest in the app, and a disclosure expands in flow rather than being clipped by the
// table's own scroll container.
// `extra` is where later slices add their own row actions (S6's promotion and fence device),
// so one row keeps one disclosure instead of growing a second.
export function MemberActions({ report, member, onMasterChange, onEject, extra, blockedReason }: {
  report: AuthorityReport; member: AuthorityMember; onMasterChange: () => void; onEject: () => void; extra?: ReactNode; blockedReason?: string;
}) {
  const promote = masterChangeBlock(report, member);
  const eject = ejectBlock(report, member);
  const reasonID = useId();
  const describedBy = (reason: string, suffix: string) => [blockedReason ? "pool-actions-blocked" : "", reason ? `${reasonID}-${suffix}` : ""].filter(Boolean).join(" ") || undefined;
  return <details className="pool-row-actions">
    <summary>Actions</summary>
    <div>
      <button type="button" aria-describedby={describedBy(promote, "promote")} disabled={Boolean(promote)} onClick={onMasterChange}>Make pool master</button>
      {promote && <small id={`${reasonID}-promote`}>{promote}</small>}
      <button type="button" className="danger" aria-describedby={describedBy(eject, "eject")} disabled={Boolean(eject)} onClick={onEject}>Eject from pool</button>
      {eject && <small id={`${reasonID}-eject`}>{eject}</small>}
      {extra}
    </div>
  </details>;
}

// The live picture, assembled from the master's own report plus each participant's. A host
// that is not reporting is unknown, never finished and never a spinner.
export function TransferStrip({ report, onCancel, cancelBusy, cancelError, onResume }: {
  report: AuthorityReport; onCancel: () => void; cancelBusy: boolean; cancelError: string;
  onResume?: () => void;
}) {
  const transfer = report.transfer;
  const last = lastTransferText(report.last_transfer);
  const conflict = masterConflictText(report);
  const resume = transferResumePrompt(report);
  if (!transfer && !last && !conflict) return null;
  const participants = (report.members || []).filter(member => transfer && (member.host_id === transfer.target_host_id || member.transfer));
  return <section className="panel pool-transfer-strip" aria-label="Pool master change">
    <div className="section-heading"><h3>Pool master change</h3></div>
    {conflict && <p className="alert" role="alert">{conflict}</p>}
    {!report.authoritative
      ? <p className="warning" role="status">The pool master is {report.master_connection}, so the state of any master change or eject is <strong>unknown</strong>. Nothing from its last report is repeated here as if it still applied.</p>
      : transfer
        ? <>
          <p>
            <strong>{transferActionText(transfer.action)}</strong> · target {hostName(report, transfer.target_host_id)} ·
            stage <strong>{transferStageText(transfer)}</strong> · status <strong>{transfer.status.replace("_", " ")}</strong>
            {transfer.observed_at ? <small>Reported by the pool master at {transfer.observed_at}</small> : null}
          </p>
          {transfer.frozen && <p className="warning">Pool settings, placements, joins and maintenance changes are refused while this runs; nothing is queued. Virtual machines that are running keep running and can still be stopped.</p>}
          {transfer.error && <p className="task-error">{transfer.error}</p>}
          {participants.length > 0 && <ul className="pool-transfer-participants">{participants.map(member => <li key={member.host_id}>
            {member.name || member.host_id}: {member.transfer
              ? <>{member.transfer.role} · {transferStageText(member.transfer)} · {member.transfer.status.replace("_", " ")}{member.transfer.error ? <small className="task-error">{member.transfer.error}</small> : null}</>
              : <>unknown — this host is not reporting its part of the change.</>}
          </li>)}</ul>}
          {transferInProgress(transfer) && (transferCancellable(report)
            ? <>
              <p className="help">The authority has not been handed over yet, so this change can still be cancelled. Nex Core decides: if the new master has taken the authority in the meantime it refuses the cancel and the change goes forward.</p>
              <div className="actions"><button type="button" disabled={cancelBusy} onClick={onCancel}>{cancelBusy ? "Submitting…" : "Cancel this master change"}</button></div>
            </>
            : <p className="help">Nex Core reports that this {transferActionText(transfer.action)} can no longer be cancelled. It has passed the step that cannot be undone and can only go forward.</p>)}
          {resume && <div className="pool-transfer-resume">
            <p className="warning" role="status">{resume}</p>
            {onResume && <div className="actions"><button type="button" className="danger" disabled={cancelBusy} onClick={onResume}>Resume on the current master…</button></div>}
          </div>}
          {cancelError && <p className="alert" role="alert">{cancelError}</p>}
        </>
        : null}
    {last && <p className={report.last_transfer?.status === "failed" ? "task-error" : undefined} role={report.last_transfer?.conflict ? "alert" : "status"}>{last}</p>}
  </section>;
}

export function MasterChangeDialog({ report, member, busy, submitError, submitReady, onConfirm, onClose }: {
  report: AuthorityReport; member: AuthorityMember; busy: boolean; submitError: string; submitReady: boolean;
  onConfirm: (confirmation: string) => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [typed, setTyped] = useState("");
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const name = member.name || member.host_id;
  const current = hostName(report, report.master_host_id);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!confirmMatches(typed, report.pool_name)) return;
    onConfirm(typed);
  }
  return <dialog ref={dialog} className="confirm-dialog pool-transfer-dialog" aria-labelledby="master-change-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="master-change-title" ref={heading} tabIndex={-1}>Make {name} the pool master</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p>{current} is the pool master of {report.pool_name} now. {name} takes the pool authority.</p>
    <p className="warning">While the change runs, pool settings, placements, joins and maintenance changes are <strong>refused</strong>; nothing is queued behind it. Running virtual machines keep running and can still be stopped.</p>
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    <form onSubmit={submit}>
      <label htmlFor="master-change-confirm">Type the pool name {report.pool_name} to confirm</label>
      <input id="master-change-confirm" autoComplete="off" required value={typed} onChange={event => setTyped(event.target.value)} />
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="primary" disabled={busy || !submitReady || !confirmMatches(typed, report.pool_name)}>{busy ? "Submitting…" : "Make pool master"}</button>
      </div>
    </form>
  </dialog>;
}

// The fence-backed resume. Nex Core powers the hand-off target off and verifies it before it
// resumes the pool on this master; the two confirmations are the pool's name and an
// acknowledgement that names the host being powered off.
export function ResumeDialog({ report, prompt, busy, submitError, submitReady, onConfirm, onClose }: {
  report: AuthorityReport; prompt: string; busy: boolean; submitError: string; submitReady: boolean;
  onConfirm: (targetHostID: string) => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [typed, setTyped] = useState("");
  const [acknowledged, setAcknowledged] = useState(false);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const targetID = report.transfer?.target_host_id || "";
  const target = hostName(report, targetID);
  const master = hostName(report, report.master_host_id);
  const ready = submitReady && Boolean(targetID) && confirmMatches(typed, report.pool_name) && acknowledged;
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!ready) return;
    onConfirm(targetID);
  }
  return <dialog ref={dialog} className="confirm-dialog pool-transfer-dialog" aria-labelledby="resume-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="resume-title" ref={heading} tabIndex={-1}>Resume {report.pool_name} on {master}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p role="status">{prompt}</p>
    <p className="warning"><strong>Nex Core powers {target} off</strong> through its recorded fence device and verifies that it is off before resuming this pool's authority on {master}.</p>
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    <form onSubmit={submit}>
      <label htmlFor="resume-confirm">Type the pool name {report.pool_name} to confirm</label>
      <input id="resume-confirm" autoComplete="off" required value={typed} onChange={event => setTyped(event.target.value)} />
      <label className="checkbox" htmlFor="resume-fence-confirm">
        <input id="resume-fence-confirm" type="checkbox" checked={acknowledged} onChange={event => setAcknowledged(event.target.checked)} />
        I understand this powers off: {target}
      </label>
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="primary danger" disabled={busy || !ready}>{busy ? "Submitting…" : `Power ${target} off and resume`}</button>
      </div>
    </form>
  </dialog>;
}

export function EjectDialog({ report, member, busy, submitError, submitReady, onConfirm, onClose }: {
  report: AuthorityReport; member: AuthorityMember; busy: boolean; submitError: string; submitReady: boolean;
  onConfirm: (confirmation: string) => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [typed, setTyped] = useState("");
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const name = member.name || member.host_id;
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!confirmMatches(typed, name)) return;
    onConfirm(typed);
  }
  return <dialog ref={dialog} className="confirm-dialog pool-transfer-dialog" aria-labelledby="eject-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="eject-title" ref={heading} tabIndex={-1}>Eject {name} from {report.pool_name}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p className="warning">{name} leaves this pool. Pool-shared networks, storage and ISOs are removed from it.</p>
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    <form onSubmit={submit}>
      <label htmlFor="eject-confirm">Type the host name {name} to confirm</label>
      <input id="eject-confirm" autoComplete="off" required value={typed} onChange={event => setTyped(event.target.value)} />
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="primary danger" disabled={busy || !submitReady || !confirmMatches(typed, name)}>{busy ? "Submitting…" : "Eject from pool"}</button>
      </div>
    </form>
  </dialog>;
}
