// SPDX-License-Identifier: AGPL-3.0-only
// Operator-facing names for the identifiers Core and the Controller report. Unknown values fall
// back to a readable form of the raw string, so a newer Core never renders as a blank label;
// the raw identifier stays available beside the label where an operator may need to grep it.
export const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const humanize = (s: string) => capitalize(s.replaceAll(/[-_.]+/g, " ").trim());

const operations: Record<string, string> = {
  "refresh-inventory": "Refresh inventory", "vm-create": "Create VM", "vm-update": "Edit VM", "vm-delete": "Delete VM",
  "vm-power": "VM power", "vm-start": "Start VM", "start-vm": "Start VM", "vm-media": "Change VM media", "vm-release-retained": "Release retained original",
  "iso-import": "Import ISO", "media-remove": "Remove ISO",
  "storage-register": "Register storage", "storage-update": "Update storage", "storage-recheck": "Recheck storage",
  "storage-detach": "Detach storage", "storage-zfs-provision": "Provision ZFS storage", "storage-zfs-replace": "Replace ZFS disk",
  "storage-credential-retire": "Retire storage credential",
  "network-register": "Register network", "network-update": "Update network", "network-remove": "Remove network",
  "management-network": "Change management network", "host-system-disk-replace": "Replace system disk",
  "pool-join": "Join pool", "pool-maintenance": "Maintenance mode", "pool-transfer": "Change pool master",
  "pool-promote": "Promote pool master", "pool-fence-profile": "Fence profile", "pool-host-fencing": "Host fencing",
  "pool-bmc-provisioning": "BMC provisioning", enroll: "Enroll host",
  "backup-repository-register": "Register backup repository", "backup-repository-test": "Test backup repository", "backup-repository-forget": "Forget backup repository", "backup-orphan-cleanup": "Delete orphaned backups",
  "backup-policy-upsert": "Save backup job", "backup-policy-pause": "Pause or resume backup job", "backup-policy-delete": "Delete backup job", "backup-policy-run-now": "Run backup job now",
  "backup-now": "Back up now", "backup-restore-as-new": "Restore as new",
  "backup-restore-replace": "Restore replace", "backup-test-restore": "Test restore",
  "backup-protect": "Protect recovery point", "backup-catalog-import": "Import recovery points",
  "backup-catalog-rebuild": "Rebuild backup catalog", "backup-test-verify": "Record restore test",
  "backup-test-cleanup": "Clean up test restore",
};
export function operationLabel(kind: string): string { return operations[kind] ?? humanize(kind); }

const statuses: Record<string, string> = { needs_attention: "Needs attention", in_sync: "In sync", awaiting_fencing: "Awaiting fencing", will_not_converge: "Will not converge", refuses_join: "Refuses join", no_peer: "No peer", not_applicable: "Not applicable", not_ready: "Not ready" };
export function statusLabel(value: string): string {
  if (!value) return "Unknown";
  return statuses[value] ?? humanize(value);
}

const checks: Record<string, string> = { libvirt: "libvirt", kvm: "KVM", "management-network": "Management network", "pool-storage-reconciliation": "Pool storage reconciliation", "storage-credentials": "Storage credentials", system: "System", web: "Web service", guest: "Guest agent", "zfs-pool": "ZFS pool" };
export function checkLabel(name: string): string { return checks[name] ?? humanize(name); }

const audits: Record<string, string> = {
  "site.create": "Site created", "host.add": "Host added", "host.revoke": "Host access revoked", "console.open": "VM console opened",
  "host.console.open": "Host console opened", "update.apply": "Update applied", "enrollment-token.create": "Enrollment token created",
  "appliance.adopt": "Appliance adopted", "appliance.bootstrap": "Appliance bootstrapped", "media.discard": "ISO discarded",
  "operation.create": "Task requested", "pool.promote": "Pool master promoted", "pool.host-fencing": "Host fencing changed",
  "pool.fence-profile": "Fence profile changed", "pool.bmc-provisioning": "BMC provisioning changed",
};
export function auditLabel(action: string): string { return audits[action] ?? humanize(action); }

// Relative for the last week, an ISO date after that; the absolute value belongs in a title attribute.
export function relativeTime(iso: string, now = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return iso;
  const s = Math.round((now - t) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 7 * 86400) return `${Math.round(s / 86400)} d ago`;
  return new Date(t).toISOString().slice(0, 10);
}
