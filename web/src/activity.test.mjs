// SPDX-License-Identifier: AGPL-3.0-only
// Activity popdown: running tasks first, then recent, capped; the pill opens the list not #/tasks.
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";

const { recentActivity, ActivityMenu } = await import(bundleTestModule(new URL("./activity.tsx", import.meta.url), "activity"));

const ops = [
  { id: "a", host_id: "h1", kind: "vm-power", request_id: "r1", status: "succeeded", created_at: "2026-09-24T10:00:00Z", updated_at: "2026-09-24T10:05:00Z", actor: "admin" },
  { id: "b", host_id: "h1", kind: "vm-create", request_id: "r2", status: "pending", created_at: "2026-09-24T10:01:00Z", updated_at: "2026-09-24T10:02:00Z", actor: "admin" },
  { id: "c", host_id: "h2", kind: "inventory-refresh", request_id: "r3", status: "failed", created_at: "2026-09-24T09:00:00Z", updated_at: "2026-09-24T10:06:00Z", actor: "admin" },
  { id: "d", host_id: "h2", kind: "vm-update", request_id: "r4", status: "queued", created_at: "2026-09-24T10:03:00Z", updated_at: "2026-09-24T10:03:00Z", actor: "admin" },
];
assert.deepEqual(recentActivity(ops).map(o => o.id), ["d", "b", "c", "a"], "unfinished first, then newest updated");
assert.equal(recentActivity([...ops, ...ops.map((o, i) => ({ ...o, id: `x${i}` }))]).length, 8, "popdown shows at most eight");

const hosts = [{ id: "h1", pool_id: "p", hostname: "core-a", revoked: false, connection: "fresh", last_seen: null, inventory: null }];
const html = renderToStaticMarkup(createElement(ActivityMenu, {
  operations: ops,
  hosts,
  upload: null,
  running: 2,
  onCancelUpload() {},
}));
assert.match(html, /class="menu activity menu-end"/);
assert.match(html, /2 tasks running\. Show tasks/);
assert.match(html, />Create VM</);
assert.match(html, />Open Tasks</);
assert.match(html, /href="#\/tasks"/);
assert.doesNotMatch(html, /<a class="activity"/, "the pill is a disclosure, not a link to Tasks");

const idle = renderToStaticMarkup(createElement(ActivityMenu, {
  operations: [],
  hosts: [],
  upload: null,
  running: 0,
  onCancelUpload() {},
}));
assert.match(idle, /No tasks running\. Show tasks/);
assert.match(idle, /No tasks yet/);
assert.match(idle, /href="#\/tasks"/);

const uploading = renderToStaticMarkup(createElement(ActivityMenu, {
  operations: [],
  hosts: [],
  upload: { name: "debian.iso", loaded: 50, total: 100, percent: 50, rate: 1024, state: "uploading", message: "", actor: "admin", updated_at: "2026-09-24T10:00:00Z" },
  running: 1,
  onCancelUpload() {},
}));
assert.match(uploading, /Upload ISO/);
assert.match(uploading, /debian\.iso · 50%/);
assert.match(uploading, />Cancel</);
console.log("activity ok");
