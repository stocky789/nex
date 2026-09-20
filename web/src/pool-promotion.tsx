// SPDX-License-Identifier: AGPL-3.0-only
// S6 "Fence-verified manual promotion and safe rejoin". Everything here is a host's own
// report. Nex Core on the candidate proves the promotion and performs it — it powers the old
// pool master off through its recorded fence device and observes the power-off itself — so
// this file renders the verdict, collects the two confirmations the route requires, and shows
// what the candidate reports. It decides nothing, retries nothing and times nothing.
//
// Owner decision OD-5: there is no path here that starts virtual machines elsewhere, sets a
// promotion aside, or treats a silent host as gone. When a restriction cannot clear, the UI
// says the host must come back or be restored, and offers no action at all.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { confirmMatches } from "./managed";
import { hostName } from "./pool-authority";

export type PromotionCheck = { name: string; status: "passed" | "blocked" | "unknown"; reason?: string; hosts?: string[]; vms?: string[] };
export type PromotionHost = {
  host_id: string; reachable: boolean; role?: string; error?: string;
  authority_generation?: number; replica_generation?: number; membership_generation?: number;
  // Core's own per-host answers: whether this host stops the promotion as things stand, and
  // whether the candidate could shut it out. `fence_unusable_reason` is Core's sentence and is
  // shown verbatim; nothing here is inferred from it.
  blocks?: boolean; fence_recorded?: boolean; fence_usable?: boolean; fence_unusable_reason?: string;
};
export type PromotionVM = { vm_id: string; host_id: string; state: "confirmed" | "fenced" | "blocked"; reason?: string };
export type FenceStatus = { host_id: string; status: string; target_id?: string; exclusion_verified?: boolean; observed_at?: string; error?: string };
export type PromotionVerdict = {
  pool_id: string; candidate_host_id: string; old_master_host_id?: string; operation_id?: string;
  authority_generation: number; can_promote: boolean; fence_required: boolean;
  checks?: PromotionCheck[]; hosts?: PromotionHost[]; vms?: PromotionVM[];
  fence?: FenceStatus | null; blocking?: string[]; observed_at?: string;
  // Core's own structural answers, so nothing here reads its prose. `ready_to_fence` is the
  // candidate saying that confirming proceeds to the fence rather than to a refusal;
  // `fence_recorded`/`fence_usable` say whether the profile exists and can actually run, and
  // `fence_unusable_reason` is the sentence Core wrote for why, shown verbatim.
  ready_to_fence?: boolean; fence_recorded?: boolean; fence_usable?: boolean;
  fence_unusable_reason?: string;
  // Every host this promotion powers off, as Core computed it. It is what the operator is
  // shown and confirms, and the promotion command must carry it back exactly: Core computes
  // it again when the promotion runs and refuses a list that does not match.
  fence_host_ids?: string[];
};
// The candidate's own reported promotion, with the host that reported it. The Controller
// records no candidate and no stage of its own.
export type Promotion = {
  host_id: string; operation_id?: string; stage?: string;
  status?: "pending" | "succeeded" | "failed" | "needs_attention"; error?: string;
  verdict?: PromotionVerdict | null; observed_at?: string;
  // Always false: a promotion has fenced the old master, so it can only go forward.
  cancellable?: boolean;
};
export type LastPromotion = { operation_id: string; action: string; status: string; error?: string; conflict?: boolean; retryable?: boolean };
// `kind` is Core's own classification of the restriction, and is what chooses the "how it
// clears" text below. Nothing here reads the reason sentence to decide anything.
export type Restricted = { reason: string; kind?: "unconfirmed" | "superseded"; since?: string; conflicts?: string[]; observed_at?: string };
// `credential_version` is the master's own counter for the private material behind the
// profile; the material never reaches this Controller. 0 means none was ever recorded, so a
// fence device that exists on paper cannot actually run.
export type MemberFence = { kind: string; endpoint?: string; configured: boolean; credential_version?: number };
// Structural shapes only, so pool-members.tsx keeps one richer type of its own.
export type PromotionMember = {
  host_id: string; name?: string; role: "master" | "member"; connection?: string;
  transfer_supported?: boolean; fence?: MemberFence | null; restricted?: Restricted | null;
  replica?: { generation: number; verified?: boolean; state?: string } | null;
};
export type PromotionReport = {
  pool_id: string; pool_name: string; master_host_id: string; master_connection: string;
  authoritative: boolean; transfer_supported?: boolean;
  promotion?: Promotion | null; last_promotion?: LastPromotion | null; members?: PromotionMember[];
};

