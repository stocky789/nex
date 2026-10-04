// SPDX-License-Identifier: AGPL-3.0-only
// Following an update outlives the service restart it causes (BUG-0442).
// run with: node src/updates.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const { followUpdate, versionLight } = await import(bundleTestModule(new URL("./updates.ts", import.meta.url), "updates"));
const sleep = async () => {};

// The Controller is stopped for the package install, so fetch rejects with no HTTP status.
const restart = [new TypeError("Failed to fetch"), new TypeError("Failed to fetch"), { phase: "restarting" }, { phase: "done", message: "Update complete" }];
const seen = [];
let unreachable = 0;
const done = await followUpdate(async () => { const next = restart.shift(); if (next instanceof Error) throw next; return next; }, st => seen.push(st.phase), sleep, false, () => unreachable++);
assert.equal(done.phase, "done", "a refused connection during the restart is not a failure");
assert.deepEqual(seen, ["restarting", "done"]);
assert.equal(unreachable, 2, "each unreachable poll is reported so the page can say it is waiting");

const failed = await followUpdate(async () => ({ phase: "failed", error: "apt failed" }), () => {}, sleep);
assert.equal(failed.error, "apt failed");

// A Core update ends "verifying" until the restarted host reports again; a host that never
// does is "attention", which must stop the wait instead of polling for ten minutes.
const unreported = [{ phase: "verifying" }, { phase: "verifying" }, { phase: "attention", error: "no report" }];
const attention = await followUpdate(async () => unreported.shift(), () => {}, sleep);
assert.equal(attention.phase, "attention", "the wait ends on attention");
assert.equal(attention.error, "no report");
const verified = [{ phase: "verifying" }, { phase: "done", message: "Update complete" }];
assert.equal((await followUpdate(async () => verified.shift(), () => {}, sleep, true)).phase, "done", "verifying counts as update activity for an uncertain apply");

await assert.rejects(followUpdate(async () => { throw Object.assign(new Error("update check failed"), { status: 500 }); }, () => {}, sleep), /update check failed/, "an HTTP error still surfaces");

const lostResponse = [Object.assign(new Error("gateway restarted"), { status: 502 }), { phase: "downloading" }, { phase: "done", message: "Update complete" }];
assert.equal((await followUpdate(async () => { const next = lostResponse.shift(); if (next instanceof Error) throw next; return next; }, () => {}, sleep, true)).phase, "done", "an uncertain apply follows a temporary status error without resubmitting");
const timedOutRead = [Object.assign(new Error("proxy timeout"), { status: 408 }), { phase: "downloading" }, { phase: "done" }];
assert.equal((await followUpdate(async () => { const next = timedOutRead.shift(); if (next instanceof Error) throw next; return next; }, () => {}, sleep, true)).phase, "done", "an uncertain apply also tolerates a proxy timeout");
assert.equal(await followUpdate(async () => ({ phase: "done" }), () => {}, sleep, true), undefined, "an old terminal status alone cannot prove the uncertain request completed");
let idleReads = 0;
assert.equal((await followUpdate(async () => { idleReads++; return { phase: "idle" }; }, () => {}, sleep, true)).phase, "idle", "an uncertain request with no reported work stops at an unknown outcome");
assert.equal(idleReads, 5);

assert.equal(await followUpdate(async () => ({ phase: "installing" }), () => {}, sleep), undefined, "gives up after the bound");

assert.equal(versionLight("Nex Core 0.4.3", "0.4.3").level, "current");
assert.equal(versionLight("Nex Core 0.4.3", "0.4.4").level, "behind");
assert.equal(versionLight(undefined, "0.4.4"), undefined, "an inventory without os_version has no light, not a crash");
assert.equal(versionLight("Nex Core 1.99.0", "1.100.0").level, "behind", "same major is orange whatever the minor");
assert.equal(versionLight("Nex Core 1.99.0", "2.0.0").level, "major");
assert.equal(versionLight("Nex Core 0.5.0", "0.4.9").level, "current", "a host ahead of the archive is not behind");
assert.equal(versionLight("Nex Core 0.4.3"), undefined, "no archive answer, no light");
assert.match(versionLight("Nex Core 1.2.0", "2.0.0").title, /Major release behind: Nex Core 2.0.0 .* Nex Core 1.2.0/);

// Plain Debian (and other non-Nex) OS strings must not light up as "up to date".
assert.equal(versionLight("Debian GNU/Linux 13 (trixie)", "0.4.3"), undefined, "plain Debian is not a Nex Core version");
assert.equal(versionLight("Ubuntu 24.04.1 LTS", "0.4.3"), undefined);
assert.equal(versionLight("Nex Core 0.4.3", "1:0.4.3-1").level, "current", "epoch and revision do not make an up-to-date host orange or red");
assert.equal(versionLight("Nex Core 0.4.3", "0.4.3-1").level, "current");

assert.equal(versionLight("Nex Core 0.5.0~alpha1", "0.5.0").level, "behind", "pre-release host against the final is behind");
assert.equal(versionLight("Nex Core 0.4.3", "0.4.3~rc1").level, "current", "pre-release archive candidate is not an upgrade");
assert.equal(versionLight("Nex Core 0.5.0.1", "0.5.0").level, "behind", "leftover base digits after matching the archive read behind");

assert.equal(versionLight("Nex Core 0.4.3", "0.4.3").label, "up to date");
assert.equal(versionLight("Nex Core 0.4.3", "0.4.4").label, "update available");
assert.equal(versionLight("Nex Core 1.0.0", "2.0.0").label, "major release behind");
// Visible text beside the version; the colour dot is decorative (SR-083).
const hostPanelSource = readFileSync(new URL("./host-panel.tsx", import.meta.url), "utf8");
assert.match(hostPanelSource, /version-light \$\{light\.level\}.*aria-hidden="true"/s);
assert.match(hostPanelSource, /· \{light\.label\}/);

console.log("ok");
