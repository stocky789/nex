// SPDX-License-Identifier: AGPL-3.0-only
// Pool rollout view: what the master's record and refusals are shown as, which controls make sense, and
// how a request is submitted and replayed. Same harness as image-updates.test.mjs.
// Run with: node src/pool-rollout.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { PoolRollout, rolloutControls, canRollBackHost, refusal, offeredSequence, submitRollout, submitControl, hostStageLabel, controllerStageLabel, pauseHint, exclusive, newerView } =
  await import(bundleTestModule(new URL("./pool-rollout.tsx", import.meta.url), "pool-rollout"));

const host = (id, stage, over = {}) => ({ host_id: id, stage, ...over });
const rollout = (over = {}) => ({ id: "ru-1", pool_id: "pool-1", sequence: 7, running_vms: "refuse", state: "running", phase: "hosts", hosts: [host("h1", "done"), host("h2", "drain")],
  created_at: "2026-10-02T00:00:00Z", updated_at: "2026-10-02T00:01:00Z", ...over });
const view = (over = {}, connection = "fresh") => ({ pool_id: "pool-1", connection, supported: true, rollout: rollout(over) });
const enabled = (c) => Object.entries(c).filter(([, v]) => v.enabled).map(([k]) => k).sort();

// Running: pause or abort; paused: resume or abort; ended: nothing; no rollout or a stale master: nothing.
assert.deepEqual(enabled(rolloutControls(view())), ["abort", "pause"]);
assert.deepEqual(enabled(rolloutControls(view({ pause_requested: true }))), ["abort"]);
assert.deepEqual(enabled(rolloutControls(view({ state: "paused", pause: { code: "operator_paused", message: "x" } }))), ["abort", "resume"]);
assert.deepEqual(enabled(rolloutControls(view({ state: "finalized" }))), []);
assert.deepEqual(enabled(rolloutControls(view({}, "stale"))), []);
assert.deepEqual(enabled(rolloutControls({ pool_id: "p", connection: "fresh", supported: true })), []);
assert.match(rolloutControls(view({}, "stale")).abort.why, /freshly connected/);
assert.equal(rolloutControls(undefined).pause.why, "There is no update in progress.");
assert.equal(rolloutControls(view({ state: "finalized" })).abort.why, "The update has ended.");

// A host can only be rolled back once it activated, and only while the rollout is paused.
const paused = view({ state: "paused" });
assert.equal(canRollBackHost(paused, host("h", "boot")), true);
assert.equal(canRollBackHost(paused, host("h", "done")), true);
assert.equal(canRollBackHost(paused, host("h", "drain")), false);
assert.equal(canRollBackHost(view(), host("h", "done")), false);
assert.equal(canRollBackHost(view({ state: "paused" }, "stale"), host("h", "done")), false);

// Refusals carry "code: message"; anything else is shown as it came.
assert.deepEqual(refusal("quorum_margin: remaining voters would lose a majority"), { code: "quorum_margin", message: "remaining voters would lose a majority" });
assert.deepEqual(refusal("bridge_required: 3"), { code: "bridge_required", message: "3" });
assert.deepEqual(refusal("the master is unreachable"), { code: "", message: "the master is unreachable" });
assert.equal(refusal(undefined), undefined);

// The prefilled sequence is the smallest one every host reports; a host that has found nothing blocks it.
const withImage = (sequence) => ({ revoked: false, inventory: { image: { booted: { label: "a", sequence: 1 }, phase: "idle", rollback_floor: 1, state_format: 3, ...(sequence ? { available: { label: "b", sequence } } : {}) } } });
assert.equal(offeredSequence([withImage(7), withImage(9)]), 7);
assert.equal(offeredSequence([withImage(7), withImage(undefined)]), undefined);
assert.equal(offeredSequence([]), undefined);

assert.equal(hostStageLabel("teardown"), "Waiting for the pool's watchdogs");
assert.equal(hostStageLabel("unknown_stage"), "unknown_stage");
assert.equal(controllerStageLabel({ stage: "swap" }), "Swapping the Controller image");
assert.match(pauseHint("master_changed"), /resume or abort/);
assert.equal(pauseHint("nothing"), "");

