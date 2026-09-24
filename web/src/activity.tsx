// SPDX-License-Identifier: AGPL-3.0-only
// Top-bar activity pill: a short task list in a popdown, with Open Tasks for the full page.
import type { Host, Operation } from "./types";
import type { Upload } from "./rail-screens";
import { unfinished } from "./dock";
import { operationLabel, relativeTime } from "./labels";
import { Badge, Menu } from "./ui";

const limit = 8;

export function recentActivity(operations: Operation[]): Operation[] {
  return [...operations]
    .sort((a, b) => {
      const ar = unfinished.has(a.status) ? 0 : 1;
      const br = unfinished.has(b.status) ? 0 : 1;
      if (ar !== br) return ar - br;
      return b.updated_at.localeCompare(a.updated_at);
    })
    .slice(0, limit);
}

export function ActivityMenu({
  operations,
  hosts,
  upload,
  running,
  onCancelUpload,
}: {
  operations: Operation[];
  hosts: Host[];
  upload: Upload | null;
  running: number;
  onCancelUpload: () => void;
}) {
  const items = recentActivity(operations);
  const hostName = (id: string) => hosts.find(h => h.id === id)?.hostname || id;
  const summaryLabel = running
    ? `${running} ${running === 1 ? "task" : "tasks"} running. Show tasks`
    : "No tasks running. Show tasks";
  const label = running
    ? <><span className="pulse" aria-hidden="true" />{running}<span className="activity-words"> {running === 1 ? "task" : "tasks"} running</span></>
    : <><span className="activity-words">No tasks running</span><span className="activity-idle" aria-hidden="true">0</span></>;
  return (
    <Menu align="end" className="activity" summaryLabel={summaryLabel} label={label}>
      {upload || items.length ? (
        <ul className="activity-tasks">
          {upload && (
            <li>
              <span>
                {upload.state === "uploading" && <span className="pulse" aria-hidden="true" />}
                Upload ISO
                <small>{upload.name} · {upload.percent}%</small>
              </span>
              <span className="activity-task-side">
                <Badge value={upload.state} />
                {upload.state === "uploading" && <button type="button" className="sm" onClick={onCancelUpload}>Cancel</button>}
              </span>
            </li>
          )}
          {items.map(o => (
            <li key={o.id}>
              <span>
                {unfinished.has(o.status) && <span className="pulse" aria-hidden="true" />}
                {operationLabel(o.kind)}
                <small>{hostName(o.host_id)} · {relativeTime(o.updated_at)}</small>
              </span>
              <Badge value={o.status} label={unfinished.has(o.status) ? "Running" : undefined} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="activity-empty">No tasks yet</p>
      )}
      <a href="#/tasks">Open Tasks</a>
    </Menu>
  );
}
