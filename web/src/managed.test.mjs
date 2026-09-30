// SPDX-License-Identifier: AGPL-3.0-only
// BUG-0080 regression: a pool registration form belongs to the tab that opened it. Selecting
// another tab must hide it instead of leaving it stacked above unrelated content until the
// operator clicks Close. There is no test harness in web/ (no vitest/jest), so this bundles the
// real module with the already-installed esbuild and renders it with react-dom/server;
// run with: node src/managed.test.mjs
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const moduleURL = bundleTestModule(new URL("./managed.tsx", import.meta.url), "managed");

// A restored pending request is how a form can be open on first render, which is also the
// reload path that first showed the stale panel.
let pending = {};
globalThis.sessionStorage = { getItem: () => JSON.stringify(pending), setItem() {} };
const { ManagedVMs, VMDetails, VMDelete, InstallMedia, VMUsage, staleNote, deleteBlocker, formTab, isoNameFromURL, isoTransferNotice, mediaDiscardable, ejectInstallOffer, guestResizeText, guestCapabilities, networkHostAccess, sentence, GuestTools, powerActions, powerConfirmText, powerBlocker, stateText } = await import(moduleURL);
const { Tasks, isoDownload } = await import(bundleTestModule(new URL("./rail-screens.tsx", import.meta.url), "rail-screens", true));

const props = {
  account: "operator", poolID: "pool-1", poolName: "pool", hosts: [], storage: [], networks: [],
  media: [], vms: [], api: async () => ({}), reload: async () => {}, setTab: () => {},
  unavailable: false, upload: null, onCancelUpload: () => {}, startIsoUpload: async () => ({}), creating: false,
  onCloseCreate: () => {},
};
const tabs = ["", "Virtual machines", "Storage", "Networking"];
// A form shows only on its own screen; the ISO library, and so the ISO upload, is on Storage.
const showsForm = (form, ownTab, tab) => tab === ownTab;

for (const [form, ownTab, heading] of [["network", "Networking", "Register guest network"], ["iso", "Storage", "Upload guest ISO"], ["vm", "Virtual machines", "Create VM · 1 of 6"]]) {
  pending = { [form]: { payload: "{}", id: "request-1" } };
  for (const tab of tabs) {
    const html = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab }));
    // Still mounted everywhere: entries and wizard progress must survive a trip to another tab.
    assert.ok(html.includes(heading), `${form} form on ${tab}: expected ${heading} to stay mounted`);
    assert.equal(/ hidden=""/.test(html), !showsForm(form, ownTab, tab), `${form} form on ${tab}: wrong visibility`);
  }
}

// The VMs screen's own list replaced the pool view's VM table: the create wizard stands alone.
pending = { vm: { payload: "{}", id: "request-1" } };
const wizardOnly = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "Virtual machines", vms: [{ id: "vm-x", pool_id: "pool-1", host_id: "h", name: "listed-vm", state: "running", freshness: "fresh" }] }));
assert.match(wizardOnly, /Create VM · 1 of 6/);
assert.doesNotMatch(wizardOnly, /listed-vm|<table/, "no VM table under the wizard");
pending = {};

assert.equal(formTab(""), "", "no open form matches no tab");
assert.equal(formTab("storage"), "", "storage registration lives in its own tab-scoped view");

assert.equal(
  isoNameFromURL("https://saimei.ftp.acc.umu.se/debian-cd/current/amd64/iso-cd/debian-13.7.0-amd64-netinst.iso"),
  "debian-13.7.0-amd64-netinst.iso",
);
assert.equal(isoNameFromURL("https://downloads.example.org/path/guest%20image.iso?token=1"), "guest image.iso");
assert.equal(isoNameFromURL("https://downloads.example.org/path/"), "");
assert.equal(isoNameFromURL(""), "");

// BUG-0152: adopted Core outcomes retain their original request/status/error, but the
// Controller's updated_at is when that history was re-observed. Only the exact Core
// adoption identity gets the recovered-history time treatment; ordinary request IDs do not.
const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");
const railSource = readFileSync(new URL("./rail-screens.tsx", import.meta.url), "utf8");
const { taskTimestamp } = await import(bundleTestModule(new URL("./settings.tsx", import.meta.url), "settings", true));
const adopted = { id: "historic", host_id: "core-a", request_id: "adopt-outcome:core-a:historic", updated_at: "2026-09-01T00:00:00Z" };
assert.equal(taskTimestamp(adopted).reobserved, true);
assert.equal(taskTimestamp({ ...adopted, request_id: "operator-adopt-outcome:core-a:historic" }).reobserved, false);
assert.equal(taskTimestamp({ ...adopted, request_id: "adopt-outcome:core-b:historic" }).reobserved, false);
assert.equal(taskTimestamp({ ...adopted, request_id: "adopt-outcome:core-a:other-operation" }).reobserved, false);
assert.match(railSource, /Reobserved from Core history/);
assert.match(railSource, /Original outcome time was not reported\./);

// BUG-0204: the ISO banner follows the host transfer record, not the status the submission
// returned. "queued" must stop being shown once the same page lists the import as succeeded.
assert.match(isoTransferNotice("windows.iso", "queued"), /^The host transfer of windows\.iso is queued\. Wait for a succeeded result/);
assert.match(isoTransferNotice("windows.iso", "dispatched"), /host transfer of windows\.iso is dispatched/);
assert.match(isoTransferNotice("windows.iso", "succeeded"), /^windows\.iso is on the host and can be used/);
assert.doesNotMatch(isoTransferNotice("windows.iso", "succeeded"), /queued|Wait for a succeeded result/);
assert.match(isoTransferNotice("windows.iso", "failed"), /^The host transfer of windows\.iso failed/);
assert.match(isoTransferNotice("windows.iso", "needs_attention"), /needs attention/);
assert.match(isoTransferNotice("windows.iso", undefined), /has no host transfer record yet/);
// A URL download never passes through the Controller, so its banner says what the host is doing.
assert.match(isoTransferNotice("virtio-win.iso", "queued", "download"), /^The host download of virtio-win\.iso is queued/);
assert.match(isoTransferNotice("virtio-win.iso", "succeeded", "download"), /^virtio-win\.iso is on the host/);

