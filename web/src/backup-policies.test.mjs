// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { bundleTestModule } from "./test-helpers.mjs";

const { BackupPolicies, retentionPreview } = await import(bundleTestModule(new URL("./backup-policies.tsx", import.meta.url), "backup-policies"));
const { stableBody } = await import(bundleTestModule(new URL("./backup-types.ts", import.meta.url), "backup-types"));
const points = [
  { id: "new", source_vm_id: "vm-a", captured_at: "2026-09-03T00:00:00Z", protected: true },
  { id: "middle", source_vm_id: "vm-a", captured_at: "2026-09-02T00:00:00Z" },
  { id: "old", source_vm_id: "vm-a", captured_at: "2026-09-01T00:00:00Z" },
];

assert.deepEqual(retentionPreview(points, ["vm-a"], 1), { keep: 2, prune: 1 });
assert.equal(retentionPreview(null, ["vm-a"], 1), null);

const pending = {};
const first = stableBody(pending, "pause-policy-a", { paused: true });
assert.equal(stableBody(pending, "pause-policy-a", { paused: false }), first, "a refreshed status must not change an exact retry");
delete pending["pause-policy-a"];
assert.deepEqual(stableBody(pending, "pause-policy-a", { paused: false }), { paused: false }, "a settled action may start a new intent");

const backupsSource = readFileSync(new URL("./backups.tsx", import.meta.url), "utf8");
const policySource = readFileSync(new URL("./backup-policies.tsx", import.meta.url), "utf8");
assert.match(backupsSource, /<BackupPolicies key=\{pool\.id\} active=\{tab === "policies"\}/, "switching tabs keeps the policy draft mounted");
assert.match(policySource, /if \(!active\) return null;/, "the inactive policy panel and listeners leave the rendered tree");
assert.match(policySource, /if \(uncertain\) return;\s*clearRequest\("policy-create"\)/, "an uncertain policy body stays frozen");
assert.match(policySource, /request_id: requestID\(key, "bpol"\)/, "an exact create retry keeps its request ID");

const props = { api: async () => ({}), data: { vms: [] }, pool: { id: "pool-1" }, repos: [], points: null, policies: [], busy: false, unavailable: false, working: "", setWorking() {}, onNotice() {}, onError() {}, error: "", reload: async () => {}, requestID: () => "held-id", clearRequest() {}, openCreate: true, onOpenCreate() {}, onCloseCreate() {} };
assert.equal(renderToStaticMarkup(createElement(BackupPolicies, { ...props, active: false })), "", "off-tab policy form leaves no hidden focusable controls or panel");
assert.match(renderToStaticMarkup(createElement(BackupPolicies, { ...props, active: true })), /Create policy/, "returning to Policies renders the preserved form");
