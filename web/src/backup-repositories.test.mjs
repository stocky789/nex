// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./backup-repositories.tsx", import.meta.url), "utf8");
const create = source.split("const onCreate = ")[1]?.split("const onTest = ")[0];
assert.ok(create, "repository create handler exists");
assert.ok(create.includes('createID.current ||= rid("brepo")'), "a lost reply retains the repository ID");
assert.ok(create.includes('request_id: requestID(createKey, "brepo")'), "a lost reply retains the request ID");
assert.ok(create.indexOf("resetForm();") > create.indexOf("await api<"), "successful submission settles held IDs");
assert.ok(source.includes("if (working || createUncertain) return;"), "uncertain registration cannot be closed and retried with a different body");
assert.equal((source.match(/disabled=\{Boolean\(working\) \|\| createUncertain\}/g) || []).length, 4, "all create fields remain frozen until exact retry settles");
