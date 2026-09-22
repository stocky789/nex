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
  .replace('from "react"', 'from "data:text/javascript,export const Fragment=()=>null;export const useEffect=()=>{};export const useRef=()=>({});export const useState=()=>[undefined,()=>{}];"')
  .replace('from "./zfs"', 'from "data:text/javascript,export const ZFSProvisionWizard=()=>null;export const ZFSReplaceDialog=()=>null;export const zfsFailedDevices=(pool)=>(pool?.devices||[]).filter(d=>d.state!==\'ONLINE\').map(d=>{const item={device:d.path||d.name,state:d.state};if(d.parent)item.parent=d.parent;return item;});export const zfsReplaceableDevices=(pool)=>zfsFailedDevices(pool).filter(d=>d.parent!==\'replacing\'&&d.parent!==\'spare\');"');
writeFileSync(file, stubbed);
const { zfsPoolSummary, zfsScanProgress, zfsSizeBytes, storageServesData, storageUsable, storageSource, storageChecked, storageLifecycleReady, storageOwnerFresh, storageObservationFresh, clusterStatusView, clusterSetupAvailable, registrationDiscardable, discardConfirmText } = await import(`file://${file}`);

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
// A mirror that lost one member still serves its VMs, and the page names the failed disk.
const degradedPool = { state: "DEGRADED", status: "One or more devices has been taken offline by the administrator.", devices: [{ name: "wwn-0x5000c500a", path: "/dev/disk/by-id/wwn-0x5000c500a", state: "OFFLINE" }, { name: "wwn-0x5000c500b", path: "/dev/disk/by-id/wwn-0x5000c500b", state: "ONLINE" }] };
assert.equal(storageUsable({ ...zfsLocal, zfs_host_access: [{ ...zfsLocal.zfs_host_access[1], pool: degradedPool }] }, "member"), true, "a degraded ZFS pool stays placement eligible");
assert.deepEqual(zfsPoolSummary(degradedPool), { text: "Pool degraded · /dev/disk/by-id/wwn-0x5000c500a offline", attention: true });
// A running scan is shown with the percentage an operator wants, read back from the sizes ZFS
// already formatted; a scan whose sizes cannot be read still names what it is doing.
assert.deepEqual(zfsPoolSummary({ ...degradedPool, state: "ONLINE", status: undefined, devices: [{ ...degradedPool.devices[0], state: "ONLINE" }, degradedPool.devices[1]], scan: { function: "RESILVER", state: "SCANNING", examined: "1.46G", to_examine: "4.89G" } }), { text: "Pool online · resilver in progress · 29% (1.46G of 4.89G)", attention: false });
assert.equal(zfsScanProgress({ function: "RESILVER", state: "SCANNING", examined: "1.46G", issued: "2.44G", to_examine: "4.89G" }), "resilver in progress · 49% (2.44G of 4.89G)", "progress prints the quantity the percentage came from");
assert.equal(zfsScanProgress({ function: "SCRUB", state: "SCANNING", examined: "129M", to_examine: "129M" }), "scrub in progress · 100% (129M of 129M)");
assert.equal(zfsScanProgress({ function: "RESILVER", state: "SCANNING" }), "resilver in progress (? of ?)");
assert.equal(zfsSizeBytes("4.89G"), 4.89 * 1024 ** 3);
assert.equal(zfsSizeBytes("512"), 512);
assert.equal(Number.isNaN(zfsSizeBytes(undefined)), true);
assert.deepEqual(zfsPoolSummary({ state: "ONLINE", scan: { function: "SCRUB", state: "FINISHED" } }), { text: "Pool online · last scrub finished", attention: false });
// ONLINE leaves with checksum errors, or a ZFS status sentence, must raise attention — not only
// non-ONLINE vdev states — and name the leaf with its counts in the summary line.
assert.deepEqual(zfsPoolSummary({ state: "ONLINE", status: "One or more devices has experienced an unrecoverable error.", devices: [{ name: "DATA-5-part1", path: "/dev/disk/by-id/DATA-5-part1", state: "ONLINE", read_errors: "0", write_errors: "0", checksum_errors: "47" }, { name: "DATA-5-part2", path: "/dev/disk/by-id/DATA-5-part2", state: "ONLINE", read_errors: "0", write_errors: "0", checksum_errors: "0" }] }), { text: "Pool online · /dev/disk/by-id/DATA-5-part1 · 47 checksum errors", attention: true });
assert.deepEqual(zfsPoolSummary({ state: "ONLINE", status: "One or more devices has experienced an unrecoverable error.", devices: [{ name: "wwn-ok", state: "ONLINE", read_errors: "0", write_errors: "0", checksum_errors: "0" }] }), { text: "Pool online", attention: true }, "a status sentence alone is attention");
assert.match(src, /export function zfsPoolSummary\(pool: ZFSPoolStatus\) \{\n  const failed = zfsFailedDevices\(pool\);/);
assert.match(src, /access\?\.zfs_pool/);
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
// Replace disk is offered on a host row only while that host's own pool observation is fresh
// (host connected and observed_at current), still reports a replaceable failed leaf (not already
// inside a replacing/spare vdev), no scan is running, and its Core can both list disks and run
// the replacement; it never replaces the "Add this host" action for a host the repository does
// not cover yet. A stale observation is prefixed "Last report" and keeps Replace hidden.
assert.match(src, /scanning \? "resilvering" : beingReplaced \? "being replaced" : pool && replaceable\.length && poolFresh && host\.capabilities\?\.includes\("storage-zfs-replace-v1"\) && host\.capabilities\?\.includes\("storage-discovery-v1"\) \? <button[\s\S]*?>Replace disk<\/button> : !result &&/);
assert.match(src, /poolFresh = host\.connection === "fresh" && storageObservationFresh\(access\?\.observed_at\)/);
// Status/action sentences are visible under the summary; the badge wraps; stale rows keep the Last report prefix (SR-056).
assert.match(src, /function ZFSPoolLines\(\{ pool, prefix \}/);
assert.match(src, /style=\{\{ whiteSpace: "normal" \}\}/);
assert.match(src, /const detail = \[pool\.status, pool\.action\]\.filter\(Boolean\)\.join\(" "\)/);
assert.match(src, /prefix=\{poolFresh \? undefined : "Last report"\}/);
assert.doesNotMatch(src, /title=\{\[pool\.status, pool\.action\]/);
assert.match(src, /zfsReplaceableDevices/);
assert.match(src, /pool\?\.scan\?\.state === "SCANNING"/);
// Replace dialog reads the live inventory pool so an ONLINE recovery reaches the empty state (SR-057).
assert.match(src, /setReplaceZFS\(\{ repositoryID: repository\.repository_id, name: repository\.name, host \}\)/);
assert.match(src, /pool=\{storage\.find\(s => s\.id === replaceZFS\.repositoryID\)\?\.zfs_host_access\?\.find\(access => access\.host_id === replaceZFS\.host\.id\)\?\.pool\}/);
assert.doesNotMatch(src, /replaceZFS\.pool/);
// The resilver itself is the repository's existing pool observation, not a second progress display.
assert.match(src, /The resilver is shown on the repository; ZFS retires the old disk itself when the resilver finishes\./);
// A pool that lost a disk still serves every VM on it, exactly as Core's storageServesData
// decides: a degraded repository stays usable for VM disks and for ISO uploads, while a
// pool that cannot serve data does not.
assert.equal(storageServesData("healthy"), true);
assert.equal(storageServesData("degraded"), true);
assert.equal(storageServesData("unavailable"), false);
assert.equal(storageServesData(undefined), false);
const degradedDataset = { ...ready, kind: "zfs", host_id: "host-1", access: { ...ready.access, status: "degraded" } };
assert.equal(storageUsable(degradedDataset, "host-1"), true);
assert.equal(storageUsable({ ...degradedDataset, access: { ...degradedDataset.access, status: "unavailable" } }, "host-1"), false);
// The ISO library applies the same rule, from the same helper.
const managedSrc = readFileSync(new URL("./managed.tsx", import.meta.url), "utf8");
assert.match(managedSrc, /isoReadyStorage = storage\.filter\(s => s\.status === "succeeded" && storageServesData\(s\.access\?\.status\)/);
assert.match(managedSrc, /zfs_pool\?: ZFSPoolStatus/);

// The ZFS-by-host row carries the same pool observation as the repository row, so the failed
// disk and the resilver are visible next to the Replace disk button that repairs them.
assert.match(src, /<ZFSPoolLines pool=\{pool\}/);
assert.match(src, /\{result\?\.layout \|\| "—"\}/);

console.log("storage.test.mjs: ok");
