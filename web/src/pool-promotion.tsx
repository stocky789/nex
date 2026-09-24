// SPDX-License-Identifier: AGPL-3.0-only
// Pool promotion and safe rejoin. Everything here is a host's own
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
import { Badge } from "./ui";
import { capitalize } from "./labels";

export type PromotionCheck = { name: string; status: "passed" | "blocked" | "unknown"; reason?: string; hosts?: string[]; vms?: string[] };
export type PromotionHost = {
  host_id: string; reachable: boolean; role?: string; error?: string;
  authority_generation?: number; replica_generation?: number; membership_generation?: number;
  // Core's own per-host answers: whether this host stops the promotion as things stand, and
  // whether the candidate could shut it out. `fence_unusable_reason` is Core's sentence and is
  // shown verbatim; nothing here is inferred from it.
  blocks?: boolean; fence_recorded?: boolean; fence_usable?: boolean; fence_unusable_reason?: string;
  // The method this promotion would shut this host out with, and what the candidate's own
  // last non-destructive check of that device found when it was failing or stale. The
  // warning is advisory: Core proves the fence again and decides.
  fence_kind?: string; fence_check_warning?: string;
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
  status?: "pending" | "succeeded" | "failed" | "needs_attention"; error?: string; automatic?: boolean;
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
export type MemberFence = {
  // Empty when no profile is recorded and this record carries only the controller report.
  kind?: string; endpoint?: string; username?: string; tls_fingerprint?: string; configured: boolean; credential_version?: number;
  // Whether the account behind this profile is one Nex Core created on the host's own
  // controller, so clearing it also removes that account.
  provisioned_by_core?: boolean;
  // What this host's own Nex Core found on its own management controller, reported through
  // the pool master. `state` is Core's closed set and is what this page switches on;
  // `detail` is Core's sentence and is shown as written.
  bmc?: HostBMC | null;
  // The member that already reported the hardware identity this host reports, when the
  // master refused this host's report because of it.
  system_uuid_conflict_host_id?: string;
  // The master's own decision that this host must not configure its controller. It is what
  // the row's actions are decided from: a host that is down after its device was cleared
  // has echoed nothing, and the master's decision is the one that is true.
  provisioning_disabled?: boolean;
  // The master's own per-method answer to "could this host be shut out right now". Absent
  // means the master did not report one, which is never ready.
  readiness?: FenceReadiness | null;
  // What this host's own Nex Core found when it looked for a watchdog, mirrored from the
  // master's membership. Absent means the host never reported one.
  watchdog?: HostWatchdog | null;
  // That host's own report that it has armed the device, mirrored from the master's durable
  // membership. Absent means the host never said.
  watchdog_armed?: WatchdogArmed | null;
};
// `kind` is Core's own classification and is the only thing acted on: only "hardware" can ever
// count, and the kernel's software watchdog is reported precisely so an operator sees why.
export type HostWatchdog = { kind: "hardware" | "emulated" | "software" | "unknown" | "none"; device?: string; driver?: string; timeout_seconds?: number; nowayout?: boolean; in_use?: boolean };
// The host's own report that it has armed that device. `kind` is what it armed, which is
// "emulated" where the host is itself a virtual machine: such a proof is evidence about a
// hypervisor, never about hardware, and every line that rests on one says so.
export type WatchdogArmed = { armed: boolean; kind?: string; device?: string; timeout_seconds?: number; fencing_generation?: number; detail?: string; observed_at?: string };
export type HostBMC = { state: string; detail?: string; address?: string; channel?: number; account?: string; observed_at?: string };
// One other host's last non-destructive read of this host's fence device. `stale` is the
// master's own answer about how long ago that was; nothing here computes freshness.
export type FenceCheck = { host_id: string; result: string; detail?: string; power?: string; observed_at?: string; stale?: boolean };
export type FenceMethod = { kind: string; state: "ready" | "not_ready" | "unknown" | "no_peer" | "not_applicable"; reason?: string; checks?: FenceCheck[] };
// The pool's own fencing answer, reported by the master with its membership: the count, the
// Host fencing setting and Core's prerequisite checklist. Nothing here counts rendered rows or
// decides a prerequisite — a client would count ejected tombstones and hosts still joining.
// `hosts` is which hosts the sentence is about, as host IDs: Core ends the sentence before
// naming them, because only a client knows their display names.
export type FencePrerequisite = { name: string; state: "met" | "unmet" | "unknown"; detail: string; hosts?: string[] };
// The pool's own Corosync/Pacemaker cluster, which exists only so a watchdog has a quorum to
// lose. Nex Core forms it for a pool that has no clustered storage; a pool that has one keeps
// the cluster clustered storage already owns, and is not armed at all.
export type FenceCluster = {
  state: "off" | "not_available" | "forming" | "arming" | "armed" | "degraded";
  detail?: string; armed: number; total: number; arming_host_id?: string;
};
export type FenceSummary = { ready: number; total: number; unready?: string[]; host_fencing: boolean; prerequisites?: FencePrerequisite[]; cluster?: FenceCluster | null };
export type FenceReadiness = { ready: boolean; methods?: FenceMethod[] };
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

const checkTitles: Record<string, string> = {
  "replica-integrity": "This host's copy of the pool records is whole and signed",
  "replica-supremacy": "No reachable host holds a newer copy",
  "accepted-ownership": "Every pool virtual machine's owner agrees with the copy",
  "old-master-exclusion": "The old pool master is shut out",
};
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
  if (fenceHostIDs(verdict).includes(host.host_id)) {
    return `powered off by this promotion${host.fence_kind ? ` · ${fenceMethodName(host.fence_kind)}` : ""}`;
  }
  if (host.blocks) return `cannot be fenced — ${fenceBlockerReason(host)}`;
  return "not powered off";
}
// When the operator may be offered the destructive step. The candidate answers this itself:
// `ready_to_fence` means confirming proceeds to the fence rather than to a refusal, and
// `can_promote` means the whole proof already holds. Neither is inferred from the checks, so a
// Core old enough to report neither field is simply never runnable.
export function promotionRunnable(verdict?: PromotionVerdict | null) {
  return Boolean(verdict?.can_promote || verdict?.ready_to_fence);
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
  if (!member.fence || !member.fence.kind) return "Fence device: not configured. A pool master with no fence device cannot be promoted over.";
  const credential = member.fence.credential_version ? "credential recorded" : "no credential recorded";
  const account = member.fence.username ? ` · ${member.fence.username}` : "";
  // The pinned certificate is shown so an operator can compare it with the controller's own
  // page; it is not a secret and it is the only thing that identifies that endpoint.
  const pinned = member.fence.tls_fingerprint ? ` · certificate ${member.fence.tls_fingerprint}` : "";
  return `Fence device: ${member.fence.kind}${member.fence.endpoint ? ` · ${member.fence.endpoint}` : ""}${account} · ${member.fence.configured ? "configured" : "not configured"} · ${credential}${pinned}`;
}

