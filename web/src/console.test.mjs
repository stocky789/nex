// SPDX-License-Identifier: AGPL-3.0-only
// BUG-0212 regression: typing into a VM must send the key a US keyboard presses for each
// character, with Shift held for the shifted half, instead of a bare keysym that a Windows
// guest reads as the unshifted character. Also covers the console's Start control (BUG-0197)
// and the per-drive CD/DVD controls. There is no test harness in web/ (no vitest/jest), so
// this transforms the real module with the already-installed esbuild and renders it with
// react-dom/server; run with: node src/console.test.mjs
import { transformSync } from "esbuild";
import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { readFileSync } from "node:fs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

// The transformed module is written inside web/node_modules so its bare imports resolve to the
// same React copy this file uses; two React instances cannot render together. noVNC and the
// stylesheet are stubbed: neither is used by the pure functions or by server rendering.
const outdir = fileURLToPath(new URL("../node_modules/.cache/nex", import.meta.url));
mkdirSync(outdir, { recursive: true });
const outfile = `${outdir}/console.test-module.mjs`;
const src = readFileSync(new URL("./console.tsx", import.meta.url), "utf8");
const { code } = transformSync(src, { loader: "tsx", format: "esm", jsx: "automatic" });
const stubbed = code
  .replace('from "@novnc/novnc"', 'from "data:text/javascript,export default class RFB {}"')
  .replace('import "./console.css";', "");
assert.doesNotMatch(stubbed, /@novnc|console\.css/, "module stubs did not apply");
writeFileSync(outfile, stubbed);
globalThis.localStorage = { getItem: () => null, setItem() {} };
const { typedKey, typedEvents, Console } = await import(`file://${outfile}`);

// The US layout, written here independently of the module's own table, keyed by the physical
// key: [KeyboardEvent code, character without Shift, character with Shift].
const layout = [
  ["Backquote", "`", "~"], ["Digit1", "1", "!"], ["Digit2", "2", "@"], ["Digit3", "3", "#"],
  ["Digit4", "4", "$"], ["Digit5", "5", "%"], ["Digit6", "6", "^"], ["Digit7", "7", "&"],
  ["Digit8", "8", "*"], ["Digit9", "9", "("], ["Digit0", "0", ")"], ["Minus", "-", "_"],
  ["Equal", "=", "+"], ["BracketLeft", "[", "{"], ["BracketRight", "]", "}"],
  ["Backslash", "\\", "|"], ["Semicolon", ";", ":"], ["Quote", "'", '"'], ["Comma", ",", "<"],
  ["Period", ".", ">"], ["Slash", "/", "?"], ["Space", " ", " "],
  ...Array.from({ length: 26 }, (_, i) => [`Key${String.fromCharCode(0x41 + i)}`, String.fromCharCode(0x61 + i), String.fromCharCode(0x41 + i)]),
];
const shift = [0xffe1, "ShiftLeft"];

// Every printable ASCII character reaches a US-layout guest as itself: read the emitted events
// back the way a guest does, from the scancode of the key and the Shift state it is sent with.
function asGuestReads(events) {
  let held = false;
  let out = "";
  for (const [sym, code, down] of events) {
    assert.equal(typeof sym, "number");
    if (sym === shift[0]) {
      assert.equal(code, shift[1], "Shift must carry its own key code");
      held = down;
      continue;
    }
    if (!down) continue;
    // The guest reads the key position and the modifier state, not the keysym. Without a code
    // QEMU presses the position that types that character unshifted, which is how every capital
    // and shifted symbol was lost; decoding it that way reproduces the reported corruption.
    const ch = String.fromCodePoint(sym);
    const key = layout.find(([c]) => c === code) || layout.find(([, plain, shifted]) => plain === ch || shifted === ch);
    assert.ok(key, `no US key position for code ${code} (keysym ${sym})`);
    out += held ? key[2] : key[1];
  }
  assert.equal(held, false, "Shift was left pressed");
  return out;
}

