// SPDX-License-Identifier: AGPL-3.0-only
// Tasks and Events: a failed task links to where its action is sent again (there is no generic
// retry request), and Events leads with the conditions hosts report now. run with: node src/settings.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { resubmitLink, Events, movedSection, sections } = await import(bundleTestModule(new URL("./settings.tsx", import.meta.url), "settings-screens", true));

const data = { sites: [], pools: [], audit: [], operations: [], hosts: [{ id: "h1", hostname: "node-1" }],
  vms: [{ id: "vm-1", name: "web-01", operation_id: "op-vm" }], storage: [{ operation_id: "op-st" }], networks: [{ operation_id: "op-net" }], media: [{ operation_id: "op-iso" }] };
const op = (id) => ({ id, host_id: "h1", kind: "vm-start" });
assert.deepEqual(resubmitLink(op("op-vm"), data), { href: "#/vms/vm-1", label: "Open web-01" });
assert.deepEqual(resubmitLink(op("op-st"), data), { href: "#/storage", label: "Open Storage" });
assert.deepEqual(resubmitLink(op("op-net"), data), { href: "#/networks", label: "Open Networks" });
assert.deepEqual(resubmitLink(op("op-iso"), data), { href: "#/storage", label: "Open the ISO library" });
assert.deepEqual(resubmitLink({ ...op("op-b"), kind: "backup-orphan-cleanup" }, data), { href: "#/settings/backups", label: "Open Backup repositories" });
assert.deepEqual(resubmitLink({ ...op("op-b"), kind: "backup-repository-forget" }, data).href, "#/settings/backups");
assert.deepEqual(resubmitLink(op("op-other"), data), { href: "#/hosts/h1", label: "Open node-1" }, "anything else is resubmitted from its host");

const events = (conditions) => renderToStaticMarkup(createElement(Events, { entries: [], data, conditions }));
assert.match(events([{ tone: "warn", title: "node-2 not reporting", detail: "1 VM shown as Unknown", href: "#/hosts/h2" }]), /Current conditions.*href="#\/hosts\/h2".*node-2 not reporting/);
assert.match(events([]), /Every host is reporting and healthy/);
assert.match(events([]), /No events recorded yet\./, "Events is not scoped, so it does not say so");
// Storage, Networks and the ISO library left settings for their own screens; old links redirect.
assert.equal(movedSection("storage"), "#/storage");
assert.equal(movedSection("iso"), "#/storage");
assert.equal(movedSection("networks"), "#/networks");
for (const kept of ["overview", "members", "ha", "backups", "updates", undefined, "constructor"]) assert.equal(movedSection(kept), undefined, String(kept));
assert.deepEqual(sections.map(s => s.label), ["Overview", "Members", "High availability", "Backup repositories", "Updates"]);
console.log("settings ok");
