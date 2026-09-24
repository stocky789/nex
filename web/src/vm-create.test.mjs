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
const { recommendedCPU, availableBaseline, baselineUnavailable, fixedCPU, selectCPU, cpuSelectionIssue, restoredNICs, autoDiskName, topologyIssue, vmNameIssue, bootKeys, renumber, defaultTPM, defaultDriversMedia, nextDriversMedia, effectiveBoot, secureBootText, tpmText, driversDriveReady, placementBlocker, networkUsable, describeNetwork } = await import(`file://${file}`);

// The install management bridge (nex0 untagged on br0) must be selectable when creating a VM on
// that host. An empty create-network list with a live master is the same gap as an empty Networks
// tab: Core never adopted a succeeded host-local bridge for the placement host.
const defaultNet = { id: "net-1", host_id: "host-1", name: "nex0 untagged", status: "succeeded", kind: "bridge", source: "br0", uplink: "nex0" };
assert.equal(networkUsable(defaultNet, "host-1"), true);
assert.equal(networkUsable(defaultNet, "host-2"), false, "a host-local bridge is only for its own host");
assert.equal(networkUsable({ ...defaultNet, status: "queued" }, "host-1"), false);
assert.equal(describeNetwork(defaultNet), "Untagged on nex0");
const poolVLAN = { id: "vlan-1", host_id: "host-1", name: "Servers", status: "succeeded", kind: "vlan", source: "nex0v100", uplink: "nex0", vlan: 100, member_access: [{ host_id: "host-2", uplink_present: true, bridge: "nex0v100" }] };
assert.equal(networkUsable(poolVLAN, "host-1"), true);
assert.equal(networkUsable(poolVLAN, "host-2"), true);
assert.equal(networkUsable(poolVLAN, "host-3"), false);

