# Phase 6 post-close staged-code review

Date: 2026-09-27. Method: read-only source trace of the staged Controller, Core,
agent, protocol and VM host backup paths after the disposable close decision.
This is a code review, not a new host or browser walkthrough. The earlier lab and
browser reports retain their original results and binary scope.

## Review findings and follow-up

The initial source review identified unsafe gaps in firmware restore, uncertain
native cutover, exact retry integrity, NFS source verification, and master authority
transfer. Subsequent worktree edits added a fail-closed firmware restore refusal,
post-copy and exact-retry digests, streamed disk hashing, Core-derived NFS mounts
and source checks, selected repository/manifest binding, and restore pins in the
authority replica. These are source observations, not new host recovery evidence.
Later staged VM host work replaced that broad firmware refusal with bounded
NVRAM and vTPM artifact installation, journaled native-definition binding and
post-definition verification. The relevant unit tests pass; a disposable native
libvirt/swtpm restore has not yet run against these edits.

The initial review found that the agent path changed `needs_attention` backup
work back to `pending` on an exact replay. Restore replace can enter that state
after cutover effects begin; the correction reconciles native state and the
durable cutover journal before settlement.
The initial promotion review found that `adoptPoolBackupAuthority` wrote
repositories, policies, run files, catalog files and pins sequentially before
recording the new master identity. A crash between those writes could expose a
mixed authority tree. It also did not install newly replicated metadata
operation receipts. This observation describes the earlier worktree version;
the correction and its current verification follow.
The completed source correction stages all backup stores, including pins and
metadata receipts, in a versioned tree and changes its active root with the
master peer record. On a peer-save error it re-reads that record; an uncertain
read or a record already selecting the new tree prevents rollback. The tree's
own rollback callback also preserves a referenced root. Focused activation and
promotion tests passed after this correction; power-loss behavior still needs
the disposable promotion campaign.
The paged authority has a 64 MiB total-byte limit and a 1024-page limit.
Records cannot cross 256 KiB pages. The encoder's greedy packing makes the
64 MiB byte bound arrive before the 1024-page count for valid records.
Exceeding either bound refuses replica construction rather than
truncating history. The new admission guard refuses crossing the bound before
committing metadata, but an older oversized history or failed push can leave
the member without a current complete backup authority replica and make
full-history takeover unavailable. The
current plan's complete retained-history takeover requirement is unproven at
this capacity boundary; a near-boundary growth/transfer/promotion test and a
product capacity decision are still required.
The later source correction preflights master metadata writes against the
signed authority limit, reserves outcome room for pending runs and receipts,
and refuses a protection marker before changing the repository when authority
cannot hold it. Code owners reported five focused capacity tests and full
agent/protocol/vmhost package tests passing; this was source/test evidence,
not a native near-boundary transfer. The greedy page encoder packs adjacent
pages beyond one 256 KiB page of combined content, so 1024 pages cannot be
reached below the 64 MiB byte limit. The focused
`TestPoolBackupAuthoritySparsePagesRemainBelowPageLimit` assertion passed:
even records above 128 KiB per page reached at most 511 pages before the
64 MiB refusal. No native transfer was part of this check.
`go test ./internal/agent -run
'TestPoolBackupPagedAuthorityRetainsMoreThanFiveThousandRuns|TestPoolBackupActivationSwitchesAllStoresWithPeerRecord|TestPoolBackupAuthorityNearTransferLimit'
-count=1` passed. The near-limit case exercised deterministic encoding over
60 MiB and refusal beyond the bound, not a native peer transfer or promotion.
The regular Core-derived NFS registration and access-test paths now use a
bounded preparation worker outside the serialized Store owner. A full
Controller outage/promotion and three-host replace rerun remains open for this
changed candidate.
An independent member-flow trace also found test restore treating errors from
master VM-definition reservation or native restored-VM registration as terminal
failure and releasing its restore pin. Both calls can leave an uncertain
definition after a lost reply. The subsequent source correction changed these
branches to `needs_attention` and retained the pin. Core now observes the exact
retained native journal and definition for as-new and test restore, then settles
only after the post-definition evidence matches. A focused placement regression
checks attention and pin retention; a native interrupted-reply run remains open.
Another admission path clears a restore pin when reserving the run reports an
error. The run file is written by atomic rename followed by directory sync, so
a late sync error can leave a pending run despite the error. The reservation
and pin must be reconciled from durable state before cleanup. The subsequent
source correction removed the immediate pin clears on all four reservation
errors, retaining protection when the write outcome is uncertain. A focused
regression simulates a saved run followed by a durability error and confirms
that its pin remains; native power-loss validation remains open.
Catalog import/rebuild now verifies repository access before scanning, reports
protection-marker and pin-repair errors, and retains catalog entries for
protected or pinned points omitted from a scan. Focused regressions passed for
injected marker-write failure and missing protected/pinned points. Rebuild
still removes ordinary absent entries without deleting repository payloads.
After promotion commits the new peer record, an empty signed catalog with
repositories enters durable `catalog-rebuild` before member re-pointing.
Import errors leave this stage at `needs_attention`; candidate and target
state machines retry the same stage without a second activation or rollback
of the committed backup root. `TestPromotedCatalogImportFailureRemainsRetryable`
called `advancePoolPromotion` for failed import and recovery, then checked
the durable status, imported point and unchanged membership generation.
Focused promotion/activation tests passed. This is source and simulated
failure evidence, not a native interrupted promotion walkthrough.

