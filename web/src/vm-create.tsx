// SPDX-License-Identifier: AGPL-3.0-only
import { useRef, useState } from "react";
import type { Host, Media, Network, Storage, VMCPU } from "./managed";
import { storageUsable } from "./storage";

export const describeNetwork = (n: Network) => n.kind === "network" ? `libvirt network ${n.source}` : n.uplink ? (n.vlan ? `VLAN ${n.vlan} on ${n.uplink}` : `Untagged on ${n.uplink}`) : `Existing bridge ${n.source}`;
export const readyHost = (h: Host) => !h.revoked && h.connection === "fresh" && Boolean(h.capabilities?.includes("managed-vm-v1"));
export type PlacementState = { maintenance?: boolean; reason?: string };
// Maintenance belongs to the pool master. A fresh host inventory by itself is not enough to
// offer it as a VM target until that authoritative observation is available.
export function placementBlocker(h: Host, state?: PlacementState): string {
  if (!readyHost(h)) return h.connection !== "fresh" ? "Host is not freshly connected." : "The host agent needs managed-VM support.";
  if (!state) return "Checking the pool master's maintenance report.";
  if (state.reason) return state.reason;
  return state.maintenance ? "This host is in maintenance; the pool master refuses new VM placement here." : "";
}

/** Pool VLAN usable on a host when registered there, or when the master reported member apply. */
export const networkUsable = (n: Network, hostID: string) => {
  if (n.status !== "succeeded") return false;
  if (n.kind !== "vlan") return n.host_id === hostID;
  if (n.host_id === hostID) return true;
  return n.member_access?.some(a => a.host_id === hostID && a.uplink_present && !a.error && a.bridge === n.source) === true;
};

/** ISO usable on a host when imported there, or verified on shared storage for that member. */
export const mediaUsable = (m: Media, hostID: string, storage: Storage[]) => {
  if (m.status !== "succeeded") return false;
  if (m.host_id === hostID) return true;
  const repo = storage.find(s => s.id === m.storage_id);
  if (!repo || (repo.kind !== "nfs" && repo.kind !== "smb" && !(repo.kind === "iscsi" && repo.source?.iscsi?.mode === "clustered_gfs2"))) return false;
  return m.member_access?.some(a => a.host_id === hostID && a.storage_id === m.storage_id && a.sha256 === m.sha256 && !a.error) === true;
};

export type DiskRow = { storage_id: string; name: string; size_gib: number; bus: string; thin: boolean; boot_order: number };
export type NICRow = { network_id: string; model: string; mac: string; mtu: number };
export type BootRow = { firmware: string; machine: string; media_boot_order: number; secure_boot: string; tpm: string };
// Draft is the request body itself, so a restored uncertain submission spreads straight back in.
type Draft = {
  host_id: string; name: string; description: string; guest_os: string;
  cpu: VMCPU;
  memory: { mode: string; size_mib: number };
  disks: DiskRow[];
  nics: NICRow[];
  boot: BootRow;
  media_id: string; drivers_media_id: string; start_after_create: boolean; autostart: boolean;
};
type Props = { poolID: string; poolName: string; hosts: Host[]; placement: Record<string, PlacementState> | null; storage: Storage[]; networks: Network[]; media: Media[]; restored: () => Partial<Draft>; create: (payload: Record<string, unknown>) => Promise<void>; busy: boolean; unavailable: boolean; setTab: (tab: string) => void; onClose: () => void };

