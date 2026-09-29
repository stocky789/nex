// SPDX-License-Identifier: AGPL-3.0-only
import { Fragment, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { ConsoleMedia, Guest } from "./console";
import { Icon } from "./icons";
import { VMWizard, adapterNames, busNames, cpuNames, describeNetwork, guestNames, readyHost, secureBootText, tpmText, type DiskRow, type NICRow, type PlacementState } from "./vm-create";
import { VMEditor, editBlocker } from "./vm-edit";
import { StorageRepositories, discardConfirmText, registrationDiscardable, storageOwnerFresh, storageServesData } from "./storage";
import { About, AccessLights, Badge, Menu, Section, type HostAccess } from "./ui";
import { capitalize } from "./labels";
import { DownloadProgress, UploadProgress, isoDownload, type Upload } from "./rail-screens";

export type Registration = { id: string; pool_id: string; host_id: string; name: string; operation_id: string; status: string; error?: string };
export type StorageSource = { nfs?: { server: string; export: string; directory?: string }; zfs?: { pool: string; dataset: string; mount_path: string }; smb?: { server: string; share: string; directory?: string }; iscsi?: { portal: string; target_iqn: string; lun: number; wwid: string; filesystem_uuid: string; exclusive_target: boolean; mode?: "exclusive" | "clustered_gfs2"; cluster_id?: string; lock_table?: string; eligible_host_ids?: string[] } };
// `member_access` is a Core observation from the pool master. It is deliberately
// separate from repository configuration: Controller uses it only to render and
// preflight placement; Core rechecks access and writer safety for the operation.
// ZFSPoolStatus is Core's `zpool status` observation, passed through by the Controller.
export type ZFSPoolStatus = { name?: string; state: string; status?: string; action?: string; scan?: { function: string; state: string; examined?: string; to_examine?: string; issued?: string; errors?: string }; devices?: { name: string; path?: string; state: string; parent?: string; read_errors?: string; write_errors?: string; checksum_errors?: string }[] };
export type Storage = Registration & { path: string; kind?: string; source?: StorageSource; identity?: Record<string, unknown>; generation?: number; tombstone?: boolean; credential_present?: boolean; zfs_provisioned?: boolean; zfs_scope?: "host" | "pool"; capabilities?: { vm_disk?: boolean; iso?: boolean; thin?: boolean; thick?: boolean; shared?: boolean; eligible_host_ids?: string[] }; access?: { status?: string; readable?: boolean; writable?: boolean; available_bytes?: number; total_bytes?: number; observed_at?: string; error?: string; zfs_pool?: ZFSPoolStatus }; member_access?: { repository_id: string; host_id: string; generation: number; identity: string; readable: boolean; writable: boolean; qemu_writable: boolean; locking: boolean; observed_at?: string; error?: string }[]; zfs_host_access?: { host_id: string; pool_guid?: string; dataset_guid?: string; path?: string; readable: boolean; writable: boolean; total_bytes?: number; available_bytes?: number; observed_at?: string; error?: string; pool?: ZFSPoolStatus }[] };
export type Network = Registration & { kind: string; source: string; uplink?: string; vlan?: number; generation?: number; member_access?: { network_id: string; host_id: string; generation: number; bridge?: string; uplink_present: boolean; observed_at?: string; error?: string }[] };
export type Media = Registration & { storage_id: string; size_bytes: number; sha256: string; member_access?: { media_id: string; host_id: string; storage_id: string; sha256?: string; observed_at?: string; error?: string }[] };
// Arrays are nullable because Go marshals an empty slice as null; render accordingly.
// boot.secure_boot and boot.tpm are absent from a definition written before those fields
// existed: an unset secure_boot on a UEFI VM is Secure Boot on, and an unset tpm is no TPM.
// drivers_media_id is the ISO in the VM's second CD drive, empty when that drive is empty.
export type CPUFeature = { name: string; policy: "require" | "disable" };
export type VMCPU = { count: number; sockets: number; cores: number; threads: number; model: string; features?: CPUFeature[] | null };
export type VMSpec = { revision?: number; description: string; guest_os: string; cpu: VMCPU; memory: { mode: string; size_mib: number }; disks: DiskRow[] | null; nics: NICRow[] | null; boot: { firmware: string; machine: string; media_boot_order: number; secure_boot?: string; tpm?: string }; media_id: string; drivers_media_id?: string; start_after_create: boolean; autostart: boolean; tags?: string[] | null };
export type VMUsage = { observed_at?: string; sample_window_ms?: number; cpu_percent?: number | null; memory_assigned_bytes?: number; memory_maximum_bytes?: number; memory_host_bytes?: number | null; disk_used_bytes?: number | null; disk_capacity_bytes?: number | null };
export type VM = Registration & { spec?: VMSpec; state: string; freshness: string; role: string; protected: boolean; guest?: Guest | null; detected_os?: { os: string; os_id?: string; observed_at: string } | null; usage?: VMUsage | null };
type NIC = { name: string; mac: string; kind?: string; master?: string; parent?: string; state: string; addresses?: string[] | null };
// inventory is the host's last stored report, returned even while the host is stale; its vms
// carry the last state the host reported for each VM.
export type Host = { id: string; pool_id: string; hostname: string; connection: string; revoked: boolean; capabilities?: string[]; inventory: { observed_at?: string; cpu_count: number; cpu_models?: string[] | null; cpu_baseline?: { model: string; features: CPUFeature[] | null; machine: string; generation: number; member_count: number; observed_at: string; error?: string } | null; memory_bytes: number; nics?: NIC[] | null; vms?: { id: string; state: string }[] | null; managed?: { iso_downloads?: { operation_id: string; received_bytes: number; total_bytes?: number }[] | null } | null } | null };
type API = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Props = { account: string; poolID: string; poolName: string; masterHostID: string; tab: string; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[]; vms: VM[]; api: API; reload: () => Promise<void>; setTab: (tab: string) => void; unavailable: boolean; upload: Upload | null; onCancelUpload: () => void; startIsoUpload: <T>(path: string, file: File) => Promise<T>; creating: boolean; onCloseCreate: () => void; onNotice?: (text: string, operationID?: string) => void };
const GiB = 1024 ** 3;
// sentence ends a message with a full stop when it has no terminal punctuation of its own.
export const sentence = (text: string) => /[.!?]$/.test(text) ? text : `${text}.`;
const task = (value: Registration) => <><Badge value={value.status} />{value.error && <p className="task-error">{value.error}</p>}{value.operation_id && <small><a className="task-link" href="#/tasks" title={`Task ${value.operation_id}`}>Task {value.operation_id}</a></small>}</>;

// Each registration form belongs to the screen that opens it; "" means no form is open. The ISO
// library sits under Storage.
export const formTab = (form: string) => ({ vm: "Virtual machines", network: "Networking", iso: "Storage" })[form] || "";

// Last path segment of a direct download URL, for prefilling the ISO name field.
export function isoNameFromURL(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  let base = "";
  try {
    base = decodeURIComponent(new URL(trimmed).pathname.split("/").filter(Boolean).pop() || "");
  } catch {
    // Incomplete while typing: still pick up a trailing .iso segment when present.
    const match = trimmed.match(/\/([^/?#]+\.iso)(?:[?#]|$)/i);
    if (match) {
      try { base = decodeURIComponent(match[1]); } catch { base = match[1]; }
    }
  }
  if (!base || base === "." || base === ".." || !/\.iso$/i.test(base)) return "";
  return base.slice(0, 100);
}

// isoTransferNotice describes what is happening to an ISO the operator just uploaded or asked
// the host to download, from the record the host reports now. The status the submission
// returned is only its first word: a banner that keeps saying "queued. Wait for a succeeded
// result" while the same page shows the iso-import task succeeded is wrong (BUG-0204).
// `status` is undefined until the ISO library lists the record.
export function isoTransferNotice(name: string, status?: string, source: "upload" | "download" = "upload"): string {
  const work = source === "download" ? "host download" : "host transfer";
  switch (status) {
    case "succeeded":
      return `${name} is on the host and can be used as install media or a driver disc.`;
    case "failed":
      return `The ${work} of ${name} failed, so it cannot be used by a VM. Read the reason in Tasks.`;
    case "needs_attention":
      return `The ${work} of ${name} needs attention: the host did not confirm it, so whether the file is usable is unknown. Check it in Tasks before using it or sending it again.`;
    case undefined:
      return `${name} has no ${work} record yet. It is not usable by a VM until one reports succeeded.`;
    default:
      return `The ${work} of ${name} is ${status}. Wait for a succeeded result before using it in a VM.`;
  }
}

// Only imports that never became host-held media can be discarded. Core owns a succeeded ISO,
// while queued and dispatched imports still belong to their host task (BUG-0203).
export const mediaDiscardable = (media: Media) => media.status === "failed" || media.status === "needs_attention";

// A create the Controller accepted may carry placement warnings from its mirror of the master's
// last report. They never refuse and never change the request: the pool master decides.
export const placementWarningNotice = (warnings: string[]) => warnings.map(sentence).join(" ");

// --- Pool resource removal and rename -------------------------------------------------------
// Removing a pool VLAN network or an imported ISO is work the pool master does: Core tears the
// resource down, records the removal durably and every member converges on its next pass. The
// Controller only submits the request and shows what the hosts report, so nothing here deletes a
// row by itself — the row goes when the master reports it gone.

// Core's generations are 1-based on the wire, with 0 meaning "written before generations existed".
export const resourceGeneration = (value?: number) => (value || 0) > 0 ? value as number : 1;

// Only a pool VLAN network is Nex's to rename or remove. A host-automatic bridge or an existing
// libvirt network was not created by Nex and Core refuses to touch it.
export const networkRemovable = (n: Network) => n.kind === "vlan";
// A succeeded import is host-held media; a failed one is still only a Controller request row,
// which keeps its existing Discard action instead.
export const mediaRemovable = (m: Media) => m.status === "succeeded";

// The typed confirmation must match the name exactly: no trimming, no case folding. A name that
// differs only by a space the operator cannot see must not arm a destructive button.
export const confirmMatches = (typed: string, name: string) => name !== "" && typed === name;

// The register-network form's name rule (required, at most 100 characters) written out for the
// rename dialog, plus the two characters Core itself refuses in a resource name.
export const validResourceName = (name: string) => {
  const value = name.trim();
  return value.length > 0 && value.length <= 100 && !value.includes("/") && !/[\u0000-]/.test(value);
};
// Submitting the name a network already has is a no-op, not an empty rename request.
export const renameChanged = (current: string, next: string) => validResourceName(next) && next.trim() !== current;

// Why a removal or rename cannot be submitted right now; "" means it can. Order matters: state
// the most fundamental reason, so an operator is not told to reconnect a host when the view they
// are looking at is the stale part.
export function lifecycleBlocker(o: { unavailable: boolean; inFlight: boolean; ready: boolean; hostFresh: boolean }): string {
  if (o.unavailable) return "Controller inventory may be out of date. Use Retry above to reload it before changing this pool resource.";
  if (!o.ready) return "Wait for Nex Core to confirm this resource before changing it.";
  if (!o.hostFresh) return "The pool master is not freshly connected. Reconnect it before changing this pool resource.";
  if (o.inFlight) return "A request for this resource is already in flight. Wait for Nex Core to report its result.";
  return "";
}

// Request bodies. The Controller resolves the target host itself (the pool master), so the body
// names only the pool and the generation the operator acted on: a view that has moved on is
// refused instead of removing something that changed underneath it.
export const networkRemoveBody = (n: Network, poolID: string) => ({ pool_id: poolID, expected_generation: resourceGeneration(n.generation) });
export const networkRenameBody = (n: Network, poolID: string, name: string) => ({ ...networkRemoveBody(n, poolID), name: name.trim() });

// One request identity per unchanged request: a retry after a lost reply replays the same
// request_id so Core returns the original outcome, while a changed request (another new name)
// mints a new one. Kept in memory for this view only — no pool object enters browser storage.
export function stableRequestID(store: Record<string, { key: string; id: string }>, scope: string, body: unknown, mint: () => string = () => crypto.randomUUID()) {
  const key = JSON.stringify(body);
  if (store[scope]?.key !== key) store[scope] = { key, id: mint() };
  return store[scope].id;
}

// One request identity per unchanged signature: a retry after a lost reply replays the same id.
export function heldRequestID(held: { id: string; signature: string }, signature: string, mint: () => string = () => crypto.randomUUID()) {
  if (held.signature !== signature || !held.id) { held.id = mint(); held.signature = signature; }
  return held.id;
}

// Per-host convergence for a pool network, as the master reports it. A member still carrying an
// older generation has not applied the current name or settings yet; saying "ready" there would
// claim a convergence that has not happened.
export function networkMemberState(n: Network, a: NonNullable<Network["member_access"]>[number]): string {
  if (a.error) return a.error;
  if (resourceGeneration(a.generation) !== resourceGeneration(n.generation)) return "converging";
  return a.uplink_present ? "ready" : "pending";
}

// One access light per pool host for a network. The host that registered it (the pool master,
// for a pool VLAN) confirms it with the registration; the master reports each member's apply of
// a pool VLAN; a host network exists only on its own host. No current report is Unknown.
export function networkHostAccess(n: Network, hosts: Host[]): HostAccess[] {
  const owner = hosts.find(h => h.id === n.host_id)?.hostname || n.host_id;
  return hosts.map(host => {
    const light = (tone: HostAccess["tone"], label: string, detail = "") => ({ host, tone, label, detail });
    if (n.kind !== "vlan" && host.id !== n.host_id) return light("neutral", "No access", `Host network on ${owner}; it exists only there.`);
    if (host.revoked || host.connection !== "fresh") return light("unknown", "Unknown", "The host is not reporting, so its network state is not current.");
    const a = n.kind === "vlan" ? n.member_access?.find(item => item.host_id === host.id) : undefined;
    if (!a && host.id === n.host_id) return n.status === "succeeded" ? light("ok", "Accessible", "Confirmed by this host when the network was registered.") : light("unknown", "Unknown", "The host has not confirmed this network yet.");
    if (!a) return light("unknown", "Unknown", "The pool master has not reported this host's state for this network.");
    const state = networkMemberState(n, a);
    return state === "ready" ? light("ok", "Accessible", `Bridge ${a.bridge || "present"} on uplink ${n.uplink || "—"}.`)
      : state === "converging" ? light("warn", "Converging", "This host has not applied the current settings yet.")
      : state === "pending" ? light("warn", "Uplink missing", `This host does not report the uplink NIC${n.uplink ? ` ${n.uplink}` : ""}.`)
      : light("bad", "Error", state);
  });
}

type Lifecycle = { scope: string; resource: "network" | "media"; action: "remove" | "rename"; id: string; name: string; base: Record<string, unknown>; note?: string };

type Pending = Record<string, { payload: string; id: string }>;
function pendingRequests(key: string): Pending {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || "{}");
    if (value && typeof value === "object" && Object.values(value).every(v => v && typeof v === "object" && "id" in v && typeof v.id === "string" && "payload" in v && typeof v.payload === "string")) return value;
  } catch {}
  return {};
}

export function ManagedVMs({ account, poolID, poolName, masterHostID, tab, hosts: allHosts, storage: allStorage, networks: allNetworks, media: allMedia, vms: allVMs, api, reload, setTab, unavailable, upload, onCancelUpload, startIsoUpload, creating, onCloseCreate, onNotice }: Props) {
  const uploading = upload?.state === "uploading";
  const hosts = allHosts.filter(h => h.pool_id === poolID);
  const hostIDs = hosts.map(host => host.id).join(",");
  const storage = allStorage.filter(s => s.pool_id === poolID);
  const networks = allNetworks.filter(n => n.pool_id === poolID);
  const media = allMedia.filter(m => m.pool_id === poolID);
  const vms = allVMs.filter(v => v.pool_id === poolID);
  const persistenceKey = `nex-pending/${account}/${poolID}`;
  const requests = useRef<Pending>(pendingRequests(persistenceKey));
  const restored = (scope: string) => { try { return JSON.parse(requests.current[scope]?.payload || "{}"); } catch { return {}; } };
  const [form, setForm] = useState(requests.current.vm ? "vm" : requests.current.iso ? "iso" : requests.current.storage ? "storage" : requests.current.network ? "network" : "");
  const [busy, setBusy] = useState(false);
  const [placement, setPlacement] = useState<Record<string, PlacementState> | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [file, setFile] = useState<File | null>(null);
  // The ISO whose host transfer this view is following, so its banner can report the record the
  // host reports now instead of the status the submission happened to return.
  const [transfer, setTransfer] = useState<{ id: string; name: string; source: "upload" | "download" } | null>(null);
  const [uploadStorage, setUploadStorage] = useState<string>(restored("iso").storage_id || "");
  const [isoSource, setIsoSource] = useState(restored("iso").url ? "url" : "file");
  const [isoURL, setIsoURL] = useState<string>(restored("iso").url || "");
  const [isoName, setIsoName] = useState<string>(restored("iso").url ? restored("iso").name : "");
  const downloadHost = hosts.find(h => h.id === storage.find(s => s.id === uploadStorage)?.host_id);
  const canDownload = Boolean(downloadHost && readyHost(downloadHost) && downloadHost.capabilities?.includes("iso-download-v1"));
  const isoReadyStorage = storage.filter(s => s.status === "succeeded" && storageServesData(s.access?.status) && s.access?.writable && s.capabilities?.iso && storageOwnerFresh(s, hosts));
  const [networkHost, setNetworkHost] = useState<string>(restored("network").host_id || "");
  const [connection, setConnection] = useState<string>(() => { const r = restored("network"); return r.kind === "vlan" ? `vlan:${r.uplink}` : r.kind ? "custom" : ""; });
  const lock = useRef(false);
  const mounted = useRef(true);
  useEffect(() => () => { mounted.current = false; }, []);
  useEffect(() => {
    if (form !== "vm") return;
    let live = true;
    setPlacement(null);
    api<{ authoritative?: boolean; master_connection?: string; members?: { host_id: string; maintenance?: boolean | null }[] }>(`pools/${encodeURIComponent(poolID)}/members`)
      .then(report => {
        if (!live) return;
        const unknown = report.authoritative
          ? "The pool master did not report this host's maintenance state."
          : `The pool master's membership report is ${report.master_connection || "not current"}; maintenance state is unknown.`;
        setPlacement(Object.fromEntries(hosts.map(host => {
          const member = (report.members || []).find(item => item.host_id === host.id);
          return [host.id, member && typeof member.maintenance === "boolean"
            ? { maintenance: member.maintenance }
            : { reason: unknown }];
        })));
      }, () => { if (live) setPlacement(Object.fromEntries(hosts.map(host => [host.id, { reason: "The pool master did not report maintenance state; wait for a fresh report before placing a VM." }]))) });
    return () => { live = false; };
  }, [api, form, poolID, hostIDs]);
  const input = useRef<HTMLInputElement>(null);
  const hostName = (id: string) => hosts.find(h => h.id === id)?.hostname || id;
  function persist() {
    try { sessionStorage.setItem(persistenceKey, JSON.stringify(requests.current)); }
    catch { setNotice("Browser storage is unavailable. Keep this page open until the task is confirmed so a retry can use the same request."); }
  }
  // An ISO upload can finish after this view unmounts, so re-read the stored map before clearing
  // an entry rather than persisting a snapshot another view has since replaced.
  function clearRequest(scope: string) {
    delete requests.current[scope];
    const stored = pendingRequests(persistenceKey);
    delete stored[scope];
    try { sessionStorage.setItem(persistenceKey, JSON.stringify(stored)); } catch {}
  }
  const nics = hosts.find(h => h.id === networkHost)?.inventory?.nics || [];
  const uplinks = nics.filter(n => !n.kind || n.kind === "physical" || n.kind === "bond");
  const bridges = nics.filter(n => n.kind === "bridge");
  function requestID(scope: string, payload: unknown) {
    const encoded = JSON.stringify(payload);
    if (requests.current[scope] && requests.current[scope].payload !== encoded) throw new Error("An earlier submission has an uncertain outcome. Restore its original entries and retry to reconcile that request, or inspect Tasks before starting another request.");
    requests.current[scope] ??= { payload: encoded, id: crypto.randomUUID() };
    persist();
    return requests.current[scope].id;
  }
  // An ISO upload runs unblocking: it must not disable Close or the rest of the pool view while
  // the file streams to the Controller. Its own guard is the shell's single-upload state.
  async function run(scope: string, action: () => Promise<void>, blocking = true) {
    if (blocking) {
      if (lock.current) return;
      lock.current = true;
      setBusy(true);
    }
    setError(""); setNotice("");
    try { await action(); } catch (e) {
      const code = (e as Error & { status?: number }).status;
      // An aborted request never reached the Controller, so its identity can be released.
      if ((e as Error & { cancelled?: boolean }).cancelled) { clearRequest(scope); return; }
      if ([400, 401, 403, 404, 413, 507].includes(code || 0)) clearRequest(scope);
      if (code === 409) {
        try {
          const current = await api<{ request_id: string }[]>("operations");
          if (current.length < 1000 && !current.some(op => op.request_id === requests.current[scope]?.id)) clearRequest(scope);
        } catch { /* Keep the original identity while the outcome is uncertain. */ }
      }
      setError((e as Error).message);
    }
    finally { if (blocking) { lock.current = false; setBusy(false); } }
  }
  function open(name: string) { setForm(name); setError(""); setNotice(""); }
  // Create VM on the VMs screen shows the wizard; an open one keeps its progress.
  useEffect(() => { if (creating) open("vm"); }, [creating]);
  function chooseFile(value: File | null) {
    if (busy || uploading) return;
    setFile(null); setError("");
    if (!value) return;
    if (!/\.iso$/i.test(value.name) || value.size < 32774) {
      setError("Choose an ISO image ending in .iso of at least 32 KiB."); return;
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
      await api<Registration>(kind === "storage" ? "storage" : "networks", "POST", { ...payload, request_id: requestID(kind, payload) });
      clearRequest(kind);
      setForm(""); await reload(); setNotice(`Registration request submitted for ${values.name}. Follow host validation in Tasks; registration does not make local storage shared.`);
    });
  }
  async function create(payload: Record<string, unknown>) {
    await run("vm", async () => {
      const result = await api<{ vm_id: string; operation: { id: string }; warnings?: string[] }>("vms", "POST", { ...payload, request_id: requestID("vm", payload) });
      clearRequest("vm");
      // The creation operation rides along in the route, so a create that fails after this 202
      // still names its task once the VM listing no longer includes it.
      await reload(); if (mounted.current) { setForm(""); onCloseCreate(); location.hash = `/vms/${result.vm_id}/${result.operation.id}`; }
      // Placement warnings come back with the accepted 202. This panel is about to unmount with
      // the route change, so they go to the shell's notice, which clears itself when the
      // operation leaves queued/dispatched.
      if (result.warnings?.length) onNotice?.(placementWarningNotice(result.warnings), result.operation.id);
    });
  }
  // Removal and rename of a pool VLAN network or a stored ISO. The dialog owns its own error so
  // the refusal appears where the operator is looking, and the request identity lives in a ref:
  // a retry of the same unchanged request replays it, a changed request gets a new one.
  const [lifecycle, setLifecycle] = useState<Lifecycle | null>(null);
  const [confirmText, setConfirmText] = useState("");
  const [renameText, setRenameText] = useState("");
  const [lifecycleError, setLifecycleError] = useState("");
  const [lifecycleBusy, setLifecycleBusy] = useState(false);
  const [inFlightScope, setInFlightScope] = useState("");
  const lifecycleRequests = useRef<Record<string, { key: string; id: string }>>({});
  const lifecycleDialog = useRef<HTMLDialogElement>(null);
  const lifecycleOpener = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    const element = lifecycleDialog.current;
    if (!element) return;
    if (lifecycle && !element.open) element.showModal();
    else if (!lifecycle && element.open) element.close();
  }, [lifecycle]);
  // Reasons a lifecycle action on this row cannot be submitted, from the host that owns it.
  function lifecycleState(hostID: string, resourceID: string, resourceStatus: string, scope: string) {
    const host = hosts.find(h => h.id === hostID);
    return lifecycleBlocker({
      unavailable, inFlight: inFlightScope === scope,
      ready: Boolean(resourceID) && resourceStatus === "succeeded",
      hostFresh: Boolean(host && readyHost(host)),
    });
  }
  function beginLifecycle(event: { currentTarget: HTMLButtonElement }, target: Lifecycle) {
    lifecycleOpener.current = event.currentTarget;
    setLifecycleError(""); setConfirmText(""); setRenameText(target.action === "rename" ? target.name : "");
    setLifecycle(target);
  }
  async function submitLifecycle() {
    if (!lifecycle || lifecycleBusy) return;
    const rename = lifecycle.action === "rename";
    // Nothing to ask a host for: the name is unchanged, or the confirmation does not match.
    if (rename && !renameChanged(lifecycle.name, renameText)) { setLifecycle(null); return; }
    if (!rename && !confirmMatches(confirmText, lifecycle.name)) return;
    const body = rename ? { ...lifecycle.base, name: renameText.trim() } : lifecycle.base;
    const request_id = stableRequestID(lifecycleRequests.current, lifecycle.scope, body);
    setLifecycleBusy(true); setLifecycleError(""); setInFlightScope(lifecycle.scope); setError(""); setNotice("");
    try {
      await api(`${lifecycle.resource === "network" ? "networks" : "media"}/${encodeURIComponent(lifecycle.id)}`, rename ? "PATCH" : "DELETE", { request_id, ...body });
      delete lifecycleRequests.current[lifecycle.scope];
      setLifecycle(null);
      await reload();
      setNotice(rename
        ? `Rename accepted for ${lifecycle.name}. The new name appears once the pool master reports it.`
        : lifecycle.resource === "network"
          ? `Removal accepted for ${lifecycle.name}. The pool master removes the VLAN bridge from every host in the pool; the network disappears from this list once the master reports the removal.`
          : `Removal accepted for ${lifecycle.name}. The ISO disappears from this list once the pool master reports the file deleted.`);
    } catch (e) {
      // The identity is kept: retrying this unchanged request replays it rather than starting
      // a second removal whose outcome could differ from the first.
      setLifecycleError((e as Error).message);
    } finally { setLifecycleBusy(false); setInFlightScope(""); }
  }
  // A registration the pool master refused is only a Controller request record: this deletes that
  // row and relays nothing. A 409 (a host reports the network as applied) is shown exactly as the
  // Controller worded it, and the row survives.
  function discardNetwork(n: Network) {
    if (!confirm(discardConfirmText("network", n.name))) return;
    void run(`network:${n.id}:discard`, async () => {
      await api(`networks/${encodeURIComponent(n.id)}`, "DELETE");
      await reload();
      setNotice(`Discarded the failed registration for ${n.name}. Register the network again to retry.`);
    });
  }
  function discardMedia(media: Media) {
    if (!confirm(`Discard ${media.name}? This removes the failed import record and any Controller staging bytes. The task and audit history are kept.`)) return;
    void run("media-discard", async () => {
      try {
        await api(`media/${encodeURIComponent(media.id)}`, "DELETE");
      } catch (error) {
        let current: Media[];
        try { current = await api<Media[]>("media"); } catch { throw error; }
        if (current.some(entry => entry.id === media.id)) throw error;
      }
      await reload();
      setNotice(`Discarded the failed import for ${media.name}. Submit the download again to retry.`);
    });
  }
  const relevant = ["Virtual machines", "Storage", "Networking"].includes(tab);
  const shows = (section: string) => tab === section;
  // An open form stays mounted across tab switches so restored entries and wizard progress survive
  // a trip to another tab (the wizard sends the operator to Storage and Networks),
  // but it is only shown on the tab that opened it instead of sitting on top of an unrelated one.
  const showForm = Boolean(form) && shows(formTab(form));
  // The banner for an ISO the operator just sent follows that record's own status.
  const transferStatus = transfer && media.find(m => m.id === transfer.id)?.status;
  return <div hidden={!relevant}>
    {error && <p className="alert" role="alert">{sentence(error)} Your entries are retained. Review Tasks before retrying an uncertain result.</p>}
    {notice && <p className="notice" role="status">{notice} <a href="#/tasks">Tasks</a></p>}
    {transfer && <p className={transferStatus === "failed" ? "alert" : transferStatus === "needs_attention" ? "warning" : "notice"} role="status">
      {isoTransferNotice(transfer.name, transferStatus || undefined, transfer.source)} <a href="#/tasks">Tasks</a>
      <button type="button" onClick={() => setTransfer(null)}>Dismiss</button>
    </p>}
    {unavailable && <p className="warning">Controller inventory may be out of date. Use Retry above to reload it before submitting changes.</p>}
    {form === "network" && <section className="panel form-panel" aria-label="Register network" hidden={!showForm}>
      <div className="section-heading"><h2>Register guest network</h2><button disabled={busy} onClick={() => setForm("")}>Close</button></div>
      <form onSubmit={e => register(e, "network")}>
        <label>Name<input name="name" required maxLength={100} autoFocus defaultValue={restored(form).name || ""} /></label>
        <label>Host<select name="host_id" required defaultValue={restored(form).host_id || ""} onChange={e => { setNetworkHost(e.target.value); setConnection(""); }}><option value="">Select host</option>{hosts.map(h => <option key={h.id} value={h.id} disabled={!readyHost(h)}>{h.hostname} · {h.connection}{!h.capabilities?.includes("managed-vm-v1") ? " · agent upgrade required" : ""}</option>)}</select></label>
        <>
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
        </>
        {!hosts.some(readyHost) && <p className="warning">A freshly connected host with managed-VM support is required. Enroll, reconnect or upgrade the host agent first.</p>}
        <button className="primary" disabled={busy || unavailable || !hosts.some(readyHost)}>{busy ? "Submitting…" : "Register and check access"}</button>
      </form>
    </section>}
    {form === "iso" && <section className="panel form-panel" aria-label="Upload ISO" hidden={!showForm}>
      <div className="section-heading"><h2>Upload guest ISO</h2><button onClick={() => setForm("")}>Close</button></div>
      <form onSubmit={e => { e.preventDefault();
        if (isoSource === "url") {
          if (!canDownload || unavailable) return;
          const payload = { storage_id: uploadStorage, name: isoName.trim(), url: isoURL.trim() };
          void run("iso", async () => {
            const result = await api<Media>("media/download", "POST", { ...payload, request_id: requestID("iso", payload) });
            clearRequest("iso");
            setTransfer({ id: result.id, name: payload.name, source: "download" });
            await reload();
          });
          return;
        }
        if (!file) { setError("Choose an ISO file first."); return; } const image = file; void run("iso", async () => {
        const payload = { storage_id: uploadStorage, name: image.name, size: image.size, modified: image.lastModified };
        const query = new URLSearchParams({ storage_id: uploadStorage, name: image.name, request_id: requestID("iso", payload) });
        const result = await startIsoUpload<Media>(`media?${query}`, image);
        clearRequest("iso");
        if (mounted.current) { setFile(null); if (input.current) input.current.value = ""; setTransfer({ id: result.id, name: image.name, source: "upload" }); }
        await reload();
      }, false); }}>
        <label>Destination storage<select required value={uploadStorage} onChange={e => setUploadStorage(e.target.value)} disabled={busy || uploading}><option value="">Select ISO-ready storage</option>{isoReadyStorage.map(s => <option key={s.id} value={s.id}>{s.name} · {hostName(s.host_id)}</option>)}</select></label>
        <label>ISO source<select value={isoSource} disabled={busy || uploading} onChange={e => { setIsoSource(e.target.value); setError(""); }}><option value="file">Upload from computer</option><option value="url">Download from public URL</option></select></label>
        {isoSource === "url" ? <>
          <label>Public ISO URL<input type="url" required maxLength={4096} value={isoURL} disabled={busy} onChange={e => {
            const url = e.target.value;
            const suggested = isoNameFromURL(url);
            setIsoURL(url);
            setIsoName(name => (!name.trim() || name === isoNameFromURL(isoURL)) ? (suggested || name) : name);
          }} placeholder="https://downloads.example.org/installer.iso" /></label>
          <label>ISO name<input required maxLength={100} pattern=".*[.][iI][sS][oO]" value={isoName} disabled={busy} onChange={e => setIsoName(e.target.value)} placeholder="installer.iso" /></label>
          <p className="help">Use a direct public HTTP or HTTPS download link. The name is filled from the URL path; edit it if you want. Nex Core downloads and validates the ISO on the selected host; the download continues if you close this page.</p>
          {uploadStorage && !canDownload && <p className="warning">URL downloads need a freshly connected host with download support. Reconnect or upgrade Nex Core.</p>}
        </> : <div className="iso-drop" onDragOver={e => e.preventDefault()} onDrop={e => { e.preventDefault(); if (e.dataTransfer.files.length !== 1) { setError("Drop one ISO image at a time."); return; } chooseFile(e.dataTransfer.files[0]); }}>
          <label>ISO file<input ref={input} type="file" accept=".iso,application/x-iso9660-image" disabled={busy || uploading} onChange={e => chooseFile(e.target.files?.[0] || null)} /></label>
          <p className="help">Drop one .iso image here or use the file picker. The Controller and host validate its content and available space.</p>
          {file && <p className="iso-filename">{file.name} · {(file.size / GiB).toFixed(2)} GiB</p>}
        </div>}
        {upload && showForm && <div className="iso-upload-status" role="status" aria-label="ISO upload to Controller">
          <div>
            <strong>Upload ISO</strong>
            <small>{upload.name}</small>
            {upload.message && <p className="task-error">{upload.message}</p>}
          </div>
          <Badge value={upload.state} />
          <UploadProgress upload={upload} onCancel={onCancelUpload} />
        </div>}
        {!isoReadyStorage.length && <p className="help">Register storage and wait for its assigned Core to report current ISO access first.</p>}
        <button className="primary" disabled={busy || uploading || unavailable || !isoReadyStorage.some(s => s.id === uploadStorage) || (isoSource === "url" ? !canDownload || !isoURL.trim() || !isoName.trim() : !file)}>{busy ? "Submitting…" : uploading ? "Uploading…" : isoSource === "url" ? "Download ISO" : "Upload and transfer ISO"}</button>
      </form>
    </section>}
    {form === "vm" && <div hidden={!showForm}><VMWizard poolID={poolID} poolName={poolName} hosts={hosts} placement={placement} storage={storage} networks={networks} media={media} restored={() => restored("vm")} create={create} busy={busy} unavailable={unavailable} setTab={setTab} onClose={() => { setForm(""); onCloseCreate(); }} /></div>}
    {shows("Networking") && <Section icon="network" title="Networks" count={networks.length} actions={<button type="button" className="sm primary" disabled={busy || unavailable} onClick={() => open("network")}>Register network</button>}>{networks.length ? <div className="table-scroll"><table className="records resources"><thead><tr><th>Name</th><th>VLAN</th><th>Uplink</th><th>Host access</th><th>VMs</th><th>Status</th><th>Actions</th></tr></thead><tbody>{[...networks].sort((a, b) => (a.vlan || 0) - (b.vlan || 0) || a.name.localeCompare(b.name)).map(n => {
      const scope = `network:${n.id || n.operation_id}`;
      const blocker = lifecycleState(masterHostID, n.id, n.status, scope);
      const users = vms.filter(v => v.spec?.nics?.some(i => i.network_id === n.id)).length;
      return <tr key={n.id || `op:${n.operation_id}`}><td><strong>{n.name}</strong><small>{n.kind === "vlan" ? "Pool VLAN" : `Host network on ${hostName(n.host_id)}`}</small></td><td>{n.kind !== "vlan" ? "—" : n.vlan ? n.vlan : "Untagged"}</td><td>{n.uplink ? <span className="mono">{n.uplink}</span> : describeNetwork(n)}</td><td><AccessLights items={networkHostAccess(n, hosts)} /></td><td>{users ? <a href={`#/vms?network=${encodeURIComponent(n.id)}`} title={`Show the VMs attached to ${n.name}`}>{users}</a> : 0}</td><td>{task(n)}</td><td>{registrationDiscardable(n) ? <div className="actions row-actions">
        <button type="button" className="sm" title="Remove this Controller record of a registration the pool master refused." disabled={busy || unavailable} onClick={() => discardNetwork(n)}>Discard failed registration</button>
      </div> : networkRemovable(n) ? <div className="actions row-actions">
        <button type="button" className="sm" title={blocker || "Change this pool network's name on the pool master."} disabled={busy || Boolean(blocker)} onClick={e => beginLifecycle(e, { scope: `${scope}:rename`, resource: "network", action: "rename", id: n.id, name: n.name, base: networkRemoveBody(n, poolID) })}>Rename</button>
        <button type="button" className="sm danger" title={blocker || "Remove this pool VLAN network from every host in the pool."} disabled={busy || Boolean(blocker)} onClick={e => beginLifecycle(e, { scope: `${scope}:remove`, resource: "network", action: "remove", id: n.id, name: n.name, base: networkRemoveBody(n, poolID), note: users ? `${users} VM definition${users === 1 ? "" : "s"} in this pool still use this network, so Nex Core will refuse the removal. Detach the network in each VM first.` : "" })}>Remove</button>
      </div> : <small>Managed on {hostName(n.host_id)}</small>}</td></tr>;
    })}</tbody></table></div> : <p className="empty">No networks reported yet.</p>}<About title="About changing networks"><p>Only pool VLAN networks are renamed or removed here; the pool master applies the change on every host and each member reports its own convergence. Changing a VLAN tag or uplink NIC means removing the network and adding it again. A registration the master refused is only a request record and is discarded instead.</p></About></Section>}
    {shows("Storage") && <StorageRepositories poolID={poolID} masterHostID={masterHostID} hosts={hosts} storage={storage} api={api} reload={reload} unavailable={unavailable} onNotice={setNotice} />}
    {shows("Storage") && <Section icon="storage" title="ISO library" count={media.length + (upload ? 1 : 0)} actions={<button type="button" className="sm primary" disabled={busy || unavailable || uploading} onClick={() => open("iso")}>Upload ISO</button>}>
      {upload && !(form === "iso" && showForm) && <div className="iso-upload-status" role="status" aria-label="ISO upload to Controller">
        <div>
          <strong>Upload ISO</strong>
          <small>{upload.name} · to Controller</small>
          {upload.message && <p className="task-error">{upload.message}</p>}
        </div>
        <Badge value={upload.state} />
        <UploadProgress upload={upload} onCancel={onCancelUpload} />
      </div>}
      {media.length ? <div className="table-scroll"><table><thead><tr><th>ISO</th><th>Host / storage</th><th>Size</th><th>Transfer task</th><th>Actions</th></tr></thead><tbody>{media.map(m => {
      const scope = `media:${m.id || m.operation_id}:remove`;
      const blocker = lifecycleState(m.host_id, m.id, m.status, scope);
      const users = vms.filter(v => v.spec?.media_id === m.id || v.spec?.drivers_media_id === m.id).length;
      const download = (m.status === "queued" || m.status === "dispatched") && isoDownload(hosts, m.host_id, m.operation_id);
      return <tr key={m.id || `op:${m.operation_id}`}><td>{m.name}<small className="mono">SHA-256: {m.sha256 || "Pending verification"}</small></td><td>{hostName(m.host_id)}<small>{storage.find(s => s.id === m.storage_id)?.name || m.storage_id}</small></td><td>{m.size_bytes > 0 ? `${(m.size_bytes / GiB).toFixed(2)} GiB` : "Unknown until downloaded"}</td><td>{task(m)}{download && <DownloadProgress name={m.name} download={download} />}</td><td>{mediaDiscardable(m) && <button type="button" className="sm" disabled={busy || unavailable} onClick={() => discardMedia(m)}>Discard failed import</button>}{mediaRemovable(m) && <button type="button" className="sm danger" title={blocker || "Delete this ISO file from its storage on the host. Nex Core refuses while the ISO is in any VM's CD drive."} disabled={busy || Boolean(blocker)} onClick={e => beginLifecycle(e, { scope, resource: "media", action: "remove", id: m.id, name: m.name, base: {}, note: users ? `${users} VM definition${users === 1 ? "" : "s"} in this pool still have this ISO in a CD drive, so Nex Core will refuse the removal. Eject it in each VM first.` : "" })}>Remove ISO</button>}</td></tr>;
    })}</tbody></table></div> : !upload ? <p className="empty">No ISO images uploaded. Select validated storage and upload installation media.</p> : null}<About title="About removing ISOs"><p>Removing an ISO deletes the file from its storage on the host. Nex Core refuses while the ISO is in any VM's CD drive; a failed import is only a request record and is discarded instead.</p></About></Section>}
    <dialog ref={lifecycleDialog} className="confirm-dialog" aria-labelledby="pool-lifecycle-title" onClose={() => { setLifecycle(null); setConfirmText(""); setRenameText(""); setLifecycleError(""); lifecycleOpener.current?.focus(); }}>
      {lifecycle && (lifecycle.action === "rename"
        ? <form onSubmit={e => { e.preventDefault(); void submitLifecycle(); }}>
          <h2 id="pool-lifecycle-title">Rename {lifecycle.name}</h2>
          <p>The pool master records the new name and every host in the pool picks it up on its next pass. The VLAN tag, the uplink NIC and the VMs on this network are unchanged.</p>
          <p className="help">To change the VLAN tag or the uplink NIC, remove this network and add it again. Nex Core never re-plumbs an existing network.</p>
          {lifecycleError && <p className="alert" role="alert">{lifecycleError}</p>}
          <label>Network name<input required autoFocus maxLength={100} value={renameText} disabled={lifecycleBusy} onChange={e => setRenameText(e.target.value)} /></label>
          <div className="actions">
            <button type="button" disabled={lifecycleBusy} onClick={() => setLifecycle(null)}>Cancel</button>
            <button className="primary" disabled={lifecycleBusy || !renameChanged(lifecycle.name, renameText)}>{lifecycleBusy ? "Submitting…" : "Rename network"}</button>
          </div>
        </form>
        // Not a form: the destructive button must never be reachable by pressing Enter in the
        // confirmation field, only by clicking the button itself.
        : <div>
          <h2 id="pool-lifecycle-title">Remove {lifecycle.resource === "network" ? "network" : "ISO"} {lifecycle.name}?</h2>
          {lifecycle.resource === "network"
            ? <><p>The pool master removes this VLAN bridge from every host in the pool and records the removal durably. Nex Core refuses while any VM still uses this network, running or stopped. The management network is never touched.</p>
              <p className="help">Type the network name to confirm. The network disappears from the list once the pool master reports the removal, not before.</p></>
            : <><p>The pool master deletes this ISO file from its storage. Nex Core refuses while the ISO is in any VM's CD drive, running or stopped. No VM disk is touched.</p>
              <p className="help">Type the ISO name to confirm. The ISO disappears from the list once the pool master reports the file deleted, not before.</p></>}
          {lifecycle.note && <p className="warning">{lifecycle.note}</p>}
          {lifecycleError && <p className="alert" role="alert">{lifecycleError}</p>}
          <label>{lifecycle.resource === "network" ? "Network name" : "ISO name"}<input autoFocus value={confirmText} disabled={lifecycleBusy} onChange={e => setConfirmText(e.target.value)} onKeyDown={e => { if (e.key === "Enter") e.preventDefault(); }} /></label>
          <div className="actions">
            <button type="button" disabled={lifecycleBusy} onClick={() => setLifecycle(null)}>Cancel</button>
            <button type="button" className="danger" disabled={lifecycleBusy || !confirmMatches(confirmText, lifecycle.name)} onClick={() => void submitLifecycle()}>{lifecycleBusy ? "Submitting…" : lifecycle.resource === "network" ? "Remove network" : "Remove ISO"}</button>
          </div>
        </div>)}
    </dialog>
  </div>;
}

