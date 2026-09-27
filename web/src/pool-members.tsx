// SPDX-License-Identifier: AGPL-3.0-only
import { Fragment, useEffect, useRef, useState, type FormEvent } from "react";
import { heldRequestID } from "./managed";
import { EjectDialog, MasterChangeDialog, MemberActions, ResumeDialog, TransferStrip, authorityText, behindText, hostName, transferInProgress, transferResumePrompt, type Authority, type LastTransfer, type Transfer } from "./pool-authority";
import {
  FenceProfileDialog, FencingDetail, fencingText, PromotionActions, PromotionDialog, PromotionStrip, RestrictedNote,
  type BMCProbeView, type FenceCluster, type FenceCredentialInput, type FenceProfileInput, type FencePrerequisite, type FenceSummary, type LastPromotion,
  type MemberFence, type Promotion, type PromotionVerdict, type Restricted,
} from "./pool-promotion";
import { Badge, Section, badgeTone, date, type Tone } from "./ui";
import { versionLight } from "./updates";
import type { Host } from "./types";
import { capitalize, checkLabel, relativeTime, statusLabel } from "./labels";

type API = <T>(path: string, method?: string, body?: unknown, progress?: (loaded: number, total: number) => void, register?: (xhr: XMLHttpRequest) => void, signal?: AbortSignal) => Promise<T>;
type Connection = "fresh" | "stale" | "unknown" | "revoked";
type Check = { name: string; status: "healthy" | "failed" | "unknown"; detail?: string };
type Access = { healthy: number; pending: number; failed: number; stale: number };
type Failure = { kind: string; id: string; name: string; error: string };
export type JoinStatus = { operation_id?: string; stage: string; status: "pending" | "succeeded" | "failed" | "needs_attention"; error?: string; observed_at?: string };
export type MemberOperation = { operation_id: string; action?: string; status: string; error?: string; conflict?: boolean };
export type Guests = { known: boolean; total: number; running: number; names?: string[] };
export type ReplicaState = "current" | "behind" | "unverified" | "refused" | "awaiting" | "unsupported" | "unknown";
export type Replica = { generation: number; digest?: string; verified: boolean; received_at?: string; acknowledged_generation: number; acknowledged_at?: string; error?: string; state: ReplicaState };
export type Member = { host_id: string; name: string; role: "master" | "member"; status: string; error?: string; peer_origin?: string; connection: Connection; checks?: Check[]; access?: Access; failures?: Failure[]; join?: JoinStatus | null; maintenance?: boolean | null; maintenance_since?: string; guests?: Guests; last_maintenance?: MemberOperation | null; replica?: Replica | null; transfer?: Transfer | null; transfer_supported?: boolean; fence?: MemberFence | null; restricted?: Restricted | null; joined_at?: string };
export type Report = { pool_id: string; pool_name: string; master_host_id: string; master_connection: Connection; master_seen_at?: string; generation: number; authoritative: boolean; maintenance_supported?: boolean; replica_supported?: boolean; transfer_supported?: boolean; host_fencing_supported?: boolean; fence?: FenceSummary | null; authority?: Authority | null; transfer?: Transfer | null; last_transfer?: LastTransfer | null; master_conflict?: string[]; promotion?: Promotion | null; last_promotion?: LastPromotion | null; members?: Member[] };
type CandidateRow = { check: string; subject: string; result: "compatible" | "will_not_converge" | "refuses_join"; detail?: string };
export type Candidate = { host_id: string; name: string; connection: Connection; eligible: boolean; rows?: CandidateRow[]; peer_address?: string; peer_port: number };

const checkNames: Record<string, string> = {
  "pool-storage-reconciliation": "Storage",
  "pool-network-reconciliation": "Networks",
  "pool-media-reconciliation": "ISO library",
  "pool-authority-replica": "Authority copy",
  "pool-promotion": "Promotion",
  "pool-restricted": "Restricted",
  "pool-authority-transfer": "Master change",
};
// The short result each state reads as on the row's status strip. Text carries the state; the
// light only repeats it.
const replicaStates: Record<ReplicaState, { result: string; tone: Tone }> = {
  current: { result: "OK", tone: "ok" },
  behind: { result: "Behind", tone: "warn" },
  unverified: { result: "Unverified", tone: "warn" },
  refused: { result: "Failed", tone: "bad" },
  awaiting: { result: "Awaiting", tone: "neutral" },
  unsupported: { result: "Not supported", tone: "warn" },
  unknown: { result: "Unknown", tone: "neutral" },
};
// PoolJoinStatus.stage is the step still to perform, so it reads as what happens next.
const stageNames: Record<string, string> = {
  precheck: "checking this host",
  csr: "requesting its pool certificate",
  joined: "joining the pool",
  rehomed: "moving its records to the pool master",
  advertised: "publishing its address to the master",
  ready: "finishing the join",
};
const resultNames: Record<CandidateRow["result"], string> = { compatible: "compatible", will_not_converge: "will not converge", refuses_join: "refuses join" };
// Text carries the verdict; the badge colour only repeats it.
const resultBadges: Record<CandidateRow["result"], string> = { compatible: "ok", will_not_converge: "warning", refuses_join: "failed" };

// The health checks are each host's own report and stay readable while the master is gone —
// choosing a promotion candidate depends on them. Access is the master's observation of its
// members, so it is unknown without a fresh master rather than repeated from the last mirror.
export function convergenceSummary(member: Member, authoritative: boolean) {
  const counted = (label: string, n?: number) => n ? `${n} ${label}` : "";
  const checks = member.checks || [];
  const access = member.access;
  const checkText = checks.length
    ? [counted("healthy", checks.filter(c => c.status === "healthy").length), counted("failed", checks.filter(c => c.status === "failed").length), counted("unknown", checks.filter(c => c.status !== "healthy" && c.status !== "failed").length)].filter(Boolean).join(", ")
    : "not reported";
  if (!authoritative) return `Checks ${checkText} · Access unknown`;
  const accessText = access
    ? [counted("healthy", access.healthy), counted("pending", access.pending), counted("failed", access.failed), counted("stale", access.stale)].filter(Boolean).join(", ") || "none registered"
    : "not reported";
  return `Checks ${checkText} · Access ${accessText}`;
}

// Progress is the host's own reported stage, never a spinner: a Controller that is not watching
// changes nothing, and a stalled join must read as stalled instead of as activity.
export function joinProgressText(join?: JoinStatus | null) {
  if (!join) return "";
  const step = stageNames[join.stage] || join.stage;
  if (join.status === "succeeded") return "Join complete.";
  if (join.status === "failed") return `Join failed before ${step}. ${join.error || "Core reported no message."}`;
  if (join.status === "needs_attention") return `Needs attention: ${join.error || "Core reported no message."} This host is already a member; Core retries ${step} on its own.`;
  return `Joining · next step: ${step}.`;
}

