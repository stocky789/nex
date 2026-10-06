// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import {
  BlockedDisks, DiskCells, deviceBody, dirty, diskColumns, shortName, size, zfsDeviceUsable, zfsDiskOrder,
  type Device, type Discovery,
} from "./zfs";

// The host's own view of its system disk mirror: the md arrays the kernel assembled, read from
// the hardware report, Core's own record of the latest disk replacement, and the one action an
// operator takes on a degraded array. Nothing is stored here; the host's report is read again.

type API = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
export const SYSTEM_DISK_CAPABILITY = "host-system-disk-replace-v1";
export type MDMember = { name: string; device: string; slot: string; state: string };
export type MDArray = { name: string; device: string; level: string; raid_disks: number; degraded: number; sync_action?: string; sync_percent?: number; sync_speed_kbs?: number; members: MDMember[] };
type Disk = { name: string; size_bytes: number; rotational: boolean };
type Task = { id: string; status: string; error?: string };
export type SystemDiskResync = { array: string; member: string; state: string; action?: string; percent?: number; speed_kbs?: number; degraded: number };
export type SystemDiskReplacement = {
  request_id: string; by_id: string; serial: string; size_bytes: number; phase: string; status: "running" | "succeeded" | "failed"; error?: string;
  started_at: string; finished_at?: string; resync?: SystemDiskResync;
};
export type SystemDiskReport = { booted_disk?: string; replacement?: SystemDiskReplacement };

export const mdMemberFailed = (member: MDMember) => /faulty|write_error|want_replacement/.test(member.state);
// recover / resync / reshape copy or rearrange data; a second replacement must wait for them.
// check and repair are reported for the panel but do not block Replace disk….
export const mdArrayRebuilding = (array: MDArray) => /^(recover|resync|reshape)$/.test(array.sync_action || "");
export const mdDiskTooSmall = (device: Device, required: number) => required > 0 && device.size_bytes < required;
export const mdReplaceReady = (device: Device | undefined, required: number, confirmation: string) =>
  !!device && !mdDiskTooSmall(device, required) && (!dirty(device) || confirmation === device.by_id);

// Submit a system-disk replacement. The caller holds request_id across retries of the same
// selection so a lost reply is replayed rather than starting a second wipe.
export async function mdReplaceSubmit(api: API, hostID: string, requestID: string, device: Device, confirmation: string): Promise<Task> {
  const accepted = await api<{ operation: Task }>(`hosts/${encodeURIComponent(hostID)}/system-disk`, "POST", {
    request_id: requestID, device: deviceBody(device, confirmation),
  });
  return accepted.operation;
}

// Follow a running rebuild: re-read the hardware report while the tab is visible.
export function mdPollRebuild(onTick: () => void, everyMs = 10000, setIntervalFn: typeof setInterval = setInterval) {
  return setIntervalFn(() => { if (document.visibilityState === "visible") onTick(); }, everyMs);
}

// Follow a submitted replace operation until its status leaves queued/dispatched.
export function mdPollOperation(
  fetchOperations: () => Promise<Task[]>, id: string, status: string, onChange: (task: Task) => void,
  everyMs = 5000, setIntervalFn: typeof setInterval = setInterval,
) {
  return setIntervalFn(async () => {
    try {
      const current = (await fetchOperations()).find(operation => operation.id === id);
      if (current && current.status !== status) onChange(current);
    } catch { /* the next tick tries again; the Tasks list is the durable record */ }
  }, everyMs);
}

// The host's phases, in the words an operator reads.
const phases: Record<string, string> = {
  accepted: "Accepted by the host", laying: "Writing the partition table", slots: "Copying the system image",
  boot: "Making the disk bootable", data: "Adding the disk to the data mirror", complete: "Complete",
};
export const mdPhase = (phase: string) => phases[phase] || phase;

// The kernel's own action names, in the words an operator acts on. A running action is reported
// whether or not the array is still degraded, because that is what it is doing now.
const actions: Record<string, string> = { recover: "Rebuilding", resync: "Resyncing", check: "Checking", repair: "Repairing", reshape: "Reshaping" };

