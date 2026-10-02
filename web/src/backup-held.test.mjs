// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { heldFor, isDefinitive, sendHeld } = await import(bundleTestModule(new URL("./backup-held.ts", import.meta.url), "backup-held"));

const status = code => Object.assign(new Error(`status ${code}`), { status: code });
const make = fields => id => ({ request_id: id, ...fields });
const sender = (...outcomes) => {
  const sent = [];
  return { sent, send: async body => { sent.push(body); const next = outcomes.shift(); if (next instanceof Error) throw next; return next; } };
};

assert.deepEqual([400, 404, 422, 409, 500, 503, undefined].map(code => isDefinitive(code ? status(code) : new Error("network"))), [true, true, true, false, false, false, false],
  "only a 4xx other than 409 proves Core did not accept the request");

// An unknown outcome keeps the frozen body, so the retry is the exact request even if the form has changed.
const held = heldFor("pool-exact");
for (const failure of [status(503), status(409), new Error("network")]) {
  const { sent, send } = sender(failure, { operation: { id: "op" } });
  const first = await sendHeld(held, "backup-now-vm", "bnow", make({ repository_id: "repo-1" }), send);
  assert.equal(first.definitive, false);
  const frozen = held.bodies["backup-now-vm"];
  assert.ok(frozen, "the body outlives the failed attempt");
  const retry = await sendHeld(held, "backup-now-vm", "bnow", make({ repository_id: "changed-form" }), send);
  assert.deepEqual(sent, [frozen, frozen], "the retry sends the same body and request_id, not the edited form");
  assert.equal(retry.reply.operation.id, "op");
  assert.equal(held.bodies["backup-now-vm"], undefined, "success releases the request");
}

// A definitive refusal releases the request so a corrected form is a new request.
const refused = sender(status(422), { operation: { id: "op" } });
const first = await sendHeld(held, "protect-p", "bpro", make({ protected: true }), refused.send);
assert.equal(first.definitive, true);
assert.equal(held.bodies["protect-p"], undefined);
await sendHeld(held, "protect-p", "bpro", make({ protected: false }), refused.send);
assert.notEqual(refused.sent[0].request_id, refused.sent[1].request_id);
assert.equal(refused.sent[1].protected, false);

// Leaving the page and coming back finds the same pool's held request; another pool never sees it.
await sendHeld(held, "restore-new-p", "bres", make({ new_vm_id: "vm-x" }), sender(status(503)).send);
assert.equal(heldFor("pool-exact"), held, "the store is keyed by pool and outlives a component");
assert.equal(heldFor("pool-exact").bodies["restore-new-p"].new_vm_id, "vm-x");
assert.deepEqual(heldFor("other-pool").bodies, {});
