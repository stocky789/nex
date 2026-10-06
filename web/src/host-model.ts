// SPDX-License-Identifier: AGPL-3.0-only
// Host health and status helpers shared by the Hosts list and Overview.
import type { Check, Host } from "./types";

function checkHealth(check: Check) {
  return check.freshness === "fresh"
    ? check.status
    : check.freshness || "unknown";
}

export type HealthIssue = { name: string; status: string; reason: string };
export type HealthSummary = { status: string; issues: HealthIssue[] };

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
  // console uses console-v1, which a Core advertises without a host console.
  if (!host.capabilities?.includes("host-console-v1")) return "The host agent does not support browser consoles. Upgrade Nex Core on this host.";
  return "";
}
