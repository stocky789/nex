// SPDX-License-Identifier: AGPL-3.0-only
// Host image updates panel: which actions the host's reported state allows, how a request is
// submitted and replayed, and what the panel shows. Same harness as host-network.test.mjs.
// Run with: node src/image-updates.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { ImageUpdates, imageActions, imageHosts, submitImageAction, imagePhaseLabel } =
  await import(bundleTestModule(new URL("./image-updates.tsx", import.meta.url), "image-updates"));

const booted = { label: "2026.10.2", sequence: 2, slot: "a" };
const previous = { label: "2026.10.1", sequence: 1, slot: "b" };
const image = (over = {}) => ({ booted, previous, phase: "committed", rollback_floor: 1, state_format: 3, channel: "stable", ...over });
const enabled = (a) => Object.entries(a).filter(([, v]) => v.enabled).map(([k]) => k).sort();

// Committed with a newer release known: check, update and roll back; nothing to activate or cancel.
assert.deepEqual(enabled(imageActions(image({ available: { label: "2026.10.3", sequence: 3 } }), "fresh")), ["apply", "check", "rollback"]);
// Nothing newer: no update offered, and the reason is given.
const idle = imageActions(image({ available: booted }), "fresh");
assert.equal(idle.apply.enabled, false);
assert.match(idle.apply.why, /No newer release/);
// Staged: activation is the only way to the next boot; staging armed nothing.
assert.deepEqual(enabled(imageActions(image({ phase: "staged", staged: { label: "2026.10.3", sequence: 3, slot: "b" }, available: { label: "2026.10.3", sequence: 3 } }), "fresh")), ["activate", "apply", "check"]);
// Armed: cancel is refused by the host once armed, so the panel does not offer it; reboot or roll back.
const armed = imageActions(image({ phase: "armed", staged: { label: "2026.10.3", sequence: 3 } }), "fresh");
assert.equal(armed.cancel.enabled, false);
assert.match(armed.cancel.why, /armed/);
assert.equal(armed.apply.enabled, false);
// A running operation allows only cancel.
const downloading = image({ phase: "downloading", available: { label: "2026.10.3", sequence: 3 }, operation: { id: "req-apply-0001", kind: "apply", phase: "downloading", started_at: "2026-10-02T00:00:00Z" } });
assert.deepEqual(enabled(imageActions(downloading, "fresh")), ["cancel"]);
// A host that is not freshly connected, or has reported nothing, gets no actions.
assert.deepEqual(enabled(imageActions(downloading, "stale")), []);
assert.deepEqual(enabled(imageActions(undefined, "fresh")), []);
// After an automatic fall-back the failed release is "previous": rolling back to it is not offered.
assert.equal(imageActions(image({ phase: "rolled_back" }), "fresh").rollback.enabled, false);

// A request keeps its identity until its content changes, so a lost reply replays instead of repeating.
const calls = [];
const api = async (path, method, body) => { calls.push({ path, method, body }); return { operation: { id: "op-1", status: "queued" } }; };
const held = { id: "", signature: "" };
await submitImageAction(api, "host-1", "apply", held, { sequence: 3 });
await submitImageAction(api, "host-1", "apply", held, { sequence: 3 });
assert.equal(calls[0].body.request_id, calls[1].body.request_id, "a retry reuses the request_id");
assert.deepEqual({ ...calls[0].body, request_id: "x" }, { sequence: 3, activate: "stage_only", running_vms: "refuse", request_id: "x" });
assert.equal(calls[0].path, "hosts/host-1/updates/apply");
assert.equal(calls[0].method, "POST");
await submitImageAction(api, "host-1", "apply", held, { sequence: 4 });
assert.notEqual(calls[2].body.request_id, calls[0].body.request_id, "a changed request gets a new identity");
// Guests are shut down only when the operator says so; check carries neither policy nor sequence.
await submitImageAction(api, "host-1", "activate", { id: "", signature: "" }, { shutdownVMs: true });
assert.equal(calls[3].body.running_vms, "shutdown");
await submitImageAction(api, "host-1", "check", { id: "", signature: "" }, { shutdownVMs: true, sequence: 9 });
assert.deepEqual(Object.keys(calls[4].body), ["request_id"]);
assert.match(calls[0].body.request_id, /^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$/, "the id is one the host engine accepts");

// The panel lists image hosts only and shows what the host reported.
const host = (id, over = {}) => ({ id, pool_id: "pool-1", hostname: id, revoked: false, connection: "fresh", last_seen: null, capabilities: [], inventory: null, ...over });
const hosts = [
  host("classic-host"),
  host("image-host", { capabilities: ["image-update-v1"], inventory: { image: image({ available: { label: "2026.10.3", sequence: 3 }, failure: { code: "boot_failed", message: "the new release failed its boot check" } }) } }),
  host("revoked-image", { revoked: true, capabilities: ["image-update-v1"] }),
];
assert.deepEqual(imageHosts(hosts).map(h => h.id), ["image-host"]);
const html = renderToStaticMarkup(createElement(ImageUpdates, { hosts, api, busy: false }));
assert.match(html, /Image updates for image-host/);
assert.doesNotMatch(html, /classic-host|revoked-image/);
assert.match(html, /2026\.10\.2 \(sequence 2, slot a\)/, "running release and slot");
assert.match(html, /2026\.10\.1 \(sequence 1, slot b\)/, "previous release is shown for roll-back");
assert.match(html, /2026\.10\.3 \(sequence 3\)/, "available release");
assert.match(html, /Sequence 1/, "rollback floor");
assert.match(html, /returned to the previous release[\s\S]*boot_failed: the new release failed its boot check/);
assert.equal(renderToStaticMarkup(createElement(ImageUpdates, { hosts: [hosts[0]], api, busy: false })), "", "a pool of classic hosts shows no image panel");
assert.equal(imagePhaseLabel("staged"), "Staged; the next boot is unchanged");
