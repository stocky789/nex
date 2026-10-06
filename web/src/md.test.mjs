// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from "node:fs";
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const src = readFileSync(new URL("./md.tsx", import.meta.url), "utf8");
const {
  mdArrayState, mdArraySummary, mdArrayRebuilding, mdMemberFailed, mdMemberDisk, mdRequiredBytes,
  mdDiskTooSmall, mdReplaceReady, mdReplaceSubmit, mdPollRebuild, mdPollOperation, mdPhase,
} = await import(bundleTestModule(new URL("./md.tsx", import.meta.url), "md"));

const member = (name, state, slot = "0") => ({ name, device: "/dev/" + name, slot, state });
const array = (extra) => ({ name: "md0", device: "/dev/md0", level: "raid1", raid_disks: 2, degraded: 0, members: [], ...extra });
const disk = (by_id, eligibility = "blank", size_bytes = 1024) => ({
  by_id: `/dev/disk/by-id/${by_id}`, serial: by_id, size_bytes, fingerprint: "sha256:" + "0".repeat(64), eligibility,
});

// The state an operator acts on: a running rebuild is named even while the array is degraded.
assert.equal(mdArrayState(array({})), "Healthy");
assert.equal(mdArrayState(array({ sync_action: "idle" })), "Healthy");
assert.equal(mdArrayState(array({ degraded: 1 })), "Degraded");
assert.equal(mdArrayState(array({ degraded: 1, sync_action: "recover", sync_percent: 25 })), "Rebuilding · 25.0%");
assert.equal(mdArrayState(array({ sync_action: "recover" })), "Rebuilding", "no invented progress when the host reports none");
assert.equal(mdArrayState(array({ sync_action: "check", sync_percent: 50 })), "Checking · 50.0%");

// recover / resync / reshape block a second Replace; check and repair do not.
assert.equal(mdArrayRebuilding(array({ sync_action: "recover" })), true);
assert.equal(mdArrayRebuilding(array({ sync_action: "resync" })), true);
assert.equal(mdArrayRebuilding(array({ sync_action: "reshape" })), true);
assert.equal(mdArrayRebuilding(array({ sync_action: "check" })), false);
assert.equal(mdArrayRebuilding(array({ sync_action: "idle" })), false);

// One sentence per state, saying what the array is doing. Healthy arrays name
// their level, not a hard-coded "Mirror", so a non-raid1 array is not described as one.
assert.match(mdArraySummary(array({})), /^raid1 of 2 disks, both in sync\.$/);
assert.match(mdArraySummary(array({ raid_disks: 3 })), /^raid1 of 3 disks, all in sync\.$/);
assert.match(mdArraySummary(array({ level: "raid0", raid_disks: 2 })), /^raid0 of 2 disks, both in sync\.$/);
assert.match(mdArraySummary(array({ degraded: 1 })), /One disk of 2 disks is missing or failed\. The host keeps running without redundancy\./);
assert.match(mdArraySummary(array({ degraded: 2, raid_disks: 3 })), /^2 disks of 3 disks/);
// While recover/resync/reshape runs, the summary says so; check keeps the generic copy line.
assert.match(mdArraySummary(array({ degraded: 1, sync_action: "recover", sync_percent: 25, sync_speed_kbs: 204800 })),
  /Rebuilding · 25\.0% at 200 MB\/s\. The array is rebuilding and is not yet fully redundant\./);
assert.match(mdArraySummary(array({ degraded: 1, sync_action: "recover", sync_percent: 25 })),
  /Rebuilding · 25\.0%\. The array is rebuilding/);
assert.match(mdArraySummary(array({ sync_action: "check", sync_percent: 50 })),
  /Checking · 50\.0%\. The host keeps running while the kernel copies the data\./);

