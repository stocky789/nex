// SPDX-License-Identifier: AGPL-3.0-only
// Smallest runnable regression check for pure logic in vm-create.tsx. There is no test
// harness in web/ yet (no vitest/jest, no "test" npm script); this uses the already-installed
// esbuild to transform and import the module directly, run with: node src/vm-create.test.mjs
import { transformSync } from "esbuild";
import { readFileSync, writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import assert from "node:assert/strict";

const src = readFileSync(new URL("./vm-create.tsx", import.meta.url), "utf8");
const { code } = transformSync(src, { loader: "tsx", format: "esm" });
const dir = mkdtempSync(join(tmpdir(), "vm-create-check-"));
const file = join(dir, "vm-create.mjs");
// Stub imports unused by the pure functions under test (React/JSX runtime, and "./managed"
// which is types only, erased by the transform) so the module loads standalone.
const stubbed = code
  .replace('from "react"', 'from "data:text/javascript,export const useRef=()=>({}),useState=()=>[undefined,()=>{}];"')
  .replace('from "./managed"', 'from "data:text/javascript,"')
  .replace('from "./storage"', 'from "data:text/javascript,export const storageUsable=()=>true;"');
writeFileSync(file, stubbed);
const { restoredNICs, autoDiskName, topologyIssue, vmNameIssue, bootKeys, renumber } = await import(`file://${file}`);

// BUG-0062: a restored draft with nics: [] (every interface explicitly removed) must stay [],
// not be replaced by the one-NIC blank template.
assert.deepEqual(restoredNICs([]), []);
assert.deepEqual(restoredNICs(undefined), [{ network_id: "", model: "virtio", mac: "", mtu: 0 }]);
assert.deepEqual(restoredNICs("not-an-array"), [{ network_id: "", model: "virtio", mac: "", mtu: 0 }]);
const kept = [{ network_id: "n1", model: "e1000", mac: "", mtu: 0 }];
assert.deepEqual(restoredNICs(kept), kept);

// Sanity: other pure exports still load and behave (guards against a broken transform hiding the check).
assert.equal(autoDiskName("pool", "vm", 0), "pool-vm");
assert.equal(topologyIssue({ count: 4, sockets: 1, cores: 2, threads: 2 }), "");

// VM display names retain ordinary punctuation, Unicode, case and surrounding spaces.
for (const name of ["Production Web 01", "guest.example.test", " 東京 Débian ", "<Guest> & 'test'", "a".repeat(200), "é".repeat(100)]) {
  assert.equal(vmNameIssue(name), "", name);
}
for (const name of ["", "  ", "a/b", "guest\nname", "guest\u007fname", "guest\u0085name", "guest\uffff", "guest\ufffe", "a".repeat(201), "é".repeat(101)]) {
  assert.notEqual(vmNameIssue(name), "", name);
}
assert.equal(autoDiskName("pool", "東京 Débian 13.1", 0), "pool--d-bian-13.1");

// Newly chosen install media (media_boot_order still 0) is first; restored explicit order is kept.
const freshMedia = {
  disks: [{ storage_id: "s1", name: "system", size_gib: 20, bus: "virtio", thin: true, boot_order: 1 }],
  boot: { firmware: "uefi", machine: "", media_boot_order: 0 },
  media_id: "iso-1",
};
assert.deepEqual(bootKeys(freshMedia), ["media", "disk-0"]);
const numbered = renumber({
  host_id: "", name: "", description: "", guest_os: "linux",
  cpu: { count: 2, sockets: 0, cores: 0, threads: 0, model: "host-passthrough" },
  memory: { mode: "dedicated", size_mib: 2048 },
  nics: [], start_after_create: false, autostart: false,
  ...freshMedia,
});
assert.equal(numbered.boot.media_boot_order, 1);
assert.equal(numbered.disks[0].boot_order, 2);
const restoredOrder = {
  disks: [{ storage_id: "s1", name: "system", size_gib: 20, bus: "virtio", thin: true, boot_order: 1 }],
  boot: { firmware: "uefi", machine: "", media_boot_order: 2 },
  media_id: "iso-1",
};
assert.deepEqual(bootKeys(restoredOrder), ["disk-0", "media"]);

console.log("vm-create.test.mjs: ok");