These findings are based on source paths in `internal/agent/backup*.go`,
`internal/agent/backup_policy.go`, `internal/agent/backup_authority_transfer.go`,
`internal/vmhost/managed_backup*.go`, and `internal/protocol/backup.go`. They do
not establish a live exploit or replace the documented disposable-lab evidence.

## Verification performed for this note

`go test ./internal/vmhost ./internal/agent ./internal/protocol ./internal/localcore
./internal/controller` passed with cached results during the initial review.
`go test ./internal/controller -run 'Backup|Policy' -count=1` passed during the
Controller replay correction review. The OpenAPI contract suite passed 74 tests
after the peer route coverage adapter and backup schema corrections. The
revised UI retry paths were independently read but not browser walked at that
point. No new product backup, restore, browser, interrupted-request or promotion
walkthrough had run during the initial source review. During the later target-host integration review, `go test
./internal/localcore ./internal/agent -run 'Backup|Restore' -count=1` passed
localcore and failed the agent alias-refusal assertion: the alias was refused
with a manifest-directory identity error, but the test expected
`ErrBackupConflict`. This is a test/error classification mismatch, not evidence
that the alias was accepted. Source review also found Core-local backup and
restore admission missing the new target host identity. The local service now
derives it from the enrolled agent, and `go test ./internal/localcore -run
Backup -count=1` passed its route shape checks; native admission was not run.
The added paged replica OpenAPI route/schema passed 74 API contract tests after
the Core path constant matched the route coverage adapter. A later focused
`go test ./internal/agent ./internal/protocol ./internal/localcore -run
'Backup|Restore' -count=1` could not build while the VM host lane was editing:
`managed_backup_restore.go` referenced an unfinished `setRestoreNVRAM`; only the
protocol package passed in that invocation. This is an in-progress result,
not a final failure assessment.
After the VM host helper landed, `go test ./internal/agent ./internal/localcore
./internal/vmhost ./internal/protocol -run 'Backup|Restore|PoolPromotion|Adopt'
-count=1` passed. `go test ./internal/agent -run
'TestPoolBackupActivationSwitchesAllStoresWithPeerRecord|TestPoolPromotionRetainsBackupAuthority|TestAdopt'
-count=1` also passed. These checks did not perform a native swtpm restore or
power-loss promotion.
`go test ./internal/agent -run
'TestPromotedCatalogImportFailureRemainsRetryable|TestPoolPromotionRebuildsCatalogFromNFSWhenIndexEmpty|TestPoolPromotionRetainsBackupAuthority|TestPoolBackupActivationSwitchesAllStoresWithPeerRecord'
-count=1` passed after the durable catalog stage was added.