// A failed member is marked from the kernel's own member state, not guessed from the count.
assert.equal(mdMemberFailed(member("sda2", "in_sync")), false);
assert.equal(mdMemberFailed(member("sda2", "faulty")), true);
assert.equal(mdMemberFailed(member("sda2", "in_sync,write_error")), true);
assert.equal(mdMemberFailed(member("sda2", "spare")), false, "a member being rebuilt onto has not failed");

// Members are shown with their disk's size, which means matching the partition to the whole disk.
const disks = [{ name: "/dev/sda", size_bytes: 512 * 1024 ** 3, rotational: false },
               { name: "/dev/nvme0n1", size_bytes: 1024 * 1024 ** 3, rotational: false }];
assert.equal(mdMemberDisk(member("sda2", "in_sync"), disks).name, "/dev/sda");
assert.equal(mdMemberDisk(member("nvme0n1p2", "in_sync"), disks).name, "/dev/nvme0n1");
assert.equal(mdMemberDisk(member("sdz2", "in_sync"), disks), undefined);

// The replacement must be at least as large as a disk the rebuild can copy from, and a failed
// member's disk is never the one measured.
const degraded = array({ degraded: 1, members: [member("sda2", "faulty"), member("nvme0n1p2", "in_sync", "1")] });
assert.equal(mdRequiredBytes(degraded, disks), 1024 * 1024 ** 3);
assert.equal(mdRequiredBytes(array({ members: [member("sdz2", "in_sync")] }), disks), 0, "no invented size when the host reports none");

// Fit gating for the picker: undersized disks stay visible but cannot be submitted; erase needs
// the typed by-id confirmation only when Core says erase_required.
assert.equal(mdDiskTooSmall(disk("small", "blank", 100), 200), true);
assert.equal(mdDiskTooSmall(disk("ok", "blank", 200), 200), false);
assert.equal(mdReplaceReady(disk("blank"), 0, ""), true);
assert.equal(mdReplaceReady(disk("old", "erase_required"), 0, ""), false);
assert.equal(mdReplaceReady(disk("old", "erase_required"), 0, "/dev/disk/by-id/old"), true);
assert.equal(mdReplaceReady(disk("small", "blank", 100), 200, ""), false);
assert.equal(mdReplaceReady(undefined, 0, ""), false);

// The host's phases read as words; an unknown phase from a newer Core is shown as reported.
assert.equal(mdPhase("slots"), "Copying the system image");
assert.equal(mdPhase("future-phase"), "future-phase");

// Submit posts to hosts/<id>/system-disk with no array, reuses request_id on retry after a
// rejected call, and sends erase_confirmation only for erase_required disks.
{
  const calls = [];
  const api = async (path, method, body) => {
    calls.push({ path, method, body });
    if (calls.length === 1) throw new Error("rejected");
    return { operation: { id: "op-1", status: "queued" } };
  };
  const blank = disk("blank");
  const requestID = "req-stable";
  await assert.rejects(() => mdReplaceSubmit(api, "host-a", requestID, blank, ""), /rejected/);
  const task = await mdReplaceSubmit(api, "host-a", requestID, blank, "");
  assert.equal(task.id, "op-1");
  assert.equal(calls.length, 2);
  assert.equal(calls[0].path, "hosts/host-a/system-disk");
  assert.equal(calls[0].method, "POST");
  assert.equal(calls[0].body.request_id, requestID);
  assert.equal(calls[1].body.request_id, requestID);
  assert.equal("array" in calls[0].body, false);
  assert.equal("erase_confirmation" in calls[0].body.device, false);
  assert.equal("erase_confirmation" in calls[1].body.device, false);

  const dirtyCalls = [];
  const dirtyApi = async (path, method, body) => { dirtyCalls.push({ path, method, body }); return { operation: { id: "op-2", status: "queued" } }; };
  const old = disk("old", "erase_required");
  await mdReplaceSubmit(dirtyApi, "host/b", "req-erase", old, old.by_id);
  assert.equal(dirtyCalls[0].path, "hosts/host%2Fb/system-disk");
  assert.equal(dirtyCalls[0].body.device.erase_confirmation, old.by_id);
}