// An all-zero topology is the documented default of one socket, Count cores and one thread.
const topology = (c: VMSpec["cpu"]) => c.sockets || c.cores || c.threads ? `${c.sockets} × ${c.cores} × ${c.threads} (sockets × cores × threads)` : `1 × ${c.count} × 1 (sockets × cores × threads)`;

// ejectInstallOffer reports whether to offer ejecting this VM's install ISO. After an install
// finishes, the disc stays in the drive ahead of every bootable disk, so each start shows
// "Press any key to boot from CD or DVD" and a key press in the console restarts the installer
// (BUG-0202). It is offered, never applied for the operator: an installer may still be running,
// and Nex does not guess that an install finished and rewrite the definition by itself.
// It needs a running VM with a fresh report, because that is the only state Nex can observe
// the VM in; a host that cannot take a media request is not offered the action at all.
export function ejectInstallOffer(vm: VM, host?: Host): boolean {
  const spec = vm.spec;
  if (!spec?.media_id || vm.protected) return false;
  const media = spec.boot.media_boot_order;
  const disks = (spec.disks || []).map(d => d.boot_order).filter(order => order > 0);
  // Nothing to fall back to, or the firmware already tries a disk first: no prompt to remove.
  if (media <= 0 || !disks.length || disks.some(order => order < media)) return false;
  if (vm.freshness !== "fresh" || vm.state !== "running") return false;
  // The guest opened its own tray, so the drive the firmware would boot from is already empty.
  // An older Core reports no observation at all; the recorded media then stands on its own.
  if (vm.guest?.media === "empty" || vm.guest?.media === "no-drive") return false;
  return Boolean(host && !host.revoked && host.connection === "fresh" && host.capabilities?.includes("vm-media-v1"));
}