## Current candidate browser rewalk

The current working-tree UI was rebuilt with `npm --prefix web run build` after
the Replace correction and served by disposable Controller B and C processes
using that same `web/dist`. There is no immutable build digest for this rewalk.
The reviewer used Playwright Core with Chrome for Testing 153.0.8010.12 at
1280×900 and 390×844. The disposable Controller C was connected to the pool
master and one member over loopback HTTPS.

On live Controller C, the Backups view showed eight recovery points. Replace
was available for three points whose source VMs were present; five points
with unavailable source VMs had no Replace action in row or drawer and showed
an explanation. The valid Replace flow named the current VM, required typed
confirmation and held Next disabled until the confirmation matched. Tasks
showed the pending v4 restore as Dispatched/Awaiting agent result, plus one
running task; the reviewer did not submit a destructive Replace. Repositories
showed two entries, repository access okay and member access unknown. The
mobile table and panel scrolled internally without widening the 390-pixel
document. No browser page errors were observed. A prior Controller B rewalk
showed the stale-host warning; the current Controller C run did not exercise
that state.

A separate browser fixture intercepted the backup-catalog GET and returned a
synthetic HTTP 503. It displayed a recovery-points-unavailable alert while the
repository selector remained populated, with no page error. This was a UI
fixture, not a live Core outage or native failure. Local screenshots were
inspected but are not attached because their raw captures contain disposable
environment details. UI typecheck, tests, build and privacy checks passed
before this rewalk. The changed-binary three-host recovery campaign and native
v4 restore result remain separate open acceptance work.

## Per-operation restore pin correction

A later native attempt found that the unresolved v3 restore's pin on one
recovery point refused a second restore to a different VM before its run was
created. Source review traced this to a pin file keyed by recovery point ID.
The correction keys pins by restore operation ID, reads older point-keyed
records into that map without dropping an unresolved pin, and leaves the
point protected while any pin remains. Same-target conflict still applies.
The signed authority replica accepts multiple pins for one point only when
their operation IDs differ. Focused legacy-migration, duplicate/corrupt pin,
distinct-target, same-target and activation tests passed; `go test
./internal/agent ./internal/protocol ./internal/vmhost -count=1` passed as
reported by the code owner. No native rerun of the changed pin behavior has
been recorded here. The v3 restore, its pin, and the legacy owner record stay
in `needs_attention` pending audited repair.
The same review found that a promoted master could receive an exact retry for
an inherited uncertain restore without holding the former master's execution
journal. Admission now returns the matching durable run outcome without
enqueuing native work, while a changed intent conflicts. Planned master
change/eject refuses unresolved backup journal work, pending runs and pending
metadata receipts before freezing. A fenced promotion retains pins and marks
inherited pending run/receipt results `needs_attention`. Focused tests passed
for exact replay, changed intent, pre-freeze refusal and inherited status;
the changed-binary native promotion/retry campaign remains open.

## Current test-restore UI correction

The current working-tree UI now lists each live successful test VM from one
recovery point in a separate labelled section, newest first. Each section
links to its test VM, offers operator-recorded passed/failed results and
requires that VM's exact ID plus fresh shut-off VM and fresh host observations
before cleanup. A failed run-inventory GET displays **Test restore history
unavailable** in the list and drawer instead of silently hiding test controls.
The UI owner reported `node web/src/backups-test-restores.test.mjs` red before
the fix and green afterward, then full web typecheck, tests, build and privacy
checks passing after the final change. No current-binary browser rewalk of
multiple test VMs or native cleanup was run during the lab pause.

## Test VM origin proof correction

