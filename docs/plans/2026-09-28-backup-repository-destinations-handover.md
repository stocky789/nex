# Backup repository destinations: continuation handover

Date: 2026-09-28. Status: **acceptance open**. This is a handover for
the [destination plan](2026-09-28-backup-repository-destinations.md), not a new
scope decision or a claim that any destination is released. The detailed
bounded evidence is in the [QA report](../qa/2026-09-28-backup-repository-destinations.md)
and [bug ledger](../BUG_LEDGER.md). All paths and identities below are public-safe.

## Fixture safety and worktree state

The project owner previously stopped work to release workstation resources;
lab work later resumed on disposable fixtures. The promoted NFS fixture was
restarted with matched current-source overlays while its former master stayed
fenced off. The latest restart reported one NFS repository and 1,054 runs;
the promoted fixture and all destination fixture guests were then stopped.
The later low-resource destination retest also ended with all selected guests,
Controller, tunnels and switch stopped. The fixtures, credentials and raw receipts remain in ignored
private `.cache/` directories; do not copy them into tracked documentation.
The later full-pool catalog and corruption checks also ended with all selected
Core, storage and private-switch processes stopped. A later fixture-owned
libvirt fencer setup could not attach its first Core guest to the host-side
private switch; the observed VDE callback used a separate container
`/var/tmp`, which is the likely cause.
No Core guest booted or promotion ran in that attempt; the labelled container
and all fixture processes were stopped. A later direct-monitor SSH fencer
resolved the fixture wiring and completed the four-destination promotion and
recovery check described below. The old master is now fenced off; do not boot
it. After the later checks, the promoted master, surviving member, storage
guest, private switch, Controller, tunnels and labelled fixture fencer were
stopped. The fixture and private receipts are preserved for remaining work.
The development worktree remains dirty with the implementation candidate and
documentation; preserve it. Agents do not commit, stage, push or change Git
history.
An operator-offered bare-metal test server received only a reachability check;
no candidate was installed or exercised there.

The tracked Core now advertises NFS, SMB3, S3 and SFTP after selected native
Core backup, restore and recovery gates passed. Most earlier non-NFS labs used
**ignored capability-only overlays**; a final tracked enabled-build browser
and product walkthrough remains open. FTP was removed from this
plan by project-owner decision and is out of scope. Complete retained backup
history beyond 64 MiB is required; truncation is unacceptable. SMB3 rotation
uses a new repository ID and a Core-owned policy/job switch. The current
candidate also rejects same-ID S3/SFTP destination or credential replacement
so retained points keep their original target.

Core owns repository definitions, credentials/receipts, health, policies,
backup/restore execution, catalog, run history and signed authority. A fresh
Controller must adopt those through authenticated Core APIs. API impact for
the combined candidate is **new contract**: typed repository and credential
contracts changed, and authenticated Core peer member-access POST was added;
missing exact peer backup operations now return 404. State owner is **Core**.
Guide impact is **updated** for all four destination workflows; the guide
labels the remaining final enabled-build and timed scheduler gates.

## What is already evidenced

- Local source/build checks passed on the latest worktree: serial full Go and
  Python suites (571 tests, one skip), web tests/typecheck/build, generated API
  reference, docs gate, privacy and diff checks. A disposable Core `.deb` built
  with placeholder appliance inputs contained all 19 expected vendored Go
  notices, byte-matched to source. Signed release package/ISO acceptance
  remains open.
  Focused red-before/green-after and race checks are recorded per bug in the
  ledger. No CI run or installed ISO/package acceptance is claimed.
- After BUG-0691/0692, a concurrent full Go run had one console-pairing
  timing failure; the focused test and Controller package passed alone, and
  serial `go test -p 1 ./... -count=1` passed every package. The final
  Python suite (570 tests, one skip), web test/typecheck/build, generated API
  reference, docs gate, privacy test and diff check also passed locally.
- Selected service profiles passed NFS/SMB mounts, SFTP pinned-key and abrupt
  guest-stop primitive durability, and single-node MinIO TLS, scoped key,
  conditional simple/multipart writes and abrupt guest-stop readback. A
  separate S3 service profile lost acknowledged objects and is unsupported.
- Each kind passed a bounded **same-host stopped two-disk backup and restore**
  with artifact/marker checks under lab overlays. NFS source disks were marker
  disks without a guest OS. S3 and SFTP restored disks were also read through
  an isolated Linux guest. SFTP additionally passed bounded test restore,
  replacement, protection, retention and rebuild; S3 passed replay, corrupt
  object refusal, protection, retention and rebuild. These checks do not cover
  the full matrix below.
- A **cross-host SFTP restore** later succeeded on master and member. Its
  restored VM had zero NICs; both disk markers matched in `qemu-io` and in a
  separate network-free, read-only Linux guest. The earlier refused peer
  operation remains `needs_attention` with no member operation; it was not
  replayed. BUG-0680 and BUG-0683 source fixes enabled the fresh request.
- A selected SMB3 policy switch used a new share/repository, preserved the
  Core policy's schedule/rules/scope/retention, rejected a stale revision, and
  kept old recovery points restorable. New-point marker readback was found in
  retained private receipts; it was not a new isolated boot. A selected SMB3
  scheduled backup settled and published one point while Controller was down.
- In matched disposable destination fixtures, S3, SFTP and SMB3 (`final2`)
  each passed Controller-submitted stopped two-disk backup, one backup
  run/point, and cross-host restore to member Core 2 after
  original disks were made unavailable. Every restored VM had zero NICs; both
  markers matched through `qemu-io` and a separate network-free read-only
  Linux guest. NFS passed the same bounded backup and restore sequence in an
  earlier matched build; `final3` repeated its cross-host restore after the
  peer NFS identity patch, with isolated readback and exact replay. Exact
  replays returned HTTP 200 without another run. All five
  repositories later reported online/readable/writable on both members after
  SMB3, S3 and SFTP outages; SMB3 recovery was delayed. Tracked SMB3, S3 and
  SFTP capabilities were disabled at that earlier checkpoint.
- A fresh Controller explicitly adopted three hosts in one pool, five
  repositories, five policies, nine catalog points and 30 runs from Core.
  Live browser read/panel review showed the four kind choices, health,
  offline warning, recovery points and policy-switch confirmation/Escape.
  Later live browser sessions submitted an SMB3 policy switch and SFTP
  credential handoff/registration. Loading, error, stale-data and destructive
  states still need full review.
- Signed paged v2 authority transfer preserved **1,052 directly seeded run
  IDs** across more than 69 MiB on both members. A fixture-owned old master
  was fenced and verified off before member promotion. The new master and
  Controller returned all 1,052 IDs through 34 authenticated pages; the NFS
  repository and metadata receipt survived. Direct seeding is simulation of
  history volume. The original promotion task later reconciled to success on
  exact replay without a second fence; the mixed-version inventory recovery
  path has only in-process regression coverage.
