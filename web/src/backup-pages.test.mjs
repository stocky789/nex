// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { appendBackupPage, backupPage, collectBackupPageItems, refreshBackupPage } = await import(bundleTestModule(new URL("./backup-types.ts", import.meta.url), "backup-pages"));
const legacy = [{ id: "old" }];
assert.deepEqual(backupPage(legacy), { items: legacy, generation: "", total: 1 }, "older Core array remains a complete list");

const first = { items: [{ id: "new" }, { id: "middle" }], generation: "g1", total: 3, next_cursor: "cursor-1" };
const second = { items: [{ id: "middle" }, { id: "old" }], generation: "g1", total: 3 };
assert.deepEqual(appendBackupPage(first, second, point => point.id), { ...second, items: [{ id: "new" }, { id: "middle" }, { id: "old" }] }, "load more keeps order without duplicate boundary records");
const changed = { items: [{ id: "newest" }], generation: "g2", total: 1 };
assert.deepEqual(appendBackupPage(first, changed, point => point.id), changed, "generation change restarts from the new first page");
assert.deepEqual(refreshBackupPage({ ...first, items: [...first.items, { id: "old" }] }, first, true).items.map(item => item.id), ["new", "middle", "old"], "polling preserves loaded rows for the same filters and generation");
assert.deepEqual(refreshBackupPage({ ...first, items: [...first.items, { id: "old" }] }, { ...first, items: [{ id: "filtered" }] }, false).items, [{ id: "filtered" }], "a filter change drops previously loaded rows even when the generation matches");
const requests = [];
const history = await collectBackupPageItems({ items: [{ id: "recent" }], next_cursor: "older", generation: "g1", total: 2 }, async (cursor, generation) => {
  requests.push({ cursor, generation });
  return { items: [{ id: "older-active-test" }], generation, total: 2 };
});
assert.deepEqual(requests, [{ cursor: "older", generation: "g1" }]);
assert.deepEqual(history.map(item => item.id), ["recent", "older-active-test"], "selected point scans later run pages before deciding which test VMs remain active");
await assert.rejects(collectBackupPageItems(first, async () => changed), /inventory changed/, "a generation change during the scan must restart or fail closed");
