// SPDX-License-Identifier: AGPL-3.0-only
// Hand types matching internal/protocol/backup.go JSON tags.
// Controller paths: /api/v1/pools/{id}/backup-repositories|catalog|backup-restore
// and POST /api/v1/vms/{id}/backup (request_id; create body is flat name/kind/source).

export type BackupNFSSource = { server: string; export: string; directory?: string };

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
  generation: number;
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
  kind: "nfs";
  source: BackupNFSSource;
  generation?: number;
  mount_path?: string;
  access?: BackupStorageAccess;
  member_access?: BackupMemberAccess[];
  created_at?: string;
  updated_at?: string;
};

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

export const nfsTarget = (r: BackupRepository) => {
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
