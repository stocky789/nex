// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent, type InputHTMLAttributes } from "react";
import type { Host, Storage, ZFSPoolStatus } from "./managed";
import { ZFSProvisionWizard, ZFSReplaceDialog, zfsFailedDevices, zfsReplaceableDevices } from "./zfs";
import { About, AccessLights, Badge, MiniBar, Section, badgeTone, size, type HostAccess } from "./ui";
import { relativeTime } from "./labels";

type API = <T>(path: string, method?: string, body?: unknown, progress?: (loaded: number, total: number) => void, register?: (xhr: XMLHttpRequest) => void, signal?: AbortSignal) => Promise<T>;
type Props = { poolID: string; masterHostID: string; hosts: Host[]; storage: Storage[]; api: API; reload: () => Promise<void>; unavailable: boolean; onNotice: (message: string) => void };
type Kind = "local" | "nfs" | "zfs" | "smb" | "iscsi";
type Draft = { name: string; host_id: string; kind: Kind; path: string; server: string; export: string; nfs_directory: string; pool: string; dataset: string; share: string; directory: string; portal: string; target_iqn: string; lun: string; wwid: string; filesystem_uuid: string; exclusive_target: boolean; iscsi_mode: "exclusive" | "clustered_gfs2"; username: string; password: string; domain: string };
type Discovery = { status: string; error?: string; candidates?: { kind: Kind; source?: ReturnType<typeof source>; path?: string; browsable: boolean; existing?: boolean }[] };
type Candidate = NonNullable<Discovery["candidates"]>[number];
type Browser = { kind: "nfs" | "smb"; path: string; base: string; context: string; candidates: Candidate[]; selected?: Candidate; loading: boolean; error: string };
type ClusterMember = { cluster_id: string; repository_id: string; status: "unconfigured" | "pending" | "blocked" | "healthy" | "failed"; healthy: boolean; quorate: boolean; mounted: boolean; fencing_verified: boolean; fences?: { host_id: string; status: "unverified" | "passed" | "failed" | "uncertain"; exclusion_verified: boolean; error?: string; observed_at?: string }[]; error?: string };
type ClusterFence = { kind: "redfish" | "ipmilan" | "virsh" | "scsi_pr"; endpoint?: string; target_id?: string; device_path?: string; virsh_uri?: string };
type ClusterFenceProof = { request_id?: string; host_id: string; status: "unverified" | "passed" | "failed" | "uncertain"; target_id?: string; exclusion_verified: boolean; observed_at?: string; error?: string };
type ClusterMemberReport = { host_id: string; status: ClusterMember; generation: number; membership_generation: number; cluster_id: string; config_digest: string; stage: string; updated_at?: string };
type ClusterOperation = { request_id: string; membership_generation: number; config: { cluster_id: string; repository_id: string; generation: number; lock_table: string; device_path?: string; mount_path?: string; nodes: { host_id: string; corosync_node_id: number; corosync_address: string; fence: ClusterFence }[]; fence_proofs?: ClusterFenceProof[] }; status: string; error?: string; updated_at?: string; members?: ClusterMemberReport[] };
type ClusterResult = { request_id?: string; pool_id?: string; host_id?: string; status: "succeeded" | "failed" | "needs_attention" | "stale"; error?: string; observed_at?: string; operations?: ClusterOperation[] };
type ZFSOutcome = { repository_id: string; pool_id: string; name: string; scope: string; host_id: string; layout: string; status: string; error?: string };

// A registration the pool master refused is only this Controller's record of a failed request:
// no host holds the resource, so discarding the row asks no host to do anything. This is the
// failed-ISO-import model applied to network and storage registrations. A failed typed storage
// registration answers its Controller row key (`pending-storage-<operation id>`) as its id, which
// is the handle the discard route takes; it is never a Core repository id, so no repository
// action is offered on such a row. Lives here because managed.tsx already imports from storage.
export const registrationDiscardable = (r: { id: string; status: string }) => r.status === "failed" && Boolean(r.id);
export const discardConfirmText = (kind: string, name: string) =>
  `Discard the failed ${kind} registration ${name}? This removes only Nex Controller's record of the failed request and asks no host to do anything. The task and audit history are kept.`;

export const clusterStatusView = (loading: boolean, error: string, status: ClusterResult["status"] | undefined, operations: number) => {
  if (loading && !status) return "checking";
  if (error || status === "stale" || (!status && !loading)) return "unavailable";
  if (operations) return "configured";
  return status === "succeeded" ? "empty" : "unavailable";
};
export const clusterSetupAvailable = (view: ReturnType<typeof clusterStatusView>) => view === "empty" || view === "configured";