// staleNote is what an operator is told about a VM whose host has not reported recently: the
// host's stored inventory still holds the last state it reported, which is not the current one.
export function staleNote(vm: VM, host?: Host): string {
  const lastReport = host?.inventory?.vms?.find(v => v.id === vm.id);
  const reportedAt = host?.inventory?.observed_at ? new Date(host.inventory.observed_at) : null;
  return `Current execution state is unknown. Last reported state: ${lastReport?.state || "unknown"}${lastReport && reportedAt && !isNaN(reportedAt.getTime()) ? ` at ${reportedAt.toLocaleString()}` : ""}. An unreachable host does not prove that this VM stopped; power actions wait until the host reports again.`;
}

type DetailProps = { vm: VM; hosts: Host[]; storage: Storage[]; networks: Network[]; media: Media[]; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string) => void; editing: boolean; onEditing: (editing: boolean) => void };

// VMDetails is the VM's definition; Edit hardware turns the same card into the editor, so a VM
// is read and changed in one place.
export function VMDetails({ vm, hosts, storage, networks, media, api, reload, busy, unavailable, onNotice, editing, onEditing }: DetailProps) {
  const spec = vm.spec;
  const host = hosts.find(h => h.id === vm.host_id);
  const blocker = editBlocker(vm, host);
  return <section className="tile" aria-label="VM definition">
    <h2>Definition{!editing && <span className="actions"><button type="button" className="sm" disabled={busy || unavailable || Boolean(blocker)} title={blocker || undefined} onClick={() => onEditing(true)}>Edit hardware</button><VMDelete vm={vm} host={host} api={api} reload={reload} busy={busy} unavailable={unavailable} onNotice={onNotice} /></span>}</h2>
    {editing && spec ? <VMEditor vm={vm} host={host} poolName={host?.hostname || ""} storage={storage} networks={networks} media={media} api={api} reload={reload} unavailable={unavailable} onNotice={onNotice} onClose={() => onEditing(false)} /> : <dl className="details"><dt>Name</dt><dd>{vm.name}</dd><dt>VM identity</dt><dd className="mono">{vm.id}</dd><dt>Observed state</dt><dd>{capitalize(stateText(vm.state))}</dd><dt>Role</dt><dd>{vm.protected ? "Protected Controller appliance" : "Guest VM"}</dd>{spec && <>{spec.description && <><dt>Description</dt><dd>{spec.description}</dd></>}<dt>Start on boot</dt><dd>{spec.autostart ? "Starts when the host boots" : "Does not start when the host boots"}</dd><dt>Guest OS</dt><dd>{guestNames[spec.guest_os] || "Not recorded"}</dd><dt>Virtual CPUs</dt><dd>{spec.cpu.count}<small>{topology(spec.cpu)} · {cpuNames[spec.cpu.model] || spec.cpu.model}</small></dd><dt>Memory</dt><dd>{spec.memory.size_mib} MiB<small>{spec.memory.mode === "dedicated" ? "Dedicated · the host locks this much RAM while the VM runs, and keeps the capacity committed to this VM while it is stopped" : "Maximum · a ceiling only; free pages return to the host when the guest reports them"}</small></dd><dt>Disks</dt><dd>{spec.disks?.length ? <ul className="plain">{spec.disks.map((d, i) => <li key={i}>{d.name} · {d.size_gib} GiB · {busNames[d.bus] || d.bus} · {d.thin ? "Thin" : "Thick"}{d.boot_order > 0 ? ` · boot ${d.boot_order}` : ""}<small>{storage.find(s => s.id === d.storage_id)?.name || d.storage_id}</small></li>)}</ul> : "None recorded"}</dd><dt>Networks</dt><dd>{spec.nics?.length ? <ul className="plain">{spec.nics.map((n, i) => <li key={i}>{networks.find(x => x.id === n.network_id)?.name || n.network_id} · {adapterNames[n.model] || n.model}{n.mtu ? ` · MTU ${n.mtu}` : ""}<small className="mono">{n.mac || "MAC derived from the VM identity"}</small></li>)}</ul> : "None"}</dd><dt>Install ISO</dt><dd>{spec.media_id ? media.find(m => m.id === spec.media_id)?.name || spec.media_id : "None · CD drive empty"}{spec.boot.media_boot_order > 0 && <small>CD drive boot order {spec.boot.media_boot_order}</small>}</dd><dt>Driver disc</dt><dd>{spec.drivers_media_id ? media.find(m => m.id === spec.drivers_media_id)?.name || spec.drivers_media_id : "None · second CD drive empty"}</dd><dt>Firmware</dt><dd>{spec.boot.firmware?.toUpperCase()}<small>Machine type {spec.boot.machine || "q35"}</small></dd><dt>Secure Boot</dt><dd>{secureBootText(spec.boot)}{spec.boot.firmware === "uefi" && !spec.boot.secure_boot && <small>Not recorded in this definition: a UEFI VM defined before the choice existed boots with the enrolled-keys firmware. Edit and save the VM to record it.</small>}</dd><dt>TPM</dt><dd>{tpmText(spec.boot)}</dd></>}{vm.operation_id && <><dt>Creation task</dt><dd>{task(vm)}</dd></>}</dl>}
    {!vm.protected && !editing && <p className="note">To install a guest, open the console and use Start VM on its toolbar so the console is attached in time for the boot prompt. Changes a running guest can take apply while it runs.</p>}
  </section>;
}

