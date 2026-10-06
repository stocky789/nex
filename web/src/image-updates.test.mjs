// SPDX-License-Identifier: AGPL-3.0-only
// Host image updates panel: which actions the host's reported state allows, how a request is
// submitted and replayed, and what the panel shows. Same harness as host-network.test.mjs.
// Run with: node src/image-updates.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import React, { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { ImageUpdates, ImageUpdateBadge, imageUpdateNotice, imageActions, imageHosts, submitImageAction, submitImageSettings, imagePhaseLabel, imageSnapshot, reportStamp, stillFollowing } =
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
  host("plain-host"),
  host("image-host", { capabilities: ["image-update-v1"], inventory: { image: image({ available: { label: "2026.10.3", sequence: 3 }, failure: { code: "boot_failed", message: "the new release failed its boot check" } }) } }),
  host("revoked-image", { revoked: true, capabilities: ["image-update-v1"] }),
];
assert.deepEqual(imageHosts(hosts).map(h => h.id), ["image-host"]);

// Update notices come only from the host's own report: newer available, staged, armed; nothing otherwise.
const withImage = (over, h = {}) => host("n", { inventory: { image: image(over) }, ...h });
const v3 = { label: "2026.10.3", sequence: 3 };
assert.equal(imageUpdateNotice(withImage({ available: v3 }))?.label, "Update available");
assert.equal(imageUpdateNotice(withImage({ available: booted })), null, "not newer than the booted release");
assert.equal(imageUpdateNotice(withImage({})), null, "nothing reported as available");
assert.equal(imageUpdateNotice(host("none")), null, "no inventory");
assert.equal(imageUpdateNotice(withImage({ available: v3 }, { revoked: true })), null);
assert.equal(imageUpdateNotice(withImage({ phase: "staged", staged: { ...v3, slot: "b" }, available: v3 }))?.kind, "staged");
assert.equal(imageUpdateNotice(withImage({ phase: "committed", staged: v3, available: v3 })), null, "a staged release is not offered again");
assert.equal(imageUpdateNotice(withImage({ phase: "armed", staged: v3, available: v3 }))?.label, "Update pending reboot");
const staleNotice = imageUpdateNotice(withImage({ available: v3 }, { connection: "stale" }));
assert.equal(staleNotice.current, false);
assert.match(staleNotice.label, /last report/, "a stale report says so");
assert.match(renderToStaticMarkup(createElement(ImageUpdateBadge, { host: withImage({ available: v3 }) })), /badge info">Update available</);
assert.equal(renderToStaticMarkup(createElement(ImageUpdateBadge, { host: withImage({}) })), "");
const html = renderToStaticMarkup(createElement(ImageUpdates, { hosts, api, busy: false }));
assert.match(html, /Image updates for image-host/);
assert.doesNotMatch(html, /plain-host|revoked-image/);
assert.match(html, /2026\.10\.2 \(sequence 2, slot a\)/, "running release and slot");
assert.match(html, /2026\.10\.1 \(sequence 1, slot b\)/, "previous release is shown for roll-back");
assert.match(html, /2026\.10\.3 \(sequence 3\)/, "available release");
assert.match(html, /Sequence 1/, "rollback floor");
assert.match(html, /returned to the previous release[\s\S]*boot_failed: the new release failed its boot check/);
assert.equal(renderToStaticMarkup(createElement(ImageUpdates, { hosts: [hosts[0]], api, busy: false })), "", "a pool of hosts that offer no image-update capability shows no image panel");
assert.equal(imagePhaseLabel("staged"), "Staged; the next boot is unchanged");

// A new host report replaces a view fetched earlier; a fetched view is used only while the report it
// was read against is still the one the page has.
const reported = host("image-host", { last_seen: "2026-10-02T00:00:10Z", capabilities: ["image-update-v1"], inventory: { image: image({ phase: "committed" }) } });
const fetched = { stamp: reportStamp(reported), view: { host_id: "image-host", connection: "fresh", supported: true, image: image({ phase: "staged", staged: { label: "2026.10.3", sequence: 3 } }) } };
assert.equal(imageSnapshot(reported, fetched).image.phase, "staged", "a view read after the report is newer");
const newer = { ...reported, last_seen: "2026-10-02T00:00:40Z", inventory: { image: image({ phase: "armed", staged: { label: "2026.10.3", sequence: 3 } }) } };
assert.equal(imageSnapshot(newer, fetched).image.phase, "armed", "a new host report is not shadowed by the older view");
// A host that has since disconnected gets no actions, whatever the earlier view said.
const gone = { ...reported, connection: "stale" };
assert.equal(imageSnapshot(gone, fetched).connection, "stale");
assert.deepEqual(enabled(imageActions(imageSnapshot(gone, fetched).image, imageSnapshot(gone, fetched).connection)), []);
const goneHTML = renderToStaticMarkup(createElement(ImageUpdates, { hosts: [gone], api, busy: false }));
assert.match(goneHTML, /actions are unavailable until it reconnects/);
assert.doesNotMatch(goneHTML, /<button type="button" class="sm"(?![^>]*disabled)/, "every action is disabled");
// The panel keeps reading while a submitted task has not finished, not only while the host reports an operation.
assert.equal(stillFollowing(false, { id: "t1", status: "queued" }), true);
assert.equal(stillFollowing(false, { id: "t1", status: "dispatched" }), true);
assert.equal(stillFollowing(false, { id: "t1", status: "succeeded" }), false);
assert.equal(stillFollowing(false, undefined), false);
assert.equal(stillFollowing(true, { id: "t1", status: "succeeded" }), true, "a running host operation is followed too");

// A channel change travels with the host's current base URL: Core writes both settings together, so
// leaving the URL out would clear the host's update source. A retry keeps its identity.
const settingsCalls = [];
const settingsApi = async (path, method, body) => { settingsCalls.push({ path, method, body }); return { operation: { id: "op-2", status: "queued" } }; };
const settingsHeld = { id: "", signature: "" };
await submitImageSettings(settingsApi, "host-1", "dev", "https://updates.example.test", settingsHeld);
await submitImageSettings(settingsApi, "host-1", "dev", "https://updates.example.test", settingsHeld);
assert.equal(settingsCalls[0].path, "hosts/host-1/updates/settings");
assert.equal(settingsCalls[0].method, "POST");
assert.deepEqual({ ...settingsCalls[0].body, request_id: "x" }, { channel: "dev", base_url: "https://updates.example.test", request_id: "x" });
assert.equal(settingsCalls[0].body.request_id, settingsCalls[1].body.request_id, "a retry reuses the request_id");
await submitImageSettings(settingsApi, "host-1", "stable", undefined, settingsHeld);
assert.deepEqual(Object.keys(settingsCalls[2].body).sort(), ["channel", "request_id"], "a host without a source sends none");
assert.notEqual(settingsCalls[2].body.request_id, settingsCalls[0].body.request_id, "another channel is another request");

// The host's reported channel is what the panel shows and preselects; the picker is off while the host is busy or away.
const onDev = host("image-host", { capabilities: ["image-update-v1"], inventory: { image: image({ channel: "dev" }) } });
const devHTML = renderToStaticMarkup(createElement(ImageUpdates, { hosts: [onDev], api, busy: false }));
assert.match(devHTML, /<dt>Channel<\/dt><dd>Development<\/dd>/);
assert.match(devHTML, /<option value="dev" selected="">Development<\/option>/);
assert.match(devHTML, /<option value="stable">Stable<\/option>/);
assert.match(renderToStaticMarkup(createElement(ImageUpdates, { hosts: [{ ...onDev, connection: "stale" }], api, busy: false })), /<select disabled=""/);
const downloadingHost = host("image-host", { capabilities: ["image-update-v1"], inventory: { image: image({ phase: "downloading", operation: { id: "req-apply-0001", kind: "apply", phase: "downloading", started_at: "2026-10-02T00:00:00Z" } }) } });
assert.match(renderToStaticMarkup(createElement(ImageUpdates, { hosts: [downloadingHost], api, busy: false })), /<select disabled=""/, "no channel change during an update operation");

// Follow accepted tasks through the panel's real handlers and polling, then use the same static
// render harness as the host-report tests. Hook dispatching matches backup-repositories.test.mjs.
function imagePanel(reportedHost, taskAPI) {
  const slots = [], effects = [], timers = new Map();
  const internals = React.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
  const original = { document: globalThis.document, setInterval: globalThis.setInterval, clearInterval: globalThis.clearInterval, setTimeout: globalThis.setTimeout };
  globalThis.document = { visibilityState: "visible" };
  globalThis.setInterval = fn => { const id = timers.size + 1; timers.set(id, fn); return id; };
  globalThis.clearInterval = id => timers.delete(id);
  globalThis.setTimeout = () => 0;
  const panel = ImageUpdates({ hosts: [reportedHost], api: taskAPI, busy: false }).props.children[0][0];
  let cursor = 0, tree;
  const dispatcher = {
    useState(initial) {
      const i = cursor++;
      slots[i] ??= { value: typeof initial === "function" ? initial() : initial };
      return [slots[i].value, next => { slots[i].value = typeof next === "function" ? next(slots[i].value) : next; }];
    },
    useRef(initial) { const i = cursor++; slots[i] ??= { current: initial }; return slots[i]; },
    useEffect(fn, deps) {
      const i = cursor++, old = slots[i];
      if (!old || deps.some((value, index) => !Object.is(value, old.deps[index]))) {
        effects.push(() => { old?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; });
      }
    },
  };
  const render = () => {
    cursor = 0;
    const previous = internals.H;
    internals.H = dispatcher;
    try { tree = panel.type(panel.props); } finally { internals.H = previous; }
    effects.splice(0).forEach(fn => fn());
    return tree;
  };
  const nodes = value => {
    if (Array.isArray(value)) return value.flatMap(nodes);
    if (!value || typeof value !== "object" || !value.props) return [];
    return [value, ...nodes(value.props.children)];
  };
  const settle = async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); render(); };
  render();
  return {
    html: () => renderToStaticMarkup(render()),
    async check() { nodes(render()).find(n => n.type === "button" && n.props.children === "Check").props.onClick(); await settle(); },
    async poll() { for (const fn of [...timers.values()]) fn(); await settle(); },
    close() {
      for (const slot of slots) slot?.cleanup?.();
      if (original.document === undefined) delete globalThis.document;
      else globalThis.document = original.document;
      globalThis.setInterval = original.setInterval;
      globalThis.clearInterval = original.clearInterval;
      globalThis.setTimeout = original.setTimeout;
    },
  };
}

