// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./backups.tsx", import.meta.url), "utf8");
const selection = source.split("const openRestore = ")[1]?.split("const closeBackup = ")[0];
assert.ok(selection, "recovery-point selection handler exists");
assert.match(selection, /if \(working\) return;/, "all point-switch actions must stop during a mutation");
assert.ok(selection.indexOf("if (working) return;") < selection.indexOf("setSelectedID(point.id)"), "the guard runs before switching the selected point");
assert.match(source, /<tr key=\{p\.id\}[^>]*onClick=\{\(\) => openRestore\(p, "new"\)\}/, "row clicks use the guarded selection handler");
