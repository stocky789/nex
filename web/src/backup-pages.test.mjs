// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { appendBackupPage, collectBackupPageItems, loadRecoveryPoints, maxBackupPages, pageURL, refreshLoadedPages, refreshRunHistory } = await import(bundleTestModule(new URL("./backup-types.ts", import.meta.url), "backup-pages"));

const first = { items: [{ id: "new" }, { id: "middle" }], generation: "g1", total: 3, next_cursor: "cursor-1" };
const second = { items: [{ id: "middle" }, { id: "old" }], generation: "g1", total: 3 };
assert.deepEqual(appendBackupPage(first, second, point => point.id), { ...second, items: [{ id: "new" }, { id: "middle" }, { id: "old" }] }, "load more keeps order without duplicate boundary records");
const changed = { items: [{ id: "newest" }], generation: "g2", total: 1 };
assert.deepEqual(appendBackupPage(first, changed, point => point.id), changed, "generation change restarts from the new first page");
assert.equal(pageURL("pools/p/backup-catalog", "c 1", "g"), "pools/p/backup-catalog?cursor=c%201&generation=g");
assert.equal(pageURL("pools/p/backup-catalog?vm_id=a", "c", "g"), "pools/p/backup-catalog?vm_id=a&cursor=c&generation=g");

const requests = [];
const history = await collectBackupPageItems({ items: [{ id: "recent" }], next_cursor: "older", generation: "g1", total: 2 }, async (cursor, generation) => {
  requests.push({ cursor, generation });
  return { items: [{ id: "older-active-test" }], generation, total: 2 };
});
assert.deepEqual(requests, [{ cursor: "older", generation: "g1" }]);
assert.deepEqual(history.map(item => item.id), ["recent", "older-active-test"], "selected point scans later run pages before deciding which test VMs remain active");
await assert.rejects(collectBackupPageItems(first, async () => changed), /inventory changed/, "a generation change during the scan must restart or fail closed");

// A misbehaving Core cannot make a walk run forever.
await assert.rejects(collectBackupPageItems({ items: [{ id: "a" }], next_cursor: "same", generation: "g", total: 100 }, async (cursor, generation) => ({ items: [{ id: "b" }], generation, total: 100, next_cursor: cursor })), /repeated a cursor/, "a repeating cursor stops the walk");
let counter = 0;
await assert.rejects(collectBackupPageItems({ items: [{ id: "a" }], next_cursor: "0", generation: "g", total: 1e9 }, async (_cursor, generation) => ({ items: [{ id: "x" }], generation, total: 1e9, next_cursor: String(++counter) })), /too large/, "an endless history stops at the page cap");
assert.equal(counter, maxBackupPages - 1, "the cap bounds the number of relayed page reads");
await assert.rejects(collectBackupPageItems({ items: [{ id: "a" }], next_cursor: "1", generation: "g", total: 1 }, async (_cursor, generation) => ({ items: [{ id: "b" }], generation, total: 1 })), /exceeded its reported total/, "more rows than Core's total is refused");
await assert.rejects(collectBackupPageItems({ items: [{ id: "a" }], next_cursor: "1", generation: "g", total: 5 }, async (_cursor, generation) => ({ items: [], generation, total: 5, next_cursor: "2" })), /empty page/);

const point = (id, vm, extra = {}) => ({ id, source_vm_id: vm, repository_id: "repo-1", ...extra });
const paths = [];
const pagedAPI = async path => {
  paths.push(path);
  return path.includes("cursor=") ? { items: [point("a2", "vm-a")], generation: "g1", total: 2 } : { items: [point("a1", "vm-a")], generation: "g1", total: 2, next_cursor: "1" };
};
assert.deepEqual((await loadRecoveryPoints(pagedAPI, "pool 1", { vm_id: "vm-a" })).map(p => p.id), ["a1", "a2"], "a paged Core is read to the last page");
assert.deepEqual(paths, ["pools/pool%201/backup-catalog?vm_id=vm-a", "pools/pool%201/backup-catalog?vm_id=vm-a&cursor=1&generation=g1"], "the VM filter is sent to Core on every page");

// A mock Core with 2 points per page whose generation covers ids only, like Core's: protection
// changes do not move it. After "Load more", a refresh must show the new attributes on every
// loaded row, including those beyond the first page.
const core = { protectedIDs: new Set(), ids: ["p1", "p2", "p3", "p4", "p5"], generation: "g1", reads: [] };
const corePage = (cursor, generation) => {
  core.reads.push(cursor ?? "first");
  if (generation && generation !== core.generation) throw Object.assign(new Error("generation changed"), { status: 409 });
  const start = cursor ? Number(cursor) : 0;
  const items = core.ids.slice(start, start + 2).map(id => point(id, "vm-a", { protected: core.protectedIDs.has(id) }));
  return { items, generation: core.generation, total: core.ids.length, ...(start + 2 < core.ids.length ? { next_cursor: String(start + 2) } : {}) };
};
const request = async (cursor, generation) => corePage(cursor, generation);
const loadedSpan = async loaded => refreshLoadedPages(corePage(), loaded, request);

