// SPDX-License-Identifier: AGPL-3.0-only
// S1 "Add host to pool": there is no test harness in web/ (no vitest/jest), so this bundles the
// real module with the already-installed esbuild and renders it with react-dom/server, the same
// way managed.test.mjs does. Run with: node src/pool-members.test.mjs
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const moduleURL = bundleTestModule(new URL("./pool-members.tsx", import.meta.url), "pool-members");
const src = readFileSync(new URL("./pool-members.tsx", import.meta.url), "utf8");
const { MembersTable, AddHostDialog, MaintenanceDialog, memberChecks, poolChecks, convergenceSummary, joinProgressText, peerOriginOf, maintenanceText, guestsText, maintenanceInFlight, maintenanceResultText, replicaText, replicaGenerations, replicaTimes, recoveryText, seenAgo, membershipVersionReady, fencingSummaryText, HostFencingSetting } = await import(moduleURL);
const { heldRequestID } = await import(bundleTestModule(new URL("./managed.tsx", import.meta.url), "managed-held"));

const master = {
  host_id: "host-a", name: "core-a", role: "master", status: "ready", connection: "fresh",
  checks: [{ name: "pool-storage-reconciliation", status: "healthy" }, { name: "pool-network-reconciliation", status: "healthy" }, { name: "pool-media-reconciliation", status: "healthy" }],
  access: { healthy: 3, pending: 0, failed: 0, stale: 0 }, failures: [], join: null,
};
const member = {
  host_id: "host-b", name: "core-b", role: "member", status: "ready", connection: "fresh",
  peer_origin: "https://192.0.2.12:9444",
  checks: [{ name: "pool-storage-reconciliation", status: "failed", detail: "1 repository is not reconciled" }, { name: "pool-network-reconciliation", status: "healthy" }],
  access: { healthy: 2, pending: 0, failed: 1, stale: 0 },
  failures: [{ kind: "network", id: "net-1", name: "VLAN 100", error: "uplink lab0 does not exist on this host" }],
  join: null,
};
const report = { pool_id: "pool-1", pool_name: "core-a", master_host_id: "host-a", master_connection: "fresh", generation: 2, authoritative: true, members: [master, member] };
const table = props => renderToStaticMarkup(createElement(MembersTable, props));

// Empty state: a pool whose master reports no membership yet says so instead of rendering nothing.
const empty = table({ report: { ...report, members: [] } });
assert.match(empty, /reports no hosts yet/);
assert.doesNotMatch(empty, /<table/);

// Members render with their role badges, and a failing member shows the Core-reported message.
const rows = table({ report });
assert.match(rows, /^<details class="pool-status"><summary>/, "the pool's own facts sit once, on one strip above the rows");
assert.equal((rows.match(/Fencing: not reported/g) || []).length, 1, "and the pool's fencing sentence is said once, behind its About disclosure");
assert.match(rows, /data-host-id="host-a"/);
assert.match(rows, /data-host-id="host-b"/);
assert.match(rows, /Pool master/);
assert.match(rows, />Member</);
assert.match(rows, /uplink lab0 does not exist on this host/);
assert.match(rows, /Storage: failed · 1 repository is not reconciled/);
assert.match(rows, /Checks 1 healthy, 1 failed · Access 2 healthy, 1 failed/);
assert.match(rows, /https:\/\/192\.0\.2\.12:9444/);
// Nex Core is each host's own inventory report, as on the Hosts list.
assert.match(rows, /<small class="pool-version">Nex Core not reported<\/small>/);
const versioned = table({ report, hosts: [{ id: "host-b", inventory: { os_version: "Nex Core 0.4.4" } }], coreAvailable: "0.4.5" });
assert.match(versioned, /<small class="pool-version">Nex Core 0\.4\.4 <span class="badge warn">Update<\/span><\/small>/);
assert.match(table({ report, hosts: [{ id: "host-b", inventory: { os_version: "Nex Core 0.4.5" } }], coreAvailable: "0.4.5" }), /<small class="pool-version">Nex Core 0\.4\.5<\/small>/);

// Joined is the pool master's own record; a Core that does not report it shows a dash.
assert.match(table({ report: { ...report, members: [{ ...master, joined_at: "2026-09-01T10:00:00Z" }] } }), /<time dateTime="2026-09-01T10:00:00Z" title="[^"]+">[^<]+<\/time>/);
assert.match(rows, /<small class="muted">Joined —<\/small>/);

// A non-authoritative report (the master is not fresh) shows every field the master keeps as
// unknown: membership, maintenance, its observations of access and the failures it recorded.
// Corrected in S6: each host's own report — its connection, its checks, its copy of the pool
// records — is that host's, not the master's, and survives the master going quiet.
const stale = table({ report: { ...report, master_connection: "stale", authoritative: false } });
assert.equal((stale.match(/<strong class="neutral">Unknown<\/strong>/g) || []).length, 11, "the pool's three facts; membership, maintenance and fencing on both rows; the master's convergence and the member's authority copy — all Unknown, in sentence case");
assert.match(stale, /Convergence <strong class="bad">Failed</, "a check the host itself reports failed still reads Failed");
assert.doesNotMatch(stale, /<strong class="ok">OK<\/strong><\/span><span class="pool-check"[^>]*><span class="dot ok"><\/span>Membership/, "membership is the master's record");
assert.match(stale, /Fence device: unknown — the pool master keeps this record and is not reporting\./, "fencing is the master's record too");
assert.doesNotMatch(stale, /ready/, "the membership status is the master's record");
assert.doesNotMatch(stale, /uplink lab0/, "the failures it recorded are its observations");
assert.doesNotMatch(stale, /2 healthy, 1 failed/, "so is its view of each member's access");
assert.match(stale, /Connection <strong class="ok">OK</, "but a host's own connection is its own report");
assert.match(stale, /Storage: failed · 1 repository is not reconciled/, "and so are its own health checks");
assert.match(stale, /Access unknown/);
assert.match(stale, /Pool master/, "role comes from the membership record, not from a derived observation");

// Join progress is the host's own reported stage as text, never a spinner.
assert.equal(joinProgressText(null), "");
assert.equal(joinProgressText({ stage: "csr", status: "pending" }), "Joining · next step: requesting its pool certificate.");
assert.equal(joinProgressText({ stage: "ready", status: "succeeded" }), "Join complete.");
assert.match(joinProgressText({ stage: "precheck", status: "failed", error: "this host has a managed VM" }), /^Join failed before checking this host\. this host has a managed VM$/);
const attention = joinProgressText({ stage: "rehomed", status: "needs_attention", error: "pool scope rehome failed: storage busy" });
assert.match(attention, /Needs attention: pool scope rehome failed: storage busy/);
assert.match(attention, /already a member/);
const joining = table({ report: { ...report, members: [master, { ...member, status: "pending", join: { stage: "rehomed", status: "needs_attention", error: "pool scope rehome failed: storage busy" } }] } });
assert.match(joining, /Needs attention: pool scope rehome failed: storage busy/);
assert.doesNotMatch(joining, /progress|spinner/i);

