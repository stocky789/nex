// SPDX-License-Identifier: AGPL-3.0-only
// Rail screens outside pool settings: Containers, Tasks and Events. Backups lives in backups.tsx.
import type { Audit, Data, ISODownloadProgress, Operation } from "./types";
import { auditLabel, operationLabel, relativeTime } from "./labels";
import { Icon } from "./icons";
import { auditTarget, AttentionList, Badge, Section, badgeTone, date, size, type Attention } from "./ui";
import { Gated, gatedButtons } from "./gated";
import "./settings.css";

export { Backups } from "./backups";

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
const downloadSize = (bytes: number) => bytes >= 1024 ** 3 ? size(bytes) : `${(bytes / 1024 ** 2).toFixed(1)} MiB`;
// The hosts report no percentage for a task, so progress is the stage it is in; the pulse marks
// the ones still running, as the top bar's activity pill counts them.
const progress: Record<string, string> = { pending: "Running on the host", dispatched: "Awaiting agent result" };
const dot = (value: string) => <span className={`dot ${badgeTone(value)}`} aria-hidden="true" />;
// Shared meter for the browser-to-Controller transfer: Tasks and the ISO library both show it.
export function UploadProgress({ upload, onCancel }: { upload: Upload; onCancel?: () => void }) {
  const running = upload.state === "uploading";
  return (
    <div className="upload-progress">
      <progress max={100} value={upload.percent} aria-label={`Upload progress for ${upload.name}`} />
      <small className="mono">{upload.percent}% · {running ? rate(upload.rate) : size(upload.total)}</small>
      {running && onCancel && <button type="button" onClick={onCancel}>Cancel</button>}
    </div>
  );
}
export function isoDownload(hosts: { id: string; connection: string; inventory?: { managed?: { iso_downloads?: ISODownloadProgress[] | null } | null } | null }[], hostID: string, operationID: string) {
  const host = hosts.find(h => h.id === hostID && h.connection === "fresh");
  return host?.inventory?.managed?.iso_downloads?.find(d => d.operation_id === operationID);
}
export function DownloadProgress({ name, download }: { name: string; download: ISODownloadProgress }) {
  const percent = download.total_bytes ? Math.min(100, Math.round(100 * download.received_bytes / download.total_bytes)) : undefined;
  return <div className="upload-progress">
    <progress max={100} value={percent} aria-label={`Download progress for ${name}`} />
    <small className="mono">{percent === undefined ? `${downloadSize(download.received_bytes)} downloaded · size unknown` : `${percent}% · ${downloadSize(download.received_bytes)} of ${downloadSize(download.total_bytes!)}`}</small>
  </div>;
}
// UploadRow covers only the browser-to-Controller transfer. The host import that follows it
// is a durable task and stays in the task list and the ISO library.
function UploadRow({ upload, onCancel }: { upload: Upload; onCancel: () => void }) {
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
      <td><UploadProgress upload={upload} onCancel={onCancel} /></td>
    </tr>
  );
}
// There is no generic retry request: a task that failed links to the place its action is sent
// again, found through the resource that records the task, or else the host it ran on.
export function resubmitLink(o: Operation, data: Pick<Data, "hosts" | "vms" | "storage" | "networks" | "media">) {
  const vm = data.vms.find(v => v.operation_id === o.id);
  if (vm) return { href: `#/vms/${encodeURIComponent(vm.id)}`, label: `Open ${vm.name}` };
  if (data.storage.some(r => r.operation_id === o.id)) return { href: "#/storage", label: "Open Storage" };
  if (data.networks.some(r => r.operation_id === o.id)) return { href: "#/networks", label: "Open Networks" };
  if (data.media.some(r => r.operation_id === o.id)) return { href: "#/storage", label: "Open the ISO library" };
  if (o.kind.startsWith("backup-repository-") || o.kind === "backup-orphan-cleanup") return { href: "#/settings/backups", label: "Open Backup repositories" };
  return { href: `#/hosts/${encodeURIComponent(o.host_id)}`, label: `Open ${data.hosts.find(h => h.id === o.host_id)?.hostname || o.host_id}` };
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
                const download = o.kind === "iso-import" && (o.status === "queued" || o.status === "dispatched") && isoDownload(hosts, o.host_id, o.id);
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
                  <td className="muted">{download
                    ? <DownloadProgress name={data.media.find(m => m.operation_id === o.id)?.name || "ISO"} download={download} />
                    : progress[o.status] ? <><span className="pulse" aria-hidden="true" />{progress[o.status]}</> : o.status === "queued" ? "Waiting to be sent" : "—"}</td>
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
                const { target: source, check } = auditTarget(entry, data);
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