Source review found that a successful test run and master owner row alone did
not prove a currently observed VM with the same ID was the original restore.
The correction checks the native creator journal and current definition on
the owner host. For a member VM, the master uses authenticated read-only peer
proof and matches the returned run and intent before saving the catalog
result. Passed additionally requires current disk and firmware observations;
Failed permits damaged artifacts while retaining exact creator and definition
proof. Cleanup binds the native delete and final owner removal to the same
creator; a lost delete reply needs an exact native receipt and absence proof.
Legacy blank creator and missing native journal refuse verification. Focused
agent/vmhost tests and full `go test ./internal/agent ./internal/protocol
./internal/vmhost -count=1` passed as reported by the code owners; no observed
pre-fix red run or current-binary native Verify/Cleanup run is claimed.

The executing Core now has a narrow exact-operation resume for a restore-as-new
or test restore retained in native `allocating`. It validates the original run,
intent, target and journal, then recopies verified artifacts for that same
operation and observes native completion before settling the run and pin.
`TestAllocatingRestoreResumesOnlyExactRetainedOperation` passed after the fix:
changed intent did not call native placement, failed exact resume kept the
attention run/pin, and successful exact resume settled once. Full affected Go
packages and privacy checks passed as reported by the agent owner. This is
hermetic test evidence; Core3 v5 native resumed outcome remains open.
New native journals now bind the immutable recovery-point manifest digest and
disk descriptors before placement. The legacy v5 `allocating` binder checks
exact staged and placed disk hashes, firmware, unchanged target and absence of
domain/TPM effects under the native lock before binding. Focused
`TestBindLegacyRestoreAllocationRequiresCompleteExistingArtifacts` passed as
reported by the vmhost owner; it does not establish the native v5 outcome.

## Test-restore browser fixture rewalk

After the grouped test-VM control correction, disposable Controller C served
the current working-tree `web/dist`, rebuilt immediately before the walk.
There was no immutable UI build hash. Playwright Core drove headless Chrome
for Testing 153.0.8010.12 at 1280×900 and 390×844. The live catalog GET
loaded eight points; the recovery-point drawer closed with Escape and no page
JavaScript error occurred.

A browser GET fixture layered two synthetic successful test runs and VM rows
over one live recovery point. The drawer showed separate newest-first blocks
with individually named Passed/Failed controls. A wrong cleanup ID left the
action disabled; typing the older VM's exact ID enabled only its own cleanup.
After the fixture removed the newer VM, the older controls remained. Keyboard
Tab moved from the newer VM link to that VM's Passed and Failed controls. A
synthetic run-inventory HTTP 503 showed **Test restore history unavailable**
and withheld Verify/Cleanup controls. A delayed empty catalog showed Loading,
then **No recovery points yet**. At 390 pixels the document did not overflow.
The reviewer inspected temporary local desktop/mobile and loading/empty/error
captures; raw captures contain disposable identifiers and are not tracked.
Web typecheck, full tests, build and repository privacy checks passed after
the final grouping edit. No backup, restore, verification or cleanup POST was
submitted in this rewalk; the multiple-test-VM observations are fixture UI
behavior, not native multi-test or cleanup proof.

## Native first-use TPM refusal and pending retry

The disposable member's first changed-binary UEFI/vTPM restore stopped before
boot because libvirt's `/var/lib/libvirt/swtpm` root was absent. The native
restore journal retained `allocating`; the run stayed `needs_attention` and
the target and point pin were preserved. This is a native failure observation,
not a completed UEFI restore. The subsequent source correction creates only
that root after ancestor validation and allows exact reserved repair of an
empty owned 0700 root. Independent review found and the owner corrected a
chmod-before-ancestor-check ordering defect in the first draft. Focused tests
cover first creation, symlink-parent refusal, empty repair and nonempty
refusal; full affected Go packages passed as reported by code owners. The
v5 native exact-operation resume was pending at this checkpoint. A later v10
attempt reached a second compatibility gate: its old pending pre-copy journal
had a null disk chain, which the current reader refused before binding.
The following bounded v11 outcome supersedes that retry status.

