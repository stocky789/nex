// SPDX-License-Identifier: AGPL-3.0-only
// Hand types matching internal/protocol/backup.go JSON tags.
// Controller paths: /api/v1/pools/{id}/backup-repositories|catalog|backup-restore
// and POST /api/v1/vms/{id}/backup (request_id; create body is flat name/kind/source).
import type { API } from "./types";

export type BackupNFSSource = { server: string; export: string; directory?: string };
export type BackupKind = "nfs" | "smb3" | "s3" | "sftp";

export const backupKinds: BackupKind[] = ["nfs", "smb3", "s3", "sftp"];

export type BackupStorageAccess = {
  status?: string;
  readable?: boolean;
  writable?: boolean;
  total_bytes?: number;
  available_bytes?: number;
  observed_at?: string;
  error?: string;
};

export type BackupMemberAccess = {
  repository_id: string;
  host_id: string;
  identity: string;
  readable: boolean;
  writable: boolean;
  qemu_writable: boolean;
  locking: boolean;
  observed_at?: string;
  error?: string;
};

export type BackupRepository = {
  id: string;
  pool_id: string;
  name: string;
  kind: BackupKind;
  source: BackupNFSSource;
  smb_source?: { server: string; share: string; directory?: string };
  s3_source?: { endpoint: string; bucket: string; prefix?: string; region?: string };
  sftp_source?: { host: string; port?: number; root: string; host_key_sha256: string };
  mount_path?: string;
  access?: BackupStorageAccess;
  member_access?: BackupMemberAccess[];
  health?: { status?: "online" | "offline" | "unknown"; observed_at?: string; error?: string };
  last_health_transition?: { status: "offline" | "recovered"; at: string; error?: string };
  created_at?: string;
  updated_at?: string;
};

// Core judges freshness on its own clock and the Controller masks a stale master,
// so the browser clock is never compared with observed_at.
export function repositoryHealth(repo: BackupRepository): "online" | "offline" | "unknown" {
  return repo.health?.status === "online" || repo.health?.status === "offline" ? repo.health.status : "unknown";
}

export type BackupRecoveryPoint = {
  id: string;
  repository_id: string;
  pool_id: string;
  source_vm_id: string;
  source_vm_name?: string;
  captured_at: string;
  consistency: string;
  manifest_sha256: string;
  protected?: boolean;
  last_test_result?: string;
  last_tested_at?: string;
  last_test_vm_id?: string;
};

export type BackupRun = {
  operation_id: string;
  kind: string;
  recovery_point_id?: string;
  policy_id?: string;
  vm_id?: string;
  repository_id: string;
  new_vm_id?: string;
  host_id?: string;
  status: string;
  error?: string;
  updated_at: string;
};

export type BackupPage<T> = { items: T[]; next_cursor?: string; generation: string; total: number };
export type BackupCatalogPage = BackupPage<BackupRecoveryPoint> & {
  untested_count: number;
  last_restore_test?: { result: string; tested_at: string };
  backup_days_14: string[];
};
export type BackupRunPage = BackupPage<BackupRun> & {
  attention_count: number;
  last_backup_run?: { status: string; updated_at: string };
};

// maxBackupPages bounds every walk of Core pages (64 items each); a longer
// history fails loudly instead of silently showing a truncated, misleading view.
export const maxBackupPages = 100;

type PageRequest<T> = (cursor: string, generation: string) => Promise<BackupPage<T>>;

// walkBackupPages appends pages after `items` while Core reports a cursor and
// returns the cursor that remains. stop may end the walk early once the caller
// has what it needs.
async function walkBackupPages<T>(first: BackupPage<T>, items: T[], requestPage: PageRequest<T>, stop?: (page: BackupPage<T>) => boolean): Promise<string | undefined> {
  const seen = new Set<string>();
  let page = first;
  for (let pages = 1; page.next_cursor && !stop?.(page); pages++) {
    const cursor = page.next_cursor;
    if (seen.has(cursor)) throw new Error("Backup inventory paging repeated a cursor.");
    if (pages >= maxBackupPages) throw new Error("Backup inventory is too large to load in full.");
    seen.add(cursor);
    page = await requestPage(cursor, first.generation);
    if (page.generation !== first.generation) throw new Error("Backup inventory changed during loading.");
    if (page.items.length === 0) throw new Error("Backup inventory paging returned an empty page.");
    items.push(...page.items);
    if (items.length > first.total) throw new Error("Backup inventory paging exceeded its reported total.");
  }
  return page.next_cursor;
}

export const pageURL = (base: string, cursor: string, generation: string) =>
  `${base}${base.includes("?") ? "&" : "?"}cursor=${encodeURIComponent(cursor)}&generation=${encodeURIComponent(generation)}`;