- A bounded **post-promotion NFS backup and restore** passed after BUG-0687
  exact settlement retry and BUG-0689 pending-run reconciliation. The backup
  retained its original manifest and two disk hashes, published one catalog
  point, and returned HTTP 202/`succeeded` on exact replay. The exact old
  restore operation settled after a matched-overlay restart. Its zero-NIC VM
  started and stopped in isolation; a separate network-free read-only Linux
  guest read both disk markers. The marker disks have no guest OS. The 1,052
  seeded run IDs remained intact; the surviving member's signed generation 19
  matched the master's 1,054 runs. This does not close the common matrix.
- A bootable two-disk, zero-NIC guest passed **NFS online crash-consistent
  backup and independent cross-host restore**: read-only disk markers matched
  and an isolated boot changed the boot marker. A separate authenticated NFS
  request lost its HTTP reply, but Core retained one succeeded run/point and
  exact replay. After BUG-0691 moved QEMU staging out of Core-private state,
  S3, SFTP and SMB3 each passed online two-disk backup with one run/point and
  exact replay under ignored capability overlays. Original disks were made
  unavailable for each cross-host restore; two marker hashes matched, zero
  NICs remained, and an isolated boot updated the boot marker. SFTP/SMB3
  restores settled directly. BUG-0692 left S3's master run pending after its
  member succeeded; restarting matched fixed Core reconciled the **same**
  restore to succeeded/registered and exact Controller replay returned HTTP
  200 without another VM. A deliberately inaccessible QEMU work root settled
  a new S3 attempt as one failed run with no point, then the disposable mode
  was restored. The wider fault, lifecycle and promotion matrix remains open.
- With Controller absent, new disposable S3 and SFTP Core-owned policies each
  dispatched at a separate UTC slot for one shut-off two-disk VM. Each settled
  one succeeded/published run and one point in authenticated Core-local backup
  status. Both policies were then paused and the low-resource fixture stopped.
  Missed/interrupted dispatch and post-promotion schedules remain open.
- Fresh Controller-absent S3 and SFTP policies then ran with their selected
  services stopped before each slot. Each left one failed run and no point;
  authenticated Core-local status preserved the 19 existing points. Both
  probes returned online after service recovery, the fault policies were
  paused, and all fixture processes stopped. Mid-upload/publication loss,
  missed Core-down slots and alert deduplication remain open.
- On the matched candidate with Controller stopped, all four kinds passed
  Core-local test restore, isolated start and marker readback, `passed`
  verification, and exact test-VM/disk cleanup while their points remained.
  The S3 test guest required an exact-ID force stop after ACPI shutdown timed
  out; the others shut down normally. Interrupted cleanup and application
  recovery beyond the markers remain open.
- With Controller stopped, all four kinds passed paused-policy Run now and
  `keep_latest=1` retention on a shut-off two-disk VM while a protected old
  point survived. NFS and SMB3 passed selected missing-index catalog rebuild.
  An S3 rebuild exceeded the Core-local 30-second reply window; BUG-0695's
  asynchronous exact retry settled the same pending operation after restart
  with the catalog intact, but its selected index had already been restored.
  SFTP rebuild refused incomplete remote delete records in the older fixture;
  its selected index was restored from the rollback copy. A later full-pool
  check passed fresh S3 missing-index rebuild and a separate clean SFTP
  missing-index rebuild, each restoring one selected point byte-for-byte;
  interrupted prune recovery remains open. The clean SFTP repository also
  refused a modified disk artifact without leaving a VM; the original
  artifact then passed a fresh test restore and cleanup. With its index
  removed, catalog rebuild retained a digest error for the modified artifact;
  exact retry after byte-for-byte repair rebuilt the index. Selected NFS and
  SMB3 modified-disk test restores also failed on digest without leaving a VM
  or staging; their original artifacts were restored byte-for-byte.
- The final matched three-Core fixture passed **fenced promotion with all four
  destinations present**. The old master was stopped by the fixture fencer;
  six repository, 15 policy, 25 point and 77 run IDs survived unchanged.
  Fresh promoted-master access probes passed for NFS, SMB3, S3 and SFTP. One
  fresh stopped two-disk backup per kind published exactly one point; source
  disks were withheld for four restores. Both restored disks compared
  identical per kind, and each zero-NIC VM booted alone with expected system
  and data markers and a fresh boot timestamp. Exact replay reused all eight
  operations without duplicates. Final inventory had 29 points and 85 runs;
  the surviving member verified the final signed generation and digest. Four
  old pending runs became `needs_attention` during authority transfer. This
  was followed by four fresh **cross-host** Controller restores on the other
  member while the source disks were withheld. Each settled once as a stopped
  zero-NIC two-disk VM, booted in isolation, and had both expected disk markers
  and a fresh boot timestamp. Exact replays reused the same four operations;
  final inventory had 29 points and 89 runs, including exactly one new
  cross-host restore run per kind. Source disk hashes matched after return.
  NFS required a forced stop after an ACPI timeout; SMB3's first short boot
  was forced off, but a longer second boot and S3/SFTP shut down normally.
  Controller catalog paging twice timed out transiently after the restores,
  then recovered. This remains bounded post-promotion proof, not broad fault
  acceptance.
- A **fresh Controller** with a new empty data directory and CA enrolled the
  promoted master and surviving member after the prior Controller stopped.
  It adopted the same six repository, 15 policy and 29 point ID sets and
  reported an 89-run total.
  It then completed one fresh stopped two-disk backup per kind with one point
  and run each; exact replays were duplicate-free. A fresh NFS point restored
  cross-host while the source disks were withheld, booted alone with both
  markers and a new boot timestamp, and shut down cleanly. Exact replay and
  source hash return passed. Final inventory was 33 points and 94 runs; a
  complete two-page Controller read matched all 94 Core-local run IDs. The
  surviving member verified the promoted master's signed generation/digest.
  The fenced former master was not enrolled into the fresh Controller.
  Mixed-version replacement was still open at this checkpoint; the later
  BUG-0684 check below covers the historical-Controller task repair.
- Four staggered daily policies created through the fresh Controller then
  dispatched with Controller and tunnels stopped. Each destination published
  one succeeded Core-owned scheduled run and point on the promoted master.
  Authenticated Core-local inventory ended at 37 points, 98 runs and 19
  policies. The four test policies were paused through Core-local API, and
  the surviving member verified the final signed generation/digest. Missed
  Core-down slots, interrupted publication and alert deduplication remain
  open.
- The fresh Controller displayed the fenced former master in the pool's
  Members view but could not name it from Core's legacy membership and linked
  to a missing Hosts panel. BUG-0696 adds an optional Core-owned hostname to
  future membership records and renders an unenrolled member without a Hosts
  link. Focused Core, Controller and UI checks passed; the existing fixture
  cannot reconstruct a name never saved by its older Core record.
  Independent review found legacy-upgrade and signed-replica compatibility
  gaps; Core now backfills only after every ready peer proves the new format
  over pinned mTLS and gates Controller inventory behind an explicit
  acknowledgement. A lost-response legacy join replay regression passed
  after an ordering correction. In a separate new disposable pool, current
  Core saved both joined hostnames in master membership and a verified signed
  member replica. A fresh empty Controller enrolled only the live master and
  displayed the offline member by that Core name with unknown connection,
  while Hosts had no row. A later live headless browser pass showed that name,
  Unknown connection and no Hosts link; repository/policy loaded, empty,
  keyboard and confirmation states passed. Error and empty API responses were
  browser mocks. All new fixture processes stopped. Native mixed-version
  upgrade remains open.
