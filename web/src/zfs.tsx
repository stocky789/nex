// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { Host, ZFSPoolStatus } from "./managed";

type API = <T>(path: string, method?: string, body?: unknown, progress?: (loaded: number, total: number) => void, register?: (xhr: XMLHttpRequest) => void, signal?: AbortSignal) => Promise<T>;
export type Device = { by_id: string; model?: string; serial: string; size_bytes: number; rotational?: boolean; eligibility?: string; fingerprint: string };
export type Discovery = { status: string; error?: string; candidates?: { device?: Device }[] };
type Chosen = { host_id: string; device: Device; confirmation: string; spare?: boolean };
type Selection = Record<string, Chosen>;
type Props = { poolID: string; hosts: Host[]; api: API; unavailable: boolean; onClose: () => void; onUseExisting: () => void; onSubmitted: (name: string) => void; repositoryID?: string; initialName?: string; initialHostID?: string; excludeHostIDs?: string[] };

// Core chooses the layout; this only says which override an operator may still ask for, so a
// selection change cannot leave a layout Core would reject with a bare validation error.
export function zfsLayoutValid(layout: string, devices: number) {
  switch (layout) {
    case "auto": return true;
    case "single": return devices === 1;
    case "mirror": return devices === 2 || devices === 3;
    case "raidz1": return devices >= 3;
    case "striped_mirrors": return devices >= 4 && devices % 2 === 0;
    case "raidz2": return devices >= 4;
    case "raidz3": return devices >= 5;
    default: return false;
  }
}
// Layouts and spares that storage-zfs-provision-v1 alone rejected need replace capability on the host.
export const zfsLayoutNeedsReplace = (layout: string, devices: number) => layout === "raidz3" || (layout === "mirror" && devices === 3);
// Display only: names the layout Core's fixed default (protocol.ZFSLayout) resolves to. The request
// still omits the layout, so Core stays the authority if its default ever changes.
export const zfsAutoLayout = (devices: number) => devices <= 1 ? "single" : devices === 2 ? "mirror" : "raidz1";
// An override that no longer fits the selected device count falls back to Core's choice.
export const zfsLayoutFor = (layout: string | undefined, devices: number) => layout && zfsLayoutValid(layout, devices) ? layout : "auto";
// One ticked host in a multi-host pool is that host's own repository; anything else is a pool
// repository whose remaining hosts can be configured later. Configuring a host always joins a pool one.
export const zfsScope = (ticked: number, eligible: number, configuring: boolean) => !configuring && ticked === 1 && eligible > 1 ? "host" : "pool";

export function zfsDeviceUsable(device: Device) {
  return device.eligibility === "blank" || device.eligibility === "erase_required";
}

// A draft saved for another host must never override the host the operator just chose.
export const zfsDraftFor = <T extends { hostID?: string }>(draft: T | null, initialHostID: string) => draft && initialHostID && draft.hostID !== initialHostID ? null : draft;

// Leaves a pool observation reports as anything but ONLINE, named exactly as Core reported them.
// parent is "replacing"/"spare" when the leaf is already inside an in-flight replacement.
export const zfsFailedDevices = (pool?: ZFSPoolStatus) => (pool?.devices || []).filter(device => device.state !== "ONLINE").map(device => {
  const item: { device: string; state: string; parent?: string } = { device: device.path || device.name, state: device.state };
  if (device.parent) item.parent = device.parent;
  return item;
});
export const zfsReplaceableDevices = (pool?: ZFSPoolStatus) => zfsFailedDevices(pool).filter(device => device.parent !== "replacing" && device.parent !== "spare");

