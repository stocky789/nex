// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./backups.tsx", import.meta.url), "utf8");
const open = source.split("const openRestore = ")[1]?.split("const closeBackup = ")[0];
const submit = source.split("const submitRestore = ")[1]?.split("const toggleProtect = ")[0];
assert.ok(open && submit, "restore handlers exist");
assert.ok(open.includes("restoreDrafts.current[restoreKey(mode, point.id)]"), "reopening the same restore recovers its submitted draft");
assert.ok(open.includes("setRestoreStep(draft ? 3 : 1)"), "uncertain retry reopens at the frozen review step");
assert.ok(submit.includes("const key = restoreKey(restoreMode, selected.id)"), "request identity is scoped to mode and recovery point");
assert.ok(submit.includes("restoreDrafts.current[key] ="), "the exact body fields are saved before submitting");
assert.ok(submit.includes("request_id: requestID(key,"), "exact retry holds its operation ID");
assert.ok(submit.includes("delete restoreDrafts.current[key]"), "a settled response releases the held draft");
assert.ok(source.includes("disabled={Boolean(working) || uncertainRestore}"), "uncertain restore form fields remain frozen");