// The operator sees only the address: the scheme is always https and the port is the one the
// host reports for its peer listener (the Controller fills in Core's default for a host that
// reports none). The origin Core wants is composed here, and anything that is not a bare
// address, such as a pasted origin or an address with a port, is refused rather than guessed at.
export function peerOriginOf(address: string, port: number) {
  const value = address.trim();
  const only = { origin: "", error: "Enter the address only, for example 192.0.2.12. The port is the host's own." };
  if (!value || /[\/@?#\s]/.test(value)) return only;
  const literal = value.includes(":") ? `[${value}]` : value;
  let url: URL;
  try { url = new URL(`https://${literal}:${port}`); } catch { return only; }
  if (url.host !== `${literal.toLowerCase()}:${port}`) return only;
  return { origin: url.origin, error: "" };
}

export function membershipVersionReady(report: Pick<Report, "authoritative" | "generation"> | null) {
  return Boolean(report?.authoritative && report.generation > 0);
}

// Maintenance is the master's durable flag. The GET sends null for it whenever the master is not
// fresh, and this never turns an unknown into "available": a host that might be drained must not
// look like a placement target.
export function maintenanceText(member: Member, authoritative: boolean) {
  if (!authoritative || member.maintenance === null || member.maintenance === undefined) return { label: "unknown", badge: "unknown" };
  return member.maintenance ? { label: "in maintenance", badge: "warning" } : { label: "available", badge: "ok" };
}

// The guest list comes from that host's own report. A host that is not reporting has no guest
// list on the wire, so it reads as unknown rather than as an empty host.
export function guestsText(member: Member) {
  const guests = member.guests;
  if (!guests || !guests.known) return "Guests unknown: this host is not reporting.";
  if (!guests.total) return "No guests on this host.";
  const names = (guests.names || []).join(", ");
  return `${guests.total} guest${guests.total === 1 ? "" : "s"} · ${guests.running} running${names ? ` · ${names}` : ""}`;
}

export function maintenanceInFlight(op?: MemberOperation | null) {
  return Boolean(op) && !["succeeded", "failed", "needs_attention"].includes(op!.status);
}

// The asynchronous 409: Core refused because the membership moved under the request. There is
// nothing to retry blindly, so say what to do instead of offering the same button again.
export function maintenanceResultText(op?: MemberOperation | null) {
  if (!op) return "";
  if (op.conflict) return `${op.error || "Nex Core refused this change."} The pool changed since you loaded it — reload.`;
  if (op.status === "failed") return `Maintenance change failed: ${op.error || "Nex Core reported no message."}`;
  if (op.status === "needs_attention") return `Needs attention: ${op.error || "Nex Core reported no message."}`;
  if (op.status === "succeeded") return "";
  return "Maintenance change submitted. The pool master records it; this shows its own report.";
}

// What a member holds is that member's own fresh report, so it is shown whatever the pool
// master is doing: with the master gone it is the evidence a promotion candidate is chosen on.
// The Controller limits the state itself — current and behind compare against a number only
// the master reports, so without a fresh master they are never claimed.
export function replicaText(member: Member) {
  if (!member.replica) return replicaStates.unknown;
  return replicaStates[member.replica.state] || replicaStates.unknown;
}

// One word cannot say which of the two reports is the older one, so the numbers are shown whenever
// they disagree. Both are host reports; the Controller compares nothing of its own.
export function replicaGenerations(replica?: Replica | null) {
  if (!replica || replica.generation === replica.acknowledged_generation) return "";
  return `This host reports copy ${replica.generation}; the pool master last saw it acknowledge ${replica.acknowledged_generation}. ${replica.generation > replica.acknowledged_generation
    ? "The master's record of this host is the older of the two."
    : "This host has not caught up with what the pool master recorded."}`;
}

// The acknowledgement is the master's observation, so it disappears with the master; what the
// member says it received is its own report and stays.
export function replicaTimes(replica?: Replica | null, authoritative = true) {
  if (!replica) return null;
  const times = [["Received", replica.received_at], ["Acknowledged", authoritative ? replica.acknowledged_at : ""]].filter(([, at]) => at);
  if (!times.length) return null;
  return <small>{times.map(([label, at], index) => <Fragment key={label}>{index > 0 && " · "}{label} <time dateTime={at} title={at}>{relativeTime(at!)}</time></Fragment>)}</small>;
}

// When a host was last heard from, said both ways: an interval an operator reads at a glance,
// and the exact instant the hosts and the audit trail use. `Intl.RelativeTimeFormat` does the
// wording; nothing here rounds a timestamp into a claim about what the host is doing.
export function seenAgo(when: string, now = Date.now()) {
  const at = Date.parse(when);
  if (!Number.isFinite(at)) return "";
  const seconds = Math.round((at - now) / 1000);
  const format = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  for (const [unit, size] of [["day", 86400], ["hour", 3600], ["minute", 60]] as const) {
    if (Math.abs(seconds) >= size) return format.format(Math.round(seconds / size), unit);
  }
  return format.format(seconds, "second");
}

// When the pool master is gone the Hosts view is the recovery view: one plain sentence about
// what is unknown, and nothing that reads an absent report as a dead host. The Controller
// reports when the master last reached it, so the banner says since when instead of only that
// it has stopped; a master that has never reported says exactly that.
export function recoveryText(report: Report, now = Date.now()) {
  const seen = report.master_seen_at
    ? `last reported to this Controller ${seenAgo(report.master_seen_at, now)}, at ${report.master_seen_at}`
    : "has never reported to this Controller";
  return `The pool master ${hostName(report, report.master_host_id)} ${seen}; it is ${report.master_connection}. Its state is unknown — this does not mean its VMs have stopped.`;
}

// The pool's own line about fencing. The count is the pool master's, reported with its
// membership; this only puts words around it. The reasons are in the Fencing details of the
// rows it counts.
export function fencingSummaryText(report: Report) {
  if (!(report.members || []).length) return "";
  if (!report.authoritative) return "Fencing: unknown — the pool master keeps this record and is not reporting. This does not mean these hosts cannot be shut out; it means nothing here knows.";
  const summary = report.fence || null;
  if (!summary) return "Fencing: not reported — this pool master does not report fencing readiness. Upgrade it.";
  if (summary.total <= 1) return "Fencing: this pool has one host. There is nothing to promote it over and no other host to shut it out, so it needs no fence device until a second host joins.";
  const unready = (summary.unready || []).map(id => hostName(report, id));
  const line = `Fencing: ready for ${summary.ready} of ${summary.total} hosts.`;
  if (!unready.length) return `${line} Every host in this pool can be shut out — by another host, or by its own watchdog — so a surviving member can be promoted if the pool master is lost.`;
  return `${line} ${unready.join(", ")} cannot be shut out — the Fencing details on ${unready.length === 1 ? "that row" : "those rows"} say why.`;
}

// The names of Core's prerequisite lines. The sentence beside each one is Core's own and is
// shown as written; this only says which line it is.
const prerequisiteNames: Record<string, string> = {
  hosts: "Hosts",
  watchdogs: "Hardware watchdogs",
  bmc: "Fence devices",
  quorum: "Pool quorum",
};
// Core ends its sentence before naming hosts and puts their IDs beside it, because only a
// client knows their display names — and a host ID is an opaque durable identifier an
// operator would have to look up while the rows above show the name.
export function prerequisiteDetail(report: Report, prerequisite: FencePrerequisite) {
  if (!prerequisite.hosts?.length) return prerequisite.detail;
  return `${prerequisite.detail}: ${prerequisite.hosts.map(id => hostName(report, id)).join(", ")}`;
}

// The pool's own cluster, in the operator's words. Core writes the sentence; this names the
// state and puts the host whose turn it is in the operator's terms rather than as a host ID.
const clusterStateNames: Record<string, string> = {
  off: "off", not_available: "not available", forming: "forming", arming: "arming",
  armed: "armed", degraded: "degraded",
};
const clusterBadges: Record<string, string> = {
  armed: "ok", arming: "pending", forming: "pending", degraded: "warning",
  not_available: "unknown", off: "unknown",
};
export function clusterText(report: Report, cluster: FenceCluster) {
  const counted = `${cluster.armed} of ${cluster.total} hosts armed.`;
  const detail = cluster.detail ? ` ${cluster.detail}` : "";
  if (cluster.state === "arming" && cluster.arming_host_id) {
    return `${counted}${detail} Waiting for ${hostName(report, cluster.arming_host_id)}.`;
  }
  if (cluster.state === "off" || cluster.state === "not_available") return cluster.detail || "No host in this pool is armed.";
  return `${counted}${detail}`;
}

// The Host fencing setting. It is on for every pool that has never been told otherwise, so an
// operator meets the consequence here whether or not they ever touch the switch: a host that
// is armed and loses quorum resets itself, and a switch failure that splits a pool reboots the
// minority side. The checklist under it is the pool master's, not this page's.
export function HostFencingSetting({ report, onChange, busy }: {
  report: Report; onChange?: (enabled: boolean) => void; busy?: boolean;
}) {
  const summary = report.fence || null;
  if (!report.authoritative) {
    return <section className="pool-host-fencing"><h3>Host fencing</h3>
      <p className="help">The pool master keeps this setting and is not reporting, so it is unknown. This does not mean these hosts cannot be shut out; it means nothing here knows.</p>
    </section>;
  }
  if (report.host_fencing_supported === false || !summary) {
    return <section className="pool-host-fencing"><h3>Host fencing</h3>
      <p className="help">This pool master is too old to report the host fencing setting. Upgrade it to see and change it.</p>
    </section>;
  }
  return <section className="pool-host-fencing"><h3>Host fencing</h3>
    <label className="pool-host-fencing-switch">
      <input type="checkbox" checked={summary.host_fencing} disabled={busy || !onChange}
        onChange={event => onChange?.(event.currentTarget.checked)} />
      Let Nex Core arm this pool's hosts to shut themselves out
    </label>
    <p className="help">Turning this on does not arm anything by itself. Nex Core arms a host's watchdog only where every prerequisite below holds.</p>
    <p className="warning" role="note">Once watchdogs are armed, a host that loses this pool's quorum resets itself and its running virtual machines stop. A switch failure that splits a pool therefore reboots the minority side. This pool's cluster runs on the management network, so changing a host's management network, or any work that interrupts it, is refused while that host is armed — put the host in maintenance first. In a three-host pool a second failure while one host is already down reboots the remaining two, because two of three are a minority: for planned work on more than one host, put the first in maintenance mode, which disarms the whole pool.</p>
    {summary.cluster?.witness ? <p className="help pool-host-fencing-witness" role="status">
      <strong>Witness</strong> <Badge value={summary.cluster.witness.state === "operating" ? "ok" : "pending"} label={summary.cluster.witness.state} />
      {summary.cluster.witness.detail ? ` ${summary.cluster.witness.detail}` : ""}
    </p> : null}
    {summary.cluster ? <p className={`help pool-host-fencing-cluster ${clusterBadges[summary.cluster.state] || "unknown"}`} role="status">
      <strong>Watchdog cluster</strong>{" "}
      <Badge value={clusterBadges[summary.cluster.state] || "unknown"} label={clusterStateNames[summary.cluster.state] || summary.cluster.state} />
      {clusterText(report, summary.cluster)}
    </p> : null}
    <ul className="pool-host-fencing-checklist">{(summary.prerequisites || []).map(prerequisite =>
      <li key={prerequisite.name}>
        <Badge value={prerequisite.state} />
        <strong>{prerequisiteNames[prerequisite.name] || prerequisite.name}</strong>
        <small>{prerequisiteDetail(report, prerequisite)}</small>
      </li>)}
    </ul>
  </section>;
}

// One line of the status strip: a light, a short label and a one-word result. The light only
// repeats the words; the full sentence is the item's tooltip and sits in the row's details.
export type StatusCheck = { label: string; result: string; tone: Tone; detail?: string };
function StatusItem({ check }: { check: StatusCheck }) {
  return <span className="pool-check" title={check.detail}><span className={`dot ${check.tone}`} />{check.label} <strong className={check.tone}>{check.result}</strong></span>;
}

// Each member's status strip. Connection, convergence and the copy it holds are the host's own
// report, so a host that is not fresh cannot vouch for them: they read Unknown, never OK.
// Membership, maintenance and fencing are the pool master's records, and read Unknown whenever
// the master is not reporting.
export function memberChecks(member: Member, report: Report): StatusCheck[] {
  const authoritative = report.authoritative;
  const own = (check: StatusCheck): StatusCheck => member.connection !== "fresh" && check.tone === "ok" ? { ...check, result: "Unknown", tone: "neutral" } : check;
  const connection: StatusCheck = member.connection === "fresh" ? { label: "Connection", result: "OK", tone: "ok" }
    : member.connection === "stale" ? { label: "Connection", result: "Stale", tone: "warn" }
      : member.connection === "revoked" ? { label: "Connection", result: "Revoked", tone: "bad" }
        : { label: "Connection", result: "Unknown", tone: "neutral" };
  const drain = maintenanceText(member, authoritative);
  const checks = [connection, membershipCheck(member, authoritative),
    { label: "Maintenance", result: capitalize(drain.label), tone: badgeTone(drain.badge), detail: guestsText(member) }];
  if (report.replica_supported !== false && member.role !== "master") {
    const replica = replicaText(member);
    checks.push(own({ label: "Authority copy", ...replica, detail: behindText(member, report) || member.replica?.error || undefined }));
  }
  checks.push(own(convergenceCheck(member, authoritative)));
  const fencing = fencingText(member, authoritative);
  const fenceResult = !authoritative ? "Unknown" : fencing.label === "not reported" ? "Not reported"
    : fencing.badge === "ok" ? "OK" : fencing.badge === "failed" ? "Failed" : fencing.badge === "warning" ? "Stale" : "Unknown";
  checks.push({ label: "Fencing", result: fenceResult, tone: badgeTone(fencing.badge), detail: capitalize(fencing.label) });
  return checks;
}

function membershipCheck(member: Member, authoritative: boolean): StatusCheck {
  const label = "Membership";
  const detail = [authoritative ? member.error : "", joinProgressText(member.join)].filter(Boolean).join(" ") || undefined;
  // A restriction and a join in progress are the host's own reports, so they outlive the master.
  if (member.restricted) return { label, result: "Restricted", tone: "bad", detail: member.restricted.reason };
  if (member.join?.status === "pending") return { label, result: "Joining", tone: "info", detail };
  if (member.join?.status === "failed") return { label, result: "Failed", tone: "bad", detail };
  if (member.join?.status === "needs_attention") return { label, result: "Needs attention", tone: "warn", detail };
  if (!authoritative) return { label, result: "Unknown", tone: "neutral", detail: "The pool master keeps this record and is not reporting." };
  const tone = badgeTone(member.status || "unknown");
  return { label, result: tone === "ok" ? "OK" : tone === "bad" ? "Failed" : statusLabel(member.status || "unknown"), tone, detail };
}

function convergenceCheck(member: Member, authoritative: boolean): StatusCheck {
  const label = "Convergence";
  const detail = convergenceSummary(member, authoritative);
  // pool-restricted is Membership's; the access figures are the master's observation.
  const checks = (member.checks || []).filter(check => check.name !== "pool-restricted");
  const access = authoritative ? member.access : undefined;
  if (checks.some(check => check.status === "failed") || (authoritative && (member.failures || []).length) || access?.failed) return { label, result: "Failed", tone: "bad", detail };
  if (!checks.length) return { label, result: "Not reported", tone: "neutral", detail };
  if (!access || access.stale || checks.some(check => check.status !== "healthy")) return { label, result: "Unknown", tone: "neutral", detail };
  if (access.pending) return { label, result: "Pending", tone: "warn", detail };
  return { label, result: "OK", tone: "ok", detail };
}

// The pool's own facts on one strip: every value is the pool master's report, and unknown while
// it is not reporting.
export function poolChecks(report: Report): StatusCheck[] {
  const authoritative = report.authoritative;
  const summary = report.fence || null;
  const checks: StatusCheck[] = [report.replica_supported === false
    ? { label: "Authority copies", result: "Not supported", tone: "warn" }
    : { label: "Authority generation", result: authoritative && report.authority ? String(report.authority.generation) : "Unknown", tone: "neutral" }];
  if (!(report.members || []).length) return checks;
  if (!authoritative) return [...checks, { label: "Fencing", result: "Unknown", tone: "neutral" }, { label: "Host fencing", result: "Unknown", tone: "neutral" }];
  if (!summary) checks.push({ label: "Fencing", result: "Not reported (upgrade pool master)", tone: "neutral" });
  else if (summary.total <= 1) checks.push({ label: "Fencing", result: "Not needed (one host)", tone: "neutral" });
  else checks.push({ label: "Fencing", result: `${summary.ready === summary.total ? "OK" : "Not ready"} (${summary.ready} of ${summary.total})`, tone: summary.ready === summary.total ? "ok" : "warn" });
  checks.push(report.host_fencing_supported === false || !summary ? { label: "Host fencing", result: "Not reported", tone: "neutral" }
    : { label: "Host fencing", result: summary.host_fencing ? "On" : "Off", tone: "neutral" });
  if (summary?.cluster?.witness) checks.push({ label: "Witness", result: capitalize(summary.cluster.witness.state), tone: summary.cluster.witness.state === "operating" ? "ok" : "warn", detail: summary.cluster.witness.detail });
  if (summary?.cluster) checks.push({ label: "Watchdog cluster", result: capitalize(clusterStateNames[summary.cluster.state] || summary.cluster.state), tone: badgeTone(clusterBadges[summary.cluster.state] || "unknown"), detail: clusterText(report, summary.cluster) });
  return checks;
}

export function MembersTable({ report, hosts = [], coreAvailable, onMaintenance, busyHost, onMasterChange, onEject, onPromote, onFenceProfile, onBMCProvisioning, bmcBusy, blockedReason, onHostFencing, hostFencingBusy }: {
  report: Report; hosts?: Host[]; coreAvailable?: string; onMaintenance?: (member: Member) => void; busyHost?: string;
  onMasterChange?: (member: Member) => void; onEject?: (member: Member) => void;
  onPromote?: (member: Member) => void; onFenceProfile?: (member: Member) => void;
  onBMCProvisioning?: (member: Member) => void; bmcBusy?: string; blockedReason?: string;
  onHostFencing?: (enabled: boolean) => void; hostFencingBusy?: boolean;
}) {
  const authoritative = report.authoritative;
  const supported = Boolean(report.maintenance_supported);
  // An explicit false means no member can hold a copy at all: drop the check and say it once.
  const replicas = report.replica_supported !== false;
  const members = report.members || [];
  if (!members.length) return <p className="empty">The pool master reports no hosts yet. A host that has just enrolled appears here once its master reports the membership.</p>;
  const actions = Boolean(onMasterChange && onEject);
  return <>
    {/* The pool's facts on one line; the explanations and the Host fencing switch open under it. */}
    <details className="pool-status">
      <summary>{poolChecks(report).map(check => <StatusItem key={check.label} check={check} />)}<span className="pool-more">About</span></summary>
      <div className="pool-status-detail">
        <p className="help">{fencingSummaryText(report)}</p>
        {replicas
          ? <p className="help">{authorityText(report)} Each member keeps a signed, read-only copy of the pool's records so the pool can be recovered if its master is lost; it is never used while the master is healthy.</p>
          : <p className="help">The pool master does not support authority copies, so no member holds one. Upgrade the pool master to give the pool a copy of its records that survives losing the master.</p>}
        <HostFencingSetting report={report} onChange={onHostFencing} busy={hostFencingBusy} />
      </div>
    </details>
    <ul className="pool-members">{members.map(member => {
      // The join stage and the connection are this host's own report, so they survive a
      // master that is gone; the membership status and every observation of it do not.
      const progress = joinProgressText(member.join);
      const failures = authoritative ? member.failures || [] : [];
      const outcome = authoritative ? maintenanceResultText(member.last_maintenance) : "";
      // The Core version is the host's own inventory report, as on the Hosts list.
      const version = hosts.find(h => h.id === member.host_id)?.inventory?.os_version;
      const light = version ? versionLight(version, coreAvailable) : undefined;
      return <li key={member.host_id} className="pool-member" data-host-id={member.host_id}>
        <div className="pool-member-head">
          <a href={`#/hosts/${encodeURIComponent(member.host_id)}`}>{member.name || member.host_id}</a> <Badge value={member.role} label={member.role === "master" ? "Pool master" : "Member"} />
          <small className="pool-version">Nex Core {version?.replace(/^Nex Core /, "") || "not reported"}{light && light.level !== "current" && <> <Badge value="warning" label="Update" /></>}</small>
          {/* When the host joined, as the pool master records it. */}
          <small className="muted">Joined {member.joined_at ? <time dateTime={member.joined_at} title={date(member.joined_at)}>{relativeTime(member.joined_at)}</time> : "—"}</small>
          {actions && <MemberActions report={report} member={member} onMasterChange={() => onMasterChange!(member)} onEject={() => onEject!(member)} blockedReason={blockedReason}
            extra={<>
              {onMaintenance && supported && <button type="button" aria-describedby={blockedReason ? "pool-actions-blocked" : undefined} disabled={!authoritative || Boolean(blockedReason) || busyHost === member.host_id || maintenanceInFlight(member.last_maintenance)} onClick={() => onMaintenance(member)}>{member.maintenance ? "Exit maintenance" : "Enter maintenance"}</button>}
              <PromotionActions report={report} member={member} authoritative={authoritative}
                onPromote={onPromote && (() => onPromote(member))} onFenceProfile={onFenceProfile && (() => onFenceProfile(member))}
                onBMCProvisioning={onBMCProvisioning && (() => onBMCProvisioning(member))} bmcBusy={bmcBusy === member.host_id} />
            </>} />}
        </div>
        {/* A restriction stops the host acting, so its explanation starts open. */}
        <details className="pool-member-more" open={Boolean(member.restricted)}>
          <summary>{memberChecks(member, report).map(check => <StatusItem key={check.label} check={check} />)}<span className="pool-more">Details<span className="sr-only"> for {member.name || member.host_id}</span></span></summary>
          <dl className="pool-member-detail">
            {member.peer_origin && <><dt>Peer address</dt><dd className="mono">{member.peer_origin}</dd></>}
            <dt>Membership</dt>
            <dd>
              <small>{authoritative ? statusLabel(member.status || "unknown") : "Unknown: the pool master keeps this record and is not reporting."}</small>
              {authoritative && member.error && <small className="task-error">{member.error}</small>}
              {progress && <small>{progress}</small>}
              <RestrictedNote report={report} member={member} />
            </dd>
            <dt>Maintenance</dt>
            <dd>
              {authoritative && member.maintenance && member.maintenance_since && <small>Since {member.maintenance_since}</small>}
              <small>{guestsText(member)}</small>
              {onMaintenance && !supported && <small>Maintenance mode needs pool-maintenance-v1 on the pool master; upgrade the pool master.</small>}
            </dd>
            {replicas && <><dt>Authority copy</dt>
              <dd className="pool-replica-cell">
                {member.role === "master" ? <small>Holds the pool records</small> : !member.replica ? <small>Not reported</small> : <>
                  {behindText(member, report) && <small>{behindText(member, report)}</small>}
                  {authoritative && replicaGenerations(member.replica) && <small>{replicaGenerations(member.replica)}</small>}
                  {member.replica.error && <small className="task-error">{member.replica.error}</small>}
                  {replicaTimes(member.replica, authoritative)}
                </>}
              </dd></>}
            <dt>Convergence</dt>
            <dd>
              <small>{convergenceSummary(member, authoritative)}</small>
              {/* The checks are the host's own report and stay readable without a fresh master.
                  pool-restricted is left out: its reason is under Membership already. */}
              {(member.checks || []).filter(check => check.status !== "healthy" && check.name !== "pool-restricted").map(check => <small key={check.name} className={check.status === "failed" ? "task-error" : undefined}>{checkNames[check.name] || checkLabel(check.name)}: {check.status}{check.detail ? ` · ${check.detail}` : ""}</small>)}
              {failures.map(failure => <small key={`${failure.kind}:${failure.id}`} className="task-error">{failure.kind} {failure.name || failure.id}: {failure.error}</small>)}
            </dd>
            {/* How this host would be shut out if it were lost. Every word of it is the pool
                master's own answer; the checks behind it never power anything off. */}
            <dt>Fencing</dt>
            <dd className="pool-fencing-cell"><FencingDetail member={member} authoritative={authoritative} /></dd>
          </dl>
        </details>
        {outcome && <small className={member.last_maintenance?.status === "failed" ? "task-error" : undefined} role={member.last_maintenance?.conflict ? "alert" : undefined}>{outcome}</small>}
      </li>;
    })}</ul>
  </>;
}

export function AddHostDialog({ candidates, loading, error, busy, submitError, submitReady = true, onConfirm, onClose, onRetryCandidates, onEnrollHost }: {
  candidates: Candidate[]; loading: boolean; error: string; busy: boolean; submitError: string; submitReady?: boolean;
  onConfirm: (hostID: string, peerOrigin: string) => void; onClose: () => void; onRetryCandidates: () => void; onEnrollHost?: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [hostID, setHostID] = useState("");
  const [origin, setOrigin] = useState("");
  const [showOriginError, setShowOriginError] = useState(false);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const selected = candidates.find(candidate => candidate.host_id === hostID);
  const peer = peerOriginOf(origin, selected?.peer_port ?? 0);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setShowOriginError(true);
    if (!submitReady || !selected || !selected.eligible || peer.error) return;
    onConfirm(selected.host_id, peer.origin);
  }
  return <dialog ref={dialog} className="confirm-dialog pool-join-dialog" aria-labelledby="add-host-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="add-host-title" ref={heading} tabIndex={-1}>Add host to pool</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    {error && <p className="alert" role="alert">{error} <button type="button" onClick={onRetryCandidates}>Retry</button></p>}
    {/* The model first: operators expect this dialog to take a fresh host's address, and it
        cannot. Enrollment is the site's Add host; this only joins a host that has enrolled. */}
    <p className="help">This pool grows by joining a host already enrolled in this site. Every enrolled host starts as the master of its own pool.</p>
    <form onSubmit={submit}>
      <fieldset disabled={busy}>
        <legend>Host to add</legend>
        {loading && !candidates.length ? <p role="status">Asking the hosts for their current state…</p>
          : !candidates.length ? <div className="empty">
            <p>There is no other enrolled host at this site yet. Enroll the new host first; once it reports, it appears here.</p>
            {onEnrollHost && <button type="button" onClick={onEnrollHost}>Enroll a new host…</button>}
          </div>
            : <ul className="pool-candidates">{candidates.map(candidate => {
              const refusals = (candidate.rows || []).filter(row => row.result === "refuses_join");
              return <li key={candidate.host_id}>
                <label><input type="radio" name="join-candidate" value={candidate.host_id} disabled={!candidate.eligible} checked={hostID === candidate.host_id} onChange={() => { setHostID(candidate.host_id); setOrigin(candidate.peer_address || ""); setShowOriginError(false); }} /> <strong>{candidate.name || candidate.host_id}</strong> · {candidate.connection}{candidate.eligible ? "" : " · cannot join"}</label>
                {refusals.map((row, index) => <small key={`${row.check}-${index}`} className="task-error">{resultNames.refuses_join}: {row.subject || row.check} · {row.detail || "Nex Core refuses this host."}</small>)}
              </li>;
            })}</ul>}
      </fieldset>
      {selected && <div className="table-scroll"><table>
        <caption>Compatibility preview for {selected.name || selected.host_id}. Nex Core checks this again and decides.</caption>
        <thead><tr><th>Check</th><th>Detail</th><th>Result</th></tr></thead>
        <tbody>{(selected.rows || []).map((row, index) => <tr key={`${row.check}-${index}`}>
          <td>{row.check}</td>
          <td>{row.subject}{row.detail ? <small>{row.detail}</small> : null}</td>
          <td><Badge value={resultBadges[row.result]} label={resultNames[row.result]} /></td>
        </tr>)}</tbody>
      </table></div>}
      {selected && (selected.rows || []).some(row => row.result === "will_not_converge") && <p className="warning">Some pool settings will not converge on this host. It can still join; the settings that cannot be applied are reported against it afterwards.</p>}
      {/* The address and the confirm button wait for a chosen host: an empty dialog must
          offer nothing that reads as "type the new host's address here". The address is the
          host's own report, so the usual case is to change nothing. */}
      {selected && <fieldset disabled={busy}>
        <legend>Address of the joining host</legend>
        <label htmlFor="peer-origin">Address</label>
        <input id="peer-origin" required autoComplete="off" placeholder="192.0.2.12" aria-describedby="peer-origin-help" value={origin} onChange={event => { setOrigin(event.target.value); setShowOriginError(false); }} />
        <p className="help" id="peer-origin-help">{selected.peer_address ? "Reported by the host as the address the pool master can reach it at. Change it only if the master must reach this host another way." : "This host does not report its address. Enter the address the pool master uses to reach it."} The master connects to the host itself, so the address must be reachable from the master, not only from this browser.</p>
        {showOriginError && peer.error && <p className="alert" role="alert">{peer.error}</p>}
      </fieldset>}
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        {selected && <button className="primary" disabled={busy || !submitReady || !selected.eligible || !origin.trim()}>{busy ? "Submitting…" : "Add host to pool"}</button>}
      </div>
    </form>
  </dialog>;
}

// Maintenance has no typed confirmation in the contract (Core accepts none); what the operator
// must see before agreeing is the guests that stay running and the plain limits of the flag.
export function MaintenanceDialog({ member, busy, submitError, submitReady, readError = "", onConfirm, onClose }: {
  member: Member; busy: boolean; submitError: string; submitReady: boolean; readError?: string; onConfirm: () => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); }, []);
  const entering = !member.maintenance;
  const name = member.name || member.host_id;
  const guests = member.guests;
  return <dialog ref={dialog} className="confirm-dialog pool-maintenance-dialog" aria-labelledby="maintenance-title"
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="maintenance-title" ref={heading} tabIndex={-1}>{entering ? `Put ${name} into maintenance` : `Take ${name} out of maintenance`}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    {entering ? <>
      <p>Maintenance mode stops the pool master placing new virtual machines on this host and refuses starting a virtual machine here.</p>
      <p className="warning">It does <strong>not</strong> stop, shut down or move any guest that is running now, and it never blocks stopping or shutting one down. A placement the master already accepted finishes. Move or stop the guests below yourself before you work on the host.</p>
    </> : <p>The pool master will place and start virtual machines on this host again. Nothing else changes.</p>}
    <p className={guests && guests.known ? undefined : "warning"}>{guestsText(member)}</p>
    {guests && guests.known && (guests.names || []).length > 0 && <ul className="pool-guests">{(guests.names || []).map(guest => <li key={guest}>{guest}</li>)}</ul>}
    {submitError && <p className="alert" role="alert">{submitError}</p>}
    {readError && <p className="alert" role="alert">The current pool report could not be read: {readError}. Retry the report before submitting.</p>}
    <div className="actions">
      <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
      <button type="button" className="primary" disabled={busy || !submitReady} onClick={onConfirm}>{busy ? "Submitting…" : entering ? "Enter maintenance" : "Exit maintenance"}</button>
    </div>
  </dialog>;
}