const layoutNames: Record<string, string> = { single: "Single disk", mirror: "Mirror", raidz1: "RAIDZ1", raidz2: "RAIDZ2", raidz3: "RAIDZ3", striped_mirrors: "Striped mirrors" };
// One line per layout: what it does, and what it needs when the ticked disks do not fit it.
const layoutRules: Record<string, string> = {
  single: "one disk, no redundancy",
  mirror: "every disk holds the same data",
  raidz1: "one disk of parity",
  raidz2: "two disks of parity",
  raidz3: "three disks of parity",
  striped_mirrors: "mirrored pairs striped together",
};
const layoutNeeds: Record<string, string> = {
  single: "needs exactly one disk",
  mirror: "needs two or three disks",
  raidz1: "needs three disks or more",
  raidz2: "needs four disks or more",
  raidz3: "needs five disks or more",
  striped_mirrors: "needs an even number of disks, four or more",
};
const overrides = ["single", "mirror", "raidz1", "raidz2", "raidz3", "striped_mirrors"];
export const zfsLayoutReason = (layout: string, devices: number) => zfsLayoutValid(layout, devices) ? "" : layoutNeeds[layout] || "";
export const size = (bytes: number) => bytes ? `${(bytes / 1024 ** 3).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB` : "Size not reported";
export const dirty = (device: Device) => device.eligibility === "erase_required";
export const shortName = (device: Device) => device.by_id.replace("/dev/disk/by-id/", "");
const spareCount = (count: number) => `${count} hot ${count === 1 ? "spare" : "spares"}`;
// The device body Core validates, for both provisioning and replacement.
export const deviceBody = (device: Device, confirmation: string) => ({ by_id: device.by_id, serial: device.serial, size_bytes: device.size_bytes, fingerprint: device.fingerprint, ...(dirty(device) ? { erase_confirmation: confirmation } : {}) });

// The plain arithmetic an operator does by hand, on the smallest ticked disk: ZFS's own
// metadata overhead is not modelled, which is why the figure is always shown as approximate.
export function zfsLayoutFacts(layout: string, devices: { size_bytes: number }[]) {
  const smallest = Math.min(...devices.map(device => device.size_bytes));
  const count = devices.length;
  const failures = (n: number) => `${n} disk ${n === 1 ? "failure" : "failures"}`;
  switch (layout) {
    case "mirror": return { usable: smallest, tolerates: failures(count - 1) };
    case "raidz1": return { usable: smallest * (count - 1), tolerates: failures(1) };
    case "raidz2": return { usable: smallest * (count - 2), tolerates: failures(2) };
    case "raidz3": return { usable: smallest * (count - 3), tolerates: failures(3) };
    case "striped_mirrors": return { usable: smallest * (count / 2), tolerates: "1 disk failure per mirrored pair" };
    default: return { usable: smallest * count, tolerates: "no disk failures" };
  }
}

// The one line under each host's disks, kept current as ticks change.
export function zfsLayoutSummary(layout: string, devices: { size_bytes: number }[], spares = 0) {
  if (!devices.length) return "";
  const facts = zfsLayoutFacts(layout, devices);
  return `${devices.length} data ${devices.length === 1 ? "disk" : "disks"}${spares ? ` + ${spareCount(spares)}` : ""} → ${layoutNames[layout]}, ~${size(facts.usable)} usable, survives ${facts.tolerates}`;
}

// Core already says why a disk cannot be used; the discovery listing carries that reason as the
// eligibility, so the empty state repeats Core's words rather than inventing its own.
export function zfsBlockedSummary(devices: Device[]) {
  const counts = new Map<string, number>();
  for (const device of devices) counts.set(device.eligibility || "not eligible", (counts.get(device.eligibility || "not eligible") || 0) + 1);
  return [...counts].map(([reason, count]) => `${count} ${reason}`).join(", ");
}

// Hot spares satisfy no part of the layout, so the layout is chosen from the data disks alone
// and the spares travel beside them.
export function zfsProvisionHosts(hostIDs: string[], chosen: Chosen[], layouts: Record<string, string>) {
  return hostIDs.flatMap(host_id => {
    const bodies = (spare: boolean) => chosen.filter(item => item.host_id === host_id && Boolean(item.spare) === spare).map(item => deviceBody(item.device, item.confirmation)).sort((a, b) => a.by_id.localeCompare(b.by_id));
    const devices = bodies(false);
    // A spare is rebuilt from the surviving copy, so fewer than two data disks drops every spare
    // even if a later untick left one selected — the same rule toggleSpare and review enforce.
    const spares = devices.length >= 2 ? bodies(true) : [];
    if (!devices.length) return [];
    const layout = zfsLayoutFor(layouts[host_id], devices.length);
    return [{ host_id, ...(layout !== "auto" ? { layout } : {}), devices, ...(spares.length ? { spares } : {}) }];
  });
}
export const zfsReplaceBody = (requestID: string, target: { repositoryID: string; poolID: string; hostID: string; oldDevice: string }, device: Device, confirmation: string) =>
  ({ request_id: requestID, repository_id: target.repositoryID, pool_id: target.poolID, host_id: target.hostID, old_device: target.oldDevice, device: deviceBody(device, confirmation) });

