// SPDX-License-Identifier: AGPL-3.0-only
// The host-local Updates screen: which actions the engine's reported state allows, and what is shown.
// Run with: node src/local-updates.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { LocalUpdates, localUpdateActions, localPhaseLabel } = await import(bundleTestModule(new URL("./local-updates.tsx", import.meta.url), "local-updates"));

const booted = { slot: "a", role: "booted", label: "2026.10.2", sequence: 2, root_hash: "aa" };
const previous = { slot: "b", role: "previous", label: "2026.10.1", sequence: 1, root_hash: "bb" };
const staged = { slot: "b", role: "staged", label: "2026.10.3", sequence: 3, root_hash: "cc" };
const idle = { running: false, slots: [booted, previous], channel: "stable", phase: "committed", rollback_floor: 1 };

const none = localUpdateActions(null);
assert.ok(Object.values(none).every(a => !a.enabled), "nothing is offered before the host has reported");

const current = localUpdateActions(idle);
assert.equal(current.check.enabled, true);
assert.equal(current.apply.enabled, false, "no newer release is known");
assert.equal(current.activate.enabled, false, "nothing is staged");
assert.equal(current.rollback.enabled, true, "a committed host can return to the retained release");
assert.equal(current.cancel.enabled, false);

assert.equal(localUpdateActions({ ...idle, available: { label: "2026.10.3", sequence: 3 } }).apply.enabled, true);
assert.equal(localUpdateActions({ ...idle, available: { label: "2026.10.2", sequence: 2 } }).apply.enabled, false, "the running release is not newer");

const waiting = localUpdateActions({ ...idle, slots: [booted, previous, staged], phase: "staged" });
assert.equal(waiting.activate.enabled, true);
assert.equal(waiting.rollback.enabled, false, "a staged host has nothing to roll back from");

const armed = localUpdateActions({ ...idle, slots: [booted, previous, staged], phase: "armed" });
assert.equal(armed.cancel.enabled, false, "cancel is refused once armed");
assert.equal(armed.apply.enabled, false);

const running = localUpdateActions({ ...idle, running: true, phase: "downloading" });
assert.equal(running.check.enabled, false);
assert.equal(running.cancel.enabled, true);

assert.equal(localPhaseLabel("staged"), "Staged; the next boot is unchanged");
assert.equal(localPhaseLabel("something-new"), "something-new");

const html = renderToStaticMarkup(createElement(LocalUpdates, { api: async () => idle, busy: false }));
assert.match(html, /Update channel/, "the channel picker is offered");
assert.match(html, /Download and stage/);
assert.match(html, /Reboot into update/);
console.log("local-updates ok");