// Each host configures its own management controller, so the normal row says what its own
// Nex Core found there. Core's own sentence is shown as written; this only decides whether
// there is one to show.
export function bmcText(member: PromotionMember, authoritative: boolean) {
  if (!authoritative) return "";
  const bmc = member.fence?.bmc;
  const conflict = member.fence?.system_uuid_conflict_host_id;
  if (conflict) {
    return `Management controller: this host reports the same hardware identity as ${conflict}, so neither of them can be given one until that is resolved. ${bmc?.detail || ""}`.trim();
  }
  if (!bmc) return "";
  if (bmc.state === "provisioned") {
    return `Management controller: configured by Nex Core${bmc.address ? ` at ${bmc.address}` : ""}${bmc.account ? ` as ${bmc.account}` : ""}.`;
  }
  return `Management controller: ${bmc.detail || "Nex Core reported no reason."}`;
}

// Set BMC access… is the way out of a host Nex Core could not configure from the inside, so
// it is offered exactly there: everywhere except a host that has the device its own Core
// created. It is decided from the pool master's record, never from the host's echo of it —
// a host that is down after its device was cleared has echoed nothing, and would otherwise
// be left with no action at all. It stays offered on a host whose profile an operator set by
// hand, because that is where they change or clear it; on a host whose Core is too old to
// report; and while the master is not fresh, where the button is disabled anyway.
export function manualBMCOffered(member: PromotionMember) {
  return !(member.fence?.configured && member.fence.provisioned_by_core);
}