// The build limit, said once and reused wherever a fence device is entered or missing. Nex
// Core refuses to record anything else, so an operator is never left believing a physical
// pool is protected.
export const fenceBuildLimit = "Only lab virsh fence devices can be verified in this release; a physical pool cannot be promoted this way yet. Nex Core refuses to record any other kind of fence device.";

const checkTitles: Record<string, string> = {
  "replica-integrity": "This host's copy of the pool records is whole and signed",
  "replica-supremacy": "No reachable host holds a newer copy",
  "accepted-ownership": "Every pool virtual machine's owner agrees with the copy",
  "old-master-exclusion": "The old pool master is shut out",
};
// Text carries the verdict; the badge colour only repeats it.
const checkBadges: Record<string, string> = { passed: "ok", blocked: "failed", unknown: "unknown" };
const vmStates: Record<string, string> = {
  confirmed: "confirmed by its owner",
  fenced: "its owner is fenced",
  blocked: "blocked",
};
// Core's own stage names for a promotion, said as what the candidate is doing.
const promotionStages: Record<string, string> = {
  preflight: "proving it can take the pool authority",
  fence: "powering the old pool master off and verifying it",
  activate: "taking the pool authority",
  repoint: "pointing every member at the new master",
  done: "finished",
  aborted: "cancelled",
};

export function promotionStageText(promotion?: Promotion | null) {
  if (!promotion) return "unknown";
  return promotionStages[promotion.stage || ""] || promotion.stage || "unknown";
}
export function promotionInProgress(report: PromotionReport) {
  return report.promotion?.status === "pending";
}
export function checkTitle(name: string) {
  return checkTitles[name] || name;
}
export function checkOf(verdict: PromotionVerdict | null | undefined, name: string) {
  return (verdict?.checks || []).find(check => check.name === name);
}
// The old master answered its own pool. Read from the host rows rather than from Core's
// sentence: this is the one case where a planned master change is the right operation.
export function oldMasterReachable(verdict?: PromotionVerdict | null) {
  if (!verdict) return false;
  const exclusion = checkOf(verdict, "old-master-exclusion");
  if (exclusion?.status !== "blocked") return false;
  return (verdict.hosts || []).some(host => host.host_id === verdict.old_master_host_id && host.reachable);
}
// No fence device is recorded for the old master — Core's own flag, not its sentence.
export function fenceProfileMissing(verdict?: PromotionVerdict | null) {
  return verdict?.fence_recorded === false;
}
// A profile is recorded but this host cannot run it: no credential was delivered to it, or the
// kind cannot be verified. Core wrote the reason; it is returned unchanged and shown verbatim.
export function fenceUnusableReason(verdict?: PromotionVerdict | null) {
  if (!verdict || verdict.fence_recorded !== true || verdict.fence_usable !== false) return "";
  return verdict.fence_unusable_reason || "Nex Core reported no reason.";
}
// Every host this promotion powers off, exactly as Core listed them. The order is Core's own
// and is preserved: the command must carry the list back unchanged.
export function fenceHostIDs(verdict?: PromotionVerdict | null) {
  return verdict?.fence_host_ids || [];
}
// The hosts that stop this promotion and cannot be shut out — an unreachable member with no
// fence device, or one this candidate cannot fence. There is no promotion while one exists,
// and no action for it either: the host comes back or it is given a fence device.
export function unfenceableBlockers(verdict?: PromotionVerdict | null) {
  const fenced = fenceHostIDs(verdict);
  return (verdict?.hosts || []).filter(host => host.blocks && !fenced.includes(host.host_id));
}
// Core's own sentence for why a host cannot be fenced, shown verbatim. Absent it, the two
// structural flags are all that is claimed.
export function fenceBlockerReason(host: PromotionHost) {
  if (host.fence_unusable_reason) return host.fence_unusable_reason;
  if (host.fence_recorded === false) return "No fence device is recorded for this host.";
  return "Nex Core reported no reason.";
}
// What the table says about a host's fence, from the structural fields only.
export function fenceCellText(verdict: PromotionVerdict, host: PromotionHost) {
  if (fenceHostIDs(verdict).includes(host.host_id)) return "powered off by this promotion";
  if (host.blocks) return `cannot be fenced — ${fenceBlockerReason(host)}`;
  return "not powered off";
}
// When the operator may be offered the destructive step. The candidate answers this itself:
// `ready_to_fence` means confirming proceeds to the fence rather than to a refusal, and
// `can_promote` means the whole proof already holds. Neither is inferred from the checks.
// The per-check walk below is only for a Core old enough to report neither field; it never
// reads an `unknown` as "fine" either.
export function promotionRunnable(verdict?: PromotionVerdict | null) {
  if (!verdict) return false;
  if (verdict.can_promote || verdict.ready_to_fence) return true;
  if (typeof verdict.can_promote === "boolean" || typeof verdict.ready_to_fence === "boolean") return false;
  return false;
}

