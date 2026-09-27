// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { replacementTarget } = await import(bundleTestModule(new URL("./backups.tsx", import.meta.url), "backups-replace", true));
const point = { source_vm_id: "vm-original" };
const vms = [{ id: "vm-other", name: "Other guest" }];

assert.equal(replacementTarget(point, vms), undefined, "a recovery point without its original VM cannot be replaced");
assert.equal(replacementTarget(null, vms), undefined);

vms.push({ id: "vm-original", name: "Named guest" });
assert.deepEqual(replacementTarget(point, vms), { id: "vm-original", name: "Named guest" }, "confirmation uses the current VM name");