const steps = ["Identity", "CPU and memory", "Disks", "Networks", "Boot and media", "Review"];
const blank: Draft = {
  host_id: "", name: "", description: "", guest_os: "linux",
  cpu: { count: 2, sockets: 0, cores: 0, threads: 0, model: "host-passthrough" },
  memory: { mode: "dedicated", size_mib: 2048 },
  disks: [{ storage_id: "", name: "system", size_gib: 20, bus: "virtio", thin: true, boot_order: 1 }],
  nics: [{ network_id: "", model: "virtio", mac: "", mtu: 0 }],
  boot: { firmware: "uefi", machine: "", media_boot_order: 0, secure_boot: "on", tpm: "none" },
  media_id: "", drivers_media_id: "", start_after_create: false, autostart: false,
};
export const guestNames: Record<string, string> = { linux: "Linux", windows: "Windows", other: "Other" };
export const busNames: Record<string, string> = { virtio: "VirtIO", sata: "SATA", scsi: "SCSI" };
export const adapterNames: Record<string, string> = {
  virtio: "VirtIO (paravirtualised)", e1000: "Intel E1000", e1000e: "Intel E1000E",
  rtl8139: "Realtek RTL8139", vmxnet3: "VMware vmxnet3",
};
// What each model is actually for, so the list is a decision and not five names.
export const adapterNotes: Record<string, string> = {
  virtio: "Fastest and lowest host CPU. The guest needs a VirtIO driver: built into Linux, and on Windows supplied by the guest tools.",
  e1000: "Emulates an Intel gigabit card almost every operating system already has a driver for. Slower and more host CPU than VirtIO; use it when a guest or installer cannot see a VirtIO adapter.",
  e1000e: "Emulates a newer Intel gigabit card. Same purpose as E1000, for guests that expect the later model.",
  rtl8139: "Emulates a 100 Mbit Realtek card for guests too old to have a gigabit driver. The 100 Mbit limit is the emulated hardware, not a setting.",
  vmxnet3: "VMware's paravirtualised adapter, for guests that already carry VMware drivers. Prefer VirtIO for anything else.",
};
export const cpuNames: Record<string, string> = { "host-passthrough": "Host passthrough", "host-model": "Host model", max: "Maximum host CPU" };
export const cpuName = (model: string) => cpuNames[model] || `QEMU model · ${model}`;
export const baselineSupported = (h?: Host) => Boolean(h?.capabilities?.includes("vm-cpu-baseline-v1"));
export function availableBaseline(h?: Host) {
  const baseline = h?.inventory?.cpu_baseline;
  return h && readyHost(h) && baselineSupported(h) && baseline?.model && baseline.machine && !baseline.error ? baseline : undefined;
}
export function baselineUnavailable(h?: Host): string {
  if (!baselineSupported(h)) return "";
  return availableBaseline(h) ? "" : `Pool CPU baseline unavailable. ${h?.inventory?.cpu_baseline?.error || "Wait for a fresh report from all pool members, or explicitly choose a CPU model."}`;
}
export const fixedCPU = (cpu: VMCPU) => cpu.features != null;
export function recommendedCPU(cpu: VMCPU, machine: string | undefined, host?: Host): boolean {
  const baseline = availableBaseline(host);
  const signature = (features: NonNullable<VMCPU["features"]>) => features.map(f => `${f.policy}:${f.name}`).sort().join(",");
  return Boolean(baseline && cpu.model === baseline.model && (machine || "q35") === baseline.machine && cpu.features
    && signature(cpu.features) === signature(baseline.features || []));
}
export function selectCPU(cpu: VMCPU, selection: string, h?: Host): VMCPU {
  if (selection === "__saved-baseline") return cpu;
  if (selection === "__pool-baseline") {
    const baseline = availableBaseline(h);
    return baseline ? { ...cpu, model: baseline.model, features: (baseline.features || []).map(f => ({ ...f })) } : { ...cpu, model: "", features: undefined };
  }
  return { ...cpu, model: selection, features: undefined };
}
export function cpuSelectionIssue(cpu: VMCPU, h?: Host): string {
  if (!cpu.model) return "Choose an available pool CPU baseline or explicitly select a CPU model.";
  if (cpu.features?.length && !baselineSupported(h)) return "This fixed CPU includes explicit features. Upgrade Nex Core before submitting this definition.";
  return "";
}
export const cpuMigrationHelp = "The pool CPU baseline covers the current members' CPU capabilities. Its model and features are saved with this VM and do not change when membership changes. Future hosts may be incompatible; migration also needs compatible runtime, storage and networking. Host CPU modes follow the host.";


// A host defines a VM's second CD drive, for a driver disc beside the install media, only when
// its Core reports this capability; an older Core would ignore the drive and silently create a
// VM whose driver ISO is nowhere.
export const driversDriveReady = (h?: Host) => Boolean(h && !h.revoked && h.connection === "fresh" && h.capabilities?.includes("vm-media-drivers-drive-v1"));

// defaultTPM is the emulated TPM a new VM gets for its guest OS. Windows 11 refuses to install
// without a TPM 2.0 (BUG-0196), so a Windows guest gets one; nothing else needs one, and a TPM
// changes measured boot and can bind BitLocker keys, so it is not added to every VM.
export const defaultTPM = (guestOS: string) => guestOS === "windows" ? "2.0" : "none";

// defaultDriversMedia picks the host's shipped virtio-win ISO when creating a Windows guest.
// Only fills an empty Driver disc; never overrides an operator choice or the install ISO.
export function defaultDriversMedia(guestOS: string, media: Media[], installMediaID = ""): string {
  if (guestOS !== "windows") return "";
  const match = media.find(m => m.id !== installMediaID && /^virtio-win/i.test(m.name));
  return match?.id || "";
}

// nextDriversMedia is the Driver disc after the guest OS or host changes. A host that cannot
// attach a driver disc gets none: its select is disabled, so a preselect could be neither seen
// as a choice nor cleared, and its Core would ignore it. Otherwise an empty disc, or the one the
// previous guest OS chose automatically, follows the new guest OS; an operator's choice stays.
export function nextDriversMedia(current: string, fromOS: string, toOS: string, media: Media[], installMediaID: string, ready: boolean): string {
  if (!ready) return "";
  return !current || current === defaultDriversMedia(fromOS, media, installMediaID) ? defaultDriversMedia(toOS, media, installMediaID) : current;
}

// Secure Boot and an emulated TPM are UEFI features. effectiveBoot is what a BIOS VM is actually
// created with, whatever the (disabled) controls last held, so the review page, the submitted
// definition and the VM Core defines all say the same thing. The protocol also refuses
// secure_boot "on" without UEFI.
export function effectiveBoot<T extends { firmware: string; secure_boot: string; tpm: string }>(boot: T): T {
  return boot.firmware === "uefi" ? boot : { ...boot, secure_boot: "", tpm: "none" };
}
// secureBootText mirrors the protocol's VMBoot.SecureBootEnabled: a UEFI definition with no
// recorded choice is running with Secure Boot on, because that is the firmware libvirt selected
// for every UEFI VM defined before the field existed (BUG-0199).
export const secureBootText = (boot: { firmware: string; secure_boot?: string }) =>
  boot.firmware !== "uefi" ? "Not available · BIOS firmware has no Secure Boot"
    : boot.secure_boot === "off" ? "Off · unsigned boot media and drivers are allowed"
    : "On · the firmware runs only signed boot software";
export const tpmText = (boot: { tpm?: string }) =>
  boot.tpm === "2.0" ? "TPM 2.0 · emulated device in this VM" : "None";
