// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";

const { cleanupBody, orphanConfirmation } = await import(bundleTestModule(new URL("./backup-orphans.tsx", import.meta.url), "backup-orphans", true));
const source = readFileSync(new URL("./backup-orphans.tsx", import.meta.url), "utf8");

// The confirmation text matches Core's protocol.OrphanCleanupConfirmation.
assert.equal(orphanConfirmation(5, 2, false), "delete 5 recovery points");
assert.equal(orphanConfirmation(5, 2, true), "delete 5 recovery points including 2 protected");

// The request carries the explicit VM ids and the totals the operator was shown.
const groups = [{ vm_id: "vm-a", recovery_points: 3, protected_points: 1 }, { vm_id: "vm-b", recovery_points: 2, protected_points: 0 }];
assert.deepEqual(cleanupBody("borphans-1", groups, true, "delete 5 recovery points including 1 protected"), {
  request_id: "borphans-1", vm_ids: ["vm-a", "vm-b"], include_protected: true,
  confirmation: "delete 5 recovery points including 1 protected", recovery_points: 5, protected_points: 1,
});

assert.ok(source.includes("disabled={busy || chosen.length === 0 || typed !== want}"), "delete stays disabled until VMs are chosen and the text is typed");
assert.ok(source.includes('useEffect(() => { requestID.current = rid("borphans"); }, [picked, includeProtected, typed]);'), "a changed selection or confirmation is a new request");
assert.ok(source.includes("${base}/orphans"), "the panel loads Core's orphan query");
assert.ok(source.includes("Include protected recovery points"), "protected points are opt-in");
assert.ok(!source.includes("localStorage"), "nothing about the cleanup is kept in browser storage");
assert.ok(source.includes('className="check" style={{ justifyContent: "flex-start"'), "the protected checkbox sits beside its text");
assert.ok(source.includes('op.status === "queued"'), "the record is not requested before Core has planned the cleanup");
assert.ok(source.includes('if (found) setRecord(found)'), "a failed cleanup with a record still shows per-point results");
assert.ok(source.includes('else if (op.status === "failed") setRefusal('), "refusal is only for a pre-plan 404 / no record");
assert.ok(source.includes('if (op.status === "failed") return null;'), "a failed cleanup still reads its record; 404 means a pure refusal");
assert.ok(source.includes("misses.current >= 15"), "the poll gives up with an error instead of waiting forever");
assert.ok(source.includes('misses.current = 0; setPollError("");'), "a successful poll clears the earlier give-up error");
