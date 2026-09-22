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
const done = await followUpdate(async () => { const next = restart.shift(); if (next instanceof Error) throw next; return next; }, st => seen.push(st.phase), sleep);
assert.equal(done.phase, "done", "a refused connection during the restart is not a failure");
assert.deepEqual(seen, ["restarting", "done"]);

const failed = await followUpdate(async () => ({ phase: "failed", error: "apt failed" }), () => {}, sleep);
assert.equal(failed.error, "apt failed");

await assert.rejects(followUpdate(async () => { throw Object.assign(new Error("update check failed"), { status: 500 }); }, () => {}, sleep), /update check failed/, "an HTTP error still surfaces");

assert.equal(await followUpdate(async () => ({ phase: "installing" }), () => {}, sleep), undefined, "gives up after the bound");

assert.equal(versionLight("Nex Core 0.4.3", "0.4.3").level, "current");
assert.equal(versionLight("Nex Core 0.4.3", "0.4.4").level, "behind");
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
const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");
assert.match(mainSource, /version-light \$\{light\.level\}.*aria-hidden="true"/s);
assert.match(mainSource, /\{inv\.os_version\} · \{light\.label\}/);

console.log("ok");
