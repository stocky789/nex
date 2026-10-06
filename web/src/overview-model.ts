// SPDX-License-Identifier: AGPL-3.0-only
// Pure Overview calculations shared with the shell (attention list, storage samples) without
// pulling the Overview screen module into the initial bundle.
import type { Data, Host } from "./types";
import { vmStateLabel, type VM } from "./managed";
import { hostHealth } from "./host-model";
import type { StatsHistory } from "./stats";
import { storageAccess } from "./storage";
import { operationLabel, relativeTime, statusLabel } from "./labels";
import { badgeTone, type Attention } from "./ui";

type Point = number | null;
type Usage = { times: number[]; cpu: Point[]; memory: Point[]; io: Point[]; memoryUsed?: number; memoryTotal?: number; hostCPU: Record<string, number> };

const gib = (bytes: number) => Math.round(bytes / 1024 ** 3);
const last = (values: Point[]) => [...values].reverse().find(v => v != null) ?? null;
const vmMiB = (vm: VM) => vm.spec?.memory.size_mib || 0;
const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;
const stopped = (vm: VM) => vm.state === "shut off" || vm.state === "stopped" || vm.state === "shutoff";

// Pool lines are the hosts' own series added up step by step: CPU weighted by each host's CPU
// count, memory as used over total, storage I/O as the summed disk throughput of every host.
export function poolUsage(histories: StatsHistory[], hosts: Host[], seconds: number): Usage {
  const cpu = new Map<number, [number, number]>(), used = new Map<number, number>(), total = new Map<number, number>(), io = new Map<number, number>();
  const add = (map: Map<number, number>, t: number, v: number) => map.set(t, (map.get(t) || 0) + v);
  const hostCPU: Record<string, number> = {};
  let memoryUsed: number | undefined, memoryTotal: number | undefined;
  for (const h of histories) {
    const step = h.step_seconds, start = Math.round(Date.parse(h.start) / 1000 / step);
    const host = hosts.find(x => x.id === h.id);
    const cores = host?.inventory?.cpu_count || 1, fresh = host?.connection === "fresh";
    const series = (key: string) => h.series.find(s => s.key === key)?.values || [];
    const lastUsed = last(series("memory.used")), lastTotal = last(series("memory.total"));
    // A host that is not reporting has no current memory figure, only an old one.
    if (lastUsed != null && lastTotal != null && fresh) { memoryUsed = (memoryUsed || 0) + lastUsed; memoryTotal = (memoryTotal || 0) + lastTotal; }
    const hostLast = last(series("cpu.total"));
    if (hostLast != null) hostCPU[h.id] = hostLast;
    series("cpu.total").forEach((v, i) => { if (v != null) { const [sum, weight] = cpu.get(start + i) || [0, 0]; cpu.set(start + i, [sum + v * cores, weight + cores]); } });
    const t = series("memory.total");
    series("memory.used").forEach((v, i) => { if (v != null && t[i]) { add(used, start + i, v); add(total, start + i, t[i]!); } });
    for (const s of h.series) if (s.group === "disk" && s.unit === "bytes_per_second") s.values.forEach((v, i) => { if (v != null) add(io, start + i, v); });
  }
  const step = histories[0]?.step_seconds || 60;
  const end = Math.max(0, ...histories.map(h => Math.round(Date.parse(h.start) / 1000 / h.step_seconds) + Math.max(0, ...h.series.map(s => s.values.length)) - 1));
  const points = Math.round(seconds / step);
  const slots = end ? Array.from({ length: points }, (_, i) => end - points + 1 + i) : [];
  return {
    times: slots.map(s => s * step * 1000),
    cpu: slots.map(s => { const c = cpu.get(s); return c ? c[0] / c[1] : null; }),
    memory: slots.map(s => total.get(s) ? 100 * used.get(s)! / total.get(s)! : null),
    io: slots.map(s => io.get(s) ?? null),
    memoryUsed, memoryTotal, hostCPU,
  };
}

// No host reports storage use over time, so the Storage tile draws from samples this browser tab
// takes on every inventory reload (about every 10 s) since the page opened, the last 30 kept. They
// are per pool, so a scope change sums other pools' samples, and nothing is stored.
type StorageSample = Record<string, { capacity: number; used: number }>;
const storageSamples: StorageSample[] = [];
export function sampleCounts(data: Pick<Data, "pools" | "storage">, samples = storageSamples) {
  const sample: StorageSample = {};
  for (const p of data.pools) sample[p.id] = { capacity: 0, used: 0 };
  for (const st of data.storage) if (sample[st.pool_id] && !st.tombstone && st.access?.total_bytes) {
    sample[st.pool_id].capacity += st.access.total_bytes;
    sample[st.pool_id].used += st.access.total_bytes - (st.access.available_bytes || 0);
  }
  samples.push(sample);
  if (samples.length > 30) samples.shift();
}
// The share of storage used in the given pools at each sample, as a percentage.
export function storageSeries(poolIDs: string[], samples = storageSamples) {
  return samples.flatMap(sample => {
    const capacity = poolIDs.reduce((sum, id) => sum + (sample[id]?.capacity || 0), 0);
    return capacity ? [100 * poolIDs.reduce((sum, id) => sum + (sample[id]?.used || 0), 0) / capacity] : [];
  });
}

