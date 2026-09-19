// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { Host } from "./managed";

type API = <T>(path: string, method?: string, body?: unknown, progress?: (loaded: number, total: number) => void, register?: (xhr: XMLHttpRequest) => void, signal?: AbortSignal) => Promise<T>;
type Device = { by_id: string; model?: string; serial: string; size_bytes: number; rotational?: boolean; eligibility?: string; fingerprint: string };
type Discovery = { status: string; error?: string; candidates?: { device?: Device }[] };
type Selection = Record<string, { host_id: string; device: Device; confirmation: string }>;
type Props = { poolID: string; hosts: Host[]; api: API; unavailable: boolean; onClose: () => void; onUseExisting: () => void; onSubmitted: (name: string) => void; repositoryID?: string; initialName?: string; initialHostID?: string; excludeHostIDs?: string[] };

// Core chooses the layout; this only says which override an operator may still ask for, so a
// selection change cannot leave a layout Core would reject with a bare validation error.
export function zfsLayoutValid(layout: string, devices: number) {
  switch (layout) {
    case "auto": return true;
    case "single": return devices === 1;
    case "mirror": return devices === 2;
    case "raidz1": return devices >= 3;
    case "striped_mirrors": return devices >= 4 && devices % 2 === 0;
    case "raidz2": return devices >= 4;
    default: return false;
  }
}
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

