// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { failedVMs, jobVMResults, repositoryChangeBody, requestRepositoryChange } = await import(bundleTestModule(new URL("./backup-policies.tsx", import.meta.url), "backup-policies"));
const { sendHeld } = await import(bundleTestModule(new URL("./backup-held.ts", import.meta.url), "backup-held-policies"));
const held = { bodies: {}, drafts: {}, points: {} };

const policy = { id: "policy-1", repository_id: "repo-1", name: "Nightly", rule: { frequency: "daily", hour: 2, minute: 0 } };
const fullPolicy = { ...policy, schedule_id: "schedule-1", revision: 7, enabled: false, paused: true, capture_mode: "snapshot", time_zone: "UTC", vm_ids: ["vm-a"], retention: { keep_latest: 3, protect_newer_than: 4 }, limits: { max_concurrent_per_repo: 2 }, overrides: [{ vm_id: "vm-a", capture_mode: "stopped" }] };
assert.deepEqual(repositoryChangeBody(fullPolicy, "repo-2", "held-id"), {
  request_id: "held-id", expected_revision: 7, schedule_id: "schedule-1", name: "Nightly", enabled: false, paused: true,
  repository_id: "repo-2", capture_mode: "snapshot", time_zone: "UTC", rule: fullPolicy.rule,
  vm_ids: fullPolicy.vm_ids, retention: fullPolicy.retention, limits: fullPolicy.limits, overrides: fullPolicy.overrides,
}, "repository change preserves the Core-owned policy fields and schedule");

// Core owns the VM list: the Controller sends it back unchanged, including VMs it cannot see.
const unseenCoverage = { ...fullPolicy, vm_ids: ["vm-a", "vm-not-in-mirror"], overrides: [{ vm_id: "vm-not-in-mirror", retention: { keep_latest: 9 } }, { vm_id: "vm-a", capture_mode: "stopped" }] };
const moved = repositoryChangeBody(unseenCoverage, "repo-2", "move-id");
assert.deepEqual(moved.vm_ids, ["vm-a", "vm-not-in-mirror"]);
assert.deepEqual(moved.overrides, unseenCoverage.overrides, "overrides of VMs the Controller does not list are not dropped");
assert.equal(repositoryChangeBody({ ...fullPolicy, overrides: undefined }, "repo-2", "move-id").overrides, undefined, "jobs without overrides remain valid");
const sent = [];
const api = async (path, method, body) => {
  sent.push({ path, method, body });
  if (sent.length === 1) throw Object.assign(new Error("uncertain reply"), { status: 503 });
  return { operation: { id: "queued-operation", status: "queued" } };
};
// The held body is built once; a retry replays it even if the operator re-picked another target.
const send = body => requestRepositoryChange(api, "pool-1", "policy-1", body);
const uncertain = await sendHeld(held, "repository-policy-1", "bpol", id => repositoryChangeBody(fullPolicy, "repo-2", id), send);
assert.equal(uncertain.definitive, false);
const queued = await sendHeld(held, "repository-policy-1", "bpol", id => repositoryChangeBody(fullPolicy, "repo-3", id), send);
assert.equal(queued.reply.operation.status, "queued", "202 response represents queued work, not a completed switch");
assert.equal(sent[0].body.repository_id, "repo-2");
assert.deepEqual(sent, [
  { path: "pools/pool-1/backup-policies/policy-1", method: "PUT", body: sent[0].body },
  { path: "pools/pool-1/backup-policies/policy-1", method: "PUT", body: sent[0].body },
], "uncertain reply retries the same PUT URL, policy body, and request ID");
assert.equal(held.bodies["repository-policy-1"], undefined);

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

