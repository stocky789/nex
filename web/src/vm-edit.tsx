// SPDX-License-Identifier: AGPL-3.0-only
import { useRef, useState, type FormEvent } from "react";
import type { Host, Media, Network, Storage, VM, VMSpec } from "./managed";
import { adapterNames, adapterNotes, applyTopology, autoDiskName, busNames, cpuName, availableBaseline, baselineSupported, baselineUnavailable, fixedCPU, recommendedCPU, selectCPU, cpuSelectionIssue, cpuMigrationHelp, describeNetwork, driversDriveReady, effectiveBoot, mediaUsable, networkUsable, readyHost, topologyIssue, vmNameIssue, type DiskRow, type NICRow } from "./vm-create";
import { storageUsable } from "./storage";
import { Icon } from "./icons";

type API = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
// The draft always carries an explicit Secure Boot and TPM choice, even for a definition
// written before those fields existed: an unset secure_boot on a UEFI VM is the enrolled-keys
// firmware libvirt already gave it, which is "on", and an unset tpm is no TPM device. Saving
// records that choice without changing what the VM boots with, so no stop is demanded for it.
// PUT replaces the whole definition, so the draft carries the tags too: an edit without them
// would clear every tag the VM has.
type Draft = { name: string; description: string; guest_os: string; cpu: VMSpec["cpu"]; memory: VMSpec["memory"]; disks: DiskRow[]; nics: NICRow[]; boot: VMSpec["boot"] & { secure_boot: string; tpm: string }; media_id: string; drivers_media_id: string; autostart: boolean; tags: string[] };
type Props = { vm: VM; host?: Host; poolName: string; storage: Storage[]; networks: Network[]; media: Media[]; api: API; reload: () => Promise<void>; unavailable: boolean; onNotice: (text: string) => void; onClose: () => void };

const macPattern = "[0-9a-f][02468ace](:[0-9a-f]{2}){5}";
const stopFirst = "Stop the VM to change this.";
const num = (v: number) => Number.isNaN(v) ? "" : v;

// draftOf is a VM's current definition as the complete update the editors submit.
export function draftOf(vm: VM): Draft {
  const spec = vm.spec!;
  return {
    name: vm.name, description: spec.description, guest_os: spec.guest_os || "other",
    cpu: { ...spec.cpu }, memory: { ...spec.memory },
    disks: (spec.disks || []).map(d => ({ ...d })), nics: (spec.nics || []).map(n => ({ ...n, mtu: n.mtu || 0 })),
    boot: { ...spec.boot, secure_boot: spec.boot.secure_boot || (spec.boot.firmware === "uefi" ? "on" : ""), tpm: spec.boot.tpm || "none" },
    media_id: spec.media_id, drivers_media_id: spec.drivers_media_id || "", autostart: spec.autostart, tags: spec.tags ?? [],
  };
}
export const updateBody = (vm: VM, draft: Draft) => ({ pool_id: vm.pool_id, host_id: vm.host_id, ...draft, boot: effectiveBoot(draft.boot), start_after_create: false });

// editBlocker explains why a VM cannot be edited now; empty means the Edit button is live.
export function editBlocker(vm: VM, host?: Host): string {
  return vm.protected ? "The Controller appliance is managed from its host's local page."
    : vm.status === "failed" || vm.status === "needs_attention" ? "The creation task did not succeed; check it in Tasks."
    : vm.status && vm.status !== "succeeded" ? "Wait for the creation task to finish."
    : !vm.spec ? "This VM has no definition to edit."
    : !host || !readyHost(host) ? "Editing needs a freshly connected host with managed-VM support."
    : !host.capabilities?.includes("vm-update-v1") ? "The host agent does not support editing VMs; upgrade Nex Core."
    : "";
}

// firmwareIssue holds Save on a UEFI-to-BIOS change until the operator acknowledges that Core
// deletes the VM's UEFI variable store (boot entries and Secure Boot variables); switching back
// starts from a fresh store.
export function firmwareIssue(current: string, next: string, acknowledged: boolean): string {
  return current === "uefi" && next === "bios" && !acknowledged ? "Confirm that switching to BIOS deletes this VM's UEFI variable store." : "";
}

