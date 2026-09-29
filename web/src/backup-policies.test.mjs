// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { bundleTestModule } from "./test-helpers.mjs";

const { BackupPolicies, retentionPreview, repositoryChangeBody, requestRepositoryChange } = await import(bundleTestModule(new URL("./backup-policies.tsx", import.meta.url), "backup-policies"));
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

const props = { api: async () => ({}), data: { vms: [], hosts: [] }, pool: { id: "pool-1" }, repos: [], points: null, policies: [], busy: false, unavailable: false, working: "", setWorking() {}, onNotice() {}, onError() {}, error: "", reload: async () => {}, requestID: () => "held-id", clearRequest() {}, openCreate: true, onOpenCreate() {}, onCloseCreate() {} };
assert.equal(renderToStaticMarkup(createElement(BackupPolicies, { ...props, active: false })), "", "off-tab policy form leaves no hidden focusable controls or panel");
assert.match(renderToStaticMarkup(createElement(BackupPolicies, { ...props, active: true })), /Create policy/, "returning to Policies renders the preserved form");
const offlineRepo = { id: "repo-1", name: "Primary", health: { status: "offline", observed_at: new Date().toISOString() } };
const policy = { id: "policy-1", repository_id: "repo-1", name: "Nightly", rule: { frequency: "daily", hour: 2, minute: 0 } };
const offlineList = renderToStaticMarkup(createElement(BackupPolicies, { ...props, active: true, repos: [offlineRepo], policies: [policy], openCreate: false }));
assert.match(offlineList, /Repository offline/, "policy list warns about its selected offline destination");
const changeList = renderToStaticMarkup(createElement(BackupPolicies, { ...props, active: true, repos: [{ ...offlineRepo, kind: "nfs" }, { id: "repo-2", name: "Replacement", kind: "nfs" }], policies: [policy], openCreate: false }));
assert.match(changeList, /Change repository/, "policy row exposes a repository change when another destination is available");
const fullPolicy = { ...policy, schedule_id: "schedule-1", revision: 7, enabled: false, paused: true, capture_mode: "snapshot", time_zone: "UTC", vm_ids: ["vm-a"], retention: { keep_latest: 3, protect_newer_than: 4 }, limits: { max_concurrent_per_repo: 2 }, overrides: [{ vm_id: "vm-a", capture_mode: "stopped" }] };
assert.deepEqual(repositoryChangeBody(fullPolicy, "repo-2", "held-id"), {
  request_id: "held-id", expected_revision: 7, schedule_id: "schedule-1", name: "Nightly", enabled: false, paused: true,
  repository_id: "repo-2", capture_mode: "snapshot", time_zone: "UTC", rule: fullPolicy.rule,
  vm_ids: fullPolicy.vm_ids, retention: fullPolicy.retention, limits: fullPolicy.limits, overrides: fullPolicy.overrides,
}, "repository change preserves the Core-owned policy fields and schedule");
const sent = [];
const pendingChange = {};
const heldChange = stableBody(pendingChange, "repository-policy-1", repositoryChangeBody(fullPolicy, "repo-2", "held-id"));
const api = async (path, method, body) => {
  sent.push({ path, method, body });
  if (sent.length === 1) throw Object.assign(new Error("uncertain reply"), { status: 503 });
  return { operation: { id: "queued-operation", status: "queued" } };
};
await assert.rejects(requestRepositoryChange(api, "pool-1", "policy-1", heldChange), /uncertain reply/);
const retryChange = stableBody(pendingChange, "repository-policy-1", repositoryChangeBody(fullPolicy, "repo-3", "new-id"));
const queued = await requestRepositoryChange(api, "pool-1", "policy-1", retryChange);
assert.equal(queued.operation.status, "queued", "202 response represents queued work, not a completed switch");
assert.deepEqual(sent, [
  { path: "pools/pool-1/backup-policies/policy-1", method: "PUT", body: heldChange },
  { path: "pools/pool-1/backup-policies/policy-1", method: "PUT", body: heldChange },
], "uncertain reply retries the same PUT URL, policy body, and request ID");