const blank: Draft = { name: "", host_id: "", kind: "local", path: "", server: "", export: "", nfs_directory: "", pool: "", dataset: "", share: "", directory: "", portal: "", target_iqn: "", lun: "", wwid: "", filesystem_uuid: "", exclusive_target: false, iscsi_mode: "exclusive", username: "", password: "", domain: "" };
const labels: Record<Kind, string> = { local: "Local directory", nfs: "NFSv4 export", zfs: "OpenZFS dataset", smb: "SMB3 share", iscsi: "iSCSI LUN" };
function fenceProofState(proof: ClusterFenceProof) {
  if (proof.status === "passed" && proof.exclusion_verified) return "Core recorded the target as excluded.";
  if (proof.status === "unverified") return "Core is reconciling this requested proof; it is not safe to retry.";
  return "Core recorded this outcome. A new verification requires explicit confirmation.";
}
function fenceProofTitle(proof?: ClusterFenceProof, ready = false, outcomeUnknown = false) {
  if (!ready) return "Wait for Core to derive cluster metadata before destructive fencing.";
  if (outcomeUnknown) return "Fence outcome is unknown. Refresh Core status before considering another verification.";
  if (!proof) return undefined;
  return proof.status === "unverified" ? "Core is reconciling the existing fence request; Nex will not retry it automatically." : "Starts a new destructive verification; Core retains the previous proof.";
}
const needsCredential = (kind: Kind, d?: Draft) => kind === "smb" || (kind === "iscsi" && Boolean(d?.username || d?.password));
function smbCredentials(d: Draft) {
  return { username: d.username, secret: d.password, ...(d.domain.trim() ? { domain: d.domain.trim() } : {}) };
}
// Core's own observation timestamp decides whether a report is current access: a report older
// than 30 seconds, undated, unparseable or dated in the future is not current.
export const storageObservationFresh = (observedAt?: string) => {
  const checked = observedAt ? new Date(observedAt).valueOf() : NaN;
  return Number.isFinite(checked) && checked <= Date.now() && Date.now() - checked <= 30_000;
};
// Core's own rule (storageServesData): a pool that lost a disk still serves every VM on it,
// so a degraded repository stays usable and is shown as needing attention, not as broken.
export const storageServesData = (status?: string) => status === "healthy" || status === "degraded";
export const storageUsable = (s: Storage, hostID: string) => {
  if (s.kind === "zfs" && s.zfs_provisioned) {
    const access = s.zfs_host_access?.find(item => item.host_id === hostID);
    return s.status === "succeeded" && s.capabilities?.vm_disk === true && Boolean(access && access.readable && access.writable && !access.error && storageObservationFresh(access.observed_at));
  }
  const base = s.status === "succeeded" && s.access?.readable === true && s.access?.writable === true && storageServesData(s.access?.status) && s.capabilities?.vm_disk === true;
  if (!base) return false;
  if (!sharedStorageProfile(s)) return s.host_id === hostID;
  if (clusteredGFS2Profile(s) && !sharedStorageEligible(s, hostID)) return false;
  if (s.host_id === hostID) {
    const eligible = s.capabilities?.eligible_host_ids;
    return !eligible || eligible.includes(hostID);
  }
  return s.member_access?.some(access => access.host_id === hostID && memberAccessState(s, access) === "accessible") === true;
};
function sharedStorageProfile(s: Storage) {
  return s.kind === "nfs" || s.kind === "smb" || clusteredGFS2Profile(s);
}
// Pool-level registration never makes a local disk shared: only NFS, SMB and clustered GFS2 are.
export function storageScope(s: Storage) {
  if (sharedStorageProfile(s)) return "Shared";
  return s.kind === "zfs" && s.zfs_provisioned && s.zfs_scope === "pool" ? "Per host" : "One host";
}
function clusteredGFS2Profile(s: Storage) {
  return s.kind === "iscsi" && s.source?.iscsi?.mode === "clustered_gfs2";
}
function sharedStorageEligible(s: Storage, hostID: string) {
  if (s.source?.iscsi?.mode === "clustered_gfs2") return s.source.iscsi.eligible_host_ids?.includes(hostID) === true;
  // NFS/SMB member eligibility is the matching Core observation below. Their
  // repository capability describes the master that authored the registration.
  return true;
}
function memberAccessState(s: Storage, access: NonNullable<Storage["member_access"]>[number]) {
  if (access.repository_id !== s.id || access.generation !== s.generation || !access.identity) return "pending";
  if (access.error) return access.error;
  if (!storageObservationFresh(access.observed_at)) return "stale";
  return access.readable && access.writable && access.qemu_writable && access.locking ? "accessible" : "not accessible";
}
export function storageSource(s: Storage) {
  const source = s.source;
  if (source?.nfs) return `${source.nfs.server}:${source.nfs.export}${source.nfs.directory ? `/${source.nfs.directory}` : ""}`;
  if (source?.smb) return `//${source.smb.server}/${source.smb.share}${source.smb.directory ? `/${source.smb.directory}` : ""}`;
  if (source?.zfs) return source.zfs.dataset === source.zfs.pool || source.zfs.dataset.startsWith(`${source.zfs.pool}/`) ? source.zfs.dataset : `${source.zfs.pool}/${source.zfs.dataset}`;
  if (source?.iscsi) return `${source.iscsi.target_iqn} · LUN ${source.iscsi.lun}`;
  return s.path || "Source unavailable";
}
export function storageChecked(value?: string) {
  if (!value) return "Not checked";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) || date.getUTCFullYear() <= 1 ? "Not checked" : `Checked ${date.toLocaleString()}`;
}
// ZFS reports scan sizes already formatted ("129M", "4.89G"), so the percentage an operator
// actually wants is read back from those strings rather than recomputed from raw bytes.
export const zfsSizeBytes = (value?: string) => {
  const parsed = /^\s*([\d.]+)\s*([KMGTPE]?)/.exec(value || "");
  if (!parsed) return NaN;
  return Number(parsed[1]) * 1024 ** "BKMGTPE".indexOf(parsed[2] || "B");
};
// A running scan is shown with its percentage; a finished one with how it ended.
// The parenthetical uses the same quantity the percentage was derived from (issued when
// present, otherwise examined), so the two halves of the sentence cannot disagree.
export function zfsScanProgress(scan: NonNullable<ZFSPoolStatus["scan"]>) {
  const name = scan.function.toLowerCase();
  if (scan.state !== "SCANNING") return `last ${name} ${scan.state.toLowerCase()}`;
  const quantity = scan.issued ?? scan.examined;
  const done = zfsSizeBytes(quantity), total = zfsSizeBytes(scan.to_examine);
  const percent = done >= 0 && total > 0 ? Math.min(100, Math.floor((done / total) * 100)) : NaN;
  return `${name} in progress${Number.isFinite(percent) ? ` · ${percent}%` : ""} (${quantity ?? "?"} of ${scan.to_examine ?? "?"})`;
}
// Non-zero ZFS leaf counters are attention, even while the leaf stays ONLINE.
const zfsDeviceErrorParts = (device: NonNullable<ZFSPoolStatus["devices"]>[number]) => {
  const parts: string[] = [];
  if (device.read_errors && device.read_errors !== "0") parts.push(`${device.read_errors} read errors`);
  if (device.write_errors && device.write_errors !== "0") parts.push(`${device.write_errors} write errors`);
  if (device.checksum_errors && device.checksum_errors !== "0") parts.push(`${device.checksum_errors} checksum errors`);
  return parts;
};
// Core reports the pool itself, so a repository that lost a disk names it and shows the
// resilver that is putting it right, instead of only a status word. Error counts and a
// non-empty ZFS status sentence also raise attention, even when every leaf is ONLINE.
export function zfsPoolSummary(pool: ZFSPoolStatus) {
  const failed = zfsFailedDevices(pool);
  const failedNames = new Set(failed.map(item => item.device));
  const parts = [`Pool ${pool.state.toLowerCase()}`, ...failed.map(item => `${item.device} ${item.state.toLowerCase()}`)];
  for (const device of pool.devices || []) {
    const errors = zfsDeviceErrorParts(device);
    if (!errors.length) continue;
    const name = device.path || device.name;
    if (failedNames.has(name)) parts.push(...errors);
    else parts.push(name, ...errors);
  }
  if (pool.scan) parts.push(zfsScanProgress(pool.scan));
  const attention = pool.state !== "ONLINE" || failed.length > 0 || Boolean(pool.status) || (pool.devices || []).some(device => zfsDeviceErrorParts(device).length > 0);
  return { text: parts.join(" · "), attention };
}
// Status and action sentences are visible text, not a hover-only title; the summary line wraps
// so a long by-id degraded string does not force the table wide (SR-056).
function ZFSPoolLines({ pool, prefix }: { pool: ZFSPoolStatus; prefix?: string }) {
  const summary = zfsPoolSummary(pool);
  const detail = [pool.status, pool.action].filter(Boolean).join(" ");
  const text = prefix ? `${prefix} · ${summary.text}` : summary.text;
  return <><small className={summary.attention ? "badge warn" : undefined} style={{ whiteSpace: "normal" }}>{text}</small>{detail ? <small>{detail}</small> : null}</>;
}
// One access light per pool host, from what Core reports for that host now. A host that is not
// reporting, or a report that is missing or old, is Unknown and never shown as accessible.
export function storageHostAccess(s: Storage, hosts: Host[]): HostAccess[] {
  const owner = hosts.find(h => h.id === s.host_id)?.hostname || s.host_id;
  const provisioned = s.kind === "zfs" && s.zfs_provisioned;
  return hosts.map(host => {
    const light = (tone: HostAccess["tone"], label: string, detail = "") => ({ host, tone, label, detail });
    // Storage that is not shared is on one host; the others have no access by design.
    if (host.id !== s.host_id && (provisioned ? s.zfs_scope !== "pool" : !sharedStorageProfile(s))) return light("neutral", "No access", `Local to ${owner}; this storage is not shared.`);
    if (!sharedStorageEligible(s, host.id)) return light("neutral", "Not eligible", "This host is not in the clustered storage's member set.");
    if (host.revoked || host.connection !== "fresh") return light("unknown", "Unknown", "The host is not reporting, so its access is not current.");
    if (provisioned) {
      const a = s.zfs_host_access?.find(item => item.host_id === host.id);
      if (!a) return light("unknown", "Unknown", "No ZFS access reported for this host.");
      if (a.error) return light("bad", "Error", a.error);
      if (!storageObservationFresh(a.observed_at)) return light("unknown", "Unknown", `Last report is not current · ${storageChecked(a.observed_at)}.`);
      const free = a.available_bytes === undefined ? "" : ` · ${size(a.available_bytes)} free`;
      return a.readable && a.writable ? light("ok", "Accessible", storageChecked(a.observed_at) + free) : light("bad", "No access", `${a.readable ? "Readable" : "Not readable"} · ${a.writable ? "writable" : "not writable"} · ${storageChecked(a.observed_at)}.`);
    }
    if (host.id === s.host_id) {
      // Core reports healthy, degraded, unavailable or unknown; anything else is not a report of access.
      const a = s.access;
      if (a?.status === "unavailable") return light("bad", "No access", a.error || storageChecked(a.observed_at));
      if (!a || !storageServesData(a.status)) return light("unknown", "Unknown", a?.error || "Core has not reported current access.");
      if (!a.readable || !a.writable) return light("bad", "No access", `${a.readable ? "Readable" : "Not readable"} · ${a.writable ? "writable" : "not writable"} · ${storageChecked(a.observed_at)}.`);
      return a.status === "degraded" ? light("warn", "Degraded", storageChecked(a.observed_at)) : light("ok", "Accessible", storageChecked(a.observed_at));
    }
    const member = s.member_access?.find(item => item.host_id === host.id);
    if (!member) return light("unknown", "Unknown", "The pool master has not reported this host's access.");
    const state = memberAccessState(s, member);
    return state === "accessible" ? light("ok", "Accessible", storageChecked(member.observed_at))
      : state === "pending" ? light("warn", "Pending", "Waiting for this host to check the current registration.")
      : state === "stale" ? light("unknown", "Unknown", `Last report is not current · ${storageChecked(member.observed_at)}.`)
      : state === "not accessible" ? light("bad", "No access", "Core found the read, write, QEMU or locking check failing on this host.")
      : light("bad", "Error", state);
  });
}
// A v2 repository exists only after Core assigns an ID/generation.  A queued browser request
// cannot safely be rechecked, updated or detached yet; Core owns the eventual identity.
export const storageLifecycleReady = (s: Storage) => !s.kind ? s.status === "succeeded" : s.status === "succeeded" && (s.generation || 0) > 0;
export const storageAccess = (s: Storage) => s.access?.status || s.status || "unknown";
// Repository access is an observation from its assigned Core. Keep the last report
// visible after a disconnect, but never present it as current access or offer an
// operation that requires that Core to accept a command.
export const storageOwnerFresh = (s: Storage, hosts: Host[]) => {
  const host = hosts.find(item => item.id === s.host_id);
  return Boolean(host && !host.revoked && host.connection === "fresh" && host.capabilities?.includes("managed-vm-v1"));
};

