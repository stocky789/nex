// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { bundleTestModule } from "./test-helpers.mjs";

const { releaseBlocker, requestRelease } = await import(bundleTestModule(new URL("./release-retained.tsx", import.meta.url), "release-retained", true));
const host = { id: "h", connection: "fresh" };
const vm = { id: "v", name: "guest", state: "shut off", freshness: "fresh" };
assert.equal(releaseBlocker(vm, host), "");
assert.match(releaseBlocker({ ...vm, state: "running" }, host), /Stop the VM/);
assert.match(releaseBlocker({ ...vm, freshness: "stale" }, host), /unknown/);
assert.match(releaseBlocker(vm, { ...host, connection: "stale" }), /freshly connected/);
assert.match(releaseBlocker({ ...vm, protected: true }, host), /appliance/);

// An uncertain failure replays one request_id; success releases it for the next release.
const sent = [];
let failures = 1;
const api = async (path, method, body) => {
  sent.push({ path, method, body });
  if (failures-- > 0) throw new Error("connection lost");
  return { operation: { id: "op-1" } };
};
const request = { current: "" };
await assert.rejects(requestRelease(api, "vm/1", request, "guest"), /connection lost/);
const held = request.current;
assert.notEqual(held, "", "a failed attempt keeps its request id");
await requestRelease(api, "vm/1", request, "guest");
assert.deepEqual(sent.map(s => s.body.request_id), [held, held], "the retry is the same request");
assert.deepEqual(sent[0], { path: "vms/vm%2F1/release-retained", method: "POST", body: { request_id: held, confirmation: "guest" } });
assert.equal(request.current, "", "success starts a fresh request next time");