// Browser-to-Controller ISO upload progress belongs on Storage as well as Tasks: the upload form
// shows the meter while open, and the ISO library keeps it after Close so the operator need not
// leave the page.
const activeUpload = {
  name: "guest.iso", loaded: 50_000_000, total: 100_000_000, percent: 50, rate: 8_000_000,
  state: "uploading", message: "", actor: "operator", updated_at: "2026-09-24T00:00:00Z",
};
pending = { iso: { payload: JSON.stringify({ storage_id: "storage-1" }), id: "iso-req" } };
const uploadOnForm = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "Storage", upload: activeUpload }));
assert.match(uploadOnForm, /aria-label="ISO upload to Controller"/);
assert.match(uploadOnForm, /Upload progress for guest\.iso/);
assert.match(uploadOnForm, /50%/);
assert.match(uploadOnForm, />Cancel</);
assert.equal((uploadOnForm.match(/aria-label="ISO upload to Controller"/g) || []).length, 1, "form open: progress once, not also under the library");
pending = {};
const uploadOnLibrary = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "Storage", upload: activeUpload }));
assert.match(uploadOnLibrary, /aria-label="ISO upload to Controller"/);
assert.match(uploadOnLibrary, /Upload progress for guest\.iso/);
assert.match(uploadOnLibrary, /to Controller/);
assert.doesNotMatch(uploadOnLibrary, /Upload running · see Tasks/);
assert.equal((uploadOnLibrary.match(/aria-label="ISO upload to Controller"/g) || []).length, 1, "form closed: progress under the ISO library");
assert.match(railSource, /export function UploadProgress/);

const downloading = { operation_id: "op-download", received_bytes: 50_000_000, total_bytes: 100_000_000 };
const downloadHost = { id: "host-1", pool_id: "pool-1", connection: "fresh", inventory: { managed: { iso_downloads: [downloading] } } };
const downloadMedia = { id: "iso-1", pool_id: "pool-1", host_id: "host-1", storage_id: "storage-1", name: "guest.iso", operation_id: "op-download", status: "dispatched", size_bytes: 0, sha256: "" };
assert.deepEqual(isoDownload([downloadHost], "host-1", "op-download"), downloading);
assert.equal(isoDownload([{ ...downloadHost, connection: "stale" }], "host-1", "op-download"), undefined, "stale bytes are not live progress");
const downloadLibrary = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "Storage", hosts: [downloadHost], media: [downloadMedia] }));
assert.match(downloadLibrary, /Download progress for guest\.iso/);
assert.match(downloadLibrary, /value="50"/);
const operation = { id: "op-download", host_id: "host-1", kind: "iso-import", request_id: "req-1", status: "dispatched", actor: "operator", updated_at: "2026-09-25T00:00:00Z" };
const tasks = (host, op = operation) => renderToStaticMarkup(createElement(Tasks, { data: { ...props, hosts: [host], operations: [op], media: [downloadMedia], audit: [] }, upload: null, onCancelUpload() {} }));
assert.match(tasks(downloadHost), /Download progress for guest\.iso/);
assert.doesNotMatch(tasks({ ...downloadHost, connection: "stale" }), /Download progress for/);
assert.doesNotMatch(tasks(downloadHost, { ...operation, status: "succeeded" }), /Download progress for/);
assert.match(tasks({ ...downloadHost, inventory: { managed: { iso_downloads: [{ ...downloading, total_bytes: 0 }] } } }), /size unknown/);