export function mdArrayState(array: MDArray): string {
  const action = actions[array.sync_action || ""];
  if (action) return array.sync_percent == null ? action : `${action} · ${array.sync_percent.toFixed(1)}%`;
  return array.degraded ? "Degraded" : "Healthy";
}

// One sentence saying what the array is doing.
export function mdArraySummary(array: MDArray): string {
  const disks = `${array.raid_disks} disks`;
  if (mdArrayRebuilding(array)) {
    const speed = array.sync_speed_kbs ? ` at ${(array.sync_speed_kbs / 1024).toFixed(0)} MB/s` : "";
    return `${mdArrayState(array)}${speed}. The array is rebuilding and is not yet fully redundant.`;
  }
  if (actions[array.sync_action || ""]) {
    const speed = array.sync_speed_kbs ? ` at ${(array.sync_speed_kbs / 1024).toFixed(0)} MB/s` : "";
    return `${mdArrayState(array)}${speed}. The host keeps running while the kernel copies the data.`;
  }
  if (array.degraded) {
    const missing = array.degraded === 1 ? "One disk" : `${array.degraded} disks`;
    return `${missing} of ${disks} is missing or failed. The host keeps running without redundancy.`;
  }
  return `${array.level} of ${disks}, ${array.raid_disks === 2 ? "both" : "all"} in sync.`;
}

// The whole disk an array member sits on, so a member can be shown with the disk's own size:
// "/dev/sda2" belongs to "/dev/sda", "/dev/nvme0n1p2" to "/dev/nvme0n1".
export const mdMemberDisk = (member: MDMember, disks: Disk[]) =>
  disks.filter(disk => member.device.startsWith(disk.name)).sort((a, b) => b.name.length - a.name.length)[0];