// A status bar segment: one host, or every VM in one state. `count` sets its share of the bar.
export type Part = { label: string; tone: string; count: number; title: string };
export function hostParts(hosts: Pick<Host, "hostname" | "connection" | "revoked">[]): Part[] {
  return hosts.map(h => {
    const [label, tone] = h.revoked ? ["Revoked", "bad"] : h.connection === "fresh" ? ["Online", "ok"] : h.connection === "stale" ? ["Stale", "warn"] : ["Unknown", "unknown"];
    return { label, tone, count: 1, title: `${h.hostname}: ${label}` };
  });
}
// VMs grouped by state, running first and unknown last. A VM on a host that is not reporting is
// Unknown, never Stopped.
const vmTones: Record<string, [string, number]> = { Running: ["ok", 0], Stopped: ["neutral", 2], Unknown: ["unknown", 3] };
export function vmParts(vms: VM[], known: (vm: VM) => boolean): Part[] {
  const groups = new Map<string, Part>();
  for (const v of vms) {
    const label = !known(v) ? "Unknown" : stopped(v) ? "Stopped" : vmStateLabel(v, true);
    const part = groups.get(label) || { label, count: 0, title: "", tone: vmTones[label]?.[0] || "warn" };
    part.count++;
    part.title = `${part.count} ${label.toLowerCase()}`;
    groups.set(label, part);
  }
  // Paused, asleep and other states sit between running and stopped.
  return [...groups.values()].sort((a, b) => (vmTones[a.label]?.[1] ?? 1) - (vmTones[b.label]?.[1] ?? 1));
}

// A KPI trend compares the first and last known values of its window, once there are two.
export function trend(values: Point[]) {
  const known = values.filter((v): v is number => v != null);
  if (known.length < 2) return null;
  const change = Math.round(known[known.length - 1] - known[0]);
  return { first: known[0], now: known[known.length - 1], change: change > 0 ? `▲ ${change}%` : change < 0 ? `▼ ${-change}%` : "" };
}

// Current conditions that need an operator, each naming what it is about and linking to it: the
// Overview's Needs attention list and the top of the Events screen. Computed from the hosts'
// current reports on every render; nothing is stored.
export function attentionItems(data: Data, hosts: Host[], vms: VM[], storage: Data["storage"]): Attention[] {
  const hostName = (id: string) => data.hosts.find(h => h.id === id)?.hostname || id;
  const hostFresh = (id: string) => hosts.some(h => h.id === id && h.connection === "fresh");
  const known = (vm: VM) => vm.freshness === "fresh" && hostFresh(vm.host_id);
  const ops = data.operations.filter(o => hosts.some(h => h.id === o.host_id));
  const attention: Attention[] = [];
  for (const h of hosts) {
    const health = hostHealth(h);
    if (health.status === "healthy") continue;
    const onHost = vms.filter(v => v.host_id === h.id).length;
    attention.push(h.connection === "fresh"
      ? { tone: badgeTone(health.status), title: `${h.hostname} ${statusLabel(health.status).toLowerCase()}`, detail: health.issues.map(i => i.reason).join(" "), href: `#/hosts/${h.id}` }
      : { tone: h.revoked ? "bad" : "warn", title: `${h.hostname} not reporting`, detail: `${onHost} ${onHost === 1 ? "VM" : "VMs"} shown as Unknown${h.last_seen ? ` · last seen ${relativeTime(h.last_seen)}` : ""}`, href: `#/hosts/${h.id}` });
  }
  for (const o of ops.filter(o => o.status === "failed" || o.status === "needs_attention").slice(0, 5))
    attention.push({ tone: o.status === "failed" ? "bad" : "warn", title: `${operationLabel(o.kind)} on ${hostName(o.host_id)} ${o.status === "failed" ? "failed" : "needs attention"}`, detail: `${relativeTime(o.updated_at)} · ${o.error || "No reason reported."}`, href: "#/tasks" });
  for (const s of storage) {
    const state = s.status === "succeeded" ? storageAccess(s) : s.status;
    const pool = s.access?.zfs_pool?.state;
    if (badgeTone(state) === "ok" && !s.access?.error && (!pool || pool === "ONLINE")) continue;
    attention.push({ tone: badgeTone(state) === "bad" ? "bad" : "warn", title: `Storage ${s.name} ${pool && pool !== "ONLINE" ? pool.toLowerCase() : statusLabel(state).toLowerCase()}`, detail: s.access?.error || s.error || `Reported by ${hostName(s.host_id)}.`, href: "#/storage" });
  }
  for (const v of vms) if (known(v) && v.state === "running" && (v.usage?.cpu_percent ?? 0) > 80)
    attention.push({ tone: "warn", title: `${v.name} at ${Math.round(v.usage!.cpu_percent!)}% CPU`, detail: `On ${hostName(v.host_id)}${v.usage?.observed_at ? ` · reported ${relativeTime(v.usage.observed_at)}` : ""}`, href: `#/vms/${v.id}` });

  return attention;
}