// BUG-0203: only an import that never became host-held media can be discarded. The confirmation
// action uses the existing DELETE contract, reconciles an ambiguous response, then reloads.
assert.equal(mediaDiscardable({ id: "failed", status: "failed" }), true);
assert.equal(mediaDiscardable({ id: "uncertain", status: "needs_attention" }), true);
for (const status of ["queued", "dispatched", "succeeded"]) assert.equal(mediaDiscardable({ id: status, status }), false);
const failedLibrary = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "Storage", media: [{ id: "failed", pool_id: "pool-1", host_id: "host-1", storage_id: "storage-1", name: "failed.iso", operation_id: "op-1", status: "failed", size_bytes: 0, sha256: "" }] }));
assert.match(failedLibrary, /Discard failed import/);
const storedLibrary = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "Storage", media: [{ id: "stored", pool_id: "pool-1", host_id: "host-1", storage_id: "storage-1", name: "stored.iso", operation_id: "op-2", status: "succeeded", size_bytes: 40000, sha256: "a" }] }));
assert.doesNotMatch(storedLibrary, /Discard failed import/);
const managedSource = readFileSync(new URL("./managed.tsx", import.meta.url), "utf8");
// DEL-5: the delete request identity belongs to its payload. Retrying after an uncertain
// response reuses it, but switching "Permanently delete disks" gets its own identity instead of
// replaying the old one with another payload and failing with an unrelated 409.
assert.match(managedSource, /request\.current\[String\(wipe\)\] \|\|= crypto\.randomUUID\(\)/);
assert.match(managedSource, /request_id: request\.current\[String\(wipe\)\], delete_disks: wipe/);
assert.match(managedSource, /confirm\(`Discard \$\{media\.name\}\?/);
assert.match(managedSource, /await api\(`media\/\$\{encodeURIComponent\(media\.id\)\}`, "DELETE"\)/);
assert.match(managedSource, /current = await api<Media\[\]>\("media"\)/);
assert.match(managedSource, /current\.some\(entry => entry\.id === media\.id\)/);
assert.match(managedSource, /await reload\(\);\s*\n\s*setNotice\(`Discarded the failed import/);

// BUG-0202: after an install the ISO stays in the drive ahead of the disks, so every boot stops
// at "Press any key to boot from CD or DVD". The eject is offered only where it is true and can
// be submitted: a running VM, install media the firmware tries before any bootable disk, a live
// drive that still holds it, and a host that can take a media request.
const mediaHost = { id: "host-1", pool_id: "pool-1", connection: "fresh", revoked: false, capabilities: ["managed-vm-v1", "vm-media-v1"] };
const installed = {
  id: "vm-1", pool_id: "pool-1", host_id: "host-1", name: "win11", state: "running", freshness: "fresh",
  protected: false, guest: { agent: "connected", clipboard: "ready", resize: false, media: "loaded" },
  spec: {
    description: "", guest_os: "windows", cpu: { count: 2, sockets: 0, cores: 0, threads: 0, model: "host-passthrough" },
    memory: { mode: "dedicated", size_mib: 4096 },
    disks: [{ storage_id: "s1", name: "system", size_gib: 64, bus: "sata", thin: true, boot_order: 2 }],
    nics: [], boot: { firmware: "uefi", machine: "", media_boot_order: 1 }, media_id: "iso-1", autostart: false, start_after_create: false,
  },
};
assert.equal(ejectInstallOffer(installed, mediaHost), true);
assert.equal(ejectInstallOffer({ ...installed, state: "shut off" }, mediaHost), false, "a stopped VM is not offered the eject");
assert.equal(ejectInstallOffer({ ...installed, freshness: "stale" }, mediaHost), false, "an unknown state is not offered the eject");
assert.equal(ejectInstallOffer({ ...installed, protected: true }, mediaHost), false, "the appliance is managed from its host");
assert.equal(ejectInstallOffer({ ...installed, spec: { ...installed.spec, media_id: "" } }, mediaHost), false, "an empty drive has nothing to eject");
assert.equal(ejectInstallOffer({ ...installed, guest: { ...installed.guest, media: "empty" } }, mediaHost), false, "the guest already opened its tray");
assert.equal(ejectInstallOffer({ ...installed, spec: { ...installed.spec, boot: { ...installed.spec.boot, media_boot_order: 3 } } }, mediaHost), false, "a disk already boots first");
assert.equal(ejectInstallOffer({ ...installed, spec: { ...installed.spec, disks: [{ ...installed.spec.disks[0], boot_order: 0 }] } }, mediaHost), false, "no disk is in the boot order to fall back to");
assert.equal(ejectInstallOffer(installed, { ...mediaHost, capabilities: ["managed-vm-v1"] }), false, "the host cannot take a media request");
assert.equal(ejectInstallOffer(installed, { ...mediaHost, connection: "stale" }), false, "the host is not freshly connected");
// A guest observation is absent on older Core builds; the recorded media then stands on its own.
assert.equal(ejectInstallOffer({ ...installed, guest: null }, mediaHost), true);

const detailProps = {
  vm: installed, hosts: [mediaHost], storage: [], networks: [], media: [{ id: "iso-1", name: "windows.iso" }],
  api: async () => ({}), reload: async () => {}, busy: false, unavailable: false, onNotice: () => {}, editing: false, onEditing: () => {},
};
const details = renderToStaticMarkup(createElement(VMDetails, detailProps));
const mediaProps = { vm: installed, host: mediaHost, drives: { drives: [], options: [] }, eject: { busy: false, note: "", onEject: () => {} }, busy: false, unavailable: false };
const offer = renderToStaticMarkup(createElement(InstallMedia, mediaProps));
assert.match(offer, /Eject install media/);
assert.match(offer, /Press any key to boot from CD or DVD/);
assert.doesNotMatch(renderToStaticMarkup(createElement(InstallMedia, { ...mediaProps, vm: { ...installed, state: "shut off" } })), /Eject install media/);
assert.doesNotMatch(details, /Eject install media/, "the offer lives with the drives, not the definition");
// A running request and a task that did not complete are both shown with the offer.
const working = renderToStaticMarkup(createElement(InstallMedia, { ...mediaProps, eject: { busy: true, note: "Ejecting the install media…", onEject: () => {} } }));
assert.match(working, /Ejecting…/);
assert.match(working, /Ejecting the install media…/);
assert.match(working, /<button type="button" disabled=""/);

// BUG-0199: a UEFI VM whose definition predates the Secure Boot field is running with Secure
// Boot on — that is the firmware libvirt gave it — and the details page must say so.
assert.match(details, /Secure Boot/);
assert.match(details, /On · the firmware runs only signed boot software/);
assert.match(details, /Not recorded in this definition/);
assert.match(details, /<dt>TPM<\/dt><dd>None<\/dd>/);
const secured = (boot) => renderToStaticMarkup(createElement(VMDetails, { ...detailProps, vm: { ...installed, spec: { ...installed.spec, boot: { ...installed.spec.boot, ...boot } } } }));
assert.match(secured({ secure_boot: "off" }), /Off · unsigned boot media and drivers are allowed/);
assert.doesNotMatch(secured({ secure_boot: "on" }), /Not recorded in this definition/);
assert.match(secured({ firmware: "bios" }), /Not available · BIOS firmware has no Secure Boot/);
assert.match(secured({ tpm: "2.0" }), /TPM 2\.0 · emulated device in this VM/);
// The second CD drive is part of the definition, so it is reported whether or not it holds a disc.
assert.match(details, /None · second CD drive empty/);
assert.match(
  renderToStaticMarkup(createElement(VMDetails, { ...detailProps, vm: { ...installed, spec: { ...installed.spec, drivers_media_id: "iso-2" } }, media: [{ id: "iso-1", name: "windows.iso" }, { id: "iso-2", name: "virtio-win.iso" }] })),
  /virtio-win\.iso/,
);

// BUG-0206: "Existing VMs: upgrade Core, stop, then Edit → Save changes" was shown for every
// dedicated-memory VM, including one created on a Core that already applies the reservation.
// The Controller cannot observe a VM's memory backing, so the row states the policy only.
assert.match(details, /Dedicated · the host locks this much RAM while the VM runs/);
assert.doesNotMatch(details, /upgrade Core, stop/);

// Roadmap item 8: Core reports nullable observations. The UI must distinguish an unavailable
// metric from zero and state CPU's vCPU-relative meaning without a sample-age row.
const usageDetails = renderToStaticMarkup(createElement(VMUsage, { vm: { ...installed, usage: { observed_at: new Date(Date.now() - 65_000).toISOString(), sample_window_ms: 30_000, cpu_percent: 125, memory_assigned_bytes: 4 * 1024 ** 3, memory_maximum_bytes: 8 * 1024 ** 3, memory_host_bytes: null, disk_used_bytes: 3 * 1024 ** 3, disk_capacity_bytes: null } } }));
assert.match(usageDetails, /VM usage/);
assert.match(usageDetails, /125% of allocated vCPUs/);
assert.match(usageDetails, /Allocation, not memory used inside the guest/);
assert.match(usageDetails, /Host memory cost<\/dt><dd>Not reported/);
assert.match(usageDetails, /Disk capacity<\/dt><dd>Not reported/);
assert.doesNotMatch(usageDetails, /Sample age|Sampled /);
assert.match(renderToStaticMarkup(createElement(VMUsage, { vm: { ...installed, usage: null } })), /This is not zero usage/);
assert.doesNotMatch(details, /Existing VMs/);

// BUG-0201 (UI half): resize is now reported from what the guest can do, not from the device
// model, so the reason must not blame a missing VirtIO display for a guest that simply has no
// tools installed — that sends an operator to recreate a VM that only needs the tools. The
// vdagent behind a resize is the channel the clipboard reads "ready" for, so that is the one
// case where the display can honestly be named.
const resizing = { agent: "connected", clipboard: "ready", resize: true };
assert.match(guestResizeText(resizing), /^Available · /);
assert.match(guestResizeText({ ...resizing, resize: false }), /this VM's display is not VirtIO/);
for (const clipboard of ["waiting", "disconnected", ""]) {
  const text = guestResizeText({ ...resizing, clipboard, resize: false });
  assert.match(text, /not running both qemu-guest-agent and the vdagent/, clipboard);
  assert.match(text, /Install the guest tools/, clipboard);
  assert.doesNotMatch(text, /^Not available · no VirtIO display/, clipboard);
}
assert.match(guestResizeText({ ...resizing, clipboard: "no-channel", resize: false }), /created before the console agent channel existed/);
// The panel renders that reason rather than the old device-model claim.
const noTools = renderToStaticMarkup(createElement(GuestTools, { vm: { ...installed, guest: { ...installed.guest, clipboard: "waiting", resize: false } } }));
assert.match(noTools, /not running both qemu-guest-agent and the vdagent that changes its resolution/);
// BUG-0243: Core reports "waiting" until qemu-guest-agent's channel is also open, so the waiting
// label must name both agents, not send the operator to fix a spice-vdagent that already runs.
assert.match(noTools, /Waiting for guest tools \(qemu-guest-agent and spice-vdagent\) in the guest/);
assert.doesNotMatch(noTools, /no VirtIO display in this VM's definition/);

// The guest tools grid: a word or two per capability, the full reason on demand, and nothing
// that was not reported ever reads Ready.
const caps = g => Object.fromEntries(guestCapabilities(g).map(c => [c.name, [c.tone, c.status]]));
assert.deepEqual(caps(null), { "Guest agent": ["unknown", "Unknown"], Clipboard: ["unknown", "Unknown"], "Display resize": ["unknown", "Unknown"] });
assert.deepEqual(caps(resizing), { "Guest agent": ["ok", "Ready"], Clipboard: ["ok", "Ready"], "Display resize": ["ok", "Ready"] });
assert.deepEqual(caps({ agent: "disconnected", clipboard: "waiting", resize: false }), { "Guest agent": ["warn", "Not running"], Clipboard: ["warn", "Waiting"], "Display resize": ["warn", "Not available"] });
assert.deepEqual(caps({ agent: "no-channel", clipboard: "no-channel", resize: false }), { "Guest agent": ["bad", "Not available"], Clipboard: ["bad", "Not available"], "Display resize": ["warn", "Not available"] });
assert.deepEqual(caps({ agent: "something-new", clipboard: "", resize: false })["Guest agent"], ["unknown", "Unknown"]);
assert.match(guestCapabilities({ ...resizing, resize: false }).find(c => c.name === "Display resize").why, /this VM's display is not VirtIO/);
// The explanations are folded away but still in the page, and the install steps sit behind one disclosure.
assert.match(noTools, /<details><summary title="[^"]*">.*Display resize.*Not available.*Why\?/);
assert.match(noTools, /<details class="about"><summary>.*How to enable guest tools<\/summary>[\s\S]*sudo apt install qemu-guest-agent spice-vdagent/);

// BUG-0208: the login page probed GET /api/v1/session before any session could exist, so every
// load of the sign-in page logged a failed request in the browser console. The probe now runs
// only where it can succeed. The marker is a hint about this browser, never an authorization:
// the Controller still authenticates every request, and no session state is kept here.
const { signedInBefore, rememberSignedIn } = await import(bundleTestModule(new URL("./session.ts", import.meta.url), "session"));
const store = new Map();
globalThis.localStorage = {
  getItem: key => store.has(key) ? store.get(key) : null,
  setItem: (key, value) => { store.set(key, String(value)); },
  removeItem: key => { store.delete(key); },
};
assert.equal(signedInBefore(), false, "a browser that never signed in must not probe the session");
rememberSignedIn(true);
assert.equal(signedInBefore(), true, "a signed-in browser probes so a reload keeps its session");
rememberSignedIn(false);
assert.equal(signedInBefore(), false, "signing out or a 401 stops the probe again");
globalThis.localStorage = { getItem() { throw new Error("storage disabled"); }, setItem() {}, removeItem() {} };
assert.equal(signedInBefore(), true, "without storage, keep the previous behaviour and probe");
// The probe is skipped, not merely silenced, and a 401 anywhere clears the marker.
assert.match(mainSource, /from "\.\/session"/);
assert.match(mainSource, /if \(!signedInBefore\(\)\) \{\s*\n\s*setSession\(null\);/);
assert.match(mainSource, /response\.status === 401 && requestGeneration === generation\.current\) \{\s*\n\s*generation\.current\+\+;\s*\n\s*rememberSignedIn\(false\);/);

// VMDelete keeps its typed-name confirmation; the dialog opens only from its own button.
const deleteHost = { id: "host-1", pool_id: "pool-1", hostname: "core-a", connection: "fresh", revoked: false, capabilities: ["vm-delete-v1"], inventory: null };
const deleteVM = { id: "vm-del", pool_id: "pool-1", host_id: "host-1", name: "guest", state: "shut off", freshness: "fresh", protected: false, role: "guest", status: "succeeded", operation_id: "op-del" };
assert.equal(deleteBlocker(deleteVM, deleteHost), "");
assert.equal(deleteBlocker({ ...deleteVM, state: "running" }, deleteHost), "Stop the VM before deleting it.");
const deleteHTML = renderToStaticMarkup(createElement(VMDelete, { vm: deleteVM, host: deleteHost, api: async () => ({}), reload: async () => {}, busy: false, unavailable: false, onNotice: () => {} }));
assert.match(deleteHTML, />Delete VM</);
assert.doesNotMatch(deleteHTML, /Delete guest\?/, "the dialog stays closed until Delete VM is selected");

// Gate round 2, UI-6: the VM listing reports "unknown" for a VM on a stale host, so the stale
// warning must read the last state from the host's stored inventory instead of the listing.
const staleHost = { ...mediaHost, connection: "stale", inventory: { cpu_count: 2, memory_bytes: 1, observed_at: "2026-09-16T10:00:00Z", vms: [{ id: "vm-1", name: "win11", state: "running" }] } };
const staleVM = { ...installed, state: "unknown", freshness: "stale" };
assert.match(staleNote(staleVM, staleHost), /Last reported state: running/);
assert.match(staleNote(staleVM, staleHost), /An unreachable host does not prove that this VM stopped/);
assert.match(staleNote(staleVM, { ...staleHost, inventory: null }), /Last reported state: unknown/);

// UI-8/GM-05: creation can start the VM, so the General tab must not claim it is always stopped;
// installing a guest starts from the Console tab so the boot prompt is not missed.
assert.doesNotMatch(details, /Creation provisions a stopped VM/);
assert.match(details, /open the console and use Start VM on its toolbar/);

// UI-9: the install-media banner describes every guest, not only a Windows BIOS prompt.
assert.match(offer, /Every start boots the ISO first/);

// UI-10: an error message without a full stop still reads as a sentence before the retry advice.
assert.match(`${sentence("name is required")} Your entries are retained`, /required\. Your entries are retained/);
assert.equal(sentence("Already queued."), "Already queued.");

// UI-1: pending registrations have no identity yet (id ""), so rows fall back to the operation.
assert.match(managedSource, /key=\{n\.id \|\| `op:\$\{n\.operation_id\}`\}/);
assert.match(managedSource, /key=\{m\.id \|\| `op:\$\{m\.operation_id\}`\}/);

// UI-2 (managed half): a created VM page carries its creation operation, so a create that later
// fails still has a route that names the task instead of an unknown VM.
assert.match(managedSource, /location\.hash = `\/vms\/\$\{result\.vm_id\}\/\$\{result\.operation\.id\}`/);

// GM-10: the delete notice is set after navigation clears the old notice and carries the task ID
// so the shell clears it when the host reports the operation's final status.
assert.match(managedSource, /await reload\(\);\s*\n\s*onNotice\(`Delete queued for[^;]+, result\.operation\.id\);/);
assert.doesNotMatch(managedSource, /Registration \$\{result\.status\}/);

// Gate3 2c: the Guest tools badge names the agent's own state. "stale" means report age only,
// so a fresh VM whose agent is disconnected must not read "stale".
const guestBadge = renderToStaticMarkup(createElement(GuestTools, { vm: { id: "vm-g", name: "g", state: "running", freshness: "fresh", guest: { agent: "disconnected", clipboard: "waiting", resize: false } } }));
assert.doesNotMatch(guestBadge, />stale</, "guest agent badge must not say stale");
assert.match(guestBadge, /<span class="badge warn">Disconnected<\/span>/);

// Gate3 2d: Start on a protected Controller appliance always confirms, warning about two Controllers;
// Start on an ordinary VM is still sent straight away.
const startItem = powerActions.find(a => a.action === "start");
assert.match(powerConfirmText(startItem, { protected: true }), /two Controllers/);
assert.equal(powerConfirmText(startItem, { protected: false }), "");
assert.match(powerConfirmText(powerActions.find(a => a.action === "force-stop"), { protected: true }), /Cuts power.*Controller appliance/);
assert.match(managedSource, /if \(powerConfirmText\(item, vm\)\) setPending\(item\)/);

// Gate3 2b (managed half): a power notice carries its task so the page can clear it when the task settles.
assert.match(managedSource, /Follow its progress in Tasks\.`, res\?\.operation\?\.id\)/);
assert.doesNotMatch(managedSource, /Tasks strip/, "the task strip is gone; notices name Tasks");

// Gate3 2f: checkboxes are not stretched by the global input width.
const styleSource = readFileSync(new URL("./style.css", import.meta.url), "utf8");
const inputRule = styleSource.indexOf("\ninput, textarea {");
const checkboxRule = styleSource.indexOf('input:where([type="checkbox"], [type="radio"]) { width: auto;');
assert.ok(inputRule >= 0 && checkboxRule > inputRule, "checkbox width rule must follow the input width rule");

console.log("managed.test.mjs: ok");

// BUG-0273: a guest that put itself to sleep (libvirt pmsuspended) is offered Start (which wakes
// it) and every running-VM action; it is not a state the operator must resolve on the host.
{
  const host = { id: "h", revoked: false, connection: "fresh", capabilities: ["vm-power-v1"] };
  assert.equal(powerBlocker({ state: "pmsuspended", freshness: "fresh" }, host), "");
  assert.match(powerBlocker({ state: "paused", freshness: "fresh" }, host), /resolve that state/);
  for (const action of ["start", "shutdown", "reboot", "force-stop", "force-reboot"]) {
    assert.ok(powerActions.find(a => a.action === action).when("pmsuspended"), `${action} must be offered while asleep`);
  }
  assert.ok(!powerActions.find(a => a.action === "start").when("running"));
  assert.match(stateText("pmsuspended"), /asleep.*Start wakes it/);
  assert.equal(stateText("running"), "running");
  assert.equal(stateText(undefined), "unknown");
}

// Pool resource removal / rename (pool VLAN networks and stored ISOs). Core does the work: the
// UI only submits the request, shows the accepted state and waits for the master to report the
// resource gone. Nothing here deletes a row by itself.
{
  const {
    networkRemovable, mediaRemovable, confirmMatches, validResourceName, renameChanged,
    lifecycleBlocker, networkRemoveBody, networkRenameBody, stableRequestID, heldRequestID, resourceGeneration,
    networkMemberState,
  } = await import(moduleURL);

  // Only a pool VLAN network is Nex's to rename or remove.
  assert.equal(networkRemovable({ kind: "vlan" }), true);
  for (const kind of ["bridge", "network", "", undefined]) assert.equal(networkRemovable({ kind }), false, `${kind}`);
  // A succeeded import is host-held media; a failed one keeps the existing discard action.
  assert.equal(mediaRemovable({ status: "succeeded" }), true);
  for (const status of ["failed", "needs_attention", "queued", "dispatched"]) assert.equal(mediaRemovable({ status }), false, status);
  assert.equal(mediaDiscardable({ status: "failed" }), true, "the discard path for failed imports is unchanged");
  assert.equal(mediaDiscardable({ status: "succeeded" }), false);

  // Exact match only: no trimming and no case folding, or a name that differs by an invisible
  // space would arm a destructive button.
  assert.equal(confirmMatches("guest-vlan", "guest-vlan"), true);
  assert.equal(confirmMatches(" guest-vlan", "guest-vlan"), false);
  assert.equal(confirmMatches("guest-vlan ", "guest-vlan"), false);
  assert.equal(confirmMatches("Guest-VLAN", "guest-vlan"), false);
  assert.equal(confirmMatches("", ""), false);

  // The register-network form's rule (required, at most 100 characters) plus what Core refuses.
  assert.equal(validResourceName("lab vlan 100"), true);
  assert.equal(validResourceName("   "), false);
  assert.equal(validResourceName("a".repeat(100)), true);
  assert.equal(validResourceName("a".repeat(101)), false);
  assert.equal(validResourceName("lab/vlan"), false);
  assert.equal(validResourceName("lab\nvlan"), false);
  // Submitting the name the network already has is a no-op, not an empty rename request.
  assert.equal(renameChanged("guests", "guests"), false);
  assert.equal(renameChanged("guests", " guests "), false);
  assert.equal(renameChanged("guests", "guests-2"), true);
  assert.equal(renameChanged("guests", ""), false);

  const ok = { unavailable: false, inFlight: false, ready: true, hostFresh: true };
  assert.equal(lifecycleBlocker(ok), "");
  assert.match(lifecycleBlocker({ ...ok, unavailable: true }), /out of date/);
  assert.match(lifecycleBlocker({ ...ok, ready: false }), /Wait for Nex Core to confirm/);
  assert.match(lifecycleBlocker({ ...ok, hostFresh: false }), /pool master is not freshly connected/);
  assert.match(lifecycleBlocker({ ...ok, inFlight: true }), /already in flight/);

  // 0 is the pre-generation wire value for 1, so a network registered before generations existed
  // is still removable at the generation Core compares against.
  assert.equal(resourceGeneration(0), 1);
  assert.equal(resourceGeneration(undefined), 1);
  assert.equal(resourceGeneration(4), 4);
  assert.deepEqual(networkRemoveBody({ generation: 0 }, "pool-1"), { pool_id: "pool-1", expected_generation: 1 });
  assert.deepEqual(networkRemoveBody({ generation: 3 }, "pool-1"), { pool_id: "pool-1", expected_generation: 3 });
  assert.deepEqual(networkRenameBody({ generation: 3 }, "pool-1", "  guests-2 "), { pool_id: "pool-1", expected_generation: 3, name: "guests-2" });

  // One request identity per unchanged request: a retry after a lost reply replays it, and only
  // a real change (another new name) mints a new one.
  let minted = 0;
  const mint = () => `request-${++minted}`;
  const store = {};
  const remove = networkRemoveBody({ generation: 2 }, "pool-1");
  assert.equal(stableRequestID(store, "network:n1:remove", remove, mint), "request-1");
  assert.equal(stableRequestID(store, "network:n1:remove", networkRemoveBody({ generation: 2 }, "pool-1"), mint), "request-1", "the same unchanged request keeps its identity");
  assert.equal(stableRequestID(store, "network:n1:rename", networkRenameBody({ generation: 2 }, "pool-1", "guests-2"), mint), "request-2", "another row/action is its own request");
  assert.equal(stableRequestID(store, "network:n1:rename", networkRenameBody({ generation: 2 }, "pool-1", "guests-3"), mint), "request-3", "a changed request mints a new identity");
  assert.equal(stableRequestID(store, "network:n1:remove", networkRemoveBody({ generation: 3 }, "pool-1"), mint), "request-4", "a moved generation is a different request");

  const held = { id: "", signature: "" };
  assert.equal(heldRequestID(held, "pool-1\nhost-b\ntrue", mint), "request-5");
  assert.equal(heldRequestID(held, "pool-1\nhost-b\ntrue", mint), "request-5", "an unchanged retry replays the same request");
  assert.equal(heldRequestID(held, "pool-1\nhost-b\nfalse", mint), "request-6", "a changed payload mints a new request");

  // Per-host convergence keeps reading sensibly through a rename: a member still on the old
  // generation has not applied the new name, so it is not reported as ready.
  const converged = { network_id: "n1", host_id: "member", generation: 2, uplink_present: true };
  assert.equal(networkMemberState({ generation: 2 }, converged), "ready");
  assert.equal(networkMemberState({ generation: 3 }, converged), "converging");
  assert.equal(networkMemberState({ generation: 2 }, { ...converged, uplink_present: false }), "pending");
  assert.equal(networkMemberState({ generation: 2 }, { ...converged, error: "uplink eno1 is missing" }), "uplink eno1 is missing");
  assert.equal(networkMemberState({ generation: 1 }, { ...converged, generation: 0 }), "ready", "0 is generation 1 on both sides");

  // The calls themselves: DELETE/PATCH on the resource, request_id plus the body, and never an
  // optimistic row deletion — the list is reloaded and the master reports the removal.
  assert.match(managedSource, /rename \? "PATCH" : "DELETE", \{ request_id, \.\.\.body \}/);
  assert.match(managedSource, /`\$\{lifecycle\.resource === "network" \? "networks" : "media"\}\/\$\{encodeURIComponent\(lifecycle\.id\)\}`/);
  assert.match(managedSource, /const body = rename \? \{ \.\.\.lifecycle\.base, name: renameText\.trim\(\) \} : lifecycle\.base;/);
  assert.match(managedSource, /base: \{\}, note:/, "an ISO removal sends request_id only");
  assert.doesNotMatch(managedSource, /setNetworks|networks\.filter\(.*!==.*lifecycle/);
  // Never a pool object in browser storage: the request identities live in a ref.
  assert.match(managedSource, /const lifecycleRequests = useRef<Record<string, \{ key: string; id: string \}>>\(\{\}\);/);
  // Enter in the confirmation field must not reach the destructive button.
  assert.match(managedSource, /onKeyDown=\{e => \{ if \(e\.key === "Enter"\) e\.preventDefault\(\); \}\}/);
  assert.match(managedSource, /<button type="button" className="danger" disabled=\{lifecycleBusy \|\| !confirmMatches\(confirmText, lifecycle\.name\)\}/);
  // Focus returns to the button that opened the dialog.
  assert.match(managedSource, /lifecycleOpener\.current\?\.focus\(\)/);

  // Rendered rows: a pool VLAN network offers both actions, a host network offers neither, and a
  // stale Controller inventory disables them with the reason in the title.
  const master = { id: "host-1", pool_id: "pool-1", hostname: "core-a", connection: "fresh", revoked: false, capabilities: ["managed-vm-v1"], inventory: null };
  const vlan = { id: "net-1", pool_id: "pool-1", host_id: "host-1", name: "guests", operation_id: "op-n1", status: "succeeded", kind: "vlan", source: "nex-guests", uplink: "eno1", vlan: 100, generation: 2, member_access: [{ network_id: "net-1", host_id: "host-1", generation: 1, uplink_present: true }] };
  const hostNetwork = { ...vlan, id: "net-2", name: "br0", kind: "bridge", source: "br0", vlan: undefined, member_access: undefined };
  const netProps = { ...props, tab: "Networking", masterHostID: "host-1", hosts: [master] };
  const vlanRow = renderToStaticMarkup(createElement(ManagedVMs, { ...netProps, networks: [vlan] }));
  assert.match(vlanRow, />Rename</);
  assert.match(vlanRow, />Remove</);
  assert.doesNotMatch(vlanRow, /<button type="button" title="[^"]*" disabled=""[^>]*>Rename</, "a confirmed network on a fresh master is actionable");
  // The member line still renders, and says converging while the member is behind the rename.
  assert.match(vlanRow, /core-a: Converging/);
  // One light per pool host: a member the master has not reported, or a host that is not
  // reporting, is Unknown and never green.
  const member = { ...master, id: "host-2", hostname: "core-b" };
  const lights = (n, hosts) => networkHostAccess(n, hosts).map(a => [a.host.hostname, a.tone, a.label]);
  assert.deepEqual(lights({ ...vlan, generation: 1 }, [master, member]), [["core-a", "ok", "Accessible"], ["core-b", "unknown", "Unknown"]]);
  // The master confirms a pool VLAN with its registration; members report their own apply.
  assert.deepEqual(lights({ ...vlan, member_access: [] }, [master, member]), [["core-a", "ok", "Accessible"], ["core-b", "unknown", "Unknown"]]);
  assert.deepEqual(lights({ ...vlan, status: "queued", member_access: [] }, [master]), [["core-a", "unknown", "Unknown"]]);
  assert.deepEqual(lights({ ...vlan, generation: 1 }, [{ ...master, connection: "stale" }]), [["core-a", "unknown", "Unknown"]]);
  assert.deepEqual(lights({ ...vlan, generation: 1, member_access: [{ ...vlan.member_access[0], uplink_present: false }] }, [master]), [["core-a", "warn", "Uplink missing"]]);
  assert.deepEqual(lights({ ...vlan, member_access: [{ ...vlan.member_access[0], error: "bridge failed" }] }, [master]), [["core-a", "bad", "Error"]]);
  assert.deepEqual(lights(hostNetwork, [master, { ...member, connection: "stale" }]), [["core-a", "ok", "Accessible"], ["core-b", "neutral", "No access"]]);
  const hostRow = renderToStaticMarkup(createElement(ManagedVMs, { ...netProps, networks: [hostNetwork] }));
  assert.doesNotMatch(hostRow, />Rename</);
  assert.doesNotMatch(hostRow, />Remove</);
  assert.match(hostRow, /Host network on core-a/);
  assert.match(hostRow, /Managed on core-a/);
  const staleRow = renderToStaticMarkup(createElement(ManagedVMs, { ...netProps, networks: [vlan], unavailable: true }));
  assert.match(staleRow, /Controller inventory may be out of date\. Use Retry above to reload it before changing this pool resource\.[^>]*disabled=""/);
  const queued = renderToStaticMarkup(createElement(ManagedVMs, { ...netProps, networks: [{ ...vlan, id: "", status: "queued" }] }));
  assert.match(queued, /Wait for Nex Core to confirm this resource[^>]*disabled=""/);

  // The ISO library offers Remove ISO only for a stored import, and keeps Discard for a failed one.
  const isoProps = { ...props, tab: "Storage", masterHostID: "host-1", hosts: [master] };
  const stored = { id: "iso-1", pool_id: "pool-1", host_id: "host-1", storage_id: "storage-1", name: "debian.iso", operation_id: "op-i1", status: "succeeded", size_bytes: 40000, sha256: "a" };
  const storedRow = renderToStaticMarkup(createElement(ManagedVMs, { ...isoProps, media: [stored] }));
  assert.match(storedRow, />Remove ISO</);
  assert.doesNotMatch(storedRow, />Discard failed import</);
  const failedRow = renderToStaticMarkup(createElement(ManagedVMs, { ...isoProps, media: [{ ...stored, id: "iso-2", status: "failed" }] }));
  assert.match(failedRow, />Discard failed import</);
  assert.doesNotMatch(failedRow, />Remove ISO</);
}

console.log("managed.test.mjs: pool resource removal ok");

// D1/D2/D4: a registration the pool master refused is only a Controller request record, placement
// warnings ride along with an accepted create, and a re-add before the master has reported the
// member is a wait rather than a failure.
{
  const { placementWarningNotice } = await import(moduleURL);
  const managedSource = readFileSync(new URL("./managed.tsx", import.meta.url), "utf8");
  const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");
  const master = { id: "host-1", pool_id: "pool-1", hostname: "core-a", connection: "fresh", revoked: false, capabilities: ["managed-vm-v1", "pool-resource-removal-v1"], inventory: null };
  const base = { ...props, masterHostID: "host-1", hosts: [master] };

  // Networking: the discard replaces Rename/Remove on a failed row and appears nowhere else.
  const vlan = { id: "net-1", pool_id: "pool-1", host_id: "host-1", name: "guests", operation_id: "op-n1", status: "succeeded", kind: "vlan", source: "nex-guests", uplink: "eno1", vlan: 100, generation: 2 };
  const failedNetwork = renderToStaticMarkup(createElement(ManagedVMs, { ...base, tab: "Networking", networks: [{ ...vlan, status: "failed", error: "uplink eno1 is not present" }] }));
  assert.match(failedNetwork, />Discard failed registration</);
  assert.doesNotMatch(failedNetwork, />Rename</);
  assert.doesNotMatch(failedNetwork, />Remove</);
  for (const status of ["queued", "dispatched", "succeeded"]) {
    const row = renderToStaticMarkup(createElement(ManagedVMs, { ...base, tab: "Networking", networks: [{ ...vlan, status }] }));
    assert.doesNotMatch(row, />Discard failed registration</, `a ${status} network registration is not a discardable request record`);
  }
  // No id to address means no discard, whatever the status says.
  assert.doesNotMatch(renderToStaticMarkup(createElement(ManagedVMs, { ...base, tab: "Networking", networks: [{ ...vlan, id: "", status: "failed" }] })), />Discard failed registration</);
  // The confirmation, the body-less DELETE, and the refresh that follows a success. A 409 from
  // the Controller ("a host reports this registration as applied") is surfaced by run() verbatim.
  assert.match(managedSource, /if \(!confirm\(discardConfirmText\("network", n\.name\)\)\) return;/);
  assert.match(managedSource, /await api\(`networks\/\$\{encodeURIComponent\(n\.id\)\}`, "DELETE"\);\s*\n\s*await reload\(\);/);
  assert.match(managedSource, /setNotice\(`Discarded the failed registration for \$\{n\.name\}/);

  // Storage: a refused registration is not listed by the Controller, so no discard is offered.
  const repository = { id: "repo-1", pool_id: "pool-1", host_id: "host-1", name: "lab-nfs", operation_id: "op-s1", status: "succeeded", kind: "nfs", path: "", generation: 2, source: { nfs: { server: "nfs.example.test", export: "/vms" } }, access: { status: "healthy", readable: true, writable: true }, capabilities: { vm_disk: true } };
  const heldStorage = renderToStaticMarkup(createElement(ManagedVMs, { ...base, tab: "Storage", storage: [repository] }));
  assert.doesNotMatch(heldStorage, />Discard failed registration</);
  assert.match(heldStorage, />Recheck</);
  assert.match(heldStorage, />Detach</);

  // D2: warnings are advisory. They say the request was accepted and that Core decides.
  assert.equal(
    placementWarningNotice(["the pool master's last report does not show this host's access to storage repository repo-1 as current; the request may sit dispatched until it does"]),
    "the pool master's last report does not show this host's access to storage repository repo-1 as current; the request may sit dispatched until it does.",
  );
  assert.equal(placementWarningNotice(["one", "two."]), "one. two.", "each warning is a sentence of its own");
  // The create panel unmounts with the route change, so the 202's warnings go to the shell notice,
  // tied to the operation so it clears itself when the task leaves queued/dispatched.
  assert.match(managedSource, /const result = await api<\{ vm_id: string; operation: \{ id: string \}; warnings\?: string\[\] \}>\("vms", "POST"/);
  // The App keeps a warning that was attached to its just-assigned route through that one
  // hashchange, while the same handler still clears notices attached to an earlier route.
  assert.match(managedSource, /if \(result\.warnings\?\.length\) onNotice\?\.\(placementWarningNotice\(result\.warnings\), result\.operation\.id\);/);
  assert.match(mainSource, /const noticeRoute = useRef\(""\);/);
  assert.match(mainSource, /noticeRoute\.current = text \? \(location\.hash\.slice\(1\) \|\| "\/"\) : ""/);
  assert.match(mainSource, /if \(noticeRoute\.current !== nextRoute\) setNotice\(""\);/);
  assert.match(mainSource, /onCloseCreate=\{\(\) => setCreating\(false\)\} onNotice=\{setNotice\}/);
  // Create-wizard side trips to Storage/Networks keep creating true so #/vms resumes the form.
  assert.match(mainSource, /wizardRoutes = new Set\(\["vms", "storage", "networks"\]\)/);
  assert.match(mainSource, /else if \(!wizardRoutes\.has\(nextKind\)\) \{\s*\n\s*setCreating\(false\);/);
  assert.doesNotMatch(mainSource, /setCreating\(pendingCreate\.current\);/);
  assert.doesNotMatch(mainSource, /o\.warnings\?\.map/, "create-time warnings stay on the 202 response, not the operations list");

  // D4: a retryable 409 from add-host is a wait. The probe and password are kept (only a success
  // clears them), the guidance says to try again, and the host's own words stay in a fold.
  assert.match(mainSource, /retryable = failure\.retryable === true;/);
  assert.match(mainSource, /hostError = failure\.host_error \|\| undefined;/);
  assert.match(mainSource, /if \(e\.status === 409 && e\.retryable\) \{\s*\n\s*setHostRetry\(\{ message: [^\n]*hostError: e\.host_error \|\| "" \}\);\s*\n\s*return null;/);
  assert.match(mainSource, /if \(!added\) return;\s*\n\s*setHostPassword\(""\);/);
  assert.match(mainSource, /<p>\{hostRetry\.message\}<\/p>/);
  assert.match(mainSource, /<summary>What the host reported<\/summary>/);
}

console.log("managed.test.mjs: failed-registration discard, placement warnings and retryable add-host ok");
