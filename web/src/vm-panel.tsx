// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import type { API, Data, Host } from "./types";
import { GuestTools, InstallMedia, VMDetails, VMPower, VMUsage, staleNote, stateText, type VM } from "./managed";
import type { ConsoleMedia } from "./console";
import { Stats, formatValue } from "./stats";
import { TagEditor, tagStyle } from "./vm-edit";
import { VMSnapshots } from "./vm-snapshots";
import { VMLastBackup } from "./vm-last-backup";
import { GuestOS } from "./guest-os";
import { Activity, Badge, Panel } from "./ui";
import { Icon } from "./icons";
import "./vms.css";

// consolePageBlocker explains why the console page cannot be opened now; an empty result means
// it can. The page offers Start VM, so a stopped VM opens it to watch its boot from the start.
export function consolePageBlocker(vm: VM, host?: Host, unavailable = false): string {
  if (unavailable) return "Controller inventory could not be loaded, so the VM's current state is unknown. Retry loading it first.";
  if (!host || host.revoked || host.connection !== "fresh") return "The VM's host is not freshly connected. Its console cannot be opened until the host reports again.";
  if (!host.capabilities?.includes("console-v1")) return "The host agent does not support browser consoles. Upgrade Nex Core on this host.";
  if (vm.freshness !== "fresh") return "Current VM state is unknown; wait for a fresh host report before opening the console.";
  return "";
}
// consoleBlocker explains why the console cannot connect now; an empty result means it can.
export function consoleBlocker(vm: VM, host?: Host, unavailable = false): string {
  return consolePageBlocker(vm, host, unavailable) || (vm.state === "running" ? "" : `The VM is observed ${stateText(vm.state)}. Use Start, wait for the next host report, then connect.`);
}
// A VM on a host that is not fresh, or while the inventory cannot load, has an unknown state, never a stopped one.
export const stateBadge = (vm: VM, unavailable = false) => unavailable || vm.freshness !== "fresh" ? <Badge value="stale" label="Unknown" /> : <Badge value={vm.state} />;
export const TagList = ({ tags }: { tags?: string[] | null }) => tags?.length ? <span className="tags">{tags.map(t => <span key={t} className="tag" style={tagStyle(t)}>{t}</span>)}</span> : <span className="muted">—</span>;

const tabs = ["Overview", "Performance", "Snapshots", "Configuration", "Activity"] as const;
const openConsole = (vm: VM) => { location.hash = `/console/${encodeURIComponent(vm.id)}`; };

type VMPanelProps = {
  vm: VM; data: Data; api: API; reload: () => Promise<void>; busy: boolean; unavailable: boolean; onNotice: (text: string, operationID?: string) => void;
  media: ConsoleMedia; eject: { busy: boolean; note: string; onEject: () => void }; onClose: () => void;
};

