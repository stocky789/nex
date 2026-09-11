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
const stubbed = code.replace('from "react"', 'from "data:text/javascript,export const useRef=()=>({});export const useState=()=>[undefined,()=>{}];"');
writeFileSync(file, stubbed);
const { storageUsable, storageSource, storageChecked, storageLifecycleReady } = await import(`file://${file}`);

const ready = { id: "nfs-1", host_id: "host-1", status: "succeeded", access: { status: "healthy", readable: true, writable: true }, capabilities: { vm_disk: true }, source: { nfs: { server: "nfs.example.test", export: "/vms" } } };
assert.equal(storageUsable(ready, "host-1"), true);
assert.equal(storageUsable({ ...ready, access: { ...ready.access, writable: false } }, "host-1"), false);
assert.equal(storageUsable({ ...ready, capabilities: {} }, "host-1"), false);
assert.match(storageSource(ready), /nfs\.example\.test/);
assert.equal(storageSource({ ...ready, source: { smb: { server: "files.example.test", share: "vms" } } }), "//files.example.test/vms");
assert.equal(storageSource({ ...ready, source: { zfs: { pool: "tank", dataset: "vms", mount_path: "/tank/vms" } } }), "tank/vms");
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