- Five fresh new-ID registrations on the promoted fixture failed as intended:
  missing NFS export, wrong SMB3 password, wrong S3 key, wrong SFTP host key,
  and S3 TLS hostname mismatch. Existing four-kind Core access retests passed;
  six repository, 37 point and 98 run IDs stayed intact. All fixture processes
  stopped with the old master fenced. BUG-0697 now routes unused Core-private
  backup receipts through the existing redacted list and retire API, with
  repository, unresolved-operation and in-flight checks and a replay
  tombstone. Focused and race tests passed, and independent source review
  found no confirmed issue. A subsequent matched current-source lab retired
  the four receipts, kept five active receipts ineligible, preserved four-kind
  access and exact ID sets, and confirmed empty-secret tombstones plus SMB
  side-file removal. Broader credential rotation, fault and browser gates
  remain open.
- The fresh Controller then restored one retained post-promotion SMB3, S3 and
  SFTP point each on the other member while the source disks were withheld.
  Each produced one succeeded run and zero-NIC two-disk VM, booted alone,
  shut down cleanly, and yielded both expected hashes plus a new boot
  timestamp in read-only disk inspection. Exact replay reused each operation
  without duplicates. The source disks returned with original hashes;
  repository and point ID sets remained six and 37, while runs rose from 98
  to 101. One final unfiltered Controller inventory read failed transiently
  and succeeded on immediate full retry. The old master stayed fenced; this
  is bounded per-kind restore-after-replacement proof, not fault acceptance.
  All fixture guests, Controller, tunnels, switch and fencer were stopped
  afterward.
- A dedicated disposable NFS daily policy is staged for the **missed Core-down
  slot** gate. The promoted master, member, storage guest, Controller, tunnels,
  switch and fencer were all stopped before its first 2026-09-28 18:47 UTC
  slot; the second is 2026-09-29 18:47 UTC. Do not restart this fixture before
  2026-09-29 18:50 UTC. The pre-existing enabled SMB3 policy was paused and
  its prior state saved. No missed or catch-up result is claimed yet; the
  fixture has no automatic resume. Any source edits during the wait require
  a newly matched overlay before the result is used as final-candidate proof.
- Source review found BUG-0698: an interrupted NFS/SMB3 retention prune could
  leave a partial point that blocked exact settlement and catalog rebuild.
  Core now records a durable destination delete intent before removal and
  resumes only that validated intent, while an unrecorded partial point still
  fails. The regression failed before and passed after the fix; focused race
  and full agent checks passed. Independent review found a same-ID replacement
  risk, corrected with a second red/green regression and re-reviewed without
  another material finding. A separate disposable native fixture then used
  an ignored exact-point hook to stop Core during NFS and SMB3 prune, leaving
  a cataloged partial old point and matching durable intent. After verified
  process death and clean current-source capability-overlay restart, each
  retained operation finished its prune. Exact replay added no run/point,
  catalog rebuild passed, and new/protected points and source hashes survived.
  This controlled fault injection does not close broader fault acceptance.
  API impact: **existing contract** (retention/rebuild
  retry semantics); Core owns the intent and catalog. Guide impact: updated.
- Server-side write denial exposed BUG-0699: SMB3 restore first failed at a
  repository QEMU write probe before VM creation, although the protected point
  was readable. Core now performs mount, owner and share-marker checks without
  write probes for point/catalog reads; publication, prune and health still
  require writable verification. A focused red/green regression, race check,
  independent source review and current-source disposable SMB3 retest passed.
  The retest restored both protected disk markers while the mounted share's
  server denied writes, kept the five point IDs and source hashes, and reused
  the exact operation on replay. The fixture was stopped cleanly. API impact:
  **existing contract** (restore and catalog-read behavior, no schema change);
  Core owns the source and point. Guide impact: updated. A locally read-only
  CIFS remount and other full-target profiles remain untested.
  A follow-up exact-replay regression failed before and passed after removing
  a redundant write probe from uncertain published manual-backup settlement.
  Policy settlement still requires writes when retention pruning is due;
  native published-point replay under write denial remains open.
- Two new size-capped NFS/SMB3 repositories each published a protected small
  control point before a measured larger stopped backup reached remote
  `ENOSPC`. Both faults settled one failed run, no new point and no stage
  directory; exact replay reused each operation. Both protected controls
  restored to shut-off zero-NIC VMs with two matching disk markers, and the
  original five point IDs and source hashes survived. A failed first SMB3
  registration during Samba setup exposed BUG-0700: its new Core mount unit
  remained and blocked fresh same-ID requests. A failed-start regression and
  an overlapping-preparation regression each failed before correction and
  passed after; source review rechecked the serialized cleanup. A fresh
  current-source disposable Core then refused first registration against an
  absent SMB share without leaving a unit/mount/repo; exact replay kept the
  failure. With the share active, a fresh credential/request for that same
  repository ID succeeded and Core access passed. The failed receipt was
  automatically retired, while original point/source hashes survived.
  API impact: **existing contract**
  (failed first mount cleanup and new-request retry, no schema change); no new
  durable state or operator guide step.

## Promoted NFS fixture reconciliation completed

**Do not boot the fenced former master.** It remained off throughout this
bounded post-promotion check.

The first restore-as-new POST returned HTTP 503 before creating a run. A later
exact request registered a stopped zero-NIC VM but left its Core run
pending/reserved and journal at `needs_attention` when authority admission
failed. The focused BUG-0689 regression failed before and passed after the
reconciliation fix; independent source review passed. Restarting the matched
overlay automatically settled that same operation without another restore.
The journal, run and API reported `succeeded`; an isolated VM start/shutdown
and a separate network-free read-only Linux guest verified both markers.
The original 1,052 seeded ID-set digest stayed intact through 1,054 total
runs. The surviving member verified signed generation 19 with the master's
matching history. The detailed result is in the QA report.

## Unfinished source and integration work

