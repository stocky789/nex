// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useState } from "react";
import type { API, Audit, Data, Host, Operation, Pool } from "./main";
import { PoolMembers } from "./pool-members";
import type { UpdateStatus } from "./updates";
import { auditLabel, operationLabel, relativeTime } from "./labels";
import { Icon, type IconKind } from "./icons";
import { AttentionList, Badge, Section, badgeTone, date, size, type Attention } from "./ui";
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

// Rule 8 of the design guide: a section whose Nex Core phase has not landed keeps its real
// header, actions and columns, disabled, and says in one row what is missing.
function Gated({ columns, capability, phase }: { columns: string[]; capability: string; phase: number }) {
  return <div className="table-scroll"><table>
    <thead><tr>{columns.map(c => <th key={c}>{c}</th>)}</tr></thead>
    <tbody><tr><td colSpan={columns.length} className="gated">Available with {capability} (Phase {phase}).</td></tr></tbody>
  </table></div>;
}
const gatedButtons = (labels: string[], capability: string, phase: number) => labels.map((label, i) =>
  <button key={label} type="button" className={i ? "sm" : "sm primary"} disabled title={`Available with ${capability} (Phase ${phase})`}>{label}</button>);

function BackupRepositories() {
  return <Section icon="backup" title="Backup repositories" actions={gatedButtons(["Create", "Policies", "Restore"], "backups", 6)}>
    <Gated columns={["Name", "Target", "Retention", "Last run", "Last restore test"]} capability="backups" phase={6} />
  </Section>;
}
function HighAvailability() {
  return <Section icon="overview" title="High availability" actions={gatedButtons(["Configure"], "high availability", 9)}>
    <Gated columns={["Virtual machine", "Restart priority", "Placement preference", "Survives loss of one host"]} capability="high availability" phase={9} />
    <p className="note">Host fencing, which high availability will rely on, is set now under <a href="#/settings/members">Members</a>.</p>
  </Section>;
}

type Updates = { update: UpdateStatus | null; note: string; phase: "idle" | "checking" | "applying" | "done" | "error"; onCheck: () => void; onApply: () => void };

// The top of a settings screen: its heading and, on the overview, the pool's own summary, which
// Settings then follows with every section in the nav's order.
export function SettingsHead({ section, pool, master, hostCount }: { section: string; pool?: Pool; master?: Host; hostCount: number }) {
  const current = sections.find(s => s.key === section);
  if (!current || (current.pool && !pool)) return null;
  return <>
    <h1 className="sr-only">{current.key === "overview" ? "Pool settings" : current.label}</h1>
    {section === "overview" && <Section icon="overview" title={`Pool ${pool!.name}`}>
      <dl className="details">
        <dt>Master</dt><dd>{master ? <a href={`#/hosts/${master.id}`}>{master.hostname}</a> : "Unknown"}</dd>
        <dt>Hosts</dt><dd>{hostCount}</dd>
      </dl>
      <p className="note">Pool settings are stored on the pool master and come back when a new Controller adopts it. Local disks stay local; host access is always explicit.</p>
    </Section>}
  </>;
}

export function Settings({ section, pool, hosts = [], api, busy, unavailable, updates, onEnrollHost }: {
  section: string; pool?: Pool; hosts?: Host[]; api: API; busy: boolean; unavailable: boolean; updates: Updates; onEnrollHost?: () => void;
}) {
  const current = sections.find(s => s.key === section);
  if (!current) return <section className="panel empty"><h2>Object not found</h2><p>This link does not match a settings section.</p><a href="#/settings">Return to settings</a></section>;
  if (current.pool && !pool) return <><h1>{current.label}</h1><p className="empty">Choose a pool in the scope menu to see its {current.label.toLowerCase()}.</p></>;
  const members = <PoolMembers key={pool?.id} poolID={pool?.id || ""} api={api} unavailable={unavailable} hosts={hosts} coreAvailable={updates.update?.core_available_version} onEnrollHost={onEnrollHost} />;
  const updatesPanel = <UpdatesPanel {...updates} busy={busy} />;
  switch (section) {
    case "overview": return <>{members}<HighAvailability /><BackupRepositories />{updatesPanel}</>;
    case "backups": return <BackupRepositories />;
    case "ha": return <HighAvailability />;
    case "members": return members;
    case "updates": return updatesPanel;
    default: return null;
  }
}

