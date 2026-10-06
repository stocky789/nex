// SPDX-License-Identifier: AGPL-3.0-only
// S6 "Fence-verified manual promotion and safe rejoin": same harness as the other pool tests —
// esbuild bundles the real module and react-dom/server renders it.
// Run with: node src/pool-promotion.test.mjs
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const moduleURL = bundleTestModule(new URL("./pool-promotion.tsx", import.meta.url), "pool-promotion");
const src = readFileSync(new URL("./pool-promotion.tsx", import.meta.url), "utf8");
const membersSrc = readFileSync(new URL("./pool-members.tsx", import.meta.url), "utf8");
const {
  PromotionActions, PromotionDialog, PromotionStrip, PromotionVerdictView, RestrictedNote, FenceProfileDialog,
  promotionBlock, fenceProfileBlock, fenceText, promotionRunnable, promotionStageText, promotionInProgress,
  oldMasterReachable, fenceProfileMissing, fenceUnusableReason, lastPromotionText, restrictedClearsText,
  fenceHostIDs, unfenceableBlockers, fenceBlockerReason, fenceCellText,
  restrictedUnconfirmed, HostRestrictedBanner,
  bmcText, manualBMCOffered, bmcReenableOffered, bmcProbePrefill,
  FencingDetail, fencingText, fenceCheckLines, watchdogText, fenceMethodName,
} = await import(moduleURL);

const master = { host_id: "host-a", name: "core-a", role: "master", connection: "stale" };
const member = {
  host_id: "host-b", name: "core-b", role: "member", connection: "fresh",
  replica: { generation: 9, verified: true, state: "unverified" },
};
// The situation this slice exists for: the pool master is gone and the member reports itself.
const report = {
  pool_id: "pool-1", pool_name: "lab-pool", master_host_id: "host-a", master_connection: "stale",
  authoritative: false, promotion: null, last_promotion: null,
  members: [master, member],
};
const liveReport = { ...report, master_connection: "fresh", authoritative: true, members: [{ ...master, connection: "fresh" }, member] };

const notFenced = "the pool master host-a has not been fenced for this promotion; Core fences it and observes the power-off when the promotion is confirmed";
const stillMaster = "the pool master host-a is reachable and holds this pool's authority; change the pool master with a planned master change instead of promoting";
const noProfile = "the pool master host-a has no recorded fence profile; record one before promoting";
const ownershipConflict = "pool VM vm-7 is recorded differently by the host that owns it; resolve that before promoting";
const noProfileReason = "no fence profile is recorded for host-a";
const noCredential = "this host holds no fence credential for host-a; it cannot fence it, so it cannot be promoted over it";
const unverifiable = "this host does not report a unique hardware identity, so a management controller cannot be confirmed to be its own";

const verdictOf = (checks, extra = {}) => ({
  pool_id: "pool-1", candidate_host_id: "host-b", old_master_host_id: "host-a",
  authority_generation: 9, can_promote: false, fence_required: true, checks,
  hosts: [
    { host_id: "host-a", reachable: false, error: "dial tcp 198.51.100.11:9443: connect: no route to host" },
    { host_id: "host-b", reachable: true, role: "member", authority_generation: 0, replica_generation: 9, membership_generation: 7 },
  ],
  vms: [], fence: null, blocking: [], observed_at: "2026-09-19T10:04:11Z", ...extra,
});
const ready = verdictOf([
  { name: "replica-integrity", status: "passed" },
  { name: "replica-supremacy", status: "passed" },
  { name: "accepted-ownership", status: "passed" },
  { name: "old-master-exclusion", status: "unknown", reason: notFenced, hosts: ["host-a"] },
], { blocking: [notFenced], ready_to_fence: true, fence_recorded: true, fence_usable: true, fence_unusable_reason: "", fence_host_ids: ["host-a"] });
// The same situation reported without any of the structural fields.
const bareReady = verdictOf(ready.checks, { blocking: [notFenced] });

// react-dom/server escapes apostrophes, so Core's sentences are matched as they are rendered.
const rx = text => new RegExp(text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/'/g, "(&#x27;|')"));
const dialogProps = { report, member, verdictBusy: false, verdictError: "", onPreflight: () => {}, busy: false, submitError: "", onConfirm: () => {}, onClose: () => {} };
const dialog = props => renderToStaticMarkup(createElement(PromotionDialog, { ...dialogProps, ...props }));

// ------------------------------------------------------------ action availability matrix

assert.equal(promotionBlock(report, member), "", "a fresh member holding a verified copy can be promoted while the master is gone");
assert.equal(promotionBlock(report, master), "This host is already the pool master.");
// A pool master that is reporting is not promoted over; that is a planned master change.
assert.match(promotionBlock(liveReport, member), /^The pool master core-a is reporting\. Use “Make pool master” for a planned master change; promoting is for a pool master that is gone\.$/);
assert.equal(promotionBlock(report, { ...member, connection: "stale" }), "The promotion candidate must be fresh and active to be promoted.");
assert.equal(promotionBlock(report, { ...member, replica: null }), "This host does not report a copy of the pool records it has verified, so it cannot be promoted.");
assert.equal(promotionBlock(report, { ...member, replica: { generation: 9, verified: false, state: "unverified" } }), "This host does not report a copy of the pool records it has verified, so it cannot be promoted.");
assert.equal(promotionBlock({ ...report, promotion: { host_id: "host-b", status: "pending", stage: "fence" } }, member), "A promotion is already running on this pool.");

// A fence device lives in the membership record the master owns, so it needs a live master.
assert.equal(fenceProfileBlock(liveReport, master), "");
assert.match(fenceProfileBlock(report, master), /^The pool master is stale, so a fence device cannot be recorded now\. The pool master keeps this record, so it has to be recorded while the master is reporting\.$/);

// The fence line: master-derived, so unknown without a fresh master, never a stale value.
assert.equal(fenceText(member, false), "Fence device: unknown — the pool master keeps this record and is not reporting.");
assert.equal(fenceText(member, true), "Fence device: not configured. A pool master with no fence device cannot be promoted over.");
assert.equal(fenceText({ ...member, fence: { kind: "virsh", endpoint: "lab.example.test", configured: true, credential_version: 0 } }, true),
  "Fence device: virsh · lab.example.test · configured · no credential recorded");
// The master's own counter for the private material behind the profile: a recorded device with
// no credential cannot actually power anything off, and the number itself is never shown.
assert.equal(fenceText({ ...member, fence: { kind: "virsh", endpoint: "lab.example.test", configured: true, credential_version: 3 } }, true),
  "Fence device: virsh · lab.example.test · configured · credential recorded");

// ------------------------------------- the host's own management controller

// Each host configures its own controller, and the row says what its Core found. Every state
// is Core's own; this file only decides which of them still needs an operator.
const provisioned = { ...member, fence: { configured: true, kind: "ipmilan", username: "nexfence", provisioned_by_core: true, credential_version: 1, bmc: { state: "provisioned", address: "192.0.2.15", account: "nexfence" } } };
assert.equal(bmcText(provisioned, true), "Management controller: configured by Nex Core at 192.0.2.15 as nexfence.");
// Master-derived, so nothing is repeated from an old report while the master is not fresh.
assert.equal(bmcText(provisioned, false), "");
assert.equal(bmcText(member, true), "");
// A refusal is Core's sentence, shown as written and never re-worded here.
const noInBand = { ...member, fence: { configured: false, bmc: { state: "no_local_interface", detail: "this host has no local IPMI interface" } } };
assert.equal(bmcText(noInBand, true), "Management controller: this host has no local IPMI interface");
// The identity conflict names the host that holds the identity, so there is something to do.
assert.match(bmcText({ ...member, fence: { configured: false, system_uuid_conflict_host_id: "host-c" } }, true),
  /this host reports the same hardware identity as host-c/);