// InstallMedia is the VM's CD drives: the ISO in each, insert and eject, and the offer to eject
// an install disc that would otherwise boot again ahead of the installed disk.
export function InstallMedia({ vm, host, drives, eject, busy, unavailable }: { vm: VM; host?: Host; drives: ConsoleMedia; eject: { busy: boolean; note: string; onEject: () => void }; busy: boolean; unavailable: boolean }) {
  const off = busy || unavailable || Boolean(drives.busy);
  return <section className="tile" aria-label="Install media">
    <h2>Install media</h2>
    {ejectInstallOffer(vm, host) && <div className="warning install-media-offer" role="status">
      <p><strong>The install ISO is still in the CD drive, ahead of the disks in the boot order.</strong> Every start boots the ISO first: the installer starts again, or waits at a prompt such as “Press any key to boot from CD or DVD” where a key press starts it again. Eject it once the guest is installed; the VM then boots from its disk. The ISO stays in the library and can be inserted again here or from the console.</p>
      <p className="help">Do not eject while an installer is still running in this VM.</p>
      {eject.note && <p className="task-error" role="alert">{eject.note}</p>}
      <div className="actions"><button type="button" disabled={busy || unavailable || eject.busy} onClick={eject.onEject}>{eject.busy ? "Ejecting…" : "Eject install media"}</button></div>
    </div>}
    <dl className="details">{drives.drives.map(d => <Fragment key={d.drive}>
      <dt>{d.label}</dt>
      <dd className="media-drive">
        <select aria-label={`Insert an ISO in ${d.label}`} disabled={off || !drives.options.length} value="" onChange={e => { if (e.target.value) d.onInsert(e.target.value); }}>
          <option value="">{d.currentName || "Empty"}</option>
          {drives.options.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}
        </select>
        <button type="button" className="sm" aria-label={`Eject ${d.label}`} disabled={off || !d.currentID} onClick={d.onEject}>Eject</button>
      </dd>
    </Fragment>)}</dl>
    {drives.note && <p className="help" role="status">{drives.note}</p>}
  </section>;
}

