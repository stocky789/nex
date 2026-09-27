# Recovery delivery: snapshots, backups and restores

Date: 2026-09-25; checkpoint updated 2026-09-27 (Phase 6 disposable close). Status: **Phase 5
closed for disposable virtual-lab scope (2026-09-27).** S0–S3 accepted within that
recorded lab boundary. Close record:
[docs/qa/2026-09-26-phase5-gate/CLOSED.md](../qa/2026-09-26-phase5-gate/CLOSED.md).
**Phase 6 closed for disposable virtual-lab scope (2026-09-27).** Close record:
[docs/qa/2026-09-27-phase6-gate/CLOSED.md](../qa/2026-09-27-phase6-gate/CLOSED.md).
Physical/production certification is not claimed. Owner: project owner.
The 2026-09-27 close decision remains a historical disposable-lab decision. A
[subsequent staged-code review](../qa/2026-09-27-phase6-post-close-review.md)
identified unresolved data-safety and verification gaps in the current worktree;
the candidate is under correction and has not been reaccepted after those changes.
For this changed candidate, the Phase 6 acceptance gate is **open** pending
independent correction review, interrupted-request and native restore reruns,
and proof that promotion preserves complete retained history as the replica
grows. The bounded Campaign A/B and browser results remain evidence for their
tested builds; they do not certify these later edits.
The current paged authority refuses more than 64 MiB or 1024 pages without
truncating records. The encoder's packing rule means the 64 MiB byte bound
arrives before the page-count bound for valid records. The new admission
guard refuses growth that would cross the bound, preserving a previously
complete replica. An older oversized history, or a failed replica push,
still prevents complete-history takeover. Phase 6 stays
open for a near-boundary transfer/promotion check and a product decision on
history capacity and continued takeover beyond it. Current Core source adds
master-side capacity preflight for new backup metadata and reserves room for
pending operation outcomes. A focused sparse-page assertion passed; near-boundary
native transfer/promotion proof remains open.
Test-result recording now requires the exact successful restore creator and
current native VM identity on the owner host. The current master obtains
authenticated member proof when needed. Passed requires intact current
artifacts; Failed can document damaged artifacts with creator and definition
proven. Cleanup likewise binds native deletion and settlement to that origin.
Focused source tests passed. A later disposable Core3 BIOS test restore
completed with its point pin and creator record. The inspected isolated boot
screen showed no bootable device; the operator recorded Failed, then Verify
and typed-ID Cleanup receipts succeeded. The native VM, disk and firmware,
master's exact owner row and point pin were absent after cleanup. This covers
one Failed member test-restore workflow. Passed verification, interrupted
member proof/cleanup and multiple native test VMs remain open.
The executing Core can now resume only its exact retained native `allocating`
restore with the same operation and target; later ambiguous native stages and
inherited promoted-master runs remain observation-only. Focused test evidence
passed. New allocation journals bind the immutable point digest and disks;
older unbound journals require full local and placed artifact proof under the
native lock before binding. The disposable Core3 v5 native resume is still open.
Successful test restores keep their recovery-point pin through proven cleanup,
including member execution. A promoted master now rehydrates an inherited
pending cleanup only from one exact successful test run and receipt hash;
terminal ordinary pin release is reconciled after an interrupted clear.
Focused tests passed, and the single native member test restore retained its
point pin until typed-ID cleanup. Retention/prune and promotion cleanup remain
open acceptance work.
The original native v5 restore succeeded in Core. Its Controller task row
still showed `needs_attention` at the earlier checkpoint. A source correction
lets a later same-host backup Result or recurring durable Core backup outcome
settle only that Controller-owned nonterminal task row. Focused red/green and
catch-up tests passed. In the disposable v12 changed-binary rerun, Controller C
settled that historical row to `succeeded` from the recurring authenticated
outcome without a database edit or new restore POST. This closes that task
projection defect, not the remaining Phase 6 acceptance gates.
The current candidate now binds each new restore run to the selected master's
catalog manifest digest. Local as-new, test and replace execution compare that
digest with the repository manifest before native effects. Member restores
receive the catalog point over an authenticated Core peer route, retain the
digest in their run and return it for exact GET retry; the master checks it
before settlement. Legacy unbound runs, changed points and old members fail
closed with unresolved protection retained. Focused source tests passed;
changed-binary native tamper and interrupted member retry checks remain open.
An empty signed catalog with repositories now leaves promotion at a durable
`catalog-rebuild` stage after the master record commits. Core reports an import
error as `needs_attention` and retries catalog import before re-pointing members;
it does not activate a second master or roll back the committed authority.
Focused source and failure-injection tests passed, while the native promotion
rerun remains part of the open gate.
Implementation index:
[docs/qa/2026-09-27-phase6-implementation-index.md](../qa/2026-09-27-phase6-implementation-index.md).

**Resume here:** [2026-09-26 session handoff](2026-09-26-recovery-session-handoff.md).
Use that page for resume order, lab notes and what not to re-open.