export async function collectBackupPageItems<T>(first: BackupPage<T>, requestPage: PageRequest<T>): Promise<T[]> {
  const items = [...first.items];
  await walkBackupPages(first, items, requestPage);
  return items;
}

// refreshLoadedPages re-reads the span the operator has loaded ("Load more")
// so mutable point fields (protected, last test) are never left stale. Core's
// generation covers only which records exist, not their attributes. If the
// generation moves mid-walk, the fresh first page wins and the walk restarts.
export async function refreshLoadedPages<T, P extends BackupPage<T>>(first: P, loaded: number, requestPage: PageRequest<T>): Promise<P> {
  const items = [...first.items];
  try {
    const next_cursor = await walkBackupPages(first, items, requestPage, () => items.length >= loaded);
    return { ...first, items, next_cursor };
  } catch (err) {
    // Core answers 409 to a cursor whose generation moved; either way the fresh first page is the truth.
    if ((err as { status?: number }).status === 409 || err instanceof Error && /changed during loading/.test(err.message)) return first;
    throw err;
  }
}

const settledRunStatuses = ["succeeded", "failed", "missed", "skipped_overlap"];

// refreshRunHistory re-reads the newest pages of the run history and keeps the
// cached remainder: settled runs never change, so the walk stops at the first
// page that holds only known settled runs and no cached unsettled run is left
// beyond it. A total that disagrees with the merged list (a forgotten
// repository removed runs) falls back to the full walk.
export async function refreshRunHistory(cached: BackupRun[] | null, first: BackupRunPage, requestPage: PageRequest<BackupRun>): Promise<BackupRun[]> {
  if (!cached) return collectBackupPageItems(first, requestPage);
  const known = new Map(cached.map(run => [run.operation_id, run]));
  const fresh = [...first.items];
  const volatile = new Set(cached.filter(run => !settledRunStatuses.includes(run.status)).map(run => run.operation_id));
  const done = (page: BackupPage<BackupRun>) => page.items.every(run => settledRunStatuses.includes(known.get(run.operation_id)?.status ?? "")) &&
    [...volatile].every(id => fresh.some(run => run.operation_id === id));
  await walkBackupPages(first, fresh, requestPage, done);
  const seen = new Set(fresh.map(run => run.operation_id));
  const merged = [...fresh, ...cached.filter(run => !seen.has(run.operation_id))];
  return merged.length === first.total ? merged : collectBackupPageItems(first, requestPage);
}

export type RecoveryPointFilter = { vm_id?: string; repository_id?: string; search?: string };

// loadRecoveryPoints returns every recovery point matching filter, walking Core pages.
export async function loadRecoveryPoints(api: API, poolID: string, filter: RecoveryPointFilter, signal?: AbortSignal): Promise<BackupRecoveryPoint[]> {
  const base = `pools/${encodeURIComponent(poolID)}/backup-catalog?${new URLSearchParams(filter)}`;
  const response = await api<BackupCatalogPage>(base, "GET", undefined, undefined, undefined, signal);
  return collectBackupPageItems(response, (cursor, generation) => api<BackupCatalogPage>(pageURL(base, cursor, generation), "GET", undefined, undefined, undefined, signal));
}

export function appendBackupPage<T>(current: BackupPage<T> | null, next: BackupPage<T>, key: (item: T) => string): BackupPage<T> {
  if (!current || current.generation !== next.generation) return next;
  const ids = new Set(current.items.map(key));
  return { ...next, items: [...current.items, ...next.items.filter(item => !ids.has(key(item)))] };
}

export type BackupOpReply = { operation?: { id?: string }; id?: string };

export const backupOpID = (reply: BackupOpReply) => reply.operation?.id || reply.id || "";

export const rid = (prefix: string) => {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return prefix + Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("").slice(0, 20);
};

export const repositoryTarget = (r: BackupRepository) => {
  if (r.kind === "smb3" && r.smb_source) return `//${r.smb_source.server}/${r.smb_source.share}${r.smb_source.directory ? `/${r.smb_source.directory}` : ""}`;
  if (r.kind === "s3" && r.s3_source) return `${r.s3_source.endpoint}/${r.s3_source.bucket}${r.s3_source.prefix ? `/${r.s3_source.prefix}` : ""}`;
  if (r.kind === "sftp" && r.sftp_source) return `${r.sftp_source.host}:${r.sftp_source.port || 22}${r.sftp_source.root}`;
  const s = r.source;
  if (!s?.server || !s.export) return "—";
  return `${s.server}:${s.export}${s.directory ? `/${s.directory}` : ""}`;
};

export function lastSuccessfulBackup(points: BackupRecoveryPoint[] | null | undefined, vmID: string): BackupRecoveryPoint | null {
  if (!points?.length) return null;
  return points
    .filter(p => p.source_vm_id === vmID)
    .slice()
    .sort((a, b) => (b.captured_at || "").localeCompare(a.captured_at || ""))[0] || null;
}