function source(d: Draft) {
  switch (d.kind) {
    case "local": return undefined;
    case "nfs": return { nfs: { server: d.server, export: d.export, ...(d.nfs_directory ? { directory: d.nfs_directory } : {}) } };
    case "zfs": return { zfs: { pool: d.pool, dataset: d.dataset, mount_path: d.path } };
    case "smb": return { smb: { server: d.server, share: d.share, ...(d.directory ? { directory: d.directory } : {}) } };
    case "iscsi": return { iscsi: { portal: d.portal, target_iqn: d.target_iqn, lun: Number(d.lun), wwid: d.wwid, filesystem_uuid: d.filesystem_uuid, exclusive_target: d.iscsi_mode === "exclusive" && d.exclusive_target, mode: d.iscsi_mode } };
  }
}

function SourceFields({ draft, setDraft, disabled, browseReady, onBrowse, onDiscoverISCSI }: { draft: Draft; setDraft: (patch: Partial<Draft>) => void; disabled: boolean; browseReady: boolean; onBrowse: (kind: "nfs" | "smb") => void; onDiscoverISCSI: () => void }) {
  const field = (key: Exclude<keyof Draft, "exclusive_target">, title: string, placeholder: string, extra: Partial<InputHTMLAttributes<HTMLInputElement>> = {}) => <label>{title}<input required value={draft[key] as string} disabled={disabled} placeholder={placeholder} onChange={e => setDraft({ [key]: e.target.value } as Partial<Draft>)} {...extra} /></label>;
  if (draft.kind === "local") return <>{field("path", "Existing directory", "/var/lib/libvirt/images", { pattern: "/.*" })}</>;
  if (draft.kind === "nfs") return <>{field("server", "NFS server", "nfs.example.test")}<button type="button" disabled={disabled || !browseReady || !draft.server} onClick={() => onBrowse("nfs")}>Browse exports</button>{field("export", "Export path", "/exports/vms", { pattern: "/.*" })}<label>Existing directory within export<input value={draft.nfs_directory} disabled={disabled} placeholder="Optional, for example team/vms" onChange={e => setDraft({ nfs_directory: e.target.value })} /></label></>;
  if (draft.kind === "zfs") return <>{field("pool", "Imported pool", "tank")}{field("dataset", "Existing dataset", "vms")}{field("path", "Reported mount point", "/tank/vms", { pattern: "/.*" })}<p className="help">This existing-dataset path uses an already imported and mounted filesystem dataset. It never creates, imports, upgrades or changes ZFS pool properties.</p></>;
  if (draft.kind === "smb") return <>{field("server", "SMB server", "files.example.test")}{field("username", "Account", "nex-vm")}<label>Domain<input value={draft.domain} disabled={disabled} autoComplete="off" onChange={e => setDraft({ domain: e.target.value })} /></label>{field("password", "Password", "", { type: "password", autoComplete: "new-password" })}<button type="button" disabled={disabled || !browseReady || !draft.server || !draft.username || !draft.password} onClick={() => onBrowse("smb")}>Browse shared folders</button>{field("share", "Share", "vms")}{draft.directory ? <p className="mono storage-selected-folder">{draft.directory}</p> : null}</>;
  return <>{field("portal", "Target portal", "storage.example.test:3260")}<button type="button" disabled={disabled || !browseReady || !draft.portal} onClick={onDiscoverISCSI}>Discover targets</button>{field("target_iqn", "Target IQN", "iqn.2026-01.example.test:vm-storage")}{field("lun", "LUN", "0", { inputMode: "numeric" })}{field("wwid", "LUN WWID", "360000000000000000000000000000001")}{field("filesystem_uuid", "Existing filesystem UUID", "00000000-0000-0000-0000-000000000000")}<label className="console-option"><input required type="checkbox" checked={draft.exclusive_target} disabled={disabled} onChange={e => setDraft({ exclusive_target: e.target.checked })} /> This LUN is restricted to this Core initiator only</label><p className="help">The LUN must already have one ext4 or XFS filesystem and be restricted to this Core initiator only. Use Clustered iSCSI / GFS2 setup below for shared block storage.</p><label>CHAP account<input value={draft.username} disabled={disabled} placeholder="Optional" onChange={e => setDraft({ username: e.target.value })} /></label><label>CHAP secret<input type="password" autoComplete="new-password" value={draft.password} disabled={disabled} onChange={e => setDraft({ password: e.target.value })} /></label></>;
}

