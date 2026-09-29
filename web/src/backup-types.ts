// SPDX-License-Identifier: AGPL-3.0-only
// Hand types matching internal/protocol/backup.go JSON tags.
// Controller paths: /api/v1/pools/{id}/backup-repositories|catalog|backup-restore
// and POST /api/v1/vms/{id}/backup (request_id; create body is flat name/kind/source).
import type { API } from "./types";

export type BackupNFSSource = { server: string; export: string; directory?: string };
export type BackupKind = "nfs" | "smb3" | "s3" | "sftp";

export function availableBackupKinds(connection?: string, capabilities: string[] = []): BackupKind[] {
  const kinds: BackupKind[] = ["nfs"];
  if (connection === "fresh") for (const kind of ["smb3", "s3", "sftp"] as BackupKind[]) {
    if (capabilities.includes(`backup-destination-${kind}-v1`)) kinds.push(kind);
  }
  return kinds;
}

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

export function refreshBackupPage<T extends { items: unknown[]; generation: string; next_cursor?: string }>(current: T | null, next: T, sameScope: boolean): T {
  return sameScope && next.generation && current?.generation === next.generation && current.items.length > next.items.length
    ? { ...next, items: current.items, next_cursor: current.next_cursor } : next;
}

export async function collectBackupPageItems<T>(first: BackupPage<T>, requestPage: (cursor: string, generation: string) => Promise<BackupPage<T>>): Promise<T[]> {
  const items = [...first.items];
  let cursor = first.next_cursor;
  while (cursor) {
    const page = await requestPage(cursor, first.generation);
    if (page.generation !== first.generation) throw new Error("Backup inventory changed during loading.");
    items.push(...page.items);
    cursor = page.next_cursor;
  }
  return items;
}

export type RecoveryPointFilter = { vm_id?: string; repository_id?: string; search?: string };

// loadRecoveryPoints returns every recovery point matching filter, walking Core pages.
export async function loadRecoveryPoints(api: API, poolID: string, filter: RecoveryPointFilter, signal?: AbortSignal): Promise<BackupRecoveryPoint[]> {
  const base = `pools/${encodeURIComponent(poolID)}/backup-catalog?${new URLSearchParams(filter)}`;
  const response = await api<BackupCatalogPage>(base, "GET", undefined, undefined, undefined, signal);
  return collectBackupPageItems(response, (cursor, generation) =>
    api<BackupCatalogPage>(`${base}&cursor=${encodeURIComponent(cursor)}&generation=${encodeURIComponent(generation)}`, "GET", undefined, undefined, undefined, signal));
}

export function appendBackupPage<T>(current: BackupPage<T> | null, next: BackupPage<T>, key: (item: T) => string): BackupPage<T> {
  if (!current || current.generation !== next.generation) return next;
  const ids = new Set(current.items.map(key));
  return { ...next, items: [...current.items, ...next.items.filter(item => !ids.has(key(item)))] };
}

export type BackupOpReply = { operation?: { id?: string }; id?: string };

export const backupOpID = (reply: BackupOpReply) => reply.operation?.id || reply.id || "";

export function stableBody<T>(pending: Record<string, T>, key: string, body: T): T {
  return pending[key] ??= body;
}

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