const printable = Array.from({ length: 0x7f - 0x20 }, (_, i) => String.fromCharCode(0x20 + i)).join("");
assert.equal(asGuestReads(Array.from(printable).flatMap(typedEvents)), printable, "printable ASCII must survive typing");
// The string from the Windows 11 lab report, which arrived as "nex-test-upper d;\path\…".
const lab = "Nex-Test_Upper D:\\Path\\virtio-win-guest-tools.exe 123";
assert.equal(asGuestReads(Array.from(lab).flatMap(typedEvents)), lab);
// The keysym must stay the character itself; the code alone would leave the protocol lying.
for (const ch of Array.from(printable)) assert.equal(typedKey(ch).sym, ch.codePointAt(0), `keysym for ${ch}`);

// The two failures actually observed, stated on their own.
assert.deepEqual(typedKey("_"), { sym: 0x5f, code: "Minus", shift: true });
assert.deepEqual(typedKey("-"), { sym: 0x2d, code: "Minus", shift: false });
assert.deepEqual(typedKey(":"), { sym: 0x3a, code: "Semicolon", shift: true });
assert.deepEqual(typedKey(";"), { sym: 0x3b, code: "Semicolon", shift: false });
assert.deepEqual(typedKey("N"), { sym: 0x4e, code: "KeyN", shift: true });
assert.deepEqual(typedKey("n"), { sym: 0x6e, code: "KeyN", shift: false });
// Shifted digit row and the remaining shifted symbols against their unshifted partners.
for (const [code, plain, shifted] of layout) {
  assert.deepEqual(typedKey(plain), { sym: plain.codePointAt(0), code, shift: false }, plain);
  if (shifted !== plain) assert.deepEqual(typedKey(shifted), { sym: shifted.codePointAt(0), code, shift: true }, shifted);
}
// Whitespace keeps working, and characters off the US layout keep the bare keysym they had.
assert.deepEqual(typedKey("\n"), { sym: 0xff0d, code: "Enter", shift: false });
assert.deepEqual(typedKey("\t"), { sym: 0xff09, code: "Tab", shift: false });
assert.deepEqual(typedKey(" "), { sym: 0x20, code: "Space", shift: false });
assert.deepEqual(typedKey("é"), { sym: 0xe9, code: null, shift: false });
assert.deepEqual(typedKey("€"), { sym: 0x0100_20ac, code: null, shift: false });

// The exact sendKey(keysym, code, down) sequence for the path fragment that failed.
assert.deepEqual(Array.from("D:\\P_1").flatMap(typedEvents), [
  [0xffe1, "ShiftLeft", true], [0x44, "KeyD", true], [0x44, "KeyD", false], [0xffe1, "ShiftLeft", false],
  [0xffe1, "ShiftLeft", true], [0x3a, "Semicolon", true], [0x3a, "Semicolon", false], [0xffe1, "ShiftLeft", false],
  [0x5c, "Backslash", true], [0x5c, "Backslash", false],
  [0xffe1, "ShiftLeft", true], [0x50, "KeyP", true], [0x50, "KeyP", false], [0xffe1, "ShiftLeft", false],
  [0xffe1, "ShiftLeft", true], [0x5f, "Minus", true], [0x5f, "Minus", false], [0xffe1, "ShiftLeft", false],
  [0x31, "Digit1", true], [0x31, "Digit1", false],
]);

// Toolbar contract. Server rendering exercises the markup only: no display attaches, so the
// controls that need a live connection are disabled here and are not what these check.
const base = { name: "win11", open: async () => "wss://controller.example.test/console" };
const render = props => renderToStaticMarkup(createElement(Console, { ...base, ...props }));

// BUG-0197: Start belongs on the console so the operator is attached before the guest boots.
const stopped = render({ power: { state: "shut off", onStart() {} } });
assert.match(stopped, /<button type="button" class="primary"[^>]*>Start VM<\/button>/);
assert.match(stopped, /attaches in time for any boot prompt, such as “Press any key to boot from CD or DVD”/);
// Gate3: the clipboard toast names both guest tools, not only spice-vdagent.
assert.match(src, /it only arrives when the guest tools \(qemu-guest-agent and spice-vdagent\) run in the guest/);
assert.doesNotMatch(render({ power: { state: "running", onStart() {} } }), /Start VM/, "a running VM offers no Start");
assert.doesNotMatch(render({}), /Start VM/, "no power control without the power prop");
assert.match(render({ power: { state: "shut off", busy: true, onStart() {} } }), /<button type="button" class="primary" disabled=""[^>]*>Starting…<\/button>/);

