// SPDX-License-Identifier: AGPL-3.0-only
// S5 "Planned master change and member eject": same harness as pool-members.test.mjs — esbuild
// bundles the real module and react-dom/server renders it. Run with: node src/pool-authority.test.mjs
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const moduleURL = bundleTestModule(new URL("./pool-authority.tsx", import.meta.url), "pool-authority");
const src = readFileSync(new URL("./pool-authority.tsx", import.meta.url), "utf8");
const membersSrc = readFileSync(new URL("./pool-members.tsx", import.meta.url), "utf8");
const {
  MasterChangeDialog, EjectDialog, MemberActions, ResumeDialog, TransferStrip,
  transferActionText, transferStageText, transferInProgress, transferCancellable,
  transferResumePrompt, lastTransferText, authorityText, behindText, masterConflictText,
  masterChangeBlock, ejectBlock, hostName,
} = await import(moduleURL);

// react-dom/server escapes apostrophes, so Core's sentences are matched as they are rendered.
const rx = text => new RegExp(text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/'/g, "(&#x27;|')"));

const master = { host_id: "host-a", name: "core-a", role: "master", status: "ready", connection: "fresh" };
const member = { host_id: "host-b", name: "core-b", role: "member", status: "ready", connection: "fresh", peer_origin: "https://192.0.2.12:9444" };
const report = {
  pool_id: "pool-1", pool_name: "lab-pool", master_host_id: "host-a", master_connection: "fresh",
  generation: 7, authoritative: true, transfer_supported: true, authority: { generation: 9, digest: "9f2c", observed_at: "2026-09-19T10:04:11Z" },
  transfer: null, last_transfer: null, master_conflict: [], members: [master, member],
};
const transfer = (extra = {}) => ({
  operation_id: "op-1", action: "promote", role: "source", target_host_id: "host-b",
  stage: "replicate", status: "pending", error: "", authority_generation: 9, frozen: true,
  observed_at: "2026-09-19T10:05:02Z", ...extra,
});
const strip = props => renderToStaticMarkup(createElement(TransferStrip, { onCancel: () => {}, cancelBusy: false, cancelError: "", ...props }));

// ------------------------------------------------------------ action availability matrix

assert.equal(masterChangeBlock(report, member), "", "a fresh, ready member with a peer address can be promoted");
assert.equal(ejectBlock(report, member), "", "and can be ejected");
// The master row offers neither action, and says why rather than hiding the buttons.
assert.equal(masterChangeBlock(report, master), "This host is already the pool master.");
assert.equal(ejectBlock(report, master), "The pool master cannot be ejected; change the pool master first.");
// A master that is not fresh: nothing is offered, because the membership is not current.
const staleReport = { ...report, master_connection: "stale", authoritative: false };
assert.match(masterChangeBlock(staleReport, member), /^The pool master is stale, so this membership is not current/);
assert.match(ejectBlock(staleReport, member), /^The pool master is stale, so this membership is not current/);
// Capability missing on the master, named.
const old = { ...report, transfer_supported: false };
assert.equal(masterChangeBlock(old, member), "Changing the pool master needs pool-authority-transfer-v1 on the pool master, the selected host and every ready member; upgrade core-a.");
assert.equal(ejectBlock(old, member), "Ejecting a member needs pool-authority-transfer-v1 on the pool master and every ready member; upgrade core-a.");
// An absent field is not a "no": an older projection keeps the actions offered.
assert.equal(masterChangeBlock({ ...report, transfer_supported: undefined }, member), "");
// S6 closed S5's first contract gap: the selected host's own advertised capability is in the
// projection, so a master change whose target could not perform one is not offered, and the
// host that must be upgraded is named. An eject needs nothing of the ejected host.
assert.equal(masterChangeBlock(report, { ...member, transfer_supported: false }),
  "Changing the pool master needs pool-authority-transfer-v1 on core-b; upgrade it before making it the pool master.");