// Polling helper: rebuild ticks only while visible.
{
  globalThis.document = { visibilityState: "hidden" };
  let rebuildTicks = 0;
  const rebuildFns = [];
  mdPollRebuild(() => { rebuildTicks += 1; }, 10, (fn) => { rebuildFns.push(fn); return 1; });
  rebuildFns[0]();
  assert.equal(rebuildTicks, 0, "hidden tab does not refresh");
  document.visibilityState = "visible";
  rebuildFns[0]();
  assert.equal(rebuildTicks, 1);

  const seen = [];
  const opFns = [];
  let ops = [{ id: "op-1", status: "queued" }];
  mdPollOperation(async () => ops, "op-1", "queued", task => seen.push(task.status), 5, (fn) => { opFns.push(fn); return 1; });
  await opFns[0]();
  assert.deepEqual(seen, []);
  ops = [{ id: "op-1", status: "succeeded" }];
  await opFns[0]();
  assert.deepEqual(seen, ["succeeded"]);
}

// Disk picker helpers come from zfs; md only adds the Fit column for the size requirement.
assert.match(src, /from "\.\/zfs"/);
assert.match(src, /diskColumns\(false, true\)/);
assert.match(src, /BlockedDisks devices=\{blocked\}/);
assert.match(src, /DiskCells device=\{d\}/);
assert.doesNotMatch(src, /const size = \(bytes/);

// Replace disk only on a degraded RAID1 that is not rebuilding; a replacement the host still has running does not
// hide it, because the host decides (it continues the same disk and gives up a pulled one); a Core without the
// capability is told to update.
assert.match(src, /array\.degraded > 0 && array\.level === "raid1" && supported && !mdArrayRebuilding\(array\) &&\s+<button/);
assert.match(src, /array\.degraded > 0 && array\.level === "raid1" && !supported &&/);
assert.match(src, /Update Nex Core, then replace the failed disk from here/);
assert.match(src, /export const SYSTEM_DISK_CAPABILITY = "host-system-disk-replace-v1"/);
// The host's report marks the disk the running system started from and shows Core's record of the
// latest replacement, so a fresh browser sees an operation another Controller started.
assert.match(src, /systemDisk\?\.booted_disk === `\/dev\/\$\{disk\.name\}`/);
assert.match(src, /running system/);
assert.match(src, /<ReplacementRecord replacement=\{replacement\} \/>/);
assert.match(src, /Nex Core lays the same partition table on it, copies the verified system image/);
assert.doesNotMatch(src, /array: array\.name/);
// The panel follows a rebuild without the operator pressing Retry. The interval calls through a
// ref and depends only on rebuilding — listing onRefresh in the deps cleared the 10s timer on
// every Stats re-render.
assert.match(src, /Single system disk, no redundancy/);
assert.match(src, /mdPollRebuild\(\(\) => onRefreshRef\.current\(\)\)/);
assert.match(src, /apiRef\.current<Task\[\]>\("operations"\)/);
assert.match(src, /\}, \[rebuilding\]\);/);
assert.match(src, /\}, \[task\?\.id, task\?\.status\]\);/);
assert.doesNotMatch(src, /\}, \[rebuilding, onRefresh\]\);/);
assert.doesNotMatch(src, /\}, \[task, api, onRefresh\]\);/);

// Empty usable list says no disk can be used; too-small is only for a non-empty list of undersized disks.
assert.match(src, /usable\.length === 0\s*\?\s*"This host reports no disk that can be used/);
assert.match(src, /: usable\.every\(d => mdDiskTooSmall\(d, required\)\)\s*\?\s*`Every spare disk on this host is smaller/);
assert.match(src, /\{devices && empty && <p className="empty"/);

console.log("md.test.mjs ok");
