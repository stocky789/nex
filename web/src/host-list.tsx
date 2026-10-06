// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useState } from "react";
import type { API, Data, Host } from "./types";
import type { StatsHistory } from "./stats";
import { loadHostStats } from "./stats-cache";
import { HostPanel, addresses, coreVersion, hostStatus, hostUsage, uptime } from "./host-panel";
import { hostConsoleBlocker } from "./host-model";
import { relativeTime } from "./labels";
import { ImageUpdateBadge } from "./image-updates";
import { Badge, MiniBar, MissingPanel, Panel, badgeTone } from "./ui";

export { hostHealth, hostConsoleBlocker } from "./host-model";

// Each online host's own 10-minute usage history, for the table's CPU and memory bars and the
// panel's sparklines. Re-read every 30 s while the page is visible; one setState per wave.
function useHostHistories(hosts: Host[], api: API) {
  const [histories, setHistories] = useState<Record<string, StatsHistory>>({});
  const online = hosts.filter(h => h.connection === "fresh" && !h.revoked).map(h => h.id).join(" ");
  useEffect(() => {
    let stopped = false;
    const ids = online.split(" ").filter(Boolean);
    const load = () => {
      if (document.visibilityState !== "visible" || !ids.length) return;
      loadHostStats(api, ids, "10m").then(next => { if (!stopped) setHistories(next); });
    };
    load();
    const timer = setInterval(load, 30_000);
    return () => { stopped = true; clearInterval(timer); };
  }, [online]);
  return histories;
}

type HostListProps = {
  data: Data; hosts: Host[]; selectedID?: string; api: API; busy: boolean; unavailable: boolean; loading: boolean;
  openHostConsole: (id: string) => Promise<string>; onRefreshInventory: () => void; onReenroll: () => void; onRevoke: () => void;
};

// HostList is the Hosts screen: every host in scope, and the selected host's slide-over panel.
export function HostList({ data, hosts, selectedID, api, busy, unavailable, loading, openHostConsole, onRefreshInventory, onReenroll, onRevoke }: HostListProps) {
  const scoped = hosts.find(h => h.id === selectedID);
  const known = data.hosts.find(h => h.id === selectedID);
  // A panel opened for a host outside the scope still needs that host's usage once the operator
  // switches scope; until then the panel is refused.
  const histories = useHostHistories(hosts, api);
  const vmCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const v of data.vms) counts.set(v.host_id, (counts.get(v.host_id) || 0) + 1);
    return counts;
  }, [data.vms]);
  const close = () => { location.hash = "/hosts"; };
  const poolName = (id: string) => data.pools.find(p => p.id === id)?.name || "—";
  return <>
    <div className="list-head"><h1>Hosts</h1><span className="muted">{hosts.length} in scope</span></div>
    {hosts.length ? <div className="table-scroll"><table className="list">
      <thead><tr><th>Name</th><th>Pool</th><th>Role</th><th>Status</th><th>IP address</th><th>CPU</th><th>Memory</th><th>VMs</th><th>Nex Core</th><th>Uptime</th></tr></thead>
      <tbody>{hosts.map(h => {
        const isMaster = data.pools.some(p => p.id === h.pool_id && p.master_host_id === h.id);
        const status = hostStatus(h, unavailable);
        const usage = hostUsage(h.connection === "fresh" ? histories[h.id] : null);
        return <tr key={h.id} data-host={h.id} className={h.id === selectedID ? "sel" : undefined} onClick={() => { location.hash = `/hosts/${encodeURIComponent(h.id)}`; }}>
          <td><span className={`dot ${badgeTone(status.value)}`} aria-hidden="true" /><a className="row-lead" href={`#/hosts/${encodeURIComponent(h.id)}`} onClick={e => e.stopPropagation()}>{h.hostname || h.id}</a></td>
          <td>{poolName(h.pool_id)}</td>
          <td><Badge value={isMaster ? "master" : "member"} label={isMaster ? "Master" : "Member"} /></td>
          <td>{status.value === "online" ? status.label : <><Badge value={status.value} label={status.label} />{h.last_seen && <span className="muted"> Last report {relativeTime(h.last_seen)}</span>}</>}<ImageUpdateBadge host={h} /></td>
          <td className="mono">{addresses(h)[0] || "—"}</td>
          <td><MiniBar percent={usage.cpu} /></td>
          <td><MiniBar percent={usage.memory} /></td>
          <td>{vmCounts.get(h.id) || 0}</td>
          <td title={h.inventory?.os_version}>{h.inventory ? coreVersion(h.inventory.os_version) : "—"}</td>
          <td>{uptime(h) ?? <span className="muted">—</span>}</td>
        </tr>;
      })}</tbody>
    </table></div> : <p className="empty">{loading ? "Loading…" : "No hosts in this scope. Use Add host in the scope menu to connect a Nex Core host."}</p>}
    <p className="note">A stale connection means reachability is unknown. It does not mean virtual machines have stopped.</p>
    {selectedID && (scoped ? <HostPanel key={scoped.id} data={data} host={scoped} history={histories[scoped.id]} api={api} busy={busy} unavailable={unavailable}
      consoleBlocker={hostConsoleBlocker(scoped)}
      openHostConsole={openHostConsole} onRefreshInventory={onRefreshInventory} onReenroll={onReenroll} onRevoke={onRevoke} onClose={close} />
    : known ? <Panel icon="host" title={known.hostname || known.id} onClose={close}>
      <p className="empty">This host is outside the current site or pool scope. Choose its site or pool in the scope menu to open it.</p>
    </Panel>
    : <MissingPanel icon="host" noun="Host" unavailable={unavailable} loading={loading} onClose={close} />)}
  </>;
}