| Item | Remaining action |
|---|---|
| BUG-0685 SFTP prune completion | The last source fix reopens and file-syncs a full completion marker after an ambiguous sync failure; its injected regression and focused race/broader SFTP checks passed and independent review approved it. The ledger now records the full-marker file-sync failure and red/green evidence. Native interrupted-write/crash proof remains open. |
| BUG-0688 backup member access | Core source now probes members through pinned authenticated peers; fixes save the exact transient member repository before S3/SFTP credential-gated probes, compare stable SMB server/share/marker identity, stamp master receipt time, expire observations, and refresh master access during periodic health probes. Focused red/green and race tests passed. A later matched overlay reported all five repositories online and readable/writable on both members after selected SFTP, S3 and SMB3 outages; SMB3 recovery was delayed. Live browser showed Core2 Unknown through 135 seconds off and fresh Accessible after restart while Core1 stayed Accessible. Master expiry and broader failure/restart remain open; no CI run exists. |
| BUG-0686 repository identity | S3/SFTP same-ID source or credential replacement now fails before preparation and under the commit lock; focused race tests and independent review passed. A selected native source-change POST and exact replay returned HTTP 503 while two retained points per kind stayed in place. Credential mutation and broader mixed-version checks remain open. Use a new ID for a moved/rotated destination. |
| BUG-0690 NFS repository identity | Same-ID NFS source changes now fail before native preparation; observed filesystem identity changes fail under the commit lock. A focused retained-point regression failed before and passed after the fix. In the promoted fixture, source-change POST and exact retry returned HTTP 503 with durable source, identity and generation unchanged. Authenticated peer NFS dispatch, member probes and promotion now carry the master's identity; a focused member test passed. A later matched disposable fixture changed the export's real NFSv4 FSID: member access failed and same-ID registration/exact replay returned HTTP 503; after restoring the original export, the unchanged repository restored an old point with matching two-disk markers, passed verification and removed the test VM/disks while retaining the point. A separate current-master/historical-member native backup refused a changed FSID before member work, retaining exact `needs_attention` replay; old-point restore and byte comparison passed after the original FSID returned. The first reverse attempt was blocked before peer admission because the old master held its global slot in the already fixed BUG-0683 loop; upgrading that master to current Core let a distinct retained operation settle once. A second clean pool then proved historical-master/current-member refusal on a changed real FSID before member work, exact attention replay, and byte-identical old-point restore after the original FSID returned. Both directions leave the fault master run pending/attention for manual reconciliation; moves use a new ID. |
| BUG-0691 online remote staging | S3/SFTP online capture now stages under a private QEMU-owned libvirt work root; pre-capture work-root errors settle failed rather than uncertain. Focused red/green and native S3/SFTP online capture, plus a deliberately inaccessible-root failed-run/replay check, passed. Earlier uncertain fixture operations remain unresolved evidence; no product replay is claimed for them. |
| BUG-0692 peer point verification | A full S3 artifact readback exceeded the short peer HTTP request context after a member restore succeeded. The fixed Core verifies under a separate bounded context and retries transient reads; focused red/green and native same-operation reconciliation passed. Large-volume verification and interruption remain open. |
| BUG-0693 terminal staging cleanup | An independent candidate review found failed captures/uploads could retain full local staged disks. Core now removes staging after terminal failures while preserving it if an online native job may still be active. A partial-conversion regression failed before and passed after the fix. A selected disposable NFS stopped-backup conversion fault settled one failed run, removed its staging, published no point and replayed the exact failure. Other-kind and broader online/interrupted-upload cleanup remain open. |
| BUG-0684 mixed-version promotion | Same-version Core/Controller task convergence passed natively. In a separate clean two-Core fixture, a verified historical Controller retained `needs_attention` after acknowledging the Core's exact successful durable promotion. Replacing only Controller with current on the same data/certificate settled that row from authenticated Core inventory without a fresh result; exact replay returned the same succeeded ID, transfer/journal bytes and fencer command count did not change, and the old master stayed off. |
| BUG-0681 authority push ordering | Source race regression passed. The current matched fixture's surviving member verified the promoted master's final signed generation and digest after eight new backup/restore runs. Interrupted/overlapping pushes still need native proof. |
| Controller Run now | Source collision fix and Core-local Run now retest passed. Final-candidate Controller POST, exact replay, settled operation and one run/point passed for all four destinations in a disposable lab. S3 also passed discarded-response exact retry. Broader interruption cases remain open. |
| BUG-0694 offline registration | The first live browser SFTP registration failed because Core required credential delivery to an offline ready member before commit. A red/green regression and independent source review passed after Core validated the master credential, committed the repository, and retried member delivery through periodic probes. On a matched disposable build, a second live browser handoff and registration succeeded with one member off; the reconnecting member gained read/write access and a verified signed repository manifest. Promotion and broad fault checks remain open. |
| BUG-0695 catalog rebuild timeout | A native S3 scan outlived the Core-local HTTP window and exact replay could not resume its pending receipt. A focused red/green retry regression passed; Core now returns `202/pending`, scans on a bounded worker and settles the same receipt. Native S3 exact retry succeeded after restart with 24 catalog points preserved. The selected S3 index had been restored before that retry, but a later full-pool test removed it again and passed a fresh rebuild. A clean SFTP repository also passed missing-index rebuild; the older SFTP fixture's incomplete delete records still block its own rebuild. Interrupted prune remains open. |
| BUG-0701 SFTP pending-prune read | Source review found that an intact manifest remained readable after a durable SFTP delete record was published. Core now refuses that read while retaining exact delete retry. The focused protocol check was red before, green after and passed focused race checking; independent review found no concrete issue. A matched native permission fault later kept the manifest intact while restore and rebuild refused the pending point; exact prune recovery and rebuild succeeded after permission returned. Abrupt syscall/crash proof remains open. |
| BUG-0702 published policy retention retry | A native S3 manifest-delete refusal left a durable delete intent, one newly published point and a `needs_attention` Run-now operation. Restore/rebuild refused the pending old point, but exact Controller replay after permission recovery and Core restart stayed `needs_attention`; an SFTP run for the same VM remained pending. A Core periodic worker now reconciles the published point and retention under the same run ID for local scheduled and Run-now work, without recapture. Focused Run-loop red/green, race and cancellation checks passed; independent review found no remaining concrete issue after the prune context was bounded. A matched fixed-build restart autonomously settled that S3 operation before Controller, and a fresh SFTP post-intent permission fault then followed the same denied/recovered sequence. Each had one run/new point, successful exact replay, and intact protected/original controls. Abrupt crash and broader fault proof remain open. |
| BUG-0703 SFTP write denial error | A new isolated read-only OpenSSH root allowed protected-point restore but a fresh backup settled failed with a misleading retained-work conflict, despite server permission denial and no new point or staging. Core's ambiguous owner/intent write checks had discarded the write error after confirming no file existed; resumed point creation and some read failures shared the classification gap. The focused regression was red before and green after, including exact intent retry; race and full agent tests passed and independent source review found no remaining concrete issue. A matched fixed-build OpenSSH retry then reported `permission denied` with one failed run, no point/stage, stable exact replay and intact protected/original controls. |
| BUG-0704 SFTP partial publication | A size-capped OpenSSH target left a partial disk plus intent after a terminal failed backup; no point appeared and a protected two-disk control restored under the full condition. Core now stages the existing manifest before artifact bytes and retains uncertain SFTP publication under the same `needs_attention` run. An authority-checked durable abort marker cleans only validated unpublished files and resumes after interruption; completed publication is checked first. The first native fixed-build restart stayed `needs_attention` because a zero-byte abort marker blocked reconciliation; a through-reconciliation regression caught it. A corrected matched restart later cleared the preserved partial/stage/intent/abort without manual deletion and settled one terminal failed run with stable replay and prior catalog hashes intact. The later BUG-0706 guarded build restored/verified/cleaned the protected two-disk control, reported healthy access and retained exact catalog and source hashes. The old hash-only partial is preserved for inspection. |
| BUG-0705 missing restore point | A Controller restore of an incomplete SFTP point created no Core run, pin or VM, but remained `needs_attention`. The Core report loop now terminal-fails definite missing/invalid pre-reservation requests while retaining uncertain errors. Focused red/green regression covers a registered repository, no run/pin/VM and exact replay. A matched native OpenSSH retest returned terminal `failed` with no Core run, pin, VM or disk and stable exact replay. |
| BUG-0706 SFTP abort symlink point | Independent review found incomplete-upload abort could follow a symlink at the point directory and delete files in its target. A focused regression was red before and green after the Core abort now requires a real point directory before descent. Focused race and serial full Go passed; independent correction review found no further concrete defect. A matched guarded build preserved the exact catalog hashes and passed protected two-disk restore, verification, cleanup and health recovery. The symlink refusal itself has source-level proof only; forge CI remains open. |
| BUG-0707 member policy Run now | A disposable Controller Run now for a member-hosted VM failed on the master without reaching the member because the durable run omitted its host. Core now binds the resolved VM host before reservation, repairs old blank-host metadata reservations before dispatch, and refuses changed placement or an old already-dispatched blank-host member run. Focused red/green and race checks plus full agent tests passed; independent review of the correction found no concrete safety issue. On a frozen-source two-Core retest, the master run named Core2, Core2 journal and Controller task succeeded, exact replay held one published point, and prior catalog/source hashes and signed replica verified. The historical failed request remained failed. |
| BUG-0708 S3 ambiguous final publication | Source review found a lost final manifest reply plus failed read could terminal-fail a published point, and final-write denial left uncataloged artifacts. Core now retains attention, verifies exact publication, and aborts only after same-key conditional tombstones fence late final and artifact writes; intent/abort records make cleanup resumable. Independent P1 final-write and P2 artifact-orphan findings corrected the initial design. Fake-S3 adversarial tests pass. Native S3Proxy final-manifest 403 after complete artifacts retained attention, then same-ID restart failed terminally with exact abort tombstones, no catalog point, stable replay and protected two-disk restore. A separate committed-final PUT with dropped reply and nine failed reads autonomously settled succeeded under the same ID, one point and unchanged object/old catalog hashes. The single-process filesystem and fixture-only fault limits apply. |
| BUG-0709 member publication attention | The Core periodic publication worker excluded accepted member peer backups. A red/green focused predicate check now permits only an accepted member journal with a matching run and command intent when the older run recorded one; focused race and independent read-only review passed. In a matched two-Core NFS fixture, a private post-sync/readback-error overlay left a member operation in attention; after Controller stopped and normal Cores restarted, both journals/runs settled succeeded autonomously. Fresh Controller adoption and exact replay held one point, 25 prior catalog hashes and protected/source hashes. The readback fault was injected; no server error is claimed. |
| BUG-0710 SFTP delayed final rename | Independent review found abort could delete artifacts after checking no final manifest while a lost-reply final rename remained in flight. Core now atomically claims the exact staged source before artifact deletion, so either final publication or abort wins. A final-wins adversarial regression failed before and passed after; the symmetric abort-wins test also passed focused race and preserved a published control. Full agent and independent correction review passed. Matched native final-manifest denial, recovery and protected two-disk control passed; a delayed-rename native race remains open. |
| BUG-0711 NFS/SMB3 publication reply | Final directory rename could succeed before a parent-sync error and Core could falsely fail an uncataloged point. Core now marks only rename/post-sync errors uncertain and checks the exact point; pre-rename errors still settle failed. Focused red/green, race and full local suites passed; independent correction review found no concrete issue. Private lost-reply overlays after real native rename and parent sync passed on NFS and SMB3 members: each settled one run/point with exact Controller replay and unchanged old catalog, protected point and source hashes. The SMB3 check retained 29 prior catalog ID/hash pairs. Actual server sync failures and forge CI remain open. |
| BUG-0712 S3 final versus abort marker | Independent review found a delayed final PUT could win after a separate abort marker was written, leaving a valid point permanently unreadable. Core now resolves the conditional final-key tombstone race before writing that marker. A final-wins adversarial test failed before and passed after; abort-wins, artifact and partial-retry tests plus focused race passed, and independent correction review found no blocker. Matched two-Core S3Proxy fixture operations proved both provider orders: final HTTP 200/tombstone HTTP 412 produced one succeeded member run/catalog point and exact replay with old hashes intact; tombstone HTTP 200/delayed final HTTP 412 produced one terminal failed run, no new point, exact failed replay and 30 unchanged prior catalog hashes. Single-process provider and fixture timing limits remain; forge CI is open. |
| BUG-0713 absent SMB3 final publication | A bounded SMB upload service stop terminal-failed one run/no point and stable replay. A separate complete-staging/pre-rename `smbd` stop left stage/final absent after recovery, but the same operation stayed `needs_attention` after exact replay and Core restart; 33 prior catalog hashes and protected/source hashes stayed intact. Filesystem reconciliation lacked an abort fence. A focused regression was red before and green after Core added an authority-checked, exact final-key O_EXCL marker, refusing existing staging and published points. Review prompted stale-authority and FIFO corrections; rebuild skips only a complete marker and interrupted marker writes resume. A matched SMB3 retest autonomously failed the preserved same ID without recapture, rebuilt the exact 33-point catalog, rejected a delayed directory rename over the marker with ENOTDIR, kept replay stable across restart and restored a protected two-disk point with matching markers/hashes. Fixture stopped; forge CI remains open. |
| S3 provider compatibility | The matched disposable MinIO fixture refused new repository registration because its wrong-ETag `DeleteObject If-Match` deleted the sentinel (204 and subsequent GET 404). Core correctly treated conditional delete as unproven and did not touch a protected repository. A separate pinned S3Proxy 4.1.1 single-process filesystem service passed signed conditional create/delete, same-key tombstone and multipart retry probes, Core registration, protected backup/restore and selected final-denial cleanup. Its conditional enforcement is bounded to one process; broader provider proof remains open. |
| Live browser workflow | A matched disposable Controller showed actual member Unknown through 135 seconds off and fresh Accessible after restart. One new paused policy, one new S3 repository with healthy master/member access, accepted-response-loss repository switch with exact retry and revisions 2→3→4, and typed-ID cleanup of a fresh two-disk test restore passed. Core pin/VM/disk absence, both pre-cleanup markers, exact replay and 28 prior hashes were checked; credentials were blank in a later new form. A later latest-source fixture passed real missing-CSRF 403 UI handling with no operation, lost-response Restore as new with exact 200 replay, typed-name Replace on only that new stopped zero-NIC VM, and actual master health Unknown after 152 seconds off then Online/recovered access. Active replacement disks carried both backup markers; old sentinel disks were retained/detached, exact replay and 31 prior catalog/source hashes held. Role-scoped permission was not tested because this build has only administrator sessions. |
| SFTP rename capability refusal | A separate OpenSSH service with all SFTP renames denied held a new Controller backup at master reservation, with no member journal or remote files, for about three minutes. Restoring rename let that same operation dispatch and publish within about eight seconds. This proves admission/health recovery, not final-manifest refusal; a boundary-specific fault remains open. |
| SFTP mid-upload outage | A matched OpenSSH session was killed after about 9 MiB of a 336 MiB artifact while service remained up; Core terminal-failed one run, removed the partial and intent, and exact replay held. A fresh operation then lost the isolated SFTP service at about 9 MiB; Core restart while service stayed down kept the same `needs_attention` run/operation and incomplete remote bytes. A repeated failed health probe advanced observation time without another offline transition. On service recovery Core cleared the exact partial/stage/intent, terminal-failed that run, and recorded recovered health; protected two-disk restore/verify/cleanup and 18 catalog/four source hashes passed. Final-manifest interruption and other-kind upload loss remain open. |
| In-flight restore copy versus retention | A genuine Core-local member Run now overlapped an SFTP restore paused after 24,707,072 of 102,432,768 first-disk bytes with a durable P2 pin. The new backup succeeded and retention pruned unpinned Q2 while keeping pinned P2, protected P/Q and newest R. The resumed restore passed both markers, exact replay, verification and typed cleanup; the pin cleared, source and 18 prior catalog hashes stayed intact, and the signed member replica verified. Repeat for other kinds and abrupt failures. |
| Remote full targets | An isolated size-capped OpenSSH target exhausted during artifact upload; the first pre-fix run failed with no point but left a partial disk and intent. A new matched upload retained one `needs_attention` run and durable abort marker, then Core cleared the exact partial/stage/intent/abort after capacity recovery and restarted, settling one failed run with stable replay and no point. A later guarded build restored/verified/cleaned the protected two-disk control, reported healthy access and retained exact catalog/source hashes. The older hash-only partial remains preserved privately for inspection. A separate capped MinIO instance refused a large multipart upload with HTTP 507 at its minimum-free threshold: one failed run/no point or incomplete upload, exact replay, protected two-disk control restore and verification under cap, and health recovery after filler removal passed. Literal S3 filesystem ENOSPC was not reached. |
| Credentials and health | Promoted-master access passed for all four kinds. Prove rotation/retirement, wrong credentials, member access after promotion, offline/recovered alert deduplication, stale/unreachable rendering and missed scheduled runs. Do not persist secrets in Controller state, logs or evidence. |

