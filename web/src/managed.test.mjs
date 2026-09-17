// SPDX-License-Identifier: AGPL-3.0-only
// BUG-0080 regression: a pool registration form belongs to the tab that opened it. Selecting
// another tab must hide it instead of leaving it stacked above unrelated content until the
// operator clicks Close. There is no test harness in web/ (no vitest/jest), so this bundles the
// real module with the already-installed esbuild and renders it with react-dom/server;
// run with: node src/managed.test.mjs
import { buildSync, transformSync } from "esbuild";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

// The bundle keeps React external and is written inside web/node_modules so its bare imports
// resolve to the same copy this file uses; two React instances cannot render together.
const outdir = fileURLToPath(new URL("../node_modules/.cache/nex", import.meta.url));
mkdirSync(outdir, { recursive: true });
const outfile = `${outdir}/managed.test-bundle.mjs`;
buildSync({
  entryPoints: [fileURLToPath(new URL("./managed.tsx", import.meta.url))],
  bundle: true, format: "esm", jsx: "automatic", target: "es2022", outfile,
  external: ["react", "react/jsx-runtime", "react-dom", "react-dom/server"],
});

// A restored pending request is how a form can be open on first render, which is also the
// reload path that first showed the stale panel.
let pending = {};
globalThis.sessionStorage = { getItem: () => JSON.stringify(pending), setItem() {} };
const { ManagedVMs, VMDetails, VMDelete, deleteBlocker, formTab, isoNameFromURL, isoTransferNotice, mediaDiscardable, ejectInstallOffer, guestResizeText, sentence, GuestTools, powerActions, powerConfirmText } = await import(`file://${outfile}`);

const props = {
  account: "operator", poolID: "pool-1", poolName: "pool", hosts: [], storage: [], networks: [],
  media: [], vms: [], api: async () => ({}), reload: async () => {}, setTab: () => {},
  unavailable: false, uploading: false, startIsoUpload: async () => ({}), createVM: false,
  onCreateVMShown: () => {},
};
const tabs = ["General", "Virtual machines", "Storage", "Networking", "ISO library", "Activity"];

for (const [form, ownTab, heading] of [["network", "Networking", "Register guest network"], ["iso", "ISO library", "Upload guest ISO"], ["vm", "Virtual machines", "Create VM · 1 of 6"]]) {
  pending = { [form]: { payload: "{}", id: "request-1" } };
  for (const tab of tabs) {
    const html = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab }));
    // Still mounted everywhere: entries and wizard progress must survive a trip to another tab.
    assert.ok(html.includes(heading), `${form} form on ${tab}: expected ${heading} to stay mounted`);
    assert.equal(/ hidden=""/.test(html), tab !== ownTab, `${form} form on ${tab}: wrong visibility`);
  }
}

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
const timingStart = mainSource.indexOf("const date =");
const timingEnd = mainSource.indexOf("const size =", timingStart);
assert.ok(timingStart >= 0 && timingEnd > timingStart, "task timestamp helper is present");
const timingCode = transformSync(mainSource.slice(timingStart, timingEnd), { loader: "tsx", format: "esm" }).code;
const timingFile = `${outdir}/main-task-timestamp.test.mjs`;
writeFileSync(timingFile, timingCode);
const { taskTimestamp } = await import(`file://${timingFile}?${Date.now()}`);
const adopted = { id: "historic", host_id: "core-a", request_id: "adopt-outcome:core-a:historic", updated_at: "2026-09-01T00:00:00Z" };
assert.equal(taskTimestamp(adopted).reobserved, true);
assert.equal(taskTimestamp({ ...adopted, request_id: "operator-adopt-outcome:core-a:historic" }).reobserved, false);
assert.equal(taskTimestamp({ ...adopted, request_id: "adopt-outcome:core-b:historic" }).reobserved, false);
assert.equal(taskTimestamp({ ...adopted, request_id: "adopt-outcome:core-a:other-operation" }).reobserved, false);
assert.match(mainSource, /Reobserved from Core history/);
assert.match(mainSource, /Original outcome time was not reported\./);

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