assert.equal(masterChangeBlock(report, { ...member, transfer_supported: true }), "");
assert.equal(masterChangeBlock(report, { ...member, transfer_supported: undefined }), "", "an older projection is not a no");
assert.equal(ejectBlock(report, { ...member, transfer_supported: false }), "");
// No peer address, or a membership that is not ready: only Core's own sentence.
assert.equal(masterChangeBlock(report, { ...member, peer_origin: "" }), "Only a ready member with a peer endpoint can become the pool master.");
assert.equal(masterChangeBlock(report, { ...member, status: "pending" }), "Only a ready member with a peer endpoint can become the pool master.");
// A member that is not reporting cannot take the authority, but can still be ejected: it
// learns of the eject when it returns.
assert.equal(masterChangeBlock(report, { ...member, connection: "stale" }), "The selected host must be fresh and active to become the pool master.");
assert.equal(ejectBlock(report, { ...member, connection: "stale" }), "", "an unreachable member can still be ejected");
// While a transfer is pending neither action is offered.
const busy = { ...report, transfer: transfer() };
assert.equal(masterChangeBlock(busy, member), "Another pool authority transfer is already in progress on this pool.");
assert.equal(ejectBlock(busy, member), "Another pool authority transfer is already in progress on this pool.");

// The row menu shows both actions with their reasons, and is a disclosure, not a column.
const menu = renderToStaticMarkup(createElement(MemberActions, { report, member, onMasterChange: () => {}, onEject: () => {} }));
assert.match(menu, /<details class="pool-row-actions"><summary>Actions<span class="sr-only"> for core-b<\/span><\/summary>/);
assert.match(menu, /<button type="button">Make pool master<\/button>/);
assert.match(menu, /<button type="button" class="danger">Eject from pool<\/button>/);
// The row's other actions share the same disclosure, ahead of the destructive eject.
assert.match(renderToStaticMarkup(createElement(MemberActions, { report, member, onMasterChange: () => {}, onEject: () => {}, extra: createElement("button", { type: "button" }, "Promote to pool master…") })),
  /<div><button type="button">Promote to pool master…<\/button><button type="button">Make pool master<\/button><button type="button" class="danger">Eject from pool<\/button><\/div><\/details>/);
assert.equal((renderToStaticMarkup(createElement(MemberActions, { report, member, onMasterChange: () => {}, onEject: () => {}, extra: createElement("span") })).match(/<details/g) || []).length, 1);
// The master can never be made master or ejected, so its row does not offer either.
const masterMenu = renderToStaticMarkup(createElement(MemberActions, { report, member: master, onMasterChange: () => {}, onEject: () => {} }));
assert.doesNotMatch(masterMenu, /Make pool master|Eject from pool/);

// ------------------------------------------------------------ the transfer status strip

// Nothing to report: no strip at all rather than an empty panel.
assert.equal(strip({ report }), "");

// Stage and status are words from the host reports, never a spinner.
assert.equal(transferStageText(transfer({ stage: "freeze" })), "pausing pool authority writes");
assert.equal(transferStageText(transfer({ stage: "repoint" })), "pointing every member at the new master");
assert.equal(transferStageText(transfer({ stage: "invented" })), "invented", "an unknown stage is printed as Core sent it");
assert.equal(transferStageText(null), "unknown");
assert.equal(transferActionText("promote"), "master change");
assert.equal(transferActionText("eject"), "eject");
assert.equal(transferActionText("transfer-cancel"), "cancel of a master change");
assert.equal(transferInProgress(transfer()), true);
assert.equal(transferInProgress(transfer({ status: "succeeded" })), false);
assert.equal(transferInProgress(null), false);

const pending = strip({ report: { ...report, transfer: transfer(), members: [master, { ...member, transfer: transfer({ role: "target", stage: "activate" }) }] } });
assert.match(pending, /<strong>master change<\/strong> · target core-b ·/);
assert.match(pending, /stage <strong>copying the pool records to the new master<\/strong>/);
assert.match(pending, /status <strong>pending<\/strong>/);
assert.match(pending, /Pool settings, placements, joins and maintenance changes are refused while this runs; nothing is queued\./);
assert.match(pending, /Virtual machines that are running keep running/);
assert.match(pending, /core-b: target · handing the pool authority to the new master · pending/);
assert.doesNotMatch(pending, /spinner|progress-bar/i);

// A participant that is not reporting is unknown, not finished.
const silent = strip({ report: { ...report, transfer: transfer(), members: [master, { ...member, connection: "stale", transfer: null }] } });
assert.match(silent, /core-b: unknown — this host is not reporting its part of the change\./);