// Set BMC access… is offered everywhere except a host whose own Core configured its controller.
assert.equal(manualBMCOffered(provisioned), false);
assert.equal(manualBMCOffered(noInBand), true);
assert.equal(manualBMCOffered(member), true, "a host whose Core reports nothing here keeps the manual form");
// Letting Nex configure the controller again is offered only where an operator turned it off,
// and never while the master is not fresh. It is the pool master's own recorded decision, not
// the host's echo of it: a host that is down after its device was cleared has echoed nothing
// and must still offer both ways out. There is no separate remove action: clearing the fence
// device is what removes the account, one operator action with one meaning.
const withdrawn = { ...member, fence: { configured: false, provisioning_disabled: true } };
assert.equal(bmcReenableOffered(withdrawn, true), true, "the master's decision is shown before the host echoes it");
assert.equal(bmcReenableOffered(withdrawn, false), false);
assert.equal(manualBMCOffered(withdrawn), true, "and a withdrawn host is never left with no action at all");
assert.equal(bmcReenableOffered({ ...member, fence: { configured: false, provisioning_disabled: true, bmc: { state: "removed" } } }, true), true);
assert.equal(bmcReenableOffered(provisioned, true), false);
assert.equal(bmcReenableOffered(noInBand, true), false);
{
  const row = renderToStaticMarkup(createElement(PromotionActions, { report: liveReport, member: provisioned, authoritative: true, onFenceProfile: () => {}, onBMCProvisioning: () => {} }));
  assert.doesNotMatch(row, /Set BMC access/, "a host Nex Core configured is not asked for a controller address");
  assert.match(row, /Clear fence device…/, "and its device is cleared from the same one entry point");
  assert.doesNotMatch(row, /Remove Nex fence account/, "there is no second destructive action");
  // What the device is and what Core found on the controller is in the row's Fencing details now,
  // not repeated beside the buttons.
  assert.doesNotMatch(row, /configured by Nex Core at 192\.0\.2\.15/);
  assert.match(renderToStaticMarkup(createElement(FencingDetail, { member: provisioned, authoritative: true })),
    /configured by Nex Core at 192\.0\.2\.15/);
}
{
  const row = renderToStaticMarkup(createElement(PromotionActions, { report: liveReport, member: noInBand, authoritative: true, onFenceProfile: () => {}, onBMCProvisioning: () => {} }));
  assert.match(row, /Set BMC access…/);
}
{
  const removed = { ...member, fence: { configured: false, provisioning_disabled: true, bmc: { state: "removed", detail: "an operator withdrew this host's fence device" } } };
  const row = renderToStaticMarkup(createElement(PromotionActions, { report: liveReport, member: removed, authoritative: true, onBMCProvisioning: () => {} }));
  assert.match(row, /Let Nex configure this controller again/);
  const busy = renderToStaticMarkup(createElement(PromotionActions, { report: liveReport, member: removed, authoritative: true, onBMCProvisioning: () => {}, bmcBusy: true }));
  assert.match(busy, /disabled=""/, "a row action shows it is in flight");
}
// Clearing a device Nex Core provisioned says that the account goes too, and the clear block
// is offered only where a profile actually exists.
{
  const dialog = renderToStaticMarkup(createElement(FenceProfileDialog, { report: liveReport, member: provisioned, busy: false, submitError: "", onConfirm: () => {}, onClear: () => {}, onClose: () => {} }));
  assert.match(dialog, /Clear fence device…/);
  const none = renderToStaticMarkup(createElement(FenceProfileDialog, { report: liveReport, member: noInBand, busy: false, submitError: "", onConfirm: () => {}, onClear: () => {}, onClose: () => {} }));
  assert.doesNotMatch(none, /Clear fence device…/, "a host with no fence device is not offered one to clear");
}
// The re-enable request goes to the pool master, which owns the record, and its refusal is
// rendered on the panel rather than in a dialog the row never opens.
assert.match(membersSrc, /members\/\$\{encodeURIComponent\(member\.host_id\)\}\/bmc-provisioning/);
assert.match(membersSrc, /\{actionError && <p className="alert" role="alert">\{actionError\}<\/p>\}/);

const actions = renderToStaticMarkup(createElement(PromotionActions, { report, member, authoritative: false, onPromote: () => {}, onFenceProfile: () => {} }));
assert.match(actions, /<button type="button" class="danger">Promote to pool master…<\/button>/);
assert.match(actions, /<button type="button" disabled="">Set BMC access…<\/button>/);
assert.match(renderToStaticMarkup(createElement(FencingDetail, { member, authoritative: false })), /Fence device: unknown/);
// Without the handlers (an unavailable view) neither button is offered at all.
assert.doesNotMatch(renderToStaticMarkup(createElement(PromotionActions, { report, member, authoritative: false })), /<button/);

// ------------------------------------------------------------ the verdict, rendered