// Unicast, lower-case, colon-separated: the only manual form the protocol accepts.
const macPattern = "[0-9a-f][02468ace](:[0-9a-f]{2}){5}";
const staleText = "Some selections are no longer validated on this host. Reselect them before continuing.";
const num = (v: number) => Number.isNaN(v) ? "" : v;
const whole = (v: number) => Number.isNaN(v) ? 0 : v;

// Topology fields are optional as a group: all 0 means the host default (1 × count × 1).
// Filling the first field seeds the other two from that default so a blank sockets box
// with a "1" placeholder cannot be mistaken for a real value while cores/threads are set.
export function applyTopology(cpu: Draft["cpu"], field: "sockets" | "cores" | "threads", raw: number): Draft["cpu"] {
  const v = whole(raw);
  if (v < 1) return { ...cpu, sockets: 0, cores: 0, threads: 0 };
  let { sockets, cores, threads } = cpu;
  if (!sockets && !cores && !threads) {
    sockets = 1;
    cores = cpu.count || 1;
    threads = 1;
  }
  return { ...cpu, sockets, cores, threads, [field]: v };
}

export function topologyIssue(cpu: Draft["cpu"]): string {
  const { count, sockets, cores, threads } = cpu;
  if (!(sockets || cores || threads)) return "";
  if (sockets < 1 || cores < 1 || threads < 1) {
    return "Set sockets, cores and threads together, or clear any of them for the default.";
  }
  if (sockets * cores * threads !== count) {
    return `Sockets × cores × threads must equal ${count || "the"} vCPUs; ${sockets} × ${cores} × ${threads} = ${sockets * cores * threads}.`;
  }
  return "";
}

// bootKeys lists the boot devices in their current order; renumber writes that order back as the
// contiguous 1..N the protocol requires, leaving a removed device or absent media at 0.
// Newly chosen install media (media_boot_order still 0) sorts first so an ISO is the default
// first boot device; an empty tray is skipped by the firmware anyway.
export function bootKeys(d: Pick<Draft, "disks" | "boot" | "media_id">) {
  const at = (k: string) => k === "media"
    ? (d.boot.media_boot_order || 0)
    : (d.disks[Number(k.slice(5))]?.boot_order || 99);
  const keys = d.disks.map((_, i) => `disk-${i}`);
  if (d.media_id) keys.push("media");
  return keys.sort((a, b) => at(a) - at(b));
}
export function renumber(d: Draft, keys = bootKeys(d)): Draft {
  return { ...d, disks: d.disks.map((disk, i) => ({ ...disk, boot_order: keys.indexOf(`disk-${i}`) + 1 })), boot: { ...d.boot, media_boot_order: d.media_id ? keys.indexOf("media") + 1 : 0 } };
}

// A restored draft's nics may legitimately be [] (an operator removed every interface, which
// the protocol allows). Only fall back to the blank template when the field is missing or not
// an array, never because it is empty, or a deliberate zero-NIC draft grows one back on reopen.
export function restoredNICs(nics: unknown): NICRow[] {
  return Array.isArray(nics) ? (nics as NICRow[]) : blank.nics;
}

export function vmNameIssue(name: string): string {
  if (!name.trim()) return "Enter a VM name.";
  if (new TextEncoder().encode(name).length > 200) return "Use at most 200 UTF-8 bytes for the VM name.";
  if (/[\/\u0000-\u001f\u007f-\u009f\ufffe\uffff]/u.test(name)) return "VM names cannot contain slashes or control characters.";
  return "";
}

// A disk name is an operator label: the host writes disk-<n>.qcow2 inside the VM's own
// directory regardless. Default it to <pool>-<vm>, suffixed after the first disk because the
// protocol requires the names to be unique within a VM. Trimmed to fit the 63-character limit
// with room for that suffix, and reduced to the characters the protocol's pattern accepts.
export function autoDiskName(pool: string, vm: string, index: number) {
  const base = [pool, vm].filter(Boolean).join("-").toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-").replace(/^[^a-z0-9]+/, "").replace(/[-._]+$/, "").slice(0, 60) || "disk";
  return index ? `${base}-${index + 1}` : base;
}