// One request identity per unchanged request; a changed one is a new request. Control carries host_id only when given.
{
  const calls = [];
  const api = async (path, method, body) => { calls.push({ path, method, body }); return { operation: { id: "task-1", status: "queued" } }; };
  const held = { id: "", signature: "" };
  await submitRollout(api, "pool-1", "check", held, 7, false);
  await submitRollout(api, "pool-1", "check", held, 7, false);
  assert.equal(calls[0].path, "pools/pool-1/updates/check");
  assert.equal(calls[0].body.request_id, calls[1].body.request_id);
  assert.deepEqual([calls[0].body.sequence, calls[0].body.running_vms], [7, "refuse"]);
  await submitRollout(api, "pool-1", "start", held, 7, true);
  assert.equal(calls[2].path, "pools/pool-1/updates/rollouts");
  assert.notEqual(calls[2].body.request_id, calls[0].body.request_id);
  assert.equal(calls[2].body.running_vms, "shutdown");
  const ctl = { id: "", signature: "" };
  await submitControl(api, "pool-1", "ru-1", "rollback_host", ctl, "h2");
  assert.equal(calls[3].path, "pools/pool-1/updates/rollouts/ru-1/control");
  assert.deepEqual([calls[3].body.action, calls[3].body.host_id], ["rollback_host", "h2"]);
  await submitControl(api, "pool-1", "ru-1", "pause", ctl);
  assert.equal("host_id" in calls[4].body, false);
}

// Rendering: before the first read the panel says so and stores nothing; the controller step, every host
// step and a pause with its stable code are shown from the record alone.
const pool = { id: "pool-1", name: "pool-1", site_id: "s", master_host_id: "h1" };
const hosts = [{ id: "h1", hostname: "alpha", revoked: false }, { id: "h2", hostname: "bravo", revoked: false }];
const api = async () => { throw new Error("not used in static render"); };
const html = renderToStaticMarkup(createElement(PoolRollout, { pool, hosts, api, busy: false }));
assert.match(html, /Pool update/);
assert.match(html, /Reading the pool master/);
const render = (v) => renderToStaticMarkup(createElement(PoolRollout, { pool, hosts, api, busy: false, initial: v }));
const body = render(view({
  state: "paused", phase: "controller", controller: { stage: "swap", host_id: "h2", error: "controller_rolled_back: the previous image and its data were restored" },
  pause: { code: "needs_attention", message: "h2: controller_rolled_back: the previous image and its data were restored" },
  hosts: [host("h1", "done"), host("h2", "boot", { stopped_by_update: ["vm-a", "vm-b"], error: "x" })],
}));
assert.match(body, /Controller first/);
assert.match(body, /Swapping the Controller image/);
assert.match(body, /bravo/);                       // host names come from the host list, ids only as a fallback
assert.match(body, /controller_rolled_back/);       // the stable code and the literal message are both shown
assert.match(body, /2 virtual machines stopped by the update/);
assert.match(body, /Release number/);
assert.match(body, /<caption class="sr-only">Pool update steps in order<\/caption>/);
assert.doesNotMatch(body, /guest|[Ss]equence|[Rr]ollout steps/); // operator wording: virtual machines, release number, pool update
assert.match(body, /Roll back host/);               // h2 has activated and the rollout is paused
assert.match(body, /needs an operator/);            // the pause hint
assert.doesNotMatch(body, /Start update/);          // no second update while one is active
const ended = render(view({ state: "finalized", phase: "finalize", finalized_sequence: 7 }));
assert.match(ended, /Finalized/);
assert.match(ended, /Start update/);
assert.match(render({ pool_id: "pool-1", connection: "fresh", supported: false }), /pool-update-rollout-v1/);
assert.match(render({ pool_id: "pool-1", connection: "stale", supported: true, rollout: rollout() }), /last report/);
// A double click sends once: the second call does nothing while the first is on the wire, and the gate
// opens again after a success and after a failure (a failed request is retried with its held request ID).
{
  const gate = { current: false };
  let sent = 0, release;
  const slow = () => { sent++; return new Promise(resolve => { release = resolve; }); };
  const first = exclusive(gate, slow);
  assert.equal(await exclusive(gate, slow), undefined);
  assert.equal(sent, 1);
  release("done");
  assert.equal(await first, "done");
  await assert.rejects(exclusive(gate, async () => { throw new Error("offline"); }), /offline/);
  assert.equal(await exclusive(gate, async () => "again"), "again");
}

// A read that arrives late never replaces a newer report of the same rollout, but a different rollout does.
{
  const newer = view({ updated_at: "2026-10-02T00:05:00Z", state: "paused" });
  const older = view({ updated_at: "2026-10-02T00:01:00Z" });
  assert.equal(newerView(newer, older), newer);
  assert.equal(newerView(older, newer), newer);
  assert.equal(newerView(undefined, older), older);
  const stale = { ...newer, connection: "stale" };
  assert.equal(newerView(newer, stale), stale);
  const other = view({ id: "ru-2", updated_at: "2026-10-02T00:00:00Z" });
  assert.equal(newerView(newer, other), other);
}

console.log("pool-rollout ok");
