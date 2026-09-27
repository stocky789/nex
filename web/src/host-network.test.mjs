// SPDX-License-Identifier: AGPL-3.0-only
// S3 "staged management-network edits": the same harness as pool-members.test.mjs — bundle the
// real module with the already-installed esbuild and render it with react-dom/server.
// Run with: node src/host-network.test.mjs
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const moduleURL = bundleTestModule(new URL("./host-network.tsx", import.meta.url), "host-network");
const src = readFileSync(new URL("./host-network.tsx", import.meta.url), "utf8");
const stepHandler = src.slice(src.indexOf("async function step("), src.indexOf("\n  return <HostNetworkPanel"));
assert.match(stepHandler, /catch \(e\) \{\n      void refresh\(true\);/, "a failed or interrupted network command reconciles the host's reported state");
const {
  HostNetworkPanel, ApplyDialog, windowError, diffRows, describeNIC,
  manageableNICs, countdownText, stagedWindowText, stageLabel, rowsFromHost, specFromRows, editSeed,
} = await import(moduleURL);
const { heldRequestID } = await import(bundleTestModule(new URL("./managed.tsx", import.meta.url), "managed-held"));

const nics = [
  { name: "nex0", mac: "52:54:00:12:34:56", kind: "physical", master: "br0", addresses: ["192.0.2.10/24"], state: "up" },
  { name: "nex1", mac: "52:54:00:12:34:57", kind: "physical", addresses: [], state: "down" },
  { name: "br0", mac: "52:54:00:12:34:56", kind: "bridge", addresses: ["192.0.2.10/24"], state: "up" },
  { name: "lo", mac: "00:00:00:00:00:00", addresses: ["127.0.0.1/8"], state: "up" },
];
const spec = {
  interfaces: [
    { name: "nex0", mac: "52:54:00:12:34:56", mode: "static", vlan: 10, address: "192.0.2.20/24", gateway: "192.0.2.1", management: true },
    { name: "nex1", mac: "52:54:00:12:34:57", mode: "disabled" },
  ],
  dns: ["192.0.2.53"],
};
const view = {
  host_id: "host-1", hostname: "core-b", connection: "fresh", supported: true, state: "staged",
  management_network: { revision: 2, stage: "staged", operation_id: "op-1", spec, window_seconds: 720, observed_at: "2026-09-19T10:03:08Z" },
  nics, check: null, last_operation: null,
};
// The panel is a pure projection of one GET, so it renders straight from props; the container
// around it only fetches and polls.
const panel = (over = {}) => renderToStaticMarkup(createElement(HostNetworkPanel, {
  view, loading: false, error: "", busy: false, submitError: "", notice: "", remaining: null,
  unavailable: false, applying: false,
  onRefresh: () => {}, onStep: () => {}, onApplyOpen: () => {}, onApplyClose: () => {}, ...over,
}));

const one = { name: "nex0", mac: "52:54:00:12:34:56", mode: "static", address: "192.0.2.10/24", gateway: "192.0.2.1", management: true };

// The rollback window mirrors Core's 2-30 minute bound.
assert.equal(windowError(10), "");
assert.equal(windowError(2), "");
assert.equal(windowError(30), "");
assert.match(windowError(1), /between 2 and 30/);
assert.match(windowError(31), /between 2 and 30/);
assert.match(windowError(Number.NaN), /between 2 and 30/);
assert.match(windowError(2.5), /whole number of minutes/);

// ---------------------------------------------------------------- projections

assert.deepEqual(manageableNICs(nics).map(n => n.name), ["nex0", "nex1"], "bridges, VLAN devices and lo are not management choices");
assert.equal(describeNIC(spec.interfaces[0]), "192.0.2.20/24 · via 192.0.2.1 · VLAN 10 · management");
assert.equal(describeNIC(spec.interfaces[1]), "not configured");
assert.equal(describeNIC({ name: "nex0", mac: "x", mode: "dhcp", management: true }), "DHCP · management");

const diff = diffRows(nics, spec);
assert.deepEqual(diff.map(r => r.name), ["nex0", "nex1"]);
assert.equal(diff[0].now, "192.0.2.10/24");
assert.equal(diff[0].proposed, "192.0.2.20/24 · via 192.0.2.1 · VLAN 10 · management");
assert.equal(diff[0].changed, true);
assert.equal(diff[1].now, "no address reported");
assert.deepEqual(diffRows([], spec).map(r => r.now), ["not reported by this host", "not reported by this host"]);
assert.deepEqual(diffRows(nics, { interfaces: [spec.interfaces[0]] }).map(r => r.proposed).at(-1), "left out of this configuration");

// The countdown is the host's own seconds_remaining, formatted; it is never invented.
assert.equal(countdownText(412), "Rolls back in 6:52 unless you confirm it.");
assert.equal(countdownText(60), "Rolls back in 1:00 unless you confirm it.");
assert.equal(countdownText(9), "Rolls back in 0:09 unless you confirm it.");
assert.match(countdownText(0), /deadline has passed/);
assert.equal(countdownText(null), "", "no report, no countdown");

assert.deepEqual(stageLabel("rolled-back"), { label: "Rolled back automatically", badge: "failed" });
assert.deepEqual(stageLabel("unknown"), { label: "Unknown", badge: "unknown" });
assert.deepEqual(stageLabel("none"), { label: "No change staged", badge: "" });

// The form is built from the host's own NICs, and carries their reported MACs into the spec.
const formRows = rowsFromHost(nics, spec);
assert.deepEqual(formRows.map(r => [r.name, r.mac, r.mode, r.vlan, r.address, r.gateway]), [
  ["nex0", "52:54:00:12:34:56", "static", "10", "192.0.2.20/24", "192.0.2.1"],
  ["nex1", "52:54:00:12:34:57", "disabled", "", "", ""],
]);
assert.deepEqual(rowsFromHost(nics).map(r => r.mode), ["disabled", "disabled"], "with no record every interface starts unconfigured");
assert.deepEqual(editSeed(null), undefined);
assert.deepEqual(editSeed({ revision: 0, stage: "none", current: spec }), spec, "live Current prefills the form");
assert.equal(editSeed({
  revision: 2, stage: "rolled-back", spec,
  current: { interfaces: [{ name: "nex0", mac: "52:54:00:12:34:56", mode: "static", address: "192.0.2.10/24", gateway: "192.0.2.1", management: true }], dns: ["192.0.2.53"] },
}).interfaces[0].address, "192.0.2.10/24", "Current wins over a rolled-back Spec");
assert.equal(editSeed({ revision: 2, stage: "staged", spec }).interfaces[0].address, "192.0.2.20/24", "without Current, Spec remains the fallback");
assert.deepEqual(specFromRows(formRows, "nex0", "192.0.2.53, 192.0.2.54"), {
  interfaces: [
    { name: "nex0", mac: "52:54:00:12:34:56", mode: "static", vlan: 10, address: "192.0.2.20/24", gateway: "192.0.2.1", management: true },
    { name: "nex1", mac: "52:54:00:12:34:57", mode: "disabled" },
  ],
  dns: ["192.0.2.53", "192.0.2.54"],
});
assert.equal(specFromRows(rowsFromHost(nics), "nex0", "").dns.length, 0);
assert.doesNotMatch(src, /validateSpec|function ipv4|function cidrOf/, "spec validation is Core's job");

// ---------------------------------------------------------------- panel states

// Unsupported: no form, and the exact upgrade requirement.
const unsupported = panel({ view: { ...view, supported: false, state: "unknown", management_network: null } });
assert.match(unsupported, /management-network-v1 on this host; upgrade Nex Core/);
assert.doesNotMatch(unsupported, /Stage this configuration/);

// A host that is not reporting: state unknown with the reason, the deadline still visible, and
// no countdown, no confirm, no apply.
const staleView = { ...view, connection: "stale", state: "unknown", management_network: { revision: 2, stage: "applied", operation_id: "op-1", spec, deadline: "2026-09-19T10:10:00Z", seconds_remaining: 412 } };
const staleMarkup = panel({ view: staleView });
assert.match(staleMarkup, /badge neutral">Unknown/);
assert.match(staleMarkup, /The host is stale, so its management network state is unknown/);
assert.match(staleMarkup, /rollback deadline of 2026-09-19T10:10:00Z/);
assert.match(staleMarkup, /revision unknown/, "the revision of a host that is not reporting is unknown too");
assert.doesNotMatch(staleMarkup, /Rolls back in/, "a countdown is never invented for a host that is not reporting");
assert.doesNotMatch(staleMarkup, /Stage this configuration/);

// A never-configured fresh host: the edit form, with reserved-example placeholders.
const emptyView = { ...view, state: "none", management_network: null };
const form = panel({ view: emptyView });
assert.match(form, /No change staged/);
assert.match(form, /placeholder="192\.0\.2\.10\/24"/);
assert.match(form, /placeholder="192\.0\.2\.53 192\.0\.2\.54"/);
assert.match(form, /<label for="host-network-window">Rollback window \(minutes\)<\/label>/);
assert.match(form, /id="host-network-window"[^>]*min="2"[^>]*max="30"[^>]*value="10"/);
assert.match(form, /name="management-nic"[^>]*value="nex0"/);
assert.match(form, /name="management-nic"[^>]*value="nex1"/);
assert.doesNotMatch(form, /name="management-nic"[^>]*value="br0"/);
assert.match(form, /<label class="sr-only" for="address-nex0">/, "every input is labelled");
assert.match(form, /host-network-nic"><b>nex0<\/b><small class="mono">/, "interface name and MAC are stacked as a labelled cell");
assert.match(form, /class="section-heading"[\s\S]*Refresh/, "Refresh sits in the panel heading, not above Interfaces");
assert.doesNotMatch(form, /fold-actions/, "the old Refresh strip under the heading is gone");
const readFailed = panel({ view: emptyView, error: "host network report unavailable" });
assert.match(readFailed, /This host could not be asked for its management network: host network report unavailable/);
assert.match(readFailed, /placeholder="192\.0\.2\.10\/24"/, "a failed background read keeps the existing edit form mounted");
assert.match(readFailed, /<button class="primary" disabled="">Stage this configuration/);
assert.match(readFailed, /This is the last reported configuration/);
const retryCancel = panel({ view: { ...view, state: "staged" }, error: "host network report unavailable", unavailable: true });
assert.match(retryCancel, /<button type="button">Discard staged change<\/button>/, "safe rollback stays available during a read outage");

// A Nex-managed host that has never staged a change still prefills from Current.
const currentOnly = panel({ view: {
  ...view, state: "none",
  management_network: {
    revision: 0, stage: "none",
    current: {
      interfaces: [
        { name: "nex0", mac: "52:54:00:12:34:56", mode: "static", address: "192.0.2.10/24", gateway: "192.0.2.1", management: true },
        { name: "nex1", mac: "52:54:00:12:34:57", mode: "disabled" },
      ],
      dns: ["192.0.2.53", "192.0.2.54"],
    },
  },
} });
assert.match(currentOnly, /value="static"/);
assert.match(currentOnly, /value="192\.0\.2\.10\/24"/);

const dhcpView = {
  ...view, state: "none",
  management_network: {
    revision: 0, stage: "none", observed_gateway: "192.0.2.1",
    current: { interfaces: [{ name: "nex0", mac: nics[0].mac, mode: "dhcp", management: true }] },
  },
};
const dhcpForm = panel({ view: dhcpView });
assert.match(dhcpForm, /id="address-nex0"[^>]*disabled=""[^>]*value="192\.0\.2\.10\/24"/);
assert.match(dhcpForm, /id="gateway-nex0"[^>]*disabled=""[^>]*value="192\.0\.2\.1"/);
assert.doesNotMatch(panel({ view: dhcpView, error: "read failed" }), /id="gateway-nex0"[^>]*value="192\.0\.2\.1"/, "a failed report must not show the old lease as current");
assert.match(currentOnly, /value="192\.0\.2\.1"/);
assert.match(currentOnly, /id="host-network-dns"[^>]*value="192\.0\.2\.53 192\.0\.2\.54"/);

// Staged: the review diff, Apply and Discard, and nothing changed on the host yet.
const stagedMarkup = panel();
assert.match(stagedMarkup, /Nothing on the host has changed yet/);
assert.match(stagedMarkup, /192\.0\.2\.20\/24 · via 192\.0\.2\.1 · VLAN 10 · management/);
assert.match(stagedMarkup, />Apply…</);
assert.match(stagedMarkup, />Discard staged change</);
assert.match(stagedMarkup, /by itself after 12 minutes unless you confirm it\./, "the window the operator chose survives a reload");
assert.doesNotMatch(stagedMarkup, /Stage this configuration/, "the form is not offered while a change is staged");

// Applied and fresh: a live countdown from the host's seconds_remaining and an enabled Confirm.
const appliedMarkup = panel({ view: { ...view, state: "applied", management_network: { revision: 2, stage: "applied", operation_id: "op-1", spec, deadline: "2026-09-19T10:10:00Z", seconds_remaining: 412 } }, remaining: 412 });
assert.match(appliedMarkup, /Rolls back in 6:52 unless you confirm it\./);
assert.match(appliedMarkup, /<button type="button" class="primary">Keep this configuration<\/button>/, "confirm is offered once the host has reached this Controller again");
assert.match(appliedMarkup, />Roll back now</);

// Applied but the host has not reported since: waiting, and Confirm disabled.
const waitingMarkup = panel({ view: { ...view, connection: "stale", state: "applied", management_network: { revision: 2, stage: "applied", operation_id: "op-1", spec, deadline: "2026-09-19T10:10:00Z", seconds_remaining: 412 } }, remaining: null });
assert.match(waitingMarkup, /Waiting for the host to reconnect over the new configuration/);
assert.match(waitingMarkup, /<button type="button" class="primary" disabled="">Keep this configuration<\/button>/);
assert.doesNotMatch(waitingMarkup, /Rolls back in/);

// Rolled back: Core's own reason, and the failing host check.
const rolledBack = panel({ view: {
  ...view, state: "rolled-back",
  management_network: { revision: 2, stage: "rolled-back", operation_id: "op-1", spec, last_error: "the new configuration would not come up" },
  check: { name: "management-network", status: "failed", detail: "the new configuration would not come up" },
} });
assert.match(rolledBack, /Rolled back automatically/);
assert.match(rolledBack, /restored its previous configuration by itself\. Nex Core(&#x27;|')s reason: the new configuration would not come up/);
assert.match(rolledBack, /Host check management-network: failed/);
assert.match(rolledBack, /Stage this configuration/, "a rolled-back host can be edited again");

// The same stage after the operator cancelled an applied change: their own undo, not a failure.
const rolledBackByOperator = panel({ view: {
  ...view, state: "rolled-back",
  management_network: { revision: 2, stage: "rolled-back", operation_id: "op-1", spec, requested_rollback: true, last_error: "rolled back at the operator's request" },
} });
assert.deepEqual(stageLabel("rolled-back", true), { label: "Rolled back at your request", badge: "" });
assert.match(rolledBackByOperator, /restored its previous configuration because you asked it to/);
assert.doesNotMatch(rolledBackByOperator, /restored its previous configuration by itself/);
assert.doesNotMatch(rolledBackByOperator, /Host check management-network/, "no check is reported for a rollback nobody has to fix");

// An asynchronous conflict is surfaced verbatim, with what to do about it.
const conflict = panel({ view: { ...view, last_operation: { operation_id: "op-1", action: "stage", status: "failed", conflict: true, error: "management network revision is 3, not 2: management network conflict" } } });
assert.match(conflict, /role="alert"[^>]*>management network revision is 3, not 2: management network conflict This host changed since you loaded it — reload\./);
// A plain Core failure is not dressed up as a conflict.
const failed = panel({ view: { ...view, last_operation: { operation_id: "op-1", action: "apply", status: "failed", conflict: false, error: "the confirmation does not name this host" } } });
assert.match(failed, /Nex Core refused the last apply: the confirmation does not name this host/);
assert.doesNotMatch(failed, /changed since you loaded it/);

// A failed request is shown verbatim: this panel never rewrites a Core or Controller message.
const serverError = panel({ view: null, error: "the host must be freshly connected before a management network change is staged or applied" });
assert.match(serverError, /This host could not be asked for its management network: the host must be freshly connected/);

// ---------------------------------------------------------------- apply confirmation

const applyProps = { view, spec, minutes: 10, busy: false, submitError: "", onConfirm: () => {}, onClose: () => {} };
const applyDialog = renderToStaticMarkup(createElement(ApplyDialog, applyProps));
assert.match(applyDialog, /<strong>core-b may become unreachable\.<\/strong>/);
assert.match(applyDialog, /restores the previous configuration by itself after 10 minutes unless you confirm/);
assert.match(applyDialog, /host console or BMC to restore management addressing/);
assert.match(applyDialog, /<label for="apply-network-name">Type the host name <strong>core-b<\/strong> to apply<\/label>/);
// Apply is blocked until the typed name matches: nothing is typed on open.
assert.match(applyDialog, /disabled=""[^>]*>Apply and start the countdown</);
assert.match(applyDialog, /192\.0\.2\.20\/24 · via 192\.0\.2\.1 · VLAN 10 · management/, "the diff is repeated in the confirmation");
assert.match(applyDialog, /DNS servers: 192\.0\.2\.53/);
assert.match(renderToStaticMarkup(createElement(ApplyDialog, { ...applyProps, spec: { interfaces: [{ name: "nex0", mac: "52:54:00:12:34:56", mode: "dhcp", management: true }] } })), /DNS servers: no override; DHCP supplies DNS/);
assert.match(renderToStaticMarkup(createElement(ApplyDialog, { ...applyProps, spec: { interfaces: [{ ...one, gateway: "" }] } })), /DNS servers: none configured/);
assert.match(renderToStaticMarkup(createElement(ApplyDialog, { ...applyProps, submitError: 'the confirmation must be the host name "core-b"' })), /role="alert"[^>]*>the confirmation must be the host name &quot;core-b&quot;/);
assert.match(applyDialog, /aria-labelledby="apply-network-title"/);
// A host too old to report the window it recorded says so, instead of naming the default as
// though it were the window the operator chose.
assert.match(renderToStaticMarkup(createElement(ApplyDialog, { ...applyProps, minutes: null })), /after the rollback window recorded when this change was staged \(2–30 minutes\) unless you confirm/);

// ---------------------------------------------------------------- the recorded window

assert.equal(stagedWindowText(720), "Once applied, the host restores its previous configuration by itself after 12 minutes unless you confirm it.");
assert.match(stagedWindowText(undefined), /rollback window it recorded \(2–30 minutes\); this host did not report which\./);

// ---------------------------------------------------------------- payloads

// The POST body is exactly the contract's, one action per step, with the held request id.
assert.match(src, /await api\(`hosts\/\$\{encodeURIComponent\(hostID\)\}\/network`, "POST", \{ request_id, action, \.\.\.\(change_id \? \{ change_id \} : \{\}\), \.\.\.extra \}\)/);
// Every step after staging names the change this page loaded from the host's own report; the
// Controller refuses it if the host has moved on, so a stale tab cannot act on another change.
assert.match(src, /const change_id = action === "stage" \? undefined : record\?\.operation_id;/);
assert.match(src, /onStep\("stage", \{ spec, window_seconds: windowSeconds, expected_revision: record\?\.revision \|\| 0 \}/);
assert.match(src, /onStep\("apply", \{ confirmation \}/);
assert.match(src, /onStep\("confirm", \{\}/);
assert.match(src, /onStep\("cancel", \{\}/);
assert.doesNotMatch(src, /request_id: crypto\.randomUUID\(\)/);
// Polling also catches delayed host reports and idle changes made elsewhere.
assert.match(src, /const timer = window\.setInterval\(catchUp, 10_000\)/);
assert.match(src, /document\.addEventListener\("visibilitychange", catchUp\)/);
assert.match(src, /window\.addEventListener\("online", catchUp\)/);
assert.match(src, /if \(reportPending\.current && !force\) return;/, "the timer does not supersede a slow host read");
assert.match(src, /await refresh\(true\);/, "a network action starts a new read even if the timer read is still pending");
assert.match(src, /if \(\(unavailable \|\| error\) && action !== "cancel"\) return;/, "only cancellation remains available against the last report");

// One request identity per step: a retry replays it, a different step is a new request.
let minted = 0;
Object.defineProperty(globalThis, "crypto", { configurable: true, value: { randomUUID: () => `request-${++minted}` } });
const held = { id: "", signature: "" };
assert.match(src, /heldRequestID\(held\.current,/);
assert.equal(heldRequestID(held, "host-1\napply\n2\nop-1\n{}"), "request-1");
assert.equal(heldRequestID(held, "host-1\napply\n2\nop-1\n{}"), "request-1", "a retry after a lost reply replays the same request");
assert.equal(heldRequestID(held, "host-1\nconfirm\n2\nop-1\n{}"), "request-2");
assert.equal(minted, 2);

console.log("host-network.test.mjs: ok");
