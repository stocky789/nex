// SPDX-License-Identifier: AGPL-3.0-only
// Shared inventory and session types for the Controller console. Kept out of main.tsx so
// ui.tsx and screen modules can import them without a cycle through the app root.
import type { Media, Network, Storage, VM } from "./managed";

export type Site = { id: string; name: string };
export type Pool = Site & { site_id: string; master_host_id: string };
export type Check = {
  name: string;
  status: string;
  freshness: string;
  observed_at: string;
  last_success?: string;
  error?: string;
};
export type ISODownloadProgress = { operation_id: string; received_bytes: number; total_bytes?: number };
export type Inventory = {
  observed_at: string;
  hostname: string;
  cpu_model: string;
  cpu_count: number;
  memory_bytes: number;
  virtualization: string[];
  os_version: string;
  agent_version: string;
  nics: { name: string; mac: string; kind?: string; master?: string; parent?: string; addresses: string[]; state: string; speed_mbps?: number }[];
  uptime_seconds?: number;
  disks: { name: string; size_bytes: number }[];
  checks: Check[];
  managed?: { iso_downloads?: ISODownloadProgress[] | null } | null;
};
export type Host = {
  id: string;
  pool_id: string;
  hostname: string;
  revoked: boolean;
  capabilities?: string[];
  connection: string;
  last_seen: string | null;
  inventory: Inventory | null;
};
export type Operation = {
  id: string;
  host_id: string;
  kind: string;
  request_id: string;
  status: string;
  error?: string;
  created_at: string;
  updated_at: string;
  actor: string;
};
export type Audit = {
  id: number;
  actor: string;
  action: string;
  resource_id: string;
  created_at: string;
  detail?: string;
};
export type Data = {
  sites: Site[];
  pools: Pool[];
  hosts: Host[];
  operations: Operation[];
  audit: Audit[];
  vms: VM[];
  storage: Storage[];
  networks: Network[];
  media: Media[];
};
export type Session = { username: string; csrf_token: string };
export type API = <T>(path: string, method?: string, body?: unknown, progress?: (loaded: number, total: number) => void, register?: (xhr: XMLHttpRequest) => void, signal?: AbortSignal) => Promise<T>;
