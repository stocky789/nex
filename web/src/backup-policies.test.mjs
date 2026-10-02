// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { failedVMs, jobVMResults, repositoryChangeBody, requestRepositoryChange } = await import(bundleTestModule(new URL("./backup-policies.tsx", import.meta.url), "backup-policies"));
const { stableBody } = await import(bundleTestModule(new URL("./backup-types.ts", import.meta.url), "backup-types"));
const pending = {};
const first = stableBody(pending, "pause-policy-a", { paused: true });
assert.equal(stableBody(pending, "pause-policy-a", { paused: false }), first, "a refreshed status must not change an exact retry");
delete pending["pause-policy-a"];
assert.deepEqual(stableBody(pending, "pause-policy-a", { paused: false }), { paused: false }, "a settled action may start a new intent");

const policy = { id: "policy-1", repository_id: "repo-1", name: "Nightly", rule: { frequency: "daily", hour: 2, minute: 0 } };
const fullPolicy = { ...policy, schedule_id: "schedule-1", revision: 7, enabled: false, paused: true, capture_mode: "snapshot", time_zone: "UTC", vm_ids: ["vm-a"], retention: { keep_latest: 3, protect_newer_than: 4 }, limits: { max_concurrent_per_repo: 2 }, overrides: [{ vm_id: "vm-a", capture_mode: "stopped" }] };
assert.deepEqual(repositoryChangeBody(fullPolicy, "repo-2", "held-id"), {
  request_id: "held-id", expected_revision: 7, schedule_id: "schedule-1", name: "Nightly", enabled: false, paused: true,
  repository_id: "repo-2", capture_mode: "snapshot", time_zone: "UTC", rule: fullPolicy.rule,
  vm_ids: fullPolicy.vm_ids, retention: fullPolicy.retention, limits: fullPolicy.limits, overrides: fullPolicy.overrides,
}, "repository change preserves the Core-owned policy fields and schedule");

const deletedCoverage = {
  ...fullPolicy,
  vm_ids: ["vm-a", "vm-deleted"],
  overrides: [
    { vm_id: "vm-deleted", retention: { keep_latest: 9 }, capture_mode: "crash" },
    { vm_id: "vm-a", retention: { keep_latest: 5 }, capture_mode: "stopped" },
  ],
};
const movedCoverage = repositoryChangeBody({ ...deletedCoverage, vm_ids: ["vm-a"] }, "repo-2", "move-id");
assert.deepEqual(movedCoverage.overrides, [deletedCoverage.overrides[1]], "deleted coverage cannot leave an invalid override, while retained VM settings survive");
assert.deepEqual(deletedCoverage.overrides.map(override => override.vm_id), ["vm-deleted", "vm-a"], "building the request does not mutate the reported Core policy");
assert.equal(repositoryChangeBody({ ...fullPolicy, overrides: undefined }, "repo-2", "move-id").overrides, undefined, "jobs without overrides remain valid");
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

const run = (vm, status, at) => ({ policy_id: "policy-1", vm_id: vm, status, updated_at: at });
assert.deepEqual(failedVMs({ id: "policy-1", vm_ids: ["a", "b", "c"] }, [run("a", "failed", "2026-01-01"), run("b", "succeeded", "2026-01-01"), run("c", "failed", "2026-01-01"), run("c", "succeeded", "2026-01-02"), { ...run("d", "failed", "2026-01-01"), policy_id: "other" }]), ["a"], "only a VM whose latest run of this job failed marks the job failed");
assert.deepEqual(failedVMs({ id: "policy-1", vm_ids: ["a"] }, null), [], "unknown run history shows no failure");

const history = [
  run("a", "needs_attention", "2026-01-03"),
  run("a", "succeeded", "2026-01-02"),
  run("a", "failed", "2026-01-01"),
  run("b", "missed", "2026-01-03"),
  run("c", "skipped", "2026-01-03"),
  run("d", "running", "2026-01-03"),
  { ...run("a", "succeeded", "2026-01-04"), policy_id: "other" },
];
const results = jobVMResults("policy-1", history);
assert.equal(results.get("a").last.status, "needs_attention", "a later non-success remains the current result despite an earlier successful backup");
assert.equal(results.get("a").lastSuccess.updated_at, "2026-01-02", "the last successful recovery point remains visible after a later unsuccessful run");
assert.deepEqual(failedVMs({ id: "policy-1", vm_ids: ["a", "b", "c", "d"] }, history), ["a", "b"], "missed and needs-attention results require attention; skipped and running do not");
const reversedResults = jobVMResults("policy-1", [...history].reverse());
for (const vmID of ["a", "b", "c", "d"]) {
  assert.deepEqual(reversedResults.get(vmID), results.get(vmID), "latest result and successful backup selection are independent of history ordering");
}

