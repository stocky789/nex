// SPDX-License-Identifier: AGPL-3.0-only
// The update channel picker: what it offers, what it preselects and when it may be used.
// Run with: node src/channel.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const { ChannelPicker, channelLabel } = await import(bundleTestModule(new URL("./channel.tsx", import.meta.url), "channel"));
const render = props => renderToStaticMarkup(createElement(ChannelPicker, { disabled: false, apply: async () => {}, ...props }));

// A host that reports nothing is on the default channel, and the button waits for a change.
const fresh = render({});
assert.match(fresh, /<option value="stable" selected="">Stable<\/option>/);
assert.match(fresh, /<option value="dev">Development<\/option>/);
assert.match(fresh, /<button type="button" class="sm" disabled="">Change channel<\/button>/, "nothing to change yet");

// A channel the picker does not offer, set through the API, stays visible and selected rather than reading as another one.
const custom = render({ current: "testing" });
assert.match(custom, /<option value="testing" selected="">testing<\/option>/);
assert.match(custom, /<option value="stable">Stable<\/option>/);

// A host that cannot take the change right now disables both controls.
const away = render({ current: "dev", disabled: true });
assert.match(away, /<select disabled="">/);
assert.match(away, /<button type="button" class="sm" disabled="">/);

assert.equal(channelLabel("dev"), "Development");
assert.equal(channelLabel("stable"), "Stable");
assert.equal(channelLabel("testing"), "testing");
assert.equal(channelLabel(undefined), "—");