assert.equal(convergenceSummary(master, true), "Checks 3 healthy · Access 3 healthy");
assert.equal(convergenceSummary({ ...master, checks: [], access: undefined }, true), "Checks not reported · Access not reported");
assert.equal(convergenceSummary({ ...master, access: { healthy: 0, pending: 0, failed: 0, stale: 0 } }, true), "Checks 3 healthy · Access none registered");
assert.equal(convergenceSummary({ ...master, checks: [{ name: "pool-media-reconciliation", status: "unknown" }] }, true), "Checks 1 unknown · Access 3 healthy");
assert.equal(convergenceSummary(member, false), "Checks 1 healthy, 1 failed · Access unknown", "the checks are the host's own; only the master's observation of access is unknown");

// The dialog: an incompatible uplink does not block the join, a refusing candidate cannot be picked.
const candidates = [
  { host_id: "host-c", name: "core-c", connection: "fresh", eligible: true, peer_port: 9444, rows: [
    { check: "version", subject: "0.3.5", result: "compatible" },
    { check: "uplink", subject: "VLAN 100 on lab0", result: "will_not_converge", detail: "uplink lab0 does not exist on core-c" },
  ] },
  { host_id: "host-d", name: "core-d", connection: "fresh", eligible: false, peer_port: 9444, rows: [
    { check: "vms", subject: "2 virtual machines", result: "refuses_join", detail: "a host with virtual machines cannot join a pool" },
  ] },
];
const dialogProps = { candidates, loading: false, error: "", busy: false, submitError: "", onConfirm: () => {}, onClose: () => {}, onRetryCandidates: () => {} };
const dialog = renderToStaticMarkup(createElement(AddHostDialog, dialogProps));
assert.match(dialog, /<input type="radio" name="join-candidate" value="host-c"/, "a candidate with only will-not-converge rows stays selectable");
assert.match(dialog, /<input type="radio" disabled="" name="join-candidate" value="host-d"/, "a refusing candidate is visible but not selectable");
assert.match(dialog, /refuses join: 2 virtual machines · a host with virtual machines cannot join a pool/);
assert.doesNotMatch(dialog, /one way in this release|cannot be removed from the pool again/, "S5 gave the pool an eject, so the join dialog no longer says joining is one way");
// The dialog says what it is for before anything else: operators took it for the place to
// enter a fresh host's address. Until a host is chosen there is no address field and no
// confirm button, so nothing reads as "type the new host's address here"; the selected
// state is walked through in the browser (tests/integration/site_selector_browser.py).
assert.match(dialog, /This pool grows by joining a host already enrolled in this site\. Every enrolled host starts as the master of its own pool\./);
assert.doesNotMatch(dialog, /peer-origin|Add host to pool<\/button>/);
assert.match(dialog, /<button type="button">Cancel<\/button>/);
// The master works out its own peer address, so the dialog never asks for it.
assert.doesNotMatch(dialog, /master-url|Pool master peer address/);
// An error carries the server's own message, for example the 409 the Controller returns.
const conflict = renderToStaticMarkup(createElement(AddHostDialog, { ...dialogProps, submitError: "host core-d is already a member of pool pool-2 Submitting again repeats the same request; it never starts a second join." }));
assert.match(conflict, /role="alert"[^>]*>host core-d is already a member of pool pool-2/);
// Loading and candidate-error states.
assert.match(renderToStaticMarkup(createElement(AddHostDialog, { ...dialogProps, candidates: [], loading: true })), /Asking the hosts for their current state/);
assert.match(renderToStaticMarkup(createElement(AddHostDialog, { ...dialogProps, candidates: [], error: "pool master is not connected" })), /pool master is not connected/);
// An empty list is not "no master": the host to add has not enrolled yet, so lead to enrolling
// it. The button is the page's own Add host form; without a page there is only the sentence.
const noCandidates = renderToStaticMarkup(createElement(AddHostDialog, { ...dialogProps, candidates: [], onEnrollHost: () => {} }));
assert.match(noCandidates, /There is no other enrolled host at this site yet\. Enroll the new host first; once it reports, it appears here\./);
assert.match(noCandidates, /<button type="button">Enroll a new host…<\/button>/);
assert.doesNotMatch(noCandidates, /peer-origin|Add host to pool<\/button>|master of its own single-host pool/);
assert.doesNotMatch(renderToStaticMarkup(createElement(AddHostDialog, { ...dialogProps, candidates: [] })), /Enroll a new host/);

// The operator enters only the address; the origin Core wants is composed with the port the
// host reports. Anything but a bare address is refused rather than guessed at.
assert.deepEqual(peerOriginOf(" 192.0.2.12 ", 9444), { origin: "https://192.0.2.12:9444", error: "" });
assert.deepEqual(peerOriginOf("Core-B.example", 9500), { origin: "https://core-b.example:9500", error: "" });
assert.deepEqual(peerOriginOf("fd00::12", 9444), { origin: "https://[fd00::12]:9444", error: "" });
for (const bad of ["", "192.0.2.12:9444", "https://192.0.2.12:9444", "192.0.2.12/", "192.0.2", "host name"]) assert.match(peerOriginOf(bad, 9444).error, /address only/, bad);
// A candidate carries the address it reports and the port the origin is composed with.
assert.match(renderToStaticMarkup(createElement(AddHostDialog, { ...dialogProps, candidates: [{ ...candidates[0], peer_address: "192.0.2.12", peer_port: 9444 }] })), /host-c/);
assert.match(peerOriginOf("https://192.0.2.12:9444/api", 9444).error, /address only/);
assert.match(peerOriginOf("user:pw@192.0.2.12", 9444).error, /address only/);

// One request identity per submitted payload: a retry after a lost reply replays it, and only a
// real edit mints a new one.
let minted = 0;
Object.defineProperty(globalThis, "crypto", { configurable: true, value: { randomUUID: () => `request-${++minted}` } });
const held = { id: "", signature: "" };
assert.equal(heldRequestID(held, "pool-1\nhost-c\nhttps://192.0.2.12:9444"), "request-1");
assert.equal(heldRequestID(held, "pool-1\nhost-c\nhttps://192.0.2.12:9444"), "request-1", "an unchanged retry replays the same request");
assert.equal(heldRequestID(held, "pool-1\nhost-c\nhttps://192.0.2.13:9444"), "request-2", "an edited payload is a new request");
assert.equal(heldRequestID(held, "pool-1\nhost-d\nhttps://192.0.2.13:9444"), "request-3", "another host is a new request");
assert.equal(minted, 3);
// The POST body is exactly the contract's and carries the held request id rather than a fresh one.
assert.match(src, /const request_id = heldRequestID\(held\.current, `\$\{poolID\}\\n\$\{hostID\}\\n\$\{peerOrigin\}`\);/);
assert.match(src, /"POST", \{ request_id, host_id: hostID, peer_origin: peerOrigin \}\)/);
assert.doesNotMatch(src, /request_id: crypto\.randomUUID\(\)/);
// A failed submit keeps the dialog open with the same held request id: retrying replays it.
assert.doesNotMatch(src, /held\.current = \{ id: "", signature: "" \}/);

// ---------------------------------------------------------------- S2: maintenance mode

const drained = {
  ...member, maintenance: true, maintenance_since: "2026-09-19T10:00:00Z",
  guests: { known: true, total: 2, running: 1, names: ["build", "db"] },
};
const supported = { ...report, maintenance_supported: true, members: [{ ...master, maintenance: false, guests: { known: true, total: 0, running: 0, names: [] } }, drained] };

