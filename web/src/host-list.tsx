// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useState } from "react";
import type { API, Check, Data, Host } from "./main";
import { statsHistory, type StatsHistory } from "./stats";
import { HostPanel, addresses, coreVersion, hostStatus, hostUsage, uptime } from "./host-panel";
import { versionLight } from "./updates";
import { relativeTime } from "./labels";
import { Badge, MiniBar, MissingPanel, badgeTone } from "./ui";

function checkHealth(check: Check) {
  return check.freshness === "fresh"
    ? check.status
    : check.freshness || "unknown";
}
type HealthIssue = { name: string; status: string; reason: string };
type HealthSummary = { status: string; issues: HealthIssue[] };
const healthSeverity: Record<string, number> = {
  healthy: 0,
  unsupported: 1,
  degraded: 2,
  unknown: 3,
  stale: 4,
  failed: 5,
};
export function hostHealth(host: Host): HealthSummary {
  if (host.revoked) return { status: "revoked", issues: [{ name: "Management access", status: "revoked", reason: "Access revoked; this host is not reporting." }] };
  const checks = host.inventory?.checks || [];
  const issues = checks.filter((check) => checkHealth(check) !== "healthy").map((check) => ({
    name: check.name,
    status: checkHealth(check),
    reason: check.error || "No reason reported by Nex Core.",
  }));
  if (host.connection !== "fresh") {
    issues.unshift({
      name: "Management connection",
      status: host.connection === "stale" ? "stale" : "unknown",
      reason: host.connection === "stale" ? "The host is not reporting fresh data; current workload execution is unknown." : "The host has not reported a current management connection.",
    });
  }
  if (!checks.length && host.connection === "fresh") {
    return { status: "unknown", issues: [{ name: "Health checks", status: "unknown", reason: "No health checks have been observed." }] };
  }
  if (!issues.length) return { status: "healthy", issues };
  return { status: issues.reduce((worst, issue) => healthSeverity[issue.status] > healthSeverity[worst] ? issue.status : worst, issues[0].status), issues };
}
export function hostConsoleBlocker(host: Host): string {
  if (host.revoked || host.connection !== "fresh") return "The host is not freshly connected. Its console cannot be opened until it reports again.";
  // Must match the gate openHostConsole applies (protocol.HostConsoleCapability); the VM
  // console uses console-v1, which an old Core advertises without a host console.
  if (!host.capabilities?.includes("host-console-v1")) return "The host agent does not support browser consoles. Upgrade Nex Core on this host.";
  return "";
}
// Each online host's own 10-minute usage history, for the table's CPU and memory bars and the
// panel's sparklines. Re-read every 30 s while the page is visible; a host that is not online is
// not asked, and one that cannot answer shows a dash.
function useHostHistories(hosts: Host[], api: API) {
  const [histories, setHistories] = useState<Record<string, StatsHistory>>({});
  const online = hosts.filter(h => h.connection === "fresh" && !h.revoked).map(h => h.id).join(" ");
  useEffect(() => {
    let stopped = false;
    const load = () => {
      if (document.visibilityState !== "visible") return;
      for (const id of online.split(" ").filter(Boolean)) {
        api<StatsHistory>(`hosts/${encodeURIComponent(id)}/stats?range=10m`).then(statsHistory)
          .then(history => { if (!stopped) setHistories(all => ({ ...all, [id]: history })); }, () => {});
      }
    };
    load();
    const timer = setInterval(load, 30_000);
    return () => { stopped = true; clearInterval(timer); };
  }, [online]);
  return histories;
}

// HostList is the Hosts screen: every host in scope, and the selected host's slide-over panel.
export function HostList({ data, hosts, selectedID, api, busy, unavailable, loading, coreAvailable, openHostConsole, onRefreshInventory, onReenroll, onRevoke }: {
  data: Data; hosts: Host[]; selectedID?: string; api: API; busy: boolean; unavailable: boolean; loading: boolean; coreAvailable?: string;
  openHostConsole: (id: string) => Promise<string>; onRefreshInventory: () => void; onReenroll: () => void; onRevoke: () => void;
}) {
  const host = data.hosts.find(h => h.id === selectedID);
  // A panel opened for a host outside the scope still needs that host's usage.
  const histories = useHostHistories(host && !hosts.includes(host) ? [...hosts, host] : hosts, api);
  const close = () => { location.hash = "/hosts"; };
  return <>
    <div className="list-head"><h1>Hosts</h1><span className="muted">{hosts.length} in scope</span></div>
    {hosts.length ? <div className="table-scroll"><table className="list">
      <thead><tr><th>Name</th><th>Pool</th><th>Role</th><th>Status</th><th>IP address</th><th>CPU</th><th>Memory</th><th>VMs</th><th>Nex Core</th><th>Uptime</th></tr></thead>
      <tbody>{hosts.map(h => {
        const light = h.inventory && versionLight(h.inventory.os_version, coreAvailable);
        const isMaster = data.pools.some(p => p.id === h.pool_id && p.master_host_id === h.id);
        const status = hostStatus(h, unavailable);
        const usage = hostUsage(h.connection === "fresh" ? histories[h.id] : null);
        return <tr key={h.id} className={h.id === selectedID ? "sel" : undefined} onClick={() => { location.hash = `/hosts/${h.id}`; }}>
          <td><span className={`dot ${badgeTone(status.value)}`} aria-hidden="true" /><a className="row-lead" href={`#/hosts/${h.id}`} onClick={e => e.stopPropagation()}>{h.hostname || h.id}</a></td>
          <td>{data.pools.find(p => p.id === h.pool_id)?.name || "—"}</td>
          <td><Badge value={isMaster ? "master" : "member"} label={isMaster ? "Master" : "Member"} /></td>
          <td>{status.value === "online" ? status.label : <><Badge value={status.value} label={status.label} />{h.last_seen && <span className="muted"> Last report {relativeTime(h.last_seen)}</span>}</>}</td>
          <td className="mono">{addresses(h)[0] || "—"}</td>
          <td><MiniBar percent={usage.cpu} /></td>
          <td><MiniBar percent={usage.memory} /></td>
          <td>{data.vms.filter(v => v.host_id === h.id).length}</td>
          <td title={h.inventory?.os_version}>{h.inventory ? coreVersion(h.inventory.os_version) : "—"}{light && light.level !== "current" && <> <Badge value="warning" label="Update" /></>}</td>
          <td>{uptime(h) ?? <span className="muted">—</span>}</td>
        </tr>;
      })}</tbody>
    </table></div> : <p className="empty">{loading ? "Loading…" : "No hosts in this scope. Use Add host in the scope menu to connect a Nex Core host."}</p>}
    <p className="note">A stale connection means reachability is unknown. It does not mean virtual machines have stopped.</p>
    {selectedID && (host ? <HostPanel key={host.id} data={data} host={host} history={histories[host.id]} api={api} busy={busy} unavailable={unavailable} coreAvailable={coreAvailable}
      consoleBlocker={hostConsoleBlocker(host)}
      openHostConsole={openHostConsole} onRefreshInventory={onRefreshInventory} onReenroll={onReenroll} onRevoke={onRevoke} onClose={close} />
    : <MissingPanel icon="host" noun="Host" unavailable={unavailable} loading={loading} onClose={close} />)}
  </>;
}
