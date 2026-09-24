// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { bundleTestModule } from "./test-helpers.mjs";

const { GuestOS } = await import(bundleTestModule(new URL("./guest-os.tsx", import.meta.url), "guest-os", true));
const render = vm => renderToStaticMarkup(createElement(GuestOS, { vm }));
for (const [id, file] of [
  ["almalinux", "almalinux.svg"],
  ["alpine", "alpine.svg"],
  ["amzn", "amazon-linux.svg"],
  ["antix", "antix.png"],
  ["arch", "arch.png"],
  ["aurora", "aurora.svg"],
  ["bazzite", "bazzite-256.png"],
  ["bluefin", "bluefin.svg"],
  ["cachyos", "cachyos.png"],
  ["centos", "centos.svg"],
  ["debian", "debian.svg"],
  ["deepin", "deepin.ico"],
  ["devuan", "devuan.ico"],
  ["elementary", "elementary.svg"],
  ["endeavouros", "endeavouros.png"],
  ["fedora", "fedora.ico"],
  ["freebsd", "freebsd-favicon.ico"],
  ["garuda", "garuda.svg"],
  ["gentoo", "gentoo.svg"],
  ["kali", "kali.svg"],
  ["linux", "linux.svg"],
  ["linuxmint", "linuxmint.svg"],
  ["mageia", "mageia-favicon.png"],
  ["manjaro", "manjaro.svg"],
  ["mswindows", "windows.svg"],
  ["mx", "mx-linux-logo.png"],
  ["neon", "kde-neon.svg"],
  ["netbsd", "netbsd-favicon.ico"],
  ["nixos", "nixos.svg"],
  ["nobara", "nobara.svg"],
  ["ol", "oracle-linux.png"],
  ["openbsd", "openbsd-favicon.ico"],
  ["openmandriva", "openmandriva.png"],
  ["opensuse", "opensuse.ico"],
  ["opensuse-leap", "opensuse-leap.svg"],
  ["opensuse-tumbleweed", "opensuse-tumbleweed.svg"],
  ["parrot", "parrot.svg"],
  ["pop", "pop.svg"],
  ["rhel", "rhel.svg"],
  ["rocky", "rocky.svg"],
  ["slackware", "slackware.ico"],
  ["sled", "suse-sle.svg"],
  ["sles", "suse-sle.svg"],
  ["solus", "solus-symbol.svg"],
  ["steamos", "steamos.ico"],
  ["tails", "tails-symbol.png"],
  ["ubuntu", "ubuntu.svg"],
  ["void", "void.svg"],
  ["windows", "windows.svg"],
  ["zorin", "zorin.svg"],
]) {
  assert.ok(existsSync(new URL(`../public/guest-os/${file}`, import.meta.url)));
  const html = render({ guest: { os_id: id, os: `${id} guest` } });
  assert.ok(html.includes(`src="/guest-os/${file}"`));
  assert.match(html, /width="18" height="18" alt=""/);
  assert.ok(html.includes(`${id} guest`));
  assert.match(html, /target="_blank" rel="noreferrer"/);
}
for (const id of [undefined, "unknown", "__proto__", "../../private", "https://example.invalid/icon.svg"]) {
  const html = render({ guest: { os_id: id, os: "Guest-reported OS" } });
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /<svg/);
  assert.match(html, /Guest-reported OS/);
}
assert.match(render({ spec: { guest_os: "linux" } }), /src="\/guest-os\/linux.svg"/);
assert.match(render({ spec: { guest_os: "linux" } }), /Linux/);
assert.doesNotMatch(render({ guest: { os: "FreeBSD" }, spec: { guest_os: "linux" } }), /<img/);
assert.match(render({}), /—/);
assert.match(render({ guest: { os: "<script>" } }), /&lt;script&gt;/);
console.log("guest OS icons: local assets, canonical IDs, generic fallback and escaped labels passed");

assert.match(render({ guest: { os_id: "Deepin", os: "deepin" } }), /deepin.ico/);
assert.match(render({ spec: { guest_os: "windows" } }), /windows.svg/);