On disposable Core3, restarting the changed Core service reconciled the
original v5 restore operation without a new restore POST. The native journal
reached `defined`/`succeeded`, the master BackupRun reached
`registered`/`succeeded`, and the Controller's Core-backed backup-runs GET
showed success. The domain was shut off with libvirt's default TPM UUID
tree. Read-only checks found the disk marker, NVRAM and TPM permall hashes
matching the captured artifacts before start. An isolated start reached UEFI
firmware using swtpm at that tree; NVRAM and TPM state changed after boot,
while the disk marker remained intact. The disposable disk contains no guest
OS or agent, so OS boot, guest data access and TPM-sealed application-data
recovery were not proven. ACPI shutdown timed out and the exact disposable
VM was stopped with libvirt. The generic Controller operation row still
showed `needs_attention` at this checkpoint despite Core run success; a
separate Controller result projection correction was pending. Full Phase 6
acceptance remains open.
The Controller owner traced that stale row to report handling that accepted
late ZFS results but not late backup results. The source correction permits
only a later authenticated terminal backup Result from the same host and
operation ID; a backup inventory projection cannot settle the task. Focused
`TestBackupResultRecoversControllerAttention` failed before and passed after
the fix. Core now keeps reporting the Controller-owned backup Journal outcome
after Result ACK and suppresses a same-ID native VM-create outcome, while
excluding member peer work. `TestAcknowledgedBackupRestoreOutcomesRemainInDurableInventory`
and `TestBackupOutcomeRecoversAcknowledgedControllerAttention` passed after
the correction. `TestAcknowledgedBackupMetadataOutcomesRemainInDurableInventory`
passed after Core inventory mapping covered repository, policy, protect,
catalog and test metadata kinds. Controller's own-operation lookup now binds
host and kind, with a red/green ID-collision regression.
`TestBackupMetadataOutcomeRecoversAcknowledgedControllerAttention` passed for
test-verification catch-up after ACK. This paragraph records the source-check
checkpoint; the following disposable rerun supersedes its pending status.
In the v12 changed-binary disposable rerun, Controller C's historical v5
restore task changed from `needs_attention` to `succeeded` through the
recurring authenticated Core outcome. No database edit or new restore POST
was made. Core2 and Core3 were fresh, and all 14 nested VMs were stopped at
the final checkpoint. The project-wide `go test ./...` passed and the Python
suite passed 553 tests with one skip, as reported by the primary operator.
This establishes catch-up for that historical operation; it does not exercise
native Verify/Cleanup, a promoted-master replay, near-boundary authority
transfer, a complete three-host recovery matrix, physical hosts or guest OS
and TPM-sealed application-data recovery. Phase 6 candidate acceptance stays open.
The native retry exercised first-use TPM root creation, legacy null-chain
normalization and exact binder. A crash between root Mkdir and chmod that
leaves an empty 0700 root was covered only by a focused unit test, not native
fault injection. `TestReadLegacyAllocatingRestoreWithReservedEmptyDiskChain`,
`TestBindLegacyRestoreAllocationRequiresCompleteExistingArtifacts` and
`TestBackupTPMRootAccessibleCreatesOnlyMissingNativeRoot` passed along with
full affected Go packages, privacy and diff checks as reported by vmhost.

## Test restore pin and promoted cleanup corrections

Independent review found that member peer settlement could clear a successful
test restore's point pin while the test VM still existed. The corrected
settlement retains it until exact cleanup. A periodic Core pass now clears
ordinary terminal restore pins whose release was interrupted, without
clearing successful test or uncertain pins. Focused member-kind and restart
pin tests passed as reported by the owners. This is source/test evidence;
native member restore plus retention was not rerun.

A promoted master may inherit a pending test-cleanup receipt without the
former master's journal. It now rehydrates only a command whose exact intent
hash matches one successful test run, then uses the existing authenticated
owner and native creator checks. A mismatch leaves the pin and receipt.
`TestPromotedMasterRecoversOnlyExactPendingTestCleanup` passed after the
correction and checks that the recovered journal is marked internal rather
than reported as the former master's Controller operation. No native
promotion/cleanup replay is claimed. A subsequent
reconcile result now reports a fresh native error when available instead of
always repeating an older attention reason.