// Letting a host configure its controller again is offered exactly where an operator turned
// it off, which is the master's own recorded decision rather than the host's echo of it.
// There is no separate "remove the account" action: clearing the fence device is what removes
// it, one operator action with one meaning.
export function bmcReenableOffered(member: PromotionMember, authoritative: boolean) {
  return authoritative && Boolean(member.fence?.provisioning_disabled);
}

// ------------------------------------------------------------------ the Fencing check

// How a host would be shut out, said in the fewest words that are true, with the reasons
// in the row's details. Every judgement here is the pool master's: this only chooses the wording.
//
//   - The master is not reporting            → unknown. Its record is the only one.
//   - It reports no readiness at all         → not reported. An older Core; never ready.
//   - A method is ready                      → the method, and how many hosts confirm it.
//   - Every check is stale                   → stale. A stale check is never readiness.
//   - Anything else                          → not protected, and Core's own reason.
const fenceMethodNames: Record<string, string> = { ipmilan: "BMC", redfish: "BMC", virsh: "virsh lab device", watchdog: "Watchdog" };
export function fenceMethodName(kind: string) {
  return fenceMethodNames[kind] || kind || "Fencing";
}
export function fencingText(member: PromotionMember, authoritative: boolean): { label: string; badge: string; reasons: string[] } {
  // fenceText below already says why it is unknown, so the cell does not say it twice.
  if (!authoritative) return { label: "unknown", badge: "unknown", reasons: [] };
  const readiness = member.fence?.readiness;
  if (!readiness || !(readiness.methods || []).length) {
    return {
      label: "not reported", badge: "unknown",
      reasons: ["This pool master does not report fencing readiness. Upgrade it to see whether this host could be shut out."],
    };
  }
  // The badge carries one method, so it carries the fence device where that is ready and
  // the watchdog otherwise: a host another host can power off is protected whatever its own
  // watchdog is doing. The watchdog's own line below says the rest.
  const all = readiness.methods || [];
  const methods = all.filter(method => method.kind !== "watchdog");
  const armed = all.find(method => method.kind === "watchdog" && method.state === "ready");
  if (armed && !methods.some(method => method.state === "ready")) {
    const emulated = member.fence?.watchdog_armed?.kind === "emulated" ? " (emulated)" : "";
    const seconds = member.fence?.watchdog_armed?.timeout_seconds;
    return { label: `Watchdog · armed${emulated}`, badge: "ok",
      reasons: [`This host feeds a watchdog that resets it${seconds ? ` ${seconds} seconds` : ""} after it loses this pool's quorum.`] };
  }
  if (!methods.length) return { label: "not protected", badge: "failed", reasons: [] };
  // A pool with one host has nobody to be checked from, ever, and nothing to promote over.
  // It is not pending and must not read as though it were.
  const alone = methods.find(method => method.state === "no_peer");
  if (alone && !methods.some(method => method.state === "ready")) {
    return { label: `${fenceMethodName(alone.kind)} · no other host to check from`, badge: "unknown", reasons: alone.reason ? [alone.reason] : [] };
  }
  const ready = methods.find(method => method.state === "ready");
  if (ready) {
    const confirmed = (ready.checks || []).filter(check => check.result === "ok" && !check.stale);
    const from = confirmed.length ? ` · checked from ${confirmed.length} host${confirmed.length === 1 ? "" : "s"}` : "";
    return { label: `${fenceMethodName(ready.kind)}${from}`, badge: "ok", reasons: ready.reason ? [ready.reason] : [] };
  }
  const reasons = methods.map(method => method.reason).filter((reason): reason is string => Boolean(reason));
  const checks = methods.flatMap(method => method.checks || []);
  if (checks.length && checks.every(check => check.stale)) {
    return { label: `${fenceMethodName(methods[0].kind)} · stale`, badge: "warning", reasons };
  }
  return { label: `Not protected — ${reasons[0] || "Nex Core reported no reason."}`, badge: "failed", reasons: reasons.slice(1) };
}