// Review 27: refusals and dispatch failures can finish a task without creating a host operation.
for (const [status, message, tone] of [
  ["failed", "check_throttled: wait 60 seconds before checking again", "bad"],
  ["needs_attention", "Host did not receive the command; reconnect and retry the same request", "warn"],
]) {
  let current = { id: "image-task-1", status: "queued" };
  const panel = imagePanel(reported, async (path, method) => {
    if (method === "POST") return { operation: current };
    if (path === "operations") return [current];
    return { host_id: reported.id, connection: "fresh", supported: true, image: reported.inventory.image };
  });
  try {
    await panel.check();
    current = { id: "image-task-1", status, error: message };
    await panel.poll();
    const failedHTML = panel.html();
    assert.ok(failedHTML.includes(`<p class="banner ${tone}" role="alert">${message}</p>`), `${status} task exposes its literal actionable error before any host operation exists`);
    assert.match(failedHTML, /href="#\/tasks" title="Task image-task-1">image-task-1<\/a>/, "the failed task stays linked");
    assert.ok(failedHTML.includes(`${status}.`), "the task retains its reported terminal status");
    assert.match(failedHTML, /2026\.10\.2 \(sequence 2, slot a\)/, "task failure does not hide the running image");
    assert.doesNotMatch(failedHTML, /Last operation/, "the host has reported no operation");

    current = { id: "image-task-2", status: "queued" };
    await panel.check();
    assert.ok(!panel.html().includes(message), "a new accepted task replaces the obsolete failure");
    current = { id: "image-task-2", status: "succeeded" };
    await panel.poll();
    const successHTML = panel.html();
    assert.match(successHTML, /href="#\/tasks" title="Task image-task-2">image-task-2<\/a> succeeded/);
    assert.ok(!successHTML.includes(message), "a successful task has no obsolete failure");
    assert.doesNotMatch(successHTML, /class="banner (?:bad|warn)" role="alert"/);
  } finally {
    panel.close();
  }
}