// A master that is not fresh makes the whole picture unknown.
const unknown = strip({ report: { ...staleReport, transfer: transfer() } });
assert.match(unknown, /the state of any master change or eject is <strong>unknown<\/strong>/);
assert.doesNotMatch(unknown, /copying the pool records/, "a last-known stage is never shown as present truth");

// needs_attention and failed are the host's own words, shown verbatim.
const attention = strip({ report: { ...report, transfer: transfer({ status: "needs_attention", stage: "repoint", error: "pool peer host-c does not support pool-authority-transfer-v1; upgrade it before changing the pool master" }) } });
assert.match(attention, /status <strong>needs attention<\/strong>/);
assert.match(attention, /task-error">pool peer host-c does not support pool-authority-transfer-v1; upgrade it before changing the pool master/);
const failed = strip({ report: { ...report, transfer: transfer({ status: "failed", stage: "aborted", error: "this pool master change was aborted on this host" }) } });
assert.match(failed, /stage <strong>cancelled<\/strong>/);
assert.match(failed, /this pool master change was aborted on this host/);
assert.match(strip({ report: { ...report, transfer: transfer({ status: "succeeded", stage: "done" }) } }), /stage <strong>finished<\/strong>/);

// ------------------------------------------------------------ cancel availability

// Core computes `cancellable` from the same predicate its cancel route enforces, so when the
// host that owns the transfer reports it, it decides outright — at any stage, either way.
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "repoint", cancellable: true }) }), true, "the owning host's own answer wins over every stage");
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "freeze", cancellable: false }) }), false, "and its no wins too");
assert.equal(transferCancellable({ ...report, transfer: transfer({ action: "eject", stage: "remove", cancellable: false }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ action: "eject", stage: "leave", cancellable: true }) }), true);
// A target reporting a later stage never overrides the owner's own answer.
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "replicate", cancellable: true }), members: [master, { ...member, transfer: transfer({ role: "target", stage: "activate" }) }] }), true);

// Without Core's own `cancellable` field, nothing is offered.
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "freeze" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "replicate" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "activate" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "preflight" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ action: "eject", stage: "freeze" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ action: "eject", stage: "remove" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ action: "eject", stage: "repoint" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ action: "eject", stage: "leave" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ stage: "repoint" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: transfer({ status: "succeeded" }) }), false);
assert.equal(transferCancellable({ ...report, transfer: null }), false);
assert.equal(transferCancellable({ ...staleReport, transfer: transfer() }), false, "nothing is offered against a master that is not fresh");
assert.equal(transferCancellable({ ...report, transfer: transfer(), members: [master, { ...member, transfer: transfer({ role: "target", stage: "repoint" }) }] }), false);
const cancellable = strip({ report: { ...report, transfer: transfer({ stage: "freeze", cancellable: true }) } });
assert.match(cancellable, /<button type="button">Cancel this master change<\/button>/);
assert.match(cancellable, /has not been handed over yet, so this change can still be cancelled/);
const forward = strip({ report: { ...report, transfer: transfer({ stage: "repoint", cancellable: false }) } });
assert.doesNotMatch(forward, /Cancel this master change/);
// The sentence names Core as the one that decided, and is true of an eject as well — the
// membership write, not the authority hand-off, is what an eject passes.
assert.match(forward, /Nex Core reports that this master change can no longer be cancelled\. It has passed the step that cannot be undone and can only go forward\./);
assert.match(strip({ report: { ...report, transfer: transfer({ action: "eject", stage: "leave", cancellable: false }) } }),
  /Nex Core reports that this eject can no longer be cancelled\./);
assert.match(strip({ report: { ...report, transfer: transfer({ stage: "freeze", cancellable: true }) }, cancelBusy: true }), /disabled=""[^>]*>Submitting…</);
assert.match(strip({ report: { ...report, transfer: transfer({ stage: "freeze" }) }, cancelError: "the pool master must be fresh and active to cancel a master change" }), /role="alert"[^>]*>the pool master must be fresh and active to cancel a master change/);

// ------------------------------------------------- the asynchronous refusal (last_transfer)

assert.equal(lastTransferText(null), "");
assert.equal(lastTransferText({ operation_id: "op-1", action: "master-change", status: "succeeded" }), "");
// The one retryable refusal is explained, not just repeated.
assert.equal(
  lastTransferText({ operation_id: "op-1", action: "master-change", status: "failed", conflict: true, retryable: true, error: "pool authority is frozen for a master change; retry when it completes" }),
  "pool authority is frozen for a master change; retry when it completes The pool is changing master; retry when it completes.");