// One CD drive is the toolbar as it was: no drive to choose, no per-drive labels, and the
// control keeps the CD/DVD name whatever the single drive is called.
const drive = (over = {}) => ({ drive: "install", label: "Install media", onInsert() {}, onEject() {}, ...over });
const options = [{ id: "iso-1", name: "windows-11.iso" }, { id: "iso-2", name: "virtio-win.iso" }];
const single = render({ media: { drives: [drive({ currentID: "iso-1", currentName: "windows-11.iso" })], options } });
assert.match(single, /aria-label="Attach ISO"/);
assert.match(single, /class="console-option">CD\/DVD/);
assert.doesNotMatch(single, /Attach ISO to/, "a single drive needs no drive picker");
assert.doesNotMatch(single, /Install media/, "a single drive is not named as one of several");
assert.equal(single.match(/>Eject</g).length, 1);
assert.match(single, /<option value="" selected="">windows-11\.iso<\/option>/);

// Two drives: each is named, and insert and eject act on the drive they are shown with.
const both = render({
  media: {
    drives: [drive({ currentID: "iso-1", currentName: "windows-11.iso" }), drive({ drive: "drivers", label: "Driver disc" })],
    options,
  },
});
assert.match(both, /aria-label="Attach ISO to Install media"/);
assert.match(both, /aria-label="Attach ISO to Driver disc"/);
assert.match(both, /aria-label="Eject Install media"/);
assert.match(both, /aria-label="Eject Driver disc"/);
assert.equal(both.match(/>Eject</g).length, 2);
assert.match(both, /<option value="" selected="">Empty<\/option>/, "the empty driver disc drive reports itself empty");
// The eject of a drive holding nothing is disabled; the drive holding an ISO can be ejected.
assert.match(both, /aria-label="Eject Driver disc" disabled=""/);
assert.doesNotMatch(both, /aria-label="Eject Install media" disabled=""/);
// A busy media change disables every drive's controls, and the note is shown.
const busy = render({ media: { drives: [drive({ currentID: "iso-1" }), drive({ drive: "drivers", label: "Driver disc" })], options, busy: true, note: "Inserting the install media…" } });
for (const label of ["Install media", "Driver disc"]) {
  assert.match(busy, new RegExp(`aria-label="Attach ISO to ${label}" disabled=""`));
}
assert.match(busy, /aria-label="Eject Install media" disabled=""/);
assert.match(busy, /Inserting the install media…/);

// W6: a guest that cannot act on a resize request is told so, instead of the option doing
// nothing, and no report at all is not treated as evidence that it can.
const guest = { agent: "connected", clipboard: "ready", resize: false };
const noResize = render({ guest });
assert.match(noResize, /<option value="remote" disabled="">Resize guest to window \(guest cannot resize\)<\/option>/);
assert.match(noResize, /has not reported that it can change its resolution/);
const unknownResize = render({});
assert.match(unknownResize, /<option value="remote" disabled="">Resize guest to window \(no guest report\)<\/option>/);
assert.match(unknownResize, /no guest report says this display can be resized/);
assert.match(render({ guest: null }), /<option value="remote" disabled="">Resize guest to window \(no guest report\)<\/option>/);
const canResize = render({ guest: { ...guest, resize: true } });
assert.match(canResize, /<option value="remote">Resize guest to window<\/option>/);
assert.doesNotMatch(canResize, /cannot resize|no guest report/);

// BUG-0207: the popout link and the toolbar keep their own row in a panel that fits its pane,
// and the clipboard lays out in columns rather than a tall stack above the display.
const css = readFileSync(new URL("./console.css", import.meta.url), "utf8");
assert.match(src, /className="console-controls"/);
assert.match(css, /section\.console \{[^}]*display: flex[^}]*flex-direction: column[^}]*max-height:/);
assert.match(css, /\.console-controls \{[^}]*flex: 0 1 auto/);
assert.match(css, /\.console-controls > \.section-heading, \.console-toolbar \{ flex: 0 0 auto; \}/);
assert.match(css, /\.console-screen \{[^}]*flex: 1 1 auto[^}]*min-height:/);
assert.match(css, /\.console-clipboard \{[^}]*overflow: auto/);
assert.match(css, /@media \(min-width: 760px\) \{\s*\.console-clipboard \{ grid-template-columns: repeat\(2/);

console.log("console.test.mjs: ok");