## Bounded native member test restore and peer status correction

An earlier transient member peer response was reported as a different
operation. Its raw reply was not captured. Source review found that a terminal
member run with a lagging command-journal Result was treated as a permanent
status mismatch; this is an inference consistent with the observation, not
wire proof. The correction keeps exact operation, intent, pool, member host,
repository, point and target checks permanent, and retries only the expected
journal lag. `TestPeerTestRestoreWaitsForJournalToCatchUpWithRun` was red
before and green after the fix as reported by the owner. An independent run
of it and `TestPeerRestoreReplyRejectsSameOperationDifferentTarget` passed.
The owner reported full agent/protocol, privacy and diff checks passing; the
primary operator reported a final project-wide `go test ./...` pass.

In a separate disposable Core3 BIOS test, the test-restore run succeeded and
the exact recovery-point pin and VM creator record were present. The
operator inspected the isolated boot screen, which visibly showed no
bootable device, and recorded Failed. The Verify task and durable receipt
succeeded, and the catalog retained Failed with the test VM identity. The
operator entered that exact VM ID for Cleanup. Its task and receipt and the
native disk-deleting operation succeeded. Read-only final checks found the
domain, copied disk and firmware absent, and the Core2 master owner row and
exact point pin absent. Existing VMs were untouched and shut off; the
disposable lab was then shut down. The screenshot and evidence JSON were
kept outside tracked documentation because they contain lab identifiers.
This establishes the one-member Failed Verify and typed Cleanup path. It
does not prove Passed, multiple native test VMs, interrupted cleanup,
promotion recovery, near-capacity authority transfer, guest OS boot or
TPM-sealed data recovery. Phase 6 candidate acceptance remains open.

## Catalog-bound restore source correction

The original review's selected-point finding remained partial after repository
owner and internal payload verification: a complete valid manifest could
replace the bytes under the same point ID without matching the master's
catalog digest. The current source pins that digest in the durable restore
run. Local as-new, test and replace compare run, current catalog and repository
manifest before native effects. The authenticated member restore POST carries
the master catalog point; a clean member persists its digest, and GET/POST
return it from the retained run for master comparison. Missing or changed
catalog entries, unbound legacy runs and members without the new route fail
closed, retaining unresolved pin protection.

An independent focused run passed
`TestRestoreManifestMustMatchMasterCatalogDigest`,
`TestCleanMemberRestorePeerPersistsMasterCatalogDigest` for all three restore
kinds, `TestRestorePeerRefusesOlderMemberWithoutCatalogRoute`,
`TestRestoreReplaceRefusesChangedValidCatalogManifestBeforeCopy` and
`TestPoolBackupAuthorityPagesRetainRestoreManifestDigest`. Peer API contract
tests passed 74/74 against the new route and optional legacy BackupRun field.
No pre-fix red run, changed-binary native payload-tamper run or interrupted
member retry has been observed for this correction. The earlier successful
native restore does not prove this catalog binding; Phase 6 candidate
acceptance remains open.

## UI retry source audit after the browser walkthrough

The UI owner reviewed repository-create and restore mounted-flow retry
handling and added focused source assertions for held request IDs and frozen
or reopened drafts; neither flow required a production code change. Policy
create did have a lifecycle defect: leaving Policies after an uncertain
submission discarded its local draft while the parent retained the request
ID. The policy panel now remains mounted with an active flag, preserving the
original body for exact retry while rendering no off-tab panel or listeners.
The expanded policy test failed before that correction and passed afterward,
as reported by the owner. Full web typecheck, tests, build and privacy passed.
No browser rewalk exercised this exact tab switch after the correction; the
earlier Controller C browser evidence remains scoped to its tested UI build.
