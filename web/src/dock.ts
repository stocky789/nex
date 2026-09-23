// SPDX-License-Identifier: AGPL-3.0-only
// Task activity for the top bar: running and failed counts, the failures to announce, and
// per-browser preferences (theme, scope, last-seen event time). localStorage is a convenience:
// any failure to read or write it leaves the default in place.
// Every status an operation holds before its outcome: the Controller creates it queued, hands it
// to the agent (dispatched) and the host reports it pending while it runs.
export const unfinished = new Set(["queued", "dispatched", "pending", "running"]);
const failed = new Set(["failed", "needs_attention"]);

export function taskCounts(ops: { status: string }[]): { running: number; failed: number } {
  return { running: ops.filter(o => unfinished.has(o.status)).length, failed: ops.filter(o => failed.has(o.status)).length };
}

export function newFailures<T extends { id: string; status: string }>(previous: T[], next: T[]): T[] {
  if (!previous.length) return [];
  const before = new Map(previous.map(o => [o.id, o.status]));
  return next.filter(o => failed.has(o.status) && before.get(o.id) !== o.status);
}

export function readPreference(key: string): string {
  try { return localStorage.getItem(key) ?? ""; } catch { return ""; }
}
export function writePreference(key: string, value: string): void {
  try { localStorage.setItem(key, value); } catch { /* preference not kept */ }
}