function ClusteredBlockStatus({ poolID, masterHostID, hosts, api, unavailable }: Pick<Props, "poolID" | "masterHostID" | "hosts" | "api" | "unavailable">) {
  const [result, setResult] = useState<ClusterResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [credentialNotice, setCredentialNotice] = useState("");
  const [fenceOutcomeNotice, setFenceOutcomeNotice] = useState("");
  const [fenceTarget, setFenceTarget] = useState<{ hostID: string; hostname: string; repositoryID: string; uuid?: string; priorProof?: ClusterFenceProof } | null>(null);
  const [fenceText, setFenceText] = useState("");
  const [unknownFenceTargets, setUnknownFenceTargets] = useState<string[]>([]);
  const fenceDialog = useRef<HTMLDialogElement>(null);
  const iscsiDiscoveryAbort = useRef<AbortController | null>(null);
  const clusterHosts = hosts.filter(host => !host.revoked && host.connection === "fresh" && host.capabilities?.includes("storage-clustered-iscsi-v2"));
  const [setup, setSetup] = useState<Record<string, string>>({ name: "", portal: "", target_iqn: "", lun: "0", wwid: "", filesystem_uuid: "" });
  const [targetDiscovery, setTargetDiscovery] = useState<{ portal: string; loading: boolean; error: string; candidates: Candidate[] } | null>(null);
  const currentPortal = useRef(setup.portal);
  currentPortal.current = setup.portal;
  const discoveryHost = hosts.find(host => host.id === masterHostID);
  const discoveryReady = Boolean(discoveryHost && !discoveryHost.revoked && discoveryHost.connection === "fresh" && discoveryHost.capabilities?.includes("storage-repositories-v1") && discoveryHost.capabilities?.includes("storage-discovery-v1"));
  async function refresh() {
    setLoading(true); setError("");
    try { setResult(await api<ClusterResult>(`storage/cluster?${new URLSearchParams({ pool_id: poolID })}`)); setUnknownFenceTargets([]); setFenceOutcomeNotice(""); }
    catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  }
  useEffect(() => { void refresh(); }, [poolID]);
  useEffect(() => {
    if (fenceTarget && !fenceDialog.current?.open) fenceDialog.current?.showModal();
  }, [fenceTarget]);
  useEffect(() => {
    if (targetDiscovery && targetDiscovery.portal !== setup.portal) {
      const abort = iscsiDiscoveryAbort.current;
      abort?.abort();
      if (iscsiDiscoveryAbort.current === abort) iscsiDiscoveryAbort.current = null;
      setTargetDiscovery(null);
    }
  }, [setup.portal, targetDiscovery]);
  const operations = result?.operations || [];
  const statusView = clusterStatusView(loading, error, result?.status, operations.length);
  // Folded while there is nothing to see; a configuration or a problem opens it, and it never
  // closes itself under an operator who is working in it.
  const [open, setOpen] = useState(false);
  useEffect(() => { if (statusView === "configured" || statusView === "unavailable") setOpen(true); }, [statusView]);
  function cancelTargetDiscovery() {
    iscsiDiscoveryAbort.current?.abort();
    iscsiDiscoveryAbort.current = null;
    setTargetDiscovery(null);
  }
  async function discoverTargets() {
    const portal = setup.portal;
    if (!portal || !discoveryReady) return;
    iscsiDiscoveryAbort.current?.abort();
    const abort = new AbortController();
    iscsiDiscoveryAbort.current = abort;
    setTargetDiscovery({ portal, loading: true, error: "", candidates: [] });
    try {
      const result = await api<Discovery>("storage/discover", "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: masterHostID, kind: "iscsi", server: portal }, undefined, undefined, abort.signal);
      if (abort.signal.aborted || iscsiDiscoveryAbort.current !== abort || currentPortal.current !== portal) return;
      const candidates = result.status === "succeeded" ? (result.candidates || []).filter(candidate => candidate.kind === "iscsi" && candidate.source?.iscsi?.target_iqn) : [];
      setTargetDiscovery({ portal, loading: false, error: result.status === "succeeded" ? result.error || "" : result.error || "Core target discovery did not complete.", candidates });
    } catch (e) {
      if (!abort.signal.aborted && iscsiDiscoveryAbort.current === abort && currentPortal.current === portal) setTargetDiscovery({ portal, loading: false, error: (e as Error).message, candidates: [] });
    } finally {
      if (iscsiDiscoveryAbort.current === abort) iscsiDiscoveryAbort.current = null;
    }
  }
  async function applySetup(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (clusterHosts.length < 3 || clusterHosts.length > 5) { setError("Clustered GFS2 needs exactly three to five fresh Core hosts."); return; }
    const requestID = crypto.randomUUID(); setLoading(true); setError(""); setCredentialNotice("");
    try {
      const credential_requests = [] as { request_id: string; kind: "fence_ssh"; target_host_id: string }[];
      for (const host of clusterHosts) {
        const id = crypto.randomUUID(); credential_requests.push({ request_id: id, kind: "fence_ssh", target_host_id: host.id });
        await api("storage/cluster/credentials", "POST", { request_id: id, cluster_request_id: requestID, pool_id: poolID, target_host_id: host.id, kind: "fence_ssh", credentials: { ssh_private_key: setup[`key:${host.id}`], ssh_known_hosts: setup[`known:${host.id}`] } });
      }
      const nodes = clusterHosts.map((host, index) => ({ host_id: host.id, corosync_node_id: index + 1, corosync_address: setup[`address:${host.id}`], fence: { kind: "virsh", endpoint: setup[`endpoint:${host.id}`], target_id: setup[`uuid:${host.id}`], ssh_login: setup[`login:${host.id}`], ssh_port: Number(setup[`port:${host.id}`]), virsh_uri: setup[`uri:${host.id}`] } }));
      await api("storage/cluster/apply", "POST", { request_id: requestID, pool_id: poolID, expected_generation: 0, credential_requests, registration: { name: setup.name, kind: "iscsi", source: { iscsi: { portal: setup.portal, target_iqn: setup.target_iqn, lun: Number(setup.lun), wwid: setup.wwid, filesystem_uuid: setup.filesystem_uuid, mode: "clustered_gfs2" } } }, config: { nodes } });
      setSetup(values => Object.fromEntries(Object.entries(values).filter(([key]) => !key.startsWith("key:") && !key.startsWith("known:")))); setCredentialNotice("Core accepted setup. Refresh Core status for per-member progress; accepted delivery is not ready storage."); await refresh();
    } catch (e) { setError((e as Error).message); } finally { setLoading(false); }
  }
  async function verifyFence(e: FormEvent<HTMLFormElement>) {
    e.preventDefault(); if (!fenceTarget || fenceText !== `FENCE ${fenceTarget.hostID}`) return;
    setLoading(true); setError("");
    try { await api("storage/cluster/verify-fence", "POST", { request_id: crypto.randomUUID(), pool_id: poolID, repository_id: fenceTarget.repositoryID, host_id: fenceTarget.hostID, fence_confirmation: fenceText }); setCredentialNotice("Fence verification was submitted to Core. Refresh status for the durable result; Nex will not retry it automatically."); fenceDialog.current?.close(); await refresh(); }
    catch (e) {
      const requestError = e as Error & { status?: number };
      if (!requestError.status || requestError.status >= 500 || requestError.status === 408) {
        setUnknownFenceTargets(targets => targets.includes(fenceTarget.hostID) ? targets : [...targets, fenceTarget.hostID]);
        setFenceOutcomeNotice(`Fence verification response was not received${requestError.message ? ` (${requestError.message})` : ""}. Its outcome is unknown; refresh Core status before any further fence action. Nex will not resend it automatically.`);
        fenceDialog.current?.close();
      } else setError(requestError.message);
    } finally { setLoading(false); }
  }
  return <section className="panel cluster-storage" aria-label="Clustered iSCSI storage"><details className="fold" open={open} onToggle={event => setOpen(event.currentTarget.open)}>
    <summary className="section-heading"><h2>Clustered iSCSI / GFS2</h2><span className="muted">{statusView === "configured" ? `${operations.length} configured` : statusView === "empty" ? "Not configured" : statusView === "checking" ? "Checking…" : "Status unavailable"}</span></summary>
    <div className="managed-actions actions fold-actions"><button type="button" disabled={loading || unavailable} onClick={() => void refresh()}>{loading ? "Checking…" : "Refresh Core status"}</button></div>
    {statusView === "checking" ? <p className="warning" role="status">Checking Core status. Clustered configuration is not confirmed yet.</p> : error ? <p className="warning" role="status">Core status is unavailable: {error}</p> : result?.status === "stale" ? <p className="warning" role="status">Core status is stale. It does not confirm a safe clustered writer.</p> : null}
    {fenceOutcomeNotice && <p className="warning" role="status">{fenceOutcomeNotice}</p>}
    {statusView === "configured" ? <div className="table-scroll"><table><thead><tr><th>Repository / setup</th><th>Core stage</th><th>Fencing</th><th>Member observations</th></tr></thead><tbody>{operations.map(operation => {
      const members = operation.members || [];
      const fenceReady = Boolean(operation.config.cluster_id && operation.config.lock_table && operation.config.device_path && operation.config.mount_path);
      const proofs = operation.config.fence_proofs || [];
      return <tr key={operation.request_id}><td><strong>{operation.config.repository_id}</strong><small>Cluster {operation.config.cluster_id} · lock table {operation.config.lock_table}</small><small>Storage generation {operation.config.generation} · membership generation {operation.membership_generation}</small></td><td><Badge value={operation.status} /><small>{operation.updated_at ? storageChecked(operation.updated_at) : "Not checked"}</small>{operation.error && <small className="task-error">{operation.error}</small>}</td><td>{proofs.length ? proofs.map(proof => <small key={proof.host_id}>Fence proof for {hosts.find(host => host.id === proof.host_id)?.hostname || proof.host_id}: <Badge value={proof.status} />{proof.request_id ? ` · request ${proof.request_id}` : ""}{proof.exclusion_verified ? " · excluded" : ""} · {fenceProofState(proof)}{proof.error ? ` ${proof.error}` : ""}</small>) : members.length ? members.map(member => <small key={member.host_id}><Badge value={member.status.fencing_verified ? "verified" : "unverified"} /> {hosts.find(host => host.id === member.host_id)?.hostname || member.host_id}{member.status.fencing_verified ? " · Core recorded a fencing proof." : " · blocked until Core records a real destructive fencing proof."}{member.status.fences?.map(fence => ` ${fence.status}${fence.exclusion_verified ? " (excluded)" : ""}`)}</small>) : <small>Unverified · no Core fence proof or member observation reported.</small>}</td><td>{operation.config.nodes.map(node => { const host = hosts.find(item => item.id === node.host_id)?.hostname || node.host_id; const member = members.find(item => item.host_id === node.host_id); const proof = proofs.find(item => item.host_id === node.host_id); const outcomeUnknown = unknownFenceTargets.includes(node.host_id); const reverify = Boolean(proof && proof.status !== "unverified"); const actionLabel = reverify ? "Reverify power fence" : "Verify power fence"; const showFenceAction = !member?.status.fencing_verified || Boolean(proof); return <small key={node.host_id}><strong>{host}</strong> · node {node.corosync_node_id} · {node.fence.kind}{node.fence.endpoint ? ` · ${node.fence.endpoint}` : ""}{node.fence.device_path ? ` · ${node.fence.device_path}` : ""} · {member ? `${member.stage}: ${member.status.status}, quorum ${member.status.quorate ? "confirmed" : "not confirmed"}, GFS2 ${member.status.mounted ? "mounted" : "not mounted"}, ${storageChecked(member.updated_at)}` : "health not yet reported"} {showFenceAction && <button type="button" title={fenceProofTitle(proof, fenceReady, outcomeUnknown)} disabled={!fenceReady || proof?.status === "unverified" || outcomeUnknown} onClick={() => setFenceTarget({ hostID: node.host_id, hostname: host, repositoryID: operation.config.repository_id, uuid: node.fence.target_id, priorProof: proof })}>{actionLabel}</button>}</small>; })}</td></tr>;
    })}</tbody></table></div> : statusView === "empty" ? <p className="empty">No clustered GFS2 configuration is currently reported by the pool master. Ordinary iSCSI remains exclusive to one Core.</p> : <p className="empty">Clustered configuration is unknown until the pool master reports current status. Setup is unavailable.</p>}
    {clusterSetupAvailable(statusView) && <form className="cluster-setup" onSubmit={applySetup}><h3>Set up clustered GFS2</h3><p className="help">Core allocates the repository identity and derives its mount and GFS2 metadata. Supply the existing LUN number, WWID and filesystem UUID from storage inventory; Core validates those identities on every selected Core.</p><div className="cluster-source"><label>Repository name<input required value={setup.name} onChange={e => setSetup(v => ({ ...v, name: e.target.value }))} /></label><label>Target portal<input required value={setup.portal} onChange={e => setSetup(v => ({ ...v, portal: e.target.value }))} /></label><div className="actions"><button type="button" disabled={targetDiscovery?.loading || !setup.portal || !discoveryReady || unavailable} onClick={() => void discoverTargets()}>{targetDiscovery?.loading ? "Discovering targets…" : "Discover targets"}</button>{targetDiscovery?.loading && <button type="button" onClick={cancelTargetDiscovery}>Cancel discovery</button>}</div>{targetDiscovery && !targetDiscovery.loading && (targetDiscovery.error ? <p className="alert" role="alert">{targetDiscovery.error}</p> : targetDiscovery.candidates.length ? <ul>{targetDiscovery.candidates.map((candidate, index) => <li key={`${candidate.source?.iscsi?.portal}-${candidate.source?.iscsi?.target_iqn}-${index}`}><button type="button" onClick={() => { const iscsi = candidate.source?.iscsi; if (iscsi) setSetup(values => ({ ...values, portal: iscsi.portal || targetDiscovery.portal, target_iqn: iscsi.target_iqn })); setTargetDiscovery(null); }}>Use {candidate.source?.iscsi?.target_iqn}</button></li>)}</ul> : <p className="empty">Core found no iSCSI targets at this portal.</p>)}<label>Target IQN<input required value={setup.target_iqn} onChange={e => setSetup(v => ({ ...v, target_iqn: e.target.value }))} /></label><label>LUN<input required inputMode="numeric" value={setup.lun} onChange={e => setSetup(v => ({ ...v, lun: e.target.value }))} /></label><label>LUN WWID<input required value={setup.wwid} onChange={e => setSetup(v => ({ ...v, wwid: e.target.value }))} /></label><label>Filesystem UUID<input required value={setup.filesystem_uuid} onChange={e => setSetup(v => ({ ...v, filesystem_uuid: e.target.value }))} /></label></div><div className="cluster-nodes">{clusterHosts.map((host, index) => <fieldset key={host.id}><legend>{host.hostname} · Corosync node {index + 1}</legend><label>Corosync address<input required value={setup[`address:${host.id}`] || ""} onChange={e => setSetup(v => ({ ...v, [`address:${host.id}`]: e.target.value }))} /></label><label>Fence SSH host<input required value={setup[`endpoint:${host.id}`] || ""} onChange={e => setSetup(v => ({ ...v, [`endpoint:${host.id}`]: e.target.value }))} /></label><label>SSH login<input required value={setup[`login:${host.id}`] || ""} onChange={e => setSetup(v => ({ ...v, [`login:${host.id}`]: e.target.value }))} /></label><label>SSH port<input required inputMode="numeric" value={setup[`port:${host.id}`] || "22"} onChange={e => setSetup(v => ({ ...v, [`port:${host.id}`]: e.target.value }))} /></label><label>Libvirt domain UUID<input required value={setup[`uuid:${host.id}`] || ""} onChange={e => setSetup(v => ({ ...v, [`uuid:${host.id}`]: e.target.value }))} /></label><label>Virsh URI<input required value={setup[`uri:${host.id}`] || ""} onChange={e => setSetup(v => ({ ...v, [`uri:${host.id}`]: e.target.value }))} /></label><label>Pinned SSH host key<textarea required value={setup[`known:${host.id}`] || ""} onChange={e => setSetup(v => ({ ...v, [`known:${host.id}`]: e.target.value }))} /></label><label>SSH private key<textarea required value={setup[`key:${host.id}`] || ""} onChange={e => setSetup(v => ({ ...v, [`key:${host.id}`]: e.target.value }))} /></label></fieldset>)}</div><button className="primary" disabled={loading || unavailable || clusterHosts.length < 3 || clusterHosts.length > 5}>{loading ? "Submitting…" : "Submit Core setup"}</button>{credentialNotice && <p className="notice" role="status">{credentialNotice}</p>}</form>}
    <About title="About clustered storage"><p>Only the pool master configures clustered block storage. Nex Core must validate all three to five members, matching GFS2 identity, quorum/DLM/Pacemaker, and destructive fencing before it reports a safe writer. BMC and SCSI-PR fencing remain unsupported until Core can verify their target identity. An unverified or blocked state never enables ordinary ext4/XFS sharing.</p></About>
    <dialog ref={fenceDialog} className="confirm-dialog" aria-labelledby="verify-fence-title" onClose={() => { setFenceTarget(null); setFenceText(""); }}>{fenceTarget && <form onSubmit={verifyFence}><h2 id="verify-fence-title">{fenceTarget.priorProof ? "Reverify" : "Verify"} power fence for {fenceTarget.hostname}?</h2><p>This powers off the external VM target{fenceTarget.uuid ? ` (${fenceTarget.uuid})` : ""}. If this is the pool master, another Core performs the proof and the master must return before the cluster can proceed.</p>{fenceTarget.priorProof && <p className="warning">Core previously reported {fenceTarget.priorProof.status}{fenceTarget.priorProof.exclusion_verified ? " with target exclusion" : ""}. This submits a new destructive verification; Core retains the prior proof.</p>}<p>Type <span className="mono">FENCE {fenceTarget.hostID}</span> to submit one destructive Core verification. Nex never retries it automatically.</p><label>Confirmation<input required autoFocus value={fenceText} onChange={e => setFenceText(e.target.value)} /></label><div className="actions"><button type="button" disabled={loading} onClick={() => fenceDialog.current?.close()}>Cancel</button><button className="danger" disabled={loading || fenceText !== `FENCE ${fenceTarget.hostID}`}>{loading ? "Submitting…" : fenceTarget.priorProof ? "Reverify power fence" : "Verify power fence"}</button></div></form>}</dialog>
  </details></section>;
}

export function StorageRepositories({ poolID, masterHostID, hosts, storage, api, reload, unavailable, onNotice }: Props) {
  const [form, setForm] = useState(false);
  const [zfsProvision, setZFSProvision] = useState(false);
  const [configureZFS, setConfigureZFS] = useState<ZFSOutcome | null>(null);
  const [replaceZFS, setReplaceZFS] = useState<{ repositoryID: string; name: string; host: Host } | null>(null);
  const [draft, setDraft] = useState<Draft>(blank);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [detachError, setDetachError] = useState("");
  const [edit, setEdit] = useState<Storage | null>(null);
  const detach = useRef<HTMLDialogElement>(null);
  const [remove, setRemove] = useState<Storage | null>(null);
  const [discovery, setDiscovery] = useState<Discovery | null>(null);
  const [discovering, setDiscovering] = useState(false);
  const discoveryAbort = useRef<AbortController | null>(null);
  const currentHosts = useRef(hosts);
  currentHosts.current = hosts;
  const browserDialog = useRef<HTMLDialogElement>(null);
  const browserAbort = useRef<AbortController | null>(null);
  const [browser, setBrowser] = useState<Browser | null>(null);
  const patch = (value: Partial<Draft>) => setDraft(d => ({ ...d, ...value }));
  const browserContext = () => {
    const hostID = draft.kind === "nfs" || draft.kind === "smb" ? masterHostID : draft.host_id;
    const host = hosts.find(h => h.id === hostID);
    return `${poolID}\n${masterHostID}\n${draft.kind}\n${draft.host_id}\n${draft.server}\n${draft.username}\n${draft.password}\n${draft.domain}\n${host?.connection}\n${host?.revoked}\n${host?.capabilities?.join(",")}`;
  };
  const currentBrowserContext = useRef(browserContext());
  currentBrowserContext.current = browserContext();
  // Shared repository changes are authored by the pool master. Do not offer a
  // member here only to have Core reject a queued request.
  const activeHosts = hosts.filter(h => !h.revoked && h.connection === "fresh" && h.capabilities?.includes("managed-vm-v1") && (!form || (draft.kind !== "nfs" && draft.kind !== "smb" && !(draft.kind === "iscsi" && draft.iscsi_mode === "clustered_gfs2")) || h.id === masterHostID));

	const browseHostID = draft.kind === "nfs" || draft.kind === "smb" ? masterHostID : draft.host_id;
	const browseReady = hosts.some(h => h.id === browseHostID && !h.revoked && h.connection === "fresh" && h.capabilities?.includes("storage-discovery-v1") && h.capabilities?.includes("storage-repositories-v1"));
	useEffect(() => {
		if (form && (draft.kind === "nfs" || draft.kind === "smb") && draft.host_id !== masterHostID) patch({ host_id: masterHostID });
	}, [form, draft.kind, draft.host_id, masterHostID]);
  useEffect(() => {
    if (browser && !browserDialog.current?.open) browserDialog.current?.showModal();
  }, [browser]);
  useEffect(() => {
    if (browser && browser.context !== browserContext()) closeBrowser();
  }, [browser, poolID, masterHostID, draft.kind, draft.host_id, draft.server, draft.username, draft.password, draft.domain, hosts]);
  function cancelBrowser() { const abort = browserAbort.current; abort?.abort(); if (browserAbort.current === abort) browserAbort.current = null; setBrowser(null); }
  function closeBrowser() { cancelBrowser(); browserDialog.current?.close(); }
  function close() { discoveryAbort.current?.abort(); closeBrowser(); setForm(false); setZFSProvision(false); setConfigureZFS(null); setEdit(null); setDraft(blank); setError(""); }
  function discoveryBody(kind: Kind, path?: string, values = draft) {
    return { request_id: crypto.randomUUID(), pool_id: poolID, host_id: values.host_id, kind, ...(kind === "nfs" || kind === "smb" ? { server: values.server } : kind === "iscsi" ? { server: values.portal } : path || values.path ? { path: path || values.path } : {}), ...(kind === "nfs" && path ? { path } : {}), ...(kind === "smb" && path ? { path } : {}), ...(kind === "smb" ? { credentials: smbCredentials(values) } : {}) };
  }
  async function discover() {
    if (!draft.host_id) { setError("Select a Core host before discovering storage."); return; }
    discoveryAbort.current?.abort(); const abort = new AbortController(); discoveryAbort.current = abort; setDiscovering(true); setBusy(true); setError(""); setDiscovery(null);
    try { setDiscovery(await api<Discovery>("storage/discover", "POST", discoveryBody(draft.kind), undefined, undefined, abort.signal)); }
    catch (e) { if (!abort.signal.aborted) setError((e as Error).message); } finally { if (discoveryAbort.current === abort) { discoveryAbort.current = null; setDiscovering(false); setBusy(false); } }
  }
  async function browse(kind: "nfs" | "smb", path = "", base = "") {
    browserAbort.current?.abort();
    const abort = new AbortController(); browserAbort.current = abort;
    const values = { ...draft, host_id: masterHostID };
    const context = browserContext();
    setBrowser({ kind, path, base, context, candidates: [], loading: true, error: "" });
    try {
      const result = await api<Discovery>("storage/discover", "POST", discoveryBody(kind, path || undefined, values), undefined, undefined, abort.signal);
      if (!abort.signal.aborted && browserAbort.current === abort && context === currentBrowserContext.current) setBrowser({ kind, path, base, context, candidates: result.status === "succeeded" ? result.candidates?.filter(candidate => candidate.browsable) || [] : [], loading: false, error: result.status === "succeeded" ? result.error || "" : result.error || "Discovery did not complete." });
    } catch (e) {
      if (!abort.signal.aborted && browserAbort.current === abort && context === currentBrowserContext.current) setBrowser({ kind, path, base, context, candidates: [], loading: false, error: (e as Error).message });
    } finally { if (browserAbort.current === abort) browserAbort.current = null; }
  }
  function openFolder(candidate: Candidate) {
    if (!browser || !candidate.path) return;
    const base = browser.base || (browser.kind === "nfs" ? candidate.source?.nfs?.export || candidate.path : candidate.source?.smb?.share || "");
    void browse(browser.kind, candidate.path, base);
  }
  function browseUp() {
    if (!browser?.path) return;
    if (browser.kind === "nfs" && browser.path === browser.base) { void browse(browser.kind); return; }
    const parent = browser.path.slice(0, browser.path.lastIndexOf("/")) || "";
    void browse(browser.kind, parent, parent ? browser.base : "");
  }
  function selectFolder() {
    if (!browser?.selected?.path) return;
    const path = browser.selected.path;
    if (browser.kind === "smb") {
      const [share, ...directory] = path.replace(/^\//, "").split("/");
      patch({ server: draft.server, share, directory: directory.join("/") });
    } else {
      const base = browser.base || browser.selected.source?.nfs?.export || path;
      patch({ server: draft.server, export: base, nfs_directory: path === base ? "" : path.startsWith(`${base}/`) ? path.slice(base.length + 1) : "" });
    }
    closeBrowser();
  }
  function selectDiscovery(candidate: NonNullable<Discovery["candidates"]>[number]) { const s = candidate.source; if (s?.nfs) patch({ kind: "nfs", server: s.nfs.server, export: s.nfs.export, nfs_directory: s.nfs.directory || "" }); else if (s?.smb) { const prefix = `/${s.smb.share}/`; patch({ kind: "smb", server: s.smb.server, share: s.smb.share, directory: candidate.path?.startsWith(prefix) ? candidate.path.slice(prefix.length) : "" }); } else if (s?.iscsi) patch({ kind: "iscsi", portal: s.iscsi.portal, target_iqn: s.iscsi.target_iqn }); else if (s?.zfs) patch({ kind: "zfs", pool: s.zfs.pool, dataset: s.zfs.dataset, path: s.zfs.mount_path }); else patch({ path: candidate.path || "" }); setDiscovery(null); }
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (edit && !storageOwnerFresh(edit, currentHosts.current)) { setError("Assigned Core is not freshly connected. Reconnect it before changing this repository."); return; }
    setBusy(true); setError("");
    try {
      const request_id = crypto.randomUUID();
      const registration = { name: draft.name.trim(), kind: draft.kind, ...(draft.kind === "local" ? { path: draft.path } : { source: source(draft) }) };
      const credentials = needsCredential(draft.kind, draft) ? (draft.kind === "smb" ? smbCredentials(draft) : { username: draft.username, secret: draft.password }) : undefined;
      // Passwords deliberately never enter session storage, notices, task payloads, or a GET response.
      // Drop the draft value as soon as the handoff payload is built so reload cannot leave it in the form.
      if (credentials) patch({ password: "" });
      if (edit) {
        let credential_receipt = "";
        if (credentials) credential_receipt = (await api<{ receipt: string }>("storage/credentials", "POST", { request_id, pool_id: poolID, host_id: draft.host_id, repository_id: edit.id, credentials })).receipt;
        if (!storageOwnerFresh(edit, currentHosts.current)) { setError("Assigned Core is not freshly connected. Reconnect it before changing this repository."); return; }
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
  async function recheck(s: Storage) { if (!storageOwnerFresh(s, currentHosts.current)) { setError("Assigned Core is not freshly connected. Reconnect it before changing this repository."); return; } setBusy(true); setError(""); try { await api(`storage/${encodeURIComponent(s.id)}/recheck`, "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: s.host_id, expected_generation: s.generation }); await reload(); onNotice(`Access recheck queued for ${s.name}.`); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function detachStorage() { if (!remove) return; if (!storageOwnerFresh(remove, currentHosts.current)) { const message = "Assigned Core is not freshly connected. Reconnect it before changing this repository."; setError(message); setDetachError(message); return; } setBusy(true); setError(""); setDetachError(""); try { await api(`storage/${encodeURIComponent(remove.id)}/detach`, "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: remove.host_id, expected_generation: remove.generation }); await reload(); onNotice(`Detach queued for ${remove.name}. Nex never erases storage data.`); detach.current?.close(); setRemove(null); } catch (e) { const message = (e as Error).message; setError(message); setDetachError(message); } finally { setBusy(false); } }
  // Deletes only the Controller's failed-request row. A 409 (a host reports the registration as
  // applied) is shown exactly as the Controller worded it; nothing is retried or reconciled here.
  async function discardStorage(s: Storage) {
    if (!confirm(discardConfirmText("storage", s.name))) return;
    setBusy(true); setError("");
    try {
      await api(`storage/${encodeURIComponent(s.id)}`, "DELETE");
      await reload();
      onNotice(`Discarded the failed registration for ${s.name}. Register the storage again to retry.`);
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  // Per-host ZFS outcomes as each Core reports them; the Controller keeps no record of its own.
  const zfsOutcomes = hosts.flatMap(host => ((host.inventory as { managed?: { zfs_provisions?: ZFSOutcome[] } } | null)?.managed?.zfs_provisions || []).filter(result => result.pool_id === poolID));
  return <>
    {error && <p className="alert" role="alert">{error}</p>}
    {zfsProvision && <ZFSProvisionWizard key={configureZFS?.repository_id || "new"} poolID={poolID} hosts={hosts} api={api} unavailable={unavailable} onClose={close} onUseExisting={() => { setZFSProvision(false); setForm(true); setDraft({ ...blank, kind: "zfs" }); }} onSubmitted={name => { void reload(); onNotice(`ZFS provisioning queued for ${name}. Follow per-host outcomes in Tasks.`); }} repositoryID={configureZFS?.repository_id} initialName={configureZFS?.name} initialHostID={configureZFS?.host_id} excludeHostIDs={configureZFS ? zfsOutcomes.filter(item => item.repository_id === configureZFS.repository_id).map(item => item.host_id) : []} />}
    {form && <section className="panel form-panel storage-form" aria-label={edit ? "Update storage" : "Register storage"}><div className="section-heading"><h2>{edit ? `Update ${edit.name}` : "Register VM storage"}</h2><button type="button" disabled={busy} onClick={close}>Close</button></div><p>Register an existing storage source. Shared NFS and SMB registrations are authored by the pool master, then each member is verified separately before use.</p><form onSubmit={submit}><label>Name<input required maxLength={100} value={draft.name} autoFocus disabled={busy} onChange={e => patch({ name: e.target.value })} /></label><label>Repository type<select value={draft.kind} disabled={busy || Boolean(edit)} onChange={e => { patch({ kind: e.target.value as Kind, username: "", password: "", domain: "", directory: "", nfs_directory: "" }); setDiscovery(null); }}>{(Object.keys(labels) as Kind[]).map(k => <option key={k} value={k}>{labels[k]}</option>)}</select></label>{draft.kind === "nfs" || draft.kind === "smb" || (draft.kind === "iscsi" && draft.iscsi_mode === "clustered_gfs2") ? <p className="help">Pool master: <strong>{hosts.find(h => h.id === masterHostID)?.hostname || "unavailable"}</strong>. Shared storage is recorded on the pool master and every host in the pool receives it.</p> : <><label htmlFor="storage-host">Host</label><select id="storage-host" required value={draft.host_id} disabled={busy || Boolean(edit)} onChange={e => patch({ host_id: e.target.value })}><option value="">Select host</option>{activeHosts.map(h => <option key={h.id} value={h.id}>{h.hostname}</option>)}</select></>}<SourceFields draft={draft} setDraft={patch} disabled={busy} browseReady={!unavailable && browseReady} onBrowse={kind => void browse(kind)} onDiscoverISCSI={() => void discover()} />{!edit && draft.kind !== "nfs" && draft.kind !== "smb" && <>{draft.kind !== "iscsi" && <button type="button" disabled={busy || unavailable || !draft.host_id} onClick={() => void discover()}>Discover safe existing sources</button>}{discovering && <button type="button" onClick={() => discoveryAbort.current?.abort()}>Cancel discovery</button>}{discovery && (discovery.candidates?.length ? <ul>{discovery.candidates.map((candidate, i) => <li key={i}><button type="button" disabled={busy} onClick={() => selectDiscovery(candidate)}>Use {candidate.path || storageSource({ source: candidate.source } as Storage)}</button>{candidate.browsable ? " · existing directory can be selected" : ""}</li>)}</ul> : <p className="empty">{discovery.error || "No compatible existing sources were found. Enter the source manually."}</p>)}</>}<p className="help">Discovery is a short-lived Core observation. It does not mount, create, format or change storage; manual entry remains available when a source is not reachable.</p><p className="help">Local, ZFS and ordinary/exclusive iSCSI repositories remain assigned to one Core. Shared NFS and SMB access is usable on a member only after Core verifies its current writable, QEMU and locking access.</p><button className="primary" disabled={busy || unavailable || !activeHosts.length}>{busy ? "Submitting…" : edit ? "Update and check access" : "Register and check access"}</button></form></section>}
    <Section icon="storage" title="Storage" count={storage.length} actions={<>
      <button className="sm primary" disabled={busy || unavailable || zfsProvision} onClick={() => { setForm(true); setZFSProvision(false); setConfigureZFS(null); setEdit(null); setDraft(blank); }}>Register storage</button>
      <button type="button" className="sm" disabled={busy || unavailable || zfsProvision} onClick={() => { setForm(false); setZFSProvision(true); setConfigureZFS(null); setEdit(null); setDraft(blank); setError(""); }}>Add ZFS storage</button>
    </>}>
      {storage.length ? <div className="table-scroll"><table className="records resources"><thead><tr><th>Name</th><th>Type</th><th>Scope</th><th>Capacity</th><th>Health</th><th>Host access</th><th>Last check</th><th>Manage</th></tr></thead><tbody>{storage.map(s => {
        const lifecycleReady = storageLifecycleReady(s);
        const ownerFresh = storageOwnerFresh(s, hosts);
        const coordinatedLifecycleUnavailable = clusteredGFS2Profile(s);
        const lifecycleTitle = !ownerFresh
          ? "Assigned Core is not freshly connected. Reconnect it before changing this repository."
          : !lifecycleReady
          ? "Wait for Nex Core to assign and confirm this repository."
          : coordinatedLifecycleUnavailable
            ? "Clustered GFS2 lifecycle changes are not coordinated yet. Recheck or refresh Core status instead."
            : undefined;
        const status = ownerFresh ? storageAccess(s) : "stale";
        const total = s.access?.total_bytes, available = s.access?.available_bytes;
        const registeredPool = s.kind === "zfs" && !s.zfs_host_access?.length ? s.access?.zfs_pool : undefined;
        const checked = s.access?.observed_at && storageChecked(s.access.observed_at) !== "Not checked" ? s.access.observed_at : "";
        return <tr key={s.id || `op:${s.operation_id}`} data-storage-id={s.id}>
          <td><span className={`dot ${badgeTone(status)}`} aria-hidden="true" /><strong>{s.name}</strong><small className="mono">{storageSource(s)}</small></td>
          <td>{labels[(s.kind || "local") as Kind] || s.kind || "Local directory"}</td>
          <td>{storageScope(s)}</td>
          <td>{total && available !== undefined ? <><MiniBar percent={(1 - available / total) * 100} /><small>{ownerFresh ? "" : "Last reported "}{size(total - available)} used · {size(available)} free</small></> : <span className="muted">Not reported</span>}</td>
          <td><Badge value={status} label={ownerFresh ? undefined : "Unknown"} />{!ownerFresh && <small>Assigned Core is not reporting; showing its last report.</small>}{s.access?.error && <small>{s.access.error}</small>}{registeredPool && <ZFSPoolLines pool={registeredPool} />}</td>
          <td><AccessLights items={storageHostAccess(s, hosts)} /></td>
          <td className="muted">{checked ? <time dateTime={checked} title={new Date(checked).toLocaleString()}>{relativeTime(checked)}</time> : "Not checked"}</td>
          <td><div className="actions row-actions">
            {registrationDiscardable(s) ? <button type="button" className="sm" title="Remove this Controller record of a registration the pool master refused." disabled={busy || unavailable} onClick={() => void discardStorage(s)}>Discard failed registration</button> : <>
            <button type="button" className="sm" title={lifecycleTitle} disabled={busy || unavailable || !ownerFresh || !lifecycleReady} onClick={() => void recheck(s)}>Recheck</button>
            <button type="button" className="sm" title={lifecycleTitle} disabled={busy || unavailable || !ownerFresh || s.tombstone || !lifecycleReady || coordinatedLifecycleUnavailable} onClick={() => beginEdit(s)}>Update{needsCredential((s.kind || "local") as Kind) ? " / rotate credentials" : ""}</button>
            <button type="button" className="sm danger" title={lifecycleTitle} disabled={busy || unavailable || !ownerFresh || s.tombstone || !lifecycleReady || coordinatedLifecycleUnavailable} onClick={() => { setRemove(s); setDetachError(""); detach.current?.showModal(); }}>Detach</button></>}
          </div></td>
        </tr>;
      })}</tbody></table></div> : <p className="empty">No storage repositories reported. Register an existing local directory, NFS export, ZFS dataset, SMB share or iSCSI filesystem.</p>}
      <About title="About storage">
        <p>Register storage connects an NFS export, SMB share, iSCSI LUN, local directory or existing ZFS dataset. Add ZFS storage builds new local storage from spare disks.</p>
        <p>Core rechecks identity and access before VM disk, media and power operations. NFS, SMB and clustered GFS2 iSCSI use a member only after Core confirms its current writable, QEMU and locking access; capacity is reported once for the repository. Local, ZFS and ordinary/exclusive iSCSI remain assigned to one Core.</p>
      </About>
    </Section>
    {(() => { const repositories = Array.from(new Map(zfsOutcomes.map(result => [result.repository_id, result])).values()); return repositories.length ? <Section icon="storage" title="ZFS storage by host">{repositories.map(repository => <div key={repository.repository_id}><h3>{repository.name}</h3><div className="table-scroll"><table><thead><tr><th>Host</th><th>Status</th><th>Layout</th><th>Action</th></tr></thead><tbody>{hosts.filter(host => !host.revoked && (repository.scope === "pool" || host.id === repository.host_id)).map(host => { const result = zfsOutcomes.find(item => item.repository_id === repository.repository_id && item.host_id === host.id); const access = storage.find(s => s.id === repository.repository_id)?.zfs_host_access?.find(item => item.host_id === host.id); const pool = access?.pool; const poolFresh = host.connection === "fresh" && storageObservationFresh(access?.observed_at); const replaceable = zfsReplaceableDevices(pool); const scanning = poolFresh && pool?.scan?.state === "SCANNING"; const beingReplaced = poolFresh && (pool?.devices || []).some(device => device.parent === "replacing" || device.parent === "spare"); return <tr key={host.id}><td>{host.hostname}</td><td>{result ? <><Badge value={result.status} />{result.error && <small>{result.error}</small>}</> : "Not set up"}{pool && <ZFSPoolLines pool={pool} prefix={poolFresh ? undefined : "Last report"} />}</td><td>{result?.layout || "—"}</td><td>{scanning ? "resilvering" : beingReplaced ? "being replaced" : pool && replaceable.length && poolFresh && host.capabilities?.includes("storage-zfs-replace-v1") && host.capabilities?.includes("storage-discovery-v1") ? <button type="button" className="danger" disabled={busy || unavailable} onClick={() => setReplaceZFS({ repositoryID: repository.repository_id, name: repository.name, host })}>Replace disk</button> : !result && host.connection === "fresh" && host.capabilities?.includes("storage-zfs-provision-v1") ? <button type="button" disabled={busy || unavailable || zfsProvision} onClick={() => { setConfigureZFS({ ...repository, host_id: host.id }); setZFSProvision(true); setForm(false); }}>Add this host</button> : "—"}</td></tr>; })}</tbody></table></div></div>)}</Section> : null; })()}
    {replaceZFS && <ZFSReplaceDialog poolID={poolID} repositoryID={replaceZFS.repositoryID} host={replaceZFS.host} pool={storage.find(s => s.id === replaceZFS.repositoryID)?.zfs_host_access?.find(access => access.host_id === replaceZFS.host.id)?.pool} api={api} unavailable={unavailable} onClose={() => setReplaceZFS(null)} onSubmitted={(failed, replacement) => { void reload(); onNotice(`Disk replacement submitted for ${replaceZFS.name} on ${replaceZFS.host.hostname}: ${replacement} replaces ${failed}. The resilver is shown on the repository; ZFS retires the old disk itself when the resilver finishes.`); }} />}
    <ClusteredBlockStatus poolID={poolID} masterHostID={masterHostID} hosts={hosts} api={api} unavailable={unavailable} />
    <dialog ref={browserDialog} className="confirm-dialog storage-browser" aria-labelledby="storage-browser-title" onCancel={event => { event.preventDefault(); closeBrowser(); }} onClose={() => { if (!browserDialog.current?.open) cancelBrowser(); }}>{browser && <><h2 id="storage-browser-title">{browser.kind === "smb" ? "Browse shared folders" : "Browse exports"}</h2><p className="mono storage-browser-path">{browser.kind === "smb" ? `//${draft.server}${browser.path}` : `${draft.server}:${browser.path || "/"}`}</p><div className="actions"><button type="button" title="Back / Up" disabled={browser.loading || !browser.path} onClick={browseUp}>Up</button><button type="button" disabled={browser.loading} onClick={() => void browse(browser.kind, browser.path, browser.base)}>Refresh</button></div>{browser.loading ? <p role="status">Loading folders…</p> : browser.error ? <><p className="alert" role="alert">{browser.error}</p><button type="button" onClick={() => void browse(browser.kind, browser.path, browser.base)}>Retry</button></> : browser.candidates.length ? <><ul className="storage-browser-list">{browser.candidates.map((candidate, index) => <li key={`${candidate.path}-${index}`}><button type="button" className="storage-browser-folder" aria-label={candidate.existing ? `Open ${candidate.path}, existing repository` : `Open ${candidate.path}`} aria-pressed={browser.selected?.path === candidate.path} onClick={() => setBrowser({ ...browser, selected: candidate })} onDoubleClick={() => openFolder(candidate)}><span className="storage-browser-name">{candidate.path}</span>{candidate.existing && <span className="storage-existing-badge" title="Existing repository">E</span>}</button></li>)}</ul>{browser.candidates.length >= 64 && <p className="help">Showing up to 64 results; use a known path manually if your folder is missing.</p>}</> : <p className="empty">No folders were found. Enter the source manually or choose another location.</p>}<div className="actions"><button type="button" disabled={browser.loading || !browser.selected} onClick={() => browser.selected && openFolder(browser.selected)}>Open folder</button><button type="button" onClick={closeBrowser}>Cancel</button><button type="button" className="primary" disabled={browser.loading || !browser.selected} onClick={selectFolder}>Select folder</button></div></>}</dialog>
    <dialog ref={detach} className="confirm-dialog" aria-labelledby="detach-storage-title" onClose={() => { setRemove(null); setDetachError(""); }}>{remove && <form onSubmit={e => { e.preventDefault(); void detachStorage(); }}><h2 id="detach-storage-title">Detach {remove.name}?</h2><p>Confirm the repository name to remove Nex’s configuration. This does not delete data.</p>{detachError && <p className="alert" role="alert">{detachError}</p>}<label>Repository name<input required autoFocus pattern={remove.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")} /></label><div className="actions"><button type="button" disabled={busy} onClick={() => detach.current?.close()}>Cancel</button><button className="danger" disabled={busy}>{busy ? "Detaching…" : "Detach storage"}</button></div></form>}</dialog>
  </>;
}
