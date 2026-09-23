// SPDX-License-Identifier: AGPL-3.0-only
// One status vocabulary: every value the Controller and Core report maps to one of five tones,
// and the square dot-and-text badge is the only badge. run with: node src/ui.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";

const { badgeTone, Badge, Banner, MissingPanel, LoadError, LoadErrorAlert, Panel, Section, ScreenBoundary } = await import(bundleTestModule(new URL("./ui.tsx", import.meta.url), "ui"));

for (const v of ["fresh", "healthy", "succeeded", "current", "available", "confirmed", "connected", "ready", "in_sync", "passed", "verified", "up"]) assert.equal(badgeTone(v), "ok", v);
for (const v of ["stale", "degraded", "pending", "dispatched", "needs_attention", "behind", "unverified", "uncertain", "warning", "disconnected", "unresponsive", "timeout", "awaiting_fencing", "staged", "applied"]) assert.equal(badgeTone(v), "warn", v);
for (const v of ["failed", "error", "revoked", "blocked", "down", "faulted", "rejected"]) assert.equal(badgeTone(v), "bad", v);
for (const v of ["queued", "uploading", "master", "Pool master", "provisioned"]) assert.equal(badgeTone(v), "info", v);
for (const v of ["unknown", "", "cancelled", "member", "not reported", "something-new"]) assert.equal(badgeTone(v), "neutral", v);

const html = renderToStaticMarkup(createElement(Badge, { value: "needs_attention" }));
assert.match(html, /class="badge warn"/);
assert.match(html, />Needs attention</, "the badge text is the operator label, never the raw enum");
assert.match(renderToStaticMarkup(createElement(Badge, { value: "master", label: "Pool master" })), />Pool master</);
assert.match(renderToStaticMarkup(createElement(Banner, { tone: "warn", role: "status" }, "x")), /class="banner warn" role="status"/);
// The load error and its Retry: shown on the page, and inside any open panel, which covers it.
let retried = 0;
const load = { error: "Inventory may be out of date.", busy: false, onRetry: () => retried++ };
const retry = LoadErrorAlert(load).props.children.find(c => c?.type === "button");
assert.equal(retry.props.children, "Retry");
retry.props.onClick();
assert.equal(retried, 1, "Retry runs the reload it was given");
const panel = (value) => renderToStaticMarkup(createElement(LoadError.Provider, { value }, createElement(Panel, { icon: "host", title: "node-1", onClose() {} }, "body")));
assert.match(panel(load), /slideover-body"[^>]*><div class="alert" role="alert">Inventory may be out of date\. <button type="button" class="sm">Retry<\/button><\/div>body/, "an open panel carries the load error and Retry");
assert.doesNotMatch(panel({ ...load, error: "" }), /Retry/);
assert.equal(MissingPanel({ icon: "host", noun: "Host", unavailable: true, loading: false, onClose() {} }).props.title, "Host unavailable");
assert.equal(MissingPanel({ icon: "vm", noun: "VM", unavailable: false, loading: false, onClose() {} }).props.title, "VM not found");

// Each section is a region named by its title alone, not its count or buttons.
const section = renderToStaticMarkup(createElement(Section, { icon: "host", title: "Members", count: 3, actions: createElement("button", null, "Refresh") }, "x"));
const [, labelID] = section.match(/aria-labelledby="([^"]+)"/);
assert.match(section, new RegExp(`<span id="${labelID}">Members</span>`));

// A screen that throws on a bad report shows a message instead of blanking the console.
const boundary = new ScreenBoundary({ children: "fine" });
assert.equal(boundary.render(), "fine");
boundary.state = ScreenBoundary.getDerivedStateFromError(new TypeError("x.match is not a function"));
assert.match(renderToStaticMarkup(boundary.render()), /This screen could not be shown.*x\.match is not a function/);
console.log("ui ok");