// The same disk columns in the wizard and the replace dialog, in the same order, sorted so the
// list does not move under the operator between discoveries.
export const zfsDiskOrder = (a: Device, b: Device) => a.size_bytes - b.size_bytes || a.by_id.localeCompare(b.by_id);
// sizeRequirement is the system-disk replace column: whether the candidate meets the remaining mirror member.
export const diskColumns = (spare?: boolean, sizeRequirement?: boolean) => <tr><th scope="col">Use</th><th scope="col">Size</th><th scope="col">Type</th><th scope="col">Model</th><th scope="col">Serial</th><th scope="col">State</th>{spare && <th scope="col">Spare</th>}{sizeRequirement && <th scope="col">Fit</th>}</tr>;
// The size cell doubles as the label of the row's tick, so the whole figure is a click target
// while the tick itself stays the thing the keyboard reaches.
export const DiskCells = ({ device, htmlFor }: { device: Device; htmlFor?: string }) => <><td>{htmlFor ? <label htmlFor={htmlFor}>{size(device.size_bytes)}</label> : size(device.size_bytes)}</td><td>{device.rotational ? "HDD" : "SSD"}</td><td>{device.model || "—"}</td><td className="mono" title={device.by_id}>{device.serial || shortName(device)}</td><td className={dirty(device) ? "zfs-old" : "muted"}>{dirty(device) ? "Has old data — will be erased" : "Blank"}</td></>;
export const BlockedDisks = ({ devices }: { devices: Device[] }) => devices.length ? <details className="zfs-blocked"><summary>{devices.length} unavailable {devices.length === 1 ? "disk" : "disks"}</summary><ul>{devices.map((device, index) => <li key={device.by_id || index}>{size(device.size_bytes)} · {device.serial || shortName(device) || "system or unnamed disk"} · {device.eligibility || "not eligible"}</li>)}</ul></details> : null;

