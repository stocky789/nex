// SPDX-License-Identifier: AGPL-3.0-only
// The bottom task strip: counts for its collapsed line, the failures to announce, and the
// per-browser open/closed preference. localStorage is a convenience: any failure to read or
// write it leaves the default in place.
const running = new Set(["pending", "dispatched"]);
const failed = new Set(["failed", "needs_attention"]);

export function taskCounts(ops: { status: string }[]): { running: number; failed: number } {
  return { running: ops.filter(o => running.has(o.status)).length, failed: ops.filter(o => failed.has(o.status)).length };
}

export function newFailures<T extends { id: string; status: string }>(previous: T[], next: T[]): T[] {
  if (!previous.length) return [];
  const before = new Map(previous.map(o => [o.id, o.status]));
  return next.filter(o => failed.has(o.status) && before.get(o.id) !== o.status);
}

export function readPreference(key: string, fallback: boolean): boolean {
  try { const v = localStorage.getItem(key); return v === null ? fallback : v === "1"; } catch { return fallback; }
}
export function writePreference(key: string, value: boolean): void {
  try { localStorage.setItem(key, value ? "1" : "0"); } catch { /* preference not kept */ }
}
