// SPDX-License-Identifier: AGPL-3.0-only
// One status vocabulary: every value the Controller and Core report maps to one of five tones,
// and the square dot-and-text badge is the only badge. run with: node src/ui.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";

const { badgeTone, Badge, Banner } = await import(bundleTestModule(new URL("./ui.tsx", import.meta.url), "ui"));

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
console.log("ui ok");
