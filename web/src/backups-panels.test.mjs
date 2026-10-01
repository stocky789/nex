// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./backups.tsx", import.meta.url), "utf8");

// G9: Back up now is a slide-over rendered ahead of the jobs and recovery-point views,
// not a block below the lists, and nothing scrolls to it before the lists have loaded.
const backupPanel = source.indexOf('<Panel wide icon="backup" title="Back up now"');
assert.ok(backupPanel > 0, "Back up now renders as a slide-over panel");
assert.ok(backupPanel < source.indexOf('id="backup-jobs-view"'), "the panel is not placed below the jobs list");
assert.ok(backupPanel < source.indexOf('id="recovery-points"'), "the panel is not placed below the recovery points");
assert.ok(!source.includes("scrollIntoView"), "no mount-time scroll can run before the content above has loaded");

// Switching points is refused while a mutation, uncertain request or repository panel holds the page.
const open = source.split("const openRestore = ")[1]?.split("const closeBackup = ")[0] ?? "";
assert.ok(open.indexOf("if (panelLocked) return;") >= 0 && open.indexOf("if (panelLocked) return;") < open.indexOf("setSelectedID(point.id)"), "the lock runs before the selected point changes");

// An uncertain restore keeps its exact request so a retry replays it.
const submit = source.split("const submitRestore = ")[1]?.split("const toggleProtect = ")[0] ?? "";
assert.ok(submit.includes("restoreDrafts.current[key] ="), "the submitted fields are saved before the request");
assert.ok(submit.includes("request_id: requestID(key,"), "the retry holds its request ID");
assert.ok(submit.includes("delete restoreDrafts.current[key]"), "a settled reply releases the held draft");
