// SPDX-License-Identifier: AGPL-3.0-only
// Identifiers Core reports are shown to operators as words. run with: node src/labels.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const { operationLabel, statusLabel, checkLabel, auditLabel, relativeTime } = await import(bundleTestModule(new URL("./labels.ts", import.meta.url), "labels"));

assert.equal(operationLabel("vm-power"), "VM power");
assert.equal(operationLabel("iso-import"), "Import ISO");
assert.equal(operationLabel("refresh-inventory"), "Refresh inventory");
assert.equal(operationLabel("storage-zfs-provision"), "Provision ZFS storage");
assert.equal(operationLabel("pool-transfer"), "Change pool master");
assert.equal(operationLabel("some-future-kind"), "Some future kind", "a newer Core never shows a blank");

assert.equal(statusLabel("needs_attention"), "Needs attention");
assert.equal(statusLabel("in_sync"), "In sync");
assert.equal(statusLabel("shut off"), "Shut off");
assert.equal(statusLabel("fresh"), "Fresh");
assert.equal(statusLabel(""), "Unknown");

assert.equal(checkLabel("pool-storage-reconciliation"), "Pool storage reconciliation");
assert.equal(checkLabel("kvm"), "KVM");
assert.equal(checkLabel("libvirt"), "libvirt");
assert.equal(checkLabel("management-network"), "Management network");

assert.equal(auditLabel("site.create"), "Site created");
assert.equal(auditLabel("host.revoke"), "Host access revoked");
assert.equal(auditLabel("host.connection.stale"), "Host connection stale");

const now = Date.parse("2026-09-23T12:00:00Z");
assert.equal(relativeTime("2026-09-23T11:59:50Z", now), "just now");
assert.equal(relativeTime("2026-09-23T11:57:00Z", now), "3 min ago");
assert.equal(relativeTime("2026-09-23T09:00:00Z", now), "3 h ago");
assert.equal(relativeTime("2026-09-20T12:00:00Z", now), "3 d ago");
assert.equal(relativeTime("2026-08-01T12:00:00Z", now), "2026-08-01");
assert.equal(relativeTime("not a date", now), "not a date");
console.log("labels ok");
