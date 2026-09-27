// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { activeTestRestores } = await import(bundleTestModule(new URL("./backups.tsx", import.meta.url), "backups-test-restores", true));
const point = { id: "point-1", repository_id: "repo-1" };
const runs = [
  { operation_id: "older", kind: "backup-test-restore", status: "succeeded", recovery_point_id: "point-1", repository_id: "repo-1", new_vm_id: "test-older", host_id: "host-1", updated_at: "2026-09-01T00:00:00Z" },
  { operation_id: "newer", kind: "backup-test-restore", status: "succeeded", recovery_point_id: "point-1", repository_id: "repo-1", new_vm_id: "test-newer", host_id: "host-2", updated_at: "2026-09-02T00:00:00Z" },
  { operation_id: "other", kind: "backup-test-restore", status: "succeeded", recovery_point_id: "point-2", repository_id: "repo-1", new_vm_id: "other", updated_at: "2026-09-03T00:00:00Z" },
  { operation_id: "failed", kind: "backup-test-restore", status: "failed", recovery_point_id: "point-1", repository_id: "repo-1", new_vm_id: "failed", updated_at: "2026-09-04T00:00:00Z" },
];
const vms = [{ id: "test-older", pool_id: "pool-1", host_id: "host-1" }, { id: "test-newer", pool_id: "pool-1", host_id: "host-2" }, { id: "other", pool_id: "pool-1", host_id: "host-3" }];

assert.deepEqual(activeTestRestores(runs, point, vms, "pool-1").map(({ vm }) => vm.id), ["test-newer", "test-older"]);
assert.deepEqual(activeTestRestores(runs, point, vms.filter(vm => vm.id !== "test-newer"), "pool-1").map(({ vm }) => vm.id), ["test-older"], "cleanup of the newest test VM must leave the older one manageable");
assert.deepEqual(activeTestRestores(runs, point, vms, "other-pool"), []);
assert.deepEqual(activeTestRestores(null, point, vms, "pool-1"), [], "missing run inventory must not expose verification or cleanup");
assert.deepEqual(activeTestRestores(runs, point, vms.map(vm => vm.id === "test-newer" ? { ...vm, host_id: "host-3" } : vm), "pool-1").map(({ vm }) => vm.id), ["test-older"], "a same-ID VM on a different host is not the recorded test restore");
assert.deepEqual(activeTestRestores(runs.map(run => run.operation_id === "newer" ? { ...run, host_id: "" } : run), point, vms, "pool-1").map(({ vm }) => vm.id), ["test-older"], "a run without host provenance cannot enable VM actions");