const usageBytes = (value?: number | null) => typeof value === "number" && Number.isFinite(value)
  ? `${(value / GiB).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB`
  : "Not reported";
export function VMUsage({ vm }: { vm: VM }) {
  const usage = vm.usage;
  return <section className="tile vm-usage" aria-label="VM usage">
    <h2>Current usage<Badge value={vm.freshness} /></h2>
    {!usage ? <p className="help">No usage sample reported. This is not zero usage: the VM may be stopped, the host may not support usage observation, or the host could not sample it without waiting for another operation.</p>
      : <dl className="details">
        <dt>CPU</dt><dd>{usage.cpu_percent == null ? "Not reported" : `${usage.cpu_percent}% of allocated vCPUs`}{usage.cpu_percent != null && usage.sample_window_ms ? <small>Average over {(usage.sample_window_ms / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })} seconds</small> : <small>A rate needs two host samples; it is not reported as zero before then.</small>}</dd>
        <dt>Memory allocation</dt><dd>{usageBytes(usage.memory_assigned_bytes)} assigned · {usageBytes(usage.memory_maximum_bytes)} maximum<small>Allocation, not memory used inside the guest.</small></dd>
        <dt>Host memory cost</dt><dd>{usageBytes(usage.memory_host_bytes)}<small>Resident memory used by this VM's host process, not guest-used memory.</small></dd>
        <dt>Disk used</dt><dd>{usageBytes(usage.disk_used_bytes)}<small>Repository space occupied by this VM's disk images.</small></dd>
        <dt>Disk capacity</dt><dd>{usageBytes(usage.disk_capacity_bytes)}<small>Virtual disk size visible to the guest.</small></dd>
      </dl>}
    <p className="note">Usage is a host observation, not a guest-health check. A stale VM status means the host's current execution state is unknown; wait for a fresh report before acting on these numbers.</p>
  </section>;
}

