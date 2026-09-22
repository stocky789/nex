// SPDX-License-Identifier: AGPL-3.0-only
// VMEDIT-2: changing a UEFI VM to BIOS makes Core delete the VM's UEFI variable store, which
// cannot be undone. The editor must say so and hold Save until the operator acknowledges it.
// run with: node src/vm-edit.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const { firmwareIssue, editBlocker, VMEditor } = await import(bundleTestModule(new URL("./vm-edit.tsx", import.meta.url), "vm-edit", true));
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

const baselineHost = { id: "h", connection: "fresh", capabilities: ["managed-vm-v1", "vm-update-v1", "vm-cpu-baseline-v1"], inventory: { cpu_count: 4, memory_bytes: 8 * 1024 ** 3, cpu_models: ["qemu64", "FutureCPU-v7"], cpu_baseline: { model: "NewBaseline", features: [{ name: "sse4.2", policy: "require" }], machine: "q35", generation: 2, member_count: 2, observed_at: "2026-01-01T00:00:00Z" } } };
const savedVM = { ...listed, spec: { ...listed.spec, cpu: { ...listed.spec.cpu, model: "SavedBaseline", features: [{ name: "avx", policy: "disable" }] }, disks: [{ name: "system", storage_id: "s", size_gib: 20, bus: "virtio", thin: true, boot_order: 1 }] } };
const renderCPU = (vm, host) => renderToStaticMarkup(createElement(VMEditor, { vm, host, poolName: "p", storage: [], networks: [], media: [], api: async () => ({}), reload: async () => {}, unavailable: false, onNotice() {}, onClose() {} }));
const savedHTML = renderCPU(savedVM, baselineHost);
assert.match(savedHTML, /value="__saved-baseline" selected="">Saved fixed CPU baseline · SavedBaseline/);
assert.match(savedHTML, /Pool CPU baseline · NewBaseline \(Recommended\)/);
// A saved fixed CPU that already matches the pool baseline is the only recommendation shown;
// two options labelled "(Recommended)" told the operator to pick one of two identical CPUs.
const matchingVM = { ...savedVM, spec: { ...savedVM.spec, cpu: { ...savedVM.spec.cpu, model: "NewBaseline", features: [{ name: "sse4.2", policy: "require" }] } } };
const matchingHTML = renderCPU(matchingVM, baselineHost);
assert.equal(matchingHTML.match(/\(Recommended\)/g).length, 1, "only one CPU option is the recommendation");
assert.match(matchingHTML, /value="__saved-baseline" selected="">Saved fixed CPU baseline · NewBaseline \(Recommended\)/);
assert.match(matchingHTML, /value="__pool-baseline">Pool CPU baseline · NewBaseline</);
assert.doesNotMatch(savedHTML, /Fixed features:|disable avx/, "CPU feature details are not displayed");
assert.match(savedHTML, /value="qemu64"/, "manual models remain available for every guest OS");
assert.match(savedHTML, /value="FutureCPU-v7"/);
const failedHTML = renderCPU(savedVM, { ...baselineHost, inventory: { ...baselineHost.inventory, cpu_baseline: { error: "member unreachable" } } });
assert.match(failedHTML, /value="__saved-baseline" selected=""/);
assert.match(failedHTML, /Pool CPU baseline unavailable/);
assert.doesNotMatch(failedHTML, /disabled="">Save changes/, "unknown recomputed baseline does not block editing an existing fixed CPU");
for (const state of ["running", "paused", "unknown"]) assert.match(renderCPU({ ...savedVM, state }, baselineHost), /CPU model<select required="" disabled=""/, "CPU/features change only on stopped VM");
assert.match(renderCPU(savedVM, { ...baselineHost, capabilities: ["managed-vm-v1", "vm-update-v1"] }), /Upgrade Nex Core/);
console.log("vm-edit pool CPU baseline check ok");