// bootIssue mirrors the shared validator: boot order values are unique and positive; gaps are
// allowed (libvirt orders by bootindex), and 0 leaves a device out.
function bootIssue(d: Draft): string {
  const orders = [...d.disks.map(x => x.boot_order), d.boot.media_boot_order].filter(o => o > 0);
  if (!orders.length) return "At least one disk or the CD drive must have a boot order.";
  if (orders.some((o, i) => orders.indexOf(o) !== i)) return "Boot order values must be unique; 0 leaves a device out.";
  return "";
}

// VMEditor turns the VM details into a form for the complete definition. It submits one
// PUT with a stable request identity per payload, so an uncertain outcome retries the same
// request. Fields a running guest cannot take are disabled with the reason; the host's own
// observation remains the authority.
export function VMEditor({ vm, host, poolName, storage, networks, media, api, reload, unavailable, onNotice, onClose }: Props) {
  const spec = vm.spec!;
  const running = vm.state === "running";
  const [draft, setDraft] = useState<Draft>(() => draftOf(vm));
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const [nvramAck, setNVRAMAck] = useState(false);
  const ids = useRef<Record<string, string>>({});
  const existingDisks = new Set((spec.disks || []).map(d => d.name));
  const existingMACs = new Set((spec.nics || []).map(n => n.mac).filter(Boolean));
  const edit = (patch: Partial<Draft>) => setDraft(d => ({ ...d, ...patch }));
  const setDisk = (i: number, patch: Partial<DiskRow>) => edit({ disks: draft.disks.map((d, n) => n === i ? { ...d, ...patch } : d) });
  const setNIC = (i: number, patch: Partial<NICRow>) => edit({ nics: draft.nics.map((n, x) => x === i ? { ...n, ...patch } : n) });
  const hostStorage = storage.filter(s => storageUsable(s, vm.host_id));
  const hostNetworks = networks.filter(n => networkUsable(n, vm.host_id)).sort((a, b) => (a.vlan || 0) - (b.vlan || 0) || a.name.localeCompare(b.name));
  const availableMedia = media.filter(m => mediaUsable(m, vm.host_id, storage));
  const driversReady = driversDriveReady(host);
  const baseline = availableBaseline(host);
  // Only one option is the recommendation: a saved fixed CPU that already matches the pool
  // baseline carries it, so the pool baseline option does not repeat it.
  const savedRecommended = fixedCPU(draft.cpu) && recommendedCPU(draft.cpu, draft.boot.machine, host);
  const baselineWarning = baselineUnavailable(host);
  const cpuIssue = cpuSelectionIssue(draft.cpu, host);
  const uefi = draft.boot.firmware === "uefi";
  const maxCPUs = Math.min(host?.inventory?.cpu_count || 64, 64);
  const maxMiB = Math.min(host?.inventory ? Math.floor(host.inventory.memory_bytes / 1024 ** 2) : 262144, 262144);
  const topoIssue = topologyIssue(draft.cpu);
  const names = draft.disks.map(d => d.name);
  const diskIssue = !draft.disks.length ? "A VM needs at least one disk." : names.some((n, i) => names.indexOf(n) !== i) ? "Disk names must be unique within this VM." : "";
  const macs = draft.nics.map(n => n.mac).filter(Boolean);
  const macIssue = macs.some(m => !new RegExp(`^${macPattern}$`).test(m)) ? "A manual MAC must be six lower-case hex octets separated by colons and unicast, such as 52:54:00:12:34:56."
    : macs.some((m, i) => macs.indexOf(m) !== i) ? "MAC addresses must be unique within this VM." : "";
  const orderIssue = bootIssue(draft);
  const dropsNVRAM = spec.boot.firmware === "uefi" && draft.boot.firmware === "bios";
  const issue = cpuIssue || vmNameIssue(draft.name) || topoIssue || diskIssue || macIssue || orderIssue || firmwareIssue(spec.boot.firmware, draft.boot.firmware, nvramAck);
  const lock = (title = stopFirst) => running ? { disabled: true, title } : {};

  function removeDisk(i: number) {
    const disk = draft.disks[i];
    if (running && disk.boot_order > 0) { setError("Removing a disk that is in the boot order changes the boot order. Stop the VM first."); return; }
    // Closing the gap it leaves is a boot-order change, which only a stopped VM can take.
    const rest = draft.disks.filter((_, n) => n !== i);
    const keys = [...rest.map((d, n) => ({ k: `disk-${n}`, o: d.boot_order })), { k: "media", o: draft.boot.media_boot_order }].filter(x => x.o > 0).sort((a, b) => a.o - b.o);
    const at = (k: string) => keys.findIndex(x => x.k === k) + 1;
    edit({ disks: rest.map((d, n) => ({ ...d, boot_order: at(`disk-${n}`) })), boot: { ...draft.boot, media_boot_order: at("media") } });
    setError("");
  }
  function addDisk() {
    let n = draft.disks.length;
    while (draft.disks.some(d => d.name === autoDiskName(poolName, vm.name, n))) n++;
    edit({ disks: [...draft.disks, { storage_id: hostStorage.length === 1 ? hostStorage[0].id : "", name: autoDiskName(poolName, vm.name, n), size_gib: 20, bus: "virtio", thin: true, boot_order: 0 }] });
  }
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (issue) { setError(issue); return; }
    const payload = updateBody(vm, draft);
    const encoded = JSON.stringify(payload);
    ids.current[encoded] ??= crypto.randomUUID();
    setWorking(true); setError("");
    try {
      await api(`vms/${encodeURIComponent(vm.id)}`, "PUT", { ...payload, request_id: ids.current[encoded] });
      delete ids.current[encoded];
      onNotice(`Changes to ${vm.name} requested. Follow the vm-update task; the details update with the host's next report.`);
      await reload();
      onClose();
    } catch (err) {
      const code = (err as Error & { status?: number }).status;
      if ([400, 404].includes(code || 0)) delete ids.current[encoded];
      setError((err as Error).message);
    } finally { setWorking(false); }
  }

  return <form className="vm-editor" aria-label="Edit VM" onSubmit={submit}>
    <p className="help">{running ? "The VM is running. Greyed fields wait until it is stopped; disks, network interfaces and the install ISO can still be added, removed or changed." : vm.state === "shut off" ? "The VM is stopped, so every field can be changed." : `The VM's observed state is ${vm.state || "unknown"}; the host decides what it can apply.`}</p>
    {vm.freshness !== "fresh" && <p className="warning">The host has not reported recently, so the observed state may be out of date.</p>}
    <fieldset>
      <legend>Identity</legend>
      <div className="vm-fields">
        <label>Name<input required maxLength={200} ref={el => { el?.setCustomValidity(vmNameIssue(draft.name)); }} value={draft.name} autoFocus {...lock()} onChange={e => edit({ name: e.target.value })} /></label>
        <label>Guest operating system<select value={draft.guest_os} onChange={e => edit({ guest_os: e.target.value })}><option value="linux">Linux</option><option value="windows">Windows</option><option value="other">Other</option></select></label>
      </div>
      <label>Description<textarea rows={2} maxLength={4096} value={draft.description} onChange={e => edit({ description: e.target.value })} /></label>
    </fieldset>
    <fieldset>
      <legend>CPU and memory</legend>
      <div className="vm-fields">
        <label>Virtual CPUs<input type="number" required min={1} max={maxCPUs} step={1} value={num(draft.cpu.count)} {...lock()} onChange={e => edit({ cpu: { ...draft.cpu, count: e.target.valueAsNumber } })} /></label>
        <label>Sockets<input type="number" min={1} max={maxCPUs} step={1} value={draft.cpu.sockets || ""} {...lock()} onChange={e => edit({ cpu: applyTopology(draft.cpu, "sockets", e.target.valueAsNumber) })} /></label>
        <label>Cores per socket<input type="number" min={1} max={maxCPUs} step={1} value={draft.cpu.cores || ""} {...lock()} onChange={e => edit({ cpu: applyTopology(draft.cpu, "cores", e.target.valueAsNumber) })} /></label>
        <label>Threads per core<input type="number" min={1} max={maxCPUs} step={1} value={draft.cpu.threads || ""} {...lock()} onChange={e => edit({ cpu: applyTopology(draft.cpu, "threads", e.target.valueAsNumber) })} /></label>
        <label>CPU model<select required value={fixedCPU(draft.cpu) ? "__saved-baseline" : draft.cpu.model} {...(vm.state !== "shut off" ? { disabled: true, title: stopFirst } : {})} onChange={e => {
          const selection = e.target.value;
          edit({ cpu: selectCPU(draft.cpu, selection, host), ...(selection === "__pool-baseline" && baseline ? { boot: { ...draft.boot, machine: baseline.machine } } : {}) });
        }}>
          {fixedCPU(draft.cpu) && <option value="__saved-baseline">Saved fixed CPU baseline · {draft.cpu.model}{savedRecommended ? " (Recommended)" : ""}</option>}
          {baselineSupported(host) && <option value="__pool-baseline" disabled={!baseline}>Pool CPU baseline{baseline ? ` · ${baseline.model}${savedRecommended ? "" : " (Recommended)"}` : " · unavailable"}</option>}
          <option value="host-passthrough">Host passthrough</option><option value="host-model">Host model</option>
          {host?.inventory?.cpu_models?.filter(m => m !== "host-passthrough" && m !== "host-model").map(m => <option key={m} value={m}>{cpuName(m)}</option>)}
          {!fixedCPU(draft.cpu) && !["host-passthrough", "host-model", ...host?.inventory?.cpu_models || []].includes(draft.cpu.model) && <option value={draft.cpu.model} disabled>{cpuName(draft.cpu.model)} · saved model not reported by this host</option>}
        </select></label>
        <label>Memory mode<select value={draft.memory.mode} {...lock()} onChange={e => edit({ memory: { ...draft.memory, mode: e.target.value } })}><option value="dedicated">Dedicated · lock RAM while running</option><option value="maximum">Maximum · a ceiling only</option></select></label>
        <label>Memory (MiB)<input type="number" required min={512} max={maxMiB} step={1} value={num(draft.memory.size_mib)} {...lock()} onChange={e => edit({ memory: { ...draft.memory, size_mib: e.target.valueAsNumber } })} /></label>
      </div>
      {baselineWarning && <p className="warning">{baselineWarning} Existing CPU selections are kept.</p>}
      {cpuIssue && <p className="task-error" role="alert">{cpuIssue}</p>}
      <p className="help">{cpuMigrationHelp}</p>
      {draft.memory.mode === "dedicated" && <p className="help">Dedicated mode allocates and locks the full amount in host RAM while running, preventing swap and sharing. Stopping releases RAM but keeps its Nex capacity commitment. A VM defined by an older Nex Core keeps the memory backing it was defined with until it is stopped and saved again; the host refuses a live edit of such a VM and says so.</p>}
      {topoIssue && <p className="task-error">{topoIssue}</p>}
    </fieldset>
    <fieldset>
      <legend>Disks</legend>
      {draft.disks.map((d, i) => {
        const existing = existingDisks.has(d.name);
        const fixed = existing ? { disabled: true, title: "Cannot be changed after creation." } : {};
        return <fieldset key={i}>
          <legend>Disk {i + 1}{existing ? "" : " · new"}</legend>
          <div className="vm-fields">
            <label>Storage<select required value={d.storage_id} {...fixed} onChange={e => setDisk(i, { storage_id: e.target.value })}><option value="">Select VM-ready storage</option>{storage.filter(s => storageUsable(s, vm.host_id) || s.id === d.storage_id).map(s => <option key={s.id} value={s.id}>{s.name} · {s.kind || "local"}</option>)}</select></label>
            <label>Disk name<input required maxLength={63} pattern={"[A-Za-z0-9][A-Za-z0-9._\\-]{0,62}"} value={d.name} {...(existing ? { disabled: true, title: "A disk is identified by its name; it cannot be renamed." } : {})} onChange={e => setDisk(i, { name: e.target.value })} /></label>
            <label>Size (GiB)<input type="number" required min={1} max={2048} step={1} value={num(d.size_gib)} {...fixed} onChange={e => setDisk(i, { size_gib: e.target.valueAsNumber })} /></label>
            <label>Bus<select value={d.bus} {...lock()} onChange={e => setDisk(i, { bus: e.target.value })}>{Object.entries(busNames).map(([v, t]) => <option key={v} value={v} disabled={running && v === "sata"}>{t}{running && v === "sata" ? " · stop the VM first" : ""}</option>)}</select></label>
            <label>Provisioning<select value={d.thin ? "thin" : "thick"} {...fixed} onChange={e => setDisk(i, { thin: e.target.value === "thin" })}><option value="thin">Thin · grows as written</option><option value="thick">Thick · allocated now</option></select></label>
            <label>Boot order<input type="number" min={0} max={9} step={1} value={num(d.boot_order)} {...lock()} onChange={e => setDisk(i, { boot_order: e.target.valueAsNumber || 0 })} /></label>
          </div>
          <div className="actions"><button type="button" disabled={working || (running && d.boot_order > 0)} title={running && d.boot_order > 0 ? "This disk is in the boot order; stop the VM first." : undefined} onClick={() => removeDisk(i)}>Remove disk {i + 1}</button></div>
        </fieldset>;
      })}
      {diskIssue && <p className="task-error">{diskIssue}</p>}
      <div className="actions"><button type="button" disabled={working || draft.disks.length >= 8} onClick={addDisk}>Add disk</button></div>
      <p className="help">A removed disk is detached from the VM and its file kept on the host; an edit never deletes data. A running guest must release a disk before the host can detach it.</p>
    </fieldset>
    <fieldset>
      <legend>Network interfaces</legend>
      {draft.nics.map((n, i) => {
        const existing = existingMACs.has(n.mac);
        const fixed = existing ? lock() : {};
        return <fieldset key={i}>
          <legend>Network interface {i + 1}{existing ? "" : " · new"}</legend>
          <div className="vm-fields">
            <label>Network<select required value={n.network_id} {...fixed} onChange={e => setNIC(i, { network_id: e.target.value })}><option value="">Select validated network</option>{networks.filter(x => networkUsable(x, vm.host_id) || x.id === n.network_id).map(x => <option key={x.id} value={x.id}>{x.name} · {describeNetwork(x)}</option>)}</select></label>
            <label>Adapter model<select value={n.model} {...fixed} onChange={e => setNIC(i, { model: e.target.value, ...(e.target.value === "virtio" ? {} : { mtu: 0 }) })}>{Object.entries(adapterNames).map(([v, t]) => <option key={v} value={v}>{t}</option>)}</select><small>{adapterNotes[n.model]}</small></label>
            <label>MAC address<input placeholder="Automatic" pattern={macPattern} value={n.mac} {...fixed} onChange={e => setNIC(i, { mac: e.target.value.trim().toLowerCase().replace(/-/g, ":") })} /></label>
            {n.model === "virtio" && <label>MTU<input type="number" min={1280} max={9216} step={1} placeholder="Network default" value={n.mtu || ""} {...fixed} onChange={e => setNIC(i, { mtu: e.target.valueAsNumber || 0 })} /></label>}
          </div>
          <div className="actions"><button type="button" disabled={working} onClick={() => edit({ nics: draft.nics.filter((_, x) => x !== i) })}>Remove network interface {i + 1}</button></div>
        </fieldset>;
      })}
      {macIssue && <p className="task-error">{macIssue}</p>}
      <div className="actions"><button type="button" disabled={working || draft.nics.length >= 8} onClick={() => edit({ nics: [...draft.nics, { network_id: hostNetworks.length === 1 ? hostNetworks[0].id : "", model: "virtio", mac: "", mtu: 0 }] })}>Add network interface</button></div>
      {!hostNetworks.length && <p className="help">No verified network on this host.</p>}
    </fieldset>
    <fieldset>
      <legend>Boot and media</legend>
      <div className="vm-fields">
        <label>Firmware<select value={draft.boot.firmware} {...lock()} onChange={e => edit({ boot: { ...draft.boot, firmware: e.target.value } })}><option value="uefi">UEFI</option><option value="bios">BIOS</option></select></label>
        <label>Machine type<input maxLength={32} placeholder="q35" pattern="[a-z0-9][a-z0-9._-]{0,31}" value={draft.boot.machine} {...lock()} onChange={e => edit({ boot: { ...draft.boot, machine: e.target.value } })} /></label>
        <label>Secure Boot<select value={uefi ? (draft.boot.secure_boot === "off" ? "off" : "on") : "unavailable"} {...(uefi ? lock() : { disabled: true, title: "Secure Boot needs UEFI firmware." })} onChange={e => edit({ boot: { ...draft.boot, secure_boot: e.target.value } })}>
          {!uefi && <option value="unavailable">Not available with BIOS</option>}
          <option value="on">On · only signed boot software runs</option>
          <option value="off">Off · unsigned boot media and drivers allowed</option>
        </select></label>
        <label>TPM<select value={uefi ? (draft.boot.tpm === "2.0" ? "2.0" : "none") : "unavailable"} {...(uefi ? lock() : { disabled: true, title: "An emulated TPM is offered with UEFI firmware." })} onChange={e => edit({ boot: { ...draft.boot, tpm: e.target.value } })}>
          {!uefi && <option value="unavailable">Not available with BIOS</option>}
          <option value="2.0">TPM 2.0 · emulated device</option>
          <option value="none">None</option>
        </select></label>
        <label>Install ISO<select value={draft.media_id} onChange={e => edit({ media_id: e.target.value, ...(e.target.value && e.target.value === draft.drivers_media_id ? { drivers_media_id: "" } : {}) })}>
          <option value="">No install media</option>
          {availableMedia.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
          {draft.media_id && !availableMedia.some(m => m.id === draft.media_id) && <option value={draft.media_id} disabled>{media.find(m => m.id === draft.media_id)?.name || draft.media_id} · not available</option>}
        </select></label>
        <label>Driver disc<select value={draft.drivers_media_id} disabled={!driversReady} title={driversReady ? undefined : "This host's Nex Core does not provide a second CD drive yet."} onChange={e => edit({ drivers_media_id: e.target.value })}>
          <option value="">No driver disc</option>
          {availableMedia.filter(m => m.id !== draft.media_id).map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
          {draft.drivers_media_id && !availableMedia.some(m => m.id === draft.drivers_media_id) && <option value={draft.drivers_media_id} disabled>{media.find(m => m.id === draft.drivers_media_id)?.name || draft.drivers_media_id} · not available</option>}
        </select></label>
        <label>CD drive boot order<input type="number" min={0} max={9} step={1} value={num(draft.boot.media_boot_order)} {...lock()} onChange={e => edit({ boot: { ...draft.boot, media_boot_order: e.target.valueAsNumber || 0 } })} /></label>
      </div>
      <p className="help">Lower numbers boot first; 0 leaves a device out. The CD drive keeps its place while empty, so an ISO can be ejected from a running VM. The driver disc sits in a second CD drive, for the virtio-win ISO a Windows installer needs beside its install media; it is not in the boot order.{driversReady ? "" : " This host's Nex Core does not provide a second CD drive yet; upgrade it to attach a driver disc."}</p>
      <p className="help">Secure Boot and the TPM are part of the firmware a guest is installed against: changing either needs the VM stopped, and an installed guest that stores BitLocker keys in its TPM cannot unlock without it.</p>
      {dropsNVRAM && <div className="warning" role="alert">
        <p>Switching this VM from UEFI to BIOS permanently deletes its UEFI variable store: boot entries and Secure Boot variables the guest or installer wrote. Switching back to UEFI starts from a fresh store, and an installed UEFI guest may not boot without repair. This cannot be undone.</p>
        <label className="console-option"><input type="checkbox" checked={nvramAck} onChange={e => setNVRAMAck(e.target.checked)} /> I understand the UEFI variable store will be deleted</label>
      </div>}
      {orderIssue && <p className="task-error">{orderIssue}</p>}
    </fieldset>
    <fieldset>
      <legend>Lifecycle</legend>
      <label className="console-option"><input type="checkbox" checked={draft.autostart} onChange={e => edit({ autostart: e.target.checked })} /> Start automatically when the host boots</label>
    </fieldset>
    {error && <p className="task-error" role="alert">{error}</p>}
    <div className="actions"><button type="button" disabled={working} onClick={onClose}>Cancel</button><button className="primary" disabled={working || unavailable || Boolean(issue)}>{working ? "Submitting…" : "Save changes"}</button></div>
  </form>;
}

export const tagsCapability = "vm-tags-v1";
const tagPattern = /^[a-z0-9-]{1,32}$/;
// Both the VM's host and its pool master must keep tags: an older Core would drop them.
export function tagsBlocker(vm: VM, host?: Host, master?: Host): string {
  if (!master?.capabilities?.includes(tagsCapability)) return "Requires Nex Core 0.4.8 or later on the pool master.";
  if (!host?.capabilities?.includes(tagsCapability)) return "Requires Nex Core 0.4.8 or later on this VM's host.";
  return editBlocker(vm, host);
}

// TagEditor changes only the tags, submitted as the complete current definition with the same
// request identity rules as the editor. Callers key it by the saved tags so a new host report
// replaces the draft.
export function TagEditor({ vm, host, master, api, reload, unavailable, onNotice }: { vm: VM; host?: Host; master?: Host; api: API; reload: () => Promise<void>; unavailable: boolean; onNotice: (text: string) => void }) {
  const saved = vm.spec?.tags ?? [];
  const [tags, setTags] = useState(saved);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const ids = useRef<Record<string, string>>({});
  const blocker = tagsBlocker(vm, host, master) || (unavailable ? "Controller inventory may be out of date. Refresh before changing tags." : "");
  const off = Boolean(blocker) || working;
  function add(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const tag = text.trim().toLowerCase();
    if (!tagPattern.test(tag)) { setError("A tag is 1 to 32 lowercase letters, digits or hyphens."); return; }
    if (!tags.includes(tag)) {
      if (tags.length >= 16) { setError("A VM allows at most 16 tags."); return; }
      setTags([...tags, tag].sort());
    }
    setText(""); setError("");
  }
  async function save() {
    const payload = updateBody(vm, { ...draftOf(vm), tags });
    const encoded = JSON.stringify(payload);
    ids.current[encoded] ??= crypto.randomUUID();
    setWorking(true); setError("");
    try {
      await api(`vms/${encodeURIComponent(vm.id)}`, "PUT", { ...payload, request_id: ids.current[encoded] });
      delete ids.current[encoded];
      onNotice(`Tags for ${vm.name} requested. They show here once the pool master reports them.`);
      await reload();
    } catch (err) {
      const code = (err as Error & { status?: number }).status;
      if ([400, 404].includes(code || 0)) delete ids.current[encoded];
      setError((err as Error).message);
    } finally { setWorking(false); }
  }
  return <div className="tag-editor">
    <ul className="tags" aria-label="Tags">
      {tags.map(tag => <li key={tag} className="tag">{tag}<button type="button" className="icon-button" aria-label={`Remove tag ${tag}`} disabled={off} onClick={() => setTags(tags.filter(t => t !== tag))}><Icon kind="close" size={12} /></button></li>)}
      {!tags.length && <li className="muted">No tags</li>}
    </ul>
    <form onSubmit={add}>
      <label className="sr-only" htmlFor={`tag-${vm.id}`}>New tag</label>
      <input id={`tag-${vm.id}`} placeholder="Add a tag, such as web" maxLength={32} value={text} disabled={off} onChange={e => setText(e.target.value)} />
      <button disabled={off || !text.trim()}>Add</button>
      <button type="button" className="primary" disabled={off || tags.join() === saved.join()} onClick={() => void save()}>{working ? "Saving…" : "Save tags"}</button>
    </form>
    {error && <p className="task-error" role="alert">{error}</p>}
    {blocker && <p className="help">{blocker}</p>}
  </div>;
}