// Every reason below mirrors a refusal the route would answer with 409, so an action that
// cannot work is not offered. Core decides again and its answer is shown verbatim.
export function promotionBlock(report: PromotionReport, member: PromotionMember) {
  if (member.role === "master") return "This host is already the pool master.";
  if (report.authoritative) return `The pool master ${hostName(report, report.master_host_id)} is reporting. Use “Make pool master” for a planned master change; promoting is for a pool master that is gone.`;
  if (member.connection !== "fresh") return "The promotion candidate must be fresh and active to be promoted.";
  if (member.transfer_supported === false) return `A fence-verified promotion needs pool-authority-transfer-v1 on the promotion candidate; upgrade ${member.name || member.host_id}.`;
  if (!member.replica || !member.replica.verified) return "This host does not report a copy of the pool records it has verified, so it cannot be promoted.";
  if (promotionInProgress(report)) return "A promotion is already running on this pool.";
  return "";
}
// A fence device is a field of the membership the pool master owns, so recording one needs a
// fresh master. That is worth saying plainly: it has to be recorded before the master is lost.
export function fenceProfileBlock(report: PromotionReport, member: PromotionMember) {
  if (!report.authoritative) return `The pool master is ${report.master_connection}, so a fence device cannot be recorded now. The pool master keeps this record, so it has to be recorded while the master is reporting.`;
  if (report.transfer_supported === false) return `Recording a fence device needs pool-authority-transfer-v1 on the pool master; upgrade ${hostName(report, report.master_host_id)}.`;
  return "";
}

// What the master's membership records about how this host would be shut out. Master-derived,
// so it is unknown while the master is not fresh rather than repeated from its last report.
export function fenceText(member: PromotionMember, authoritative: boolean) {
  if (!authoritative) return "Fence device: unknown — the pool master keeps this record and is not reporting.";
  if (!member.fence) return "Fence device: not configured. A pool master with no fence device cannot be promoted over.";
  const credential = member.fence.credential_version ? "credential recorded" : "no credential recorded";
  return `Fence device: ${member.fence.kind}${member.fence.endpoint ? ` · ${member.fence.endpoint}` : ""} · ${member.fence.configured ? "configured" : "not configured"} · ${credential}`;
}

// A restarted master cannot know whether the pool promoted somebody while it was down, so it
// refuses to act until the other members tell it. The two kinds clear differently; Core
// classifies which it is and that classification is what chooses the text.
export function restrictedUnconfirmed(restricted?: Restricted | null) {
  return restricted?.kind === "unconfirmed";
}
export function restrictedClearsText(report: PromotionReport, member: PromotionMember) {
  const restricted = member.restricted;
  if (!restricted) return "";
  // A Core old enough to report no kind is not guessed at: its reason is shown above, and no
  // claim about how the restriction clears is made from it.
  if (!restricted.kind) return "";
  if (restricted.kind === "superseded") {
    return "It clears by itself once this host has rejoined the current pool master, taken its copy of the pool records and reconciled with it. A virtual machine named above stays defined here and fenced off until the two agree where it lives; it is never started twice and it is never deleted here.";
  }
  const silent = (report.members || [])
    .filter(other => other.host_id !== member.host_id && other.connection !== "fresh")
    .map(other => other.name || other.host_id);
  if (!silent.length) return "It clears by itself once every other ready member of this pool answers and names this host as its master.";
  // Owner decision OD-5: this is where the pool stays restricted. No action is offered.
  return `It clears only when every other ready member answers and names this host as its master. ${silent.join(", ")} ${silent.length === 1 ? "is" : "are"} not reporting, so ${silent.length === 1 ? "it cannot" : "they cannot"} answer. Bring that host back, or restore its Nex Core state from a backup. Nothing here can clear the restriction instead, and waiting does not clear it.`;
}

