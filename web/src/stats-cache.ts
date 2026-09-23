// SPDX-License-Identifier: AGPL-3.0-only
// Shared host stats history so Overview and Hosts do not fan out duplicate polls for the same
// host and range while both are open in turn.
import { statsHistory, type StatsHistory } from "./stats";
import type { API } from "./types";

type Entry = { history: StatsHistory; at: number };
const cache = new Map<string, Entry>();
const keyOf = (id: string, range: string) => `${id}|${range}`;

export function peekHostStats(id: string, range: string, maxAgeMS = 30_000): StatsHistory | null {
  const hit = cache.get(keyOf(id, range));
  if (!hit) return null;
  return Date.now() - hit.at <= maxAgeMS ? hit.history : null;
}

export function putHostStats(id: string, range: string, history: StatsHistory) {
  cache.set(keyOf(id, range), { history, at: Date.now() });
}

export function pruneHostStats(onlineIDs: Iterable<string>, range: string) {
  const keep = new Set([...onlineIDs].map(id => keyOf(id, range)));
  for (const key of cache.keys()) {
    if (key.endsWith(`|${range}`) && !keep.has(key)) cache.delete(key);
  }
}

// One wave of GETs for the given hosts and range; results are cached and returned as a map.
export async function loadHostStats(api: API, ids: string[], range: string): Promise<Record<string, StatsHistory>> {
  const unique = [...new Set(ids.filter(Boolean))];
  const rows = await Promise.all(unique.map(id => {
    const cached = peekHostStats(id, range, 5_000);
    if (cached) return Promise.resolve([id, cached] as const);
    return api<StatsHistory>(`hosts/${encodeURIComponent(id)}/stats?range=${range}`)
      .then(statsHistory)
      .then(history => {
        putHostStats(id, range, history);
        return [id, history] as const;
      }, () => null);
  }));
  const next: Record<string, StatsHistory> = {};
  for (const row of rows) if (row) next[row[0]] = row[1];
  pruneHostStats(Object.keys(next), range);
  return next;
}
