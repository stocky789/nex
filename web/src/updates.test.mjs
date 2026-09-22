// SPDX-License-Identifier: AGPL-3.0-only
// Following an update outlives the service restart it causes (BUG-0442).
// run with: node src/updates.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const { followUpdate } = await import(bundleTestModule(new URL("./updates.ts", import.meta.url), "updates"));
const sleep = async () => {};

// The Controller is stopped for the package install, so fetch rejects with no HTTP status.
const restart = [new TypeError("Failed to fetch"), new TypeError("Failed to fetch"), { phase: "restarting" }, { phase: "done", message: "Update complete" }];
const seen = [];
const done = await followUpdate(async () => { const next = restart.shift(); if (next instanceof Error) throw next; return next; }, st => seen.push(st.phase), sleep);
assert.equal(done.phase, "done", "a refused connection during the restart is not a failure");
assert.deepEqual(seen, ["restarting", "done"]);

const failed = await followUpdate(async () => ({ phase: "failed", error: "apt failed" }), () => {}, sleep);
assert.equal(failed.error, "apt failed");

await assert.rejects(followUpdate(async () => { throw Object.assign(new Error("update check failed"), { status: 500 }); }, () => {}, sleep), /update check failed/, "an HTTP error still surfaces");

assert.equal(await followUpdate(async () => ({ phase: "installing" }), () => {}, sleep), undefined, "gives up after the bound");
console.log("ok");