// One text badge, Core's own reason, the conflicts it names, and plainly what the host will
// and will not do. The badge colour only repeats what the words already say.
export function RestrictedNote({ report, member }: { report: PromotionReport; member: PromotionMember }) {
  const restricted = member.restricted;
  if (!restricted) return null;
  const conflicts = restricted.conflicts || [];
  return <div className="pool-restricted" role="status">
    <span className="badge failed">Restricted</span>
    <small className="task-error">{restricted.reason}</small>
    {restricted.since && <small>Since {restricted.since}</small>}
    {conflicts.length > 0 && <small>Virtual machines this host still defines that the pool records elsewhere: {conflicts.join(", ")}</small>}
    <small>While it is restricted this host starts no virtual machine and makes no pool change. Stopping a virtual machine, its console, local recovery and its own reporting all still work.</small>
    {restrictedClearsText(report, member) && <small>{restrictedClearsText(report, member)}</small>}
  </div>;
}

// ------------------------------------------------- the same restriction, on the host's page

// A host that is restricted is not doing what an operator expects, and its own page is where
// that is looked for. The membership is pool-scoped, so this reads the one pool report the
// host belongs to; it is the same projection the Hosts view renders and nothing is kept here.
export function HostRestrictedBanner({ poolID, hostID, api }: {
  poolID: string; hostID: string; api: <T>(path: string) => Promise<T>;
}) {
  const [report, setReport] = useState<PromotionReport | null>(null);
  const load = useRef(() => {
    api<PromotionReport>(`pools/${encodeURIComponent(poolID)}/members`)
      .then(answer => setReport(answer), () => {});
  });
  load.current = () => {
    api<PromotionReport>(`pools/${encodeURIComponent(poolID)}/members`)
      .then(answer => setReport(answer), () => {});
  };
  useEffect(() => {
    setReport(null);
    load.current();
  }, [poolID, hostID]);
  const member = (report?.members || []).find(entry => entry.host_id === hostID);
  const restricted = Boolean(member?.restricted);
  useEffect(() => {
    if (!restricted) return;
    const onFocus = () => load.current();
    window.addEventListener("focus", onFocus);
    const timer = window.setInterval(() => load.current(), 10_000);
    return () => { window.removeEventListener("focus", onFocus); window.clearInterval(timer); };
  }, [restricted, poolID, hostID]);
  if (!report || !member?.restricted) return null;
  return <section className="panel pool-host-restricted" aria-label="Host restriction">
    <div className="section-heading"><h2>Restricted by Nex Core</h2></div>
    <RestrictedNote report={report} member={member} />
  </section>;
}

// The row actions S6 adds, rendered inside the same disclosure as the S5 ones.
export function PromotionActions({ report, member, authoritative, onPromote, onFenceProfile }: {
  report: PromotionReport; member: PromotionMember; authoritative: boolean;
  onPromote?: () => void; onFenceProfile?: () => void;
}) {
  const promote = promotionBlock(report, member);
  const fence = fenceProfileBlock(report, member);
  return <>
    <small>{fenceText(member, authoritative)}</small>
    {onFenceProfile && <>
      <button type="button" disabled={Boolean(fence)} onClick={onFenceProfile}>Record fence device…</button>
      {fence && <small>{fence}</small>}
    </>}
    {onPromote && <>
      <button type="button" className="danger" disabled={Boolean(promote)} onClick={onPromote}>Promote to pool master…</button>
      {promote && <small>{promote}</small>}
    </>}
  </>;
}

// ------------------------------------------------------------------ the verdict, as a list