function UpdatesPanel({
  update,
  note,
  phase,
  busy,
  onCheck,
  onApply,
}: Updates & { busy: boolean }) {
  const working = phase === "checking" || phase === "applying";
  const pct = typeof update?.progress === "number" ? update.progress : undefined;
  // Updates run through apt in fixed steps, so prefer the step the server reports
  // over a percentage; "downloading" now covers refreshing the package index too.
  const label =
    phase === "checking" ? "Checking…"
      : update?.message && update.phase && update.phase !== "done" ? `${update.message}…`
        : update?.phase === "downloading" ? "Downloading…"
          : update?.phase === "installing" ? "Installing…"
            : update?.phase === "restarting" ? "Restarting…"
              : phase === "applying" ? "Updating…"
                : note || "Done";
  // Every status object is a fresh read from the Controller, so its arrival is the check time.
  const [checkedAt, setCheckedAt] = useState("");
  useEffect(() => { if (update && !update.error) setCheckedAt(new Date().toISOString()); }, [update]);
  return (
    <Section icon="refresh" title="Updates" actions={<>
      {update && !update.error && <Badge value={update.update_available ? "warning" : "ok"} label={update.update_available ? "Update available" : "Up to date"} />}
      <button className="sm primary" disabled={busy || !update?.update_available} onClick={onApply}>Update</button>
      <button className="sm" disabled={busy} onClick={onCheck}>Check</button>
    </>}>
      <dl className="details">
        <dt>Nex Controller installed</dt>
        <dd>{update?.installed_version || "—"}</dd>
        <dt>Available</dt>
        <dd>{!update ? "—" : update.update_available ? (update.available_version || "—") : update.error ? "—" : "Up to date"}</dd>
        {update?.core_available_version && <><dt>Nex Core in the archive</dt><dd>{update.core_available_version}</dd></>}
        {update?.repo_url && <><dt>Package archive</dt><dd className="mono">{update.repo_url}</dd></>}
        <dt>Last checked</dt>
        <dd>{checkedAt ? <time dateTime={checkedAt} title={date(checkedAt)}>{relativeTime(checkedAt)}</time> : "Not yet"}</dd>
      </dl>
      {(working || phase === "done") && <p className="small" role="status">{label}</p>}
      {working && (
        <progress
          aria-label={phase === "checking" ? "Checking for updates" : "Applying update"}
          max={100}
          value={phase === "applying" && pct !== undefined ? pct : undefined}
        />
      )}
      {update?.error && <p className="warning" role="status">{update.error}</p>}
      {note && phase === "idle" && <p className="notice" role="status">{note}</p>}
      <p className="note">Each host's Nex Core version is listed under Members; hosts update through their own package archive.</p>
    </Section>
  );
}

// Backups (Phase 6) and container services (Phase 10) have their place in the console now; the
// pages show the real layout with disabled controls and never an invented job or stack.
export function Backups() {
  return <>
    <h1 className="sr-only">Backups</h1>
    <Section icon="backup" title="Backup jobs" actions={gatedButtons(["Back up now", "Restore", "Test restore"], "backups", 6)}>
      <Gated columns={["Job", "Virtual machines", "Schedule", "Last run", "Result", "Last restore test"]} capability="backups" phase={6} />
      <p className="note">A completed backup is not proof of a working restore: each job will show its last verified restore test beside its last run.</p>
    </Section>
    <BackupRepositories />
  </>;
}
export function Containers() {
  return <>
    <h1 className="sr-only">Container services</h1>
    <Section icon="containers" title="Swarm clusters" actions={gatedButtons(["Create cluster"], "container services", 10)}>
      <Gated columns={["Cluster", "Node VMs", "Managers", "Services", "Status"]} capability="container services" phase={10} />
    </Section>
    <Section icon="containers" title="Stacks and services" actions={gatedButtons(["Deploy stack"], "container services", 10)}>
      <div className="empty containers-empty">
        <Icon kind="containers" size={28} />
        <p>Stacks and services will be listed here, each with its application's own colourful icon, running on Docker Swarm in Nex-managed node VMs.</p>
        <p className="note">Available with container services (Phase 10).</p>
      </div>
    </Section>
  </>;
}