// Each other host's own last look at this host's fence device, so an operator sees which
// host could not reach it rather than only that something could not.
export function fenceCheckLines(member: PromotionMember, authoritative: boolean) {
  if (!authoritative) return [];
  return ((member.fence?.readiness?.methods || []).flatMap(method => method.checks || [])).map(check => {
    const when = check.observed_at ? ` · last confirmed ${check.observed_at}` : "";
    if (check.stale) return `${check.host_id}: stale — Nex Core has not had this confirmed recently${when}.`;
    if (check.result === "ok") return `${check.host_id}: reachable${check.power ? `, machine ${check.power}` : ""}${when}.`;
    return `${check.host_id}: ${check.detail || check.result}${when}.`;
  });
}

// What the master says about this host's own watchdog, as its own line. When the watchdog is
// what the badge above already carries the line would only repeat it, so it is left out.
export function watchdogText(member: PromotionMember, authoritative: boolean) {
  if (!authoritative) return "";
  const methods = member.fence?.readiness?.methods || [];
  const method = methods.find(entry => entry.kind === "watchdog");
  if (!method) return "";
  if (method.state === "ready") {
    if (!methods.some(entry => entry.kind !== "watchdog" && entry.state === "ready")) return "";
    const emulated = member.fence?.watchdog_armed?.kind === "emulated" ? ", emulated" : "";
    return `Watchdog: armed${emulated} — this host also resets itself if it loses this pool's quorum.`;
  }
  return `Watchdog: ${method.reason || method.state}`;
}