export function PromotionVerdictView({ report, verdict }: { report: PromotionReport; verdict: PromotionVerdict }) {
  const checks = verdict.checks || [];
  const hosts = verdict.hosts || [];
  const vms = verdict.vms || [];
  const blocking = verdict.blocking || [];
  const fence = verdict.fence;
  return <div className="pool-promotion-verdict">
    <p>Nex Core on {hostName(report, verdict.candidate_host_id)} checked this itself and reported the following at {verdict.observed_at || "an unreported time"}. It holds copy {verdict.authority_generation} of the pool records.</p>
    <ul className="pool-promotion-checks">{checks.map(check => <li key={check.name}>
      <span className={`badge ${checkBadges[check.status] || "unknown"}`}>{check.status}</span> {checkTitle(check.name)}
      {check.reason && <small className={check.status === "blocked" ? "task-error" : undefined}>{check.reason}</small>}
      {(check.hosts || []).length > 0 && <small>Hosts: {(check.hosts || []).map(id => hostName(report, id)).join(", ")}</small>}
      {(check.vms || []).length > 0 && <small>Virtual machines: {(check.vms || []).join(", ")}</small>}
    </li>)}</ul>
    <p className="help"><strong>unknown</strong> stops a promotion exactly as <strong>blocked</strong> does. It says only that Nex Core could not observe that check, never that it is fine.</p>

    {hosts.length > 0 && <div className="table-scroll"><table>
      <caption>What each host in the pool records answered when {hostName(report, verdict.candidate_host_id)} asked it.</caption>
      <thead><tr><th>Host</th><th>Answered</th><th>Role</th><th>Pool authority</th><th>Copy</th><th>Membership</th><th>Stops this promotion</th><th>Fence</th></tr></thead>
      <tbody>{hosts.map(host => <tr key={host.host_id}>
        <td>{hostName(report, host.host_id)}</td>
        <td>{host.reachable ? "answered" : "no answer"}{host.error ? <small className="task-error">{host.error}</small> : null}</td>
        <td>{host.role || "unknown"}</td>
        <td>{host.reachable ? host.authority_generation || 0 : "unknown"}</td>
        <td>{host.reachable ? host.replica_generation || 0 : "unknown"}</td>
        <td>{host.reachable ? host.membership_generation || 0 : "unknown"}</td>
        <td><span className={`badge ${host.blocks ? "failed" : "ok"}`}>{host.blocks ? "stops it" : "no"}</span></td>
        <td className={host.blocks && !fenceHostIDs(verdict).includes(host.host_id) ? "task-error" : undefined}>{fenceCellText(verdict, host)}</td>
      </tr>)}</tbody>
    </table></div>}

    {vms.length > 0 && <>
      <div className="table-scroll"><table>
        <caption>Every virtual machine in the pool records, and whether the host that owns it agrees.</caption>
        <thead><tr><th>Virtual machine</th><th>Owner</th><th>Ownership</th><th>Reason</th></tr></thead>
        <tbody>{vms.map(vm => <tr key={vm.vm_id}>
          <td>{vm.vm_id}</td>
          <td>{hostName(report, vm.host_id)}</td>
          <td><span className={`badge ${vm.state === "confirmed" ? "ok" : vm.state === "blocked" ? "failed" : "warning"}`}>{vmStates[vm.state] || vm.state}</span></td>
          <td>{vm.reason ? <small className={vm.state === "blocked" ? "task-error" : undefined}>{vm.reason}</small> : null}</td>
        </tr>)}</tbody>
      </table></div>
      <p className="help">A virtual machine that is blocked stays defined and stays visible in the pool after a promotion. It cannot be started until the disagreement about where it lives is resolved, and Nex never starts a second copy of it. Being visible is not the same as its disks being available: that is checked when it is started.</p>
    </>}

    {fenceHostIDs(verdict).length > 0 && <p className="warning">This promotion powers off {fenceHostIDs(verdict).map(id => hostName(report, id)).join(", ")}, and Nex Core verifies each one is off before it takes the pool authority.</p>}
    <p>{fence
      ? <>Fence of {hostName(report, fence.host_id)}: <strong>{fence.status}</strong> · {fence.exclusion_verified ? "power-off observed" : "power-off not observed"}{fence.target_id ? ` · target ${fence.target_id}` : ""}{fence.observed_at ? ` · ${fence.observed_at}` : ""}</>
      : <>No fence of the old pool master has been performed for this promotion. Nex Core fences it and observes the power-off itself, when the promotion is confirmed.</>}</p>
    {fence?.error && <p className="task-error">{fence.error}</p>}

    {blocking.length > 0 && <>
      <p><strong>What stops this promotion</strong></p>
      <ul className="pool-promotion-blocking">{blocking.map((reason, index) => <li key={index} className="task-error">{reason}</li>)}</ul>
    </>}
  </div>;
}

// ------------------------------------------------------------------ the promotion dialog

