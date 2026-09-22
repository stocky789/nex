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
const { zfsLayoutValid, zfsDeviceUsable, zfsDraftFor, zfsAutoLayout, zfsLayoutFor, zfsScope } = await import(`file://${file}`);
// "Configure this host" on core-c must not resume a draft saved for core-b.
assert.equal(zfsDraftFor({ hostID: "core-b" }, "core-c"), null);
assert.deepEqual(zfsDraftFor({ hostID: "core-b" }, "core-b"), { hostID: "core-b" });
assert.deepEqual(zfsDraftFor({ hostID: "core-b" }, ""), { hostID: "core-b" });

// A layout override stays offered, and stays on a draft, only while the device count still fits it.
assert.equal(zfsLayoutValid("auto", 0), true);
for (const [layout, valid] of [["single", [1]], ["mirror", [2]], ["raidz1", [3, 4, 5, 6]], ["striped_mirrors", [4, 6]], ["raidz2", [4, 5, 6]]]) {
  for (const count of [1, 2, 3, 4, 5, 6]) assert.equal(zfsLayoutValid(layout, count), valid.includes(count), `${layout} with ${count}`);
}
assert.equal(zfsLayoutValid("zraid9", 4), false);
// An override that no longer fits the ticked disks falls back to Core's choice instead of being sent.
assert.equal(zfsLayoutFor("mirror", 2), "mirror");
assert.equal(zfsLayoutFor("mirror", 3), "auto");
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
console.log("zfs.test.mjs: ok");
