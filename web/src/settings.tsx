// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import type { API, Host, Pool } from "./types";
import { PoolMembers } from "./pool-members";
import { Icon, type IconKind } from "./icons";
import { Section } from "./ui";
import { Gated, gatedButtons } from "./gated";
import { ImageUpdates } from "./image-updates";
import { PoolRollout } from "./pool-rollout";
import { BackupRepositories } from "./backup-repositories";
import "./settings.css";

// Pool-level configuration, stacked by section. Storage, Networks and the ISO library are
// operational screens of their own (#/storage, #/networks), not settings.
export const sections: { key: string; label: string; icon: IconKind; pool: boolean }[] = [
  { key: "overview", label: "Overview", icon: "overview", pool: true },
  { key: "members", label: "Members", icon: "host", pool: true },
  { key: "ha", label: "High availability", icon: "overview", pool: true },
  { key: "backups", label: "Backup repositories", icon: "backup", pool: true },
  { key: "updates", label: "Updates", icon: "refresh", pool: false },
];
// Storage, Networks and the ISO library were settings sections; their old links still work.
const moved = new Map([["storage", "#/storage"], ["iso", "#/storage"], ["networks", "#/networks"]]);
export const movedSection = (section = "") => moved.get(section);

export function SettingsNav({ section, pool, master }: { section: string; pool?: Pool; master?: Host }) {
  const [query, setQuery] = useState("");
  return <nav className="snav" aria-label="Settings">
    <label className="search"><Icon kind="search" /><span className="sr-only">Search settings</span><input type="search" placeholder="Search settings" value={query} onChange={e => setQuery(e.target.value)} /></label>
    {sections.filter(s => s.label.toLowerCase().includes(query.trim().toLowerCase())).map(s =>
      <a key={s.key} href={`#/settings/${s.key}`} aria-current={s.key === section ? "page" : undefined}><Icon kind={s.icon} />{s.label}</a>)}
    {pool && <><h2>{pool.name} · master {master?.hostname || "unknown"}</h2><p className="note">Settings are stored on the pool master.</p></>}
  </nav>;
}

function HighAvailability() {
  return <Section icon="overview" title="High availability" actions={gatedButtons(["Configure"], "high availability", 9)}>
    <Gated columns={["Virtual machine", "Restart priority", "Placement preference", "Survives loss of one host"]} capability="high availability" phase={9} />
    <p className="note">Host fencing, which high availability will rely on, is set now under <a href="#/settings/members">Members</a>.</p>
  </Section>;
}

// The top of a settings screen: its heading and, on the overview, the pool's own summary, which
// Settings then follows with every section in the nav's order.
export function SettingsHead({ section, pool, master, hostCount }: { section: string; pool?: Pool; master?: Host; hostCount: number }) {
  const current = sections.find(s => s.key === section);
  if (!current || (current.pool && !pool)) return null;
  return <>
    <h1 className="sr-only">{current.key === "overview" ? "Pool settings" : current.label}</h1>
    {section === "overview" && <Section icon="overview" title={`Pool ${pool!.name}`}>
      <dl className="details">
        <dt>Master</dt><dd>{master ? <a href={`#/hosts/${encodeURIComponent(master.id)}`}>{master.hostname}</a> : "Unknown"}</dd>
        <dt>Hosts</dt><dd>{hostCount}</dd>
      </dl>
      <p className="note">Pool settings are stored on the pool master and come back when a new Controller adopts it. Local disks stay local; host access is always explicit.</p>
    </Section>}
  </>;
}

export function Settings({ section, pool, hosts = [], api, busy, unavailable, onEnrollHost, onNotice }: {
  section: string; pool?: Pool; hosts?: Host[]; api: API; busy: boolean; unavailable: boolean; onEnrollHost?: () => void;
  onNotice?: (text: string, operationID?: string) => void;
}) {
  const notice = onNotice || (() => {});
  const current = sections.find(s => s.key === section);
  if (!current) return <section className="panel empty"><h2>Object not found</h2><p>This link does not match a settings section.</p><a href="#/settings">Return to settings</a></section>;
  if (current.pool && !pool) return <><h1>{current.label}</h1><p className="empty">Choose a pool in the scope menu to see its {current.label.toLowerCase()}.</p></>;
  const members = <PoolMembers key={pool?.id} poolID={pool?.id || ""} api={api} unavailable={unavailable} hosts={hosts} onEnrollHost={onEnrollHost} />;
  const updatesPanel = <><ImageUpdates hosts={hosts} api={api} busy={busy} />{pool ? <PoolRollout key={pool.id} pool={pool} hosts={hosts} api={api} busy={busy} /> : null}</>;
  switch (section) {
    case "overview": return <>{members}<HighAvailability />{pool ? <BackupRepositories api={api} poolID={pool.id} masterHostID={pool.master_host_id} hosts={hosts} busy={busy} unavailable={unavailable} onNotice={notice} /> : null}{updatesPanel}</>;
    case "backups": return pool ? <BackupRepositories api={api} poolID={pool.id} masterHostID={pool.master_host_id} hosts={hosts} busy={busy} unavailable={unavailable} onNotice={notice} /> : null;
    case "ha": return <HighAvailability />;
    case "members": return members;
    case "updates": return updatesPanel;
    default: return null;
  }
}

export { Backups, Containers, Tasks, Events, taskTimestamp, resubmitLink, UploadProgress, type Upload } from "./rail-screens";
