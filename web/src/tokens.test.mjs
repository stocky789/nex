// SPDX-License-Identifier: AGPL-3.0-only
// The UI's colours live in tokens.css alone, no chrome surface is a gradient, and every
// text/background token pair meets WCAG AA. run with: node src/tokens.test.mjs
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";

const read = (f) => readFileSync(new URL(`./${f}`, import.meta.url), "utf8");
const tokens = read("tokens.css");
// Every Controller stylesheet (local.css is the Nex Core host page, outside the guide) follows the design guide: token colours only, no gradients (the
// Overview's stale-host hatch is the one allowed), no coloured side stripes, corners of 2 or 3 px.
for (const file of readdirSync(new URL(".", import.meta.url)).filter(f => f.endsWith(".css") && f !== "tokens.css" && f !== "local.css")) {
  const css = read(file);
  const gradients = (css.match(/linear-gradient/g) || []).length;
  assert.equal(gradients, file === "overview.css" ? 1 : 0, `${file}: no gradients`);
  const literals = css.match(/#[0-9a-f]{3,8}\b|\b(rgba?|hsla?)\(/gi) || [];
  assert.deepEqual(literals, [], `${file}: colours come from tokens.css; found ${literals.join(" ")}`);
  // A 1 px neutral divider beside a nav or the slide-over is not a stripe; a thick or coloured one is.
  assert.ok(!/border-(left|right)(-color)?\s*:[^;]*((?<![\d.])[2-9]px|--(accent|ok|warn|bad|info|danger))/.test(css), `${file}: no side stripes`);
  assert.ok(!/border-radius:\s*([4-9]|\d{2,})px/.test(css), `${file}: corners are 2 or 3 px`);
}

const [light, dark] = tokens.split(':root[data-theme="dark"]');
assert.ok(light && dark, "tokens.css defines light and dark theme blocks");
const declarations = (block) => (block.match(/--[\w-]+:[^;]+;/g) || []).join("");
assert.ok(declarations(dark).includes("--canvas:"), "dark theme defines colours");
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
const pairs = [["text", "canvas"], ["text", "panel"], ["text", "sunken"], ["text", "select"], ["muted", "canvas"], ["muted", "panel"], ["muted", "sunken"], ["accent", "panel"], ["accent", "canvas"], ["on-accent", "accent"], ["on-rail", "rail"], ["rail-hi", "rail"], ["on-danger", "danger"], ["ok", "ok-bg"], ["warn", "warn-bg"], ["bad", "bad-bg"], ["info", "info-bg"], ["neutral", "neutral-bg"]];
for (const [fg, bg] of pairs) {
  assert.ok(ratio(hex(light, fg), hex(light, bg)) >= 4.5, `light ${fg} on ${bg}`);
  assert.ok(ratio(hex(dark, fg), hex(dark, bg)) >= 4.5, `dark ${fg} on ${bg}`);
}
// Chart lines are graphics, so they need 3:1 against the panel they are drawn on.
for (const series of ["s1", "s2", "s3", "s4", ...[1, 2, 3, 4, 5, 6, 7, 8].map((i) => `series-${i}`)]) {
  assert.ok(ratio(hex(light, series), hex(light, "panel")) >= 3, `light ${series} on panel`);
  assert.ok(ratio(hex(dark, series), hex(dark, "panel")) >= 3, `dark ${series} on panel`);
}
console.log("tokens ok");