// GuestTools shows what the host observed inside a running guest: agent facts and the
// console channels behind clipboard and display resize. Absence is shown as unknown.
const guestAgentText: Record<string, string> = {
  connected: "Connected · guest agent answering",
  unresponsive: "Attached but not answering · the guest agent may be starting or hung",
  disconnected: "Not running in the guest",
  "no-channel": "Channel missing · this VM was created before guest tools support; recreate it to add the channel",
};
const guestClipboardText: Record<string, string> = {
  ready: "Ready · copy and paste work both ways in the console",
  waiting: "Waiting for guest tools (qemu-guest-agent and spice-vdagent) in the guest · Type into VM still works",
  "no-channel": "Channel missing · this VM was created before console clipboard support; recreate it to add the channel",
};
// A resize needs both halves: a VirtIO display in the VM and the guest's vdagent to act on the
// request. Core now reports the pair rather than the device model alone (BUG-0201), and the
// Controller sees only that verdict — no display model is in the definition it mirrors. It can
// still name the missing half: the vdagent is the channel the clipboard reads "ready" for, so a
// guest whose clipboard is ready and still cannot resize is one without a VirtIO display.
// Blaming the display for every false answer would send an operator to recreate a perfectly
// good VM when installing the guest tools is all that is needed.
export function guestResizeText(g: Guest): string {
  if (g.resize) return "Available · the console can resize the guest to the browser window";
  if (g.clipboard === "ready") return "Not available · the guest is running its tools, so this VM's display is not VirtIO. Only a VM recreated with a VirtIO display can be resized.";
  if (g.clipboard === "no-channel") return "Not available · this VM was created before the console agent channel existed, so nothing in the guest can act on a resize. Recreate it, then install the guest tools.";
  return "Not available · the guest is not running both qemu-guest-agent and the vdagent that changes its resolution. Install the guest tools; Clipboard reads Ready exactly when resize becomes available. A VM whose display is not VirtIO cannot resize even then.";
}
// guestCapabilities is the guest tools block's status grid: one row per console capability, a
// word or two for the state and the full reason on demand. Anything not reported is Unknown.
export function guestCapabilities(g?: Guest | null): { name: string; tone: string; status: string; why: string }[] {
  const unknown = "Not observed yet. The host reports guest tools only for a running VM.";
  const agent = !g ? ["unknown", "Unknown", unknown]
    : g.agent === "connected" ? ["ok", "Ready"] : g.agent === "unresponsive" ? ["warn", "Not answering"] : g.agent === "disconnected" ? ["warn", "Not running"]
    : g.agent === "no-channel" ? ["bad", "Not available"] : ["unknown", "Unknown"];
  const clipboard = !g ? ["unknown", "Unknown", unknown]
    : g.clipboard === "ready" ? ["ok", "Ready"] : g.clipboard === "waiting" ? ["warn", "Waiting"] : g.clipboard === "no-channel" ? ["bad", "Not available"] : ["unknown", "Unknown"];
  const resize = !g ? ["unknown", "Unknown", unknown] : g.resize ? ["ok", "Ready"] : ["warn", "Not available"];
  return [
    { name: "Guest agent", tone: agent[0], status: agent[1], why: agent[2] || guestAgentText[g!.agent] || `Reported as ${g!.agent || "nothing"}.` },
    { name: "Clipboard", tone: clipboard[0], status: clipboard[1], why: clipboard[2] || guestClipboardText[g!.clipboard] || `Reported as ${g!.clipboard || "nothing"}.` },
    { name: "Display resize", tone: resize[0], status: resize[1], why: resize[2] || guestResizeText(g!) },
  ];
}
export function GuestTools({ vm }: { vm: VM }) {
  const g = vm.guest;
  const running = vm.freshness === "fresh" && vm.state === "running";
  return <section className="tile" aria-label="Guest tools">
    <h2>Guest tools{g ? <Badge value={g.agent || "unknown"} /> : <Badge value="unknown" />}</h2>
    {!g && <p className="help">{running ? "No guest observation yet. Wait for the next report, or upgrade Nex Core if it does not observe guests." : "Guest tools are observed while the VM is running."}</p>}
    <ul className="guest-caps">{guestCapabilities(g).map(c => <li key={c.name}><details>
      <summary title={c.why}><span>{c.name}</span><span className={`badge ${c.tone === "unknown" ? "neutral" : c.tone}`}>{c.status}</span><span className="link">{c.tone === "ok" ? "Details" : "Why?"}</span></summary>
      <p className="help">{c.why}</p>
    </details></li>)}</ul>
    {g && <dl className="details">
      {g.os && <><dt>Operating system</dt><dd>{g.os}</dd></>}
      {g.hostname && <><dt>Guest hostname</dt><dd>{g.hostname}</dd></>}
      <dt>IP addresses</dt><dd>{g.addresses?.length ? <ul className="plain">{g.addresses.map(a => <li key={a.interface + a.address} className="mono">{a.address} <small>{a.interface}</small></li>)}</ul> : g.agent === "connected" ? "None reported" : "Unknown until the guest agent answers"}</dd>
    </dl>}
    <About title="How to enable guest tools">
      {g?.agent === "no-channel" ? <p>This VM was created before guest tools support, so it has no guest agent channel. Recreate it to add the channel, then install the tools below. In the meantime the console's <strong>Type into VM</strong> still works.</p>
        : running && g && g.agent !== "connected" ? <p><strong>Guest tools are not responding.</strong> Install them inside the VM to see its OS, hostname and IP addresses here and to use the console clipboard.</p> : null}
      <GuestToolsAdvisory />
      <p>A connected agent reports facts and enables graceful operations; it is not proof that the guest's applications are healthy.</p>
    </About>
  </section>;
}

