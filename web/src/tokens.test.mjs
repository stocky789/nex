// SPDX-License-Identifier: AGPL-3.0-only
// The UI's colours live in tokens.css alone, no chrome surface is a gradient, and every
// text/background token pair meets WCAG AA. run with: node src/tokens.test.mjs
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (f) => readFileSync(new URL(`./${f}`, import.meta.url), "utf8");
const tokens = read("tokens.css");
for (const file of ["style.css", "stats.css", "console.css"]) {
  const css = read(file);
  assert.equal((css.match(/linear-gradient/g) || []).length, 0, `${file}: no gradients`);
  const literals = css.match(/#[0-9a-f]{3,8}\b/gi) || [];
  assert.deepEqual(literals, [], `${file}: colours come from tokens.css; found ${literals.join(" ")}`);
}

const [light, dark = ""] = tokens.split("@media (prefers-color-scheme: dark)");
const hex = (block, name) => {
  const m = block.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`));
  assert.ok(m, `token --${name} is defined`);
  return m[1];
};
const luminance = (h) => {
  const c = [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
const ratio = (a, b) => { const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
const pairs = [["text", "canvas"], ["text", "panel"], ["text", "sunken"], ["muted", "canvas"], ["muted", "panel"], ["muted", "sunken"], ["accent", "panel"], ["accent", "canvas"], ["on-accent", "accent"], ["on-topbar", "topbar"], ["on-danger", "danger"], ["ok", "ok-bg"], ["warn", "warn-bg"], ["bad", "bad-bg"], ["info", "info-bg"], ["neutral", "neutral-bg"]];
for (const [fg, bg] of pairs) {
  assert.ok(ratio(hex(light, fg), hex(light, bg)) >= 4.5, `light ${fg} on ${bg}`);
  if (dark) assert.ok(ratio(hex(dark, fg), hex(dark, bg)) >= 4.5, `dark ${fg} on ${bg}`);
}
// Chart lines are graphics, so they need 3:1 against the panel they are drawn on.
for (let i = 1; i <= 8; i++) {
  assert.ok(ratio(hex(light, `series-${i}`), hex(light, "panel")) >= 3, `light series-${i} on panel`);
  if (dark) assert.ok(ratio(hex(dark, `series-${i}`), hex(dark, "panel")) >= 3, `dark series-${i} on panel`);
}
console.log("tokens ok");
