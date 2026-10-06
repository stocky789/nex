// SPDX-License-Identifier: AGPL-3.0-only
import { transformSync } from "esbuild";
import { readFileSync, writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import assert from "node:assert/strict";

const src = readFileSync(new URL("./zfs.tsx", import.meta.url), "utf8");
const { code } = transformSync(src, { loader: "tsx", format: "esm" });
const dir = mkdtempSync(join(tmpdir(), "zfs-check-"));
const file = join(dir, "zfs.mjs");
writeFileSync(file, code
  .replace('from "react"', 'from "data:text/javascript,export const useEffect=()=>{};export const useRef=()=>({current:null});export const useState=()=>[undefined,()=>{}];"')
  .replace('from "./managed"', 'from "data:text/javascript,"'));
const { zfsLayoutValid, zfsLayoutNeedsReplace, zfsLayoutFacts, zfsLayoutSummary, zfsLayoutReason, zfsBlockedSummary, zfsDiskOrder, zfsDeviceUsable, zfsDraftFor, zfsAutoLayout, zfsLayoutFor, zfsScope, zfsProvisionHosts, zfsReplaceBody, zfsFailedDevices, zfsReplaceableDevices, deviceBody, dirty, shortName, size, diskColumns, DiskCells, BlockedDisks } = await import(`file://${file}`);
assert.equal(typeof diskColumns, "function");
assert.equal(typeof DiskCells, "function");
assert.equal(typeof BlockedDisks, "function");
assert.equal(typeof size, "function");
assert.equal(typeof shortName, "function");
assert.equal(typeof zfsLayoutNeedsReplace, "function");
assert.equal(zfsLayoutNeedsReplace("mirror", 3), true);
assert.equal(zfsLayoutNeedsReplace("mirror", 2), false);
assert.equal(zfsLayoutNeedsReplace("raidz3", 5), true);
assert.equal(zfsLayoutNeedsReplace("raidz1", 3), false);
assert.equal(dirty({ eligibility: "erase_required" }), true);
assert.equal("erase_confirmation" in deviceBody({ by_id: "/dev/disk/by-id/a", serial: "a", size_bytes: 1, fingerprint: "f", eligibility: "blank" }, "/dev/disk/by-id/a"), false);
assert.equal(deviceBody({ by_id: "/dev/disk/by-id/a", serial: "a", size_bytes: 1, fingerprint: "f", eligibility: "erase_required" }, "/dev/disk/by-id/a").erase_confirmation, "/dev/disk/by-id/a");
// "Configure this host" on core-c must not resume a draft saved for core-b.
assert.equal(zfsDraftFor({ hostID: "core-b" }, "core-c"), null);
assert.deepEqual(zfsDraftFor({ hostID: "core-b" }, "core-b"), { hostID: "core-b" });
assert.deepEqual(zfsDraftFor({ hostID: "core-b" }, ""), { hostID: "core-b" });

// A layout override stays offered, and stays on a draft, only while the device count still fits it.
assert.equal(zfsLayoutValid("auto", 0), true);
// A three-way mirror is what an operator needs to add a disk before removing a suspect one.
for (const [layout, valid] of [["single", [1]], ["mirror", [2, 3]], ["raidz1", [3, 4, 5, 6]], ["striped_mirrors", [4, 6]], ["raidz2", [4, 5, 6]], ["raidz3", [5, 6]]]) {
  for (const count of [1, 2, 3, 4, 5, 6]) assert.equal(zfsLayoutValid(layout, count), valid.includes(count), `${layout} with ${count}`);
}
assert.equal(zfsLayoutValid("zraid9", 4), false);
// An override that no longer fits the ticked disks falls back to Core's choice instead of being sent.
assert.equal(zfsLayoutFor("mirror", 2), "mirror");
assert.equal(zfsLayoutFor("mirror", 3), "mirror");
assert.equal(zfsLayoutFor("mirror", 4), "auto");
assert.equal(zfsLayoutFor(undefined, 3), "auto");
// The displayed automatic layout names Core's fixed default (protocol.ZFSLayout).
assert.deepEqual([1, 2, 3, 4, 6].map(zfsAutoLayout), ["single", "mirror", "raidz1", "raidz1", "raidz1"]);
// One ticked host of several is that host's own repository; otherwise a pool repository.
assert.equal(zfsScope(1, 2, false), "host");
assert.equal(zfsScope(2, 2, false), "pool");
assert.equal(zfsScope(1, 1, false), "pool", "a single-host pool keeps a repository later members can join");
assert.equal(zfsScope(1, 3, true), "pool", "adding a host always extends the existing pool repository");
// H5: the replay signature is stored only as a digest, so no by-id name or typed confirmation is persisted.
assert.match(src, /const signature = Array\.from\(new Uint8Array\(await crypto\.subtle\.digest\("SHA-256"/);
assert.doesNotMatch(src, /submitted\.current = JSON\.stringify/);
// A connection change while the dialog is open cannot change the offered hosts, scope or payload.
assert.match(src, /const \[eligibleHosts, setEligibleHosts\] = useState\(liveHosts\)/);
// H5: neither the disk selection nor its typed erase confirmation is ever written to session
// storage, and the step is not restored, so a reopened wizard re-discovers and re-arms erasure.
assert.match(src, /JSON\.stringify\(\{ hostID: initialHostID, hostIDs, name, layouts, requestID: requestID\.current, submitted: submitted\.current \}\)/);
assert.match(src, /useState<Selection>\(\{\}\)/);
assert.match(src, /useState<"disks" \| "confirm">\("disks"\)/);
assert.equal(zfsDeviceUsable({ by_id: "/dev/disk/by-id/blank", serial: "blank", size_bytes: 1, eligibility: "blank" }), true);
assert.equal(zfsDeviceUsable({ by_id: "/dev/disk/by-id/old", serial: "old", size_bytes: 1, eligibility: "erase_required" }), true);
assert.equal(zfsDeviceUsable({ by_id: "/dev/disk/by-id/system", serial: "system", size_bytes: 1, eligibility: "system disk" }), false);
// Hot spares are sent beside the data disks and count toward no part of the layout: three ticked
// disks with one spare is still a two-disk mirror, and the typed erase confirmation travels with
// a spare exactly as with a data disk.
const disk = (name, eligibility = "blank") => ({ by_id: `/dev/disk/by-id/${name}`, serial: name, size_bytes: 1024, fingerprint: "sha256:" + "0".repeat(64), eligibility });
const chosen = [
  { host_id: "core-a", device: disk("d2"), confirmation: "" },
  { host_id: "core-a", device: disk("d1"), confirmation: "" },
  { host_id: "core-a", device: disk("s1", "erase_required"), confirmation: "/dev/disk/by-id/s1", spare: true },
  { host_id: "core-b", device: disk("b1", "erase_required"), confirmation: "/dev/disk/by-id/b1" },
];
assert.deepEqual(zfsProvisionHosts(["core-a", "core-b", "core-c"], chosen, { "core-a": "mirror" }), [
  { host_id: "core-a", layout: "mirror", devices: [{ by_id: "/dev/disk/by-id/d1", serial: "d1", size_bytes: 1024, fingerprint: chosen[0].device.fingerprint }, { by_id: "/dev/disk/by-id/d2", serial: "d2", size_bytes: 1024, fingerprint: chosen[0].device.fingerprint }], spares: [{ by_id: "/dev/disk/by-id/s1", serial: "s1", size_bytes: 1024, fingerprint: chosen[0].device.fingerprint, erase_confirmation: "/dev/disk/by-id/s1" }] },
  { host_id: "core-b", devices: [{ by_id: "/dev/disk/by-id/b1", serial: "b1", size_bytes: 1024, fingerprint: chosen[0].device.fingerprint, erase_confirmation: "/dev/disk/by-id/b1" }] },
]);
// A host with spares but no data disk builds no pool, so it is not offered as a layout at all.
assert.deepEqual(zfsProvisionHosts(["core-a"], [{ ...chosen[2] }], {}), []);
// Spares on fewer than two data disks are dropped — unticking after marking Spare must not
// submit a single-disk pool with a hot spare (SR-046).
assert.deepEqual(zfsProvisionHosts(["core-a"], [
  { host_id: "core-a", device: disk("d1"), confirmation: "" },
  { host_id: "core-a", device: disk("s1"), confirmation: "", spare: true },
], {}), [
  { host_id: "core-a", devices: [{ by_id: "/dev/disk/by-id/d1", serial: "d1", size_bytes: 1024, fingerprint: chosen[0].device.fingerprint }] },
]);
assert.equal("spares" in zfsProvisionHosts(["core-a"], [
  { host_id: "core-a", device: disk("d1"), confirmation: "" },
  { host_id: "core-a", device: disk("d2"), confirmation: "" },
  { host_id: "core-a", device: disk("s1"), confirmation: "", spare: true },
], {})[0], true);
// A three-way mirror override survives the spare, which a layout rule counting all four would drop.
assert.equal(zfsProvisionHosts(["core-a"], [...chosen.slice(0, 3), { host_id: "core-a", device: disk("d3"), confirmation: "" }], { "core-a": "mirror" })[0].layout, "mirror");

// The failed leaf is named exactly as the pool observation reported it, path first.
assert.deepEqual(zfsFailedDevices({ state: "DEGRADED", devices: [{ name: "wwn-a", path: "/dev/disk/by-id/wwn-a", state: "FAULTED" }, { name: "wwn-b", path: "/dev/disk/by-id/wwn-b", state: "ONLINE" }, { name: "wwn-c", state: "OFFLINE" }] }), [{ device: "/dev/disk/by-id/wwn-a", state: "FAULTED" }, { device: "wwn-c", state: "OFFLINE" }]);
assert.deepEqual(zfsFailedDevices(undefined), []);
assert.deepEqual(zfsReplaceableDevices({ state: "DEGRADED", devices: [{ name: "old", path: "/dev/disk/by-id/old", state: "OFFLINE", parent: "replacing" }, { name: "other", state: "FAULTED" }] }), [{ device: "other", state: "FAULTED" }]);
assert.deepEqual(zfsFailedDevices({ state: "DEGRADED", devices: [{ name: "old", path: "/dev/disk/by-id/old", state: "OFFLINE", parent: "replacing" }] }), [{ device: "/dev/disk/by-id/old", state: "OFFLINE", parent: "replacing" }]);

// The replacement submission names one repository, one host, the failed leaf and one revalidated
// replacement, with the erase confirmation only when the disk carries old data.
assert.deepEqual(zfsReplaceBody("req-1", { repositoryID: "repo-1", poolID: "pool-1", hostID: "core-a", oldDevice: "/dev/disk/by-id/wwn-a" }, disk("new", "erase_required"), "/dev/disk/by-id/new"), {
  request_id: "req-1", repository_id: "repo-1", pool_id: "pool-1", host_id: "core-a", old_device: "/dev/disk/by-id/wwn-a",
  device: { by_id: "/dev/disk/by-id/new", serial: "new", size_bytes: 1024, fingerprint: chosen[0].device.fingerprint, erase_confirmation: "/dev/disk/by-id/new" },
});
assert.equal("erase_confirmation" in zfsReplaceBody("req-1", { repositoryID: "repo-1", poolID: "pool-1", hostID: "core-a", oldDevice: "/dev/disk/by-id/wwn-a" }, disk("new"), "").device, false);
// One request identity per intent: a changed selection is a new request, an unchanged retry is not.
// selectedFailed follows the live pool so a leaf that returned ONLINE cannot be submitted.
assert.match(src, /const signature = `\$\{selectedFailed\}\\n\$\{chosen\.by_id\}`;\n\s+if \(submitted\.current !== signature\)/);
assert.match(src, /const selectedFailed = failed\.some\(item => item\.device === oldDevice\) \? oldDevice : ""/);
assert.match(src, /pool\?: ZFSPoolStatus/);
// Review re-applies the spare redundancy rule; unticking a data disk after Spare cannot bypass it.
assert.match(src, /sparesFor\(host\.id\) && countFor\(host\.id\) < 2/);
assert.match(src, /const spares = devices\.length >= 2 \? bodies\(true\) : \[\]/);
assert.match(src, /if \(data < 2\) \{\n\s+for \(const \[entry, item\] of Object\.entries\(next\)\)/);
// The live summary under each host: what the ticks add up to, what it costs and what it
// survives. The arithmetic is on the smallest ticked disk, as ZFS itself builds the vdev.
const gib = 1024 ** 3;
const sized = (...sizes) => sizes.map(size_bytes => ({ size_bytes }));
assert.deepEqual(zfsLayoutFacts("single", sized(8 * gib)), { usable: 8 * gib, tolerates: "no disk failures" });
assert.deepEqual(zfsLayoutFacts("mirror", sized(8 * gib, 8 * gib)), { usable: 8 * gib, tolerates: "1 disk failure" });
assert.deepEqual(zfsLayoutFacts("mirror", sized(8 * gib, 8 * gib, 8 * gib)), { usable: 8 * gib, tolerates: "2 disk failures" });
assert.deepEqual(zfsLayoutFacts("raidz1", sized(8 * gib, 8 * gib, 8 * gib)), { usable: 16 * gib, tolerates: "1 disk failure" });
assert.deepEqual(zfsLayoutFacts("raidz2", sized(8 * gib, 8 * gib, 8 * gib, 8 * gib)), { usable: 16 * gib, tolerates: "2 disk failures" });
assert.deepEqual(zfsLayoutFacts("raidz3", sized(8 * gib, 8 * gib, 8 * gib, 8 * gib, 8 * gib)), { usable: 16 * gib, tolerates: "3 disk failures" });
assert.deepEqual(zfsLayoutFacts("striped_mirrors", sized(8 * gib, 8 * gib, 8 * gib, 8 * gib)), { usable: 16 * gib, tolerates: "1 disk failure per mirrored pair" });
// A mixed-size mirror is only as large as its smallest disk, and the summary says so.
assert.deepEqual(zfsLayoutFacts("raidz1", sized(8 * gib, 12 * gib, 16 * gib)), { usable: 16 * gib, tolerates: "1 disk failure" });
assert.equal(zfsLayoutSummary("raidz1", sized(8 * gib, 8 * gib, 8 * gib), 1), "3 data disks + 1 hot spare → RAIDZ1, ~16 GiB usable, survives 1 disk failure");
assert.equal(zfsLayoutSummary("single", sized(8 * gib)), "1 data disk → Single disk, ~8 GiB usable, survives no disk failures");
assert.equal(zfsLayoutSummary("mirror", [], 2), "", "a host with no data disk builds nothing to summarise");
// A layout the ticked disks cannot build says what it needs instead of vanishing from the list.
assert.equal(zfsLayoutReason("raidz2", 4), "");
assert.equal(zfsLayoutReason("raidz2", 3), "needs four disks or more");
assert.equal(zfsLayoutReason("mirror", 4), "needs two or three disks");
assert.equal(zfsLayoutReason("striped_mirrors", 5), "needs an even number of disks, four or more");
// The empty state repeats Core's own reasons for every disk it will not offer.
assert.equal(zfsBlockedSummary([disk("a", "mounted"), disk("b", "mounted"), disk("c", "member of an imported zpool")]), "2 mounted, 1 member of an imported zpool");
assert.equal(zfsBlockedSummary([{ by_id: "/dev/disk/by-id/x", serial: "x", size_bytes: 1 }]), "1 not eligible");
// Disks are listed smallest first, then by id, so the list does not reorder between discoveries.
assert.deepEqual([disk("b"), disk("a"), { ...disk("c"), size_bytes: 1 }].sort(zfsDiskOrder).map(device => device.serial), ["c", "a", "b"]);
// The Spare tick only appears once holding a disk back still leaves a redundant pool, and
// stays while a spare is held so it can be given back.
assert.match(src, /const spareColumn = Boolean\(replaceCapable\) && \(count >= 3 \|\| spares > 0\);/);
assert.match(src, /if \(checked && countFor\(host\.id\) < 3\)/);
// Every layout is listed with its one-line description; the automatic entry names Core's default.
assert.match(src, /Automatic — Core's default for \{count\} \{count === 1 \? "disk" : "disks"\}: \{layoutNames\[zfsAutoLayout\(count\)\]\}/);
assert.match(src, /\{layoutNames\[layout\]\} — \{reason \|\| layoutRules\[layout\]\}/);
// One possible layout is a sentence, not a select with a single entry.
assert.match(src, /choices\.length > 1 \? <fieldset className="zfs-layouts">/);
assert.match(src, /disks build"\} only \{layoutNames\[zfsAutoLayout\(count\)\]\}/);
// The replacement dialog preselects nothing and names the disk its confirm button erases.
assert.match(src, /const \[oldDevice, setOldDevice\] = useState\(""\);/);
assert.match(src, /chosen \? `Erase \$\{shortName\(chosen\)\} and replace` : "Erase disk and replace"/);
assert.match(src, /<strong>ZFS retires the old disk itself when the resilver finishes<\/strong>/);
// Erase confirmation shows the by-id to type (SR-028 / BUG-0487).
assert.match(src, /Type this by-id device name to erase its old data<input[\s\S]*?<span className="mono">\{chosen\.by_id\}<\/span><\/label>/);

// Blank stays muted whether or not a Spare column is last (SR-112).
const styleSource = readFileSync(new URL("./style.css", import.meta.url), "utf8");
assert.doesNotMatch(styleSource, /\.zfs-layout label \{/);
assert.doesNotMatch(styleSource, /^td:last-child \{[^}]*color/m, "no blanket muting of the last column; blank cells carry .muted themselves");

console.log("zfs.test.mjs: ok");