// GuestToolsAdvisory tells an operator how to install the guest tools in a running VM whose
// agent has not connected, for both Linux and Windows guests.
export function GuestToolsAdvisory() {
  return <div className="guest-advisory">
    <p>They are standard packages; open the <strong>Console</strong> to run these if the VM has no network yet.</p>
    <div className="guest-advisory-grid">
      <div>
        <h3>Linux</h3>
        <p className="help">Debian and Ubuntu:</p>
        <pre>sudo apt update{"\n"}sudo apt install qemu-guest-agent spice-vdagent</pre>
        <p className="help">Other distributions: install their <code>qemu-guest-agent</code> and <code>spice-vdagent</code> packages, then <code>systemctl enable --now qemu-guest-agent spice-vdagentd</code>. Both start on their own; no reboot is needed. The clipboard needs a graphical desktop session.</p>
      </div>
      <div>
        <h3>Windows</h3>
        <p className="help">Run the virtio-win guest tools installer, which bundles the QEMU guest agent and the SPICE vdagent:</p>
        <pre>virtio-win-guest-tools.exe</pre>
        <p className="help">Download it from the <a href="https://github.com/virtio-win/virtio-win-pkg-scripts" target="_blank" rel="noopener">virtio-win project</a>. It installs both as Windows services that start automatically.</p>
      </div>
    </div>
  </div>;
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
            : "Transfer succeeded · not rescanned";
        }
        return <tr key={image.id}><td>{image.name}</td><td>{destination?.name || image.storage_id}{destination && <small>{destination.path}</small>}</td><td>{image.size_bytes > 0 ? <>{(image.size_bytes / GiB).toLocaleString(undefined, { maximumFractionDigits: 2 })} GiB<small>{image.size_bytes.toLocaleString()} bytes</small></> : "Unknown until downloaded"}</td><td>{availability}</td><td>{task(image)}</td></tr>;
      })}</tbody>
    </table></div> : <p className="empty">{unavailable ? "ISO records could not be loaded. Retry when the Controller is reachable." : "No ISO transfers recorded for this host. Upload an ISO to this host's storage from the pool's ISO library."}</p>}
    <p className="panel-note">Availability is based on completed transfer records, validated storage and host connectivity. Files are not continuously rescanned; VM creation checks the selected ISO again.</p>
  </section>;
}

// A guest that put itself to sleep (libvirt "pmsuspended") is an active VM that runs nothing:
// Start wakes it, Shut down and Reboot wake it first, the force actions act on it directly.
export const asleep = "pmsuspended";
const active = (state: string) => state === "running" || state === asleep;
export const powerActions = [
  { action: "start", label: "Start", icon: "start", when: (state: string) => state === "shut off" || state === asleep, confirm: "" },
  { action: "shutdown", label: "Shut down", icon: "shutdown", when: active, confirm: "Asks the guest to shut down (ACPI). A guest without ACPI support ignores it; use Force stop if it stays running." },
  { action: "reboot", label: "Reboot", icon: "reboot", when: active, confirm: "Asks the guest to reboot (ACPI). A guest without ACPI support ignores it." },
  { action: "force-stop", label: "Force stop", icon: "stop", when: active, confirm: "Cuts power immediately, like pulling the plug. Unsaved guest data is lost." },
  { action: "force-reboot", label: "Force reboot", icon: "reboot", when: active, confirm: "Hard-resets the guest immediately (like the reset button). Unsaved guest data is lost." },
] as const;