// The Fencing details under a member's row: the state in full, the reasons, the fence device
// and what every other host's last look at it found. The row's status strip carries the short
// result; nothing here is colour alone.
export function FencingDetail({ member, authoritative }: { member: PromotionMember; authoritative: boolean }) {
  const fencing = fencingText(member, authoritative);
  const checks = fenceCheckLines(member, authoritative);
  // "not reported" is the pool master's answer for every row; the pool strip says it once.
  const reasons = fencing.label === "not reported" ? [] : fencing.reasons;
  return <>
    <small className={fencing.badge === "failed" ? "task-error" : undefined}>{capitalize(fencing.label)}</small>
    {reasons.map((reason, index) => <small key={index} className={fencing.badge === "failed" ? "task-error" : undefined}>{reason}</small>)}
    <small>{fenceText(member, authoritative)}</small>
    {watchdogText(member, authoritative) && <small>{watchdogText(member, authoritative)}</small>}
    {bmcText(member, authoritative) && <small>{bmcText(member, authoritative)}</small>}
    {checks.length > 0 && <ul className="pool-fence-checks">{checks.map((line, index) => <li key={index}>{line}</li>)}</ul>}
  </>;
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
    <Badge value="failed" label="Restricted" />
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
export function HostRestrictedBanner({ poolID, hostID, api, report: provided }: {
  poolID: string; hostID: string; api: <T>(path: string) => Promise<T>;
  report?: PromotionReport | null;
}) {
  const [fetched, setFetched] = useState<PromotionReport | null>(null);
  const load = useRef<() => void>(() => {});
  load.current = () => {
    api<PromotionReport>(`pools/${encodeURIComponent(poolID)}/members`)
      .then(answer => setFetched(answer), () => {});
  };
  // Parent host panel may already poll members; only fetch here when no report is supplied.
  useEffect(() => {
    if (provided !== undefined) return;
    setFetched(null);
    load.current();
  }, [poolID, hostID, provided]);
  const report = provided !== undefined ? provided : fetched;
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
export function PromotionActions({ report, member, authoritative, onPromote, onFenceProfile, onBMCProvisioning, bmcBusy }: {
  report: PromotionReport; member: PromotionMember; authoritative: boolean;
  onPromote?: () => void; onFenceProfile?: () => void; onBMCProvisioning?: () => void; bmcBusy?: boolean;
}) {
  const promote = promotionBlock(report, member);
  const fence = fenceProfileBlock(report, member);
  return <>
    {/* What the fence device is and what Nex Core found on the controller is in the row's
        Fencing details; this is only what an operator can do about it. */}
    {/* One entry point to this host's fence device. On a host Nex Core configured itself
        there is nothing to type, so it opens on the device that is there and its Clear
        action — which is also what removes the account Core created. */}
    {onFenceProfile && <>
      <button type="button" disabled={Boolean(fence)} onClick={onFenceProfile}>
        {manualBMCOffered(member) ? "Set BMC access…" : "Clear fence device…"}
      </button>
      {fence && <small>{fence}</small>}
    </>}
    {onBMCProvisioning && bmcReenableOffered(member, authoritative) &&
      <button type="button" disabled={bmcBusy} onClick={onBMCProvisioning}>{bmcBusy ? "Submitting…" : "Let Nex configure this controller again"}</button>}
    {onPromote && member.role !== "master" && <>
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
      <Badge value={check.status} /> {checkTitle(check.name)}
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
        <td><Badge value={host.blocks ? "failed" : "ok"} label={host.blocks ? "stops it" : "no"} /></td>
        <td className={host.blocks && !fenceHostIDs(verdict).includes(host.host_id) ? "task-error" : undefined}>
          {fenceCellText(verdict, host)}
          {/* Advisory: the candidate's own last non-destructive check of that device.
              Nex Core proves the fence again when the promotion runs and decides. */}
          {host.fence_check_warning && <>
            <small className="pool-fence-advisory">Advisory: {host.fence_check_warning}</small>
            <small className="pool-fence-advisory">Nex Core proves the fence itself when the promotion runs; this stops nothing.</small>
          </>}
        </td>
      </tr>)}</tbody>
    </table></div>}

    {vms.length > 0 && <>
      <div className="table-scroll"><table>
        <caption>Every virtual machine in the pool records, and whether the host that owns it agrees.</caption>
        <thead><tr><th>Virtual machine</th><th>Owner</th><th>Ownership</th><th>Reason</th></tr></thead>
        <tbody>{vms.map(vm => <tr key={vm.vm_id}>
          <td>{vm.vm_id}</td>
          <td>{hostName(report, vm.host_id)}</td>
          <td><Badge value={vm.state === "confirmed" ? "ok" : vm.state === "blocked" ? "failed" : "warning"} label={vmStates[vm.state] || vm.state} /></td>
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

export function PromotionDialog({ report, member, verdict, verdictError, verdictBusy, onPreflight, busy, submitError, readError = "", onConfirm, onClose }: {
  report: PromotionReport; member: PromotionMember; verdict: PromotionVerdict | null;
  verdictError: string; verdictBusy: boolean; onPreflight: () => void;
  busy: boolean; submitError: string; readError?: string; onConfirm: (confirmation: string, fenceHostIDs: string[]) => void; onClose: () => void;
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
  const ready = !readError && runnable && fenceIDs.length > 0 && confirmMatches(typed, report.pool_name) && acknowledged;
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
    {readError && <p className="alert" role="alert">The current pool report could not be read: {readError}. Retry the report before submitting.</p>}
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
        </>
        : unusable
          ? <>
            <p className="warning" role="status">A fence device is recorded for {oldMaster}, but {name} cannot use it, so it cannot be promoted over it.</p>
            <p className="task-error">{unusable}</p>
            <p>Nex Core on {name} decides this itself. Resolve what it named on the pool master while it is still reporting, then check again.</p>
            </>
          : blockers.length
            ? <>
              <p className="warning" role="status">{blockers.map(host => hostName(report, host.host_id)).join(", ")} {blockers.length === 1 ? "stops" : "stop"} this promotion and cannot be powered off, so {name} cannot be promoted.</p>
              <ul className="pool-promotion-blocking">{blockers.map(host => <li key={host.host_id} className="task-error">{hostName(report, host.host_id)}: {fenceBlockerReason(host)}</li>)}</ul>
              <p>{blockers.length === 1 ? "That host" : "Those hosts"} must be brought back so {name} can see {blockers.length === 1 ? "it" : "them"} again, or be given a fence device on the pool master while it is still reporting. Until then this pool cannot be promoted: {name} cannot prove no other host could take the pool authority as well.</p>
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
  return <section className="substrip pool-promotion-strip" aria-label="Pool master promotion">
    <h3>Pool master promotion</h3>
    {promotion && <>
      <p>
        Candidate <strong>{hostName(report, promotion.host_id)}</strong> ·
        stage <strong>{promotionStageText(promotion)}</strong> ·
        status <strong>{(promotion.status || "unknown").replace("_", " ")}</strong>{promotion.automatic ? " · automatic" : ""}
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

export type FenceProfileInput =
  | { kind: "redfish" | "ipmilan"; endpoint: string; target_id: string; username: string }
  | { kind: "virsh"; endpoint: string; target_id: string; virsh_uri: string; ssh_login: string; ssh_port: number };
export type FenceCredentialInput = { secret: string } | { ssh_private_key: string; ssh_known_hosts: string };
type BMCKind = "redfish" | "ipmilan";
export type BMCProbeView = {
  host_id: string; status: "succeeded" | "failed"; error: string;
  bmc_probe: { methods: { kind: BMCKind; reachable: boolean; target_ids?: string[]; detail?: string }[] | null; recommended?: { kind: BMCKind; target_id: string; reason: string } } | null;
};

// Only Core's recommendation fills anything in; a refusal is Core's own sentence.
export function bmcProbePrefill(view: BMCProbeView): { kind: BMCKind; targetID: string; note: string } | { error: string } {
  const choice = view.status === "succeeded" ? view.bmc_probe?.recommended : undefined;
  if (!choice) return { error: view.error || "The pool master found no method to recommend." };
  return { kind: choice.kind, targetID: choice.target_id, note: choice.reason };
}

// The normal path is a management controller: an address, an account and a password. Nex Core
// asks that controller which machine it manages and refuses to power anything off unless the
// answer is this host, so nothing here is trusted on the operator's word.
//
// The virsh device below it is for a Nex Core running as a virtual machine in a disposable
// lab, where there is no controller to ask. It stays reachable, behind a disclosure, because
// it is not how a real pool is protected.
export function FenceProfileDialog({ report, member, busy, submitError, readError = "", onConfirm, onClear, onClose, onDetect }: {
  report: PromotionReport; member: PromotionMember; busy: boolean; submitError: string; readError?: string;
  onConfirm: (fence: FenceProfileInput, credential: FenceCredentialInput) => void;
  onClear: () => void; onClose: () => void;
  onDetect?: (endpoint: string, username: string, secret: string) => Promise<BMCProbeView>;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [kind, setKind] = useState<BMCKind>("redfish");
  const [address, setAddress] = useState("");
  const [systemID, setSystemID] = useState("");
  const [account, setAccount] = useState("");
  const [password, setPassword] = useState("");
  const [detecting, setDetecting] = useState(false);
  const [detected, setDetected] = useState<{ note: string } | { error: string } | null>(null);
  const [lab, setLab] = useState(false);
  const [endpoint, setEndpoint] = useState("");
  const [targetID, setTargetID] = useState("");
  const [login, setLogin] = useState("");
  const [port, setPort] = useState("22");
  const [key, setKey] = useState("");
  const [knownHosts, setKnownHosts] = useState("");
  const [clearing, setClearing] = useState(false);
  // A host whose own Nex Core configured its controller has nothing to type. The row's
  // action for it is Clear fence device…, so the dialog opens on that and keeps the manual
  // form behind a disclosure instead of presenting an empty required form the label never
  // promised. (Reported by independent review of slice 2.)
  const provisioned = Boolean(member.fence?.configured && member.fence.provisioned_by_core);
  const [manual, setManual] = useState(!provisioned);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const name = member.name || member.host_id;
  const complete = lab
    ? Boolean(endpoint.trim() && targetID.trim() && login.trim() && port.trim() && key.trim() && knownHosts.trim())
    : Boolean(address.trim() && systemID.trim() && account.trim() && password);
  // The account goes with Detect only together with its password; either alone probes anonymously.
  async function detect() {
    if (!onDetect || !address.trim() || detecting || busy) return;
    setDetecting(true); setDetected(null);
    const signIn = account.trim() && password;
    try {
      const prefill = bmcProbePrefill(await onDetect(address.trim(), signIn ? account.trim() : "", signIn ? password : ""));
      if ("kind" in prefill) { setKind(prefill.kind); setSystemID(prefill.targetID); }
      setDetected(prefill);
    } catch (e) {
      setDetected({ error: (e as Error).message });
    } finally { setDetecting(false); }
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!complete || busy || readError) return;
    if (lab) {
      onConfirm(
        { kind: "virsh", endpoint: endpoint.trim(), target_id: targetID.trim(), virsh_uri: "qemu:///system", ssh_login: login.trim(), ssh_port: Number(port) },
        { ssh_private_key: key, ssh_known_hosts: knownHosts },
      );
    } else {
      onConfirm(
        { kind, endpoint: address.trim(), target_id: systemID.trim(), username: account.trim() },
        { secret: password },
      );
    }
    // The credential is handed over once and never held here afterwards: it is delivered to
    // Nex Core through the one-time credential handoff and never comes back on the wire.
    setPassword("");
    setKey("");
    setKnownHosts("");
  }
  return <dialog ref={dialog} className="confirm-dialog pool-fence-dialog" aria-labelledby="fence-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="fence-title" ref={heading} tabIndex={-1}>Fence device for {name}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p>A fence device is how another host powers {name} off and proves it is off. Without one, a surviving member cannot be promoted over {name} if it is lost.</p>
    <p className="help">The pool master keeps this record, and every member receives it in its copy of the pool records. The password is delivered to Nex Core once, over its own encrypted handoff; this Controller never stores it and never shows it again.</p>
    <p>{fenceText(member, report.authoritative)}</p>
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    {readError && <p className="alert" role="alert">The current pool report could not be read: {readError}. Retry the report before submitting.</p>}
    {provisioned && <p className="help">
      <button type="button" className="link" disabled={busy} aria-expanded={manual} onClick={() => setManual(!manual)}>
        {manual ? "Keep the device Nex Core configured" : "Set BMC access by hand instead…"}
      </button>
    </p>}
    {manual && <form onSubmit={submit}>
      {!lab && <fieldset disabled={busy || detecting}>
        <legend>Management controller of {name}</legend>
        <label htmlFor="fence-address">Address</label>
        <input id="fence-address" required autoComplete="off" placeholder="bmc.example.test" aria-describedby="fence-address-help" value={address} onChange={event => setAddress(event.target.value)} />
        <p className="help" id="fence-address-help">The management controller of {name}, reachable from every host that could be promoted — not only from this browser.</p>
        <label htmlFor="fence-account">Account</label>
        <input id="fence-account" required autoComplete="off" aria-describedby="fence-account-help" value={account} onChange={event => setAccount(event.target.value)} />
        <p className="help" id="fence-account-help">An account with power control on this controller. Nex Core needs nothing else from it.</p>
        <label htmlFor="fence-password">Password</label>
        <input id="fence-password" required type="password" className="secret" autoComplete="off" aria-describedby="fence-password-help" value={password} onChange={event => setPassword(event.target.value)} />
        <p className="help" id="fence-password-help">Entered once. It is sent to Nex Core over the one-time credential handoff, never stored by this Controller, never written to the audit trail and never shown back here.</p>
        {onDetect && <div className="actions pool-fence-detect">
          <button type="button" disabled={!address.trim()} onClick={() => void detect()}>{detecting ? "Detecting…" : "Detect"}</button>
          {detected && ("error" in detected
            ? <p className="alert" role="alert">{detected.error}</p>
            : <p className="help" role="status">{detected.note}</p>)}
        </div>}
        <fieldset className="pool-fence-kind">
          <legend>Kind</legend>
          <label className="checkbox" htmlFor="fence-kind-redfish">
            <input id="fence-kind-redfish" type="radio" name="fence-kind" value="redfish" checked={kind === "redfish"} onChange={() => setKind("redfish")} />
            Redfish
          </label>
          <label className="checkbox" htmlFor="fence-kind-ipmi">
            <input id="fence-kind-ipmi" type="radio" name="fence-kind" value="ipmilan" checked={kind === "ipmilan"} onChange={() => setKind("ipmilan")} />
            IPMI
          </label>
        </fieldset>
        <label htmlFor="fence-system">{kind === "redfish" ? "Redfish system identifier" : "IPMI plug"}</label>
        <input id="fence-system" required autoComplete="off" placeholder={kind === "redfish" ? "1" : "1"} aria-describedby="fence-system-help" value={systemID} onChange={event => setSystemID(event.target.value)} />
        <p className="help" id="fence-system-help">{kind === "redfish"
          ? "The ComputerSystem this controller manages, usually 1 or System.Embedded.1."
          : "The outlet or plug this controller powers, usually 1."}</p>
        <p className="help">When you submit this, Nex Core connects to the controller, records the exact certificate it presents and asks it which machine it manages. It refuses unless the answer is {name}'s own machine, so a host that does not report a unique hardware identity cannot be given a controller here. Afterwards every connection must present that same certificate: the recorded fingerprint is shown on this host's row, and you can compare it with the controller's own page.</p>
      </fieldset>}
      {lab && <>
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
      </>}
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="primary" disabled={busy || Boolean(readError) || detecting || !complete}>{busy ? "Submitting…" : lab ? "Record virsh fence device" : "Set BMC access"}</button>
      </div>
    </form>}
    {manual && <p className="help pool-fence-lab">
      <button type="button" className="link" disabled={busy} aria-expanded={lab} onClick={() => setLab(!lab)}>
        {lab ? "This host has a management controller" : "This host runs as a virtual machine in a lab"}
      </button>
    </p>}
    {member.fence?.kind && <div className="actions pool-fence-clear">
      {clearing
        ? <>
          <p className="warning" role="status">Clearing the fence device means no surviving member can be promoted over {name} if it is lost. Virtual machines are not affected.{member.fence.provisioned_by_core ? ` Nex Core configured this controller itself, so it also removes the ${member.fence.username || "Nex"} account from it and stops configuring it until you say otherwise.` : ""}</p>
          <button type="button" disabled={busy} onClick={() => setClearing(false)}>Keep it</button>
          <button type="button" className="danger" disabled={busy || Boolean(readError)} onClick={onClear}>{busy ? "Submitting…" : `Clear the fence device for ${name}`}</button>
        </>
        : <button type="button" className="danger" disabled={busy} onClick={() => setClearing(true)}>Clear fence device…</button>}
    </div>}
  </dialog>;
}
