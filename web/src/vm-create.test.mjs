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
  .replace('from "./managed"', 'from "data:text/javascript,"');
writeFileSync(file, stubbed);
const { restoredNICs, autoDiskName, topologyIssue } = await import(`file://${file}`);

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

console.log("vm-create.test.mjs: ok");