// stateText names an observed libvirt state for operators; every other state is shown as reported.
// A VM's state in one word for lists and maps; a VM whose state is not current is Unknown, never Stopped.
export function vmStateLabel(vm: Pick<VM, "state" | "freshness">, known = vm.freshness === "fresh"): string {
  if (!known) return "Unknown";
  return vm.state === "running" ? "Running" : vm.state === "shut off" ? "Stopped" : vm.state === asleep ? "Asleep" : stateText(vm.state).replace(/^./, c => c.toUpperCase());
}
export function stateText(state?: string): string {
  return state === asleep ? "asleep (the guest suspended itself; Start wakes it)" : state || "unknown";
}

// powerBlocker explains why central power requests are unavailable for a VM now; an empty
// result means they can be submitted.
export function powerBlocker(vm: VM, host?: Host): string {
  return !host || host.revoked || host.connection !== "fresh" ? "Power controls need a freshly connected host."
    : !host.capabilities?.includes("vm-power-v1") ? "The host agent does not support central power requests; upgrade Nex Core."
    : vm.freshness !== "fresh" ? "Current VM state is unknown; wait for a fresh host report."
    : vm.state !== "running" && vm.state !== "shut off" && vm.state !== asleep ? `The VM is ${vm.state || "unknown"}; resolve that state on its host before a power request.`
    : "";
}

// powerConfirmText is the confirmation a power action needs on this VM; empty means it is sent
// straight away. Start has no confirmation of its own, but a protected Controller appliance that
// this Controller sees shut off is not this Controller, so starting it could run two Controllers.
export function powerConfirmText(item: typeof powerActions[number], vm: VM): string {
  if (!vm.protected) return item.confirm;
  return item.action === "start" ? "This is a Controller appliance. If another Controller now manages this host, starting it runs two Controllers; never do that. Recover it from the host-local page instead."
    : `${item.confirm} This is the Controller appliance: central management stops until it runs again; recover it from the host-local page.`;
}

// VMPower is a VM panel's power actions: Start and Shut down as buttons, then `children`, then
// Reboot, Force stop and Force reboot in a More menu, each greyed out with a reason when the host
// cannot take the request. Requests are durable host tasks; the observed state, refreshed by host
// reports, tells what actually happened.
export function VMPower({ vm, host, api, reload, busy, unavailable, onNotice, children }: { vm: VM; host?: Host; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string, operationID?: string) => void; children?: ReactNode }) {
  const [pending, setPending] = useState<typeof powerActions[number] | null>(null);
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const request = useRef<Record<string, string>>({});
  const dialog = useRef<HTMLDialogElement>(null);
  // The opener is disabled while its request runs, so the dialog cannot restore focus by itself.
  const opener = useRef<HTMLElement | null>(null);
  const blocker = unavailable ? "Controller inventory could not be loaded, so the VM's current state is unknown. Retry loading it first." : powerBlocker(vm, host);
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (pending && !element.open) element.showModal();
    else if (!pending && element.open) element.close();
  }, [pending]);
  async function submit(item: typeof powerActions[number]) {
    setWorking(true); setError("");
    request.current[item.action] ??= crypto.randomUUID();
    try {
      const res = await api<{ operation?: { id?: string } }>(`vms/${encodeURIComponent(vm.id)}/power`, "POST", { request_id: request.current[item.action], action: item.action });
      delete request.current[item.action];
      onNotice(`${item.label} queued for ${vm.name}. Follow its progress in Tasks.`, res?.operation?.id);
      await reload();
      // Close only once the button can take focus back, so the dialog returns focus to it.
      setPending(null);
    } catch (e) {
      // The dialog is the error surface for every action: it stays (or opens, for Start) with the
      // message, and its confirm button retries with the same request identity.
      setError((e as Error).message);
      setPending(item);
    }
    finally { setWorking(false); }
  }
  const button = (item: typeof powerActions[number]) => <button key={item.action} type="button" className={`sm tool-${item.action}`} title={blocker || item.label}
    disabled={busy || working || unavailable || Boolean(blocker) || !item.when(vm.state)}
    onClick={e => { opener.current = e.currentTarget.closest("details")?.querySelector("summary") || e.currentTarget; setError(""); if (powerConfirmText(item, vm)) setPending(item); else void submit(item); }}><Icon kind={item.icon} />{item.label}</button>;
  return <div className="vm-power" role="group" aria-label="VM power">
    {powerActions.slice(0, 2).map(button)}
    {children}
    <Menu align="end" className="vm-more" label={<>More<Icon kind="chevron" size={14} /></>}>{powerActions.slice(2).map(button)}</Menu>
    <dialog ref={dialog} className="confirm-dialog" aria-labelledby="power-confirm-title" onCancel={e => { if (working) e.preventDefault(); }} onClose={() => { setPending(null); setError(""); opener.current?.focus(); }}>
      {pending && vm && <form aria-label={`Confirm ${pending.label.toLowerCase()}`} onSubmit={e => { e.preventDefault(); void submit(pending); }}>
        <h2 id="power-confirm-title">{pending.label} {vm.name}?</h2>
        {powerConfirmText(pending, vm) && <p>{powerConfirmText(pending, vm)}</p>}
        {error && <p className="task-error" role="alert">{error}</p>}
        <div className="actions"><button type="button" disabled={working} onClick={() => setPending(null)}>Cancel</button><button className={pending.action === "force-stop" || pending.action === "force-reboot" ? "danger" : "primary"} disabled={working}>{working ? "Requesting…" : `Confirm ${pending.label.toLowerCase()}`}</button></div>
      </form>}
    </dialog>
  </div>;
}

export function deleteBlocker(vm: VM, host?: Host): string {
  return vm.protected ? "The Controller appliance is managed from its host's local page, not deleted here."
    : !host || host.revoked || host.connection !== "fresh" ? "Delete needs a freshly connected host."
    : !host.capabilities?.includes("vm-delete-v1") ? "The host agent does not support deleting VMs; upgrade Nex Core."
    : vm.freshness !== "fresh" ? "Current VM state is unknown; wait for a fresh host report."
    : vm.state !== "shut off" ? "Stop the VM before deleting it."
    : "";
}

// VMDelete confirms by typing the VM name and optionally wiping disk files on the host.
export function VMDelete({ vm, host, api, reload, busy, unavailable, onNotice }: {
  vm: VM; host?: Host; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string, operationID?: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [wipe, setWipe] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const request = useRef<Record<string, string>>({});
  const dialog = useRef<HTMLDialogElement>(null);
  const blocker = deleteBlocker(vm, host);
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (open && !element.open) element.showModal();
    else if (!open && element.open) element.close();
  }, [open]);
  async function submit() {
    setWorking(true); setError("");
    // One identity per payload: a retry replays it, a changed wipe choice is a new request.
    request.current[String(wipe)] ||= crypto.randomUUID();
    try {
      const result = await api<{ operation: { id: string } }>(`vms/${encodeURIComponent(vm.id)}`, "DELETE", { request_id: request.current[String(wipe)], delete_disks: wipe });
      request.current = {};
      setOpen(false);
      if (location.hash === `#/vms/${vm.id}` || location.hash.startsWith(`#/vms/${vm.id}/`) || location.hash.startsWith(`#/console/${vm.id}`)) {
        location.hash = "#/vms";
      }
      // Navigating clears page notices on hashchange; the awaited reload lets that run first.
      await reload();
      onNotice(`Delete queued for ${vm.name}. Follow its progress in Tasks.`, result.operation.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setWorking(false);
    }
  }
  return <>
    <button type="button" className="sm danger" disabled={busy || working || unavailable || Boolean(blocker)} title={blocker || undefined}
      onClick={() => { setError(""); setWipe(false); setName(""); setOpen(true); }}>Delete VM</button>
    <dialog ref={dialog} className="confirm-dialog" aria-labelledby="vm-delete-title" onCancel={e => { if (working) e.preventDefault(); }}
      onClose={() => { setOpen(false); setError(""); setWipe(false); setName(""); }}>
      {open && <form onSubmit={e => { e.preventDefault(); void submit(); }}>
        <h2 id="vm-delete-title">Delete {vm.name}?</h2>
        <p>Removes this VM from Nex and libvirt. Type the VM name to confirm.</p>
        <label className="disk-wipe"><input type="checkbox" checked={wipe} onChange={e => setWipe(e.target.checked)} disabled={working} /> Permanently delete disks from storage</label>
        <p className="help">{wipe ? "Disk files will be permanently removed from the host storage after the VM is undefined." : "Disk files stay on the host storage and are not deleted."}</p>
        {error && <p className="task-error" role="alert">{error}</p>}
        <label>VM name<input required autoFocus value={name} onChange={e => setName(e.target.value)} disabled={working}
          pattern={vm.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")} /></label>
        <div className="actions">
          <button type="button" disabled={working} onClick={() => setOpen(false)}>Cancel</button>
          <button className="danger" disabled={working || name !== vm.name}>{working ? "Requesting…" : wipe ? "Delete VM and disks" : "Delete VM"}</button>
        </div>
      </form>}
    </dialog>
  </>;
}
