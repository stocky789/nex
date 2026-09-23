// SPDX-License-Identifier: AGPL-3.0-only
// The top bar counts what is running and what failed, and a failure that appears between
// polls is announced once. run with: node src/dock.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const { taskCounts, newFailures, readPreference, writePreference } = await import(bundleTestModule(new URL("./dock.ts", import.meta.url), "dock"));

assert.deepEqual(taskCounts([{ status: "queued" }, { status: "pending" }, { status: "dispatched" }, { status: "failed" }, { status: "needs_attention" }, { status: "succeeded" }]), { running: 3, failed: 2 }, "a queued task is not finished, so it counts");
assert.deepEqual(taskCounts([]), { running: 0, failed: 0 });

const before = [{ id: "a", status: "pending" }, { id: "b", status: "failed" }];
assert.deepEqual(newFailures(before, [{ id: "a", status: "failed" }, { id: "b", status: "failed" }]), [{ id: "a", status: "failed" }], "only the transition is announced");
assert.deepEqual(newFailures(before, [{ id: "c", status: "needs_attention" }]), [{ id: "c", status: "needs_attention" }], "a task created and failed between polls is announced");
assert.deepEqual(newFailures([], [{ id: "b", status: "failed" }]), [], "the first load announces nothing");

globalThis.localStorage = undefined;
assert.equal(readPreference("nex.theme"), "", "no storage: nothing kept");
writePreference("nex.theme", "dark");
const store = new Map();
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
assert.equal(readPreference("nex.theme"), "");
writePreference("nex.theme", "dark");
assert.equal(readPreference("nex.theme"), "dark");
globalThis.localStorage = { getItem: () => { throw new Error("blocked"); }, setItem: () => { throw new Error("blocked"); } };
assert.equal(readPreference("nex.theme"), "", "a throwing store keeps nothing");
writePreference("nex.theme", "light");
console.log("dock ok");