// VMWizard collects one VM specification. It never submits by itself: the owning panel holds the
// durable request identity, so an uncertain outcome keeps the same request on retry.
export function VMWizard({ poolID, poolName, hosts, placement, storage, networks, media, restored, create, busy, unavailable, setTab, onClose }: Props) {
  const [step, setStep] = useState(0);
  // The TPM follows the guest OS until the operator sets it themselves; a restored draft that
  // already carries one keeps it.
  const [cpuChosen, setCPUChosen] = useState(() => Boolean(restored().cpu?.model));
  const [tpmChosen, setTPMChosen] = useState(() => Boolean(restored().boot?.tpm));
  const [draft, setDraft] = useState<Draft>(() => {
    const r = restored();
    const d = { ...blank, ...r, cpu: { ...blank.cpu, ...r.cpu }, memory: { ...blank.memory, ...r.memory }, boot: { ...blank.boot, ...r.boot } };
    const initialHost = hosts.find(h => h.id === d.host_id);
    if (!r.cpu?.model && baselineSupported(initialHost)) {
      d.cpu = selectCPU(d.cpu, "__pool-baseline", initialHost);
      const baseline = availableBaseline(initialHost);
      if (baseline) d.boot.machine = baseline.machine;
    }
    if (!r.boot?.tpm) d.boot = { ...d.boot, tpm: defaultTPM(d.guest_os) };
    // blank.disks is only a shape template (name "system"); a fresh wizard must start from
    // autoDiskName so renameVM can carry the label with the VM name. Restored drafts keep
    // their own disks, including an operator override.
    const started = blank.disks.map((disk, i) => ({ ...disk, name: autoDiskName(poolName, d.name, i) }));
    const disks = Array.isArray(r.disks) && r.disks.length ? r.disks : started;
    const nics = restoredNICs(r.nics);
    return renumber({ ...d, disks, nics });
  });
  const heading = useRef<HTMLHeadingElement>(null);
  const go = (to: number) => { setStep(to); setTimeout(() => heading.current?.focus(), 0); };
  const edit = (patch: Partial<Draft>) => setDraft(d => renumber({ ...d, ...patch }));
  const setDisk = (i: number, patch: Partial<DiskRow>) => edit({ disks: draft.disks.map((d, n) => n === i ? { ...d, ...patch } : d) });
  const setNIC = (i: number, patch: Partial<NICRow>) => edit({ nics: draft.nics.map((n, x) => x === i ? { ...n, ...patch } : n) });
  const selectedHost = hosts.find(h => h.id === draft.host_id);
  const hostName = (id: string) => hosts.find(h => h.id === id)?.hostname || id;
  const hostStorage = storage.filter(s => storageUsable(s, draft.host_id));
  const readyNetworks = (id: string) => networks.filter(n => networkUsable(n, id)).sort((a, b) => (a.vlan || 0) - (b.vlan || 0) || a.name.localeCompare(b.name));
  const hostNetworks = readyNetworks(draft.host_id);
  const hostMedia = media.filter(m => mediaUsable(m, draft.host_id, storage));
  const selectedMedia = media.find(m => m.id === draft.media_id);
  const selectedDrivers = media.find(m => m.id === draft.drivers_media_id);
  const driversReady = driversDriveReady(selectedHost);
  const baseline = availableBaseline(selectedHost);
  // Only one option is the recommendation: a saved fixed CPU that already matches the pool
  // baseline carries it, so the pool baseline option does not repeat it.
  const savedRecommended = fixedCPU(draft.cpu) && recommendedCPU(draft.cpu, draft.boot.machine, selectedHost);
  const baselineWarning = baselineUnavailable(selectedHost);
  const cpuIssue = cpuSelectionIssue(draft.cpu, selectedHost);
  const uefi = draft.boot.firmware === "uefi";
  const boot = effectiveBoot(draft.boot);
  const maxCPUs = Math.min(selectedHost?.inventory?.cpu_count || 64, 64);
  const maxMiB = Math.min(selectedHost?.inventory ? Math.floor(selectedHost.inventory.memory_bytes / 1024 ** 2) : 262144, 262144);
  const order = bootKeys(draft);
  const bootLabel = (k: string) => k === "media" ? `Install media · ${selectedMedia?.name || "ISO"}` : `Disk ${Number(k.slice(5)) + 1} · ${draft.disks[Number(k.slice(5))]?.name || "unnamed"}`;

  const { count, sockets, cores, threads } = draft.cpu;
  const topoIssue = topologyIssue(draft.cpu);
  const names = draft.disks.map(d => d.name);
  const diskIssue = names.some((n, i) => names.indexOf(n) !== i) ? "Disk names must be unique within this VM." : "";
  const macs = draft.nics.map(n => n.mac).filter(Boolean);
  const macIssue = macs.some(m => !new RegExp(`^${macPattern}$`).test(m)) ? "A manual MAC must be six lower-case hex octets separated by colons and unicast (an even second character), such as 52:54:00:12:34:56. Leave it blank for an automatic address."
    : macs.includes("00:00:00:00:00:00") ? "00:00:00:00:00:00 cannot be used as a MAC address."
    : macs.some((m, i) => macs.indexOf(m) !== i) ? "MAC addresses must be unique within this VM." : "";
  const staleDisks = draft.disks.some(d => d.storage_id && !hostStorage.some(s => s.id === d.storage_id));
  const staleNICs = draft.nics.some(n => n.network_id && !hostNetworks.some(x => x.id === n.network_id));
  const staleMedia = Boolean(draft.media_id) && !hostMedia.some(m => m.id === draft.media_id);
  const staleDrivers = Boolean(draft.drivers_media_id) && !hostMedia.some(m => m.id === draft.drivers_media_id);
  const mediaIssue = staleMedia ? "That ISO is no longer available on this host. Choose another or continue without install media."
    : staleDrivers ? "That driver disc is no longer available on this host. Choose another or continue without one." : "";
  const stepIssue = ["", cpuIssue || topoIssue, diskIssue || (staleDisks ? staleText : ""), macIssue || (staleNICs ? staleText : ""), mediaIssue, ""][step];
  const hostReady = Boolean(selectedHost && !placementBlocker(selectedHost, placement?.[selectedHost.id]));
  const blocked = Boolean(vmNameIssue(draft.name)) || !hostReady || staleDisks || staleNICs || staleMedia || staleDrivers || Boolean(cpuIssue || topoIssue || diskIssue || macIssue);
  const setTopo = (field: "sockets" | "cores" | "threads", raw: number) =>
    setDraft(d => renumber({ ...d, cpu: applyTopology(d.cpu, field, raw) }));

  function chooseHost(id: string) {
    const s = storage.filter(x => storageUsable(x, id));
    const n = readyNetworks(id);
    const mediaForHost = media.filter(m => mediaUsable(m, id, storage));
    const host = hosts.find(h => h.id === id);
    const hostBaseline = availableBaseline(host);
    if (!cpuChosen && hostBaseline) setCPUChosen(true);
    edit({
      ...(!cpuChosen && baselineSupported(host) ? { cpu: selectCPU(draft.cpu, "__pool-baseline", host), ...(hostBaseline ? { boot: { ...draft.boot, machine: hostBaseline.machine } } : {}) } : {}),
      host_id: id,
      media_id: "",
      drivers_media_id: nextDriversMedia("", draft.guest_os, draft.guest_os, mediaForHost, "", driversDriveReady(hosts.find(h => h.id === id))),
      disks: draft.disks.map(d => ({ ...d, storage_id: s.length === 1 ? s[0].id : "" })),
      nics: draft.nics.map(x => ({ ...x, network_id: n.length === 1 ? n[0].id : "" })),
    });
  }
  // Renaming the VM carries the derived disk names with it, but only while a disk still holds
  // the name we generated: once the operator types their own label it is left alone.
  function renameVM(name: string) {
    edit({ name, disks: draft.disks.map((d, i) => d.name === autoDiskName(poolName, draft.name, i) ? { ...d, name: autoDiskName(poolName, name, i) } : d) });
  }
  function addDisk() {
    let n = draft.disks.length;
    while (draft.disks.some(d => d.name === autoDiskName(poolName, draft.name, n))) n++;
    edit({ disks: [...draft.disks, { storage_id: hostStorage.length === 1 ? hostStorage[0].id : "", name: autoDiskName(poolName, draft.name, n), size_gib: 20, bus: "virtio", thin: true, boot_order: 0 }] });
  }
  function move(index: number, delta: number) {
    const keys = bootKeys(draft), to = index + delta;
    if (to < 0 || to >= keys.length) return;
    [keys[index], keys[to]] = [keys[to], keys[index]];
    setDraft(d => renumber(d, keys));
  }

  return <section className="panel form-panel vm-wizard" aria-label="Create VM">
    <div className="section-heading"><h2 ref={heading} tabIndex={-1}>Create VM · {step + 1} of 6: {steps[step]}</h2><button disabled={busy} onClick={onClose}>Close</button></div>
    <form onSubmit={e => { e.preventDefault(); if (stepIssue || busy || unavailable || (step === 5 && blocked)) return; if (step < 5) go(step + 1); else void create({ ...draft, drivers_media_id: driversReady ? draft.drivers_media_id : "", boot, pool_id: poolID }); }}>
      {step === 0 && <>
        <label>VM name<input required maxLength={200} ref={el => { el?.setCustomValidity(vmNameIssue(draft.name)); }} value={draft.name} autoFocus onChange={e => renameVM(e.target.value)} /></label>
        <label>Description<textarea rows={2} maxLength={4096} value={draft.description} onChange={e => edit({ description: e.target.value })} /><small>Optional. At most 4096 characters; stored with the VM definition on the pool master.</small></label>
        <label>Guest operating system<select value={draft.guest_os} onChange={e => {
          const guest_os = e.target.value;
          edit({
            guest_os,
            drivers_media_id: nextDriversMedia(draft.drivers_media_id, draft.guest_os, guest_os, hostMedia, draft.media_id, driversReady),
            ...(tpmChosen ? {} : { boot: { ...draft.boot, tpm: defaultTPM(guest_os) } }),
          });
        }}><option value="linux">Linux</option><option value="windows">Windows</option><option value="other">Other</option></select><small>Chooses the firmware defaults on the Boot and media step: a Windows guest starts with an emulated TPM 2.0, which Windows 11 requires. Nex does not install or configure the guest.</small></label>
        <label>Placement host<select required value={draft.host_id} onChange={e => chooseHost(e.target.value)}><option value="">Select host</option>{hosts.map(h => { const blocker = placementBlocker(h, placement?.[h.id]); return <option key={h.id} value={h.id} disabled={Boolean(blocker)}>{h.hostname} · {blocker || h.connection}</option>; })}</select></label>
        {hosts.map(h => { const blocker = placementBlocker(h, placement?.[h.id]); return blocker ? <small key={h.id} className="task-error">{h.hostname}: {blocker}</small> : null; })}
        <p className="help">The VM belongs to this pool and is assigned to the selected host. Local storage, networks and ISO images must be validated for that host.</p>
        {!hosts.some(readyHost) && <p className="warning">A freshly connected host with managed-VM support is required. Enroll, reconnect or upgrade the host agent first.</p>}
      </>}
      {step === 1 && <>
        <div className="vm-fields">
          <label>Virtual CPUs<input type="number" required min={1} max={maxCPUs} step={1} value={num(count)} autoFocus onChange={e => edit({ cpu: { ...draft.cpu, count: e.target.valueAsNumber } })} /></label>
          <label>Sockets<input type="number" min={1} max={maxCPUs} step={1} aria-describedby={topoIssue ? "vm-topology-issue" : undefined} value={sockets || ""} onChange={e => setTopo("sockets", e.target.valueAsNumber)} /></label>
          <label>Cores per socket<input type="number" min={1} max={maxCPUs} step={1} aria-describedby={topoIssue ? "vm-topology-issue" : undefined} value={cores || ""} onChange={e => setTopo("cores", e.target.valueAsNumber)} /></label>
          <label>Threads per core<input type="number" min={1} max={maxCPUs} step={1} aria-describedby={topoIssue ? "vm-topology-issue" : undefined} value={threads || ""} onChange={e => setTopo("threads", e.target.valueAsNumber)} /></label>
        </div>
        <p className="help">Leave sockets, cores and threads blank for the default: one socket with {count || "the chosen number of"} cores and one thread per core.</p>
        {topoIssue && <p className="task-error" id="vm-topology-issue">{topoIssue}</p>}
        <label>CPU model<select required value={fixedCPU(draft.cpu) ? "__saved-baseline" : draft.cpu.model} onChange={e => {
          const selection = e.target.value;
          setCPUChosen(true);
          edit({ cpu: selectCPU(draft.cpu, selection, selectedHost), ...(selection === "__pool-baseline" && baseline ? { boot: { ...draft.boot, machine: baseline.machine } } : {}) });
        }}>
          {!draft.cpu.model && <option value="" disabled>Select a CPU baseline or model</option>}
          {fixedCPU(draft.cpu) && <option value="__saved-baseline">Saved fixed CPU baseline · {draft.cpu.model}{savedRecommended ? " (Recommended)" : ""}</option>}
          {baselineSupported(selectedHost) && <option value="__pool-baseline" disabled={!baseline}>Pool CPU baseline{baseline ? ` · ${baseline.model}${savedRecommended ? "" : " (Recommended)"}` : " · unavailable"}</option>}
          <option value="host-passthrough">Host passthrough</option><option value="host-model">Host model</option>
          {selectedHost?.inventory?.cpu_models?.filter(m => m !== "host-passthrough" && m !== "host-model").map(m => <option key={m} value={m}>{cpuName(m)}</option>)}
          {!fixedCPU(draft.cpu) && draft.cpu.model && !["host-passthrough", "host-model", ...selectedHost?.inventory?.cpu_models || []].includes(draft.cpu.model) && <option value={draft.cpu.model} disabled>{cpuName(draft.cpu.model)} · saved model not reported by this host</option>}
        </select></label>
        {baselineWarning && <p className="warning">{baselineWarning} Existing CPU selections are kept.</p>}
        <p className="help">{cpuMigrationHelp}</p>
        <fieldset>
          <legend>Memory mode</legend>
          <label className="console-option"><input type="radio" name="vm-memory-mode" value="dedicated" checked={draft.memory.mode === "dedicated"} onChange={() => edit({ memory: { ...draft.memory, mode: "dedicated" } })} /> Dedicated</label>
          <p className="help">Requires updated Nex Core. Dedicated mode allocates and locks the full amount in host RAM while running, preventing swap and sharing. Stopping releases RAM but keeps its Nex capacity commitment.</p>
          <label className="console-option"><input type="radio" name="vm-memory-mode" value="maximum" checked={draft.memory.mode === "maximum"} onChange={() => edit({ memory: { ...draft.memory, mode: "maximum" } })} /> Maximum</label>
          <p className="help">The VM may use up to this much but reserves nothing. It returns memory it frees, which requires guest tools running in the guest.</p>
        </fieldset>
        <label>Memory (MiB)<input type="number" required min={512} max={maxMiB} step={1} value={num(draft.memory.size_mib)} onChange={e => edit({ memory: { ...draft.memory, size_mib: e.target.valueAsNumber } })} /></label>
        <p className="help">Host-reported capacity: {selectedHost?.inventory ? `${selectedHost.inventory.cpu_count} CPUs · ${Math.floor(selectedHost.inventory.memory_bytes / 1024 ** 2)} MiB total memory` : "unknown"}. Nex checks current availability before creation and uses this size as the ceiling in both modes.</p>
        {draft.memory.mode === "maximum" && draft.guest_os === "windows" && <p className="help">Windows guests only return freed memory once the virtio-win guest tools are installed. Until then freed pages stay allocated; maximum mode does not lock host RAM.</p>}
      </>}
      {step === 2 && <>
        {draft.disks.map((d, i) => <fieldset key={i}>
          <legend>Disk {i + 1}</legend>
          <div className="vm-fields">
            <label>Storage<select required value={d.storage_id} onChange={e => setDisk(i, { storage_id: e.target.value })}><option value="">Select VM-ready storage</option>{hostStorage.map(s => <option key={s.id} value={s.id}>{s.name} · {s.kind || "local"}</option>)}</select></label>
            <label>Disk name<input required maxLength={63} pattern={"[A-Za-z0-9][A-Za-z0-9._\\-]{0,62}"} title="Use 1–63 letters, digits, dots, dashes or underscores; start with a letter or digit." aria-describedby={diskIssue ? "vm-disk-issue" : undefined} value={d.name} onChange={e => setDisk(i, { name: e.target.value })} /></label>
            <label>Size (GiB)<input type="number" required min={1} max={2048} step={1} value={num(d.size_gib)} onChange={e => setDisk(i, { size_gib: e.target.valueAsNumber })} /></label>
            <label>Bus<select value={d.bus} onChange={e => setDisk(i, { bus: e.target.value })}>{Object.entries(busNames).map(([v, t]) => <option key={v} value={v}>{t}</option>)}</select></label>
            <label>Provisioning<select value={d.thin ? "thin" : "thick"} onChange={e => setDisk(i, { thin: e.target.value === "thin" })}><option value="thin">Thin · grows as written</option><option value="thick">Thick · allocated now</option></select></label>
          </div>
          <div className="actions"><button type="button" disabled={busy || draft.disks.length < 2} onClick={() => edit({ disks: draft.disks.filter((_, n) => n !== i) })}>Remove disk {i + 1}</button></div>
        </fieldset>)}
        {diskIssue && <p className="task-error" id="vm-disk-issue">{diskIssue}</p>}
        {staleDisks && <p className="warning">{staleText}</p>}
        <div className="actions"><button type="button" disabled={busy || draft.disks.length >= 8} onClick={addDisk}>Add disk</button></div>
        <p className="help">A VM needs 1 to 8 disks. Each disk is created inside this VM's own directory on the selected storage; existing files are never reused.</p>
        {!hostStorage.length && <p className="help">No VM-ready storage on this host. <button type="button" onClick={() => setTab("Storage")}>Open Storage</button></p>}
      </>}
      {step === 3 && <>
        {draft.nics.map((n, i) => <fieldset key={i}>
          <legend>Network interface {i + 1}</legend>
          <div className="vm-fields">
            <label>Network<select required value={n.network_id} onChange={e => setNIC(i, { network_id: e.target.value })}><option value="">Select validated network</option>{hostNetworks.map(x => <option key={x.id} value={x.id}>{x.name} · {describeNetwork(x)}</option>)}</select><small>Verified on the placement host. Registration does not make a host network reachable from another host.</small></label>
            <label>Adapter model<select value={n.model} onChange={e => setNIC(i, { model: e.target.value, ...(e.target.value === "virtio" ? {} : { mtu: 0 }) })}>{Object.entries(adapterNames).map(([v, t]) => <option key={v} value={v}>{t}</option>)}</select><small>{adapterNotes[n.model]}</small></label>
            <label>MAC address<input placeholder="Automatic" pattern={macPattern} title="Leave blank for an automatic address, or use six lower-case hex octets separated by colons with an even second character, such as 52:54:00:12:34:56." aria-describedby={macIssue ? "vm-nic-issue" : undefined} value={n.mac} onChange={e => setNIC(i, { mac: e.target.value.trim().toLowerCase().replace(/-/g, ":") })} /><small>Blank derives a stable address from this VM's identity.</small></label>
            {n.model === "virtio"
              ? <label>MTU<input type="number" min={1280} max={9216} step={1} placeholder="Network default" value={n.mtu || ""} onChange={e => setNIC(i, { mtu: whole(e.target.valueAsNumber) })} /><small>Blank uses the network's own MTU. 9000 is the usual jumbo-frame value and every device along the path must agree, or traffic is dropped without an error.</small></label>
              : <p className="help">An MTU can only be set on a VirtIO adapter, which is the one model that tells the guest. On {adapterNames[n.model]} the guest keeps its own MTU; set it inside the guest instead.</p>}
          </div>
          <div className="actions"><button type="button" disabled={busy} onClick={() => edit({ nics: draft.nics.filter((_, x) => x !== i) })}>Remove network interface {i + 1}</button></div>
        </fieldset>)}
        {!draft.nics.length && <p className="help">This VM has no network interfaces. It will start with no guest networking.</p>}
        {macIssue && <p className="task-error" id="vm-nic-issue">{macIssue}</p>}
        {staleNICs && <p className="warning">{staleText}</p>}
        <div className="actions"><button type="button" disabled={busy || draft.nics.length >= 8} onClick={() => edit({ nics: [...draft.nics, { network_id: hostNetworks.length === 1 ? hostNetworks[0].id : "", model: "virtio", mac: "", mtu: 0 }] })}>Add network interface</button></div>
        <p className="help">A VM may have up to 8 interfaces.</p>
        {!hostNetworks.length && <p className="help">No verified network on this host. <button type="button" onClick={() => setTab("Networking")}>Open Networking</button></p>}
      </>}
      {step === 4 && <>
        <div className="vm-fields">
          <label>Firmware<select value={draft.boot.firmware} onChange={e => edit({ boot: { ...draft.boot, firmware: e.target.value } })}><option value="uefi">UEFI</option><option value="bios">BIOS</option></select></label>
          <label>Secure Boot<select value={uefi ? (boot.secure_boot === "off" ? "off" : "on") : "unavailable"} disabled={!uefi} title={uefi ? undefined : "Secure Boot needs UEFI firmware."} onChange={e => edit({ boot: { ...draft.boot, secure_boot: e.target.value } })}>
            {!uefi && <option value="unavailable">Not available with BIOS</option>}
            <option value="on">On · only signed boot software runs</option>
            <option value="off">Off · unsigned boot media and drivers allowed</option>
          </select></label>
          <label>TPM<select value={uefi ? (boot.tpm === "2.0" ? "2.0" : "none") : "unavailable"} disabled={!uefi} title={uefi ? undefined : "An emulated TPM is offered with UEFI firmware."} onChange={e => { setTPMChosen(true); edit({ boot: { ...draft.boot, tpm: e.target.value } }); }}>
            {!uefi && <option value="unavailable">Not available with BIOS</option>}
            <option value="2.0">TPM 2.0 · emulated device</option>
            <option value="none">None</option>
          </select></label>
        </div>
        <p className="help">Windows 11 refuses to install without Secure Boot and a TPM 2.0, so a Windows guest starts with both. Turn Secure Boot off to boot unsigned media or load unsigned drivers. The emulated TPM's state belongs to this VM and is removed with it; a guest that stores BitLocker keys in it cannot unlock without it.</p>
        <label>Install ISO<select value={draft.media_id} aria-describedby={mediaIssue ? "vm-media-issue" : undefined} onChange={e => edit({ media_id: e.target.value, ...(e.target.value && e.target.value === draft.drivers_media_id ? { drivers_media_id: "" } : {}) })}>
          <option value="">No install media</option>
          {hostMedia.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
          {staleMedia && <option value={draft.media_id} disabled>{selectedMedia?.name || draft.media_id} · not available</option>}
        </select></label>
        <label>Driver disc<select value={draft.drivers_media_id} disabled={!driversReady} title={driversReady ? undefined : "This host's Nex Core does not provide a second CD drive yet."} onChange={e => edit({ drivers_media_id: e.target.value })}>
          <option value="">No driver disc</option>
          {hostMedia.filter(m => m.id !== draft.media_id).map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
          {staleDrivers && <option value={draft.drivers_media_id} disabled>{selectedDrivers?.name || draft.drivers_media_id} · not available</option>}
        </select></label>
        <p className="help">Optional second CD drive beside the install ISO. Windows on VirtIO needs virtio-win here (shipped hosts pre-select it).{driversReady ? "" : " This host's Nex Core does not provide a second CD drive yet; upgrade it to attach a driver disc."}</p>
        {mediaIssue && <p className="task-error" id="vm-media-issue">{mediaIssue}</p>}
        {!hostMedia.length && <p className="help">No transferred ISO is available on this host. <button type="button" onClick={() => setTab("ISO library")}>Open ISO library</button></p>}
        <fieldset>
          <legend>Boot order</legend>
          <ol className="boot-order">{order.map((k, i) => <li key={k}><span>{bootLabel(k)}</span><button type="button" disabled={busy || i === 0} aria-label={`Move ${bootLabel(k)} up in the boot order`} onClick={() => move(i, -1)}>Up</button><button type="button" disabled={busy || i === order.length - 1} aria-label={`Move ${bootLabel(k)} down in the boot order`} onClick={() => move(i, 1)}>Down</button></li>)}</ol>
          <p className="help">The host tries these in order. Install media is first by default; move the disk up after the guest is installed if you want.</p>
        </fieldset>
      </>}
      {step === 5 && <>
        <dl className="details">
          <dt>VM name</dt><dd>{draft.name}</dd>
          <dt>Description</dt><dd>{draft.description || "None"}</dd>
          <dt>Guest OS</dt><dd>{guestNames[draft.guest_os] || "Unset"}</dd>
          <dt>Assigned host</dt><dd>{hostName(draft.host_id)}</dd>
          <dt>CPU</dt><dd>{count} vCPUs · {(sockets || cores || threads) !== 0 ? `${sockets} × ${cores} × ${threads}` : `1 socket × ${count} cores × 1 thread`} · {cpuNames[draft.cpu.model] || draft.cpu.model}</dd>
          <dt>Memory</dt><dd>{draft.memory.size_mib} MiB · {draft.memory.mode === "dedicated" ? "Dedicated policy · locks host RAM while running; requires updated Nex Core" : "Maximum · reserves nothing, returns freed memory when guest tools run"}</dd>
          <dt>Disks</dt><dd><ul className="plain">{draft.disks.map((d, i) => <li key={i}>{d.name} · {storage.find(s => s.id === d.storage_id)?.name || "Unavailable"} · {d.size_gib} GiB · {busNames[d.bus] || d.bus} · {d.thin ? "Thin" : "Thick"}</li>)}</ul></dd>
          <dt>Network interfaces</dt><dd>{draft.nics.length ? <ul className="plain">{draft.nics.map((n, i) => <li key={i}>{(x => x ? `${x.name} · ${describeNetwork(x)}` : "Unavailable")(networks.find(x => x.id === n.network_id))} · {adapterNames[n.model] || n.model} · {n.mac ? <span className="mono">{n.mac}</span> : "Automatic MAC"}{n.mtu ? ` · MTU ${n.mtu}` : ""}</li>)}</ul> : "None"}</dd>
          <dt>Firmware</dt><dd>{draft.boot.firmware.toUpperCase()}</dd>
          <dt>Secure Boot</dt><dd>{secureBootText(boot)}</dd>
          <dt>TPM</dt><dd>{tpmText(boot)}</dd>
          <dt>Install ISO</dt><dd>{draft.media_id ? selectedMedia?.name || "Unavailable" : "None"}</dd>
          <dt>Driver disc</dt><dd>{driversReady && draft.drivers_media_id ? selectedDrivers?.name || "Unavailable" : "None · second CD drive empty"}</dd>
          <dt>Boot order</dt><dd><ol>{order.map(k => <li key={k}>{bootLabel(k)}</li>)}</ol></dd>
        </dl>
        <fieldset>
          <legend>Lifecycle</legend>
          <label className="console-option"><input type="checkbox" checked={draft.start_after_create} onChange={e => edit({ start_after_create: e.target.checked })} /> Start after creation</label>
          <p className="help">The host starts the VM once, as the last step of the creation task. If that start fails, creation still succeeds and a separate failed power task records why the VM stayed stopped.</p>
          <label className="console-option"><input type="checkbox" checked={draft.autostart} onChange={e => edit({ autostart: e.target.checked })} /> Start automatically when the host boots</label>
        </fieldset>
        <p>Follow the durable task to see validation and creation results. A creation task that does not report a result is uncertain, not failed; check Task history before retrying.</p>
        {(staleDisks || staleNICs || staleMedia) && <p className="warning">{staleText}</p>}
      </>}
      {cpuIssue && <p className="warning" role="alert">{cpuIssue}</p>}
      {selectedHost && !readyHost(selectedHost) && <p className="warning">The selected host is unavailable or lacks managed-VM support. Reconnect or upgrade the agent before creating the VM.</p>}
      <div className="actions">{step > 0 && <button type="button" disabled={busy} onClick={() => go(step - 1)}>Back</button>}<button className="primary" disabled={busy || unavailable || Boolean(stepIssue) || (step === 5 && blocked)}>{busy ? "Submitting…" : step < 5 ? "Next" : draft.start_after_create ? "Create and start VM" : "Create stopped VM"}</button></div>
    </form>
  </section>;
}