BUG-0704/0705/0706 API impact: **existing contract**. Core now reconciles
uncertain SFTP publication, terminal-fails definite missing/invalid restore
admission, and refuses unsafe symlink cleanup through existing result statuses;
no new route or schema was needed. Core owns the staged manifest, abort marker,
run and journal; a fresh Controller adopts their result through authenticated
Core inventory and operation APIs. At that checkpoint, guide impact was
**updated** for the supported restore-as-new missing-point failure while
SFTP was disabled. The current guide covers enabled SFTP.

## Original plan's common acceptance matrix still to run

Perform this **for each of NFS, SMB3, S3 and SFTP** through authenticated Core
APIs and the Controller UI. The same-host stopped paths above are partial
evidence; repeat on the final matched candidate where later source fixes
affect behavior.

| Gate | Remaining proof |
|---|---|
| Registration and policy | Register and test exact server/share/bucket/root and owner identity from each executing host; validate wrong pool/input/role, secret handoff and replay; create a policy naming the repository and show its Core-owned revision/health. |
| Capture | Broaden the now-passed stopped and online two-disk checks to service/publication interruption, remote hash faults and ambiguous/lost replies for every kind; prove accurate failed/missed runs when each repository is unavailable. |
| Independent restore | Repeat the now-passed source-disks-unavailable, cross-host isolated boot and marker checks after further source changes and across destination outages, version transitions and promotion. |
| Other lifecycle | Test restore, verification and cleanup passed in one matched, Controller-absent fixture for all four kinds. Protected old points and `keep_latest=1` retention passed for all four; selected missing-index rebuild passed for NFS, SMB3, S3 and a clean SFTP repository. Controlled native post-intent prune refusal and exact recovery passed for all four kinds, using partial deletion for NFS/SMB3 and service permission denial for S3/SFTP. A native SFTP test restore held a Core pin while a later retention run pruned an older unpinned point and retained the pinned, newest and protected points; cleanup cleared the pin. A later true SFTP restore-copy overlap also retained pinned P2 while a member backup pruned Q2, then restored both markers. NFS Replace passed with typed browser confirmation and active-disk marker readback on a disposable VM; an earlier SFTP replacement passed. Matched SMB3 and S3 exact-target API replacements each switched to new active disks with two backup markers, retained detached sentinel originals, passed replay/cleanup and kept 33 prior hashes; the generic typed-name UI was exercised on NFS. A latest-source SFTP fixture SIGKILLed Core after a durable delete record and artifact unlink; same-ID restart completed P's partial prune while retaining Q/protected control, exact replay, catalog rebuild, 35 old hashes and protected two-disk restore passed. A later pinned S3Proxy fixture SIGKILLed Core after P's durable delete record plus partial object removal; same Q run/replay/rebuild completed after restart, retained 36 prior hashes and passed protected two-disk restore. A selected S3 catalog rebuild also survived Core SIGKILL after its new protected index was saved: same-ID replay, 37 old hashes and two-disk restore held. Interrupted rebuild, selected corrupt-artifact rebuild refusal/repair, and pinned restore-copy/prune overlap now passed for all four. The older SFTP repository's unresolved delete records still block its own rebuild. |
| Outage and adoption | Four-kind fenced promotion, retained IDs, same-host backup/restore, cross-host booted restore and exact replay passed. A fresh Controller with no old database adopted the enrolled live hosts and stable repository/policy/point IDs plus the full Core-local run set, then completed four new backups and one cross-host NFS restore. It later restored retained SMB3, S3 and SFTP points cross-host with isolated boot, two-disk marker readback and exact replay. Four schedules dispatched with Controller off. The current-Core offline member name/unknown state/no Hosts link, mixed-version NFS peer refusals in both directions, and old-to-new Controller promotion task repair passed in separate fixtures. Continue missed Core-down slots and broader mixed-version recovery. |
| Fault matrix | A selected SFTP pinned host-key mismatch failed access and recovered after the correct key returned. Selected NFS, SMB3 and clean SFTP modified disk artifacts were refused without leaving a VM; restored artifacts compared byte-for-byte, and the repaired SFTP point passed a fresh test restore and cleanup. NFS/SMB3 controlled partial prune/restart passed with exact retry and protected controls. S3/SFTP controlled post-intent permission faults also recovered exactly after access returned. With server-side write denial, all four kinds each recorded one failed new backup/no point and exact replay; protected two-disk controls restored under denial for all four. The SMB3 restore initially failed at a write probe (BUG-0699), then a current-source overlay restored its protected point under the same denial with both disk markers verified. The SFTP pre-fix failure mislabeled permission denial as a conflict (BUG-0703); a matched fixed-build native check reported the true permission error. New size-capped NFS/SMB3 targets reached ENOSPC during artifact publication, each with one failed run/no new point, exact replay and protected two-disk control restore. A separate capped MinIO target refused multipart upload with HTTP 507, one failed run/no point or incomplete upload, exact replay and protected control restore under cap. The SFTP full-target partial cleaned after capacity recovery and protected restore/health passed on the guarded build; measured SFTP child and whole-service loss during upload also passed same-operation failure/recovery with offline alert dedup and protected controls. NFS/SMB3 post-sync lost-reply, S3 final/tombstone races, live browser lost-response restore and master health expiry passed in selected later fixtures. A latest-source NFS fixture stopped its server after 1 MiB staged upload and separately after a complete staged manifest/artifacts before final rename; each hard-mounted operation paused without a final point, resumed after service return, published once and survived replay/restart, with a protected two-disk restore and 31 prior hashes intact. A matched S3Proxy fixture stopped the provider after 1 MiB of multipart bytes sent upstream and separately after two durable artifacts before final manifest; both exact IDs failed after recovery/restart with no point, catalog rebuild kept 33 old hashes, and a protected two-disk restore passed. A separate SFTP exact-ID pre-rename pause plus isolated OpenSSH service kill terminal-failed the same run after recovery, left no incomplete point, retained 33 old hashes and passed protected two-disk restore/refusal checks. Selected upload/publication service-loss cells are now covered for all four kinds; continue per-kind lost API reply and selected unproven fault boundaries; exact retry without duplicate writable VM or lost prior point. Record per-server limits. |