assert.equal(
  lastTransferText({ operation_id: "op-2", action: "eject", status: "failed", conflict: true, error: "pool membership changed since this request was composed; review the pool and try again" }),
  "pool membership changed since this request was composed; review the pool and try again The pool changed since you loaded it — reload and look again before retrying.");
// Core's eject refusals arrive here, verbatim, where the operator acted.
assert.equal(
  lastTransferText({ operation_id: "op-3", action: "eject", status: "failed", error: "host host-b still owns pool VM records (vm-1, vm-2); move or delete them before ejecting it" }),
  "The eject failed: host host-b still owns pool VM records (vm-1, vm-2); move or delete them before ejecting it");
// Core's three forward-only cancel refusals. They are `conflict` with `retryable` false, and
// they are the newest transfer operation, so `action` is what identifies them — never the
// sentence, which is shown exactly as Core wrote it and never offered as a retry.
for (const forwardOnly of [
  "the target has already activated this pool authority; the master change can only go forward",
  "host host-b is already removed from this pool's membership; the eject can only go forward",
  "this pool promotion has powered the old pool master off; it cannot be cancelled and can only go forward",
]) {
  const text = lastTransferText({ operation_id: "op-9", action: "transfer-cancel", status: "failed", conflict: true, retryable: false, error: forwardOnly });
  assert.equal(text, `${forwardOnly} Nothing was cancelled, and retrying will not cancel it either; the change completes on the hosts.`);
  assert.doesNotMatch(text, /reload and look again before retrying|retry when it completes/);
}
// A cancel that failed for any other reason is still reported as a plain failure.
assert.equal(lastTransferText({ operation_id: "op-9", action: "transfer-cancel", status: "failed", error: "the pool master must be fresh and active to cancel a master change" }),
  "The cancel of a master change failed: the pool master must be fresh and active to cancel a master change");
// And a genuine stale-read conflict on a master change is untouched by the rule above.
assert.match(lastTransferText({ operation_id: "op-9", action: "master-change", status: "failed", conflict: true, error: "pool membership changed since this request was composed; review the pool and try again" }),
  /reload and look again before retrying\.$/);