// BUG-0203: only an import that never became host-held media can be discarded. The confirmation
// action uses the existing DELETE contract, reconciles an ambiguous response, then reloads.
assert.equal(mediaDiscardable({ id: "failed", status: "failed" }), true);
assert.equal(mediaDiscardable({ id: "uncertain", status: "needs_attention" }), true);
for (const status of ["queued", "dispatched", "succeeded"]) assert.equal(mediaDiscardable({ id: status, status }), false);
const failedLibrary = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "ISO library", media: [{ id: "failed", pool_id: "pool-1", host_id: "host-1", storage_id: "storage-1", name: "failed.iso", operation_id: "op-1", status: "failed", size_bytes: 0, sha256: "" }] }));
assert.match(failedLibrary, /Discard failed import/);
const storedLibrary = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab: "ISO library", media: [{ id: "stored", pool_id: "pool-1", host_id: "host-1", storage_id: "storage-1", name: "stored.iso", operation_id: "op-2", status: "succeeded", size_bytes: 40000, sha256: "a" }] }));
assert.doesNotMatch(storedLibrary, /Discard failed import/);
const managedSource = readFileSync(new URL("./managed.tsx", import.meta.url), "utf8");
// DEL-5: the delete request identity belongs to its payload. Retrying after an uncertain
// response reuses it, but switching "Permanently delete disks" gets its own identity instead of
// replaying the old one with another payload and failing with an unrelated 409.
assert.match(managedSource, /request\.current\[String\(wipe\)\] \|\|= crypto\.randomUUID\(\)/);
assert.match(managedSource, /request_id: request\.current\[String\(wipe\)\], delete_disks: wipe/);
assert.match(managedSource, /confirm\(`Discard \$\{media\.name\}\?/);
assert.match(managedSource, /await api\(`media\/\$\{media\.id\}`, "DELETE"\)/);
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
  api: async () => ({}), reload: async () => {}, busy: false, unavailable: false, onNotice: () => {},
  installMedia: { busy: false, note: "", onEject: () => {} },
};
const details = renderToStaticMarkup(createElement(VMDetails, detailProps));
assert.match(details, /Eject install media/);
assert.match(details, /Press any key to boot from CD or DVD/);
assert.doesNotMatch(renderToStaticMarkup(createElement(VMDetails, { ...detailProps, vm: { ...installed, state: "shut off" } })), /Eject install media/);
// Without the wiring there is no action to offer, so the panel must not advertise one.
assert.doesNotMatch(renderToStaticMarkup(createElement(VMDetails, { ...detailProps, installMedia: undefined })), /Eject install media/);
// A running request and a task that did not complete are both shown with the offer.
const working = renderToStaticMarkup(createElement(VMDetails, { ...detailProps, installMedia: { busy: true, note: "Ejecting the install media…", onEject: () => {} } }));
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
const usageDetails = renderToStaticMarkup(createElement(VMDetails, { ...detailProps, vm: { ...installed, usage: { observed_at: new Date(Date.now() - 65_000).toISOString(), sample_window_ms: 30_000, cpu_percent: 125, memory_assigned_bytes: 4 * 1024 ** 3, memory_maximum_bytes: 8 * 1024 ** 3, memory_host_bytes: null, disk_used_bytes: 3 * 1024 ** 3, disk_capacity_bytes: null } } }));
assert.match(usageDetails, /VM usage/);
assert.match(usageDetails, /125% of allocated vCPUs/);
assert.match(usageDetails, /Allocation, not memory used inside the guest/);
assert.match(usageDetails, /Host memory cost<\/dt><dd>Not reported/);
assert.match(usageDetails, /Disk capacity<\/dt><dd>Not reported/);
assert.doesNotMatch(usageDetails, /Sample age|Sampled /);
assert.match(renderToStaticMarkup(createElement(VMDetails, { ...detailProps, vm: { ...installed, usage: null } })), /This is not zero usage/);
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
const noTools = renderToStaticMarkup(createElement(VMDetails, { ...detailProps, vm: { ...installed, guest: { ...installed.guest, clipboard: "waiting", resize: false } } }));
assert.match(noTools, /not running both qemu-guest-agent and the vdagent that changes its resolution/);
// BUG-0243: Core reports "waiting" until qemu-guest-agent's channel is also open, so the waiting
// label must name both agents, not send the operator to fix a spice-vdagent that already runs.
assert.match(noTools, /Waiting for guest tools \(qemu-guest-agent and spice-vdagent\) in the guest/);
assert.doesNotMatch(noTools, /no VirtIO display in this VM's definition/);

// BUG-0208: the login page probed GET /api/v1/session before any session could exist, so every
// load of the sign-in page logged a failed request in the browser console. The probe now runs
// only where it can succeed. The marker is a hint about this browser, never an authorization:
// the Controller still authenticates every request, and no session state is kept here.
const sessionStart = mainSource.indexOf("const signedInKey");
const sessionEnd = mainSource.indexOf("function Brand()", sessionStart);
assert.ok(sessionStart >= 0 && sessionEnd > sessionStart, "session probe helpers are present");
const sessionFile = `${outdir}/main-session-probe.test.mjs`;
writeFileSync(sessionFile, transformSync(mainSource.slice(sessionStart, sessionEnd), { loader: "tsx", format: "esm" }).code
  + "\nexport { signedInBefore, rememberSignedIn };\n");
const store = new Map();
globalThis.localStorage = {
  getItem: key => store.has(key) ? store.get(key) : null,
  setItem: (key, value) => { store.set(key, String(value)); },
  removeItem: key => { store.delete(key); },
};
const { signedInBefore, rememberSignedIn } = await import(`file://${sessionFile}?${Date.now()}`);
assert.equal(signedInBefore(), false, "a browser that never signed in must not probe the session");
rememberSignedIn(true);
assert.equal(signedInBefore(), true, "a signed-in browser probes so a reload keeps its session");
rememberSignedIn(false);
assert.equal(signedInBefore(), false, "signing out or a 401 stops the probe again");
globalThis.localStorage = { getItem() { throw new Error("storage disabled"); }, setItem() {}, removeItem() {} };
assert.equal(signedInBefore(), true, "without storage, keep the previous behaviour and probe");
// The probe is skipped, not merely silenced, and a 401 anywhere clears the marker.
assert.match(mainSource, /if \(!signedInBefore\(\)\) \{\s*\n\s*setSession\(null\);/);
assert.match(mainSource, /response\.status === 401 && requestGeneration === generation\.current\) \{\s*\n\s*generation\.current\+\+;\s*\n\s*rememberSignedIn\(false\);/);

// BUG-0218: right-click Delete mounted VMDelete closed, so onOpenChange(false) cleared the host
// and the confirm dialog never appeared. Seed open from openSignal and only notify after the
// first open/close transition.
const deleteHost = { id: "host-1", pool_id: "pool-1", hostname: "core-a", connection: "fresh", revoked: false, capabilities: ["vm-delete-v1"], inventory: null };
const deleteVM = { id: "vm-del", pool_id: "pool-1", host_id: "host-1", name: "guest", state: "shut off", freshness: "fresh", protected: false, role: "guest", status: "succeeded", operation_id: "op-del" };
assert.equal(deleteBlocker(deleteVM, deleteHost), "");
assert.equal(deleteBlocker({ ...deleteVM, state: "running" }, deleteHost), "Stop the VM before deleting it.");
const deleteProps = {
  vm: deleteVM, host: deleteHost, api: async () => ({}), reload: async () => {}, busy: false, unavailable: false, onNotice: () => {}, showButton: false,
};
const menuDelete = renderToStaticMarkup(createElement(VMDelete, { ...deleteProps, openSignal: 1 }));
assert.match(menuDelete, /Delete guest\?/, "context-menu openSignal must seed the confirm dialog open");
assert.match(menuDelete, /Permanently delete disks from storage/);
assert.doesNotMatch(renderToStaticMarkup(createElement(VMDelete, deleteProps)), /Delete guest\?/, "without openSignal the dialog stays closed");
assert.match(managedSource, /useState\(\(\) => \(openSignal \?\? 0\) > 0\)/);
assert.match(managedSource, /prevOpen\.current === null/);
assert.doesNotMatch(managedSource, /else if \(!open && element\.open\) element\.close\(\);\s*\n\s*onOpenChange\?\.\(open\);/);
assert.match(mainSource, /Icon kind="delete"/);
assert.match(mainSource, /Icon kind="console"/);

// Gate round 2, UI-6: the VM listing reports "unknown" for a VM on a stale host, so the stale
// warning must read the last state from the host's stored inventory instead of the listing.
const staleHost = { ...mediaHost, connection: "stale", inventory: { cpu_count: 2, memory_bytes: 1, observed_at: "2026-09-16T10:00:00Z", vms: [{ id: "vm-1", name: "win11", state: "running" }] } };
const staleDetails = renderToStaticMarkup(createElement(VMDetails, { ...detailProps, hosts: [staleHost], vm: { ...installed, state: "unknown", freshness: "stale" } }));
assert.match(staleDetails, /Last reported state: running/);
assert.match(staleDetails, /An unreachable host does not prove that this VM stopped/);
assert.match(renderToStaticMarkup(createElement(VMDetails, { ...detailProps, hosts: [{ ...staleHost, inventory: null }], vm: { ...installed, state: "unknown", freshness: "stale" } })), /Last reported state: unknown/);

// UI-8/GM-05: creation can start the VM, so the General tab must not claim it is always stopped;
// installing a guest starts from the Console tab so the boot prompt is not missed.
assert.doesNotMatch(details, /Creation provisions a stopped VM/);
assert.match(details, /open the Console tab and use Start VM on its toolbar/);
assert.match(details, /Select Edit to change the VM/);

// UI-9: the install-media banner describes every guest, not only a Windows BIOS prompt.
assert.match(details, /Every start boots the ISO first/);

// UI-10: an error message without a full stop still reads as a sentence before the retry advice.
assert.match(`${sentence("name is required")} Your entries are retained`, /required\. Your entries are retained/);
assert.equal(sentence("Already queued."), "Already queued.");

// UI-1: pending registrations have no identity yet (id ""), so rows fall back to the operation.
assert.match(managedSource, /key=\{n\.id \|\| `op:\$\{n\.operation_id\}`\}/);
assert.match(managedSource, /key=\{m\.id \|\| `op:\$\{m\.operation_id\}`\}/);

// UI-2 (managed half): a created VM page carries its creation operation, so a create that later
// fails still has a route that names the task instead of an unknown VM.
assert.match(managedSource, /location\.hash = `\/vms\/\$\{result\.vm_id\}\/\$\{result\.operation\.id\}`/);

// GM-10: the delete notice is set after the navigation's hashchange handler (which clears notices)
// has run, so the host page keeps "Delete queued for …".
assert.match(managedSource, /await reload\(\);\s*\n\s*onNotice\(`Delete queued for/);

// Gate3 2c: the Guest tools badge names the agent's own state. "stale" means report age only,
// so a fresh VM whose agent is disconnected must not read "stale".
const guestBadge = renderToStaticMarkup(createElement(GuestTools, { vm: { id: "vm-g", name: "g", state: "running", freshness: "fresh", guest: { agent: "disconnected", clipboard: "waiting", resize: false } } }));
assert.doesNotMatch(guestBadge, />stale</, "guest agent badge must not say stale");
assert.match(guestBadge, /<span class="badge stale">disconnected<\/span>/);

// Gate3 2d: Start on a protected Controller appliance always confirms, warning about two Controllers;
// Start on an ordinary VM is still sent straight away.
const startItem = powerActions.find(a => a.action === "start");
assert.match(powerConfirmText(startItem, { protected: true }), /two Controllers/);
assert.equal(powerConfirmText(startItem, { protected: false }), "");
assert.match(powerConfirmText(powerActions.find(a => a.action === "force-stop"), { protected: true }), /Cuts power.*Controller appliance/);
assert.match(managedSource, /if \(vm && powerConfirmText\(item, vm\)\) setPending\(item\)/);

// Gate3 2b (managed half): a power notice carries its task so the page can clear it when the task settles.
assert.match(managedSource, /Follow its progress in Recent tasks\.`, res\?\.operation\?\.id\)/);

// Gate3 2f/2g: checkboxes are not stretched by the global input width, and a VM of a host that
// is not fresh has a gray tree icon.
const styleSource = readFileSync(new URL("./style.css", import.meta.url), "utf8");
const inputRule = styleSource.indexOf("\ninput, textarea {");
const checkboxRule = styleSource.indexOf('input:where([type="checkbox"], [type="radio"]) { width: auto; }');
assert.ok(inputRule >= 0 && checkboxRule > inputRule, "checkbox width rule must follow the input width rule");
assert.match(styleSource, /\.tree \.host-link\.vm-unknown \.resource-icon \{ filter: grayscale\(1\)/);

console.log("managed.test.mjs: ok");