export function PoolMembers({ poolID, api, unavailable, hosts, coreAvailable, onEnrollHost }: { poolID: string; api: API; unavailable: boolean; hosts?: Host[]; coreAvailable?: string; onEnrollHost?: () => void }) {
  const [report, setReport] = useState<Report | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [candidatesLoading, setCandidatesLoading] = useState(false);
  const [candidatesError, setCandidatesError] = useState("");
  const [busy, setBusy] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const [notice, setNotice] = useState("");
  const held = useRef({ id: "", signature: "" });
  const reportRequest = useRef(0);
  const reportPending = useRef(0);
  const [drainTarget, setDrainTarget] = useState<Member | null>(null);
  const [drainBusy, setDrainBusy] = useState(false);
  const [drainError, setDrainError] = useState("");
  const heldDrain = useRef({ id: "", signature: "" });
  const [promoteTarget, setPromoteTarget] = useState<Member | null>(null);
  const [ejectTarget, setEjectTarget] = useState<Member | null>(null);
  const [transferBusy, setTransferBusy] = useState(false);
  const [transferError, setTransferError] = useState("");
  const [cancelBusy, setCancelBusy] = useState(false);
  const [cancelError, setCancelError] = useState("");
  const [resumeOpen, setResumeOpen] = useState(false);
  const heldTransfer = useRef({ id: "", signature: "" });
  const heldCancel = useRef({ id: "", signature: "" });
  const [promotionTarget, setPromotionTarget] = useState<Member | null>(null);
  const [verdict, setVerdict] = useState<PromotionVerdict | null>(null);
  const [verdictBusy, setVerdictBusy] = useState(false);
  const [verdictError, setVerdictError] = useState("");
  const [promotionBusy, setPromotionBusy] = useState(false);
  const [promotionError, setPromotionError] = useState("");
  const heldPromotion = useRef({ id: "", signature: "" });
  const [fenceTarget, setFenceTarget] = useState<Member | null>(null);
  const [fenceBusy, setFenceBusy] = useState(false);
  const [fenceError, setFenceError] = useState("");
  const heldFence = useRef({ id: "", signature: "" });
  const heldBMC = useRef({ id: "", signature: "" });
  const [bmcBusy, setBMCBusy] = useState("");
  const [hostFencingBusy, setHostFencingBusy] = useState(false);
  const heldHostFencing = useRef({ id: "", signature: "" });
  const [actionError, setActionError] = useState("");
  async function refresh(force = false) {
    if (reportPending.current && !force) return;
    const request = ++reportRequest.current;
    reportPending.current = request;
    setLoading(true);
    try {
      const next = await api<Report>(`pools/${encodeURIComponent(poolID)}/members`);
      if (request === reportRequest.current) { setReport(next); setError(""); }
    } catch (e) { if (request === reportRequest.current) setError((e as Error).message); }
    finally {
      if (request === reportRequest.current) {
        reportPending.current = 0;
        setLoading(false);
      }
    }
  }
  async function loadCandidates() {
    setCandidatesLoading(true); setCandidatesError("");
    try { setCandidates((await api<{ candidates?: Candidate[] }>(`pools/${encodeURIComponent(poolID)}/join-candidates`)).candidates || []); }
    catch (e) { setCandidatesError((e as Error).message); }
    finally { setCandidatesLoading(false); }
  }
  useEffect(() => {
    const catchUp = () => { if (document.visibilityState === "visible") void refresh(); };
    catchUp();
    const timer = window.setInterval(catchUp, 10_000);
    document.addEventListener("visibilitychange", catchUp);
    window.addEventListener("online", catchUp);
    return () => {
      ++reportRequest.current;
      reportPending.current = 0;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", catchUp);
      window.removeEventListener("online", catchUp);
    };
  }, [poolID]);
  // While this panel is open, keep asking the master so idle settings, delayed Core results,
  // and changes made elsewhere appear without an operator refresh.
  const transferring = transferInProgress(report?.transfer) || (report?.members || []).some(member => transferInProgress(member.transfer));
  const blockedReason = transferring
    ? "A master change or eject is running on this pool. The pool master refuses every other pool authority change until it completes; retry when it does."
    : "";
  async function confirm(hostID: string, peerOrigin: string) {
    setBusy(true); setSubmitError("");
    const request_id = heldRequestID(held.current, `${poolID}\n${hostID}\n${peerOrigin}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/members`, "POST", { request_id, host_id: hostID, peer_origin: peerOrigin });
      setOpen(false);
      setNotice("Nex Core accepted the request. The joining host performs the join itself and reports its progress below; closing this page does not stop it.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      // The same request_id is kept, so pressing the button again replays this request instead of
      // asking for a second join.
      setSubmitError(`${(e as Error).message} Submitting again repeats the same request; it never starts a second join.`);
    } finally { setBusy(false); }
  }
  // The membership generation the operator is looking at. Core refuses the command if the master
  // has moved on, so the reload is forced rather than the change landing on a different pool.
  async function setMaintenance(member: Member) {
    const enabled = !member.maintenance;
    setDrainBusy(true); setDrainError("");
    const request_id = heldRequestID(heldDrain.current, `${poolID}\n${member.host_id}\n${enabled}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/members/${encodeURIComponent(member.host_id)}/maintenance`, "POST", { request_id, enabled, expected_version: report?.generation || 0 });
      setDrainTarget(null);
      setNotice(enabled
        ? "Nex Core accepted the request. The pool master records maintenance; guests already running on the host are untouched."
        : "Nex Core accepted the request. The pool master will place and start virtual machines on this host again.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setDrainError(`${(e as Error).message} Submitting again repeats the same request; it never records a second change.`);
    } finally { setDrainBusy(false); }
  }
  // A master change and an eject are one durable command to the pool master, with the loaded
  // membership generation so the change cannot land on a pool that moved underneath it.
  async function changeMaster(member: Member, confirmation: string) {
    setTransferBusy(true); setTransferError("");
    const request_id = heldRequestID(heldTransfer.current, `${poolID}\nmaster-change\n${member.host_id}\n${confirmation}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/master`, "POST", { request_id, host_id: member.host_id, expected_version: report?.generation || 0, confirmation });
      setPromoteTarget(null);
      setNotice("Nex Core accepted the request. The pool master runs the change itself and every host reports its own part; closing this page does not stop it.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setTransferError(`${(e as Error).message} Submitting again repeats the same request; it never starts a second master change.`);
    } finally { setTransferBusy(false); }
  }
  async function eject(member: Member, confirmation: string) {
    setTransferBusy(true); setTransferError("");
    const request_id = heldRequestID(heldTransfer.current, `${poolID}\neject\n${member.host_id}\n${confirmation}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/members/${encodeURIComponent(member.host_id)}`, "DELETE", { request_id, expected_version: report?.generation || 0, confirmation });
      setEjectTarget(null);
      setNotice("Nex Core accepted the request. The pool master decides whether the host can leave; if it still owns pool records or a storage reservation, Core's refusal appears under Pool master change.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setTransferError(`${(e as Error).message} Submitting again repeats the same request; it never starts a second eject.`);
    } finally { setTransferBusy(false); }
  }
  // One route for both: the plain cancel, and the fence-backed resume Core asks for when the
  // hand-off target has not answered. The resume carries the operator's separate power-off
  // acknowledgement and the exact host Core named, so it is a different request identity.
  async function cancelTransfer(fenceTargetID?: string) {
    const target = report?.transfer?.target_host_id || "";
    setCancelBusy(true); setCancelError("");
    const request_id = heldRequestID(heldCancel.current, `${poolID}\ntransfer-cancel\n${target}\n${fenceTargetID || ""}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/master/cancel`, "POST", fenceTargetID
        ? { request_id, host_id: target, expected_version: report?.generation || 0, fence_confirmed: true, fence_host_ids: [fenceTargetID] }
        : { request_id, host_id: target, expected_version: report?.generation || 0 });
      setResumeOpen(false);
      setNotice(fenceTargetID
        ? "Nex Core accepted the request. It powers that host off and verifies it before it resumes this pool's authority; if the host answers first, Core cancels the master change with that answer instead."
        : "Nex Core accepted the cancel. If the new master has already taken the pool authority, Core refuses it and the change goes forward.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setCancelError(`${(e as Error).message} Submitting again repeats the same request.`);
    } finally { setCancelBusy(false); }
  }
  // The preflight stores, journals and changes nothing on any host, so it is safe to repeat.
  // Its refusals — including the 504 when the candidate has not answered yet — are shown in
  // Core's own words, because they are the reasons the promotion is stopped.
  async function preflight(member: Member) {
    setVerdictBusy(true); setVerdictError("");
    try {
      const answer = await api<{ promotion?: PromotionVerdict }>(`pools/${encodeURIComponent(poolID)}/master/promote/preflight`, "POST", { host_id: member.host_id });
      setVerdict(answer.promotion || null);
    } catch (e) {
      setVerdictError((e as Error).message);
    } finally { setVerdictBusy(false); }
  }
  // One durable command to the candidate, which performs the promotion itself. The typed pool
  // name and the power-off acknowledgement are both preconditions; Nex Core proves everything
  // again and produces the fence proof itself.
  // `fence_host_ids` is Core's own list from the verdict the operator confirmed, sent back
  // unchanged. Core computes it again when the promotion runs and refuses a list that no
  // longer matches, so a host is never powered off without having been named here first.
  async function promote(member: Member, confirmation: string, fenceHostIDs: string[]) {
    setPromotionBusy(true); setPromotionError("");
    const request_id = heldRequestID(heldPromotion.current, `${poolID}\npromote\n${member.host_id}\n${confirmation}\n${fenceHostIDs.join(",")}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/master/promote`, "POST", { request_id, host_id: member.host_id, confirmation, fence_confirmed: true, fence_host_ids: fenceHostIDs });
      setPromotionTarget(null);
      setNotice("Nex Core accepted the request. The candidate runs the promotion itself and reports its own stage; closing this page does not stop it.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setPromotionError(`${(e as Error).message} Submitting again repeats the same request; it never starts a second promotion.`);
    } finally { setPromotionBusy(false); }
  }
  // The fence device is a field of the membership the pool master owns, so this one goes to
  // the master. The credential travels in its own field and is never stored by this
  // Controller; a null profile clears the record.
  async function setFenceProfile(member: Member, fence: FenceProfileInput | null, credential?: FenceCredentialInput) {
    setFenceBusy(true); setFenceError("");
    const request_id = heldRequestID(heldFence.current, `${poolID}\nfence-profile\n${member.host_id}\n${JSON.stringify(fence)}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/members/${encodeURIComponent(member.host_id)}/fence-profile`, "POST", credential ? { request_id, fence, credential } : { request_id, fence });
      setFenceTarget(null);
      setNotice(fence
        ? "Nex Core accepted the request. The pool master records the fence device and every member receives it with its copy of the pool records; if this host's Nex Core cannot verify that kind of device it refuses it and says so on this page."
        : "Nex Core accepted the request. The pool master is clearing the fence device for this host.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setFenceError(`${(e as Error).message} Submitting again repeats the same request; it never records a second change.`);
    } finally { setFenceBusy(false); }
  }
  // Detect asks the pool master and waits for its answer; nothing is recorded, so each Detect is
  // a fresh request and the password is handed over again.
  function detectBMC(member: Member, endpoint: string, username: string, secret: string) {
    const request_id = crypto.randomUUID();
    return api<BMCProbeView>(`pools/${encodeURIComponent(poolID)}/members/${encodeURIComponent(member.host_id)}/bmc-probe`, "POST",
      username ? { request_id, endpoint, username, credential: { secret } } : { request_id, endpoint });
  }
  // Host fencing is one pool setting, and the pool master keeps it. Turning it off needs no
  // confirmation: it only stops Nex Core arming anything, and the consequence of leaving it on
  // is on the page beside the switch.
  async function setHostFencing(enabled: boolean) {
    setActionError(""); setNotice(""); setHostFencingBusy(true);
    const request_id = heldRequestID(heldHostFencing.current, `${poolID}\nhost-fencing\n${enabled}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/host-fencing`, "POST", { request_id, enabled });
      setNotice(enabled
        ? "Nex Core accepted the request. The pool master records host fencing as on; it arms a host's watchdog only where every prerequisite holds."
        : "Nex Core accepted the request. The pool master records host fencing as off, so no host is armed to reset itself.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setActionError(`${(e as Error).message} Submitting again repeats the same request; it never records a second change.`);
    } finally { setHostFencingBusy(false); }
  }
  // Whether a host may configure its own management controller is a field of the membership
  // the pool master owns, so this one goes to the master. There is no "off" request: clearing
  // the fence device is what turns it off.
  async function setBMCProvisioning(member: Member) {
    setActionError(""); setNotice(""); setBMCBusy(member.host_id);
    const request_id = heldRequestID(heldBMC.current, `${poolID}\nbmc-provisioning\n${member.host_id}`);
    try {
      await api(`pools/${encodeURIComponent(poolID)}/members/${encodeURIComponent(member.host_id)}/bmc-provisioning`, "POST", { request_id });
      setNotice("Nex Core accepted the request. That host configures its management controller again on its next pass and reports what it finds.");
      await refresh(true);
    } catch (e) {
      void refresh(true);
      setActionError(`${(e as Error).message} Submitting again repeats the same request; it never makes a second change.`);
    } finally { setBMCBusy(""); }
  }
  const authoritative = Boolean(report?.authoritative);
  const writesDisabled = unavailable || Boolean(error);
  return <Section icon="host" title="Members" count={report?.members?.length} actions={<>
    {report ? !authoritative && <Badge value="stale" label="Not current" /> : <span className="muted">{loading ? "Loading…" : "Unavailable"}</span>}
    <button type="button" className="sm primary" aria-describedby={blockedReason ? "pool-actions-blocked" : undefined} disabled={writesDisabled || !report || Boolean(blockedReason)} onClick={() => { setSubmitError(""); setNotice(""); setOpen(true); void loadCandidates(); }}>Add host to pool</button>
    <button type="button" className="sm" disabled={loading} onClick={() => void refresh(true)}>{loading ? "Refreshing…" : "Refresh"}</button>
  </>}>
    {blockedReason && <p className="warning" id="pool-actions-blocked" role="status">{blockedReason}</p>}
    {notice && <p className="notice" role="status">{notice}</p>}
    {/* A row or panel action has no dialog to show its refusal in, so Core's sentence lands here. */}
    {actionError && <p className="alert" role="alert">{actionError}</p>}
    {error ? <p className="alert" role="alert">The pool master could not be asked for its membership: {error}</p>
      : report && !authoritative ? <div className="warning pool-recovery" role="status">
        <p>{recoveryText(report)}</p>
        <p>What the pool master keeps — each host's membership, its maintenance flag, its fence device and everything the master observed — is shown as unknown below. What each host reports about itself is still its own, and is shown.</p>
        <p>If the pool master is gone for good, a surviving member that holds a copy of the pool records it has verified can be promoted from its row.</p>
      </div>
        : null}
    {report && !writesDisabled && <TransferStrip report={report} onCancel={() => void cancelTransfer()} cancelBusy={cancelBusy} cancelError={cancelError}
      onResume={() => { setCancelError(""); setNotice(""); setResumeOpen(true); }} />}
    {report && <PromotionStrip report={report} />}
    {report ? <MembersTable report={report} hosts={hosts} coreAvailable={coreAvailable} busyHost={drainTarget?.host_id} blockedReason={blockedReason}
      onMaintenance={writesDisabled ? undefined : member => { setDrainError(""); setNotice(""); setDrainTarget(member); }}
      onMasterChange={writesDisabled ? undefined : member => { setTransferError(""); setNotice(""); setPromoteTarget(member); }}
      onEject={writesDisabled ? undefined : member => { setTransferError(""); setNotice(""); setEjectTarget(member); }}
      onPromote={writesDisabled ? undefined : member => { setPromotionError(""); setVerdictError(""); setVerdict(null); setNotice(""); setPromotionTarget(member); void preflight(member); }}
      onFenceProfile={writesDisabled ? undefined : member => { setFenceError(""); setNotice(""); setFenceTarget(member); }}
      onBMCProvisioning={writesDisabled ? undefined : member => void setBMCProvisioning(member)} bmcBusy={bmcBusy}
      onHostFencing={writesDisabled ? undefined : enabled => void setHostFencing(enabled)} hostFencingBusy={hostFencingBusy} /> : loading ? <p className="empty" role="status">Asking the pool master for its membership…</p> : <p className="empty">The pool master has not reported a membership.</p>}
    {drainTarget && <MaintenanceDialog member={drainTarget} busy={drainBusy} submitError={drainError || (error ? `The current pool report could not be read: ${error}. Retry the report before submitting.` : "")} submitReady={membershipVersionReady(report) && !writesDisabled} onConfirm={() => { if (!writesDisabled) void setMaintenance(drainTarget); }} onClose={() => { if (!drainBusy) setDrainTarget(null); }} />}
    {report && promoteTarget && <MasterChangeDialog report={report} member={promoteTarget} busy={transferBusy} submitError={transferError || (error ? `The current pool report could not be read: ${error}. Retry the report before submitting.` : "")} submitReady={membershipVersionReady(report) && !writesDisabled} onConfirm={confirmation => { if (!writesDisabled) void changeMaster(promoteTarget, confirmation); }} onClose={() => { if (!transferBusy) setPromoteTarget(null); }} />}
    {report && ejectTarget && <EjectDialog report={report} member={ejectTarget} busy={transferBusy} submitError={transferError || (error ? `The current pool report could not be read: ${error}. Retry the report before submitting.` : "")} submitReady={membershipVersionReady(report) && !writesDisabled} onConfirm={confirmation => { if (!writesDisabled) void eject(ejectTarget, confirmation); }} onClose={() => { if (!transferBusy) setEjectTarget(null); }} />}
    {report && promotionTarget && <PromotionDialog report={report} member={promotionTarget} verdict={verdict} verdictBusy={verdictBusy} verdictError={verdictError} readError={error}
      onPreflight={() => void preflight(promotionTarget)} busy={promotionBusy} submitError={promotionError}
      onConfirm={(confirmation, fenceHostIDs) => void promote(promotionTarget, confirmation, fenceHostIDs)} onClose={() => { if (!promotionBusy) { setPromotionTarget(null); setVerdict(null); } }} />}
    {report && resumeOpen && transferResumePrompt(report) && <ResumeDialog report={report} prompt={transferResumePrompt(report)} busy={cancelBusy} submitError={cancelError || (error ? `The current pool report could not be read: ${error}. Retry the report before submitting.` : "")} submitReady={membershipVersionReady(report) && !writesDisabled}
      onConfirm={targetHostID => void cancelTransfer(targetHostID)} onClose={() => { if (!cancelBusy) setResumeOpen(false); }} />}
    {report && fenceTarget && <FenceProfileDialog report={report} member={fenceTarget} busy={fenceBusy} submitError={fenceError} readError={error}
      onConfirm={(fence, credential) => void setFenceProfile(fenceTarget, fence, credential)}
      onClear={() => void setFenceProfile(fenceTarget, null)} onClose={() => { if (!fenceBusy) setFenceTarget(null); }}
      onDetect={(endpoint, username, secret) => detectBMC(fenceTarget, endpoint, username, secret)} />}
    {open && <AddHostDialog candidates={candidates} loading={candidatesLoading} error={candidatesError} busy={busy} submitError={submitError || (error ? `The current pool report could not be read: ${error}. Retry the report before submitting.` : "")} submitReady={!writesDisabled} onConfirm={confirm} onClose={() => { if (!busy) setOpen(false); }} onRetryCandidates={() => void loadCandidates()}
      onEnrollHost={onEnrollHost && (() => { setOpen(false); onEnrollHost(); })} />}
  </Section>;
}
