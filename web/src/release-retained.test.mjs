// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";

const { releaseBlocker } = await import(bundleTestModule(new URL("./release-retained.tsx", import.meta.url), "release-retained", true));
const host = { id: "h", connection: "fresh" };
const vm = { id: "v", name: "guest", state: "shut off", freshness: "fresh" };
assert.equal(releaseBlocker(vm, host), "");
assert.match(releaseBlocker({ ...vm, state: "running" }, host), /Stop the VM/);
assert.match(releaseBlocker({ ...vm, freshness: "stale" }, host), /unknown/);
assert.match(releaseBlocker(vm, { ...host, connection: "stale" }), /freshly connected/);
assert.match(releaseBlocker({ ...vm, protected: true }, host), /appliance/);

const source = readFileSync(new URL("./release-retained.tsx", import.meta.url), "utf8");
assert.ok(source.includes("`vms/${encodeURIComponent(vm.id)}/release-retained`"), "the action calls the Controller route");
assert.ok(source.includes("confirmation: name") && source.includes("name !== vm.name"), "the VM name is typed to confirm");
assert.ok(source.includes('request.current ||= crypto.randomUUID()'), "a retry replays one request_id");
