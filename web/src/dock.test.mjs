// SPDX-License-Identifier: AGPL-3.0-only
// The task strip counts what is running and what failed, and a failure that appears between
// polls is announced once. run with: node src/dock.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const { taskCounts, newFailures, readPreference, writePreference } = await import(bundleTestModule(new URL("./dock.ts", import.meta.url), "dock"));

assert.deepEqual(taskCounts([{ status: "pending" }, { status: "dispatched" }, { status: "failed" }, { status: "needs_attention" }, { status: "succeeded" }]), { running: 2, failed: 2 });
assert.deepEqual(taskCounts([]), { running: 0, failed: 0 });

const before = [{ id: "a", status: "pending" }, { id: "b", status: "failed" }];
assert.deepEqual(newFailures(before, [{ id: "a", status: "failed" }, { id: "b", status: "failed" }]), [{ id: "a", status: "failed" }], "only the transition is announced");
assert.deepEqual(newFailures(before, [{ id: "c", status: "needs_attention" }]), [{ id: "c", status: "needs_attention" }], "a task created and failed between polls is announced");
assert.deepEqual(newFailures([], [{ id: "b", status: "failed" }]), [], "the first load announces nothing");

globalThis.localStorage = undefined;
assert.equal(readPreference("nex.dock", true), true, "no storage: the fallback");
writePreference("nex.dock", false);
const store = new Map();
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
writePreference("nex.dock", false);
assert.equal(readPreference("nex.dock", true), false);
globalThis.localStorage = { getItem: () => { throw new Error("blocked"); }, setItem: () => { throw new Error("blocked"); } };
assert.equal(readPreference("nex.dock", true), true, "a throwing store is the fallback");
writePreference("nex.dock", true);
console.log("dock ok");