export function SystemDiskMirror({ arrays, disks, hostID, poolID, api, supported, reported, systemDisk, onRefresh }: {
  arrays: MDArray[]; disks: Disk[]; hostID: string; poolID: string; api: API; supported: boolean; reported: boolean; systemDisk?: SystemDiskReport; onRefresh: () => void;
}) {
  const [replacing, setReplacing] = useState<MDArray | null>(null);
  const [task, setTask] = useState<Task | null>(null);
  const replacement = systemDisk?.replacement;
  const rebuilding = arrays.some(array => actions[array.sync_action || ""]) || replacement?.status === "running";
  // Stats re-renders on its own 5s history poll and used to pass a fresh onRefresh/api each time.
  // Hold the latest callbacks in refs so the rebuild and task intervals depend only on state that
  // should restart them — otherwise a 10s/5s timer is cleared before it can fire.
  const onRefreshRef = useRef(onRefresh);
  const apiRef = useRef(api);
  onRefreshRef.current = onRefresh;
  apiRef.current = api;

  // A rebuild moves on its own, so the panel follows it: the hardware report is read again while
  // one runs, and the submitted replacement is followed until the host reports its outcome.
  useEffect(() => {
    if (!rebuilding) return;
    const timer = mdPollRebuild(() => onRefreshRef.current());
    return () => clearInterval(timer);
  }, [rebuilding]);
  useEffect(() => {
    if (!task || (task.status !== "queued" && task.status !== "dispatched")) return;
    const id = task.id, status = task.status;
    const timer = mdPollOperation(
      () => apiRef.current<Task[]>("operations"), id, status,
      current => { setTask(current); onRefreshRef.current(); },
    );
    return () => clearInterval(timer);
  }, [task?.id, task?.status]);

  if (!reported) return <div className="md-arrays">
    <h3>System disk</h3>
    <p className="empty">This host's Nex Core does not report its RAID arrays. Update Nex Core to see whether its system disk is mirrored.</p>
  </div>;
  if (!arrays.length) return <div className="md-arrays">
    <h3>System disk</h3>
    <p className="empty">Single system disk, no redundancy. A mirrored system disk is chosen when Nex Core is installed.</p>
  </div>;
  return <div className="md-arrays">
    <h3>System disk mirror</h3>
    {task && task.status === "failed" && <p className="alert" role="alert">The host could not replace the disk: {task.error}</p>}
    {replacement && <ReplacementRecord replacement={replacement} />}
    {arrays.map(array => <div key={array.name} className="md-array">
      <p className="md-line">
        <strong>{array.device}</strong> · {array.level} · <span className={array.degraded ? "md-degraded" : "md-healthy"}>{mdArrayState(array)}</span>
        {array.degraded > 0 && array.level === "raid1" && supported && !mdArrayRebuilding(array) &&
          <button type="button" className="danger" onClick={() => setReplacing(array)}>Replace disk…</button>}
      </p>
      <p className="md-summary">{mdArraySummary(array)}</p>
      {array.sync_percent != null && actions[array.sync_action || ""] &&
        <progress className="md-progress" max={100} value={array.sync_percent} aria-label={`${mdArrayState(array)} ${array.device}`} />}
      {array.degraded > 0 && array.level === "raid1" && !supported &&
        <p className="empty">This host's Nex Core cannot replace a system disk yet. Update Nex Core, then replace the failed disk from here.</p>}
      <ul className="md-members">
        {array.members.map(member => {
          const disk = mdMemberDisk(member, disks);
          return <li key={member.name}>
            {member.device}{disk ? ` · ${size(disk.size_bytes)} ${disk.rotational ? "HDD" : "SSD"}` : ""} · {member.state}
            {member.slot !== "none" ? ` · slot ${member.slot}` : ""}
            {mdMemberFailed(member) && <span className="md-degraded"> · Failed</span>}
            {disk && systemDisk?.booted_disk === `/dev/${disk.name}` && <span> · running system</span>}
          </li>;
        })}
        {array.members.length < array.raid_disks &&
          <li className="md-degraded">{array.raid_disks - array.members.length} disk missing — the mirror has no copy of this data</li>}
      </ul>
    </div>)}
    {replacing && <ReplaceSystemDisk array={replacing} required={mdRequiredBytes(replacing, disks)} hostID={hostID} poolID={poolID} api={api}
      onClose={() => setReplacing(null)} onSubmitted={submitted => { setTask(submitted); onRefresh(); }} />}
  </div>;
}

// A replacement must be at least as large as the disk the rebuild copies from, which is the
// disk of any member that is still in sync. Zero when the host reports no size for it.
export function mdRequiredBytes(array: MDArray, disks: Disk[]): number {
  for (const member of array.members) {
    if (mdMemberFailed(member)) continue;
    const disk = mdMemberDisk(member, disks);
    if (disk) return disk.size_bytes;
  }
  return 0;
}

// Core's record of the latest replacement, so a fresh Controller or browser shows an operation
// another one started.
function ReplacementRecord({ replacement }: { replacement: SystemDiskReplacement }) {
  const resync = replacement.resync;
  const disk = replacement.by_id.replace("/dev/disk/by-id/", "") || replacement.serial;
  return <div className="md-array">
    <p className="md-line"><strong>Disk replacement</strong> · <span>{disk}</span> · {replacement.status === "running" ? <span>{mdPhase(replacement.phase)}…</span>
      : <span className={replacement.status === "failed" ? "md-degraded" : "md-healthy"}>{replacement.status === "failed" ? "Failed" : "Done"}</span>}</p>
    {replacement.status === "running" && <p className="md-summary">Nex Core is still working on this. The host keeps running.</p>}
    {replacement.status === "failed" && <p className="alert" role="alert">The host could not replace the disk: {replacement.error || "no reason reported"} It stopped at: {mdPhase(replacement.phase).toLowerCase()}.</p>}
    {resync?.percent != null && resync.action &&
      <><p className="md-summary">Rebuilding the data mirror · {resync.percent.toFixed(1)}%{resync.speed_kbs ? ` at ${(resync.speed_kbs / 1024).toFixed(0)} MB/s` : ""}</p>
        <progress className="md-progress" max={100} value={resync.percent} aria-label={`Rebuilding ${resync.array}`} /></>}
  </div>;
}

function ReplaceSystemDisk({ array, required, hostID, poolID, api, onClose, onSubmitted }: {
  array: MDArray; required: number; hostID: string; poolID: string; api: API; onClose: () => void; onSubmitted: (task: Task) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const requestID = useRef("");
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [selected, setSelected] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // Separate from busy, which also covers listing the disks: only a submitted replacement may
  // say it is submitting.
  const [submitting, setSubmitting] = useState(false);
  const device = devices?.find(d => d.by_id === selected);
  // One request identity per selection: a retry after a lost reply replays the same request
  // rather than starting a second replacement, and every new dialog (after a failed
  // operation too) starts with a new one.
  useEffect(() => { requestID.current = crypto.randomUUID(); }, [selected, confirmation]);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); void load(); }, []);

  async function load() {
    setBusy(true); setError(""); setDevices(null); setSelected(""); setConfirmation("");
    try {
      const result = await api<Discovery>("storage/discover", "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: hostID, kind: "zfs-device" });
      if (result.status !== "succeeded") throw new Error(result.error || "Nex Core could not list this host's disks.");
      setDevices((result.candidates || []).flatMap(c => c.device ? [c.device] : []));
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  async function submit() {
    if (!device) return;
    setBusy(true); setSubmitting(true); setError("");
    try {
      onSubmitted(await mdReplaceSubmit(api, hostID, requestID.current, device, confirmation));
      onClose();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); setSubmitting(false); }
  }

  const usable = (devices || []).filter(zfsDeviceUsable).sort(zfsDiskOrder);
  const blocked = (devices || []).filter(d => !zfsDeviceUsable(d)).sort(zfsDiskOrder);
  const ready = mdReplaceReady(device, required, confirmation);
  // usable.every(small) is vacuously true on [], so an empty list must take the "no disk" branch;
  // the too-small sentence is only for a non-empty list where every candidate is undersized.
  const empty = usable.length === 0
    ? "This host reports no disk that can be used. Every disk is in use by the system, a pool or another array. Fit a spare disk, then refresh."
    : usable.every(d => mdDiskTooSmall(d, required))
      ? `Every spare disk on this host is smaller than the remaining system disk (${size(required)}). Fit a larger disk, then refresh.`
      : "";
  return <dialog ref={dialog} className="confirm-dialog" aria-labelledby="md-replace-title" onCancel={event => { event.preventDefault(); if (!busy) onClose(); }} onClose={() => { if (busy) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="md-replace-title" ref={heading} tabIndex={-1}>Replace a disk of {array.device}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p className="warning">Everything on the disk you pick is erased. Nex Core lays the same partition table on it, copies the verified system image from the disk that is still working, makes it bootable and adds it to the data mirror. The host keeps running and its VMs are not interrupted; the data mirror then rebuilds in the background.</p>
    {error && <p className="alert" role="alert">{error}</p>}
    {!devices && !error && <p role="status">Asking this host for its disks…</p>}
    {devices && empty && <p className="empty" role="status">{empty}</p>}
    {usable.length > 0 && <fieldset className="md-choices"><legend>Replacement disk{required > 0 ? ` · at least ${size(required)}` : ""}</legend>
      <div className="table-scroll"><table className="zfs-disk-table"><thead>{diskColumns(false, true)}</thead><tbody>
        {usable.map(d => {
          const id = `md-replacement-${shortName(d)}`;
          const small = mdDiskTooSmall(d, required);
          return <tr key={d.by_id}>
            <td><input id={id} type="radio" name="md-replacement" value={d.by_id} disabled={small} checked={selected === d.by_id}
              onChange={() => { setSelected(d.by_id); setConfirmation(""); }} aria-label={`Use ${d.by_id}`} /></td>
            <DiskCells device={d} htmlFor={id} />
            <td className={small ? "zfs-old" : "muted"}>{small ? `Too small — needs at least ${size(required)}` : "Fits"}</td>
          </tr>;
        })}
      </tbody></table></div>
    </fieldset>}
    <BlockedDisks devices={blocked} />
    {device && dirty(device) && !mdDiskTooSmall(device, required) &&
      <label>Type this by-id device name to erase its old data<input required autoComplete="off" value={confirmation} onChange={event => setConfirmation(event.target.value)} /><span className="mono">{device.by_id}</span></label>}
    <div className="actions">
      <button type="button" disabled={busy} onClick={() => void load()}>Refresh disks</button>
      <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
      <button type="button" className="danger" disabled={busy || !ready} onClick={() => void submit()}>
        {submitting ? "Submitting…" : device ? `Erase ${shortName(device)} and rebuild the mirror` : "Erase disk and rebuild the mirror"}
      </button>
    </div>
  </dialog>;
}
