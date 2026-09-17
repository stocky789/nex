// SPDX-License-Identifier: AGPL-3.0-only
// VMEDIT-2: changing a UEFI VM to BIOS makes Core delete the VM's UEFI variable store, which
// cannot be undone. The editor must say so and hold Save until the operator acknowledges it.
// run with: node src/vm-edit.test.mjs
import { buildSync } from "esbuild";
import { mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";

const outdir = fileURLToPath(new URL("../node_modules/.cache/nex", import.meta.url));
mkdirSync(outdir, { recursive: true });
const outfile = `${outdir}/vm-edit.test-bundle.mjs`;
buildSync({
  entryPoints: [fileURLToPath(new URL("./vm-edit.tsx", import.meta.url))],
  bundle: true, format: "esm", jsx: "automatic", target: "es2022", outfile,
  external: ["react", "react/jsx-runtime", "react-dom", "react-dom/server"], loader: { ".css": "empty" },
});
const { firmwareIssue, editBlocker, VMEditor } = await import(`file://${outfile}`);
const { createElement } = await import("react");
const { renderToStaticMarkup } = await import("react-dom/server");

assert.match(firmwareIssue("uefi", "bios", false), /variable store/, "UEFI to BIOS needs acknowledgement");
assert.equal(firmwareIssue("uefi", "bios", true), "", "acknowledged");
assert.equal(firmwareIssue("uefi", "uefi", false), "");
assert.equal(firmwareIssue("bios", "uefi", false), "", "BIOS to UEFI deletes nothing");
assert.equal(firmwareIssue("bios", "bios", false), "");
// Gate3 2a: a VM whose creation task is still queued or running cannot be edited yet.
const readyVM = { id: "vm-1", status: "succeeded", protected: false, spec: {} };
assert.equal(editBlocker({ ...readyVM, status: "queued" }), "Wait for the creation task to finish.");
assert.equal(editBlocker({ ...readyVM, status: "dispatched" }), "Wait for the creation task to finish.");
assert.notEqual(editBlocker(readyVM), "Wait for the creation task to finish.");
// A create that ended failed or needs_attention has finished; Edit stays blocked but points to Task history.
for (const status of ["failed", "needs_attention"]) assert.equal(editBlocker({ ...readyVM, status }), "The creation task did not succeed; check it in Task history.", status);
// VMEDIT-3 follow-up: the VM listing reports each interface's effective MAC, including the one
// derived for an automatic interface. The editor must show those interfaces as existing, with
// that MAC in the field it submits, so removing or changing one names it unambiguously.
const macs = ["52:54:00:aa:00:00", "52:54:00:aa:00:01"];
const listed = { id: "vm-1", pool_id: "p", host_id: "h", name: "vm", status: "succeeded", state: "shut off", freshness: "fresh", protected: false,
  spec: { description: "", guest_os: "linux", cpu: { count: 1, model: "host-passthrough" }, memory: { mode: "dedicated", size_mib: 512 }, disks: [],
    nics: macs.map(mac => ({ network_id: "n", model: "virtio", mac, mtu: 0 })), boot: { firmware: "bios", machine: "q35", media_boot_order: 0 }, media_id: "", autostart: false } };
const html = renderToStaticMarkup(createElement(VMEditor, { vm: listed, poolName: "p", storage: [], networks: [], media: [], api: async () => ({}), reload: async () => {}, unavailable: false, onNotice() {}, onClose() {} }));
for (const mac of macs) assert.match(html, new RegExp(`value="${mac}"`), `editor carries existing MAC ${mac}`);
assert.doesNotMatch(html, /Network interface \d · new/, "listed interfaces are existing, not new");
console.log("vm-edit firmware and interface check ok");