let shown = await loadedSpan(0);
assert.deepEqual(shown.items.map(p => p.id), ["p1", "p2"], "a poll with nothing extra loaded reads only the first page");
assert.equal(shown.next_cursor, "2");
shown = appendBackupPage(shown, corePage("2", shown.generation), p => p.id);
shown = appendBackupPage(shown, corePage("4", shown.generation), p => p.id);
assert.deepEqual(shown.items.map(p => p.id), ["p1", "p2", "p3", "p4", "p5"]);
core.protectedIDs.add("p4");
core.reads.length = 0;
const refreshed = await loadedSpan(shown.items.length);
assert.deepEqual(refreshed.items.map(p => [p.id, p.protected]), [["p1", false], ["p2", false], ["p3", false], ["p4", true], ["p5", false]], "a poll refreshes protection on rows loaded beyond the first page");
assert.equal(refreshed.next_cursor, undefined, "the refreshed span keeps Core's end of list");
assert.deepEqual(core.reads, ["first", "2", "4"], "the refresh re-reads exactly the loaded span");
const partial = await loadedSpan(3);
assert.deepEqual(partial.items.map(p => p.id), ["p1", "p2", "p3", "p4"], "the refresh covers the loaded rows and keeps the cursor for the rest");
assert.equal(partial.next_cursor, "4");

// If Core's inventory moves mid-refresh, the fresh first page wins and the walk restarts from it.
core.reads.length = 0;
const moving = await refreshLoadedPages(corePage(), 5, async cursor => { core.generation = "g2"; return corePage(cursor, "g1"); });
assert.equal(moving.generation, "g1");
assert.deepEqual(moving.items.map(p => p.id), ["p1", "p2"], "a 409 during the refresh falls back to the first page");

// Run history: a poll reads only the newest pages while older settled runs are cached.
const run = (id, status, extra = {}) => ({ operation_id: id, status, kind: "scheduled", repository_id: "r", updated_at: "2026-01-01", ...extra });
const runCore = { runs: [], reads: 0 };
const runPage = (cursor, generation) => {
  runCore.reads++;
  const start = cursor ? Number(cursor) : 0;
  const items = runCore.runs.slice(start, start + 2);
  return { items, generation: generation ?? "g", total: runCore.runs.length, ...(start + 2 < runCore.runs.length ? { next_cursor: String(start + 2) } : {}) };
};
const runRequest = async (cursor, generation) => runPage(cursor, generation);
runCore.runs = ["r6", "r5", "r4", "r3", "r2", "r1"].map(id => run(id, "succeeded"));
let cached = await refreshRunHistory(null, runPage(), runRequest);
assert.equal(cached.length, 6, "the first load walks the whole history");
runCore.reads = 0;
runCore.runs = [run("r7", "pending"), ...runCore.runs];
cached = await refreshRunHistory(cached, runPage(), runRequest);
assert.deepEqual(cached.map(r => r.operation_id), ["r7", "r6", "r5", "r4", "r3", "r2", "r1"], "a new run is merged ahead of the cached history");
assert.equal(runCore.reads, 2, "a poll with one new run reads two pages, not the whole history");
runCore.reads = 0;
runCore.runs = [run("r7", "succeeded"), ...runCore.runs.slice(1)];
cached = await refreshRunHistory(cached, runPage(), runRequest);
assert.equal(cached[0].status, "succeeded", "a run that settled is refreshed");
runCore.reads = 0;
runCore.runs = runCore.runs.map(r => r.operation_id === "r2" ? run("r2", "failed") : r);
cached = await refreshRunHistory(cached.map(r => r.operation_id === "r2" ? run("r2", "pending") : r), runPage(), runRequest);
assert.equal(cached.find(r => r.operation_id === "r2").status, "failed", "an old unsettled run is re-read until it settles");
runCore.reads = 0;
runCore.runs = runCore.runs.filter(r => r.operation_id !== "r5" && r.operation_id !== "r6");
cached = await refreshRunHistory(cached, runPage(), runRequest);
assert.deepEqual(cached.map(r => r.operation_id), ["r7", "r4", "r3", "r2", "r1"], "runs Core removed (a forgotten repository) do not linger in the cache");