assert.match(lastTransferText({ operation_id: "op-4", action: "master-change", status: "needs_attention", error: "" }), /^Needs attention: Nex Core reported no message\. The hosts carry on without this Controller/);
assert.match(lastTransferText({ operation_id: "op-5", action: "master-change", status: "queued" }), /^The master change was submitted\./);
const refused = strip({ report: { ...report, last_transfer: { operation_id: "op-3", action: "eject", status: "failed", error: "host host-b still holds a pool-shared storage reservation on repo-1; let it finish before ejecting it" } } });
assert.match(refused, /task-error"[^>]*>The eject failed: host host-b still holds a pool-shared storage reservation on repo-1; let it finish before ejecting it/);
const frozen = strip({ report: { ...report, last_transfer: { operation_id: "op-1", action: "master-change", status: "failed", conflict: true, retryable: true, error: "pool authority is frozen for a master change; retry when it completes" } } });
assert.match(frozen, /role="alert"/);
assert.match(frozen, /The pool is changing master; retry when it completes\./);

// ------------------------------------------------------------ master_conflict banner

assert.equal(masterConflictText(report), "");
assert.equal(masterConflictText({ ...report, master_conflict: undefined }), "");
const conflicted = { ...report, master_conflict: ["host-b"] };
assert.equal(masterConflictText(conflicted),
  "core-b reports being the pool master of this pool, at no newer generation than core-a, which this Controller still treats as the master. Nothing is changed while they disagree: check both hosts before you act.");
assert.match(masterConflictText({ ...report, master_conflict: ["host-b", "host-c"] }), /^core-b, host-c report being/, "a claimant that is not in the membership is named by its host id");
assert.match(strip({ report: conflicted }), /class="alert" role="alert">core-b reports being the pool master/);
assert.equal(hostName(report, "host-z"), "host-z");

// ------------------------------------------------- authority generation and "behind by N"

assert.equal(authorityText(report), "The pool authority is at generation 9.");
assert.equal(authorityText({ ...report, authority: null }), "The pool master is not reporting an authority generation, so how far behind a copy is cannot be said here.");
assert.equal(authorityText(staleReport), "The pool master is not reporting an authority generation, so how far behind a copy is cannot be said here.");
const holding = gen => ({ ...member, replica: { generation: gen, acknowledged_generation: gen } });
assert.equal(behindText(holding(9), report), "");
assert.equal(behindText(holding(8), report), "1 generation behind");
assert.equal(behindText(holding(7), report), "2 generations behind");
assert.equal(behindText({ ...member, replica: { generation: 9, acknowledged_generation: 6 } }, report), "3 generations behind", "the lower of the two reports is the one that can be relied on");
assert.equal(behindText(holding(7), { ...report, authority: null }), "", "without a reported authority generation nothing is claimed");
assert.equal(behindText(holding(7), staleReport), "");
assert.equal(behindText({ ...master, replica: { generation: 0, acknowledged_generation: 0 } }, report), "", "the master holds the authority; it is not behind itself");
assert.equal(behindText(member, report), "", "a member with no replica report says nothing here");

// ------------------------------------------------------------ the two dialogs

const dialogProps = { report, busy: false, submitError: "", submitReady: true, onConfirm: () => {}, onClose: () => {} };
const promote = renderToStaticMarkup(createElement(MasterChangeDialog, { ...dialogProps, member }));
assert.match(promote, /aria-labelledby="master-change-title"/);
assert.match(promote, /Make core-b the pool master/);
assert.match(promote, /core-a is the pool master of lab-pool now\. core-b takes the pool authority/);
assert.match(promote, /pool settings, placements, joins and maintenance changes are <strong>refused<\/strong>/);
assert.doesNotMatch(promote, /keep their identities and their names/);
assert.doesNotMatch(promote, /Until core-b has taken the authority/);
// The typed confirmation is the pool's name, it is a labelled input, and nothing can be
// submitted until it matches.
assert.match(promote, /<label for="master-change-confirm">Type the pool name lab-pool to confirm<\/label>/);
assert.match(promote, /<button class="primary" disabled="">Make pool master<\/button>/);
assert.match(renderToStaticMarkup(createElement(MasterChangeDialog, { ...dialogProps, member, submitReady: false })), /<button class="primary" disabled="">Make pool master<\/button>/);
const promoteBusy = renderToStaticMarkup(createElement(MasterChangeDialog, { ...dialogProps, member, busy: true }));
assert.match(promoteBusy, /disabled="">Submitting…</);
const promoteError = renderToStaticMarkup(createElement(MasterChangeDialog, { ...dialogProps, member, submitError: "the pool master reports membership generation 7; reload the pool and retry" }));
assert.match(promoteError, /role="alert"[^>]*>the pool master reports membership generation 7; reload the pool and retry/);

const ejectDialog = renderToStaticMarkup(createElement(EjectDialog, { ...dialogProps, member }));
assert.match(ejectDialog, /aria-labelledby="eject-title"/);
assert.match(ejectDialog, /Eject core-b from lab-pool/);
assert.match(ejectDialog, /Pool-shared networks, storage and ISOs are removed from it/);
assert.doesNotMatch(ejectDialog, /becomes the pool master of its own new single-host pool/);
assert.match(ejectDialog, /<label for="eject-confirm">Type the host name core-b to confirm<\/label>/);
assert.match(ejectDialog, /<button class="primary danger" disabled="">Eject from pool<\/button>/);
const ejectError = renderToStaticMarkup(createElement(EjectDialog, { ...dialogProps, member, submitError: "the confirmation must be the host name \"core-b\"" }));
assert.match(ejectError, /role="alert"[^>]*>the confirmation must be the host name/);

assert.match(src, /confirmMatches\(typed, report\.pool_name\)/);
assert.match(src, /import \{ confirmMatches \} from "\.\/managed"/);
// No dialog here can be dismissed while its request is in flight.
assert.equal((src.match(/onClose=\{\(\) => \{ if \(busy\) dialog\.current\?\.showModal\(\); else onClose\(\); \}\}/g) || []).length, 3);

// ------------------------------------------- the hand-off whose target never answered

// The gate is structural: Core's own reported fields decide, and its sentence is only what
// gets displayed. The source says it handed the authority over and that the transfer is past
// cancelling, the transfer is unfinished, and the target's own row is not fresh.
const resumePrompt = "pool peer host-b did not answer, and this pool's authority stays with it until that host is powered off; confirm powering off host-b to resume this master change's source";
const handedOffTransfer = { stage: "activate", status: "needs_attention", handed_off: true, cancellable: true, error: "pool peer host-b did not answer this transfer step" };
const lostTarget = { ...member, connection: "stale" };
const handedOff = {
  ...report,
  members: [master, lostTarget],
  transfer: transfer(handedOffTransfer),
  last_transfer: { operation_id: "op-9", action: "transfer-cancel", status: "failed", error: resumePrompt },
};
assert.equal(transferResumePrompt(handedOff), resumePrompt, "Core's sentence is what is displayed");
// Without a sentence from Core the offer still stands: the reported fields are the gate.
assert.equal(transferResumePrompt({ ...handedOff, last_transfer: null, transfer: transfer({ ...handedOffTransfer, error: "" }) }),
  "This pool's authority was handed to core-b, and that host is not reporting. The authority stays with it until it is powered off.");
assert.equal(transferResumePrompt({ ...handedOff, last_transfer: null }), "pool peer host-b did not answer this transfer step", "the transfer's own error is Core's too");
// An older Core that cannot say whether it handed the authority over offers nothing at all.
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, handed_off: undefined }) }), "", "an unsaid flag is unknown, not false");
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, handed_off: false }) }), "", "nothing irreversible has happened yet");
// Core keeps a handed-off master change cancellable — the plain cancel still wins if the
// target answers that it never activated — so `cancellable` never withdraws this offer. Both
// are shown, and Core decides which one it accepts.
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, cancellable: true }) }), resumePrompt);
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, cancellable: false }) }), resumePrompt);
assert.equal(transferResumePrompt(report), "", "no transfer, no offer");
// The target being back is the whole reason not to power it off.
assert.equal(transferResumePrompt({ ...handedOff, members: [master, member] }), "", "the target is reporting again");
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, target_host_id: "host-c" }) }),
  "pool peer host-b did not answer, and this pool's authority stays with it until that host is powered off; confirm powering off host-b to resume this master change's source",
  "a target with no row at all is not fresh either");
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, role: "target" }) }), "", "only the source can resume");
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, stage: "done", status: "succeeded" }) }), "");
assert.equal(transferResumePrompt({ ...handedOff, transfer: transfer({ ...handedOffTransfer, stage: "aborted", status: "failed" }) }), "");
assert.equal(transferResumePrompt({ ...handedOff, authoritative: false }), "", "nothing is offered against a master that is not fresh");
// The gate reads fields, never Core's prose.
assert.doesNotMatch(src, /confirm powering off/, "the resume offer no longer matches on a sentence");