// The flag, its timestamp and the guests still on the host are rendered from the master's report.
const drainedRows = table({ report: supported, onMaintenance: () => {}, onMasterChange: () => {}, onEject: () => {} });
assert.match(drainedRows, /Maintenance <strong class="warn">In maintenance/);
assert.match(drainedRows, /Maintenance <strong class="ok">Available/, "a member that is not drained reads as available, not blank");
assert.match(drainedRows, /<small>Since 2026-09-19T10:00:00Z<\/small>/);
assert.match(drainedRows, /2 guests · 1 running · build, db/);
assert.match(drainedRows, />Exit maintenance</);
assert.match(drainedRows, />Enter maintenance</);

// A host whose guests are not known must say so; "no guests" would invite draining a busy host.
assert.equal(guestsText({ ...member, guests: { known: false, total: 0, running: 0, names: [] } }), "Guests unknown: this host is not reporting.");
assert.equal(guestsText(member), "Guests unknown: this host is not reporting.", "a member with no guests field at all is unknown too");
assert.equal(guestsText({ ...member, guests: { known: true, total: 0, running: 0, names: [] } }), "No guests on this host.");
assert.equal(guestsText({ ...member, guests: { known: true, total: 1, running: 0, names: ["build"] } }), "1 guest · 0 running · build");
assert.match(table({ report: { ...supported, members: [{ ...drained, guests: { known: false, total: 0, running: 0, names: [] } }] }, onMaintenance: () => {}, onMasterChange: () => {}, onEject: () => {} }), /Guests unknown: this host is not reporting\./);

// null maintenance (the master is not fresh) is unknown, never "available".
assert.deepEqual(maintenanceText({ ...member, maintenance: null }, true), { label: "unknown", badge: "unknown" });
assert.deepEqual(maintenanceText(member, true), { label: "unknown", badge: "unknown" }, "an absent flag is unknown");
assert.deepEqual(maintenanceText({ ...member, maintenance: false }, true), { label: "available", badge: "ok" });
assert.deepEqual(maintenanceText({ ...member, maintenance: true }, false), { label: "unknown", badge: "unknown" }, "a non-authoritative report is unknown whatever it holds");

// Without pool-maintenance-v1 on the master there is no toggle at all, and the reason is shown.
const unsupported = table({ report: { ...supported, maintenance_supported: false }, onMaintenance: () => {}, onMasterChange: () => {}, onEject: () => {} });
assert.doesNotMatch(unsupported, />Enter maintenance</);
assert.match(unsupported, /Maintenance mode needs pool-maintenance-v1 on the pool master; upgrade the pool master\./);
// With no handler (the view is unavailable) neither the button nor the upgrade note is offered.
assert.doesNotMatch(table({ report: supported }), /maintenance mode needs|Enter maintenance/i);

// The asynchronous 409: Core's own sentence, then what to do about it.
const conflictOp = { operation_id: "op-1", status: "failed", conflict: true, error: "pool membership changed since this request was composed; review the pool and try again" };
assert.equal(maintenanceResultText(conflictOp), "pool membership changed since this request was composed; review the pool and try again The pool changed since you loaded it — reload.");
assert.equal(maintenanceResultText({ operation_id: "op-2", status: "failed", error: "no such member of this pool" }), "Maintenance change failed: no such member of this pool");
assert.equal(maintenanceResultText({ operation_id: "op-3", status: "needs_attention", error: "maintenance mode could not be recorded durably" }), "Needs attention: maintenance mode could not be recorded durably");
assert.equal(maintenanceResultText({ operation_id: "op-4", status: "succeeded" }), "");
assert.equal(maintenanceResultText({ operation_id: "op-5", status: "queued" }), "Maintenance change submitted. The pool master records it; this shows its own report.");
assert.equal(maintenanceResultText(null), "");
const conflictRows = table({ report: { ...supported, members: [{ ...drained, last_maintenance: conflictOp }] }, onMaintenance: () => {}, onMasterChange: () => {}, onEject: () => {} });
assert.match(conflictRows, /role="alert"[^>]*>pool membership changed since this request was composed; review the pool and try again The pool changed since you loaded it — reload\./);
// An in-flight change is not offered a second button while Core is still working on it.
assert.equal(maintenanceInFlight({ operation_id: "op-6", status: "queued" }), true);
assert.equal(maintenanceInFlight({ operation_id: "op-7", status: "succeeded" }), false);
assert.equal(maintenanceInFlight(null), false);
assert.match(table({ report: { ...supported, members: [{ ...drained, last_maintenance: { operation_id: "op-6", status: "queued" } }] }, onMaintenance: () => {}, onMasterChange: () => {}, onEject: () => {} }), /<button type="button" disabled=""[^>]*>Exit maintenance</);

// The dialog states plainly what maintenance does and does not do, and lists the guests.
const enterDialog = renderToStaticMarkup(createElement(MaintenanceDialog, { member: { ...drained, maintenance: false }, busy: false, submitError: "", submitReady: true, onConfirm: () => {}, onClose: () => {} }));
assert.match(enterDialog, /Put core-b into maintenance/);
assert.match(enterDialog, /stops the pool master placing new virtual machines on this host and refuses starting a virtual machine here/);
assert.match(enterDialog, /does <strong>not<\/strong> stop, shut down or move any guest that is running now/);
assert.match(enterDialog, /never blocks stopping or shutting one down/);
assert.match(enterDialog, /2 guests · 1 running · build, db/);
assert.match(enterDialog, /<li>build<\/li><li>db<\/li>/);
assert.match(enterDialog, /aria-labelledby="maintenance-title"/);
const exitDialog = renderToStaticMarkup(createElement(MaintenanceDialog, { member: drained, busy: false, submitError: "", submitReady: true, onConfirm: () => {}, onClose: () => {} }));
assert.match(exitDialog, /Take core-b out of maintenance/);
assert.match(exitDialog, />Exit maintenance</);
// The 409 the Controller returns is shown verbatim.
const drainConflict = renderToStaticMarkup(createElement(MaintenanceDialog, { member: drained, busy: false, submitError: "the pool master reports membership generation 8; reload the pool and retry Submitting again repeats the same request; it never records a second change.", submitReady: true, onConfirm: () => {}, onClose: () => {} }));
assert.match(renderToStaticMarkup(createElement(MaintenanceDialog, { member: drained, busy: false, submitError: "", submitReady: false, onConfirm: () => {}, onClose: () => {} })), /<button type="button" class="primary" disabled="">Exit maintenance<\/button>/);
assert.equal(membershipVersionReady({ authoritative: true, generation: 7 }), true);
assert.equal(membershipVersionReady({ authoritative: false, generation: 7 }), false);
assert.equal(membershipVersionReady({ authoritative: true, generation: 0 }), false);
assert.match(src, /submitReady=\{membershipVersionReady\(report\)\}/);
assert.match(drainConflict, /role="alert"[^>]*>the pool master reports membership generation 8; reload the pool and retry/);

