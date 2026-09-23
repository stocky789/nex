// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { loadHostStats, peekHostStats, putHostStats } = await import(
  bundleTestModule(new URL("./stats-cache.ts", import.meta.url), "stats-cache")
);

const history = (id) => ({
  object: "host", id, range: "10m", step_seconds: 5, start: "2026-09-23T00:00:00Z", series: [],
});

let calls = 0;
const api = async (path) => {
  calls++;
  const id = path.split("/")[1];
  return history(id);
};

const first = await loadHostStats(api, ["h1", "h2", "h1"], "10m");
assert.equal(calls, 2, "duplicate ids share one request");
assert.ok(first.h1 && first.h2);

calls = 0;
const second = await loadHostStats(api, ["h1"], "10m");
assert.equal(calls, 0, "fresh cache skips the network");
assert.equal(second.h1.id, "h1");
assert.ok(peekHostStats("h1", "10m"));

putHostStats("h3", "10m", history("h3"));
assert.equal(peekHostStats("h3", "10m")?.id, "h3");
assert.equal(peekHostStats("h3", "24h"), null);

console.log("stats-cache ok");