// VMPanel is the selected VM's slide-over: power and console actions above, then its state,
// usage, definition and history. Features whose Nex Core phase has not landed show their place
// and say what is missing. Callers key this by vm.id so tab state resets per VM.
export function VMPanel({ vm, data, api, reload, busy, unavailable, onNotice, media, eject, onClose }: VMPanelProps) {
  const [tab, setTab] = useState<string>("Overview");
  const [editing, setEditing] = useState(false);
  const host = data.hosts.find(h => h.id === vm.host_id);
  const pool = data.pools.find(p => p.id === vm.pool_id);
  const master = data.hosts.find(h => h.id === pool?.master_host_id);
  const spec = vm.spec;
  const blocker = consolePageBlocker(vm, host, unavailable);
  const addresses = vm.guest?.addresses || [];
  return <Panel icon="vm" title={vm.name} badge={stateBadge(vm, unavailable)} onClose={onClose} tabs={tabs} tab={tab} onTab={t => { setTab(t); setEditing(false); }} wide={tab === "Performance" || editing}
    sub={<>on <a href={`#/hosts/${encodeURIComponent(vm.host_id)}`}>{host?.hostname || vm.host_id}</a> · <a href={`#/pools/${encodeURIComponent(vm.pool_id)}`}>{pool?.name || vm.pool_id}</a></>}
    actions={<>
      <button type="button" className="sm primary" disabled={Boolean(blocker)} title={blocker || "Open the console full screen"} onClick={() => openConsole(vm)}><Icon kind="console" />Console</button>
      <VMPower vm={vm} host={host} api={api} reload={reload} busy={busy} unavailable={unavailable} onNotice={onNotice}>
        <button type="button" className="sm" disabled title="Available with live migration (Phase 7)"><Icon kind="migrate" />Migrate</button>
      </VMPower>
    </>}>
    {tab === "Overview" && <>
      {vm.freshness !== "fresh" && <p className="why" role="status">{host?.hostname || "The host"} is not reporting. {staleNote(vm, host)}</p>}
      <div className="vm-console-preview">
        <span>{blocker || (vm.state === "running" ? `${vm.name} is running. Open the console to see its display and type into it.` : `${vm.name} is ${stateText(vm.state)}. Open the console and use Start VM there to watch it boot.`)}</span>
        <button type="button" className="sm" disabled={Boolean(blocker)} onClick={() => openConsole(vm)}>Open console</button>
      </div>
      <section className="tile" aria-label="Properties">
        <dl className="details">
          <dt>Guest OS</dt><dd><GuestOS vm={vm} /></dd>
          <dt>IP address</dt><dd>{addresses.length ? <ul className="plain">{addresses.map(a => <li key={a.interface + a.address} className="mono">{a.address}</li>)}</ul> : "Not reported"}</dd>
          {spec && <>
            <dt>vCPU / Memory</dt><dd>{spec.cpu.count} / {formatValue("bytes", spec.memory.size_mib * 1024 ** 2)}</dd>
            <dt>Disks</dt><dd>{spec.disks?.length ? <ul className="plain">{spec.disks.map(d => <li key={d.name}>{d.name} · {d.size_gib} GiB · {data.storage.find(s => s.id === d.storage_id)?.name || d.storage_id}</li>)}</ul> : "None recorded"}</dd>
            <dt>Networks</dt><dd>{spec.nics?.length ? <ul className="plain">{spec.nics.map((n, i) => { const net = data.networks.find(x => x.id === n.network_id); return <li key={i}>{net?.name || n.network_id}{net?.vlan ? ` (VLAN ${net.vlan})` : ""}</li>; })}</ul> : "None"}</dd>
          </>}
          <dt>Guest agent</dt><dd>{vm.guest ? <Badge value={vm.guest.agent || "unknown"} /> : "Not reported"}</dd>
          <dt>Last backup</dt><VMLastBackup vmID={vm.id} poolID={vm.pool_id} api={api} />
          {spec && <><dt>Firmware</dt><dd>{spec.boot.firmware === "uefi" ? `UEFI · Secure Boot ${spec.boot.secure_boot === "off" ? "off" : "on"}` : spec.boot.firmware?.toUpperCase()}</dd></>}
          <dt>Tags</dt><dd><TagList tags={spec?.tags} /></dd>
        </dl>
      </section>
      <InstallMedia vm={vm} host={host} drives={media} eject={eject} busy={busy} unavailable={unavailable} />
      <GuestTools vm={vm} />
    </>}
    {tab === "Performance" && <><VMUsage vm={vm} /><Stats key={vm.id} kind="vm" id={vm.id} api={api} /></>}
    {tab === "Snapshots" && <VMSnapshots vm={vm} host={host} api={api} reload={reload} busy={busy} unavailable={unavailable} onNotice={onNotice} />}
    {tab === "Configuration" && <>
      <VMDetails vm={vm} hosts={data.hosts} storage={data.storage} networks={data.networks} media={data.media} api={api} reload={reload} busy={busy} unavailable={unavailable} onNotice={onNotice} editing={editing} onEditing={setEditing} />
      {!editing && <>
        <section className="tile" aria-label="Tags">
          <h2>Tags</h2>
          <TagEditor vm={vm} host={host} master={master} api={api} reload={reload} unavailable={unavailable} onNotice={onNotice} known={[...new Set(data.vms.filter(v => v.pool_id === vm.pool_id).flatMap(v => v.spec?.tags ?? []))].sort()} />
        </section>
        <section className="tile" aria-label="High availability">
          <h2>High availability</h2>
          <div className="vm-fields">
            <label>Restart priority<select disabled><option>Not set</option></select></label>
            <label>Placement preference<select disabled><option>Any host</option></select></label>
          </div>
          <p className="note">Available with high availability (Phase 9).</p>
        </section>
      </>}
    </>}
    {tab === "Activity" && <Activity entries={data.audit.filter(a => a.resource_id === vm.id)} data={data} />}
  </Panel>;
}