// The POST is exactly the contract's: desired state, the loaded generation, and the held request
// id so a retry after a lost reply replays the request rather than recording a second change.
assert.match(src, /const request_id = heldRequestID\(heldDrain\.current, `\$\{poolID\}\\n\$\{member\.host_id\}\\n\$\{enabled\}`\);/);
assert.match(src, /\/members\/\$\{encodeURIComponent\(member\.host_id\)\}\/maintenance`, "POST", \{ request_id, enabled, expected_version: report\?\.generation \|\| 0 \}\)/);
// The maintenance command takes no confirmation field: the exact body above is the whole of
// it, and the maintenance dialog collects nothing to put there.
assert.doesNotMatch(src.slice(src.indexOf("async function setMaintenance"), src.indexOf("} finally { setDrainBusy(false); }")), /confirmation/);
// A retry with the same target and desired state keeps the same request id.
const heldDrainTest = { id: "", signature: "" };
assert.equal(heldRequestID(heldDrainTest, "pool-1\nhost-b\ntrue"), "request-4");
assert.equal(heldRequestID(heldDrainTest, "pool-1\nhost-b\ntrue"), "request-4", "a retry replays the same maintenance request");
assert.equal(heldRequestID(heldDrainTest, "pool-1\nhost-b\nfalse"), "request-5", "the opposite desired state is a different request");

// ---------------------------------------------------------------- S4: authority replica

const replicaOf = extra => ({ generation: 7, digest: "9f2c", verified: true, received_at: "2026-09-19T10:04:11Z", acknowledged_generation: 7, acknowledged_at: "2026-09-19T10:04:11Z", error: "", state: "current", ...extra });
const withReplica = (replica, extra) => ({ ...report, replica_supported: true, members: [master, { ...member, replica, ...extra }] });

// Every state is a word, not a colour, and it is the host-reported state that is shown.
assert.deepEqual(replicaText({ ...member, replica: replicaOf({}) }), { result: "OK", tone: "ok" });
assert.deepEqual(replicaText({ ...member, replica: replicaOf({ state: "behind" }) }), { result: "Behind", tone: "warn" });
assert.equal(replicaText({ ...member, replica: replicaOf({ state: "unverified", verified: false }) }).result, "Unverified");
assert.deepEqual(replicaText({ ...member, replica: replicaOf({ state: "refused" }) }), { result: "Failed", tone: "bad" });
assert.equal(replicaText({ ...member, replica: replicaOf({ state: "awaiting" }) }).result, "Awaiting");
assert.equal(replicaText({ ...member, replica: replicaOf({ state: "unsupported" }) }).result, "Not supported");
assert.equal(replicaText({ ...member, replica: replicaOf({ state: "unknown" }) }).result, "Unknown");
assert.equal(replicaText({ ...member, replica: replicaOf({ state: "invented" }) }).result, "Unknown", "a state this build does not know is unknown, never guessed");
assert.equal(replicaText(member).result, "Unknown", "a member with no replica report is unknown");
// What a member holds is its own fresh report and stays readable without a fresh master —
// with the master gone it is how a promotion candidate is chosen. The Controller limits the
// state itself: current and behind compare against a number only the master reports, so they
// never arrive without one, and the master's acknowledgement disappears with the master.
const staleReplica = table({ report: { ...withReplica(replicaOf({ state: "unverified", verified: false })), master_connection: "stale", authoritative: false } });
assert.match(staleReplica, /Authority copy <strong class="warn">Unverified</, "the member's own report of its copy is still shown");
assert.match(staleReplica, /Received <time dateTime="2026-09-19T10:04:11Z"/, "and when it received it");
assert.doesNotMatch(staleReplica, /Acknowledged/, "the acknowledgement is the master's observation");
assert.doesNotMatch(staleReplica, /This host reports copy/, "and so is the number it is compared with");
assert.doesNotMatch(table({ report: { ...withReplica(replicaOf({})), master_connection: "stale", authoritative: false } }), /generations behind/, "nothing is called behind without the master's generation");

// The header and the current row.
const currentRows = table({ report: withReplica(replicaOf({})) });
assert.match(currentRows, /<dt>Authority copy<\/dt>/);
assert.match(currentRows, /Authority copy <strong class="ok">OK</);
assert.match(currentRows, /Received <time dateTime="2026-09-19T10:04:11Z" title="2026-09-19T10:04:11Z">[^<]+<\/time> · Acknowledged <time dateTime="2026-09-19T10:04:11Z"/);
assert.doesNotMatch(currentRows, /This host reports copy/, "agreeing reports need no explanation");

// The master's own row holds the authority; it is not blank and it is not a state.
assert.match(currentRows, /<small>Holds the pool records<\/small>/);

// Both generations, with a plain sentence saying which side is the older report.
assert.equal(replicaGenerations(null), "");
assert.equal(replicaGenerations(replicaOf({})), "");
assert.match(replicaGenerations(replicaOf({ generation: 6, state: "behind" })), /^This host reports copy 6; the pool master last saw it acknowledge 7\. This host has not caught up with what the pool master recorded\.$/);
assert.match(replicaGenerations(replicaOf({ acknowledged_generation: 6, state: "behind" })), /^This host reports copy 7; the pool master last saw it acknowledge 6\. The master's record of this host is the older of the two\.$/);
const behindRows = table({ report: withReplica(replicaOf({ generation: 6, state: "behind" })) });
assert.match(behindRows, /Authority copy <strong class="warn">Behind</);
assert.match(behindRows, /This host reports copy 6; the pool master last saw it acknowledge 7\./);

// A refusal shows Core's own text.
const refusedRows = table({ report: withReplica(replicaOf({ state: "refused", verified: false, error: "replica signature does not verify against the pool CA" })) });
assert.match(refusedRows, /Authority copy <strong class="bad">Failed</);
assert.match(refusedRows, /task-error">replica signature does not verify against the pool CA/);

// Awaiting the first copy: no numbers, no times, no alarm.
const awaitingRows = table({ report: withReplica({ generation: 0, digest: "", verified: false, received_at: "", acknowledged_generation: 0, acknowledged_at: "", error: "", state: "awaiting" }) });
assert.match(awaitingRows, /Authority copy <strong class="neutral">Awaiting</);
assert.doesNotMatch(awaitingRows, /This host reports copy/);
assert.equal(replicaTimes({ generation: 0, acknowledged_generation: 0 }), null);
assert.equal(replicaTimes(null), null);
assert.match(renderToStaticMarkup(replicaTimes(replicaOf({ acknowledged_at: "" }))), /^<small>Received <time dateTime="2026-09-19T10:04:11Z" title="2026-09-19T10:04:11Z">[^<]+<\/time><\/small>$/);

// A member the master records as too old to hold a copy: the state and Core's reason.
const upgradeRows = table({ report: withReplica(replicaOf({ generation: 0, acknowledged_generation: 0, verified: false, received_at: "", acknowledged_at: "", state: "unsupported", error: "host core-b does not advertise pool-authority-replica-v1; upgrade it" })) });
assert.match(upgradeRows, /Authority copy <strong class="warn">Not supported</);
assert.match(upgradeRows, /host core-b does not advertise pool-authority-replica-v1; upgrade it/);

// A master without the capability: no check and no detail line, one note on the pool strip instead.
const noReplicas = table({ report: { ...withReplica(replicaOf({})), replica_supported: false } });
assert.doesNotMatch(noReplicas, /<dt>Authority copy<\/dt>|>Authority copy </);
assert.match(noReplicas, /Authority copies <strong class="warn">Not supported</);
assert.match(noReplicas, /The pool master does not support authority copies, so no member holds one\. Upgrade the pool master/);
// With copies supported, one sentence says what the copy is for and that it is not used while the master is healthy.
assert.match(currentRows, /signed, read-only copy of the pool(&#x27;|')s records so the pool can be recovered if its master is lost; it is never used while the master is healthy/);

// The fourth health check is labelled like the other three in the convergence summary.
const checked = table({ report: withReplica(replicaOf({}), { checks: [...member.checks, { name: "pool-authority-replica", status: "failed", detail: "no copy acknowledged for 2 probes" }] }) });
assert.match(checked, /Authority copy: failed · no copy acknowledged for 2 probes/);
assert.equal(convergenceSummary({ ...member, checks: [{ name: "pool-authority-replica", status: "healthy" }] }, true), "Checks 1 healthy · Access 2 healthy, 1 failed");
// The checks array is Core's, and it grows. The summary counts by status and the rows are keyed
// by name, so nothing here depends on how many entries there are or what order they arrive in.
const sevenChecks = [
  { name: "pool-storage-reconciliation", status: "healthy" },
  { name: "pool-network-reconciliation", status: "healthy" },
  { name: "pool-media-reconciliation", status: "healthy" },
  { name: "pool-authority-replica", status: "healthy" },
  { name: "pool-restricted", status: "failed", detail: "restricted" },
  { name: "pool-promotion", status: "healthy" },
  { name: "pool-authority-transfer", status: "unknown", detail: "" },
];
assert.equal(convergenceSummary({ ...member, checks: sevenChecks }, true), "Checks 5 healthy, 1 failed, 1 unknown · Access 2 healthy, 1 failed");
assert.equal(convergenceSummary({ ...member, checks: [...sevenChecks].reverse() }, true), convergenceSummary({ ...member, checks: sevenChecks }, true), "order changes nothing");
assert.equal(convergenceSummary({ ...member, checks: [...sevenChecks, { name: "pool-something-later", status: "healthy" }] }, true), "Checks 6 healthy, 1 failed, 1 unknown · Access 2 healthy, 1 failed");
// The 7th check is named in words, and pool-restricted is still not repeated in this column.
const sevenTable = table({ report: { ...report, members: [{ ...member, checks: sevenChecks }] } });
assert.match(sevenTable, /Master change: unknown/);
assert.doesNotMatch(sevenTable, /pool-authority-transfer/, "the check is labelled, not printed raw");
assert.doesNotMatch(sevenTable, /Restricted: failed/);
// Nothing indexes the array by position or by length.
assert.doesNotMatch(src, /checks\s*\[\s*\d/);
assert.doesNotMatch(src, /checks(\s*\|\|\s*\[\])?\)?\.length\s*(===|!==|>|<|>=|<=)\s*\d/);

// ------------------------------------------- S5: planned master change and member eject

const transferReport = {
  ...report, replica_supported: true, transfer_supported: true,
  authority: { generation: 9, digest: "9f2c", observed_at: "2026-09-19T10:04:11Z" },
  transfer: null, last_transfer: null, master_conflict: [],
  members: [master, { ...member, replica: replicaOf({ generation: 7, acknowledged_generation: 7, state: "behind" }) }],
};
const handlers = { onMasterChange: () => {}, onEject: () => {}, onMaintenance: () => {} };

// The row actions are one menu at the end of each row's name line.
const withActions = table({ report: transferReport, ...handlers });
assert.match(withActions, /<details class="menu pool-row-actions menu-end"><summary>Actions<span class="sr-only"> for [^<]+<\/span><\/summary>/);
assert.match(withActions, />Make pool master</);
assert.match(withActions, />Eject from pool</);
assert.equal((withActions.match(/<details class="menu pool-row-actions menu-end"/g) || []).length, 2, "one menu per row");
assert.doesNotMatch(withActions, /<table/, "the members are rows, not a wide table");
// Without the handlers (an unavailable view) there are no row actions at all.
assert.doesNotMatch(table({ report: transferReport }), /pool-row-actions/);
// The master's row does not offer actions that can never apply to it.
assert.doesNotMatch(withActions, /This host is already the pool master\.|The pool master cannot be ejected/);
assert.equal((withActions.match(/>Make pool master</g) || []).length, 1, "only the member's row offers it");

// The absolute generation the S4 column had no reference for, and how far behind a copy is.
assert.match(withActions, /The pool authority is at generation 9\./);
assert.match(withActions, /<small>2 generations behind<\/small>/);
assert.doesNotMatch(table({ report: { ...transferReport, authority: null }, ...handlers }), /generations behind/);
assert.match(table({ report: { ...transferReport, authority: null }, ...handlers }), /not reporting an authority generation/);

// While a transfer is pending every other pool authority change is refused, and the reason
// is one note the disabled buttons point at rather than a silently dead control.
const pendingTransfer = { operation_id: "op-1", action: "promote", role: "source", target_host_id: "host-b", stage: "replicate", status: "pending", frozen: true, observed_at: "2026-09-19T10:05:02Z" };
const blocked = "A master change or eject is running on this pool. The pool master refuses every other pool authority change until it completes; retry when it does.";
const blockedRows = table({ report: { ...transferReport, maintenance_supported: true, transfer: pendingTransfer }, blockedReason: blocked, ...handlers });
assert.match(table({ report: { ...transferReport, maintenance_supported: true }, ...handlers }), /<button type="button">Enter maintenance</, "with no transfer running the toggle is live");
assert.match(blockedRows, /<button type="button" aria-describedby="pool-actions-blocked" disabled="">Enter maintenance</);
assert.match(blockedRows, /<button type="button" aria-describedby="pool-actions-blocked [^"]+-promote" disabled="">Make pool master</);
assert.match(blockedRows, /<button type="button" class="danger" aria-describedby="pool-actions-blocked [^"]+-eject" disabled="">Eject from pool</);
assert.match(blockedRows, /disabled=""[^>]*>Make pool master<\/button><small id="[^"]+-promote">Another pool authority transfer is already in progress on this pool\.<\/small>/);
assert.match(blockedRows, /disabled=""[^>]*>Eject from pool<\/button><small id="[^"]+-eject">Another pool authority transfer is already in progress on this pool\.<\/small>/);
// The same reason disables "Add host to pool" and the polling covers a running transfer.
assert.match(src, /const transferring = transferInProgress\(report\?\.transfer\) \|\| \(report\?\.members \|\| \[\]\)\.some\(member => transferInProgress\(member\.transfer\)\);/);
assert.match(src, /if \(\(!joining && !transferring && !promoting\) \|\| unavailable\) return;/);
assert.match(src, /aria-describedby=\{blockedReason \? "pool-actions-blocked" : undefined\} disabled=\{unavailable \|\| !report \|\| Boolean\(blockedReason\)\}/);
assert.match(src, /<p className="warning" id="pool-actions-blocked" role="status">\{blockedReason\}<\/p>/);

// The three requests are exactly the contract's bodies, each with the loaded membership
// generation and a held request id so a retry after a lost reply replays it.
assert.match(src, /`pools\/\$\{encodeURIComponent\(poolID\)\}\/master`, "POST", \{ request_id, host_id: member\.host_id, expected_version: report\?\.generation \|\| 0, confirmation \}\)/);
assert.match(src, /`pools\/\$\{encodeURIComponent\(poolID\)\}\/members\/\$\{encodeURIComponent\(member\.host_id\)\}`, "DELETE", \{ request_id, expected_version: report\?\.generation \|\| 0, confirmation \}\)/);
// The cancel carries Core's fence fields only for the fence-backed resume; the plain cancel
// body is unchanged. (Both bodies are asserted in full in pool-authority.test.mjs.)
assert.match(src, /: \{ request_id, host_id: target, expected_version: report\?\.generation \|\| 0 \}\)/);
assert.match(src, /heldRequestID\(heldTransfer\.current, `\$\{poolID\}\\nmaster-change\\n\$\{member\.host_id\}\\n\$\{confirmation\}`\)/);
assert.match(src, /heldRequestID\(heldTransfer\.current, `\$\{poolID\}\\neject\\n\$\{member\.host_id\}\\n\$\{confirmation\}`\)/);
assert.match(src, /heldRequestID\(heldCancel\.current, `\$\{poolID\}\\ntransfer-cancel\\n\$\{target\}\\n\$\{fenceTargetID \|\| ""\}`\)/);
// A retry of the same master change replays one request; a different target is a new one.
const heldTransferTest = { id: "", signature: "" };
assert.equal(heldRequestID(heldTransferTest, "pool-1\nmaster-change\nhost-b\ncore-a"), "request-6");
assert.equal(heldRequestID(heldTransferTest, "pool-1\nmaster-change\nhost-b\ncore-a"), "request-6", "a retry replays the same master change");
assert.equal(heldRequestID(heldTransferTest, "pool-1\neject\nhost-b\ncore-b"), "request-7", "the eject of the same host is a different request");

// ------------------------------- S6: the recovery view, restricted hosts and fence devices

// When the pool master is gone the Hosts view becomes the recovery view. The one thing it
// must never do is read an absent report as a host that stopped.
// Since when, from the Controller's own record of when that host last reached it: the interval
// an operator reads at a glance and the exact instant the hosts and the audit trail use.
const now = Date.parse("2026-09-19T12:00:00Z");
const lostMaster = { ...report, master_connection: "stale", authoritative: false, master_seen_at: "2026-09-19T11:48:00Z" };
assert.equal(recoveryText(lostMaster, now),
  "The pool master core-a last reported to this Controller 12 minutes ago, at 2026-09-19T11:48:00Z; it is stale. Its state is unknown — this does not mean its VMs have stopped.");
assert.match(recoveryText({ ...lostMaster, master_seen_at: "2026-09-17T12:00:00Z" }, now), /last reported to this Controller 2 days ago, at 2026-09-17T12:00:00Z/);
assert.match(recoveryText({ ...lostMaster, master_seen_at: "2026-09-19T09:00:00Z" }, now), /last reported to this Controller 3 hours ago, at 2026-09-19T09:00:00Z/);
assert.equal(seenAgo("2026-09-19T11:59:58Z", now), "2 seconds ago");
assert.equal(seenAgo("not a time", now), "", "an unparsable timestamp says nothing rather than a wrong interval");
// A master that has never reached this Controller says exactly that; it is not "just now".
assert.equal(recoveryText({ ...lostMaster, master_seen_at: "" }, now),
  "The pool master core-a has never reported to this Controller; it is stale. Its state is unknown — this does not mean its VMs have stopped.");
assert.equal(recoveryText({ ...lostMaster, master_seen_at: undefined }, now), recoveryText({ ...lostMaster, master_seen_at: "" }, now));
// The sentence that must never change, on every path.
for (const text of [recoveryText(lostMaster, now), recoveryText({ ...lostMaster, master_seen_at: "" }, now), recoveryText({ ...lostMaster, master_connection: "unknown" }, now)]) {
  assert.match(text, /Its state is unknown — this does not mean its VMs have stopped\.$/);
}
assert.match(recoveryText({ ...lostMaster, master_connection: "unknown" }, now), /; it is unknown\. Its state is unknown/);
assert.match(src, /<div className="warning pool-recovery" role="status">/);
assert.match(src, /\{recoveryText\(report\)\}/);
assert.match(src, /its fence device and everything the master observed — is shown as unknown below/);
assert.match(src, /a surviving member that holds a copy of the pool records it has verified can be promoted from its row/);
assert.doesNotMatch(src, /so this membership is not current\. Every host's connection/, "the old wall-of-unknowns warning is gone");

// The recovery row: this host's own report of what it holds, its restriction and its
// capability, beside master-derived fields that stay unknown.
const restricted = {
  reason: "this host's pool authority was superseded by another master; it starts no shared-storage guest and writes no pool authority until it has rejoined and reconciled (VM ownership conflicts: vm-7)",
  since: "2026-09-19T10:11:00Z", conflicts: ["vm-7"],
};
const recovery = {
  ...report, master_connection: "stale", authoritative: false, replica_supported: true, transfer_supported: true,
  promotion: null, last_promotion: null,
  members: [
    { ...master, connection: "stale" },
    // Without the master there is no number to compare against, so the Controller limits the
    // state to what the member alone proves: it holds a copy it verified, and nothing is
    // called current or behind.
    { ...member, transfer_supported: true, replica: replicaOf({ state: "unknown", acknowledged_generation: 0, acknowledged_at: "" }), restricted },
  ],
};
const recoveryRows = table({ report: recovery, ...handlers, onPromote: () => {}, onFenceProfile: () => {} });
assert.match(recoveryRows, /badge bad">Restricted<\/span>/);
assert.match(recoveryRows, /this host&#x27;s pool authority was superseded by another master/);
assert.match(recoveryRows, /<small>Since 2026-09-19T10:11:00Z<\/small>/);
assert.match(recoveryRows, /Virtual machines this host still defines that the pool records elsewhere: vm-7/);
assert.match(recoveryRows, /Authority copy <strong class="neutral">Unknown</);
assert.match(recoveryRows, /<dd class="pool-replica-cell"><small>Received <time dateTime="2026-09-19T10:04:11Z" title="2026-09-19T10:04:11Z">[^<]+<\/time><\/small><\/dd>/, "the member's own copy is still reported, without a claim the master alone could support");
assert.match(recoveryRows, /Membership <strong class="bad">Restricted</, "the restriction is the row's membership result");
assert.match(recoveryRows, /<details class="pool-member-more" open="">/, "and its explanation starts open");
// A candidate that has not verified the copy it holds is not offered the action at all.
assert.match(table({ report: { ...recovery, members: [recovery.members[0], { ...recovery.members[1], replica: replicaOf({ state: "unverified", verified: false }) }] }, ...handlers, onPromote: () => {} }),
  /disabled=""[^>]*>Promote to pool master…<\/button><small>This host does not report a copy of the pool records it has verified, so it cannot be promoted\.<\/small>/);
assert.match(recoveryRows, /Fence device: unknown — the pool master keeps this record and is not reporting\./);
assert.match(recoveryRows, /<button type="button" class="danger">Promote to pool master…<\/button>/);
assert.match(recoveryRows, /<button type="button" disabled="">Set BMC access…<\/button>/);
assert.equal((recoveryRows.match(/<details class="menu pool-row-actions menu-end"/g) || []).length, 2, "one disclosure per row, not one per slice");
// Core's own pool-restricted check is not repeated in the convergence cell beside the badge.
const checkedRestricted = table({ report: { ...recovery, members: [recovery.members[0], { ...recovery.members[1], checks: [{ name: "pool-restricted", status: "failed", detail: restricted.reason }] }] }, ...handlers });
assert.equal((checkedRestricted.match(/>[^<]*superseded by another master/g) || []).length, 1, "shown once; the Membership result's tooltip repeats it only as a tooltip");
// With a fresh master the fence device is the master's record, and recording one is offered.
const fenced = table({ report: { ...report, transfer_supported: true, members: [{ ...master, fence: { kind: "virsh", endpoint: "lab.example.test", configured: true } }, member] }, ...handlers, onPromote: () => {}, onFenceProfile: () => {} });
assert.match(fenced, /Fence device: virsh · lab\.example\.test · configured/);
assert.match(fenced, /<button type="button">Set BMC access…<\/button>/);
// A promotion is not offered against a pool master that is reporting.
assert.match(fenced, /Use “Make pool master” for a planned master change; promoting is for a pool master that is gone\./);

console.log("pool-members.test.mjs: ok");

// The pool's own fencing line, counted from the master's per-host answers. It never turns a
// quiet master into a protected pool, and it names the hosts whose rows say why.
{
  const readiness = ready => ({ ready, methods: [{ kind: "ipmilan", state: ready ? "ready" : "not_ready", reason: ready ? "" : "this management controller did not answer (from core-b)" }] });
  const withPool = pool => ({
    ...report, authoritative: true, fence: pool,
    members: [
      { ...report.members[0], fence: { configured: true, kind: "ipmilan", readiness: readiness(true) } },
      { ...report.members[1], fence: { configured: true, kind: "ipmilan", readiness: readiness(false) } },
    ],
  });
  // The count is the pool master's own, on its own row, and the names are resolved through
  // the same membership; nothing here counts the rows it is about to render.
  const fenced = withPool({ ready: 1, total: 2, unready: [report.members[1].host_id], host_fencing: true });
  assert.match(fencingSummaryText(fenced), /Fencing: ready for 1 of 2 hosts\./);
  assert.match(fencingSummaryText(fenced), /the Fencing details on that row say why/);
  assert.match(fencingSummaryText(fenced), new RegExp(report.members[1].name));
  assert.match(fencingSummaryText(withPool({ ready: 2, total: 2, host_fencing: true })), /Fencing: ready for 2 of 2 hosts\./);
  assert.match(fencingSummaryText({ ...fenced, authoritative: false }), /^Fencing: unknown/);
  // A master too old to report a count, and a pool of one host, each say their own thing.
  assert.match(fencingSummaryText(withPool(undefined)), /^Fencing: not reported/);
  assert.match(fencingSummaryText(withPool({ ready: 0, total: 1, host_fencing: true })), /this pool has one host/);
  // Each row carries its own Fencing result and a labelled Fencing detail.
  assert.match(table({ report: fenced }), /Fencing <strong class="bad">Failed</);
  assert.match(table({ report: fenced }), /<dt>Fencing<\/dt><dd class="pool-fencing-cell">/);
}

// The Host fencing setting. It defaults to on, so the operator has to meet the consequence
// without going looking for it, and the checklist beside it is Core's own.
{
  const withSetting = (fence, extra = {}) => ({ ...report, authoritative: true, host_fencing_supported: true, fence, ...extra });
  const summary = {
    ready: 3, total: 3, host_fencing: true,
    prerequisites: [
      { name: "hosts", state: "met", detail: "this pool has 3 hosts, so a partition can have a majority side" },
      { name: "watchdogs", state: "unmet", detail: "these hosts report no hardware watchdog: core-b" },
      { name: "quorum", state: "unknown", detail: "this pool has no quorum service yet" },
    ],
  };
  const setting = renderToStaticMarkup(createElement(HostFencingSetting, { report: withSetting(summary), onChange: () => {} }));
  assert.match(setting, /Host fencing/);
  assert.match(setting, /checked=""/, "the switch shows the state the pool master records");
  // The consequence is on the page, not behind the switch.
  assert.match(setting, /resets itself and its running virtual machines stop/);
  assert.match(setting, /reboots the minority side/);
  assert.match(setting, /does not arm anything by itself/);
  // Core's checklist, with Core's own sentences and the line names this page supplies.
  assert.match(setting, /Hardware watchdogs/);
  assert.match(setting, /these hosts report no hardware watchdog: core-b/);
  assert.match(setting, /Pool quorum/);
  assert.match(setting, /this pool has no quorum service yet/);
  // Turned off, the switch follows the master and the consequence is still shown.
  const off = renderToStaticMarkup(createElement(HostFencingSetting, { report: withSetting({ ...summary, host_fencing: false }), onChange: () => {} }));
  assert.doesNotMatch(off, /checked=""/);
  assert.match(off, /reboots the minority side/);
  // A master that is not reporting, and one too old for the setting, each say their own
  // thing and offer no switch at all.
  const quiet = renderToStaticMarkup(createElement(HostFencingSetting, { report: withSetting(summary, { authoritative: false }) }));
  assert.match(quiet, /is not reporting/);
  assert.doesNotMatch(quiet, /<input/);
  const older = renderToStaticMarkup(createElement(HostFencingSetting, { report: { ...report, authoritative: true, host_fencing_supported: false } }));
  assert.match(older, /too old to report the host fencing setting/);
  assert.doesNotMatch(older, /<input/);

  // The pool's own cluster: what it is doing, which host it is waiting for by name, and the
  // management-network consequence, which is now a way an armed host can be reset by planned
  // work rather than by a failure.
  assert.match(setting, /refused while that host is armed/);
  const arming = renderToStaticMarkup(createElement(HostFencingSetting, {
    report: withSetting({ ...summary, cluster: { state: "arming", detail: "arming host 2 of 3", armed: 1, total: 3, arming_host_id: report.members[1].host_id } }),
    onChange: () => {},
  }));
  assert.match(arming, /1 of 3 hosts armed/);
  assert.match(arming, new RegExp(`Waiting for ${report.members[1].name}`), "the host whose turn it is is named, not shown as an opaque ID");
  const unavailable = renderToStaticMarkup(createElement(HostFencingSetting, {
    report: withSetting({ ...summary, cluster: { state: "not_available", detail: "this pool runs clustered storage; watchdog fencing for it is not available yet", armed: 0, total: 0 } }),
    onChange: () => {},
  }));
  assert.match(unavailable, /this pool runs clustered storage/);
  assert.match(unavailable, /not available/);
}

// A refused Host fencing toggle is a panel action, not a failed membership read: its refusal
// lands in the action alert, so the page never claims the master could not be asked for its
// membership and the refusal clears on the next attempt.
{
  const handler = src.slice(src.indexOf("async function setHostFencing"), src.indexOf("async function setBMCProvisioning"));
  assert.match(handler, /setActionError\(""\); setNotice\(""\);/, "the toggle clears the previous refusal before asking again");
  assert.match(handler, /setActionError\(`\$\{\(e as Error\)\.message\}/, "Core's refusal goes to the action alert");
  assert.doesNotMatch(handler, /setError\(/, "setError is refresh()'s, and renders as a membership read failure");
}

