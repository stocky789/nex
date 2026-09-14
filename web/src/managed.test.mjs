// SPDX-License-Identifier: AGPL-3.0-only
// BUG-0080 regression: a pool registration form belongs to the tab that opened it. Selecting
// another tab must hide it instead of leaving it stacked above unrelated content until the
// operator clicks Close. There is no test harness in web/ (no vitest/jest), so this bundles the
// real module with the already-installed esbuild and renders it with react-dom/server;
// run with: node src/managed.test.mjs
import { buildSync, transformSync } from "esbuild";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

// The bundle keeps React external and is written inside web/node_modules so its bare imports
// resolve to the same copy this file uses; two React instances cannot render together.
const outdir = fileURLToPath(new URL("../node_modules/.cache/nex", import.meta.url));
mkdirSync(outdir, { recursive: true });
const outfile = `${outdir}/managed.test-bundle.mjs`;
buildSync({
  entryPoints: [fileURLToPath(new URL("./managed.tsx", import.meta.url))],
  bundle: true, format: "esm", jsx: "automatic", target: "es2022", outfile,
  external: ["react", "react/jsx-runtime", "react-dom", "react-dom/server"],
});

// A restored pending request is how a form can be open on first render, which is also the
// reload path that first showed the stale panel.
let pending = {};
globalThis.sessionStorage = { getItem: () => JSON.stringify(pending), setItem() {} };
const { ManagedVMs, formTab } = await import(`file://${outfile}`);

const props = {
  account: "operator", poolID: "pool-1", poolName: "pool", hosts: [], storage: [], networks: [],
  media: [], vms: [], api: async () => ({}), reload: async () => {}, setTab: () => {},
  unavailable: false, uploading: false, startIsoUpload: async () => ({}), createVM: false,
  onCreateVMShown: () => {},
};
const tabs = ["General", "Virtual machines", "Storage", "Networking", "ISO library", "Activity"];

for (const [form, ownTab, heading] of [["network", "Networking", "Register guest network"], ["iso", "ISO library", "Upload guest ISO"], ["vm", "Virtual machines", "Create VM · 1 of 6"]]) {
  pending = { [form]: { payload: "{}", id: "request-1" } };
  for (const tab of tabs) {
    const html = renderToStaticMarkup(createElement(ManagedVMs, { ...props, tab }));
    // Still mounted everywhere: entries and wizard progress must survive a trip to another tab.
    assert.ok(html.includes(heading), `${form} form on ${tab}: expected ${heading} to stay mounted`);
    assert.equal(/ hidden=""/.test(html), tab !== ownTab, `${form} form on ${tab}: wrong visibility`);
  }
}

assert.equal(formTab(""), "", "no open form matches no tab");
assert.equal(formTab("storage"), "", "storage registration lives in its own tab-scoped view");

// BUG-0152: adopted Core outcomes retain their original request/status/error, but the
// Controller's updated_at is when that history was re-observed. Only the exact Core
// adoption identity gets the recovered-history time treatment; ordinary request IDs do not.
const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");
const timingStart = mainSource.indexOf("const date =");
const timingEnd = mainSource.indexOf("const size =", timingStart);
assert.ok(timingStart >= 0 && timingEnd > timingStart, "task timestamp helper is present");
const timingCode = transformSync(mainSource.slice(timingStart, timingEnd), { loader: "tsx", format: "esm" }).code;
const timingFile = `${outdir}/main-task-timestamp.test.mjs`;
writeFileSync(timingFile, timingCode);
const { taskTimestamp } = await import(`file://${timingFile}?${Date.now()}`);
const adopted = { id: "historic", host_id: "core-a", request_id: "adopt-outcome:core-a:historic", updated_at: "2026-09-01T00:00:00Z" };
assert.equal(taskTimestamp(adopted).reobserved, true);
assert.equal(taskTimestamp({ ...adopted, request_id: "operator-adopt-outcome:core-a:historic" }).reobserved, false);
assert.equal(taskTimestamp({ ...adopted, request_id: "adopt-outcome:core-b:historic" }).reobserved, false);
assert.equal(taskTimestamp({ ...adopted, request_id: "adopt-outcome:core-a:other-operation" }).reobserved, false);
assert.match(mainSource, /Reobserved from Core history/);
assert.match(mainSource, /Original outcome time was not reported\./);

console.log("managed.test.mjs: ok");