const adoptedOutcomePrefix = "adopt-outcome:";
export function taskTimestamp(operation: Pick<Operation, "id" | "host_id" | "request_id" | "updated_at">) {
  const reobserved = operation.request_id === `${adoptedOutcomePrefix}${operation.host_id}:${operation.id}`;
  return {
    reobserved,
    value: date(operation.updated_at),
    iso: operation.updated_at,
  };
}
export type Upload = {
  name: string;
  loaded: number;
  total: number;
  percent: number;
  rate: number;
  state: "uploading" | "succeeded" | "failed" | "cancelled";
  message: string;
  actor: string;
  updated_at: string;
};
const rate = (bytes: number) =>
  bytes >= 1024 ** 2
    ? `${(bytes / 1024 ** 2).toFixed(1)} MiB/s`
    : `${Math.max(0, Math.round(bytes / 1024))} KiB/s`;
// The hosts report no percentage for a task, so progress is the stage it is in; the pulse marks
// the ones still running, as the top bar's activity pill counts them.
const progress: Record<string, string> = { pending: "Running on the host", dispatched: "Awaiting agent result" };
const dot = (value: string) => <span className={`dot ${badgeTone(value)}`} aria-hidden="true" />;
// UploadRow covers only the browser-to-Controller transfer. The host import that follows it
// is a durable task and stays in the task list and the ISO library.
function UploadRow({ upload, onCancel }: { upload: Upload; onCancel: () => void }) {
  const running = upload.state === "uploading";
  return (
    <tr aria-label="ISO upload to Controller">
      <td>
        {dot(upload.state)}<strong>Upload ISO</strong>
        <small>{upload.name} · Controller</small>
        {upload.message && <p className="task-error">{upload.message}</p>}
      </td>
      <td>{upload.actor}</td>
      <td className="muted">{date(upload.updated_at)}</td>
      {/* Only the outcome is announced; percent changes far too often for a live region. */}
      <td><span role="status"><Badge value={upload.state} /></span></td>
      <td>
        <div className="upload-progress">
          <progress max={100} value={upload.percent} aria-label={`Upload progress for ${upload.name}`} />
          <small className="mono">{upload.percent}% · {running ? rate(upload.rate) : size(upload.total)}</small>
          {running && <button onClick={onCancel}>Cancel</button>}
        </div>
      </td>
    </tr>
  );
}
// There is no generic retry request: a task that failed links to the place its action is sent
// again, found through the resource that records the task, or else the host it ran on.
export function resubmitLink(o: Operation, data: Pick<Data, "hosts" | "vms" | "storage" | "networks" | "media">) {
  const vm = data.vms.find(v => v.operation_id === o.id);
  if (vm) return { href: `#/vms/${vm.id}`, label: `Open ${vm.name}` };
  if (data.storage.some(r => r.operation_id === o.id)) return { href: "#/storage", label: "Open Storage" };
  if (data.networks.some(r => r.operation_id === o.id)) return { href: "#/networks", label: "Open Networks" };
  if (data.media.some(r => r.operation_id === o.id)) return { href: "#/storage", label: "Open the ISO library" };
  return { href: `#/hosts/${o.host_id}`, label: `Open ${data.hosts.find(h => h.id === o.host_id)?.hostname || o.host_id}` };
}
export function Tasks({ data, upload, onCancelUpload }: { data: Data; upload: Upload | null; onCancelUpload: () => void }) {
  const { operations, hosts } = data;
  return <>
    <h1 className="sr-only">Tasks</h1>
    <Section icon="tasks" title="Tasks" count={operations.length + (upload ? 1 : 0)}>
      {operations.length || upload ? (
        <div className="table-scroll">
          <table className="records tasks">
            <thead><tr><th>Task</th><th>By</th><th>When</th><th>Result</th><th>Progress</th></tr></thead>
            <tbody>
              {upload && <UploadRow upload={upload} onCancel={onCancelUpload} />}
              {operations.map((o) => {
                const timestamp = taskTimestamp(o);
                return <tr key={o.id}>
                  <td>
                    {dot(o.status)}<strong>{operationLabel(o.kind)}</strong>
                    <small><a href={`#/hosts/${o.host_id}`}>{hosts.find((h) => h.id === o.host_id)?.hostname || o.host_id}</a> · <span className="mono">{o.id}</span></small>
                    {o.error && <p className="task-error">{o.error}</p>}
                    {(o.status === "failed" || o.status === "needs_attention") && (link => <small><a href={link.href}>{link.label}</a> to send it again.</small>)(resubmitLink(o, data))}
                  </td>
                  <td>{o.actor}</td>
                  <td className="muted">{timestamp.reobserved ? <><strong>Reobserved from Core history</strong><small>{timestamp.value}</small><small>Original outcome time was not reported.</small></> : <time dateTime={timestamp.iso} title={timestamp.value}>{relativeTime(timestamp.iso)}</time>}</td>
                  <td><Badge value={o.status} /></td>
                  <td className="muted">{progress[o.status] ? <><span className="pulse" aria-hidden="true" />{progress[o.status]}</> : o.status === "queued" ? "Waiting to be sent" : "—"}</td>
                </tr>;
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="empty">No tasks yet. Host inventory refresh requests will appear here.</p>
      )}
      <p className="note">Pending results survive reconnects. A delayed response is not proof that an operation failed.</p>
    </Section>
  </>;
}

// Events is the audit history as a table: recorded changes and debounced health transitions.
// Health entries are recorded as "health.<status>", so their last part is the result.
export function Events({ entries, data, conditions }: { entries: Audit[]; data: Data; conditions: Attention[] }) {
  return <>
    <h1 className="sr-only">Events</h1>
    <Section icon="warning" title="Current conditions" count={conditions.length}>
      {conditions.length ? <AttentionList items={conditions} /> : <p className="empty">Every host is reporting and healthy, and no task needs attention.</p>}
      <p className="note">What the hosts report right now: hosts not reporting, failing checks, storage faults and failed tasks.</p>
    </Section>
    <Section icon="events" title="Events" count={entries.length}>
      {entries.length ? (
        <div className="table-scroll">
          <table className="records">
            <thead><tr><th>Event</th><th>Source</th><th>When</th><th>Result</th></tr></thead>
            <tbody>
              {entries.map((entry) => {
                const [hostID, check] = entry.resource_id.split("/");
                const source =
                  data.hosts.find((h) => h.id === hostID)?.hostname ||
                  data.vms.find((v) => v.id === entry.resource_id)?.name ||
                  data.pools.find((p) => p.id === entry.resource_id)?.name ||
                  data.sites.find((s) => s.id === entry.resource_id)?.name ||
                  entry.resource_id;
                const outcome = entry.action.split(".").pop() || "";
                const health = badgeTone(outcome) !== "neutral";
                return <tr key={entry.id}>
                  <td>
                    {dot(health ? outcome : "")}<strong>{auditLabel(entry.action)}</strong>
                    <small>by {entry.actor}</small>
                    {entry.detail && <p className="help">{entry.detail}</p>}
                  </td>
                  <td>{source}{check ? ` / ${check}` : ""}</td>
                  <td className="muted"><time dateTime={entry.created_at} title={date(entry.created_at)}>{relativeTime(entry.created_at)}</time></td>
                  <td>{health ? <Badge value={outcome} /> : <Badge value="recorded" label="Recorded" />}</td>
                </tr>;
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="empty">No events recorded yet.</p>
      )}
      <p className="note">Recorded changes and debounced health transitions. Current check results are shown on each host.</p>
    </Section>
  </>;
}