const layoutNames: Record<string, string> = { single: "Single disk", mirror: "Mirror", raidz1: "RAIDZ1", raidz2: "RAIDZ2", striped_mirrors: "Striped mirrors" };
const overrides = ["raidz1", "striped_mirrors", "raidz2", "mirror", "single"];
const size = (bytes: number) => bytes ? `${(bytes / 1024 ** 3).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB` : "Size not reported";
const dirty = (device: Device) => device.eligibility === "erase_required";
const shortName = (device: Device) => device.by_id.replace("/dev/disk/by-id/", "");

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
  const countFor = (hostID: string) => selected.filter(item => item.host_id === hostID).length;
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
      return next;
    });
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
    setError(""); setStep("confirm");
  }
  async function submit() {
    if (needsErase.some(item => item.confirmation !== item.device.by_id)) { setError("Type each displayed by-id device name before erasing it."); return; }
    setLoading(true); setError("");
    try {
      const byHost: Record<string, { by_id: string; serial: string; size_bytes: number; fingerprint: string; erase_confirmation?: string }[]> = {};
      for (const host of selectedHosts) byHost[host.id] = selected.filter(item => item.host_id === host.id).map(item => ({ by_id: item.device.by_id, serial: item.device.serial, size_bytes: item.device.size_bytes, fingerprint: item.device.fingerprint, ...(dirty(item.device) ? { erase_confirmation: item.confirmation } : {}) })).sort((a, b) => a.by_id.localeCompare(b.by_id));
      const payload = { ...(repositoryID ? { repository_id: repositoryID } : {}), pool_id: poolID, name: name.trim(), scope, hosts: Object.entries(byHost).filter(([, selectedDevices]) => selectedDevices.length).map(([host_id, selectedDevices]) => { const layout = zfsLayoutFor(layouts[host_id], selectedDevices.length); return { host_id, ...(layout !== "auto" ? { layout } : {}), devices: selectedDevices }; }) };
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
      <p className="help">Tick the disks to use on each host. Every host builds its own local ZFS pool from its own disks; this is not shared storage, so moving a VM to another host copies its disks.</p>
      {loading && !Object.keys(devices).length && <p role="status">Asking each host for its disks…</p>}
      {eligibleHosts.map(host => { const ticked = hostIDs.includes(host.id); const usable = (devices[host.id] || []).filter(zfsDeviceUsable); const blocked = (devices[host.id] || []).filter(device => !zfsDeviceUsable(device)); const count = countFor(host.id); const choices = overrides.filter(layout => zfsLayoutValid(layout, count)); return <fieldset key={host.id} className="zfs-host" disabled={loading}>
        <legend>{repositoryID ? host.hostname : <label className="zfs-scope"><input type="checkbox" checked={ticked} onChange={event => toggleHost(host, event.target.checked)} /> {host.hostname}</label>}</legend>
        {!ticked ? <p className="help">Not included. If only one host stays ticked, the storage belongs to that host alone and no other host can join it later. To add this host later instead, keep it ticked and tick none of its disks.</p> : deviceErrors[host.id] ? <p className="alert" role="alert">{deviceErrors[host.id]} · This host is left as it is.</p> : <>
          {usable.length ? <ul className="zfs-disks">{usable.map(device => <li key={device.by_id}><label className="zfs-scope"><input aria-label={`Use ${device.by_id}`} aria-describedby={`zfs-disk-${host.id}-${shortName(device)}`} type="checkbox" checked={Boolean(selections[`${host.id}\n${device.by_id}`])} onChange={event => toggle(host, device, event.target.checked)} /><span id={`zfs-disk-${host.id}-${shortName(device)}`} className="zfs-disk"><strong>{size(device.size_bytes)} {device.rotational ? "HDD" : "SSD"}</strong><span title={device.by_id}>{device.model ? `${device.model} · ` : ""}{device.serial || shortName(device)}</span><span className={dirty(device) ? "zfs-old" : "muted"}>{dirty(device) ? "Has old data — will be erased" : "Blank"}</span></span></label></li>)}</ul> : devices[host.id] && <p className="empty">No spare disks on this host.</p>}
          {blocked.length > 0 && <details className="zfs-blocked"><summary>{blocked.length} unavailable {blocked.length === 1 ? "disk" : "disks"}</summary><ul>{blocked.map((device, index) => <li key={device.by_id || index}>{size(device.size_bytes)} · {device.serial || shortName(device) || "system or unnamed disk"} · {device.eligibility || "not eligible"}</li>)}</ul></details>}
          {count > 0 && <p className="zfs-layout">{count} {count === 1 ? "disk" : "disks"} → <strong>{layoutLabel(host.id)}</strong>{count === 1 && <span className="zfs-old"> · no redundancy</span>}{choices.length > 1 && <label>Layout<select value={zfsLayoutFor(layouts[host.id], count)} onChange={event => setLayouts(current => ({ ...current, [host.id]: event.target.value }))}><option value="auto">Automatic ({layoutNames[zfsAutoLayout(count)]})</option>{choices.map(layout => <option key={layout} value={layout}>{layoutNames[layout]}</option>)}</select></label>}</p>}
        </>}
      </fieldset>; })}
      {hosts.filter(host => !host.revoked && !excludeHostIDs.includes(host.id) && !repositoryID && !eligibleHosts.includes(host)).map(host => <p key={host.id} className="help">{host.hostname} · unavailable: {host.connection !== "fresh" ? "not connected right now" : "this Core version cannot provision ZFS"}</p>)}
      <div className="actions">{!repositoryID && <button type="button" className="zfs-existing" onClick={onUseExisting}>Use an existing dataset instead</button>}<button type="button" disabled={loading || unavailable} onClick={() => void loadDevices()}>{loading ? "Loading disks…" : "Refresh disks"}</button><button type="button" disabled={loading} onClick={onClose}>Cancel</button><button className="primary" disabled={loading || unavailable || !selected.length}>Review and erase →</button></div>
    </form>}
    {step === "confirm" && <><p className="warning">Everything on the disks below is erased. Core checks each disk's name, serial and size again before it acts, and touches no other disk.</p><dl className="details"><dt>Storage name</dt><dd>{name}</dd><dt>Hosts</dt><dd>{scope === "host" ? `Only ${selectedHosts[0]?.hostname}` : `${selectedHosts.filter(host => countFor(host.id)).map(host => host.hostname).join(", ")} · other hosts can be added later`}</dd></dl>{selectedHosts.map(host => { const rows = selected.filter(item => item.host_id === host.id); return rows.length ? <fieldset key={host.id}><legend>{host.hostname} · {layoutLabel(host.id)}</legend>{rows.map(item => <div key={item.device.by_id}><p className="mono">{item.device.by_id} · {item.device.serial} · {size(item.device.size_bytes)}</p>{dirty(item.device) && <label>Type this by-id device name to erase its old data<input required autoComplete="off" value={item.confirmation} onChange={event => setSelections(current => ({ ...current, [`${host.id}\n${item.device.by_id}`]: { ...item, confirmation: event.target.value } }))} /></label>}</div>)}</fieldset> : null; })}<div className="actions"><button type="button" disabled={loading} onClick={() => setStep("disks")}>Back</button><button className="danger" type="button" disabled={loading || unavailable || needsErase.some(item => item.confirmation !== item.device.by_id)} onClick={() => void submit()}>{loading ? "Submitting…" : "Erase disks and create storage"}</button></div></>}
  </dialog>;
}