// The strip shows Core's prompt and offers the action only when a handler is given.
const resumeStrip = strip({ report: handedOff, onResume: () => {} });
assert.match(resumeStrip, rx(resumePrompt));
assert.match(resumeStrip, /<button type="button" class="danger">Resume on the current master…<\/button>/);
assert.doesNotMatch(strip({ report, onResume: () => {} }), /Resume on the current master/);
assert.doesNotMatch(strip({ report: handedOff }), /<button[^>]*>Resume on the current master/, "an unavailable view offers no action");
assert.match(strip({ report: handedOff, onResume: () => {}, cancelBusy: true }), /class="danger" disabled=""[^>]*>Resume on the current master…<\/button>/);

// The dialog is blunt about the power-off, and both confirmations are required.
const resumeProps = { report: handedOff, prompt: resumePrompt, busy: false, submitError: "", submitReady: true, onConfirm: () => {}, onClose: () => {} };
const resumeDialog = renderToStaticMarkup(createElement(ResumeDialog, resumeProps));
assert.match(resumeDialog, /aria-labelledby="resume-title"/);
assert.match(resumeDialog, /Resume lab-pool on core-a/);
assert.match(resumeDialog, rx(resumePrompt));
assert.match(resumeDialog, /<strong>Nex Core powers core-b off<\/strong> through its recorded fence device and verifies that it is off before resuming this pool&#x27;s authority on core-a/);
assert.doesNotMatch(resumeDialog, /If core-b answers before Nex Core fences it/);
assert.match(resumeDialog, /<label for="resume-confirm">Type the pool name lab-pool to confirm<\/label>/);
assert.match(resumeDialog, /<input id="resume-fence-confirm" type="checkbox"\/?>/);
assert.match(resumeDialog, /I understand this powers off: core-b/);
assert.match(resumeDialog, /<button class="primary danger" disabled="">Power core-b off and resume<\/button>/);
assert.match(renderToStaticMarkup(createElement(ResumeDialog, { ...resumeProps, busy: true })), /disabled=""[^>]*>Submitting…</);
assert.match(renderToStaticMarkup(createElement(ResumeDialog, { ...resumeProps, submitError: "pool peer host-b is reachable; retry the cancellation rather than powering it off" })),
  /role="alert"[^>]*>pool peer host-b is reachable; retry the cancellation rather than powering it off/);
// Both gates are real, and the host sent is Core's own target, never anything typed.
assert.match(src, /const ready = submitReady && Boolean\(targetID\) && confirmMatches\(typed, report\.pool_name\) && acknowledged;/);
assert.match(src, /if \(!ready\) return;\n\s*onConfirm\(targetID\);/);

// The cancel body: the plain one unchanged, and the resume carrying Core's two fields with the
// one host it named. A different body is a different request identity.
assert.match(membersSrc, /\{ request_id, host_id: target, expected_version: report\?\.generation \|\| 0, fence_confirmed: true, fence_host_ids: \[fenceTargetID\] \}/);
assert.match(membersSrc, /: \{ request_id, host_id: target, expected_version: report\?\.generation \|\| 0 \}\)/);
assert.match(membersSrc, /heldRequestID\(heldCancel\.current, `\$\{poolID\}\\ntransfer-cancel\\n\$\{target\}\\n\$\{fenceTargetID \|\| ""\}`\)/);
// The dialog is opened only while Core is asking for it, so the action cannot outlive the prompt.
assert.match(membersSrc, /resumeOpen && transferResumePrompt\(report\) && <ResumeDialog/);

