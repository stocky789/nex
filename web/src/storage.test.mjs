// SPDX-License-Identifier: AGPL-3.0-only
import { transformSync } from "esbuild";
import { readFileSync, writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import assert from "node:assert/strict";

const src = readFileSync(new URL("./storage.tsx", import.meta.url), "utf8");
const { code } = transformSync(src, { loader: "tsx", format: "esm" });
const dir = mkdtempSync(join(tmpdir(), "storage-check-"));
const file = join(dir, "storage.mjs");
const stubbed = code
  .replace('from "react"', 'from "data:text/javascript,export const useEffect=()=>{};export const useRef=()=>({});export const useState=()=>[undefined,()=>{}];"')
  .replace('from "./zfs"', 'from "data:text/javascript,export const ZFSProvisionWizard=()=>null;"');
writeFileSync(file, stubbed);
const { storageUsable, storageSource, storageChecked, storageLifecycleReady, storageOwnerFresh, storageObservationFresh, clusterStatusView, clusterSetupAvailable, registrationDiscardable, discardConfirmText } = await import(`file://${file}`);

// D1: only a registration the pool master refused is a discardable Controller request record.
// A queued one has no id to address and is still the host's business; a succeeded one is a
// repository the host holds and is removed with detach instead.
assert.equal(registrationDiscardable({ id: "pending-storage-op-1", status: "failed" }), true);
assert.equal(registrationDiscardable({ id: "", status: "failed" }), false);
assert.equal(registrationDiscardable({ id: "", status: "queued" }), false);
assert.equal(registrationDiscardable({ id: "repo-1", status: "queued" }), false);
assert.equal(registrationDiscardable({ id: "repo-1", status: "dispatched" }), false);
assert.equal(registrationDiscardable({ id: "repo-1", status: "succeeded" }), false);
// The confirmation says what it does and does not do: no host is asked for anything.
assert.match(discardConfirmText("storage", "lab-nfs-fault"), /^Discard the failed storage registration lab-nfs-fault\?/);
assert.match(discardConfirmText("network", "guests"), /removes only Nex Controller's record of the failed request and asks no host to do anything/);
// The discard takes no body and addresses the row by the id the list answered.
assert.match(src, /await api\(`storage\/\$\{encodeURIComponent\(s\.id\)\}`, "DELETE"\);/);
assert.match(src, /catch \(e\) \{ setError\(\(e as Error\)\.message\); \}/);
// A failed row offers the discard instead of the repository actions, never beside them.
assert.match(src, /registrationDiscardable\(s\) \? <button type="button"[\s\S]*?>Discard failed registration<\/button> : <>[\s\S]*?>Recheck<\/button>[\s\S]*?>Detach<\/button><\/>\}/);

// One freshness rule for every Core observation: undated, unparseable, older than 30 s or dated
// in the future is not current access.
assert.equal(storageObservationFresh(new Date().toISOString()), true);
assert.equal(storageObservationFresh(new Date(Date.now() - 31_000).toISOString()), false);
assert.equal(storageObservationFresh(new Date(Date.now() + 5_000).toISOString()), false);
assert.equal(storageObservationFresh(undefined), false);
assert.equal(storageObservationFresh("not-a-date"), false);

assert.equal(clusterStatusView(true, "", undefined, 0), "checking");
assert.equal(clusterStatusView(false, "pool master unavailable", undefined, 0), "unavailable");
assert.equal(clusterStatusView(false, "", "succeeded", 0), "empty");
assert.equal(clusterStatusView(false, "", "succeeded", 1), "configured");
assert.equal(clusterStatusView(false, "", "stale", 0), "unavailable");
assert.equal(clusterSetupAvailable("configured"), true);
assert.equal(clusterSetupAvailable("empty"), true);
assert.equal(clusterSetupAvailable("checking"), false);
assert.equal(clusterSetupAvailable("unavailable"), false);

const ready = { id: "nfs-1", host_id: "host-1", status: "succeeded", access: { status: "healthy", readable: true, writable: true }, capabilities: { vm_disk: true }, source: { nfs: { server: "nfs.example.test", export: "/vms" } } };
const hosts = [{ id: "host-1", connection: "fresh", revoked: false, capabilities: ["managed-vm-v1"] }];
assert.equal(storageOwnerFresh(ready, hosts), true);
assert.equal(storageOwnerFresh(ready, [{ ...hosts[0], connection: "stale" }]), false);
assert.equal(storageOwnerFresh(ready, [{ ...hosts[0], revoked: true }]), false);
assert.equal(storageOwnerFresh(ready, [{ ...hosts[0], capabilities: [] }]), false);
assert.equal(storageUsable(ready, "host-1"), true);
assert.equal(storageUsable({ ...ready, access: { ...ready.access, writable: false } }, "host-1"), false);
assert.equal(storageUsable({ ...ready, capabilities: {} }, "host-1"), false);
const zfsLocal = { ...ready, kind: "zfs", zfs_provisioned: true, zfs_scope: "pool", capabilities: { vm_disk: true, shared: false }, zfs_host_access: [{ host_id: "host-1", readable: false, writable: false, observed_at: new Date().toISOString() }, { host_id: "member", readable: true, writable: true, available_bytes: 7, observed_at: new Date().toISOString() }] };
assert.equal(storageUsable(zfsLocal, "host-1"), false, "master can be unconfigured while another pool-local ZFS host is usable");
assert.equal(storageUsable(zfsLocal, "member"), true);
assert.equal(storageUsable({ ...zfsLocal, zfs_host_access: [{ ...zfsLocal.zfs_host_access[1], observed_at: new Date(Date.now() - 31_000).toISOString() }] }, "member"), false, "stale member access is not placement eligible");
assert.equal(storageUsable({ ...zfsLocal, zfs_scope: "host", host_id: "host-1" }, "member"), true, "a host-scoped canonical ZFS repository follows Core's selected-member access");
const member = { repository_id: "nfs-1", host_id: "host-1", generation: 2, identity: "nfs-id", readable: true, writable: true, qemu_writable: true, locking: true, observed_at: new Date().toISOString() };
assert.equal(storageUsable({ ...ready, kind: "nfs", host_id: "master", generation: 2, capabilities: { vm_disk: true, eligible_host_ids: ["master"] }, member_access: [member] }, "host-1"), true);
assert.equal(storageUsable({ ...ready, kind: "nfs", host_id: "master", capabilities: { vm_disk: true, eligible_host_ids: ["other-master"] } }, "master"), false);
assert.equal(storageUsable({ ...ready, kind: "nfs", host_id: "master", generation: 2, member_access: [{ ...member, locking: false }] }, "host-1"), false);
assert.equal(storageUsable({ ...ready, kind: "nfs", host_id: "master", generation: 2, member_access: [{ ...member, observed_at: new Date(Date.now() - 31_000).toISOString() }] }, "host-1"), false);
const clustered = { ...ready, id: "gfs2-1", kind: "iscsi", host_id: "master", generation: 2, source: { iscsi: { portal: "storage.example.test:3260", target_iqn: "iqn.2026-01.example.test:gfs2", lun: 0, wwid: "3600gfs2", filesystem_uuid: "00000000-0000-0000-0000-000000000000", exclusive_target: false, mode: "clustered_gfs2", cluster_id: "cluster-qa", lock_table: "cluster-qa:gfs2", eligible_host_ids: ["master", "host-1", "host-2"] } }, member_access: [{ ...member, repository_id: "gfs2-1" }] };
assert.equal(storageUsable(clustered, "host-1"), true);
assert.equal(storageUsable(clustered, "host-3"), false);
assert.equal(storageUsable({ ...clustered, member_access: [{ ...clustered.member_access[0], error: "Core access check failed" }] }, "host-1"), false);
assert.equal(storageUsable({ ...clustered, member_access: [{ ...clustered.member_access[0], observed_at: new Date(Date.now() - 31_000).toISOString() }] }, "host-1"), false);
assert.equal(storageUsable({ ...clustered, source: { iscsi: { ...clustered.source.iscsi, mode: "exclusive" } } }, "host-1"), false);
assert.match(storageSource(ready), /nfs\.example\.test/);
assert.equal(storageSource({ ...ready, source: { smb: { server: "files.example.test", share: "vms" } } }), "//files.example.test/vms");
assert.equal(storageSource({ ...ready, source: { zfs: { pool: "tank", dataset: "vms", mount_path: "/tank/vms" } } }), "tank/vms");
assert.equal(storageSource({ ...ready, source: { zfs: { pool: "tank", dataset: "tank/vms", mount_path: "/tank/vms" } } }), "tank/vms");
assert.equal(storageSource({ ...ready, source: { zfs: { pool: "tank", dataset: "tank", mount_path: "/tank" } } }), "tank");
assert.equal(storageSource({ ...ready, source: { iscsi: { target_iqn: "iqn.2026-01.example.test:vms", lun: 2, portal: "storage.example.test:3260", wwid: "wwid", filesystem_uuid: "uuid", exclusive_target: true } } }), "iqn.2026-01.example.test:vms · LUN 2");
assert.equal(storageSource({ ...ready, source: undefined, path: "" }), "Source unavailable");
assert.equal(storageChecked("0001-01-01T00:00:00Z"), "Not checked");
assert.equal(storageChecked("not-a-date"), "Not checked");
assert.match(storageChecked("2026-09-11T00:00:00Z"), /^Checked /);
assert.equal(storageLifecycleReady({ ...ready, kind: "nfs", generation: 0 }), false);
assert.equal(storageLifecycleReady({ ...ready, kind: "nfs", generation: 1 }), true);
assert.equal(storageLifecycleReady({ ...ready, kind: "nfs", generation: 1, status: "failed" }), false);
assert.equal(storageLifecycleReady({ ...ready, kind: undefined }), true);
console.log("storage.test.mjs: ok");