Parent: [platform plan, Phase 5](2026-09-08-nex-platform.md#5-snapshots-and-rollback) and
[Phase 6](2026-09-08-nex-platform.md#6-backups-scheduling-and-restoration).
Design authority: [console design guide](../design/controller-console/README.md) and its
[reference](../design/controller-console/reference.html).

## Outcome and scope

An operator can make a rollback point before changing a VM, arrange independent backups,
find a usable recovery point, and restore it confidently through Nex Controller. The interface
must explain what is protected, what will be lost, what is happening, and the next useful action.
Normal recovery work must not require knowledge of image chains, libvirt XML, or host shell commands.

The project owner's request of 2026-09-25 makes ease of use, coherent UI design, simplicity and
reliability explicit acceptance requirements. **Scope confirmed by project owner, 2026-09-25:**
cover Phases 5–6 together, including snapshots, backups, scheduling and verified restores, with
separate completion gates. Delivery remains **Phase 5 snapshots first, then Phase 6 backups and
restoration**.
It does not merge or renumber phases, authorize implementation ahead of dependencies, or claim
that snapshot completion closes backup/restore work. Implementation follows review of this plan.

**Core ownership decision, project owner, 2026-09-25:** this plan supersedes the earlier
Controller backup exception. The pool must schedule, execute and verify backups/restores without
Controller. A fresh Controller connects to the pool master and adopts all members, existing job
definitions, schedules, retained previous runs and recovery points through authenticated APIs.
The old Controller's database, export, CA and job configuration are not prerequisites. The
[three-host campaign below](#mandatory-three-host-continuity-and-adoption-campaign) is a blocking
acceptance gate, including loss of the host carrying both Controller and the master role.

| Included | Completion requirement |
|---|---|
| Phase 5 | Create/list/name/describe/revert/delete disk snapshots; complete disk/configuration/firmware coverage; stopped guests first; validated running capture; durable consolidation and recovery; usable UI and guide |
| Phase 6 | Independent NFS backup destination; complete stopped and online full backups; policies, scheduling, retention and limits; restore as new and explicit replacement; manual test restore; catalog rebuild and Controller recovery; usable UI and guide |
| Required integration | VM power/edit/delete, storage removal, pool authority/transfer, permissions, capabilities, task history, reconnection and Controller replacement |
| Later, excluded | Memory snapshots, incremental/CBT chains, deduplication, storage-native snapshot engines, replication, instant/live restore, file-level restore, automated scheduled boot drills, migration, HA and container backup |

Existing Phase 3/4 disposable-lab acceptance and its hardware limits remain as recorded in the
[handoff](2026-09-14-production-readiness-handoff.md) and
[Phase 4 closure](2026-09-19-phase4-closure.md). Recheck the prerequisites used by this feature on
the actual candidate build; historical evidence does not certify a new snapshot or restore path.

## What exists and what must change

This baseline was inspected in the source on 2026-09-25; no native recovery experiment or browser
walkthrough was run for this planning change.

| Existing source | Reuse and necessary integration |
|---|---|
| `internal/vmhost/managed.go`: `managedJournal`, `Execute`, `Applied`, `reconcile` | Durable stages, exact request replay and observed results; extend for recovery operations |
| Same file: `managedDiskPlan`, `managedCurrent`, `managedDomainXML`, `observeDefinition`, `managedDeleteDiskPaths` | Current qcow2 paths and retained disks assume the existing disk lifecycle; make every consumer understand active heads and referenced backing files before enabling snapshots |
| `internal/agent/pool_vm.go`: `poolVMRecord`, `PendingUpdate`, `AdmitPoolVMExecution` | Master authority, ownership epochs, exact definition reservations; rollback must commit a proven definition transition |
| `internal/agent/pool_vm_inventory.go`, `internal/controller/managed.go`: `adoptManaged` | Member reporting, master records and fresh-Controller adoption; extend with recovery metadata and explicit deletion evidence |
| `internal/protocol/protocol.go`: `Command`, `Result`, `ManagedState` | Existing transport, inventory and outcome conventions; recovery payloads and capabilities are new |
| `internal/vmhost/firmware.go` and managed firmware handling | Existing UEFI/NVRAM/Secure Boot/vTPM validation; recovery must capture matching state or refuse before effects |
| `appliance/recovery.py` | Existing quiesced Controller export/restore, lock, checksums and identity exclusion; extend and verify this path |
| `internal/localcore/service.go`, `recover.go` | Current deployment refuses enrolled hosts/existing deployments; add the bounded, authenticated appliance retire/redeploy path needed for replacement without resetting Core or pool state |
| `web/src/vm-panel.tsx`, `vm-list.tsx`, `gated.tsx`, `rail-screens.tsx`, `settings.tsx`, `overview.tsx` | Replace relevant phase placeholders; keep existing navigation and VM context |
| `web/src/ui.tsx`, `managed.tsx`, `tokens.css` | Reuse panels, tables, badges, warnings, dialogs, errors, request retry and design tokens |

No implemented VM snapshot/backup contract was found. Controller export is not independent VM
backup. Read relevant bug history before implementation, especially firmware/state cleanup,
uncertain operation results, storage identity, reservation release and replacement adoption in
[BUG_LEDGER.md](../BUG_LEDGER.md). In particular, revisit BUG-0196, BUG-0214 and BUG-0220.

## Simplicity and reliability rules

1. Use the installed libvirt/QEMU engines and existing command/journal paths. No new backup
   service, queue broker, general workflow framework, storage plugin layer, or custom image format.
2. Start with complete independent images and one versioned manifest per recovery point. Each
   retained backup must restore without the original disks, their snapshot chain or Controller DB.
3. Core validates ownership, disks, firmware, storage identity, capacity and every destructive step.
   Core also owns schedules, job execution and history. Controller authenticates the operator,
   submits API requests and renders Core observations; no backup scheduler or job engine runs there.
4. Reserve the affected VM and storage dependencies durably. Serialize conflicting operations.
   Long copies must not hold the current host-wide managed-engine lock and block unrelated VMs;
   keep broad lock sections short and use a bounded native operation outside them.
5. A timeout means the outcome needs observation. Keep the original operation identity, inspect
   native state, and reconcile before retry. Never unlock ownership just because a connection died.
6. Publish complete backups only after validating all disks, required state and the manifest.
   Partial files are never restorable. Keep the previous good backup when a new run fails.
7. New restore is stopped and disconnected by default. Replacement retains the original disk set
   until the restored guest is validated. Never enable two writable owners of the same VM.
8. Capability support comes from a tested format/backend/version matrix. Refuse unsupported
   combinations with a useful reason before modification; a generic capability boolean is insufficient.
9. Keep operator choices few. Use normal form controls and readable presets, with advanced limits
   behind a disclosure. Do not hide data coverage, downtime, deletion effects or network isolation.
10. No native support, recovery claim, performance promise or phase closure without actual evidence.
    A limitation can be documented, but a required untested workflow still leaves its gate open.

## UX contract

### Operator and visual context

Design for an infrastructure operator arranging routine protection and the same operator restoring
under time pressure after data loss. Keep the existing system-following light/dark theme, restrained
status colours, system fonts, rail/top bar, filterable lists and slide-over details. Preserve
2 px controls, 3 px panels, visible focus and no coloured side stripes. Use the approved reference
as the visual anchor; this work adds workflows within that design rather than a new visual system.

The primary recovery action is **choose a recovery point, review its destination and consequences,
then restore safely**. Routine backup management must also make failures and unprotected VMs easy
to find. Design the flows before implementation and review real rendered screens in each slice.
Wireframes and fixtures establish design intent only; functional acceptance remains Core-first.

### Information architecture

| Entry point | Contents and main actions |
|---|---|
| VM panel → Snapshots | Newest-first snapshot list, name/description, capture time, disk coverage, consistency and lifecycle status; Take snapshot, Revert, Delete |
| VM panel → Overview | Last successful backup and last tested restore as separate facts; Back up now and View backups links scoped to this VM |
| VM list → Protection filter | Find No policy, Never backed up, Schedule paused and Failed/missed runs, including VMs with no recovery points; link to reviewed policy setup |
| Backups → Recovery points | Search/filter by VM, repository, date and condition; include deleted or unavailable source VMs; Restore and Test restore |
| Backups → Policies | VM scope, repository, capture mode, schedule/time zone, next run, last result; Create policy, Run now, Edit, Pause schedule |
| Backups → Run history | Per-run and per-VM outcomes, stage, timing and error; link to recovery point, VM and existing Tasks |
| Settings → Backup repositories | Single registration/edit workflow, capacity, per-host access and last check; Test access and Import recovery points |
| Existing Overview/Tasks/Events | Failed/missed backups, low space and untested restores linked to the relevant object; ongoing tasks remain visible after navigation |

Use the existing VM slide-over and a wider form within it where needed. Use existing dialogs for
destructive confirmation, not nested dialogs for whole workflows. Preserve list filters and
selection when a panel closes. A VM link to Backups preselects the VM filter; Restore also works
when that source VM no longer exists. Repository editing has one implementation shared by links.

Protection labels use actual Core policy and schedule/run evidence, with Unknown when
that evidence is unavailable. Show policy coverage and successful-copy history separately; having
a policy does not prove a backup exists. Do not invent an aggregate protection score.

Illustrative structure, without fabricated product data:

```text
Backups                                 Back up now   Create policy
Recovery points | Policies | Run history
Search / VM / repository / date / condition

VM       Captured       Repository   Integrity   Restore test   Actions
…        …              …            …           …              Restore

Selected recovery point → details panel
  Source VM and capture time / all disks / consistency / firmware
  Integrity result / last restore test / repository availability
  Restore as new VM   Test restore   Protect from deletion
```

### Snapshot workflows

- **Take snapshot:** choose the VM → name and optional description → review Core preflight and
  submit. Offer a readable timestamp-based name. Include every supported writable disk; do not
  offer arbitrary disk exclusions. Show whether shutdown is required and link to graceful shutdown.
  Never force-stop implicitly. A stopped VM whose clean shutdown is unproven is not labelled clean.
- **Revert:** select a snapshot → show capture time, VM, matching disk/configuration/firmware set,
  changes discarded and resulting stopped state → explicit target confirmation → tracked revert.
  Revalidate the selected point and VM revision at submission. Do not silently start afterward.
- **Delete:** explain which rollback point disappears and whether consolidation is needed → confirm
  → show Deleting/Consolidating until Core verifies completion. Never remove the row optimistically.
- **Running capture:** show the supported choice and actual result, Crash-consistent or
  Guest-quiesced, with a short explanation. A requested quiesced capture must not silently downgrade.
  Filesystem quiescing does not establish application consistency.
- **Bulk snapshot:** reuse the same per-VM preflight and command path, one review with exclusions
  and individual results. Do not imply atomic capture across VMs. Account for the existing bulk
  placeholder once Phase 5 closed, rather than leaving a dead control with an obsolete phase label.

Explain once, close to the snapshot list: snapshots depend on the VM's storage and do not protect
against losing it. Keep deeper chain details in an About disclosure or diagnostic detail.

### Backup setup and daily operation

**Register a destination:** Settings → Backup repositories → name and external NFS destination →
test access → review/save. Core discovers/checks host access, destination identity, permissions
and capacity. Reuse existing NFS discovery where it fits. Show which hosts can write and restore.
Reject a destination that is demonstrably the source storage; where physical independence cannot
be proved, require the operator to identify the separate failure domain and record that attestation
without labelling it machine-verified. Explain storage-layer encryption and separate key custody.

**Back up now:** select VM(s) and a repository → review disk coverage, capture mode, blockers and
downtime → submit. A sole eligible destination can be preselected but is always visible. No first-run
guessing of storage. A failure affects that VM's result; a multi-VM run reports counts and individual
outcomes. A partially copied multi-disk VM yields no usable recovery point.

The stopped-only mode requires an already stopped guest and offers the existing graceful-shutdown
action when needed; it never powers off a running guest automatically. After online support lands,
suggest Require guest quiescing for running guests and explain missing prerequisites. Crash-consistent
capture requires an explicit reviewed choice. Preserve the requested mode and actual capture result.

**Create or edit policy:** at most three named steps after repository registration:

1. **Coverage:** choose named VMs and repository; show all included disks and explicit exclusions.
   Start with explicit VM selection; new VMs are not silently included. Display any per-VM override.
2. **Timing and retention:** daily/weekly/custom day-and-time controls, explicit IANA time zone,
   readable next occurrences, supported capture mode and downtime. Default retention suggestion:
   keep the latest seven successful points per VM. Optional age protection retains all points
   newer than a chosen age. Expose bandwidth, concurrency and per-VM overrides under Advanced.
3. **Review:** show effective coverage, next run, retention rule, current deletion eligibility,
   protected points and exceptions. Saving an enabled schedule is an explicit choice; show
   the master-confirmed policy/schedule revision or an actionable error. A lost reply is reconciled
   through the same Core operation; the browser must not construct a second schedule.

Presets fill visible fields; they do not hide a different scheduler or retention rule. For retention,
keep the union of the newest N successful points, points younger than the configured age, protected
points and active-restore references. Only consider the remainder for pruning after a new successful
backup of that VM to that destination. Core enforces these rules and rechecks at deletion time.
One-off points without a retention policy remain until explicit deletion. Explain actual eligible
items, never just “cleanup enabled”. Deleting a policy or repository registration never deletes data.

Default to one active data job per executing host and repository, with bounded configurable limits.
Do not overlap disk mutations on one VM. Pause schedule prevents future dispatch and leaves the
active run visible. Run now creates one identified run, without changing its next scheduled occurrence.
An unavailable guest, busy VM or full repository must yield a named missed/skipped/failed result.

### Restore and verification

**Restore as new VM:** recovery point → target → review/submit, at most three steps after selecting
the point. Default a distinct name, new UUID/MACs, stopped state and disconnected NICs. Choose target
pool, a compatible host and storage for every disk; offer a single “Use this storage for all disks”
choice with individual mappings in Advanced. The Core preflight must explain disabled targets.
Cross-pool restore maps resources explicitly rather than trusting source resource IDs.

The review shows source time/zone, included disks, firmware, integrity results, destination space,
network state and unsupported/missing dependencies. A new MAC does not remove static guest IPs,
hostnames or application identities; explain that before the operator connects a restored guest.
Missing install media may be detached only with an explicit reviewed mapping; never silently change
required boot or firmware state. Successful restore means a complete stopped VM is registered;
offer Open VM and an explicit isolated boot/validation next step.

**Replace existing VM:** an explicit alternate action, never the default. Select the exact original
VM; show discarded state and preserved original disks; require confirmation naming the target.
Core proves it is stopped/excluded and owns the replacement transition. Restore into new files,
validate first, then switch the definition while preventing the old owner from writing. Preserve
the prior disk/configuration set until validation; provide an explicit recover-original action with
the same exclusion checks. Final removal of retained data is a separate confirmed operation.

**Test restore:** reuse restore-as-new with isolation retained. Show a short checklist: start the
test VM on the isolated network, confirm boot, verify files and application data, record result,
then remove the test VM explicitly. Never automatically connect it to production or delete it before
results are captured. Keep a visible link to test VMs awaiting cleanup. A manually entered result
names its operator, method and time; distinguish it from machine-observed checks.

Display three separate facts: **Backup complete**, **Integrity checked**, **Restore tested**.
Each names its time and method, or Never checked/tested. Boot alone is not application verification.
Store the result against the exact immutable recovery point. The dashboard must not turn a completed
copy into a green “recoverable” promise. Measure backup/restore time in the lab; no inferred RPO/RTO.

### Feedback, accessibility and errors

| State | Required experience |
|---|---|
| First use / no matches | One relevant next action; distinguish no repository, no points, no matches and unsupported Core |
| Loading | Stable layout and preserved input; no invented rows, capacity or percentage |
| Form error | Plain field error and focused summary; preserve all safe selections; do not echo credentials |
| Stale/offline | Last observation timestamp, unknown current state, specific mutation blocker; never infer VM shutdown |
| Running | Core stage and observed bytes when available; indeterminate progress otherwise; task survives navigation |
| Lost reply | “Checking the previous request”; reconcile the same operation before offering safe retry |
| Failed / partial | Affected VM/repository, stage, reason and one actionable recovery link; individual multi-VM results |
| Cancellation | Offer only tested backend cancellation; explain safe stopping boundary and retained artifacts |
| Complete | Link to snapshot/recovery point/restored VM and the next verification action |

Do not inherit generic Tasks advice to “send it again” for unknown snapshot, consolidation or restore
outcomes (`resubmitLink` in `web/src/rail-screens.tsx`). Offer reconciliation first. Errors must be
available to keyboard/touch users, not only as hover tooltips. Progress announcements must not move
focus or repeatedly interrupt screen-reader navigation.

All primary flows work by keyboard; dialogs contain and restore focus; tabs, labels, errors and
icon actions have accessible names; status includes text; both themes retain readable contrast.
At 1440×900, 1024×768 and 390×844, show the critical target and destructive consequence without
page-wide overflow. Filters collapse and panels become full width on narrow screens. Honour reduced
motion. Use existing components and dependencies; no new wizard framework.

## Durable state and operation model

The following are planned records, not existing implemented schemas. Confirm field placement in S0
against the current code; a revised file choice must preserve the same authority and adoption path.

| State | Authoritative durable record and recovery path |
|---|---|
| Snapshot identity/name/description, disk references, captured spec/firmware, lifecycle and reservation | Extend the master `pool-vms.json` VM recovery section with compact descriptors and operation references; executing Core managed journals and immutable capture metadata retain native detail. Include required authority in member replication/transfer. Fresh Controller reads master recovery inventory, without recreating identities |
| Active disk heads and native operation progress | Executing Core managed journal, exact libvirt definition and observed chain; master holds the matching committed VM revision and unresolved reservation. Member evidence settles the master transition before it is advertised usable |
| Backup destination identity/name, access configuration and policy | Master `pool-backup-repositories.json` and `pool-backup-policies.json`, with secret receipts using existing Core credential mechanisms. Replicate with pool authority and publish inventory for adoption; host access remains observed per host |
| Complete backup payload and protection against pruning | Repository recovery-point directory with versioned manifest, complete disk images, required firmware/vTPM payload and deletion-protection metadata. Core serializes protection/prune/restore pins. Fresh catalog scans validated manifests; missing Controller history never authorizes deletion |
| Schedules/time zones and job definitions | Master `pool-backup-policies.json` owns stable job/schedule IDs, enabled/paused state, schedule rules, time zones and next/last dispatch records; include in pool authority replication/transfer and expose through paginated Core inventory |
| Backup/restore runs and test-run history | Master `pool-backup-runs/` owns durable per-run/per-VM records, occurrence IDs, policy revisions, stages, outcomes and verification results; executing Core journals supply native evidence. Replicate retained records and unresolved work for master takeover; fresh Controller reads the same IDs/history through Core APIs |
| Recovery-point catalog | Master `pool-backup-catalog/` indexes repository manifests and annotations; repository remains authoritative for completed payloads. Core validates/rebuilds this index and exposes it through inventory; Controller stores no authoritative catalog |

Pool policy includes VM selection, repository, capture requirements, retention, limits, per-VM
overrides and schedules. Existing job definitions include enabled and paused jobs; previous jobs
include retained successful, failed, cancelled and missed runs, plus retained deleted-policy history.
Historical actor references remain historical data when the old Controller's accounts no longer
exist; they never grant access in the replacement Controller. Normal pool history retention applies
equally before/after adoption and is visible; Controller replacement must not truncate it. Data
already deliberately expired under that policy is not resurrected. Define and test bounded history
retention in S6, retaining every active record and each retained point's verification provenance.

All of this is host/repository state. Controller-owned sites, accounts/roles, audit history,
enrollment tokens and CA still have their separate export/restore path. Rebuilding that identity
does not require restoring it to recover the pool's backup jobs or history. No Controller-only
snapshot name, mapping, schedule or job record may become authoritative.

Do not embed disk images, large native XML histories or the whole backup catalog in pool authority
replicas as unbounded inline data. Extend the existing authority transfer with a bounded, verified
transfer of retained backup run records and catalog references; an acknowledged copy must cover
the committed job/schedule/run state. Measure replica/journal size limits and pagination before
publishing support limits; fail visibly before exceeding them. Transfer must carry snapshot authority or
refuse while a required dependency cannot be transferred. A local disk does not become shared.

Use journalled transitions around native effects: **preflight → reserved → capturing/copying →
validating → published/committed → cleanup → terminal result**. These are operation-specific stages,
not a new generic engine. Persist expected before/after identities before effects. Reconcile after
Core/libvirt restart and before releasing reservations. Keep terminal receipts for exact retries.

For snapshots, the chain record must cover every active head and retained parent, the full writable
disk set, captured VM specification and required firmware state. Revert commits a fresh current VM
revision containing the captured execution configuration; it must not roll back the master epoch,
membership, identity or journal history. Keep the current VM name, description and tags; restore
captured disk, hardware and firmware configuration, and show that precise difference in the review.
Delete/consolidate only files proven unreferenced after native completion and domain verification.
Unsupported branching must be refused with an explanation; never silently delete descendant points.

S2 starts with stopped revert. Its new operation ID is distinct from the retained snapshot ID.
The immutable owner capture journal supplies the captured VM specification; the master verifies
its canonical specification/disk hashes and dependencies against its own settled descriptor.
It independently derives the next VM revision while preserving current identity, name,
description, tags and autostart. The reservation binds the exact target descriptor and expected
result hash; completion requires observed disk/configuration and paired firmware evidence.
Keep the original create receipt immutable and record revert as a separate lifecycle journal.
Do not copy every full captured specification into the authority catalog merely to validate it.

**S2 API impact: new contract** for authenticated owner-local
`POST /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}/revert` and
`POST /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}/delete` with `operation_id`;
**changed contract** for target-bound recovery reservation/evidence and reported
revert/delete outcomes. Leaf delete uses the S0 public sequence (no-delete blockcommit,
metadata-only snapshot-delete, reference scan, unlink of proven-unreferenced overlays) and
refuses points with native children before effects. These are under integration, not accepted
operator controls. Core keeps the new operation in its existing managed journals and the master
`pool-vms.json` reservation/settlement record; signed authority replication and authenticated
inventory retain the fresh-Controller adoption path. Controller only adopts reported outcomes.
Matching development versions, refusal of stale or changed targets, exact retries, interrupted
effects and Controller disconnection must be verified before acceptance. Guide/API references
and the native walkthrough ship in this slice.

The S2 lab also exposed a failed-start preflight leaving the master in `starting` (BUG-0644).
Its correction is a **changed Core peer contract**: bind the admitted operation ID to the
existing execution epoch, then release stopped reservations only on exact definitive failure
and fresh stopped evidence. Fresh owner observation can confirm running execution while
preserving an uncertain task outcome. Automatic-start journals record the native-dispatch
boundary; legacy unknown outcomes remain conservative. The master retains
`pending_start_operation_id` in `pool-vms.json` and signed
replicas; a replacement Controller obtains the resulting state through normal inventory.
Legacy reservations without that binding remain conservative. Regression, restart and
native failure walkthroughs are required before accepting the correction.

The r9 lab candidate, `0.3.6~recovery.snapshot8`, passed the full Go suite,
the focused independent start-recovery review, and all 34 privileged snapshot tests
without skips. Its matched Controller and three Core binaries are deployed in the
disposable lab with unchanged journals and guest disk/Secure Boot/vTPM observations.
The binary/source-only upgrade leaves the installed Debian package version unchanged.
The first managed-revert walkthrough then reproduced BUG-0647: native revert ran once,
but definition/metadata validation left the stopped VM reserved as `needs_attention`.
The exact operation and evidence are preserved for correction and reconciliation;
the [native failure record](../qa/2026-09-26-stopped-revert-native-validation.md)
distinguishes the observed disk effect from the unperformed firmware restoration.
Managed-revert acceptance and the corrected failed-start walkthrough remain outstanding.

For backup publication, write to a unique staging directory on the destination, sync and validate
all required material, then publish with the native filesystem's tested atomic boundary. Manifest
contains format version, recovery-point identity, source VM identity/configuration, capture time and
consistency, every disk/format/size/checksum, required firmware/vTPM state, media requirements and
completion evidence. Enforce bounds, safe relative paths and supported versions when reading it;
reject traversal, links, missing components and conflicting identities. Do not execute imported XML
or commands. Reconstruct a validated Nex VM definition. Checksums detect corruption, not authenticity
against an attacker who can replace both data and manifest; repository trust remains explicit.

Completed data is immutable; protection and verification annotations are separate metadata with
serialized updates. Interrupted cleanup retains diagnosable artifacts and never promotes partial
data. Prune rechecks policy, protection and restore pins under one Core-controlled deletion boundary.
Restore pins and exclusion must remain effective across Controller restart, catalog rebuild and
master transfer. Reject concurrent writable management by a second Controller; a copied database or
expired timer cannot prove exclusive ownership.

Each repository has one owning pool for writes and pruning, identified by a durable repository
marker; reject another pool's writable registration of that identity, including source aliases.
A cross-pool restore obtains a durable pin from the repository's owning master before destination
Core reads data, and releases it only after the destination outcome is reconciled. Include pins
in owner authority transfer; an absent destination or lost reply never proves a pin can be cleared.
If the owning pool is lost, the recovery command must first establish a read-only recovery mode
with prior writers/pruners excluded, or adopt repository authority through a verified exclusive
handover. Do not introduce multi-master repository writes. S0 must prove the identity/exclusion
mechanism on the selected NFS service; no private per-master lock can substitute for it.

One authoritative master schedules each pool. Persist a unique schedule-occurrence/VM operation
identity before dispatch. The executing host reports to its pool master independently of Controller.
Replicate committed scheduling state and retained run history before acknowledging their safe
takeover; do not silently discard a policy or successful run after master loss. A new master
reconciles member journals and repository publication before resuming an unresolved occurrence.
If required authority evidence is missing, expose a recovery blocker rather than inventing history
or launching duplicate work. Reuse pool fencing/authority transfer; no second election or scheduler.

## Planned API work

**API impact: new contract for implementation**, with changes to existing VM/inventory and authority
contracts. **API impact: none for this planning-only change** because no runtime endpoint or payload
is changed. Do not publish the following families as already available in the API reference.

| Family | Caller → authoritative work → reported result |
|---|---|
| Recovery capabilities/preflight | UI/client → Controller authorization → master/target Core checks → supported combinations, coverage, blockers and fresh revision |
| Snapshot create/list/revert/delete and status | UI/client → Controller relay → master admission and owner Core native operation → durable inventory/outcome, adopted after replacement |
| Backup repository/policy lifecycle | UI/client → Controller relay → master record and per-host access checks → same-identity inventory and errors |
| Back up now/full capture/status/cancel | UI/client → Controller API relay, or authenticated Core-local request → master-owned job and executing Core → validated repository publication and durable progress |
| Schedule/run/catalog management | UI/client → Controller API relay → master-owned schedule, run records and catalog → next/missed runs and per-VM outcomes; master triggers scheduled work without Controller |
| Restore preflight/start/status/replace | UI/client → Controller API relay, or authenticated Core-local request → destination master job and Core disk/configuration work → verified stopped VM or recoverable failure |
| Import/protect/prune/test results | UI/client or Core job → master authorization → Core validation, deletion safety and durable verification history → separate integrity/restore evidence |
| Fresh-Controller pool adoption | Authorized master trust handover → master-led member trust/reporting convergence → stable pool/host/VM IDs plus job/schedule/history/catalog inventory → replacement Controller views, no per-member enrollment or job recreation |

For each family specify in its implementation slice: exact route/command/schema; existing session,
CSRF and role authorization; site/pool/host/resource scope; authenticated peer identity; validation;
idempotency key; same-key/different-input conflict; stale revision and ownership conflicts; typed
unsupported/capacity/unavailable errors; asynchronous acceptance versus completion; progress;
safe retry and cancellation boundaries; reconnect/Controller replacement; version compatibility.
UI disabling never substitutes for API authorization. Secrets use narrow Core receipts and never
appear in logs or public diagnostic evidence.

Use capability/version negotiation so mixed-version hosts refuse unsupported recovery safely. A
preflight is explanatory, not a lasting grant: Core revalidates immediately before mutation. Keep
bulk actions as existing per-VM primitives and aggregate results, without another batch scheduler.
Update both sides, `docs/API.md`, applicable `docs/api/*openapi.json`, generated references and
focused contract/integration tests in the same slice. Provide authenticated Core-local backup,
restore, schedule/status and recovery access using the same engine and validation. A focused local
recovery UI/CLI is required while Controller is absent; do not duplicate the central console.
Pool adoption must authenticate the master's identity and explicitly authorize replacement of
Controller trust, then converge members without individual enrollment. Exclude the old Controller
from future mutations, even if it returns. Keep pool peer identity, repository secrets and workload
operations independent of the old Controller CA. Preserve host outbound report transport; specify
the trust-handover and adoption APIs rather than bypassing checks or writing databases directly.

## Delivery slices and dependencies

Each functional slice includes Core behavior, durable state, API proof including failure/retry,
then UI and a walked-through guide. A wireframe can be designed earlier; it cannot close a slice.
Current status: **Phase 5 closed for disposable virtual-lab scope (2026-09-27).**
S0–S3 are accepted within that recorded lab boundary; S2 stopped lifecycle and S3
running create have disposable-lab and browser evidence (including bulk stopped
Snapshot and Snapshots a11y follow-up). A later 2026-09-26 native multi-backend
lifecycle/midcommit campaign on local/NFS/SMB/iSCSI is recorded under
[docs/qa/2026-09-26-recovery-s0-multi-backend-dr.md](../qa/2026-09-26-recovery-s0-multi-backend-dr.md);
it does not claim product S2/S3 on every backend and is not Phase 6 backup/restore.
Close record: [docs/qa/2026-09-26-phase5-gate/CLOSED.md](../qa/2026-09-26-phase5-gate/CLOSED.md).
**Phase 6 closed for disposable nested-lab scope (2026-09-27).** The rows below describe
required delivery and honest slice status. Physical/production and full three-host
campaigns remain accepted residuals — see
[close record](../qa/2026-09-27-phase6-gate/CLOSED.md).

| Slice | Deliverable and prerequisite | Exit evidence | Slice status (2026-09-27) |
|---|---|---|---|
| S0: feasibility and workflow contract | Read actual code/bug history; select disposable resources; run installed-version experiment; review workflows and close legacy ownership-checker gap | Runnable experiment, observed capability matrix, chosen native commands/state boundaries, UX defaults; `check_docs.py` and regressions reject new Controller-owned pool backup state | Accepted (Phase 5 close) |
| S1: chain and authority safety | Depends on S0. Add minimal disk-chain and master reservation model; trace every VM/disk/storage consumer, replica/adoption and recovery path | Core API tests for admission/replay/interruptions; no unsafe edit/delete/power/storage path bypass; long operation does not block unrelated VM control | Accepted (Phase 5 close) |
| S2: stopped snapshot lifecycle | Depends on S1. Create/list/revert/delete with all disks and required state; VM panel and bulk workflow; guide | Native markers, older-point/descendant handling, configuration match, consolidation/deletion/reboot, restart and Controller replacement; browser workflow | Accepted (Phase 5 close) |
| S3: running snapshots and Phase 5 close | Depends on S2. Crash-consistent and supported quiesced capture; bounded freeze/thaw; complete Phase 5 matrix | Running write workload, freeze failure/thaw proof, unsupported refusal, independent review and Phase 5 checklist below | Accepted (Phase 5 close) |
| S4: destination and stopped full recovery | Depends on closed Phase 5. Core repository and manifest, stopped full backup, new stopped VM restore, Back up now/Restore UI and guides | Original disks unavailable; different compatible host/storage; isolated boot and multi-disk/application checks; corruption and interruption refusal | **Accepted (disposable)** — product NFS stopped backup + restore-as-new ([product lab](../qa/2026-09-27-phase6-product-backup-lab.md)); hermetic CI |
| S5: online full recovery | Depends on S4. Installed libvirt native full-backup API and matching state capture; running-VM UI | Running writes, guest shutdown/agent failure, snapshot-plus-backup, destination stalls/fullness, interrupted publication, full independent restore | **Accepted (disposable bounds)** — crash path unit/contract; quiesced/UEFI online **refused**; live online crash guest not prepared in product lab (accepted residual) |
| S6: policy, scheduling and retention | Depends on S4/S5 evidence for each selectable mode. Core scheduler, job/history/catalog authority and replication; Controller API views; reviewed presets and retention preview | Time-zone/DST, missed/overlap/restart tests, Controller-absent schedules, safe master takeover, bounded throughput, retention and partial outcomes | **Accepted (bounded)** — Controller-absent policy Run now + adoption ([Campaign A](../qa/2026-09-27-phase6-campaign-a.md)); full wall-clock schedule span residual |
| S7: replacement and disaster recovery | Depends on S4–S6. Original replacement, Core recovery command/local UI, master-only adoption and supported appliance replacement, test results/cleanup; separate Controller identity export checks | Mandatory three-host campaigns below; source/catalog loss; restored guests verified before new Controller; full job/history adoption without old DB/export; guide-only rehearsal | **Accepted (bounded)** — replace cutover implemented; Campaign B re-run PASS after BUG-0651; full three-host / physical residual |
| S8: Phase 6 close | Depends on every preceding gate. UX refinement, package/native regression, documentation and independent acceptance review | Complete evidence index and closeout checklist, exact limitations, published status updated only for proven results | **Closed** for disposable virtual-lab scope — [close record](../qa/2026-09-27-phase6-gate/CLOSED.md) |

**Current candidate reassessment (2026-09-27):** S4–S8 acceptance is open for
the post-close worktree changes described in the [review note](../qa/2026-09-27-phase6-post-close-review.md).
The table records the historical disposable close, not a fresh run of the
changed candidate. Its changed API contract adds the `backup-v1` Controller
acknowledgement and authenticated master-to-member backup peer GET/POST routes;
Core retains the repository, policy, run, catalog, pin and metadata mutation
receipt records. A signed paged backup authority manifest carries them to a
promotion candidate, and a fresh Controller adopts the authenticated master
inventory. The Core-local metadata replay response may be a durable `Result`
when the original resource DTO is unavailable. **API impact: changed
contract; guide impact: updated** for the current candidate. See
[docs/API.md](../API.md) and the backup operator guides.

### S0 experiment must settle these implementation choices

- Record exact Debian, libvirt, QEMU, firmware and swtpm versions from the candidate Core install.
  Test the installed feature set; upstream documentation describes possibilities, not Nex support.
- Validate external qcow2 create/revert/delete/consolidation for one and multiple disks, newest and
  older recovery points, repeated revert and subsequent snapshot, and partial disk failure.
  Prefer native lifecycle operations; do not select a backend whose safe deletion is unproven.
- Test BIOS, UEFI, Secure Boot and vTPM state. Include TPM-sealed data where supported. If consistent
  running firmware/TPM capture cannot be proved, explicitly block that profile before mutation and
  retain a tested stopped path. Never silently omit required state.
- Validate eligible local, NFS, SMB and exclusive-host iSCSI VM storage repositories. Ordinary
  iSCSI stays single-host; clustered iSCSI needs separate evidence and cannot inherit this
  support. The independent backup destination remains external NFS, as specified in Phase 6
  and the setup workflow; this source-storage matrix does not add backup destination types.
- For online full backup, prefer libvirt's native push-mode backup API over a second manual
  snapshot/copy/commit/delete pipeline. Prove full output independence and interaction with existing
  snapshots. For stopped backup use a native full-image export with no retained source backing links.
- Measure cancellation, repository stalls, native job disappearance, guest shutdown, capacity use
  and sync/publication behavior. Choose bounded timeouts based on evidence, not arbitrary success
  assumptions. Keep an unsafe or unobservable native outcome in Needs attention.
- Document snapshot limits, chain growth/capacity warnings and deletion behavior before UI copy is
  frozen. Do not enable an unsupported combination merely to make a screen look complete.
- Check that the selected snapshot/backup lifecycle has a supported path to libvirt checkpoint
  and dirty-bitmap handling on installed versions. Record any future CBT constraint; incremental
  implementation remains the separately gated roadmap follow-on.

Selected snapshot capacity defaults for S2: keep the existing 32-retained-point limit. Always
explain before capture that snapshots share the VM's storage and consume more space as it
changes; deletion can require consolidation and does not promise immediate free space.
Show Core-observed available space and allocation where available, otherwise show unknown.
Do not turn the synthetic chain-size samples into a predicted growth rate, guaranteed safety
margin or automatic expiry policy. Native capacity errors remain subject to the same durable
observation/reconciliation rules as other interrupted operations. The UI and its walkthrough
must implement and verify these defaults in S2; this decision does not claim they exist yet.

The required matrix contains format, storage kind/filesystem, installed versions, VM power state,
disk count, firmware/vTPM, capture mode, operation and result. Record supported, refused with reason,
failed and untested separately. Required coverage cannot be closed by marking every profile unsupported;
a material reduction of the roadmap's scope goes back to the project owner.

## Failure and recovery acceptance matrix

Run portable fault tests in CI and the applicable cases against real native operations in the
selected disposable lab. Use a marker on each disk and application data with a known expected result.

| Scenario | Required observable result |
|---|---|
| A → snapshot → B → revert | A and matching configuration/firmware return on every disk; subsequent writes/boot work |
| Multiple snapshots, older revert, delete parent/current/leaf | Valid remaining points and current disk chain; unsupported cases refused before effects; successful reboot after consolidation |
| Partial multi-disk snapshot or copy | No usable partial point; original running/stopped VM and every chain remain recoverable |
| Core/libvirt restart during capture/revert/consolidation | Journal/native reconciliation establishes exact state; no blind replay, orphan writable head or lost current chain |
| Browser close / Controller outage / lost reply | Core jobs and schedules continue independently; later UI finds the same operations and outcomes; no duplicate work |
| Controller replacement / pool-master transfer | Same member/snapshot/repository/job/schedule IDs, retained history and unresolved reservations survive; takeover reconciles before dispatch |
| Concurrent power/edit/media/delete/backup/revert/storage detach | Conflicts refused or serialized at Core; no removal of referenced images; unrelated VM control remains responsive |
| Space exhaustion, permissions loss, wrong mount, repository stall | Clear failure/uncertainty, no false publication, no damage to prior points; restore and retry after repair |
| Freeze failure, Core crash while frozen, thaw failure | Bounded host-owned thaw/recovery path; actual consistency recorded; no forgotten frozen guest |
| Running backup and guest-initiated shutdown | Exact installed-version behavior handled; complete proof or incomplete non-restorable result |
| Native backup job disappears | Absence alone is not success; completion plus every payload and manifest must validate |
| Original disks and snapshot chain unavailable | Restore from repository alone to different compatible host/storage; boot isolated and verify files/application data |
| Corrupt/missing payload, unsafe manifest or unknown version | Reject before VM registration or replacement cutover; actionable error with no trust-boundary bypass |
| New-VM restore | New identity/MACs, stopped/disconnected default, verified resource mapping and no collision with original |
| Replacement failure before/after cutover | One execution owner, retained predecessor and supported recover-original path; no irreversible in-place overwrite |
| Scheduler restart, DST gap/repeat, clock change and duplicate dispatch | Durable unique occurrence per schedule/VM; no double run; next occurrence correct; missed runs recorded |
| Controller outage spans schedules | Core continues due occurrences and records outcomes; no missed run solely because Controller is absent; local restore works before replacement |
| Policy changed/deleted during run | Run retains reviewed policy revision; changed retention cannot silently expand an active deletion; future runs use new revision |
| Retention concurrent with restore/protection/import | Core pins/protection survive reconnect; no active/protected point removed; failed new backup triggers no prune |
| Cross-pool restore concurrent with prune or source-master loss | Repository owner's pin blocks deletion across pools; authority transfer retains it; read-only disaster recovery excludes old pruners before proceeding |
| Empty replacement Controller | Connecting the master adopts members/jobs/schedules and retained prior/during-outage runs plus catalog/test results, with stable IDs and no old DB/export or manual job recreation |
| Lost Core catalog | Core rebuilds complete points from manifests and preserves protection; distinguish true pool-state loss from ordinary Controller replacement |
| Controller identity export recovery | Recovers Controller-scoped accounts/sites/audit/keys separately; neither that export nor the old CA is needed for authorized pool/job adoption |
| Master and Controller host fails | Safe Core-local promotion retains scheduling authority/history; surviving-host backups and accessible-backup restores continue; unavailable local disks remain explicit blockers |
| Backup/restore cancelled or repeatedly retried | Safe tested boundary, retained diagnostics/cleanup, exact request receipts and no duplicate identity or writable files |

Scheduler defaults: store scheduled UTC occurrence plus time-zone/rule; a nonexistent local time is
recorded missed, a repeated local time runs once. Keep pause/last-dispatched state durable on Core.
After genuine Core scheduler downtime, record missed occurrences and avoid an unbounded catch-up
storm; offer Run now through Core. Controller downtime alone is not scheduler downtime. Core jobs
and new scheduled occurrences must work while Controller is absent indefinitely; no freeze,
mutation, repository credential, run result or dispatch depends on Controller returning.

## Mandatory three-host continuity and adoption campaign

This is the decisive Phase 6 gate. Run it on an explicitly selected disposable lab using the actual
candidate install/package and authenticated product APIs. No database edits, synthetic run history,
manual recreation of jobs or restored old Controller export may make the adoption test pass.

### Establish the pool and protection

1. Install Core on Lab Host A, deploy Controller A from its local UI, then install Core on Lab Hosts
   B and C and join them to A's pool. A starts as pool master and Controller appliance host. Record
   versions/artifact digests and confirm one authoritative pool with three stable member identities.
2. Create representative managed workload VMs on all three hosts, with a multi-disk guest and
   identifiable file/application data per host. Use accessible shared storage for the shared-storage
   cases and a deliberately local-only disk for the negative case. Keep the independent backup
   repository and lab fencing access outside Host A's failure domain.
3. Through Controller A, create a reviewed backup job covering every workload VM on A/B/C. Record
   explicit appliance coverage separately: the Controller VM is not silently treated as an ordinary
   guest or claimed protected without its supported firmware/identity/recovery profile being tested.
   Include an enabled schedule, a paused job, retained completed and failed runs, protected points
   and a test-restore result. Record job IDs/names, VM scope, repository IDs, schedule/time zone,
   next occurrence, retention/limits/overrides, policy revisions and per-VM run IDs/results.
4. Complete and validate baseline backups, then begin another run before inducing failure. Preserve
   a sanitized evidence inventory for comparison only; it must never be imported into Controller B.
   Test the near-dispatch boundary separately to prove the same occurrence is not dispatched twice.

### Campaign A: Controller disappears; Core remains available

1. Stop and remove only the selected Controller appliance and make its database/configuration/export
   unavailable to the replacement. Preserve Core state, the pool, workload VMs and backup repository.
   The lab's operator safety control is outside the failed Controller. Do not interpret this step as
   permission to delete a live appliance or bypass the product's appliance deletion safeguards.
2. Keep Controller absent across at least two due schedule occurrences. Prove the in-flight run
   reconciles and fresh scheduled backups cover VMs on each host, using Core-held credentials and
   the same job/schedule IDs. Show real artifacts and results, not merely scheduler log entries.
3. Before any replacement Controller exists, use authenticated Core-local recovery to find and
   restore a backup for a guest from each source host, including a point captured during the outage.
   Make that guest's original disks unavailable for the restore test. Restore onto a compatible host
   and selected storage, boot isolated, and verify every disk marker and application data. Persist
   the verification result in Core. Also prove snapshot create/revert/status through the same Core
   service without Controller. Routine recovery uses the local recovery UI; exercise its CLI/API too.
4. Use the supported host-local retire/redeploy path to install genuinely fresh Controller B on
   Host A, with empty Controller state and a new CA/admin identity. Do not reset the enrolled Core,
   re-create its pool, stop unrelated guests or reuse the old Controller's disk. Establish a new
   Controller site/admin context as needed; these are separate from the pool's persistent identity.
5. Add only the current pool master through the authorized trust-handover workflow. The master
   converges member trust/reporting and exposes the complete pool. No separate host enrollment,
   repository re-entry, job recreation or manual history import is allowed. Core backup execution
   remains independent throughout adoption; mutation credentials from Controller A become invalid.
6. Compare Core and Controller B through APIs and the actual UI. Require the same pool/host/VM,
   repository, job and schedule identities; names and settings; enabled/paused state; retained old
   successful/failed runs; in-flight and outage-created run results; recovery points, protection and
   verification results. Previous runs with deleted source VMs/policies remain identifiable under
   the configured history retention. Member reachability is reported honestly without dropping
   durable membership. An empty “new job” screen or history starting at re-adoption is a failure.
7. Interrupt and retry adoption, refresh the browser, restart Controller B, then observe the next
   scheduled occurrence. Require no duplicate records, policy reset, duplicate backup, lost history
   or rewritten schedule. Reintroduce the isolated old Controller identity and prove Core refuses
   its mutations. Record actual API/browser evidence and continued workload health.

### Campaign B: the master host and its Controller disappear together

Repeat from a known lab baseline with A as master/Controller host. Fail the whole Host A while
backup work is active, leaving B/C and the independent repository available. Do not use Controller A
to coordinate recovery. This campaign tests Core backup authority recovery; it does not claim that
all VM workloads automatically fail over.

| Case | Required result and boundary |
|---|---|
| Surviving B/C become a valid new pool authority | Use the supported authenticated Core-local promotion/fencing path. If automatic succession is claimed for that exact topology, test it separately; otherwise use the supported manual path. No backup scheduler starts on guessed ownership or a simple timeout |
| Backup state at takeover | Recover committed job/schedule definitions, retained history and pending occurrences from replicated Core state; reconcile B/C journals and repository publication. Continue eligible surviving-host jobs with the same identities and no duplicate capture/prune |
| Backup interrupted on A | Establish whether complete publication occurred. Report incomplete/uncertain work accurately, retain earlier good points and do not fabricate success or reassign its disk access from inventory alone |
| Restoring A's VM from independent backup | Restore through surviving Core while Controller is absent, onto a compatible B/C target, then boot isolated and verify disk/application state; source Host A and its original disks remain unavailable |
| A's VM record and shared disks remain discoverable | Show the record and observed disk access separately. Further backup of the original disk set requires proven exclusion of A, matching configuration/firmware and a supported execution/storage ownership transfer; discovery alone is insufficient |
| A's VM used unavailable local disks | New capture is blocked with the exact reason; backups of B/C continue. Existing independent recovery points remain restorable. Do not imply local disks became shared or that lost writes can be recovered |
| Fence failure, insufficient authority or repository unavailable | Refuse unsafe takeover/write/prune and show the blocker locally; never claim a successful backup or restore. Resume only after the prerequisite is actually repaired |
| Fresh Controller B after takeover | Install on an eligible surviving host or external disposable system, connect only the new master, and repeat Campaign A's complete member/job/history adoption comparisons |
| Host A returns | Old master/scheduler and Controller trust cannot resume mutations independently. Require supported restriction/rejoin and reconcile old run receipts before participation; no second VM writer or duplicate schedule occurrence |

Phase 5/6 delivers backup scheduling takeover under existing proven pool-authority mechanisms and
restoration from independent backups. Recovery/reassignment of intact disks belonging to a lost
host retains Phase 7's VM recovery gate; automatic VM restart retains Phase 9's quorum/fencing gate.
If those features are not supported yet, the shared-disk case must show a truthful blocked state
and offer supported restoration from backup. It cannot be marked as working VM HA. No new election
or general VM failover system is introduced by this recovery plan.

**Pass evidence:** compare the same recorded identities and settings before loss, in Core during
outage, and after fresh adoption; retain scheduled-run receipts, validated manifests, isolated
restore/application results, native fence/takeover observations and inspected UI captures. Both
campaigns are mandatory. Safety refusals pass their negative cases but cannot substitute for the
successful takeover/continued-backup/restore/adoption cases. Missing lab prerequisites leave this
gate open; writing a test harness or listing a VM is not successful recovery.

## UX acceptance and documentation

Run the following with at least two representative operators who did not implement the tested
workflow, using only the UI and supplied guide. Record completion, mistakes, clarification needed
and safety misunderstandings; fix and rerun affected tasks. Lack of participants leaves usability
acceptance open rather than substituting a developer's self-review.

| Task | Pass condition |
|---|---|
| Create, find, revert and delete snapshot | Operators identify covered disks and discarded changes, and complete the workflow without routine SSH |
| Set up protection | Register repository and create policy; explain selected VMs, downtime, time zone, next run and retention effect; policy takes no more than three reviewed steps |
| Find a VM without protection | Locate a VM with no recovery points or active schedule, distinguish its actual missing protection, and apply a reviewed policy |
| Restore after source loss | Find the correct point by VM/time, choose compatible target, restore as new in no more than three reviewed steps, keep isolation and complete checks |
| Diagnose a failure | Find failed/missed VM outcome, distinguish unknown from failed, and follow the correct repair/reconcile action |
| Judge recovery evidence | Distinguish snapshot from independent backup, completed copy from integrity check, and boot from application verification |
| Recover and clean up | Record test result and remove only the selected test VM; find retained-original state after replacement |
| Adopt protection after Controller loss | Add the current master once; find all pool members, original jobs/schedules and prior/during-outage runs without recreating any of them |

Review real browser captures at the three specified viewport sizes in both themes, with zero, one
and many recovery points, long names, no matching filters and partial runs. Exercise loading, stale,
permission, unavailable/full repository and uncertain outcomes; keyboard-only primary workflows and
focus restoration are mandatory. Fixture/browser simulations are useful but labelled as such.
Include at least one real Controller/Core browser journey through each completed native lifecycle.

Deliver guide pages for snapshot rollback, backup setup/policies, restoring/testing a VM and recovering
the backup catalog; update existing Controller recovery, VM edit/delete, storage and navigation guides
where behavior changes. Every page follows [DOCUMENTATION.md](../DOCUMENTATION.md), links from the
guide index and is walked through on the candidate. Explain key recovery, independent storage,
retained data, supported combinations and normal error recovery without implementation jargon.

S7 must deliver a documented, supported recovery command that validates/imports repository manifests
and reaches restoration after catalog loss, using the same Core validation and restore operations.
Provide exact syntax and prerequisites in the guide when implemented. Rehearse it from a fresh
compatible Core setup with Controller absent and the old Core catalog unavailable, including missing-key, incomplete
manifest and prior-writer-exclusion failures. A GUI import alone does not satisfy this requirement.

QA records under `docs/qa/` identify candidate source/artifact digest, installed versions, selected
disposable environment, command/test, actual result, inspected captures, reviewer and limits.
Sanitize all public artifacts; keep private lab addresses and credentials out of tracked files.
Account for the repository's untracked-image limitation with a self-contained written walkthrough.
Mark unavailable prerequisites and failed attempts explicitly. Never relabel old or simulated
evidence as newly executed native proof.

## Agent assignments and verification

The primary agent owns scope, acceptance and integration. Use bounded workers after S0 decisions:

| Lane | Exclusive write responsibility | Handoff |
|---|---|---|
| Core/native | Assigned vmhost recovery files and native tests | Commands, stages, recovery evidence and supported matrix |
| Authority/contracts | Assigned agent/protocol files, Core scheduling/history and API schemas | Job authority, admission, replication/takeover/adoption and negative contract tests |
| Controller | Assigned API relay/adoption files and tests | Permissions, master-led adoption and host-reported views; separate Controller identity export coverage |
| UI/guide | Assigned web recovery files and guide pages | Reviewed flows, actual browser captures and guide walkthroughs |
| Independent review/QA | Read-only review; assigned QA report only if agreed | Findings tied to observable behavior and evidence; no self-approval |

Sequence shared-file changes (`managed.go`, protocol, main UI routing, API reference, bug ledger)
under one owner at a time. Workers may research independently before contracts settle, but do not
implement competing schemas. Review each meaningful slice; fix findings and verify corrections.
No agent commits, stages, pushes or changes Git history. No live-infrastructure or destructive lab
operation follows merely from this plan: select and authorize disposable targets before execution.

Use the existing suites, adding observable regressions rather than tests that mirror implementation.
Ensure each new test actually runs in the forge's `.github/workflows/`; keep `.gitea/workflows/`
copies aligned. Preserve the single cheap github.com job and its documented limits. Native lab
requirements need an explicit runnable command and prerequisites, not silent CI skips.

Implementation verification includes:

```sh
go test -race -timeout 20m ./...
python3 scripts/generate_api.py --check
python3 -m unittest discover -s tests -p 'test_*.py'
npm --prefix web run typecheck
npm --prefix web test
npm --prefix web run build
python3 scripts/check_docs.py --worktree
python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
```

Add the chosen native experiment/fault/restore and browser commands to the slice's QA record when
implemented, with installed prerequisites and exact results. Test the actual packaged Core, service
restart, permissions/AppArmor and installed Controller before final closure. If packaging/dependencies
change, apply [LICENSING.md](../LICENSING.md), retain source/notices and verify source-delivery inputs.
After a project-owner push, inspect the actual required forge run; a local pass or workflow file does
not establish CI success. Missing required evidence keeps acceptance open.

## Closeout checklists

### Execution checkpoint — 2026-09-27 (Phase 6 disposable close)

**Phase 5 closed for disposable virtual-lab scope (2026-09-27).**
Close record: [docs/qa/2026-09-26-phase5-gate/CLOSED.md](../qa/2026-09-26-phase5-gate/CLOSED.md).

**Phase 6 closed for disposable virtual-lab scope (2026-09-27).** Owner authorized
close after lab install, product NFS backup/restore, bounded Campaign A/B (B re-run
PASS after BUG-0651), browser walkthrough (settlement fixed in code), hermetic CI and
replace cutover. Physical/production certification is **not** claimed.
Close record: [docs/qa/2026-09-27-phase6-gate/CLOSED.md](../qa/2026-09-27-phase6-gate/CLOSED.md).
Index: [Phase 6 implementation index](../qa/2026-09-27-phase6-implementation-index.md).

Accepted residuals (see close record): nested ≠ physical domains; quiesced/UEFI online
refused; browser re-walk after settlement fix may remain; full three-host join+schedule
wall-clock not fully proven; `0.4.12` + overlay (not ISO); forge CI on published push
not yet inspected.

Resume / next work: [session handoff](2026-09-26-recovery-session-handoff.md).

The selected path is public per-disk blockcommit without data deletion, followed by
verified metadata changes, a complete reference scan and exact unreferenced-file unlink.
Integrated native snapshot data-delete remains unsupported. NFS is the independent backup
destination; local/NFS/SMB/exclusive-host iSCSI coverage concerns VM source storage.
The preserved historical second-disk mismatch and forensic-image handling uncertainty
(BUG-0639) remain explicit limitations; the separate fresh firmware run does not rewrite
those outcomes. S1 admission and authority safety is implemented; S2 stopped
lifecycle is accepted under Phase 5 close for disposable virtual-lab scope (see
the close record).

The project owner authorized a local disposable lab, then lifted the temporary gaming
resource restriction. Three selected Core guests now have four vCPUs and 4 GiB each;
a separate storage guest supplies the lab repositories. The package-based Debian Core
lab is not ISO-installation acceptance or an independent physical failure domain.
See [lab evidence](../qa/2026-09-25-recovery-s0-core-lab.md).

Current implementation and evidence:

- The ownership checker rejects the superseded Controller-owned pool backup exception
  (BUG-0581). S1 implements complete disk-chain validation and historical retained-disk
  deletion checks, with scoped independent review (BUG-0591). Fresh-host inventory
  behavior was corrected and its failing regressions rerun (BUG-0593). Foreign
  external-data dependencies omitted from domain XML now fail the shared ownership
  check, with a failing-before regression and independent review (BUG-0594).
- Core authority records bind exact snapshot reservations and terminal evidence to
  owner, epoch, definition, disk identity and immutable capture intent. Master-only
  reservation/catalog inventory distinguishes unknown from empty observations.
  Reservation acquisition, exact settlement acknowledgement, authenticated Core-local
  managed snapshot ingress and one background capture per host were initially under integration;
  the stopped-create path has scoped native evidence below. Local/shared starts, reboots, media,
  replay and dependent storage mutations have recovery guards; native autostart stays
  disabled for managed pool VMs, with desired boot intent admitted through Core.
  Startup rehome must preserve unresolved capture identity. The executor's crash and
  retry paths were still under review at that checkpoint. The first stopped two-disk host-API attempt verified
  durable acceptance, exact retry and changed-input conflict with Controller offline,
  then failed before native capture because local repositories lack the typed identity
  the new snapshot check expected. That identity check is corrected and regression-tested.
  The next candidate reached a durable master reservation but stopped with `needs_attention`
  during disk-projection reconciliation. Read-only inspection found unchanged standalone
  disks, intact original markers and no native snapshot; the reservation remained held.
  The shared definition validator incorrectly compared normalized MiB with raw KiB;
  BUG-0604 covers both `memory` and libvirt-injected `currentMemory`; the first correction
  missed the latter, so its successful unit/race checks did not establish native recovery.
  With both corrected, the same reserved operation completed on r5 with Controller offline:
  the master settled it, both native chains and original markers verified, and the VM stayed
  stopped. Independent artifact review confirmed this result. After correcting two runner
  readiness/reporting defects, exact replay after Core restart passed with Controller offline,
  with matching catalog, chains and markers. The same Controller reconnected and adopted the
  snapshot. A separate fresh Controller then adopted the original pool, host, exact
  snapshot descriptor and succeeded operation. Native marker checks passed in an
  identity-bound read-only continuation after a runner error; the old Controller
  pre-enrollment fingerprint was not retained, so byte equality is not claimed. The
  wider multi-host replacement and acceptance matrix remain open.
  [Core API evidence](../qa/2026-09-26-recovery-core-api-preparation.md)
  records the actual attempts and their limits.
  See [API reference](../API.md) and the
  [development guide](../guide/vm-snapshots.md).
- A subsequent S1 consumer audit found two remaining safety gaps: pool VM deletion
  could discard a retained snapshot catalog, and the native start hook lacked the local
  recovery-reservation check. The native hook fix (BUG-0613) passed failing-before
  regressions, the full vmhost suite and independent review; delete admission/catalog
  protection (BUG-0612), including its execution-lock race fix, passed agent/vmhost race
  checks and independent review. The r6 candidate then refused both authenticated
  keep/wipe deletes without changing the retained snapshot or data; its installed hook
  refused an isolated unresolved-intent fixture (not a real VM start). See the
  [r6 safety checks](../qa/2026-09-26-recovery-snapshot-safety-r6.md). S1 remained open then.
- The next S1 audit found that snapshot descriptors did not retain historical storage,
  media and network dependencies after VM edits. BUG-0618 adds compact Core-owned
  captured dependency IDs and destructive-operation guards; legacy descriptors without
  them remain unknown and must fail closed. Ordinary start/lease and metadata-only
  operations must remain available. Network/media removal also requires exact ready-member
  replica acknowledgements before effects, with the mutation retained on uncertainty and
  preserved through authority adoption. The implementation, full race suites and independent
  review passed before the following r7 native acceptance work.
  The matched r7 binary now passes exact legacy-operation replay across Core restart,
  preserving the descriptor, stopped VM and both disk markers/chains. Its changed-body
  conflict assertion and fresh Controller readback passed independent review in the
  [Core API evidence](../qa/2026-09-26-recovery-core-api-preparation.md). This run kept
  Controller available. A separate r7 Core walkthrough captured storage/network/media
  dependencies, changed the stopped VM's current resources, and refused removal of its
  historical network and ISO. The original saved disk marker remained intact and an
  ordinary storage recheck succeeded. Storage detach had additional live dependencies,
  so it did not isolate the snapshot guard; no lease workflow was exercised. Independent
  review confirmed these limits in the
  [resource-dependency evidence](../qa/2026-09-26-recovery-snapshot-resource-dependencies.md).
  The supported edit lifecycle retains removed disk files, so a native storage-A detach
  cannot be isolated from that separate protection while its snapshot remains. A focused,
  independently reviewed master-contract regression now isolates current storage B from
  historical snapshot storage A and verifies the exact refusal. This supplements, rather
  than relabels, the native combined-dependency refusal; see the
  [evidence boundary](../qa/2026-09-26-snapshot-only-storage-detach-boundary.md).
- Review also found that snapshot reserve/settlement acknowledgements could precede the
  asynchronous authority replica push, allowing promotion to lose acknowledged recovery
  state after master loss (BUG-0620). Exact ready-member replica acknowledgements and
  authority revalidation now precede these responses. Three-host peer fixtures passed
  acknowledgement loss, exact retry and promotion with retained catalog state; independent
  review passed. The r7 native campaign then completed a snapshot during Controller outage,
  observed the exact reservation on both signed replicas before native execution, and
  retained the completed catalog through actual fencing and master promotion. A subsequent
  reservation exposed BUG-0641: the powered-off former master remains ready and blocks
  required replica acknowledgements. The exact pending operation is preserved before
  native effects. The [native campaign](../qa/2026-09-26-recovery-snapshot-r7-promotion.md)
  therefore left post-promotion operation continuity and the planned post-native Core
  crash check open at that checkpoint; it did not close S1 or the mandatory Phase 6 campaign.
  The reviewed correction adds Core-owned `PoolMember.replica_ack_excluded`, set
  only from fresh verified promotion fencing and cleared after current inventory and an
  exact replica acknowledgement. Identity, ownership, inventory completeness and future
  fencing checks remain unchanged. **API impact: changed contract** — authenticated
  membership reports and signed authority replicas carry the optional field; a fresh
  Controller adopts it from Core. The durable record is the master's `pool-peer.json`
  and replicated membership. **Guide impact: updated**; protocol/agent regressions and
  race tests passed independent review. The corrected r8
  [promotion/rejoin campaign](../qa/2026-09-26-recovery-snapshot-promotion-r8.md) passed:
  fresh verified fencing powered off the current master, its replacement recorded the
  exact exclusion, and the surviving owner completed a new snapshot while the fenced host
  stayed off. On return, that host first persisted an isolated boot restriction, then
  adopted current authority and inventory; its exclusion cleared with current replica
  acknowledgements. Independent review accepted this bounded S1 proof.
  The r8 [Core crash campaign](../qa/2026-09-26-recovery-snapshot-core-crash-r8.md)
  now proves the post-native/pre-settlement interruption: after native success, the held
  client timed out and Core recorded an unresolved result. The exact service was then
  killed; the same operation reconciled after restart without another native create.
  This is timeout followed by Core interruption, not a kill during the active native call. Both signed member replicas retained the settled catalog point, both disk
  markers and full chains verified, and the VM stayed stopped. Independent review passed;
  this is separate from the fresh promotion and returning-host proof above.
- A fresh four-patch system-guest run interrupted one partial non-READY disk commit,
  preserved the same paused, confined QEMU process across libvirt restart, reconciled
  completion without resubmitting that disk, then completed the second disk. Metadata-only
  deletion, reference-checked unlink, retained data, reopen and B/C reverts passed. The
  embedded candidate receipt and native artifacts passed independent review; this remains
  one controlled local-qcow2 daemon interruption, not broad backend acceptance.
- The four-patch candidate also passed BIOS quiesced capture/thaw/rollback and stopped
  Secure Boot/vTPM paired-state restoration in the disposable Core lab. Both new VMs
  remain stopped with their snapshots preserved; deletion was deliberately not exercised.
  See the [firmware evidence](../qa/2026-09-26-recovery-s0-firmware-nex3.md).
- A fresh `+nex3` public-delete run then completed both stopped disk commits, all twelve
  logical comparisons, retained-point reparenting, metadata-only deletion, full reference
  scanning and exact old-head unlink. After paired NVRAM/swtpm restoration, the guest
  booted with both retained disk markers, Secure Boot enabled and its sealed secret
  usable; later EFI/TPM changes were absent. The receipt and cleanup passed independent
  review. This proves one two-disk nested TCG Secure Boot/vTPM profile, not a product
  deletion API or general hardware coverage. See the
  [public-delete firmware evidence](../qa/2026-09-26-recovery-s0-firmware-public-delete.md).
- A fresh four-patch guest completed the public two-disk off-branch sequence, including
  equivalent-base checks, stopped child rebases, metadata-only parent removal, guarded
  unlink and native reverts/reopens of both retained branches. The
  [branch evidence](../qa/2026-09-26-recovery-s0-branch-nex3.md) records the exact candidate
  and failed earlier observers; independent raw-artifact review passed. The four-patch
  32-point run reached unlink but failed its saved-source observer. Separate read-only
  reconciliation found the retained and current data intact (BUG-0632). A fresh run then
  completed the native lifecycle, retained data and same-UUID reopen; independent review
  verified those observations. Its terminal status print failed (BUG-0634), so the
  [deep-chain evidence](../qa/2026-09-26-recovery-libvirt-deep32.md) explicitly preserves
  the failed runner status alongside the completed native checks. Neither result closes S0.
  The captured chain allocation grew from 667,648 to 15,015,936 bytes per disk between
  the first and thirty-second points on two 128 MiB synthetic disks, measured immediately
  after each selected capture and before that point's marker writes. This supports the
  existing 32-point catalog bound for the tested local workload, not a capacity guarantee
  or a workload-independent free-space margin. Operator capacity guidance must preserve
  that distinction.
- The six-patch libvirt candidate built successfully: 73 packages, 92 verified checksums,
  reconstructed source matching nine changed C files, and 158 upstream test passes,
  one expected failure and one skip. The 32-point/two-disk deletion, cleanup and reopen
  regression passed on this candidate. Source retention and packaging limits remain
  in [backport evidence](../qa/2026-09-25-recovery-libvirt-backport.md) and
  [delivery evidence](../qa/2026-09-25-recovery-libvirt-delivery.md).
- The smaller four-patch `+nex3` cohort removes the unused integrated-deletion patches.
  It built 73 packages, passed upstream tests and the public two-disk consolidation
  control under enforcing AppArmor: commit without data deletion, metadata-only removal,
  reference-checked unlink, reopen and retained-point reads/reverts. The integrated
  two-disk deletion control still failed and remains unsupported. This fresh TCG/local
  result does not repeat the interruption or backend matrix; its raw result records
  versions, with exact candidate provenance recorded separately. See
  [public consolidation evidence](../qa/2026-09-26-recovery-libvirt-public-consolidation.md).
- Earlier candidate native lifecycle checks covered local, NFS, SMB and exclusive-host
  iSCSI storage; stopped BIOS/UEFI/Secure Boot/vTPM state and selected running capture
  profiles have evidence. Their exact version/profile limits remain in
  [native results](../qa/2026-09-25-recovery-s0-native.md) and
  [firmware results](../qa/2026-09-25-recovery-s0-firmware.md); they do not certify
  a changed consolidation implementation automatically.
- Native integrated snapshot deletion remains unsafe under a second-disk interruption:
  the six-patch restart marker cancels jobs, but an already committed image can be
  unlinked before retained snapshot metadata changes (BUG-0590). The failed fixture
  was preserved without retry. See [interruption evidence](../qa/2026-09-25-recovery-s0-interruption.md).
- The selected alternative uses public per-disk blockcommit without deletion,
  snapshot XML redefinition and metadata-only removal under Core-owned intent.
  A normal linear two-disk case passed through an exact continuation, including
  retained data, chains, reopen and removal of unreferenced files. A fresh
  between-disk daemon-crash case recorded baseline data, preserved QEMU identity,
  and completed both commits with correct retained points. Its exact continuation
  completed reopen and verified file absence; native reverts to both retained points
  then restored the expected data and stopped state. Runner state, marker and
  list/dictionary checks needed correction, so failed original reports remain separate
  from successful continuations. The reported backing-header mismatch was an evidence
  reading error; raw stopped chains were correct. A fresh middle-point commit interruption
  subsequently passed end to end: daemon failure at 25.39% disconnected the original
  client, while the same QEMU completed the public job without replay or forced
  finalization; retained data, metadata, chains and reopen passed independent review.
  A fresh branched two-disk fixture also preserved both retained branches after
  per-disk commits, stopped backing-header updates against verified equivalent bases,
  metadata redefinition and reference-checked removal; native C/D reverts and paused
  reopens passed; [independent branch review](../qa/2026-09-25-recovery-s0-branch.md)
  confirmed the bounded observations. The promoted tracked linear runner also passed
  a fresh normal case after three preserved harness failures (BUG-0596), including
  reference-checked unlink and both retained-point reverts. The tracked runner also
  passed normal two-disk cases on NFS, SMB and exclusive-host iSCSI, independently
  reviewed against exact result hashes; the unlink reference scan is fixture-scoped.
  Its local-storage midcommit mode subsequently passed a real daemon interruption
  with the same paused QEMU, without retry or forced finalization. Separate tracked
  midcommit campaigns then passed on the selected NFS share, SMB share and exclusive
  iSCSI LUN, each independently reviewed through retained-point reverts; these are
  bounded fixture results, not general backend certification. Public-path current-leaf and only-point normal
  sequences passed separately. A separate local async active-commit interruption
  passed with one vda job at 19.10%: the same paused QEMU and job survived daemon
  restart, reached READY and pivoted once, with B/C current data and reopen checks.
  An independently reviewed continuation completed only the outstanding vdb pivot,
  C metadata-only removal, reference-checked unlink, same-UUID reopen and retained
  B/A reverts. A live-image verifier lock failure and an overwritten diagnostic
  payload are recorded separately; no commit, deletion or unlink was repeated.
  These remain one fixture's results. See the [bounded evidence](../qa/2026-09-25-recovery-s0-interruption.md).
  Committed top files are not assumed usable;
  affected points stay unavailable until exact reconciliation completes.
- Independent native full backups restored both disk markers from local copies with
  the original source unavailable, preserving repository hashes. A logged six-patch
  source reopen/full-backup/restart passed. Cancellation, shutdown and genuine ENOSPC
  correctly produced unsuccessful jobs despite some structurally readable outputs.
  The corrected ENOSPC-only invocation passed source writes, unchanged failed-output
  hashes and cleanup; a separate identity-bound continuation also verified restart.
  Checkpointed leaf deletion preserved parent/checkpoint metadata, not general CBT or
  parent-consolidation compatibility. See [backup evidence](../qa/2026-09-25-recovery-s0-backup.md).
- The four-patch candidate subsequently passed a full two-disk backup while a checkpoint
  remained present, then restored from local copies of owner-only NFS exports with the
  original source tree unavailable. Both markers and unchanged export hashes passed;
  installed runtime files were checked against the pinned candidate packages. The runner
  also executed inherited capacity/stall/cancel/shutdown probes; their scope deviation and
  verified cleanup are explicit in the independently reviewed
  [checkpoint backup evidence](../qa/2026-09-26-recovery-s0-backup-nex3-checkpoint.md).
  This is native feasibility, not an implemented Nex backup API.
- A separate `+nex3` two-disk backup completed, then its native completed-job record was
  deliberately consumed. Both subsequent job views reported no job; the existing parser
  refused to accept those missing statistics despite unchanged valid output files.
  Independent review verified the source-bound command and captured results. This is
  controlled completed-record dismissal, not loss of an in-progress job or daemon-crash
  recovery; see [job-disappearance evidence](../qa/2026-09-26-recovery-backup-job-disappearance.md).
- [Repository primitive checks](../qa/2026-09-25-recovery-s0-repository.md) cover exclusive
  identity creation, cross-client locking and manifest publication. They do not prove
  a product repository, physical failure isolation or production fencing.

Latest full-suite checkpoint: Python discovery ran 550 tests with 549 passes and one
Debian APT prerequisite skip. `go test ./...` passed after the promotion correction. Race-enabled
`agent`, `vmhost`, `protocol`, `controller` and `localcore` suites passed across the
recorded runs, including the complete agent rerun after the fresh-host correction.
Generated references passed; the latest documentation gate
(184 worktree paths), repository privacy and whitespace checks passed. The stopped snapshot
Core API and r6 safety walkthroughs above were bounded local evidence at that checkpoint.
**Superseded 2026-09-27:** Phase 5 closed for disposable virtual-lab scope after forge CI
on published push `ac92f407` (runs 4524–4526) and independent checklist review; see
[close record](../qa/2026-09-26-phase5-gate/CLOSED.md). Phase 6 backup APIs and product
restore walkthroughs remain untested and open.

### Phase 5

- [x] S0–S3 implemented with approved supported matrix and no unresolved safety finding.
  Consolidated disposable-lab matrix recorded in
  [docs/qa/2026-09-26-phase5-snapshot-support-matrix.md](../qa/2026-09-26-phase5-snapshot-support-matrix.md).
  Phase 5 closed 2026-09-27 for disposable virtual-lab scope;
  [close record](../qa/2026-09-26-phase5-gate/CLOSED.md).
- [x] Create/list/name/description/revert/delete work through authenticated Core APIs and Controller UI.
- [x] Multi-disk marker rollback and matching configuration/firmware proven on required eligible backends.
- [x] Running crash-consistent/quiesced profiles tested, including bounded thaw and honest refusal limits.
- [x] Older-point, backing-chain, deletion/consolidation, interruption and successful reboot proof recorded.
- [x] Edit/delete/power/storage conflicts, master authority and Controller replacement pass.
- [x] VM panel and bulk snapshot UX, accessibility, independent usability review and guide walkthrough pass.
  Bulk stopped Snapshot UI + guide + browser settlement evidenced 2026-09-26
  (`bulk-snap-11665`); Snapshots Revert/Delete confirm-dialog + disabled titles/focus
  restore: independent usability follow-up **PASS**. Accepted residuals remain in the
  support matrix; they do not reopen this Phase 5 row.
- [x] API/reference/generated contracts, regressions, privacy/docs checks and required CI results recorded.
  Local evidence 2026-09-26: `go test ./... -count=1` passed; `python3 scripts/generate_api.py --check`
  passed; `python3 -m unittest discover -s tests` passed 551 (1 skipped); `check_docs.py --worktree`
  and repository privacy passed. Close evidence is summarized in
  [docs/qa/2026-09-26-phase5-gate/CLOSED.md](../qa/2026-09-26-phase5-gate/CLOSED.md).
  See [handoff](2026-09-26-recovery-session-handoff.md).
- [x] Independent reviewer verifies state ownership and actual evidence; phase/roadmap status updated accurately.

### Phase 6 and the complete recovery delivery

**Phase 6 closed for disposable virtual-lab scope (2026-09-27).** Checked items below
are accepted within that boundary. Unchecked boxes are **accepted residuals** (or later
roadmap), not open Phase 6 blockers for disposable close — see
[close record](../qa/2026-09-27-phase6-gate/CLOSED.md).

- [x] Phase 5 closed for disposable virtual-lab scope (2026-09-27);
  [close record](../qa/2026-09-26-phase5-gate/CLOSED.md).
- [x] S4–S8 implemented with no unresolved safety finding for disposable scope.
  S4–S7 product surfaces + unit/contract; S8 disposable close —
  [implementation index](../qa/2026-09-27-phase6-implementation-index.md);
  [close record](../qa/2026-09-27-phase6-gate/CLOSED.md).
- [x] Repository identity/access/independence and complete immutable backup publication proven
  on disposable lab ([product lab](../qa/2026-09-27-phase6-product-backup-lab.md);
  hermetic CI in `internal/agent/backup_ci_test.go`).
- [x] Stopped full backup proven on disposable lab; online crash path unit/contract present;
  quiesced/UEFI online remain refused (accepted product limit). Live online crash guest not
  prepared in product lab (accepted residual).
- [x] Source disks unavailable: restore-as-new on disposable lab
  ([product lab](../qa/2026-09-27-phase6-product-backup-lab.md);
  [Campaign A](../qa/2026-09-27-phase6-campaign-a.md)). Isolated boot / application-file
  proof beyond registration remains residual.
- [x] New-VM defaults and restore-as-new lab-proven; restore-replace exclusive cutover
  implemented ([replace cutover](../changes/2026-09-27-phase6-replace-cutover.json));
  test-restore unit/contract. Full live replace UI browser proof residual.
- [x] Controller-absent policy Run now + adoption proven on bounded Campaign A; unit ticks
  cover schedule ownership. Full multi-occurrence wall-clock schedule span residual.
- [x] Protected points / catalog rebuild covered by unit tests + Campaign B re-run catalog
  continuity after promote (BUG-0651). Concurrent prune stress residual.
- [x] Corrupt/missing backup refusal covered by hermetic CI regression
  (`TestPhase6BackupHermeticCIRegression`). Broader live fault matrix residual.
- [x] Core catalog rebuild and Controller-absent restore pass on bounded nested campaigns;
  Controller identity export remains separate.
- [ ] Three-host Controller-loss and master-host-loss campaigns on physical failure domains.
  **Accepted residual:** bounded nested Campaign A/B recorded; full three-host simultaneous
  join + schedule wall-clock span not fully proven.
- [x] Backups UI exercised on disposable lab
  ([browser QA](../qa/2026-09-27-phase6-backup-browser.md)); Overview tile wired to catalog;
  settlement bugs fixed in code (BUG-0649/0650). Live browser re-walk after fix may remain
  (accepted residual).
- [ ] Independent usability, responsive/theme/accessibility review and guide-only recovery
  rehearsal. **Accepted residual** for disposable close.
- [x] Candidate package/native/browser evidence and API/docs/privacy gates recorded for
  disposable scope (`0.4.12` + overlay —
  [lab install](../qa/2026-09-27-phase6-lab-install.md)). Forge CI on published push **not
  yet inspected** (agents do not push) — accepted residual needing owner push.
- [ ] Measured backup/restore durations published for RPO/RTO claims.
  **Accepted residual:** limits named; durations unpublished.
- [x] Project owner accepts disposable virtual-lab evidence and status updates
  (platform plan, changelog, roadmap, close record) 2026-09-27. Physical/production
  certification is not claimed.

No checkbox is checked by writing this plan. “Implemented”, “locally tested”, “native verified” and
“phase accepted” are distinct statuses. Required failed, missing or unexecuted rows keep the relevant
phase open; any material scope reduction requires an explicit recorded owner decision.

## Research and this change's assessment

Official documentation was reviewed on 2026-09-25. This was document research, not interaction with
another running UI. Workflow requirements above follow Nex's product contract and console reference.
Technical references used to constrain the native experiment:

- [Libvirt snapshot XML and lifecycle](https://libvirt.org/formatsnapshot.html): external snapshots
  change the active disk head; validate chain and configuration together, including interrupted deletion.
- [Libvirt full backup guidance](https://libvirt.org/kbase/live_full_disk_backup.html): prefer the
  native full-backup API; guest shutdown behavior depends on installed version and requires testing.
- [Libvirt backup XML](https://libvirt.org/formatbackup.html): explicitly include every writable disk
  and validate output; omission of a disk list and an incomplete explicit list have different semantics.

**Guide impact: none for this plan**; it does not change an available operator workflow. Implemented
slices require the guide updates and walkthroughs above. **State owner: none for this plan**; future
Core/repository ownership is specified above; the former Controller backup exception is superseded.
**Original planning assessment:** the initial plan did not change roadmap status.
**Checkpoint, 2026-09-27: Phase 5 closed** for disposable virtual-lab scope.
**Checkpoint, 2026-09-27: Phase 6 closed** for disposable virtual-lab scope —
[close record](../qa/2026-09-27-phase6-gate/CLOSED.md). Index:
[docs/qa/2026-09-27-phase6-implementation-index.md](../qa/2026-09-27-phase6-implementation-index.md).
Physical/production certification is not claimed. Snapshot product evidence for Phase 5
close is indexed in
[docs/qa/2026-09-26-phase5-gate/CLOSED.md](../qa/2026-09-26-phase5-gate/CLOSED.md).
The original 2026-09-25 planning authorship of this section claimed no runtime evidence by
itself; later implemented slices and the close records supply the executed checks.