export function PromotionDialog({ report, member, verdict, verdictError, verdictBusy, onPreflight, busy, submitError, onConfirm, onClose }: {
  report: PromotionReport; member: PromotionMember; verdict: PromotionVerdict | null;
  verdictError: string; verdictBusy: boolean; onPreflight: () => void;
  busy: boolean; submitError: string; onConfirm: (confirmation: string, fenceHostIDs: string[]) => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [typed, setTyped] = useState("");
  const [acknowledged, setAcknowledged] = useState(false);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const name = member.name || member.host_id;
  const oldMaster = hostName(report, verdict?.old_master_host_id || report.master_host_id);
  const reachable = oldMasterReachable(verdict);
  const noFence = fenceProfileMissing(verdict);
  const unusable = fenceUnusableReason(verdict);
  const blockers = unfenceableBlockers(verdict);
  const runnable = promotionRunnable(verdict) && !blockers.length;
  // The list the operator is confirming, from the verdict in front of them. Re-running the
  // preflight can change it, so the acknowledgement is dropped whenever it does: what was
  // agreed to is a set of named hosts, not a checkbox.
  const fenceIDs = fenceHostIDs(verdict);
  const fenceKey = fenceIDs.join("\n");
  useEffect(() => { setAcknowledged(false); }, [fenceKey]);
  const fenceNames = fenceIDs.map(id => hostName(report, id)).join(", ");
  const ready = runnable && fenceIDs.length > 0 && confirmMatches(typed, report.pool_name) && acknowledged;
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!ready) return;
    onConfirm(typed.trim(), fenceIDs);
  }
  return <dialog ref={dialog} className="confirm-dialog pool-promotion-dialog" aria-labelledby="promotion-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="promotion-title" ref={heading} tabIndex={-1}>Promote {name} to pool master of {report.pool_name}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>

    <h3>Step 1 — what Nex Core reports</h3>
    {verdictError && <p className="alert" role="alert">{verdictError}</p>}
    {verdict
      ? <PromotionVerdictView report={report} verdict={verdict} />
      : <p role="status">{verdictBusy ? `Asking ${name} to check the pool. It answers on its next report, so this can take up to a minute.` : "Nex Core has not been asked yet."}</p>}
    <div className="actions"><button type="button" disabled={verdictBusy || busy} onClick={onPreflight}>{verdictBusy ? "Checking…" : "Check again"}</button></div>

    <h3>Step 2 — confirm the promotion</h3>
    {/* Core computes the hosts it must power off again when the promotion runs, and refuses a
        list that no longer matches. Its refusal is shown as it wrote it, with the one thing
        that resolves it. */}
    {submitError && <>
      <p className="alert" role="alert">{submitError}</p>
      <p>Nex Core works out which hosts it must power off again when the promotion runs, so the list can have changed since this check. Re-run the check above, read the hosts it names now, and confirm those.</p>
    </>}
    {reachable
      ? <>
        <p className="warning" role="status">{checkOf(verdict, "old-master-exclusion")?.reason}</p>
        <p>Close this and use <strong>Make pool master</strong> on {name}'s row instead. A planned master change hands the pool authority over without powering anything off.</p>
      </>
      : noFence
        ? <>
          <p className="warning" role="status">No fence device is recorded for {oldMaster}, so {name} cannot shut it out and cannot be promoted over it.</p>
          {checkOf(verdict, "old-master-exclusion")?.reason && <p className="task-error">{checkOf(verdict, "old-master-exclusion")?.reason}</p>}
          <p>A fence device is part of the membership record the pool master keeps, so it can only be recorded while the pool master is reporting — that is, before it is lost. This pool cannot be promoted now.</p>
          <p className="help">{fenceBuildLimit}</p>
        </>
        : unusable
          ? <>
            <p className="warning" role="status">A fence device is recorded for {oldMaster}, but {name} cannot use it, so it cannot be promoted over it.</p>
            <p className="task-error">{unusable}</p>
            <p>Nex Core on {name} decides this itself. Resolve what it named on the pool master while it is still reporting, then check again.</p>
            <p className="help">{fenceBuildLimit}</p>
          </>
          : blockers.length
            ? <>
              <p className="warning" role="status">{blockers.map(host => hostName(report, host.host_id)).join(", ")} {blockers.length === 1 ? "stops" : "stop"} this promotion and cannot be powered off, so {name} cannot be promoted.</p>
              <ul className="pool-promotion-blocking">{blockers.map(host => <li key={host.host_id} className="task-error">{hostName(report, host.host_id)}: {fenceBlockerReason(host)}</li>)}</ul>
              <p>{blockers.length === 1 ? "That host" : "Those hosts"} must be brought back so {name} can see {blockers.length === 1 ? "it" : "them"} again, or be given a fence device on the pool master while it is still reporting. Until then this pool cannot be promoted: {name} cannot prove no other host could take the pool authority as well.</p>
              <p className="help">{fenceBuildLimit}</p>
            </>
          : !runnable || !fenceIDs.length
          ? <p role="status">{!verdict
            ? "Nothing is offered until Nex Core has reported its checks."
            : runnable
              ? "Nex Core reports no host that this promotion would power off, so there is nothing to confirm here. Check again."
              : "This promotion is stopped by the checks above. Nex Core decides again when you submit it, so resolve what it named and check again; nothing here can pass a check that did not pass."}</p>
          : <form onSubmit={submit}>
            <p className="warning"><strong>Nex Core on {name} powers {fenceNames} off</strong> through {fenceIDs.length === 1 ? "its" : "their"} recorded fence {fenceIDs.length === 1 ? "device" : "devices"}, and verifies that {fenceIDs.length === 1 ? "it is" : "they are"} off, before it takes the pool authority.</p>
            <label htmlFor="promotion-confirm">Type the pool name {report.pool_name} to confirm</label>
            <input id="promotion-confirm" autoComplete="off" required value={typed} onChange={event => setTyped(event.target.value)} />
            <label className="checkbox" htmlFor="promotion-fence-confirm">
              <input id="promotion-fence-confirm" type="checkbox" checked={acknowledged} onChange={event => setAcknowledged(event.target.checked)} />
              I understand this powers off: {fenceNames}
            </label>
            <div className="actions">
              <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
              <button className="primary danger" disabled={busy || !ready}>{busy ? "Submitting…" : "Promote to pool master"}</button>
            </div>
          </form>}
  </dialog>;
}

