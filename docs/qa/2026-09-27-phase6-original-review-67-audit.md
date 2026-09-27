# Phase 5/6 staged review: original 67 findings audit

Date: 2026-09-27. This matrix preserves the numbering and sections of the original staged-code review. **Source fixed** means the current worktree contains a correction; it does not mean the corrected candidate passed native acceptance. **Evidence open** means the reported behavior still needs the named walkthrough or fault test. The original review ran focused Go checks, UI typecheck/build, the API generator, documentation and privacy checks; it did not complete a full race run, browser walk, or native NFS/libvirt reproduction. Later evidence is linked below. The historical Phase 6 close covers an earlier binary overlay; [current candidate acceptance is open](2026-09-27-phase6-gate/CLOSED.md#subsequent-review).

## Core and API: operation blockers

| # | Original finding | Current status | Strongest evidence and limit |
|---|---|---|---|
| 1 | Controller repository lacks usable mount | Source fixed; evidence open | Core derives the NFS mount; [review](2026-09-27-phase6-post-close-review.md#review-findings-and-follow-up) records source checks. Changed-candidate repository creation and remount need native rerun. |
| 2 | Local directory passes as external NFS | Source fixed; evidence open | Core checks mounted source and owner; native unmount/wrong-source refusal remains unwalked. |
| 3 | Member manual backup/restore rejected | Source fixed; evidence open | Authenticated master/member routing added; one member test restore completed [Failed Verify and Cleanup](2026-09-27-phase6-post-close-review.md#bounded-native-member-test-restore-and-peer-status-correction). Full member backup/restore matrix remains open. |
| 4 | Scheduled master backup conflicts with reservation | Source fixed; evidence open | Focused schedule/agent tests passed; full wall-clock schedule run remains open in the [plan](../plans/2026-09-25-recovery-snapshots-backups-restores.md). |
| 5 | Member schedule remains pending | Source fixed; evidence open | Peer dispatch has focused tests; full member wall-clock schedule and promotion span remains open. |
| 6 | Five Controller actions panic after commit | Source fixed | Focused Controller backup tests passed in the [review](2026-09-27-phase6-post-close-review.md#verification-performed-for-this-note); no changed-candidate native sweep of all five actions is recorded. |
| 7 | Restore VM ID path escape | Source fixed | Boundary validation was tightened; focused Go checks are recorded in the [change assessment](../changes/2026-09-27-phase6-post-close-doc-correction.json). No hostile-path native run is claimed. |
| 8 | Core-local restore duplicates a pool VM ID | Source fixed; evidence open | Pool-master reservation and exact owner release have focused checks ([BUG-0654](../BUG_LEDGER.md#bug-0654--definite-restore-placement-refusal-stranded-a-provisional-vm-owner)); native cross-host collision/release remains open. |
| 9 | Controller restore lacks pin | Source fixed; evidence open | Controller-dispatched run reservation/pin path has focused regressions ([BUG-0649](../BUG_LEDGER.md#bug-0649--backup-now-exact-retry-failed-after-a-published-recovery-point), [BUG-0650](../BUG_LEDGER.md#bug-0650--restore-as-new-needs_attention-with-missing-pool-backup-runs-journal)); interrupted native restore/prune remains open. |
| 10 | Online target directory may deny QEMU | Source fixed; evidence open | Permission correction is source checked; distinct-user native online backup remains unproven. |

## Backup and restore integrity and recovery

| # | Original finding | Current status | Strongest evidence and limit |
|---|---|---|---|
| 11 | NVRAM/vTPM absent on restore | Source fixed; evidence open | Disposable UEFI/vTPM restore reached shut-off success and isolated firmware start; guest OS and TPM-sealed application data remain unproven ([review](2026-09-27-phase6-post-close-review.md#native-first-use-tpm-refusal-and-pending-retry)). |
| 12 | Firmware copy lacks digest check | Source fixed; evidence open | Bounded artifact install and digest checks have focused Go tests; native post-copy hashes matched on the disposable v5 restore. Fault-injected corrupt firmware was not run. |
| 13 | Selected repository/point/source not bound | Source fixed; evidence open | Focused agent/protocol/vmhost checks passed for master/member immutable catalog digest binding, changed valid manifest refusal, and fail-closed legacy/older-member paths. Independent source review found no blocker; native tamper and mixed-version recovery remain untested. |
| 14 | Replace accepts another VM's point | Source fixed; evidence open | Source VM identity is checked; no changed-candidate destructive wrong-source native Replace run is recorded. |
| 15 | Interrupted online backup may delete active output | Source fixed; evidence open | Native-job outcome is reconciled before cleanup; interrupted QEMU/NFS fault walkthrough remains open. |
| 16 | Uncertain define settled failure and unpinned | Source fixed; evidence open | `needs_attention` retains pin; focused placement and exact resume regressions passed ([review](2026-09-27-phase6-post-close-review.md#test-vm-origin-proof-correction)). Native lost-reply injection remains open. |
| 17 | Replace retry may clear pin after cutover | Source fixed; evidence open | Durable cutover journal reconciliation is source checked; native interrupted Replace retry remains open. |
| 18 | Partial copy cannot exact-resume | Source fixed; evidence open | Exact artifact/digest resume has focused tests; v5 native allocating restore resumed after Core restart without a new POST. Other interruption points remain open. |
| 19 | Target directory predates intent journal | Source fixed; evidence open | Placement now journals/binds native intent before effects; crash between filesystem steps remains unit-level only. |
| 20 | Published retry trusts damaged payload | Source fixed; evidence open | Exact retry rehashes retained payload; [BUG-0649](../BUG_LEDGER.md#bug-0649--backup-now-exact-retry-failed-after-a-published-recovery-point) has a focused regression. Native corruption plus lost-reply retry remains open. |
| 21 | Retry records wrong capture time | Source fixed | Exact retry takes immutable point metadata; focused publish retry regression passed ([BUG-0649](../BUG_LEDGER.md#bug-0649--backup-now-exact-retry-failed-after-a-published-recovery-point)). |
| 22 | Disk digest reads whole image | Source fixed | Streaming digest replaced whole-file read; focused package tests passed. No large-disk memory profile is claimed. |
| 23 | Manifest limit checked after allocation | Source fixed | Bounded read added; focused package tests passed. No live oversized NFS response test is claimed. |
| 24 | Captured-spec digest unchecked | Source fixed | Digest is recomputed at the protocol boundary; focused protocol tests passed. |
| 25 | Rebuild drops catalog before scan | Source fixed; evidence open | Rebuild verifies access, retains protected/pinned missing entries, and surfaces repair errors; injected failures passed focused tests ([review](2026-09-27-phase6-post-close-review.md#review-findings-and-follow-up)). Native interrupted rebuild remains open. |
| 26 | Test verification lacks origin proof | Source fixed; evidence open | Exact successful run and current native creator proof added; one member Failed Verify succeeded natively. Passed and reused-ID native cases remain open ([review](2026-09-27-phase6-post-close-review.md#bounded-native-member-test-restore-and-peer-status-correction)). |
| 27 | Cleanup leaves test VM/disks | Source fixed; evidence open | One typed-ID member Cleanup removed native domain, disk, firmware, owner and pin. Interrupted cleanup/promotion replay remains open ([review](2026-09-27-phase6-post-close-review.md#bounded-native-member-test-restore-and-peer-status-correction)). |

## Authority, policy, scheduling, and Controller contracts

| # | Original finding | Current status | Strongest evidence and limit |
|---|---|---|---|
| 28 | Mutations bypass transfer freeze | Source fixed; evidence open | Authority writes are serialized/preflighted; focused transfer tests passed. Native concurrent mutation/promotion remains open. |
| 29 | Promotion truncates retained state | Source fixed; evidence open | Signed paged transfer retains full admitted history; >5,000-run and >60 MiB focused tests passed. Near-boundary native transfer/promotion remains open ([BUG-0657](../BUG_LEDGER.md#bug-0657--backup-history-growth-could-outpace-the-signed-member-replica)). |
| 30 | Promotion omits restore pins | Source fixed; evidence open | Authority carries per-operation pins; focused activation tests passed. Native unresolved-pin promotion/prune remains open ([BUG-0655](../BUG_LEDGER.md#bug-0655--one-restore-pin-blocked-another-target-using-the-same-point)). |
| 31 | Scheduler writes Store off owner loop | Source fixed | Tick mutations moved to serialized owner path; focused agent/race checks passed in the original review follow-up. Full wall-clock outage span remains open. |
| 32 | Downtime enumerates stale slots | Source fixed | Due-slot selection now advances to latest eligible occurrence; focused policy tests passed. Long real-time outage is not claimed. |
| 33 | Scheduled retention never invoked | Source fixed; evidence open | Run policy identity is preserved through settlement; focused policy tests passed. Native schedule-to-prune span remains open. |
| 34 | Prune removes catalog only | Source fixed; evidence open | Repository payload prune and protected/pinned checks added; the unused catalog-only helper was removed. Concurrent native prune/rebuild stress remains open. |
| 35 | Retention ignores reviewed revision/errors | Source fixed; evidence open | Run revision and prune errors are bound to settlement; focused policy tests passed. Native policy-edit-during-run/prune fault remains open. |
| 36 | Core-local mutations lack exact receipts | Source fixed; evidence open | Durable metadata receipts support exact replay and transfer; focused receipt/promotion checks passed. Native lost-reply/power-loss matrix remains open. |
| 37 | Run now becomes scheduled kind | Source fixed | Focused schedule tests passed; bounded [Campaign A](2026-09-27-phase6-campaign-a.md) exercised policy Run now without Controller. |
| 38 | Policy accepts unknown/foreign VMs | Source fixed | `TestBackupPolicyRefusesUnknownAndForeignVMs` passed; no native foreign-VM policy submission is claimed. |
| 39 | VM capture override ignored | Source fixed | Dispatch uses per-VM override; focused policy tests passed. Live mixed-mode coverage remains open. |
| 40 | Concurrency limits unenforced | Source fixed; evidence open | `TestBackupScheduleHonorsRepositoryConcurrencyLimit` passed; the host limit and saturated native multi-host schedule still lack this focused runtime evidence. |
| 41 | Policy PUT changes schedule ID | Source fixed | Controller keeps existing schedule identity; focused Controller policy tests passed. |
| 42 | Lost policy reply never settles | Source fixed; evidence open | Core durable outcome projection and Controller catch-up have focused regressions; live lost-reply policy result not recorded. |
| 43 | Rolling versions lack backup capability contract | Source fixed; evidence open | `backup-v1` capability acknowledgement and report gating documented in [API change assessment](../changes/2026-09-27-phase6-post-close-doc-correction.json). Rolling old/new binary pair not lab-tested. |
| 44 | Backup inventory lacks pool scope validation | Source fixed | Controller validates reported pool identity; focused Controller checks passed. |

## Web UI

| # | Original finding | Current status | Strongest evidence and limit |
|---|---|---|---|
| 45 | Repository retry changes resource ID | Source fixed; evidence open | `backup-repositories.test.mjs` checks retained repository/request IDs and frozen uncertain fields against source. No lost-reply browser replay was run. |
| 46 | Restore retry ID mishandled across edits | Source fixed; evidence open | `backups-retry.test.mjs` checks retained per-point/mode draft, request ID and frozen fields against source. Browser Replace confirmation was walked without a destructive POST or lost-reply replay ([browser rewalk](2026-09-27-phase6-post-close-review.md#current-candidate-browser-rewalk)). |
| 47 | Policy changed-payload retry shares ID | Source fixed; evidence open | `backup-policies.test.mjs` was red before and green after preserving the uncertain draft across tab switches; SSR checks the inactive tab removes focusable controls. No create lost-reply browser replay was run. |
| 48 | Test restore lacks Verify/Cleanup UI | Source fixed; evidence open | Browser fixture exercised separate test VMs, result controls and typed Cleanup at desktop/mobile; no Verify/Cleanup POST in that walk ([fixture rewalk](2026-09-27-phase6-post-close-review.md#test-restore-browser-fixture-rewalk)). |
| 49 | Replace lacks typed target confirmation | Source fixed | Live Controller browser rewalk required exact current VM name and held Next disabled until match; no destructive Replace submitted. |
| 50 | Policy errors hidden on active tab | Source fixed | UI tests/typecheck/build passed; policy HTTP failure was not browser walked in the current-candidate rewalk. |
| 51 | One inventory failure hides points | Source fixed | Synthetic catalog 503 displayed its own alert while repository selector remained populated. Other independent-failure combinations not walked. |
| 52 | Retention preview counts protected point | Source fixed | UI policy regression passed; no native preview/prune comparison run. |
| 53 | HTTP 202 leaves inventory stale | Source fixed; evidence open | Settlement refresh was added; current browser walk showed pending task and live catalog, without submitting a new backup/restore. |
| 54 | Row actions active during mutation | **Source fixed** | In-flight action disabling is green in the UI test/build checks; no concurrent-click browser test is claimed. |
| 55 | Run now only targets first VM | Source fixed | UI selection test passed; native selected-second-VM policy run not recorded. |
| 56 | VM Overview omits last test result | Source fixed | UI tests/typecheck/build passed; current browser rewalk did not inspect VM Overview. |
| 57 | Overview backup errors/labels wrong | Source fixed | UI tests/typecheck/build passed; no current-candidate Overview error-state browser capture recorded. |
| 58 | Overview ignores failed backup runs | Source fixed | Core-backed attention projection added; no browser failure-state walk recorded. |
| 59 | Replace UI allows stale/wrong host | Source fixed; evidence open | Live browser showed valid Replace targets and disabled action for missing sources; stale-host was seen on an earlier Controller B walk, not current C. Native stale-host refusal remains open. |
| 60 | HTTP 202 copy claims completion | Source fixed | Notices now distinguish acceptance from terminal result; UI checks passed. |
| 61 | Stale member access stays green | Source fixed; evidence open | Live Controller C showed member access unknown; stale-host warning was exercised earlier on B, not current C. |
| 62 | Weekly schedule allows no day | Source fixed | UI policy validation test passed; no browser invalid-week submission recorded. |
| 63 | Last check falls back to registration | Source fixed | UI check passed; browser repository view showed access status, without a never-tested repository fixture. |
| 64 | Deleted source VM options repeat | Source fixed | UI check passed; no multi-point duplicate-options browser fixture recorded. |

## Guides and acceptance evidence

| # | Original finding | Current status | Strongest evidence and limit |
|---|---|---|---|
| 65 | Close claim exceeds observed recovery | **Evidence open** | Historical [product lab](2026-09-27-phase6-product-backup-lab.md) did not prove restored guest marker/application readback. Later current-candidate browser and one UEFI firmware start/member Failed test cleanup narrow the gap but do not prove guest OS data, full three-host campaign or near-capacity transfer. [Gate](2026-09-27-phase6-gate/CLOSED.md#subsequent-review) explicitly reopens acceptance. |
| 66 | Recovery guides overstate checks | Source fixed; evidence open | Guides now distinguish the bounded native UEFI/vTPM restore-as-new from unrun Replace cutover and guest OS recovery. Guide-only changed-candidate recovery rehearsal remains open. |
| 67 | Recover-original and cleanup guide inaccurate | Source fixed; evidence open | Replace/test-restore guides now match supported actions; one native typed test cleanup passed, while operator recover-original walkthrough remains open. |

## Current acceptance boundary

The [post-close review](2026-09-27-phase6-post-close-review.md) records source corrections, focused tests, current-candidate browser walks, one native UEFI/vTPM restore, and one native member Failed Verify/Cleanup. Finding 13 is source fixed with native tamper/mixed-version evidence open; finding 65 remains an evidence gap. Interrupted native cutover/promotion, full changed-candidate three-host recovery, near-capacity authority transfer, guest OS and TPM-sealed application-data recovery, and published forge CI remain unproven. This matrix is an audit of findings, not a phase close decision.