An isolated latest-source SFTP catalog rebuild survived Core SIGKILL after
its new protected two-disk point's index was durably imported. The same
operation succeeded after restart and exact replay; 38 older ID/manifest
hashes and all 39 index hashes stayed exact, and protected restore and
cleanup passed. All fixture processes were stopped. The ignored exact-point
pause selected the crash boundary; see the QA report for evidence and limits.
An isolated NFS rebuild passed the same real Core-kill boundary: its rebuilt
index matched the stashed copy before the kill, the same operation settled
after normal restart, 39 older catalog entries and all 40 index hashes stayed
exact, and protected two-disk restore and cleanup passed. Its fixture was
fully stopped before the separate SMB3 check.
An isolated SMB3 rebuild then survived the same real Core kill after its
new index was durably saved. The same rebuild operation and exact replay
succeeded after normal restart; all 40 prior entries and 41 index hashes
stayed exact, and protected two-disk restore and cleanup passed.
An isolated NFS corruption check then refused a one-byte-mutated artifact
during restore and rebuild without creating a VM or importing the point.
Byte-exact repair let a Core-local exact-ID retry import it once while
preserving 41 older catalog hashes and the protected two-disk restore.
Controller still showed that same operation in `needs_attention` after Core
reported success. BUG-0714 now reconciles the matching Core journal from its
terminal receipt. A matched fixed-build NFS retest recovered the preserved
stale task on startup, then repeated one-byte corruption/refusal/byte-exact
repair on a fresh point: the same rebuild ID settled Core receipt, journal
and live Controller task, imported one point, retained 43 prior hashes and
passed protected two-disk restore/cleanup. Its fixture was stopped. SMB3/S3
corrupt-rebuild checks were next. SMB3 then passed a matched one-byte
corruption/refusal and byte-exact same-ID repair: Core receipt, journal and
live Controller task converged; 44 older catalog hashes and protected
two-disk restore stayed intact, and its fixture was stopped. S3 remains
open at that point. A separate pinned single-process S3Proxy prefix then
passed the same corrupt-artifact refusal and byte-exact same-ID repair:
signed GET proved the fixture-only mutated bytes, Core and Controller
converged without restart, 45 older catalog hashes and protected two-disk
restore stayed intact, and all fixture processes and container were stopped.
The selected S3 restore surfaced a generic conflict for a digest mismatch;
see the QA report for that limit. Corrupt-artifact rebuild refusal and repair
now have selected native evidence for all four kinds.
Two distinct disposable pool masters then raced different NFS repository IDs
against one fresh export subdirectory. One claimed the sole owner record;
the loser retained HTTP 503 on exact replay with no repository or point and
proved it could register a separate scratch target. The winner published and
restored a protected two-disk point, while 47 older index hashes and source
hashes stayed exact. HTTP 503 is a generic local API diagnostic limit, not a
required status-code failure in this plan. Its fixture was fully stopped;
SMB3/S3/SFTP cross-pool races followed; see the QA report.
The SMB3 variant passed on a fresh share directory: both Controller
registrations queued, Core retained one sole owner and failed the other
with a foreign-pool error, exact loser replay made no repository or point,
and the winner's protected two-disk backup/restore matched markers and full
disk hashes. The original pool's 48 catalog indexes and source hashes stayed
exact. Its fixture was stopped before S3 setup. S3/SFTP owner races followed.
The S3 variant passed on a fresh pinned S3Proxy prefix. Signed GET showed
the winner's sole owner object; the loser retained a failed exact replay
and had no repository. The winner's protected two-disk backup and restore
matched both markers and full disk hashes; older catalog and source hashes
and the owner hash stayed exact. Typed cleanup and full fixture shutdown
passed. SFTP was the remaining owner-race kind.
SFTP then passed on a fresh confined OpenSSH root with separate scoped
credential receipts: one pool retained the sole owner marker, the loser's
exact replay remained failed with no repository, and the winner's protected
two-disk backup and independent restore matched both markers and full disk
hashes. Older catalog, source and owner hashes stayed exact. Selected
cross-pool Core owner-conflict proof now covers all four kinds, and all
fixture processes were stopped. The barrier synchronized HTTP submissions;
it does not prove both remote claims hit the lock at the same microsecond.
An NFS master restore then held a durable P pin and a synced 4,096-byte
partial first disk while a Core-local member Run-now published R and pruned
only unpinned Q. The paused restore completed with both 96 MiB disk hashes
and ext4 markers matching the source; exact replays, typed cleanup and pin
removal passed. The 48 older catalog hashes and source disks stayed exact,
the master returned to the normal matched binary, and the fixture was fully
stopped. SMB3/S3 restore-copy overlap followed.
The SMB3 variant then passed on a fresh share directory: the master held a
durable P pin and 4,096 synced restore bytes while a member Run-now
published R and pruned only Q. Restored full disk hashes and markers,
replays, typed cleanup and pin removal passed; 51 older catalog hashes and
source hashes stayed exact. The normal binary was restored and the fixture
stopped. The S3 variant first proved pin/prune but its restore failed after
a long zero-byte fixture GetObject pause; exact replay remained failed with
no VM, disk or pin. A fresh isolated S3 prefix and sparse marker source kept
the real copy paused for 20.3 seconds after 4,096 synced bytes and a durable
P pin. Member R succeeded and pruned only Q; release completed the original
restore with both disk markers and full provider-artifact hashes matching.
Exact replays and typed cleanup passed; original catalog/source hashes and
12 old/new provider artifacts remained intact. The normal binary returned,
root was relocked, and all fixture processes stopped. Selected native
restore-copy/retention overlap now passed for all four kinds; the S3 check
is bounded to the pinned single-process S3Proxy service and short pause.
A separate SFTP Controller backup then lost its accepted HTTP 202 reply to
the caller through a private zero-byte downstream relay. Exact request replay
found one succeeded run and point; a protected two-disk SFTP control restored
with both markers and full hashes matching, then cleaned up. Sixty earlier
catalog hashes and source disks stayed exact, and all fixture processes
stopped. SMB3 accepted-reply loss then passed in a separate share directory:
the caller saw zero response bytes after Controller's accepted HTTP 202,
identical retry found one succeeded Core run/point, and a protected two-disk
control restored with full hashes and markers matching. The 62 earlier
catalog hashes and source disks stayed exact. A current-binary interrupted
restore gate followed on that healthy fixture. A private exact-operation
hook stopped an SMB3 restore after a durable pin and 4,096 synced local
bytes; the master Core was really SIGKILLed. Normal matched Core restart
autonomously completed the same restore ID and Controller task before
replay. One shut-off zero-NIC two-disk VM matched remote artifact hashes
and markers, exact replay made no duplicate, cleanup cleared VM/disks/pin,
all 64 catalog and source hashes stayed exact, and the fixture stopped.
This reruns the Phase 6 allocating-restore interruption on the changed
candidate; other interruption stages remain outside this selected gate.
Independent review then found BUG-0715: asynchronous repository preparation
could touch an empty remote target before master authority was checked at
commit. A shared pre-preparation master/authority check under the transfer
freeze lock now fences all four kinds; SFTP adoption refuses an absent owner
for an established identity. Member/freeze/adoption regressions failed before
their fixes and passed afterward, and independent correction review found no
blocker. A fresh matched two-Core SFTP fixture then registered an exact owner
on the master, observed readable/writable member access, published and
restored a protected two-disk point with full hashes and markers matching,
passed exact replays/cleanup and retained 64 prior catalog/source hashes.
All fixture processes stopped. A member-local new registration was refused
without remote files, but used a mismatched credential receipt because
Controller refused member handoff; valid-credential ordering has focused
source proof only. The
registration-only lock can delay a transfer freeze by the existing bounded
preparation timeout; a lawful precommit same-pool owner marker can remain if
freeze wins after preparation but before inventory commit, without a point.