// ------------------------------------------------------------------ the progress strip

// The result of the command the operator submitted, which is where Core's asynchronous
// refusals arrive. A promotion is never retried unchanged: the condition must change first.
export function lastPromotionText(last?: LastPromotion | null) {
  if (!last || last.status === "succeeded") return "";
  const message = last.error || "Nex Core reported no message.";
  if (last.conflict) return `${message} Nothing was promoted. Resolve what Nex Core named and check the pool again before submitting another promotion.`;
  if (last.status === "failed") return `The promotion failed: ${message}`;
  if (last.status === "needs_attention") return `Needs attention: ${message} The promotion is durable and still running on Nex Core; the stage above is the candidate's own report.`;
  return "The promotion was submitted. The candidate runs it; this shows what it reports.";
}

export function PromotionStrip({ report }: { report: PromotionReport }) {
  const promotion = report.promotion;
  const last = lastPromotionText(report.last_promotion);
  if (!promotion && !last) return null;
  return <section className="panel pool-promotion-strip" aria-label="Pool master promotion">
    <div className="section-heading"><h3>Pool master promotion</h3></div>
    {promotion && <>
      <p>
        Candidate <strong>{hostName(report, promotion.host_id)}</strong> ·
        stage <strong>{promotionStageText(promotion)}</strong> ·
        status <strong>{(promotion.status || "unknown").replace("_", " ")}</strong>
        {promotion.observed_at ? <small>Reported by {hostName(report, promotion.host_id)} at {promotion.observed_at}</small> : null}
      </p>
      {promotion.error && <p className="task-error">{promotion.error}</p>}
      {/* Core reports whether this can still be cancelled. A promotion fences, so it answers
          false at every stage; the sentence is said rather than a button being offered. */}
      {promotion.status === "pending" && promotion.cancellable === false
        && <p className="help">Nex Core reports that this promotion cannot be cancelled. It can only go forward.</p>}
      {promotion.verdict && <PromotionVerdictView report={report} verdict={promotion.verdict} />}
    </>}
    {last && <p className={report.last_promotion?.status === "failed" ? "task-error" : undefined} role={report.last_promotion?.conflict ? "alert" : "status"}>{last}</p>}
  </section>;
}

// ------------------------------------------------------------------ the fence device dialog

export type FenceProfileInput = { kind: "virsh"; endpoint: string; target_id: string; virsh_uri: string; ssh_login: string; ssh_port: number };
export type FenceCredentialInput = { ssh_private_key: string; ssh_known_hosts: string };