// BUG-0196: Windows 11 refuses to install without a TPM 2.0, so a Windows guest is drafted with
// one and every other guest is not. The wizard applies this to a new draft and whenever the
// guest OS changes, until the operator chooses a TPM themselves.
assert.equal(defaultTPM("windows"), "2.0");
assert.equal(defaultTPM("linux"), "none");
assert.equal(defaultTPM("other"), "none");
assert.equal(defaultTPM(""), "none");
assert.equal(defaultDriversMedia("linux", [{ id: "v", name: "virtio-win.iso", host_id: "h", status: "succeeded" }]), "");
assert.equal(defaultDriversMedia("windows", [{ id: "v", name: "virtio-win.iso", host_id: "h", status: "succeeded" }]), "v");
assert.equal(defaultDriversMedia("windows", [{ id: "v", name: "virtio-win.iso", host_id: "h", status: "succeeded" }], "v"), "");
assert.equal(defaultDriversMedia("windows", [{ id: "a", name: "windows.iso", host_id: "h", status: "succeeded" }]), "");
// F2: a host without vm-media-drivers-drive-v1 never gets a driver disc preselected, and a stale
// one is cleared, because its disabled select cannot show or clear the choice and its Core
// would ignore the second drive. F3: leaving Windows removes the virtio-win disc the wizard
// chose itself, but keeps a disc the operator picked.
const isos = [{ id: "v", name: "virtio-win.iso", host_id: "h", status: "succeeded" }, { id: "d", name: "drivers.iso", host_id: "h", status: "succeeded" }];
assert.equal(nextDriversMedia("", "linux", "windows", isos, "", true), "v");
assert.equal(nextDriversMedia("", "linux", "windows", isos, "", false), "", "no preselect when the host cannot attach a driver disc");
assert.equal(nextDriversMedia("v", "windows", "windows", isos, "", false), "", "a value the host cannot attach is cleared");
assert.equal(nextDriversMedia("v", "windows", "linux", isos, "", true), "", "leaving Windows clears the automatic virtio-win disc");
assert.equal(nextDriversMedia("v", "windows", "other", isos, "", true), "");
assert.equal(nextDriversMedia("d", "windows", "linux", isos, "", true), "d", "an operator's own choice stays");
assert.equal(nextDriversMedia("d", "linux", "windows", isos, "", true), "d");
assert.doesNotMatch(src, /defaultDriversMedia\((draft\.guest_os|guest_os),/, "wizard call sites go through the readiness-aware nextDriversMedia");
assert.match(src, /drivers_media_id: driversReady \? draft\.drivers_media_id : ""/, "a host without the capability is never sent a driver disc");
assert.match(src, /if \(!r\.boot\?\.tpm\) d\.boot = \{ \.\.\.d\.boot, tpm: defaultTPM\(d\.guest_os\) \};/);
assert.match(src, /tpmChosen \? \{\} : \{ boot: \{ \.\.\.draft\.boot, tpm: defaultTPM\(guest_os\) \} \}/);

// Secure Boot and an emulated TPM are UEFI features; a BIOS VM is created without either,
// whatever the disabled controls last held. The protocol also refuses secure_boot "on" on BIOS.
assert.deepEqual(effectiveBoot({ firmware: "uefi", secure_boot: "off", tpm: "2.0" }), { firmware: "uefi", secure_boot: "off", tpm: "2.0" });
assert.deepEqual(effectiveBoot({ firmware: "bios", secure_boot: "on", tpm: "2.0" }), { firmware: "bios", secure_boot: "", tpm: "none" });

// BUG-0199: an unset secure_boot on a UEFI definition is Secure Boot on — the firmware libvirt
// already selected for it — not "unknown" and not off.
assert.match(secureBootText({ firmware: "uefi" }), /^On · /);
assert.match(secureBootText({ firmware: "uefi", secure_boot: "" }), /^On · /);
assert.match(secureBootText({ firmware: "uefi", secure_boot: "on" }), /^On · /);
assert.match(secureBootText({ firmware: "uefi", secure_boot: "off" }), /^Off · /);
assert.match(secureBootText({ firmware: "bios", secure_boot: "" }), /^Not available · /);
assert.match(tpmText({ tpm: "2.0" }), /^TPM 2\.0 · /);
assert.equal(tpmText({}), "None");
assert.equal(tpmText({ tpm: "none" }), "None");

// The second CD drive exists only on a host whose Core defines it; an older Core would ignore
// the driver ISO and leave the operator believing a disc is attached.
const driversHost = { id: "host-1", connection: "fresh", revoked: false, capabilities: ["managed-vm-v1", "vm-media-v1", "vm-media-drivers-drive-v1"] };
// U6: an authoritative pool-members response, rather than the host inventory, decides whether
// a fresh managed host can receive a new VM. Unknown is deliberately not treated as available.
assert.match(placementBlocker(driversHost), /Checking the pool master's maintenance report/);
assert.match(placementBlocker(driversHost, { maintenance: true }), /in maintenance/);
assert.equal(placementBlocker(driversHost, { maintenance: false }), "");
assert.match(placementBlocker(driversHost, { reason: "maintenance state is unknown" }), /unknown/);
assert.equal(driversDriveReady(driversHost), true);
assert.equal(driversDriveReady({ ...driversHost, capabilities: ["managed-vm-v1", "vm-media-v1"] }), false);
assert.equal(driversDriveReady({ ...driversHost, connection: "stale" }), false);
assert.equal(driversDriveReady({ ...driversHost, revoked: true }), false);
assert.equal(driversDriveReady(undefined), false);

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

// UI-7/GM-04: the host starts the VM as the last step of the creation task; there is no second
// start task on success, so the wizard must not promise two tasks.
assert.doesNotMatch(src, /you will see two tasks/);
assert.match(src, /as the last step of the creation task/);

console.log("vm-create.test.mjs: ok");

// Pool CPU choices come from Core, independent of guest OS and any model-name list.
const baselineHost = { ...driversHost, capabilities: [...driversHost.capabilities, "vm-cpu-baseline-v1"], inventory: {
  cpu_models: ["qemu64", "FutureCPU-v7"], cpu_baseline: { model: "SyntheticCPU-v8", features: [{ name: "avx", policy: "disable" }, { name: "sse4.2", policy: "require" }], machine: "pc-q35-10.0", generation: 2, member_count: 2, observed_at: "2026-01-01T00:00:00Z" },
} };
const manualCPU = { count: 2, sockets: 0, cores: 0, threads: 0, model: "qemu64" };
const savedCPU = selectCPU(manualCPU, "__pool-baseline", baselineHost);
assert.equal(savedCPU.model, "SyntheticCPU-v8", "native baseline need not be a named model in the catalogue");
assert.deepEqual(savedCPU.features, baselineHost.inventory.cpu_baseline.features);
assert.notEqual(savedCPU.features, baselineHost.inventory.cpu_baseline.features, "draft freezes a copy of the explicit CPU contract");
assert.equal(fixedCPU(savedCPU), true);
assert.equal(cpuSelectionIssue(savedCPU, baselineHost), "");
assert.equal(selectCPU(savedCPU, "qemu64", baselineHost).features, undefined, "manual model selection clears baseline features");
assert.equal(selectCPU(savedCPU, "FutureCPU-v7", baselineHost).model, "FutureCPU-v7", "new model names need no UI allowlist");
assert.equal(selectCPU(savedCPU, "host-model", baselineHost).features, undefined);
for (const h of [undefined, driversHost, { ...baselineHost, connection: "stale" }, { ...baselineHost, revoked: true }, { ...baselineHost, inventory: null }, { ...baselineHost, inventory: { cpu_baseline: { ...baselineHost.inventory.cpu_baseline, error: "member unreachable" } } }]) {
  assert.equal(availableBaseline(h), undefined);
  assert.equal(selectCPU(manualCPU, "__pool-baseline", h).model, "");
}
assert.match(cpuSelectionIssue({ ...manualCPU, model: "" }, baselineHost), /explicitly select/);
assert.match(cpuSelectionIssue(savedCPU, driversHost), /Upgrade Nex Core/);
assert.equal(cpuSelectionIssue(manualCPU, driversHost), "", "older Core manual behavior stays valid");
assert.equal(cpuSelectionIssue({ ...manualCPU, features: [] }, driversHost), "", "empty feature list requires no extension support");
const unavailableHost = { ...baselineHost, inventory: { cpu_baseline: { error: "member unreachable" } } };
assert.match(baselineUnavailable(unavailableHost), /member unreachable/);
assert.equal(cpuSelectionIssue(savedCPU, unavailableHost), "", "unavailable recomputation never replaces or rejects saved CPU; Core revalidates placement");
assert.equal(savedCPU.model, "SyntheticCPU-v8");
console.log("vm-create pool CPU baseline check ok");

assert.equal(selectCPU(savedCPU, "__saved-baseline", baselineHost), savedCPU, "reselecting a saved snapshot never sends a UI sentinel as model");

assert.equal(recommendedCPU(savedCPU, baselineHost.inventory.cpu_baseline.machine, baselineHost), true);
assert.equal(recommendedCPU({ ...savedCPU, features: [...savedCPU.features].reverse() }, baselineHost.inventory.cpu_baseline.machine, baselineHost), true);
assert.equal(recommendedCPU({ ...savedCPU, model: "OldBaseline" }, baselineHost.inventory.cpu_baseline.machine, baselineHost), false);
assert.equal(recommendedCPU({ ...savedCPU, features: [] }, baselineHost.inventory.cpu_baseline.machine, baselineHost), false);
assert.equal(recommendedCPU(savedCPU, "other-machine", baselineHost), false);
assert.equal(recommendedCPU(savedCPU, baselineHost.inventory.cpu_baseline.machine, undefined), false);

assert.equal(recommendedCPU({ ...savedCPU, features: [savedCPU.features[0], savedCPU.features[0]] }, baselineHost.inventory.cpu_baseline.machine, baselineHost), false);

// Exactly one CPU option is the recommendation. When the saved fixed CPU already matches the pool
// baseline it carries the label, so the pool baseline option must not repeat it. This wizard has no
// renderer in this check; vm-edit.test.mjs asserts the rendered markup of the identical expression.
assert.match(src, /savedRecommended \? " \(Recommended\)" : ""/);
assert.match(src, /\$\{savedRecommended \? "" : " \(Recommended\)"\}/);
