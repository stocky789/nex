// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { cleanupBody, orphanConfirmation, pollCleanup } = await import(bundleTestModule(new URL("./backup-orphans.tsx", import.meta.url), "backup-orphans", true));

// The confirmation text matches Core's protocol.OrphanCleanupConfirmation.
assert.equal(orphanConfirmation(5, 2, false), "delete 5 recovery points");
assert.equal(orphanConfirmation(5, 2, true), "delete 5 recovery points including 2 protected");

// The request carries the explicit VM ids and the totals the operator was shown.
const groups = [{ vm_id: "vm-a", recovery_points: 3, protected_points: 1 }, { vm_id: "vm-b", recovery_points: 2, protected_points: 0 }];
assert.deepEqual(cleanupBody("borphans-1", groups, true, "delete 5 recovery points including 1 protected"), {
  request_id: "borphans-1", vm_ids: ["vm-a", "vm-b"], include_protected: true,
  confirmation: "delete 5 recovery points including 1 protected", recovery_points: 5, protected_points: 1,
});

// pollCleanup follows Core's operation, then its cleanup record.
const notFound = Object.assign(new Error("not found"), { status: 404 });
const fakeAPI = (op, record) => async path => {
  if (path === "operations") return op ? [{ id: "op-1", ...op }, { id: "other", status: "failed" }] : [];
  if (record === "404") throw notFound;
  return record;
};
const base = "pools/p/backup-repositories/r";
await assert.rejects(pollCleanup(fakeAPI(null), base, "op-1"), /not reported/, "an operation Core has not listed yet is retried, not read as a result");
assert.equal(await pollCleanup(fakeAPI({ status: "queued" }, "404"), base, "op-1"), null, "the record is not requested before Core has planned the cleanup");
assert.equal(await pollCleanup(fakeAPI({ status: "dispatched" }, "404"), base, "op-1"), null);
assert.deepEqual(await pollCleanup(fakeAPI({ status: "failed", error: "counts changed" }, "404"), base, "op-1"), { refusal: "counts changed" }, "a failed operation with no record is a refusal carrying Core's error");
assert.deepEqual(await pollCleanup(fakeAPI({ status: "failed" }, "404"), base, "op-1"), { refusal: "Core refused the cleanup." });
await assert.rejects(pollCleanup(fakeAPI({ status: "succeeded" }, "404"), base, "op-1"), /not found/, "a missing record behind a succeeded operation is an error to retry");
const failedRecord = { status: "failed", points: [{ recovery_point_id: "a", vm_id: "vm-a", status: "deleted" }] };
assert.deepEqual(await pollCleanup(fakeAPI({ status: "failed" }, failedRecord), base, "op-1"), { record: failedRecord }, "a failed cleanup with a record still shows its per-point results");
const running = { status: "running", points: [] };
assert.deepEqual(await pollCleanup(fakeAPI({ status: "succeeded" }, running), base, "op-1"), { record: running }, "a running record behind an accepted operation keeps being followed");
assert.deepEqual(await pollCleanup(fakeAPI({ status: "needs_attention" }, running), base, "op-1"), { stalled: running }, "a running record behind a needs-attention operation will not settle");
assert.deepEqual(await pollCleanup(fakeAPI({ status: "failed" }, running), base, "op-1"), { stalled: running });