// The status strip: each check's short result, derived from what the hosts and the master report.
{
  const results = (m, r = report) => Object.fromEntries(memberChecks(m, r).map(check => [check.label, check.result]));
  const tones = (m, r = report) => Object.fromEntries(memberChecks(m, r).map(check => [check.label, check.tone]));
  const healthy = { ...member, checks: [{ name: "pool-storage-reconciliation", status: "healthy" }], access: { healthy: 2, pending: 0, failed: 0, stale: 0 }, failures: [], maintenance: false,
    replica: { generation: 9, acknowledged_generation: 9, verified: true, state: "current" }, fence: { configured: true, kind: "ipmilan", readiness: { ready: true, methods: [{ kind: "ipmilan", state: "ready", checks: [{ host_id: "host-a", result: "ok" }] }] } } };
  const live = { ...report, replica_supported: true, authority: { generation: 9 } };
  assert.deepEqual(results(healthy, live), { Connection: "OK", Membership: "OK", Maintenance: "Available", "Authority copy": "OK", Convergence: "OK", Fencing: "OK" });
  assert.deepEqual(tones(healthy, live), { Connection: "ok", Membership: "ok", Maintenance: "ok", "Authority copy": "ok", Convergence: "ok", Fencing: "ok" });
  // A stale host cannot vouch for its own last report: what it reported reads Unknown, never OK.
  assert.deepEqual(results({ ...healthy, connection: "stale" }, live), { Connection: "Stale", Membership: "OK", Maintenance: "Available", "Authority copy": "Unknown", Convergence: "Unknown", Fencing: "OK" });
  assert.equal(tones({ ...healthy, connection: "stale" }, live).Convergence, "neutral");
  assert.equal(results({ ...healthy, connection: "revoked" }, live).Connection, "Revoked");
  assert.equal(results({ ...healthy, connection: "unknown" }, live).Connection, "Unknown");
  // A master that is not reporting leaves its own records Unknown; the host's own reports stay.
  const quiet = results(healthy, { ...live, authoritative: false });
  assert.equal(quiet.Membership, "Unknown");
  assert.equal(quiet.Maintenance, "Unknown");
  assert.equal(quiet.Fencing, "Unknown");
  assert.equal(quiet.Convergence, "Unknown", "access is the master's observation, so convergence cannot read OK");
  assert.equal(quiet.Connection, "OK");
  // Membership: the restriction and the join are the host's own; the status is the master's.
  assert.equal(results({ ...healthy, restricted: { reason: "restricted" } }, live).Membership, "Restricted");
  assert.equal(results({ ...healthy, join: { stage: "csr", status: "pending" } }, live).Membership, "Joining");
  assert.equal(results({ ...healthy, join: { stage: "csr", status: "failed" } }, live).Membership, "Failed");
  assert.equal(results({ ...healthy, join: { stage: "rehomed", status: "needs_attention" } }, live).Membership, "Needs attention");
  assert.equal(results({ ...healthy, status: "failed" }, live).Membership, "Failed");
  assert.equal(results({ ...healthy, status: "needs_attention", error: "The host has not reported for 7 minutes." }, live).Membership, "Needs attention");
  assert.equal(memberChecks({ ...healthy, status: "needs_attention", error: "The host has not reported for 7 minutes." }, live)[1].detail, "The host has not reported for 7 minutes.", "the reason is the tooltip");
  assert.equal(results({ ...healthy, maintenance: true }, live).Maintenance, "In maintenance");
  // Authority copy: behind, refused and unreported are never OK; the master's own row has none.
  assert.equal(results({ ...healthy, replica: { ...healthy.replica, state: "behind" } }, live)["Authority copy"], "Behind");
  assert.equal(results({ ...healthy, replica: { ...healthy.replica, state: "refused" } }, live)["Authority copy"], "Failed");
  assert.equal(results({ ...healthy, replica: null }, live)["Authority copy"], "Unknown");
  assert.equal(results({ ...healthy, role: "master" }, live)["Authority copy"], undefined);
  // Convergence: any failure fails it; nothing reported, an unknown check or stale access is Unknown.
  assert.equal(results({ ...healthy, checks: [{ name: "pool-network-reconciliation", status: "failed" }] }, live).Convergence, "Failed");
  assert.equal(results({ ...healthy, failures: [{ kind: "network", id: "n", name: "n", error: "e" }] }, live).Convergence, "Failed");
  assert.equal(results({ ...healthy, access: { healthy: 1, pending: 0, failed: 1, stale: 0 } }, live).Convergence, "Failed");
  assert.equal(results({ ...healthy, checks: [] }, live).Convergence, "Not reported");
  assert.equal(results({ ...healthy, checks: [{ name: "pool-media-reconciliation", status: "unknown" }] }, live).Convergence, "Unknown");
  assert.equal(results({ ...healthy, access: { healthy: 1, pending: 0, failed: 0, stale: 1 } }, live).Convergence, "Unknown");
  assert.equal(results({ ...healthy, access: { healthy: 1, pending: 1, failed: 0, stale: 0 } }, live).Convergence, "Pending");
  assert.equal(results({ ...healthy, checks: [...healthy.checks, { name: "pool-restricted", status: "failed" }] }, live).Convergence, "OK", "the restriction is Membership's, not counted twice");
  // Fencing: the master's per-host answer, in one word.
  const method = extra => ({ ...healthy, fence: { configured: true, kind: "ipmilan", readiness: { ready: false, methods: [{ kind: "ipmilan", ...extra }] } } });
  assert.equal(results(method({ state: "not_ready", reason: "no answer" }), live).Fencing, "Failed");
  assert.equal(results(method({ state: "not_ready", checks: [{ host_id: "host-a", result: "ok", stale: true }] }), live).Fencing, "Stale");
  assert.equal(results({ ...healthy, fence: null }, live).Fencing, "Not reported");
  assert.equal(results(method({ state: "no_peer" }), live).Fencing, "Unknown");

  // The pool strip: every value the master's, Unknown without it.
  const pool = r => Object.fromEntries(poolChecks(r).map(check => [check.label, check.result]));
  assert.deepEqual(pool(live), { "Authority generation": "9", Fencing: "Not reported (upgrade pool master)", "Host fencing": "Not reported" });
  assert.deepEqual(pool({ ...live, authoritative: false }), { "Authority generation": "Unknown", Fencing: "Unknown", "Host fencing": "Unknown" });
  assert.deepEqual(pool({ ...live, fence: { ready: 1, total: 2, host_fencing: true, cluster: { state: "degraded", armed: 1, total: 2 } } }),
    { "Authority generation": "9", Fencing: "Not ready (1 of 2)", "Host fencing": "On", "Watchdog cluster": "Degraded" });
  assert.equal(pool({ ...live, fence: { ready: 2, total: 2, host_fencing: false } }).Fencing, "OK (2 of 2)");
  assert.equal(pool({ ...live, fence: { ready: 2, total: 2, host_fencing: false } })["Host fencing"], "Off");
  assert.equal(pool({ ...live, fence: { ready: 0, total: 1, host_fencing: true } }).Fencing, "Not needed (one host)");
  assert.equal(pool({ ...live, replica_supported: false })["Authority copies"], "Not supported");
  // The Host fencing switch and its consequence are still on the page, behind the strip's About.
  const setting = table({ report: { ...live, host_fencing_supported: true, fence: { ready: 2, total: 2, host_fencing: true, prerequisites: [] } }, onHostFencing: () => {} });
  assert.match(setting, /<details class="pool-status">[\s\S]*<input type="checkbox" checked=""[\s\S]*reboots the minority side[\s\S]*<\/details><ul class="pool-members">/);
}