export function FenceProfileDialog({ report, member, busy, submitError, onConfirm, onClear, onClose }: {
  report: PromotionReport; member: PromotionMember; busy: boolean; submitError: string;
  onConfirm: (fence: FenceProfileInput, credential: FenceCredentialInput) => void;
  onClear: () => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [endpoint, setEndpoint] = useState("");
  const [targetID, setTargetID] = useState("");
  const [login, setLogin] = useState("");
  const [port, setPort] = useState("22");
  const [key, setKey] = useState("");
  const [knownHosts, setKnownHosts] = useState("");
  const [clearing, setClearing] = useState(false);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const name = member.name || member.host_id;
  const complete = Boolean(endpoint.trim() && targetID.trim() && login.trim() && port.trim() && key.trim() && knownHosts.trim());
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!complete || busy) return;
    onConfirm(
      { kind: "virsh", endpoint: endpoint.trim(), target_id: targetID.trim(), virsh_uri: "qemu:///system", ssh_login: login.trim(), ssh_port: Number(port) },
      { ssh_private_key: key, ssh_known_hosts: knownHosts },
    );
    // The credential is handed over once and never held here afterwards: it is delivered to
    // Nex Core through the one-time credential handoff and never comes back on the wire.
    setKey("");
    setKnownHosts("");
  }
  return <dialog ref={dialog} className="confirm-dialog pool-fence-dialog" aria-labelledby="fence-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="fence-title" ref={heading} tabIndex={-1}>Fence device for {name}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p>A fence device is how another host powers {name} off and proves it is off. Without one, a surviving member cannot be promoted over {name} if it is lost.</p>
    <p className="warning">{fenceBuildLimit}</p>
    <p className="help">The pool master keeps this record, and every member receives it in its copy of the pool records. The credential is delivered to Nex Core once, over its own encrypted handoff; this Controller never stores it and never shows it again.</p>
    <p>{fenceText(member, report.authoritative)}</p>
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    <form onSubmit={submit}>
      <fieldset disabled={busy}>
        <legend>virsh fence device</legend>
        <label htmlFor="fence-endpoint">Host running libvirt</label>
        <input id="fence-endpoint" required autoComplete="off" placeholder="lab.example.test" aria-describedby="fence-endpoint-help" value={endpoint} onChange={event => setEndpoint(event.target.value)} />
        <p className="help" id="fence-endpoint-help">The machine Nex Core connects to over SSH to power the guest off. It must be reachable from every host that could be promoted, not only from this browser.</p>
        <label htmlFor="fence-target">libvirt domain of {name}</label>
        <input id="fence-target" required autoComplete="off" aria-describedby="fence-target-help" value={targetID} onChange={event => setTargetID(event.target.value)} />
        <p className="help" id="fence-target-help">The domain name or UUID of this host's own virtual machine. Nex Core confirms the domain's identity itself before it accepts a power-off as proof.</p>
        <label htmlFor="fence-login">SSH login</label>
        <input id="fence-login" required autoComplete="off" value={login} onChange={event => setLogin(event.target.value)} />
        <label htmlFor="fence-port">SSH port</label>
        <input id="fence-port" required type="number" min="1" max="65535" value={port} onChange={event => setPort(event.target.value)} />
        <p className="help">The connection is pinned to qemu:///system over this SSH identity.</p>
      </fieldset>
      <fieldset disabled={busy}>
        <legend>Credential</legend>
        <label htmlFor="fence-key">SSH private key</label>
        <textarea id="fence-key" required className="secret" autoComplete="off" spellCheck={false} rows={4} aria-describedby="fence-key-help" value={key} onChange={event => setKey(event.target.value)} />
        <p className="help" id="fence-key-help">Entered once. It is sent to Nex Core over the one-time credential handoff, never stored by this Controller, never written to the audit trail and never shown back here.</p>
        <label htmlFor="fence-known-hosts">SSH known hosts entry</label>
        <textarea id="fence-known-hosts" required className="secret" autoComplete="off" spellCheck={false} rows={2} value={knownHosts} onChange={event => setKnownHosts(event.target.value)} />
        <p className="help">The host key line Nex Core pins the connection to, so a fence can never talk to a substituted machine.</p>
      </fieldset>
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="primary" disabled={busy || !complete}>{busy ? "Submitting…" : "Record fence device"}</button>
      </div>
    </form>
    {member.fence && <div className="actions pool-fence-clear">
      {clearing
        ? <>
          <p className="warning" role="status">Clearing the fence device means no surviving member can be promoted over {name} if it is lost. Virtual machines are not affected.</p>
          <button type="button" disabled={busy} onClick={() => setClearing(false)}>Keep it</button>
          <button type="button" className="danger" disabled={busy} onClick={onClear}>{busy ? "Submitting…" : `Clear the fence device for ${name}`}</button>
        </>
        : <button type="button" className="danger" disabled={busy} onClick={() => setClearing(true)}>Clear fence device…</button>}
    </div>}
  </dialog>;
}