All four passed bounded stopped and online cross-host recovery, Controller-off
scheduled dispatch and post-promotion same-host backup plus cross-host restore.
S3 and SFTP also passed real partial-prune Core-kill recovery; SFTP passed
pre-final service loss. Selected native cross-pool owner conflicts and pinned
restore-copy/prune overlap now passed for all four. The genuine two-slot
missed-run check, enabled UI review and final socket-dependent source gates
remain open. Independent read-only review found and corrected one stale
historical SFTP handover sentence. Do not repeat proven
crash boundaries merely to broaden the lab matrix.

## 2026-09-29 review and fix pass

A second independent source review found defects across every destination,
dispatch, flow, authority, credential and Controller area; BUG-0716 to
BUG-0736 fix them, and a cleanup removed old-version compatibility paths under
the owner's 2026-09-29 no-legacy decision. The
[destination plan](2026-09-28-backup-repository-destinations.md#2026-09-29-review-and-fix-pass)
lists what changed, the decisions made (no legacy compatibility; no fixed
`executeBackupNow` deadline) and the open owner decisions. Only focused unit
tests have run: **none of these fixes has lab, native-server or forge CI
evidence**. Before relying on earlier native results, detach lab NFS/SMB3
backup mount units written by an older build, then rerun the full test suites
and the native four-kind matrix on the current tree.

## Final delivery gates

1. SMB3/S3/SFTP capability advertisement and the operator guide are updated
   after the selected Core gates. Complete the final tracked enabled-build
   walkthrough of setup, permissions, errors, credential rotation and recovery.
   Complete live browser review of loading, empty, offline/stale, permission,
   credential, keyboard, uncertain-result and destructive-action states;
   inspect private captures and publish only sanitized conclusions.
2. Update [API.md](../API.md), OpenAPI contracts and generated reference for
   any further source change. Keep the [change record](../changes/2026-09-28-backup-repository-destinations.json)
   and [QA report](../qa/2026-09-28-backup-repository-destinations.md) aligned
   with actual results; BUG-0687 and BUG-0688 are recorded, and historical
   BUG-0683 native retest wording was corrected. Record API impact, guide
   impact and Core state ownership.
3. On the **final** worktree, run `go test ./...`,
   `python3 -m unittest discover -s tests`, web `npm test`, `npm run
   typecheck`, `npm run build`, `python3 scripts/generate_api.py --check`,
   `python3 scripts/check_docs.py --worktree`, the repository privacy test and
   `git diff --check`; run focused race, API and native checks for changed
   paths. BUG-0687 and BUG-0689 focused regressions passed. The latest full
   Go suite passed after BUG-0715's source correction but before tracked
   capability enablement. Python (571 tests, one skip) passed earlier, before
   the current session lost socket access. A later full Python attempt ran 568
   tests with 15 errors, two failures and one skip from denied sockets, GPG
   agent startup and systemd socket options. Current enabled-source focused
   capability, Controller web test/typecheck/build, API generation,
   documentation, privacy, diff, website check/build and API contract checks
   passed. The final serial Go suite also stopped on denied `httptest`
   loopback listeners in five packages. The final enabled-build
   socket-dependent suites and lab remain open; inspect actual failures/skips
   when socket access returns.
4. Independently review the final implementation and documentation after the
   remaining checks. A separate read-only reviewer found no findings in the
   scoped BUG-0691/0692 source and evidence batch. A broader independent
   read-only review found BUG-0693 and a stale NFS limit; both were corrected,
   and a read-only follow-up found no issue in the correction. The bounded package notice check does not certify a signed
   release package or ISO; recheck the component manifest, complete matching
   source and source-delivery path before distribution.
5. Project owner alone performs Git commits and remote operations. After an
   authorized push, inspect the actual forge CI run and required jobs; no CI
   result exists for this candidate yet. Do not claim the destination phase or
   parent recovery gate complete until the native matrix, safety/recovery,
   documentation and UI gates above pass.

This handover changes no implementation or API. API impact: **none** for this
document itself; it records the candidate's new/changed contracts. Guide
impact: **none**; no operator instruction changes here.