// ------------------------------------------- a cancel that succeeded is not a failure

// Core now answers a cancel that did what was asked with `succeeded` and its reason. The
// fence-backed resume arrives here too, and it reads as the outcome it is.
assert.equal(lastTransferText({ operation_id: "op-9", action: "transfer-cancel", status: "succeeded", error: "the pool master change was cancelled by an operator" }),
  "The master change was cancelled: the pool master change was cancelled by an operator");
assert.equal(lastTransferText({ operation_id: "op-9", action: "transfer-cancel", status: "succeeded", error: "the target of this master change was powered off and could not take the pool's authority; this host resumed it" }),
  "The master change was cancelled: the target of this master change was powered off and could not take the pool's authority; this host resumed it");
assert.equal(lastTransferText({ operation_id: "op-9", action: "transfer-cancel", status: "succeeded" }), "The master change was cancelled.");
// The Controller carries a performed cancel's sentence in `reason` with `error` empty, so the
// outcome is read from there; `error` stays the fallback a refusal uses.
assert.equal(lastTransferText({ operation_id: "op-9", action: "transfer-cancel", status: "succeeded", error: "", reason: "the target of this master change was powered off and could not take the pool's authority; this host resumed it" }),
  "The master change was cancelled: the target of this master change was powered off and could not take the pool's authority; this host resumed it");
assert.equal(lastTransferText({ operation_id: "op-2", action: "eject", status: "failed", reason: "", error: "host host-b still owns pool VM records" }),
  "The eject failed: host host-b still owns pool VM records");
// Every other succeeded operation still says nothing.
assert.equal(lastTransferText({ operation_id: "op-1", action: "master-change", status: "succeeded", error: "" }), "");
const cancelled = strip({ report: { ...report, last_transfer: { operation_id: "op-9", action: "transfer-cancel", status: "succeeded", error: "the target of this master change was powered off and could not take the pool's authority; this host resumed it" } } });
assert.match(cancelled, /The master change was cancelled: the target of this master change was powered off/);
assert.doesNotMatch(cancelled, /task-error/, "a cancellation that succeeded is not rendered as a failure");
assert.doesNotMatch(cancelled, /role="alert"/);

console.log("pool-authority.test.mjs: ok");
