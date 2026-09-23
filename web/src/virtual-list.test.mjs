// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { bundleTestModule } from "./test-helpers.mjs";

const { VirtualPad, useVirtualWindow } = await import(
  bundleTestModule(new URL("./virtual-list.tsx", import.meta.url), "virtual-list")
);

assert.equal(renderToStaticMarkup(createElement(VirtualPad, { height: 0, colSpan: 3 })), "");
const pad = renderToStaticMarkup(createElement(VirtualPad, { height: 72, colSpan: 4 }));
assert.match(pad, /class="virtual-pad"/);
assert.match(pad, /height:72px/);
assert.match(pad, /colSpan="4"/);

// useVirtualWindow is exercised through the VM list; export presence is enough here.
assert.equal(typeof useVirtualWindow, "function");
console.log("virtual-list ok");