const full = verdictOf([
  { name: "replica-integrity", status: "passed" },
  { name: "replica-supremacy", status: "blocked", reason: "pool host host-c holds a newer pool authority than this host; promote it instead", hosts: ["host-c"] },
  { name: "accepted-ownership", status: "blocked", reason: ownershipConflict, vms: ["vm-7"] },
  { name: "old-master-exclusion", status: "unknown", reason: notFenced, hosts: ["host-a"] },
], {
  vms: [
    { vm_id: "vm-1", host_id: "host-b", state: "confirmed" },
    { vm_id: "vm-4", host_id: "host-a", state: "fenced", reason: "its owner is unreachable and will be fenced" },
    { vm_id: "vm-7", host_id: "host-c", state: "blocked", reason: "its owner reports definition revision 4, not 3" },
  ],
  fence: { host_id: "host-a", status: "failed", target_id: "core-01", exclusion_verified: false, observed_at: "2026-09-19T10:06:00Z", error: "virsh target identity was not independently confirmed" },
  blocking: ["pool host host-c holds a newer pool authority than this host; promote it instead", ownershipConflict, notFenced],
});
const verdictMarkup = renderToStaticMarkup(createElement(PromotionVerdictView, { report, verdict: full }));
// Each check is a word, not a colour, with Core's reason verbatim and the hosts and VMs named.
assert.match(verdictMarkup, /badge ok">Passed<\/span> This host&#x27;s copy of the pool records is whole and signed/);
assert.match(verdictMarkup, /badge bad">Blocked<\/span> No reachable host holds a newer copy/);
assert.match(verdictMarkup, /badge bad">Blocked<\/span> Every pool virtual machine&#x27;s owner agrees with the copy/);
assert.match(verdictMarkup, /badge neutral">Unknown<\/span> The old pool master is shut out/);
assert.match(verdictMarkup, /pool host host-c holds a newer pool authority than this host; promote it instead/);
assert.match(verdictMarkup, /<small>Hosts: host-c<\/small>/);
assert.match(verdictMarkup, /<small>Virtual machines: vm-7<\/small>/);
assert.match(verdictMarkup, /<strong>unknown<\/strong> stops a promotion exactly as <strong>blocked<\/strong> does/);
// The per-host table, with an unreachable host's own error and no invented generations.
assert.match(verdictMarkup, /<td>core-a<\/td><td>no answer<small class="task-error">dial tcp 198\.51\.100\.11:9443: connect: no route to host<\/small><\/td>/);
assert.match(verdictMarkup, /<td>core-b<\/td><td>answered<\/td><td>member<\/td><td>0<\/td><td>9<\/td><td>7<\/td>/);
assert.match(verdictMarkup, /<td>unknown<\/td><td>unknown<\/td><td>unknown<\/td>/, "a host that did not answer reports no generations");
// The per-VM ownership table: every state in words, and what a blocked VM means.
assert.match(verdictMarkup, /badge ok">confirmed by its owner</);
assert.match(verdictMarkup, /badge warn">its owner is fenced</);
assert.match(verdictMarkup, /badge bad">blocked</);
assert.match(verdictMarkup, /its owner reports definition revision 4, not 3/);
assert.match(verdictMarkup, /stays defined and stays visible in the pool after a promotion\. It cannot be started until the disagreement about where it lives is resolved/);
assert.match(verdictMarkup, /Being visible is not the same as its disks being available/);
// The fence status, and every blocking reason as the list of what stops it.
assert.match(verdictMarkup, /Fence of core-a: <strong>failed<\/strong> · power-off not observed · target core-01 · 2026-09-19T10:06:00Z/);
assert.match(verdictMarkup, /task-error">virsh target identity was not independently confirmed/);
assert.match(verdictMarkup, /What stops this promotion/);
assert.equal((verdictMarkup.match(/pool-promotion-blocking/g) || []).length, 1);
assert.match(verdictMarkup, /holds copy 9 of the pool records/);
// No fence yet is said as that, not as a pass.
assert.match(renderToStaticMarkup(createElement(PromotionVerdictView, { report, verdict: ready })), /No fence of the old pool master has been performed for this promotion\. Nex Core fences it and observes the power-off itself/);
assert.doesNotMatch(verdictMarkup, /spinner|progress-bar/i);

// ------------------------------------------------------------ when step 2 is offered at all

// The candidate answers this itself. `ready_to_fence` is the normal ready state — can_promote
// stays false there, because Core only satisfies old-master-exclusion by fencing — and nothing
// here infers it from the check statuses.
assert.equal(promotionRunnable(ready), true);
assert.equal(promotionRunnable({ ...ready, can_promote: true, ready_to_fence: false }), true, "the whole proof holding is enough on its own");
assert.equal(promotionRunnable({ ...ready, ready_to_fence: false }), false, "Core's own no is final, whatever the checks look like");
assert.equal(promotionRunnable(full), false, "another check blocked is never passed off as ready");
assert.equal(promotionRunnable(null), false);
assert.equal(promotionRunnable(verdictOf([])), false, "a verdict with no checks claims nothing");
// Core's no outranks a set of checks that would otherwise read as ready: no inference is left.
assert.equal(promotionRunnable({ ...bareReady, ready_to_fence: false }), false);
// A report carrying neither field is never runnable; nothing is inferred from checks.
assert.equal(promotionRunnable(bareReady), false, "without Core's structural fields nothing is inferred");
assert.equal(promotionRunnable(verdictOf([{ name: "replica-integrity", status: "unknown" }])), false);
const reachableVerdict = verdictOf([
  { name: "replica-integrity", status: "passed" }, { name: "replica-supremacy", status: "passed" },
  { name: "accepted-ownership", status: "passed" },
  { name: "old-master-exclusion", status: "blocked", reason: stillMaster, hosts: ["host-a"] },
], { hosts: [{ host_id: "host-a", reachable: true, role: "master", authority_generation: 9, membership_generation: 7 }, { host_id: "host-b", reachable: true, role: "member", replica_generation: 9 }], blocking: [stillMaster], ready_to_fence: false, fence_recorded: true, fence_usable: true });
const noProfileVerdict = verdictOf([
  { name: "replica-integrity", status: "passed" }, { name: "replica-supremacy", status: "passed" },
  { name: "accepted-ownership", status: "passed" },
  { name: "old-master-exclusion", status: "blocked", reason: noProfile, hosts: ["host-a"] },
], { blocking: [noProfile], ready_to_fence: false, fence_recorded: false, fence_usable: false, fence_unusable_reason: noProfileReason });
assert.equal(oldMasterReachable(reachableVerdict), true);
assert.equal(oldMasterReachable(ready), false);
assert.equal(oldMasterReachable(noProfileVerdict), false);
// The fence states are Core's two booleans plus its own sentence — never its prose matched.
assert.equal(fenceProfileMissing(noProfileVerdict), true);
assert.equal(fenceProfileMissing(ready), false);
assert.equal(fenceProfileMissing(reachableVerdict), false);
assert.equal(fenceProfileMissing(bareReady), false, "a report without the flag claims nothing");
assert.equal(fenceProfileMissing({ ...ready, fence_recorded: false }), true);
// A profile that is recorded but cannot run: the candidate holds no credential for it, or the
// kind cannot be verified at all. One branch, told apart by Core's own reason.
const noCredentialVerdict = { ...ready, ready_to_fence: false, fence_usable: false, fence_unusable_reason: noCredential };
const unverifiableVerdict = { ...ready, ready_to_fence: false, fence_usable: false, fence_unusable_reason: unverifiable };
assert.equal(fenceUnusableReason(noCredentialVerdict), noCredential);
assert.equal(fenceUnusableReason(unverifiableVerdict), unverifiable);
assert.equal(fenceUnusableReason(ready), "", "a usable profile has no unusable reason");
assert.equal(fenceUnusableReason(noProfileVerdict), "", "no profile is its own state, not an unusable one");
assert.equal(fenceUnusableReason(bareReady), "", "a report without the field claims nothing here either");
assert.equal(fenceUnusableReason({ ...ready, fence_usable: false }), "Nex Core reported no reason.");

// ------------------------------------------------------------ the two-step dialog

// No verdict yet: step 1 says what is being asked and step 2 offers nothing.
const asking = dialog({ verdict: null, verdictBusy: true });
assert.match(asking, /aria-labelledby="promotion-title"/);
assert.match(asking, /Promote core-b to pool master of lab-pool/);
assert.match(asking, /Asking core-b to check the pool\. It answers on its next report/);
assert.match(asking, /Nothing is offered until Nex Core has reported its checks\./);
assert.doesNotMatch(asking, /promotion-fence-confirm/);
// A preflight refusal, including the 504 and the 499, is shown in Core's own words.
assert.match(dialog({ verdict: null, verdictError: "the promotion candidate did not answer the promotion preflight before the deadline; retry" }),
  /role="alert"[^>]*>the promotion candidate did not answer the promotion preflight before the deadline; retry/);
assert.match(dialog({ verdict: null, verdictError: "the promotion candidate must be fresh and active to answer a preflight" }),
  /role="alert"[^>]*>the promotion candidate must be fresh and active to answer a preflight/);
assert.match(dialog({ verdict: null, verdictError: "the client disconnected before the promotion candidate answered; retry" }), /the client disconnected before the promotion candidate answered; retry/);
// The preflight stores nothing, so it can be re-run from the dialog.
assert.match(asking, /<button type="button" disabled="">Checking…<\/button>/);
assert.match(dialog({ verdict: ready }), /<button type="button">Check again<\/button>/);

// A blocked verdict: no step 2 at all, and no promise that submitting would change anything.
const blockedDialog = dialog({ verdict: full });
assert.doesNotMatch(blockedDialog, /promotion-fence-confirm/);
assert.doesNotMatch(blockedDialog, /Type the pool name/);
assert.match(blockedDialog, /This promotion is stopped by the checks above\./);
assert.match(blockedDialog, /nothing here can pass a check that did not pass/);

// The old master answered: no step 2, Core's own sentence, and the planned operation named.
const reachableDialog = dialog({ verdict: reachableVerdict });
assert.doesNotMatch(reachableDialog, /promotion-fence-confirm/);
assert.match(reachableDialog, rx(stillMaster));
assert.match(reachableDialog, /use <strong>Make pool master<\/strong> on core-b&#x27;s row instead/);

// No fence device recorded: no step 2, the D2 limit stated outright, and why it is too late.
// The state comes from fence_recorded; Core's reason is still rendered, never matched on.
const noProfileDialog = dialog({ verdict: noProfileVerdict });
assert.doesNotMatch(noProfileDialog, /promotion-fence-confirm/);
assert.match(noProfileDialog, /No fence device is recorded for core-a, so core-b cannot shut it out and cannot be promoted over it\./);
assert.match(noProfileDialog, /has no recorded fence profile; record one before promoting/);
assert.match(noProfileDialog, /only be recorded while the pool master is reporting — that is, before it is lost/);
// A verdict with fence_recorded false and no check reason at all still reaches this state.
assert.match(dialog({ verdict: { ...noProfileVerdict, checks: [] } }), /No fence device is recorded for core-a/);

// Recorded but unusable: one branch for both reasons, Core's sentence verbatim, D2 kept.
const noCredentialDialog = dialog({ verdict: noCredentialVerdict });
assert.doesNotMatch(noCredentialDialog, /promotion-fence-confirm/);
assert.match(noCredentialDialog, /A fence device is recorded for core-a, but core-b cannot use it, so it cannot be promoted over it\./);
assert.match(noCredentialDialog, rx(noCredential));
const unverifiableDialog = dialog({ verdict: unverifiableVerdict });
assert.doesNotMatch(unverifiableDialog, /promotion-fence-confirm/);
assert.match(unverifiableDialog, rx(unverifiable));

// Ready: step 2 states plainly what Nex Core will do, and is gated on both confirmations.
const readyDialog = dialog({ verdict: ready });
assert.match(readyDialog, /<strong>Nex Core on core-b powers core-a off<\/strong> through its recorded fence device, and verifies that it is off, before it takes the pool authority/);
assert.doesNotMatch(readyDialog, /Virtual machines running on the hosts that survive/);
assert.doesNotMatch(readyDialog, /When core-a comes back it rejoins/);
assert.match(readyDialog, /<label for="promotion-confirm">Type the pool name lab-pool to confirm<\/label>/);
assert.match(readyDialog, /<input id="promotion-fence-confirm" type="checkbox"\/?>/);
assert.match(readyDialog, /I understand this powers off: core-a/);
// Neither confirmation is pre-filled, so the button cannot submit by itself.
assert.match(readyDialog, /<button class="primary danger" disabled="">Promote to pool master<\/button>/);
assert.match(dialog({ verdict: ready, busy: true }), /disabled=""[^>]*>Submitting…</);
assert.match(dialog({ verdict: ready, submitError: "promoting this host powers the old pool master off; confirm that before the promotion can run" }),
  /role="alert"[^>]*>promoting this host powers the old pool master off; confirm that before the promotion can run/);
assert.match(dialog({ verdict: ready, readError: "pool report unavailable" }), /current pool report could not be read: pool report unavailable/);
// Both gates are genuinely required, not decoration.
assert.match(src, /const ready = !readError && runnable && fenceIDs\.length > 0 && confirmMatches\(typed, report\.pool_name\) && acknowledged;/);
assert.match(src, /if \(!ready\) return;/);
assert.match(src, /import \{ confirmMatches \} from "\.\/managed"/);
// The dialog cannot be dismissed while the request is in flight.
assert.match(src, /onClose=\{\(\) => \{ if \(busy\) dialog\.current\?\.showModal\(\); else onClose\(\); \}\}/);

// ------------------------------------------- every host this promotion powers off

// A partitioned member that could be promoted too: Core reports it as blocking, names it in
// fence_host_ids, and the promotion powers both hosts off. The operator is told all of it.
const twoHosts = verdictOf([
  { name: "replica-integrity", status: "passed" }, { name: "replica-supremacy", status: "unknown", reason: "pool host host-c is unreachable and holds this pool's authority replica, so it could be promoted too; it must be fenced before this host can be promoted", hosts: ["host-c"] },
  { name: "accepted-ownership", status: "passed" },
  { name: "old-master-exclusion", status: "unknown", reason: notFenced, hosts: ["host-a", "host-c"] },
], {
  hosts: [
    { host_id: "host-a", reachable: false, blocks: false, fence_recorded: true, fence_usable: true, error: "no route to host" },
    { host_id: "host-b", reachable: true, role: "member", blocks: false, fence_recorded: false, fence_usable: false, replica_generation: 9 },
    { host_id: "host-c", reachable: false, blocks: true, fence_recorded: true, fence_usable: true, error: "no route to host" },
  ],
  ready_to_fence: true, fence_recorded: true, fence_usable: true, fence_host_ids: ["host-a", "host-c"],
});
const threeReport = { ...report, members: [master, member, { host_id: "host-c", name: "core-c", role: "member", connection: "stale" }] };
assert.deepEqual(fenceHostIDs(twoHosts), ["host-a", "host-c"]);
assert.deepEqual(fenceHostIDs(ready), ["host-a"]);
assert.deepEqual(fenceHostIDs(bareReady), [], "a report without the field names none, so nothing is claimed");
assert.deepEqual(unfenceableBlockers(twoHosts), [], "a blocking host Core will fence is not a blocker for the operator");

// The per-host table says, for every host, whether it stops the promotion and what happens to it.
const twoMarkup = renderToStaticMarkup(createElement(PromotionVerdictView, { report: threeReport, verdict: twoHosts }));
assert.match(twoMarkup, /<th>Stops this promotion<\/th><th>Fence<\/th>/);
assert.match(twoMarkup, /<td>core-c<\/td>.*?<span class="badge bad">stops it<\/span><\/td><td>powered off by this promotion<\/td>/);
assert.match(twoMarkup, /<td>core-a<\/td>.*?<span class="badge ok">no<\/span><\/td><td>powered off by this promotion<\/td>/);
assert.match(twoMarkup, /<td>core-b<\/td>.*?<span class="badge ok">no<\/span><\/td><td>not powered off<\/td>/);
assert.match(twoMarkup, /This promotion powers off core-a, core-c, and Nex Core verifies each one is off/);

// Step 2 names every host by name, in the acknowledgement too, and says why an unreachable
// member is powered off as well.
const twoDialog = dialog({ report: threeReport, verdict: twoHosts });
assert.match(twoDialog, /<strong>Nex Core on core-b powers core-a, core-c off<\/strong> through their recorded fence devices, and verifies that they are off/);
assert.doesNotMatch(twoDialog, /pool-promotion-fenced/);
assert.match(twoDialog, /I understand this powers off: core-a, core-c/);
// One host is still said in the singular, and the old sentence is unchanged for it.
assert.match(readyDialog, /I understand this powers off: core-a/);
assert.doesNotMatch(readyDialog, /core-c/);

// A blocking host that cannot be fenced: no step 2 at all, Core's reason verbatim, and the two
// things that actually resolve it. No override is offered, because there is none.
const unfenceable = verdictOf([
  { name: "replica-integrity", status: "passed" }, { name: "replica-supremacy", status: "unknown", reason: "pool host host-c is unreachable and holds this pool's authority replica, so it could be promoted too; it must be fenced before this host can be promoted", hosts: ["host-c"] },
  { name: "accepted-ownership", status: "passed" },
  { name: "old-master-exclusion", status: "blocked", reason: "this host cannot fence host-c: no fence profile is recorded for host-c, so it cannot be promoted while those hosts are unreachable", hosts: ["host-a", "host-c"] },
], {
  hosts: [
    { host_id: "host-a", reachable: false, blocks: false, fence_recorded: true, fence_usable: true },
    { host_id: "host-c", reachable: false, blocks: true, fence_recorded: false, fence_usable: false, fence_unusable_reason: "no fence profile is recorded for host-c" },
  ],
  ready_to_fence: false, fence_recorded: true, fence_usable: true, fence_host_ids: ["host-a"],
});
assert.deepEqual(unfenceableBlockers(unfenceable).map(host => host.host_id), ["host-c"]);
assert.equal(fenceBlockerReason(unfenceable.hosts[1]), "no fence profile is recorded for host-c");
assert.equal(fenceBlockerReason({ host_id: "host-d", reachable: false, blocks: true, fence_recorded: false }), "No fence device is recorded for this host.");
assert.equal(fenceBlockerReason({ host_id: "host-d", reachable: false, blocks: true, fence_recorded: true, fence_usable: false }), "Nex Core reported no reason.");
assert.equal(fenceCellText(unfenceable, unfenceable.hosts[1]), "cannot be fenced — no fence profile is recorded for host-c");
const unfenceableDialog = dialog({ report: threeReport, verdict: unfenceable });
assert.doesNotMatch(unfenceableDialog, /promotion-fence-confirm/, "no step 2 while a blocking host cannot be powered off");
assert.doesNotMatch(unfenceableDialog, /Type the pool name/);
assert.match(unfenceableDialog, /core-c stops this promotion and cannot be powered off, so core-b cannot be promoted\./);
assert.match(unfenceableDialog, /<li class="task-error">core-c: no fence profile is recorded for host-c<\/li>/);
assert.match(unfenceableDialog, /That host must be brought back so core-b can see it again, or be given a fence device on the pool master while it is still reporting\./);
assert.match(unfenceableDialog, /core-b cannot prove no other host could take the pool authority as well/);
// Even a verdict Core still calls ready_to_fence does not reach step 2 while one exists.
assert.doesNotMatch(dialog({ report: threeReport, verdict: { ...unfenceable, ready_to_fence: true } }), /promotion-fence-confirm/);

// Core's mismatch refusal, verbatim, with the one thing that resolves it — and it is shown
// wherever step 2 has gone, not only inside the form.
const mismatch = "this promotion must power off host-a host-c, which is not what was confirmed; confirm the hosts this pool reports and try again";
for (const markup of [dialog({ verdict: ready, submitError: mismatch }), dialog({ report: threeReport, verdict: unfenceable, submitError: mismatch })]) {
  assert.match(markup, rx(mismatch));
  assert.match(markup, /Re-run the check above, read the hosts it names now, and confirm those\./);
}

// The list the operator confirmed is what is sent, and the acknowledgement is dropped whenever
// a re-run changes that list: what was agreed to is a set of named hosts, not a checkbox.
assert.match(src, /const fenceIDs = fenceHostIDs\(verdict\);/);
assert.match(src, /const fenceKey = fenceIDs\.join\("\\n"\);/);
assert.match(src, /useEffect\(\(\) => \{ setAcknowledged\(false\); \}, \[fenceKey\]\);/);
assert.match(src, /onConfirm\(typed\.trim\(\), fenceIDs\);/);

// A ready verdict that names no host at all is not offered either: Core refuses an empty list.
assert.match(dialog({ verdict: { ...ready, fence_host_ids: [] } }), /Nex Core reports no host that this promotion would power off/);
assert.doesNotMatch(dialog({ verdict: { ...ready, fence_host_ids: [] } }), /promotion-fence-confirm/);

// The POST body is exactly the contract's, with the explicit power-off acknowledgement, Core's
// own host list sent back unchanged, and a held request id so a retry after a lost reply
// replays the same promotion — while a different list is a different request.
assert.match(membersSrc, /`pools\/\$\{encodeURIComponent\(poolID\)\}\/master\/promote`, "POST", \{ request_id, host_id: member\.host_id, confirmation, fence_confirmed: true, fence_host_ids: fenceHostIDs \}\)/);
assert.match(membersSrc, /heldRequestID\(heldPromotion\.current, `\$\{poolID\}\\npromote\\n\$\{member\.host_id\}\\n\$\{confirmation\}\\n\$\{fenceHostIDs\.join\(","\)\}`\)/);
assert.match(membersSrc, /onConfirm=\{\(confirmation, fenceHostIDs\) => void promote\(promotionTarget, confirmation, fenceHostIDs\)\}/);
assert.match(membersSrc, /`pools\/\$\{encodeURIComponent\(poolID\)\}\/master\/promote\/preflight`, "POST", \{ host_id: member\.host_id \}\)/);
assert.doesNotMatch(membersSrc.slice(membersSrc.indexOf("async function promote("), membersSrc.indexOf("} finally { setPromotionBusy(false); }")), /expected_version/);

// ------------------------------------------------------------ the progress strip

assert.equal(renderToStaticMarkup(createElement(PromotionStrip, { report })), "", "nothing reported, no panel");
assert.equal(promotionStageText(null), "unknown");
assert.equal(promotionStageText({ host_id: "host-b", stage: "fence" }), "powering the old pool master off and verifying it");
assert.equal(promotionStageText({ host_id: "host-b", stage: "activate" }), "taking the pool authority");
assert.equal(promotionStageText({ host_id: "host-b", stage: "invented" }), "invented", "an unknown stage is printed as Core sent it");
assert.equal(promotionInProgress({ ...report, promotion: { host_id: "host-b", status: "pending" } }), true);
assert.equal(promotionInProgress(report), false);
const running = renderToStaticMarkup(createElement(PromotionStrip, { report: { ...report, promotion: { host_id: "host-b", operation_id: "op-1", stage: "fence", status: "pending", observed_at: "2026-09-19T10:05:02Z" } } }));
assert.match(renderToStaticMarkup(createElement(PromotionStrip, { report: { ...report, promotion: { host_id: "host-b", stage: "fence", status: "pending", automatic: true } } })), /status <strong>pending<\/strong> · automatic/);
assert.doesNotMatch(running, /automatic/);
assert.match(running, /Candidate <strong>core-b<\/strong>/);
assert.match(running, /stage <strong>powering the old pool master off and verifying it<\/strong>/);
assert.match(running, /status <strong>pending<\/strong>/);
assert.match(running, /Reported by core-b at 2026-09-19T10:05:02Z/);
assert.doesNotMatch(running, /spinner|progress-bar/i);
// A promotion fences, so Core reports cancellable false at every stage. The sentence is said;
// no cancel is ever offered, and the stage name decides nothing.
const forwardOnly = renderToStaticMarkup(createElement(PromotionStrip, { report: { ...report, promotion: { host_id: "host-b", stage: "fence", status: "pending", cancellable: false } } }));
assert.match(forwardOnly, /Nex Core reports that this promotion cannot be cancelled\. It can only go forward\./);
assert.doesNotMatch(forwardOnly, /<button/);
assert.doesNotMatch(running, /cannot be cancelled/, "a Core that reports no flag claims nothing");
assert.doesNotMatch(renderToStaticMarkup(createElement(PromotionStrip, { report: { ...report, promotion: { host_id: "host-b", stage: "done", status: "succeeded", cancellable: false } } })), /cannot be cancelled/);
const attention = renderToStaticMarkup(createElement(PromotionStrip, { report: { ...report, promotion: { host_id: "host-b", stage: "activate", status: "needs_attention", error: "the old pool master was not excluded: virsh target identity was not independently confirmed" } } }));
assert.match(attention, /status <strong>needs attention<\/strong>/);
assert.match(attention, /task-error">the old pool master was not excluded: virsh target identity was not independently confirmed/);
// The embedded verdict is rendered where the promotion is, not summarized away.
assert.match(renderToStaticMarkup(createElement(PromotionStrip, { report: { ...report, promotion: { host_id: "host-b", stage: "preflight", status: "failed", verdict: full } } })), /What stops this promotion/);

// The asynchronous refusal, verbatim. Nothing about a promotion is retried unchanged.
assert.equal(lastPromotionText(null), "");
assert.equal(lastPromotionText({ operation_id: "op-1", action: "promote", status: "succeeded" }), "");
assert.equal(lastPromotionText({ operation_id: "op-1", action: "promote", status: "failed", error: stillMaster }), `The promotion failed: ${stillMaster}`);
assert.equal(lastPromotionText({ operation_id: "op-1", action: "promote", status: "failed", conflict: true, error: ownershipConflict }),
  `${ownershipConflict} Nothing was promoted. Resolve what Nex Core named and check the pool again before submitting another promotion.`);
assert.match(lastPromotionText({ operation_id: "op-1", action: "promote", status: "needs_attention", error: "" }), /^Needs attention: Nex Core reported no message\. The promotion is durable and still running on Nex Core/);
assert.match(lastPromotionText({ operation_id: "op-1", action: "promote", status: "queued" }), /^The promotion was submitted\./);
const refused = renderToStaticMarkup(createElement(PromotionStrip, { report: { ...report, last_promotion: { operation_id: "op-1", action: "promote", status: "failed", error: noProfile } } }));
assert.match(refused, /task-error"[^>]*>The promotion failed: the pool master host-a has no recorded fence profile/);

// ------------------------------------------------------------ restricted hosts

const superseded = { reason: "this host's pool authority was superseded by another master; it starts no shared-storage guest and writes no pool authority until it has rejoined and reconciled (VM ownership conflicts: vm-7)", kind: "superseded", since: "2026-09-19T10:11:00Z", conflicts: ["vm-7"] };
const unconfirmed = { reason: "this pool master has not confirmed its authority since it restarted; until every other member confirms it, it starts no shared-storage guest and writes no pool authority", kind: "unconfirmed", since: "2026-09-19T10:11:00Z" };
// Core classifies the restriction; the kind is what chooses the text, never its sentence.
assert.equal(restrictedUnconfirmed(unconfirmed), true);
assert.equal(restrictedUnconfirmed(superseded), false);
assert.equal(restrictedUnconfirmed(null), false);
// The kinds swap cleanly even when the sentences would say the opposite, which is exactly what
// sentence matching used to get wrong.
const swapped = { ...report, members: [{ ...master, connection: "fresh", restricted: { ...unconfirmed, reason: superseded.reason } }, member] };
assert.match(restrictedClearsText(swapped, swapped.members[0]), /^It clears by itself once every other ready member/);
assert.match(restrictedClearsText({ ...report, members: [{ ...master, restricted: { ...superseded, reason: unconfirmed.reason } }, member] }, { ...master, restricted: { ...superseded, reason: unconfirmed.reason } }),
  /^It clears by itself once this host has rejoined the current pool master/);
// A Core old enough to report no kind is not guessed at: the reason still shows, the claim does not.
const kindless = { ...master, connection: "fresh", restricted: { reason: superseded.reason, since: "2026-09-19T10:11:00Z" } };
assert.equal(restrictedClearsText({ ...report, members: [kindless, member] }, kindless), "");
const kindlessMarkup = renderToStaticMarkup(createElement(RestrictedNote, { report, member: kindless }));
assert.match(kindlessMarkup, /badge bad">Restricted<\/span>/);
assert.match(kindlessMarkup, /superseded by another master/);
assert.doesNotMatch(kindlessMarkup, /It clears/);

const supersededMarkup = renderToStaticMarkup(createElement(RestrictedNote, { report, member: { ...member, restricted: superseded } }));
assert.match(supersededMarkup, /badge bad">Restricted<\/span>/);
assert.match(supersededMarkup, /task-error">this host&#x27;s pool authority was superseded by another master; it starts no shared-storage guest and writes no pool authority until it has rejoined and reconciled \(VM ownership conflicts: vm-7\)/);
assert.match(supersededMarkup, /<small>Since 2026-09-19T10:11:00Z<\/small>/);
assert.match(supersededMarkup, /Virtual machines this host still defines that the pool records elsewhere: vm-7/);
assert.match(supersededMarkup, /starts no virtual machine and makes no pool change\. Stopping a virtual machine, its console, local recovery and its own reporting all still work\./);
assert.match(supersededMarkup, /clears by itself once this host has rejoined the current pool master, taken its copy of the pool records and reconciled with it/);
assert.match(supersededMarkup, /never started twice and it is never deleted here/);
assert.equal(renderToStaticMarkup(createElement(RestrictedNote, { report, member })), "", "a host reporting no restriction shows none");

// A restarted master with every other member reporting: it clears itself.
const allFresh = { ...report, members: [{ ...master, connection: "fresh", restricted: unconfirmed }, member] };
assert.equal(restrictedClearsText(allFresh, allFresh.members[0]),
  "It clears by itself once every other ready member of this pool answers and names this host as its master.");
// OWNER DECISION OD-5: a restarted master plus a member that stays unreachable. The host must
// be brought back or restored, and no action of any kind is offered for it.
const od5 = { ...report, members: [{ ...master, connection: "fresh", restricted: unconfirmed }, member, { host_id: "host-c", name: "core-c", role: "member", connection: "stale" }] };
const od5Markup = renderToStaticMarkup(createElement(RestrictedNote, { report: od5, member: od5.members[0] }));
assert.match(od5Markup, /core-c is not reporting, so it cannot answer\./);
assert.match(od5Markup, /Bring that host back, or restore its Nex Core state from a backup\./);
assert.match(od5Markup, /Nothing here can clear the restriction instead, and waiting does not clear it\./);
assert.doesNotMatch(od5Markup, /<button/, "OD-5: there is no action, so none is offered");
const od5Two = { ...od5, members: [od5.members[0], { ...member, connection: "stale" }, od5.members[2]] };
assert.match(restrictedClearsText(od5Two, od5Two.members[0]), /core-b, core-c are not reporting, so they cannot answer\./);

// ------------------------------------------------------------ the fence device dialog

const fenceProps = { report: liveReport, member: { ...master, connection: "fresh" }, busy: false, submitError: "", onConfirm: () => {}, onClear: () => {}, onClose: () => {} };
const fenceDialog = renderToStaticMarkup(createElement(FenceProfileDialog, fenceProps));
assert.match(fenceDialog, /aria-labelledby="fence-title"/);
assert.match(fenceDialog, /Fence device for core-a/);
// The normal path is the management controller: address, kind, account, password. The SSH-key
// virsh device is not on it at all.
assert.match(fenceDialog, /<legend>Management controller of core-a<\/legend>/);
assert.match(fenceDialog, /id="fence-kind-redfish"[^>]*type="radio"[^>]*checked=""/);
assert.match(fenceDialog, /id="fence-kind-ipmi"[^>]*type="radio"/);
assert.match(fenceDialog, /<label for="fence-address">Address<\/label>/);
assert.match(fenceDialog, /<label for="fence-system">Redfish system identifier<\/label>/);
assert.match(fenceDialog, /<label for="fence-account">Account<\/label>/);
assert.match(fenceDialog, /id="fence-password"[^>]*type="password"[^>]*class="secret"/);
assert.doesNotMatch(fenceDialog, /Host running libvirt|SSH private key/);
// What Nex Core actually does when this is submitted, said where the operator enters it:
// pin the certificate, prove the machine, and hold every later connection to that certificate.
assert.match(fenceDialog, /records the exact certificate it presents and asks it which machine it manages/);
assert.match(fenceDialog, /a host that does not report a unique hardware identity cannot be given a controller here/);
assert.match(fenceDialog, /every connection must present that same certificate/);
// The credential is entered once, never stored by the Controller and never shown back.
assert.match(fenceDialog, /never stored by this Controller, never written to the audit trail and never shown back here/);
// The lab device stays reachable behind one link, and is not the normal path.
assert.match(fenceDialog, /aria-expanded="false"[^>]*>This host runs as a virtual machine in a lab<\/button>/);
// The state holding the credential is emptied as soon as it is handed over.
assert.match(src, /setPassword\(""\);\n\s*setKey\(""\);\n\s*setKnownHosts\(""\);/);
// Nothing in the dialog ever renders a credential the server sent: only the inputs hold one.
assert.doesNotMatch(src, /credential_ref|ssh_host_key_ref/);
// Core's record-time refusal arrives verbatim.
assert.match(renderToStaticMarkup(createElement(FenceProfileDialog, { ...fenceProps, submitError: "this fence profile cannot be verified by this host: this host does not report a unique hardware identity, so a management controller cannot be confirmed to be its own" })),
  /role="alert"[^>]*>this fence profile cannot be verified by this host: this host does not report a unique hardware identity/);
// Clearing is offered only when a profile exists, and only behind its own confirmation.
assert.doesNotMatch(fenceDialog, /Clear fence device…/);
const configured = renderToStaticMarkup(createElement(FenceProfileDialog, { ...fenceProps, member: { ...fenceProps.member, fence: { kind: "redfish", endpoint: "bmc.example.test", username: "nex-fence", tls_fingerprint: "ab".repeat(32), configured: true, credential_version: 2 } } }));
assert.match(configured, /Fence device: redfish · bmc\.example\.test · nex-fence · configured · credential recorded · certificate abab/);
assert.match(configured, /<button type="button" class="danger">Clear fence device…<\/button>/);
const fenceReadFailed = renderToStaticMarkup(createElement(FenceProfileDialog, { ...fenceProps, readError: "pool report unavailable", member: { ...fenceProps.member, fence: { kind: "redfish", endpoint: "bmc.example.test", username: "nex-fence", tls_fingerprint: "ab".repeat(32), configured: true, credential_version: 2 } } }));
assert.match(fenceReadFailed, /current pool report could not be read: pool report unavailable/);
assert.match(src, /disabled=\{busy \|\| Boolean\(readError\)\} onClick=\{onClear\}/);
assert.match(src, /if \(!complete \|\| busy \|\| readError\) return;/);

// The two bodies are exactly the contract's: the profile as sent, the credential in its own
// field, and `fence: null` to clear. The credential is never part of the request identity.
assert.match(membersSrc, /\/fence-profile`, "POST", credential \? \{ request_id, fence, credential \} : \{ request_id, fence \}\)/);
assert.match(membersSrc, /heldRequestID\(heldFence\.current, `\$\{poolID\}\\nfence-profile\\n\$\{member\.host_id\}\\n\$\{JSON\.stringify\(fence\)\}`\)/);
assert.match(membersSrc, /onClear=\{\(\) => void setFenceProfile\(fenceTarget, null\)\}/);
assert.match(src, /kind: "virsh", endpoint: endpoint\.trim\(\), target_id: targetID\.trim\(\), virsh_uri: "qemu:\/\/\/system", ssh_login: login\.trim\(\), ssh_port: Number\(port\)/);

// Detect: offered only when the page can ask the pool master, and it comes before the method.
assert.doesNotMatch(fenceDialog, />Detect</);
const detectDialog = renderToStaticMarkup(createElement(FenceProfileDialog, { ...fenceProps, onDetect: async () => ({}) }));
assert.match(detectDialog, /<button type="button" disabled="">Detect<\/button>/, "no address, no Detect");
assert.ok(detectDialog.indexOf(">Detect<") < detectDialog.indexOf('id="fence-kind-redfish"'));
assert.ok(detectDialog.indexOf('id="fence-password"') < detectDialog.indexOf(">Detect<"));
// Only Core's recommendation fills the form in; a refusal is its own sentence.
const probe = { host_id: "host-a", status: "succeeded", error: "", bmc_probe: {
  methods: [{ kind: "redfish", reachable: true, target_ids: ["System.Embedded.1"] }, { kind: "ipmilan", reachable: false, detail: "no answer" }],
  recommended: { kind: "redfish", target_id: "System.Embedded.1", reason: "Redfish answered and manages this host." } } };
assert.deepEqual(bmcProbePrefill(probe), { kind: "redfish", targetID: "System.Embedded.1", note: "Redfish answered and manages this host." });
assert.deepEqual(bmcProbePrefill({ ...probe, status: "failed", error: "neither Redfish nor IPMI answered at bmc.example.test" }),
  { error: "neither Redfish nor IPMI answered at bmc.example.test" });
assert.deepEqual(bmcProbePrefill({ ...probe, bmc_probe: { methods: [] } }), { error: "The pool master found no method to recommend." });
assert.match(src, /if \("kind" in prefill\) \{ setKind\(prefill\.kind\); setSystemID\(prefill\.targetID\); \}/);
// The body is the contract's: an account only with its password, a fresh request each time.
assert.match(membersSrc, /\/bmc-probe`, "POST",\n\s*username \? \{ request_id, endpoint, username, credential: \{ secret \} \} : \{ request_id, endpoint \}\)/);
assert.match(membersSrc, /const request_id = crypto\.randomUUID\(\);\n\s*return api<BMCProbeView>/);
assert.match(membersSrc, /onDetect=\{\(endpoint, username, secret\) => detectBMC\(fenceTarget, endpoint, username, secret\)\}/);
// Detect never locks the dialog open: Close follows only the Set BMC access request.
assert.match(src, /onCancel=\{event => \{ event\.preventDefault\(\); if \(!busy\) onClose\(\); \}\}\n\s*onClose=\{\(\) => \{ if \(busy\) dialog\.current\?\.showModal\(\); else onClose\(\); \}\}>\n\s*<div className="section-heading"><h2 id="fence-title"/);
assert.doesNotMatch(src, /iDRAC|XClarity|iLO\b/);

// ------------------------------------------------------------ OD-5: no escape hatch anywhere

// Owner decision OD-5: no route, field or affordance offers a force, an override, a way to
// set a host aside as gone, or a timeout-based path — and none may be added here.
for (const [name, text] of [["pool-promotion.tsx", src], ["pool-authority.tsx", readFileSync(new URL("./pool-authority.tsx", import.meta.url), "utf8")]]) {
  assert.doesNotMatch(text, /\bforce[ds]?\b/i, `${name} offers no force`);
  assert.doesNotMatch(text, /\boverrid(e|es|den|ing)\b/i, `${name} offers no override`);
  assert.doesNotMatch(text, /\bdeclar(e|es|ed|ing)\b/i, `${name} declares no host anything`);
}
// "lost" is allowed as a plain description of a master that is gone, never as an action.
for (const markup of [actions, readyDialog, noProfileDialog, reachableDialog, blockedDialog, od5Markup, supersededMarkup, running, configured]) {
  assert.doesNotMatch(markup, /\b(force[ds]?|overrid\w*|declar\w*|permanently lost|host lost)\b/i);
  assert.doesNotMatch(markup, /<button[^>]*>[^<]*\blost\b[^<]*<\/button>/i);
}

// ------------------------------------- the restriction on the host's own page

// The host page has no pool report of its own, so the banner reads the one pool projection the
// host belongs to and renders the same note.
{
  const asked = [];
  const poolAnswer = { ...report, members: [{ ...master, connection: "fresh", restricted: unconfirmed }, member] };
  const api = path => { asked.push(path); return Promise.resolve(poolAnswer); };
  const { renderToString } = await import("react-dom/server");
  assert.equal(renderToString(createElement(HostRestrictedBanner, { poolID: "pool-1", hostID: "host-a", api })), "");
  assert.deepEqual(asked, [], "static render performs no request");
}
assert.match(src, /api<PromotionReport>\(`pools\/\$\{encodeURIComponent\(poolID\)\}\/members`\)/);
assert.match(src, /if \(!report \|\| !member\?\.restricted\) return null;/);
assert.match(src, /window\.addEventListener\("focus", onFocus\)/);
assert.match(src, /window\.setInterval\(\(\) => load\.current\(\), 10_000\)/);
assert.match(src, /if \(!restricted\) return;/);
// The host panel renders it on its Overview tab only, keyed by host so a different host refetches.
{
  const mainSrc = readFileSync(new URL("./host-panel.tsx", import.meta.url), "utf8");
  assert.match(mainSrc, /<HostRestrictedBanner key=\{host\.id\} poolID=\{host\.pool_id\} hostID=\{host\.id\} api=\{api\} report=\{report/);
  assert.equal((mainSrc.match(/HostRestrictedBanner/g) || []).length, 2, "one import and one use");
}

// ------------------------------------- no Core sentence is matched on any more

// Every state above is chosen by a structural field. These are the sentences this file used to
// match on to tell those states apart; matching them again is the defect, so their absence is
// asserted. Rendering a reason Core sent stays fine — it arrives as data, not as a literal.
for (const sentence of [
  "has no recorded fence profile",
  "has not confirmed its authority since it restarted",
  "holds no fence credential",
]) assert.ok(!src.includes(sentence), `pool-promotion.tsx must not match on Core's sentence ${JSON.stringify(sentence)}`);
// And nothing reads a reason, an error or a stage as a string to decide with.
assert.doesNotMatch(src, /\.(reason|error|detail|stage)[^\n]*\.(includes|startsWith|endsWith|match)\(/);

console.log("pool-promotion.test.mjs: ok");

// ------------------------------------------------- the Fencing details and the pool summary

// Every word of the column is the pool master's own answer; this only chooses the wording,
// and never turns an unknown or a stale check into a protected host.
{
  const withReadiness = (readiness, fence = {}) => ({ ...member, fence: { configured: true, kind: "ipmilan", ...fence, readiness } });
  const ok = (host_id, extra = {}) => ({ host_id, result: "ok", power: "on", observed_at: "2026-09-22T00:10:00Z", ...extra });

  // Ready, and how many hosts confirm it — a BMC device says "BMC", not its wire kind.
  const ready = withReadiness({ ready: true, methods: [{ kind: "ipmilan", state: "ready", checks: [ok("host-a"), ok("host-c")] }] });
  assert.equal(fencingText(ready, true).label, "BMC · checked from 2 hosts");
  assert.equal(fencingText(ready, true).badge, "ok");
  assert.equal(fenceMethodName("redfish"), "BMC");
  assert.equal(fenceMethodName("virsh"), "virsh lab device");

  // Stale is stale, never ready, and the badge is not the only thing that says so.
  const stale = withReadiness({ ready: false, methods: [{ kind: "ipmilan", state: "not_ready", reason: "the last check from host-a is stale", checks: [ok("host-a", { result: "ok", stale: true })] }] });
  assert.equal(fencingText(stale, true).label, "BMC · stale");
  assert.notEqual(fencingText(stale, true).badge, "ok");
  assert.match(renderToStaticMarkup(createElement(FencingDetail, { member: stale, authoritative: true })), /stale/);

  // A failing check names the host that found it, on the row.
  const broken = withReadiness({ ready: false, methods: [{ kind: "ipmilan", state: "not_ready",
    reason: "this management controller did not answer (from host-a)",
    checks: [{ host_id: "host-a", result: "unreachable", detail: "this management controller did not answer", observed_at: "2026-09-22T00:10:00Z" }] }] });
  assert.match(fencingText(broken, true).label, /^Not protected — /);
  assert.match(fenceCheckLines(broken, true)[0], /^host-a: this management controller did not answer/);

  // No device at all, and a host nothing has looked at yet. Neither is ever ready.
  const none = { ...member, fence: { configured: false, readiness: { ready: false, methods: [{ kind: "bmc", state: "not_ready", reason: "no fence device is recorded for this host" }] } } };
  assert.equal(fencingText(none, true).label, "Not protected — no fence device is recorded for this host");
  const unchecked = withReadiness({ ready: false, methods: [{ kind: "ipmilan", state: "unknown", reason: "no other host in this pool has checked this host's management controller yet" }] });
  assert.match(fencingText(unchecked, true).label, /^Not protected — no other host/);
  assert.notEqual(fencingText(unchecked, true).badge, "ok");

  // A pool master that reports no readiness is "not reported", never ready.
  const older = { ...member, fence: { configured: true, kind: "ipmilan" } };
  assert.equal(fencingText(older, true).label, "not reported");
  assert.match(fencingText(older, true).reasons[0], /does not report fencing readiness/);
  // And a master that is not reporting at all says so rather than repeating its last word.
  assert.equal(fencingText(ready, false).label, "unknown");
  assert.deepEqual(fenceCheckLines(ready, false), []);

  // A pool of one host is not waiting for a check that cannot happen.
  const alone = withReadiness({ ready: false, methods: [{ kind: "ipmilan", state: "no_peer",
    reason: "this pool has no other host to check this host's management controller from, and no other host to promote in its place" }] });
  assert.equal(fencingText(alone, true).label, "BMC · no other host to check from");
  assert.notEqual(fencingText(alone, true).badge, "ok");
  assert.doesNotMatch(fencingText(alone, true).reasons[0], /yet/);
  assert.equal(fenceMethodName("watchdog"), "Watchdog");

  // The watchdog is a second method, on its own line. Nothing arms one in this release, so
  // it must never read as protection and must never change the badge.
  const watchdog = state => withReadiness({ ready: false, methods: [
    { kind: "ipmilan", state: "unknown", reason: "no other host in this pool has checked this host's management controller yet" }, state] });
  const unarmed = watchdog({ kind: "watchdog", state: "not_ready", reason: "this host's hardware watchdog is not armed: Nex Core arms watchdogs once this pool has quorum" });
  assert.match(fencingText(unarmed, true).label, /^Not protected — no other host/, "the watchdog must not change the badge");
  assert.match(watchdogText(unarmed, true), /^Watchdog: this host's hardware watchdog is not armed/);
  assert.match(renderToStaticMarkup(createElement(FencingDetail, { member: unarmed, authoritative: true })), /hardware watchdog is not armed/);
  const software = watchdog({ kind: "watchdog", state: "not_ready", reason: "this host has only the kernel's software watchdog, which a hung kernel cannot run, so it does not count" });
  assert.match(watchdogText(software, true), /does not count/);
  const twoHosts = watchdog({ kind: "watchdog", state: "not_applicable", reason: "a watchdog needs three or more hosts to have a majority side; this pool is fenced through its management controllers" });
  assert.match(watchdogText(twoHosts, true), /three or more hosts/);
  assert.doesNotMatch(watchdogText(twoHosts, true), /yet/, "a two-host pool is not waiting for anything");
  // A ready BMC still reads as ready with a watchdog beside it, and the watchdog line is
  // still there to say why nothing is armed.
  const both = withReadiness({ ready: true, methods: [{ kind: "ipmilan", state: "ready", checks: [ok("host-a")] },
    { kind: "watchdog", state: "not_ready", reason: "this host's hardware watchdog is not armed: Nex Core arms watchdogs once this pool has quorum" }] });
  assert.equal(fencingText(both, true).label, "BMC · checked from 1 host");
  assert.match(watchdogText(both, true), /not armed/);
  // A master that is not reporting says nothing about a watchdog either.
  assert.equal(watchdogText(both, false), "");
}

// The promotion verdict names the method each blocking host would be shut out with, and
// warns from the candidate's own last check without letting it decide anything.
{
  const verdict = { pool_id: "pool-1", candidate_host_id: "host-b", authority_generation: 4, can_promote: false,
    fence_required: true, fence_host_ids: ["host-a"],
    hosts: [{ host_id: "host-a", reachable: false, blocks: true, fence_recorded: true, fence_usable: true,
      fence_kind: "ipmilan", fence_check_warning: "this host's last check of that fence device failed: no answer" }] };
  assert.equal(fenceCellText(verdict, verdict.hosts[0]), "powered off by this promotion · BMC");
  const view = renderToStaticMarkup(createElement(PromotionVerdictView, { report, verdict }));
  assert.match(view, /Advisory: this host&#x27;s last check of that fence device failed/);
  assert.match(view, /<small class="pool-fence-advisory">Nex Core proves the fence itself when the promotion runs; this stops nothing\.<\/small>/);
  // It is one line inside a table cell, not the block callout .warning renders.
  assert.match(view, /class="pool-fence-advisory"/);
  assert.doesNotMatch(view, /<small class="warning">/);
}