export function ZFSProvisionWizard({ poolID, hosts, api, unavailable, onClose, onUseExisting, onSubmitted, repositoryID = "", initialName = "", initialHostID = "", excludeHostIDs = [] }: Props) {
  const storageKey = `nex-zfs-provision:${poolID}:${repositoryID || "new"}`;
  const restored = useRef<{ hostID?: string; hostIDs?: string[]; name?: string; layouts?: Record<string, string>; requestID?: string; submitted?: string } | null>(null);
  if (!restored.current) { try { restored.current = zfsDraftFor(JSON.parse(sessionStorage.getItem(storageKey) || "null"), initialHostID); } catch {} }
  // Neither the step, the discovered devices nor the disk selection (with its typed erase
  // confirmations) is ever restored: a reopened wizard asks Core again and must re-arm erasure.
  const [step, setStep] = useState<"disks" | "confirm">("disks");
  const liveHosts = hosts.filter(host => !host.revoked && host.connection === "fresh" && host.capabilities?.includes("storage-zfs-provision-v1") && host.capabilities?.includes("storage-discovery-v1") && !excludeHostIDs.includes(host.id) && (!repositoryID || host.id === initialHostID));
  // The hosts on offer are a snapshot taken whenever Core is asked for disks. A connection change
  // while the dialog is open must not silently flip the scope, hide a confirmation that is still
  // required, or turn an unchanged retry into a new request; Core still refuses a stale host.
  const [eligibleHosts, setEligibleHosts] = useState(liveHosts);
  const [hostIDs, setHostIDs] = useState<string[]>(() => restored.current?.hostIDs?.filter(id => liveHosts.some(host => host.id === id)) || liveHosts.map(host => host.id));
  const [devices, setDevices] = useState<Record<string, Device[]>>({});
  const [deviceErrors, setDeviceErrors] = useState<Record<string, string>>({});
  const [selections, setSelections] = useState<Selection>({});
  const [name, setName] = useState(restored.current?.name || initialName);
  const [layouts, setLayouts] = useState<Record<string, string>>(restored.current?.layouts || {});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const requestID = useRef(restored.current?.requestID || "");
  const submitted = useRef(restored.current?.submitted || "");
  const selectedHosts = eligibleHosts.filter(host => hostIDs.includes(host.id));
  const selected = Object.values(selections);
  const needsErase = selected.filter(item => dirty(item.device));
  const scope = zfsScope(selectedHosts.length, eligibleHosts.length, Boolean(repositoryID));
  // The layout is built from the data disks; hot spares belong to no vdev of it.
  const countFor = (hostID: string) => selected.filter(item => item.host_id === hostID && !item.spare).length;
  const sparesFor = (hostID: string) => selected.filter(item => item.host_id === hostID && item.spare).length;
  const layoutLabel = (hostID: string) => { const layout = zfsLayoutFor(layouts[hostID], countFor(hostID)); return layoutNames[layout === "auto" ? zfsAutoLayout(countFor(hostID)) : layout]; };
  const persist = () => { try { sessionStorage.setItem(storageKey, JSON.stringify({ hostID: initialHostID, hostIDs, name, layouts, requestID: requestID.current, submitted: submitted.current })); } catch {} };
  useEffect(persist, [storageKey, hostIDs, name, layouts]);
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); void loadDevices(); }, []);
  useEffect(() => { heading.current?.focus(); }, [step]);

  function toggle(host: Host, device: Device, checked: boolean) {
    const key = `${host.id}\n${device.by_id}`;
    setSelections(current => {
      const next = { ...current };
      if (checked) next[key] = { host_id: host.id, device, confirmation: "" };
      else delete next[key];
      // Unticking a data disk can leave a spare on a non-redundant set; give those spares back
      // as data disks so the selection keeps matching the rule toggleSpare enforces on tick.
      const data = Object.values(next).filter(item => item.host_id === host.id && !item.spare).length;
      if (data < 2) {
        for (const [entry, item] of Object.entries(next)) {
          if (item.host_id === host.id && item.spare) next[entry] = { ...item, spare: false };
        }
      }
      return next;
    });
    setError("");
  }
  function toggleSpare(host: Host, device: Device, checked: boolean) {
    // A spare is rebuilt from the surviving copy, so the data disks it backs must still be
    // redundant without it: two of them at least.
    if (checked && countFor(host.id) < 3) { setError(`Keep at least two data disks on ${host.hostname}. A hot spare is rebuilt from the other disks, so it cannot be held back from a pool that would then have no redundancy.`); return; }
    setError("");
    setSelections(current => ({ ...current, [`${host.id}\n${device.by_id}`]: { ...current[`${host.id}\n${device.by_id}`], spare: checked } }));
  }
  function toggleHost(host: Host, checked: boolean) {
    setHostIDs(current => checked ? [...current, host.id] : current.filter(id => id !== host.id));
    if (!checked) setSelections(current => Object.fromEntries(Object.entries(current).filter(([, item]) => item.host_id !== host.id)));
  }
  async function loadDevices() {
    const eligibleHosts = liveHosts;
    setEligibleHosts(eligibleHosts); setHostIDs(current => current.filter(id => eligibleHosts.some(host => host.id === id)));
    if (!eligibleHosts.length) { setError("No freshly connected host supports ZFS provisioning."); return; }
    setLoading(true); setError(""); setDevices({}); setDeviceErrors({}); setSelections({});
    // One host that cannot report devices must not hide the devices of the others; its failure
    // is shown against that host, which simply stays unconfigured.
    const replies = await Promise.allSettled(eligibleHosts.map(async host => {
      const result = await api<Discovery>("storage/discover", "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: host.id, kind: "zfs-device" });
      if (result.status !== "succeeded") throw new Error(result.error || "Core could not list devices.");
      return [host.id, result.candidates?.flatMap(candidate => candidate.device ? [candidate.device] : []) || []] as const;
    }));
    setDevices(Object.fromEntries(replies.flatMap(reply => reply.status === "fulfilled" ? [reply.value] : [])));
    setDeviceErrors(Object.fromEntries(replies.flatMap((reply, index) => reply.status === "rejected" ? [[eligibleHosts[index].id, (reply.reason as Error).message]] : [])));
    setLoading(false);
  }
  function review(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!selected.length) { setError("Tick at least one disk. A host with no disks ticked is left as it is."); return; }
    const spareOnly = selectedHosts.filter(host => sparesFor(host.id) && !countFor(host.id));
    if (spareOnly.length) { setError(`Tick at least one data disk on ${spareOnly.map(host => host.hostname).join(", ")}. Hot spares alone build no pool.`); return; }
    // Re-apply the spare redundancy rule here: unticking a data disk after marking a spare can
    // leave a single-disk pool with a hot spare, which toggleSpare alone cannot catch.
    const nonRedundant = selectedHosts.filter(host => sparesFor(host.id) && countFor(host.id) < 2);
    if (nonRedundant.length) { setError(`Keep at least two data disks on ${nonRedundant.map(host => host.hostname).join(", ")}. A hot spare is rebuilt from the other disks, so it cannot be held back from a pool that would then have no redundancy.`); return; }
    setError(""); setStep("confirm");
  }
  async function submit() {
    if (needsErase.some(item => item.confirmation !== item.device.by_id)) { setError("Type each displayed by-id device name before erasing it."); return; }
    setLoading(true); setError("");
    try {
      const payload = { ...(repositoryID ? { repository_id: repositoryID } : {}), pool_id: poolID, name: name.trim(), scope, hosts: zfsProvisionHosts(selectedHosts.map(host => host.id), selected, layouts) };
      // One request identity per submitted intent, so a retry after a lost reply replays the same
      // request. Editing the selection, layout, name or hosts is a different intent.
      // Only its digest is kept, so session storage never holds a disk selection or a typed confirmation.
      const signature = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify(payload)))), byte => byte.toString(16).padStart(2, "0")).join("");
      if (submitted.current !== signature) { requestID.current = crypto.randomUUID(); submitted.current = signature; }
      persist();
      await api("storage/zfs/provision", "POST", { request_id: requestID.current, ...payload });
      try { sessionStorage.removeItem(storageKey); } catch {};
      onSubmitted(name.trim()); onClose();
    } catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  }
  const title = repositoryID ? `Add ${eligibleHosts[0]?.hostname || "host"} to ${initialName}` : "Add ZFS storage";
  return <dialog ref={dialog} className="confirm-dialog zfs-dialog" aria-labelledby="zfs-dialog-title" onCancel={event => { event.preventDefault(); if (!loading) onClose(); }} onClose={() => { if (loading) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="zfs-dialog-title" ref={heading} tabIndex={-1}>{title}</h2><button type="button" disabled={loading} onClick={onClose}>Close</button></div>
    <ol className="steps"><li aria-current={step === "disks" ? "step" : undefined}>Choose disks</li><li aria-current={step === "confirm" ? "step" : undefined}>Review and erase</li></ol>
    {error && <p className="alert" role="alert">{error}</p>}
    {step === "disks" && <form onSubmit={review}>
      <label>Storage name<input required maxLength={100} value={name} readOnly={Boolean(repositoryID)} onChange={event => setName(event.target.value)} /></label>
      <p className="help">Tick the disks to use on each host. Every host builds its own local ZFS pool from its own disks; this is not shared storage, so moving a VM to another host copies its disks. Tick <strong>Spare</strong> to hold a disk back as a hot spare: it stores nothing until a data disk fails, and then ZFS resilvers onto it by itself.</p>
      {loading && !Object.keys(devices).length && <p role="status">Asking each host for its disks…</p>}
      {eligibleHosts.map(host => { const ticked = hostIDs.includes(host.id); const usable = (devices[host.id] || []).filter(zfsDeviceUsable).sort(zfsDiskOrder); const blocked = (devices[host.id] || []).filter(device => !zfsDeviceUsable(device)); const count = countFor(host.id); const spares = sparesFor(host.id); const replaceCapable = host.capabilities?.includes("storage-zfs-replace-v1"); const choices = overrides.filter(layout => zfsLayoutValid(layout, count) && (replaceCapable || !zfsLayoutNeedsReplace(layout, count))); const dataDisks = selected.filter(item => item.host_id === host.id && !item.spare).map(item => item.device); const rawLayout = zfsLayoutFor(layouts[host.id], count); const selectedLayout = !replaceCapable && rawLayout !== "auto" && zfsLayoutNeedsReplace(rawLayout, count) ? "auto" : rawLayout; const effective = selectedLayout === "auto" ? zfsAutoLayout(count) : selectedLayout; const eraseHere = selected.filter(item => item.host_id === host.id && dirty(item.device)).length;
        // A hot spare only makes sense once holding one back still leaves a redundant pool,
        // so the tick appears then — and stays while one is held, so it can be given back.
        // Both the Spare column and the widened layout radios need storage-zfs-replace-v1.
        const spareColumn = Boolean(replaceCapable) && (count >= 3 || spares > 0); return <fieldset key={host.id} className="zfs-host" disabled={loading}>
        <legend>{repositoryID ? host.hostname : <label className="zfs-scope"><input type="checkbox" checked={ticked} onChange={event => toggleHost(host, event.target.checked)} /> {host.hostname}</label>}</legend>
        {!ticked ? <p className="help">Not included. If only one host stays ticked, the storage belongs to that host alone and no other host can join it later. To add this host later instead, keep it ticked and tick none of its disks.</p> : deviceErrors[host.id] ? <p className="alert" role="alert">{deviceErrors[host.id]} · This host is left as it is.</p> : <>
          {usable.length ? <><div className="table-scroll"><table className="zfs-disk-table"><thead>{diskColumns(spareColumn)}</thead><tbody>{usable.map(device => { const chosen = selections[`${host.id}\n${device.by_id}`]; return <tr key={device.by_id}><td><input id={`zfs-use-${host.id}-${shortName(device)}`} aria-label={`Use ${device.by_id}`} type="checkbox" checked={Boolean(chosen)} onChange={event => toggle(host, device, event.target.checked)} /></td><DiskCells device={device} htmlFor={`zfs-use-${host.id}-${shortName(device)}`} />{spareColumn && <td>{chosen ? <input aria-label={`Keep ${device.by_id} as a hot spare`} type="checkbox" checked={Boolean(chosen.spare)} onChange={event => toggleSpare(host, device, event.target.checked)} /> : "—"}</td>}</tr>; })}</tbody></table></div>
          {spareColumn && <p className="help">A hot spare stores nothing until a data disk fails; ZFS then resilvers onto it by itself.</p>}
          {eraseHere > 0 && <p className="zfs-old">{eraseHere} ticked {eraseHere === 1 ? "disk holds" : "disks hold"} old data. You type each device name on the next step before anything is erased.</p>}</> : devices[host.id] && <p className="empty">No disk on this host can be used for new storage{blocked.length ? `: ${zfsBlockedSummary(blocked)}` : ""}. Fit a disk Nex can erase, then refresh.</p>}
          <BlockedDisks devices={blocked} />
          {count > 0 && <>
            <p className="zfs-layout" role="status"><strong>{zfsLayoutSummary(effective, dataDisks, replaceCapable ? spares : 0)}</strong>{count === 1 && <span className="zfs-old"> · no redundancy</span>}</p>
            {choices.length > 1 ? <fieldset className="zfs-layouts"><legend>Layout</legend>
              <label className="zfs-scope"><input type="radio" name={`zfs-layout-${host.id}`} checked={selectedLayout === "auto"} onChange={() => setLayouts(current => ({ ...current, [host.id]: "auto" }))} /> Automatic — Core's default for {count} {count === 1 ? "disk" : "disks"}: {layoutNames[zfsAutoLayout(count)]}</label>
              {overrides.filter(layout => replaceCapable || !zfsLayoutNeedsReplace(layout, count)).map(layout => { const reason = zfsLayoutReason(layout, count); return <label key={layout} className={reason ? "zfs-scope muted" : "zfs-scope"}><input type="radio" name={`zfs-layout-${host.id}`} disabled={Boolean(reason)} checked={selectedLayout === layout} onChange={() => setLayouts(current => ({ ...current, [host.id]: layout }))} /> {layoutNames[layout]} — {reason || layoutRules[layout]}</label>; })}
            </fieldset> : <p className="help">{count} {count === 1 ? "disk builds" : "disks build"} only {layoutNames[zfsAutoLayout(count)]}: {layoutRules[zfsAutoLayout(count)]}. Tick another disk for a choice of layouts.</p>}
          </>}
        </>}
      </fieldset>; })}
      {hosts.filter(host => !host.revoked && !excludeHostIDs.includes(host.id) && !repositoryID && !eligibleHosts.includes(host)).map(host => <p key={host.id} className="help">{host.hostname} · unavailable: {host.connection !== "fresh" ? "not connected right now" : "this Core version cannot provision ZFS"}</p>)}
      <div className="actions">{!repositoryID && <button type="button" className="zfs-existing" onClick={onUseExisting}>Use an existing dataset instead</button>}<button type="button" disabled={loading || unavailable} onClick={() => void loadDevices()}>{loading ? "Loading disks…" : "Refresh disks"}</button><button type="button" disabled={loading} onClick={onClose}>Cancel</button><button className="primary" disabled={loading || unavailable || !selected.length}>Review and erase →</button></div>
    </form>}
    {step === "confirm" && <><p className="warning">Everything on the disks below is erased. Core checks each disk's name, serial and size again before it acts, and touches no other disk.</p><dl className="details"><dt>Storage name</dt><dd>{name}</dd><dt>Hosts</dt><dd>{scope === "host" ? `Only ${selectedHosts[0]?.hostname}` : `${selectedHosts.filter(host => countFor(host.id)).map(host => host.hostname).join(", ")} · other hosts can be added later`}</dd></dl>{selectedHosts.map(host => { const rows = selected.filter(item => item.host_id === host.id); return rows.length ? <fieldset key={host.id}><legend>{host.hostname} · {layoutLabel(host.id)}{sparesFor(host.id) > 0 ? ` · ${spareCount(sparesFor(host.id))}` : ""}</legend>{rows.map(item => <div key={item.device.by_id}><p className="mono">{item.device.by_id} · {item.device.serial} · {size(item.device.size_bytes)} · {item.spare ? "hot spare" : "data disk"}</p>{dirty(item.device) && <label>Type this by-id device name to erase its old data<input required autoComplete="off" value={item.confirmation} onChange={event => setSelections(current => ({ ...current, [`${host.id}\n${item.device.by_id}`]: { ...item, confirmation: event.target.value } }))} /></label>}</div>)}</fieldset> : null; })}<div className="actions"><button type="button" disabled={loading} onClick={() => setStep("disks")}>Back</button><button className="danger" type="button" disabled={loading || unavailable || needsErase.some(item => item.confirmation !== item.device.by_id)} onClick={() => void submit()}>{loading ? "Submitting…" : "Erase disks and create storage"}</button></div></>}
  </dialog>;
}

type ReplaceProps = { poolID: string; repositoryID: string; host: Host; pool?: ZFSPoolStatus; api: API; unavailable: boolean; onClose: () => void; onSubmitted: (failed: string, replacement: string) => void };

// Replacing a failed disk is one durable Core command. The resilver it starts is reported by the
// repository's ordinary pool observation, and ZFS retires the old disk itself when it finishes.
export function ZFSReplaceDialog({ poolID, repositoryID, host, pool, api, unavailable, onClose, onSubmitted }: ReplaceProps) {
  // pool comes from the live Storage inventory on each render, so a spare engaging or an
  // operator running zpool online while the dialog is open can reach the empty state below.
  const failed = zfsReplaceableDevices(pool);
  // Nothing is preselected: erasing a disk is the operator's choice to make, not a default
  // this dialog arrives with, even when the pool reports only one failed disk.
  const [oldDevice, setOldDevice] = useState("");
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [chosen, setChosen] = useState<Device | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  // One request identity per intent, so a retry after a lost reply replays the same request.
  // Choosing another failed leaf or another replacement is a different intent.
  const requestID = useRef("");
  const submitted = useRef("");
  // A leaf that came back ONLINE (or moved into a replacing/spare vdev) is no longer offered.
  const selectedFailed = failed.some(item => item.device === oldDevice) ? oldDevice : "";
  useEffect(() => { if (!dialog.current?.open) dialog.current?.showModal(); heading.current?.focus(); void loadDevices(); }, []);

  async function loadDevices() {
    setLoading(true); setError(""); setDevices(null); setChosen(null); setConfirmation("");
    try {
      const result = await api<Discovery>("storage/discover", "POST", { request_id: crypto.randomUUID(), pool_id: poolID, host_id: host.id, kind: "zfs-device" });
      if (result.status !== "succeeded") throw new Error(result.error || "Core could not list devices.");
      setDevices(result.candidates?.flatMap(candidate => candidate.device ? [candidate.device] : []) || []);
    } catch (e) { setError((e as Error).message); setDevices([]); }
    finally { setLoading(false); }
  }
  async function submit() {
    if (!selectedFailed || !chosen) { setError("Choose the failed disk and its replacement."); return; }
    if (dirty(chosen) && confirmation !== chosen.by_id) { setError("Type the displayed by-id device name before erasing it."); return; }
    setLoading(true); setError("");
    try {
      const signature = `${selectedFailed}\n${chosen.by_id}`;
      if (submitted.current !== signature) { requestID.current = crypto.randomUUID(); submitted.current = signature; }
      await api("storage/zfs/replace", "POST", zfsReplaceBody(requestID.current, { repositoryID, poolID, hostID: host.id, oldDevice: selectedFailed }, chosen, confirmation));
      onSubmitted(selectedFailed, chosen.by_id); onClose();
    } catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  }
  const usable = (devices || []).filter(zfsDeviceUsable).sort(zfsDiskOrder);
  const blocked = (devices || []).filter(device => !zfsDeviceUsable(device));
  const ready = Boolean(selectedFailed && chosen && (!dirty(chosen) || confirmation === chosen.by_id));
  return <dialog ref={dialog} className="confirm-dialog zfs-dialog" aria-labelledby="zfs-replace-title" onCancel={event => { event.preventDefault(); if (!loading) onClose(); }} onClose={() => { if (loading) dialog.current?.showModal(); else onClose(); }}>
    <div className="section-heading"><h2 id="zfs-replace-title" ref={heading} tabIndex={-1}>Replace a disk on {host.hostname}</h2><button type="button" disabled={loading} onClick={onClose}>Close</button></div>
    {error && <p className="alert" role="alert">{error}</p>}
    <fieldset disabled={loading}><legend>Failed disk</legend>
      {failed.length ? <ul className="zfs-disks">{failed.map(item => <li key={item.device}><label className="zfs-scope"><input type="radio" name="zfs-replace-old" checked={selectedFailed === item.device} onChange={() => setOldDevice(item.device)} /> <span className="mono">{item.device}</span> · {item.state.toLowerCase()}</label></li>)}</ul> : <p className="empty">This host reports no failed disk in its pool any more. Refresh the repository before replacing anything.</p>}
    </fieldset>
    <fieldset disabled={loading}><legend>Replacement disk</legend>
      {loading && !devices ? <p role="status">Asking {host.hostname} for its disks…</p> : usable.length ? <div className="table-scroll"><table className="zfs-disk-table"><thead>{diskColumns()}</thead><tbody>{usable.map(device => <tr key={device.by_id}><td><input id={`zfs-replacement-${shortName(device)}`} type="radio" name="zfs-replace-new" aria-label={`Replace with ${device.by_id}`} checked={chosen?.by_id === device.by_id} onChange={() => { setChosen(device); setConfirmation(""); }} /></td><DiskCells device={device} htmlFor={`zfs-replacement-${shortName(device)}`} /></tr>)}</tbody></table></div> : devices && <p className="empty">No disk on this host can be used as a replacement{blocked.length ? `: ${zfsBlockedSummary(blocked)}` : ""}. Fit a disk at least as large as the failed one, then refresh.</p>}
      <BlockedDisks devices={blocked} />
      {chosen && dirty(chosen) && <label>Type this by-id device name to erase its old data<input required autoComplete="off" value={confirmation} onChange={event => setConfirmation(event.target.value)} /><span className="mono">{chosen.by_id}</span></label>}
    </fieldset>
    <p className="help">Core checks both disks again before it acts, erases the replacement and puts it into the pool. The pool then resilvers onto it, which the repository shows while it runs; the VMs on it keep running throughout. <strong>ZFS retires the old disk itself when the resilver finishes</strong> — nothing removes it by hand.</p>
    <div className="actions"><button type="button" disabled={loading || unavailable} onClick={() => void loadDevices()}>{loading ? "Loading disks…" : "Refresh disks"}</button><button type="button" disabled={loading} onClick={onClose}>Cancel</button><button className="danger" type="button" disabled={loading || unavailable || !ready} onClick={() => void submit()}>{loading ? "Submitting…" : chosen ? `Erase ${shortName(chosen)} and replace` : "Erase disk and replace"}</button></div>
  </dialog>;
}
