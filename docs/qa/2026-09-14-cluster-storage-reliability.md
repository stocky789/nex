# Cluster storage reliability follow-up

Date: 2026-09-13 to 2026-09-14. Status: in progress; not production acceptance. Scope and
decisions: [concurrent storage plan](../plans/2026-09-12-concurrent-pool-storage.md). Causes and
fixes: [bug ledger](../BUG_LEDGER.md) (BUG-0156 to BUG-0173). Earlier evidence:
[concurrent pool storage](2026-09-12-concurrent-pool-storage.md).

## Current outcome

- Latest clean builds (BUG-0173, then the storage-cluster refactor) each passed two clean
  150-sample idle observations and fresh six-guest SMB, NFS and clustered-iSCSI GFS2 workloads,
  with no deadline or lock-probe journal lines. Bounded no-fault runs on one lab only.
- Passed bounded scenarios: stopped local/NFS attachment transition (BUG-0132), six-writer
  master-agent outage (diagnostic build), selected master activation replay (BUG-0135), and one
  complete Core3 natural-partition fence/return.
- Earlier candidates retained intermittent repository deadlines and failed loaded health despite
  passing payload integrity; those failures stay recorded below.

## Environment and method

- Existing disposable three-Core nested lab and active replacement Controller from the
  [handoff](../plans/2026-09-14-production-readiness-handoff.md). Initial read-only baseline:
  18 retained guest definitions (later 20), zero running guests, fresh Controller reports,
  three-member quorum, GFS2 mounts, iSCSI sessions, healthy CPU-model checks.
- Idle observation: 150 two-second samples over five minutes of member reconciliation health (the
  master has no member-reconciliation field). Failed samples repeating one producer timestamp
  count as one observation. Loaded observation: same sampler during six fsynced 600-write GFS2
  guest writers, or six-guest (two per Core) NFS/SMB write/reboot-hash/lock-refusal runs.
- Every rollout is sequential per Core with retained previous artifacts and verified pins
  (running executable and source archive), unchanged boot IDs, native cluster configuration,
  services and mounts, Controller identity, and all guests stopped with empty native
  running/autostart sets. "Final guard" means that check after a run.
- Diagnostic builds add only fixed stage names, backend kinds, hashed request correlation and
  monotonic durations (emitted only for slow/expired calls); no deadline, check, fencing or
  ownership change. They are not repairs. Builds/tests paused during observations.
- Raw receipts, journals, credentials, identifiers and diagnostic source snapshots are private;
  entries are sanitized summaries reviewed independently unless noted.

## Idle reconciliation health

| Candidate | Result | Detail |
|---|---|---|
| BUG-0156 fixed | Failed | One member 2 episodes (7 samples), other 1 (3); both recovered; journals lack backend |
| Diagnostic (flush split) | Failed | NFS 3.000 s (validation 2.103 s, reporting 853 ms); iSCSI 3.001 s (validation 2.155 s, locking 252 ms, reporting 572 ms); each 4 samples. Master access handlers ~350 ms file+dir flush plus up to ~500 ms mutex; no slow applied-inventory at 250 ms; SMB credential flushes under deadline; host I/O pressure elevated |
| BUG-0158 clean | Failed | Core2 1 sample/1 obs; Core3 4 samples/1 obs; no timings |
| BUG-0159 diagnostic | Failed | Core3 one iSCSI episode (2 samples): 3.000 s = validation 2.415 s (GFS metadata 311 ms, cluster health 328 ms, path 886 ms, QEMU 888 ms: create 360, sync 175, read 346), locking 556 ms, reporting 7 ms. Overlapping cluster worker 5.197 s. Topology: Core COW overlays on COW backing FS, shared raw LUN in writethrough mode |
| BUG-0159 clean (17:52:17.848–17:57:18.332 UTC, 09-13) | Failed | Core2 144/6 (one deadline obs in 5, one repository failure); Core3 147/3 (one repository failure in 2, one deadline from just before window) |
| BUG-0163 capped | Failed | Core2 149/1, Core3 147/3; one deadline each ~18:33:13/18:33:12 UTC; later NFS/SMB/GFS access current. Outer host: no CPU/memory pressure; backing device ~289 MiB written, busy ~14.8 s in 16 s. Pre-idle access capture overwritten (unusable) |
| BUG-0164 clean (18:56:02–19:01:01) | Failed | Core2 148/2 (obs 18:59:53.081); Core3 144/6 (obs 18:58:16.251, 18:59:52.120) |
| Correlated diagnostic, 371-path manifest (19:25:14–19:30:13) | Passed | 150 healthy each; NFS/SMB/GFS access passed |
| BUG-0165/0166 partial diagnostic (20:40:41.932–20:45:41.825) | Failed | Core2 150 healthy; Core3 141/9 in 3 obs (20:41:12 ×5, 20:42:27 ×2, 20:45:32 ×2). Validation 2.739 s (lock 240 ms), 2.323 s (lock 650 ms), 3.018 s incl. QEMU 2.34 s (traversal 383, create 406, sync 477, read 374, write 454, remove 246 ms); nearby journal-inspection commands 305–399 ms; no `tunegfs2` row. Correlations only |
| BUG-0167 clean (original-root fallback in practice) | Passed | 60/59 distinct obs. GFS root is root-owned mode 2770, so no per-Core leaves were used; `.nex-qemu-access-*` entries retained (first scan used wrong prefix) |
| BUG-0167 descriptor-based (22:40:35.055603–22:45:34.084834) | Failed | Core3 150 healthy; Core2 147/3, one obs 22:40:45.648150985Z `pool storage reconciliation repository deadline`; no substep timings |
| Timing diagnostic `aacabf…` (02:01:56–02:06:56, 09-14) | Passed | 61/59 distinct obs |
| Combined clean `cee98f…` | Passed | All three hosts healthy 59-model CPU; 61/60 distinct; no error or deadline journal lines |
| Three-file timing overlay | Passed | Also healthy during SMB repeat and three start/stop cycles (below) |
| BUG-0171 clean (04:49:20–04:54:18) | Passed | No unhealthy member samples |
| BUG-0173 clean, before and after workloads | Passed ×2 | Zero failed/stale samples; no deadline/lock-probe journal lines |
| Refactor clean, before and after workloads | Passed ×2 | Clean; no deadline, lock-probe, fence, withdraw or attention lines |

Earlier measured failures (before BUG-0156, no backend attribution for the last row): iSCSI
nested cluster reconciliation 2.985–3.316 s of a 3 s repository context; SMB combined
credential/inventory stage 10.165–13.319 s (not isolated); NFS access reporting 2.894 s in a
failed pass; one access-validation stage returned after 11.292 s. Source review found cluster
scheduling below the repository busy check.

## GFS2 six-writer loaded runs

| Candidate | Integrity | Reconciliation health |
|---|---|---|
| BUG-0159 clean | Passed: 600 writes each, hashes and rereads, prior archives kept | Failed: all-six overlap 168.124 s, 84 samples; Core2 73/11 (4 obs: 3 repository failures, 1 deadline); Core3 64/20 (6 obs: 5 deadlines, 1 failure) |
| Correlated diagnostic | Passed | Failed: overlap 170.392 s (19:37:40.030315–19:40:30.422648), 85 samples; Core2 65/20 (5 obs), Core3 61/24 (6 obs). iSCSI: 2.611 s GFS metadata + 365 ms cluster health; another 1.508 s metadata, 357 ms health, 640 ms path, 495 ms QEMU. NFS: create 1.869–2.108 s, chown/chmod ~0.8 s, remove 0.33–0.43 s. Some expired during peer lease POST (zero-duration lock traces, not slow syscalls). Managed inventory 2.399–2.663 s in two failures outside the interval |
| BUG-0167 clean (fallback) | Passed (receipt's second-read field repeats the first value; executed comparison confirmed by review) | Failed: overlap 182.682 s, 91 samples; Core2 87/4 (1 obs), Core3 73/18 (5 obs); full 232-sample capture 2 and 8 obs |
| Timing diagnostic `aacabf…` | Passed: hashes matched both rereads; 12 prior archives kept; fresh NFS/SMB/iSCSI access after | Failed: 150 samples over 298.466 s, overlap 169.878 s (two writers outlasted observer). Core2 148/2, obs 02:15:32.942834977, clustered iSCSI: 3.015 s total, validation 3.004 s (GFS2 metadata 1.429 s, cluster health 351 ms, QEMU 1.217 s; isolated probe sync 1.09 s; a second overlapping probe). Core3 146/4, obs 02:14:10.418347179, NFS: 3.436 s total, validation 3.426 s, probe 3.423 s (create 1.894 s, remove 850 ms). Nested durations are not additive. No SMB deadline |
| Combined clean `cee98f…` | Passed | Failed: observer covered every writer (79–86 samples each, 79 common); Core2 one deadline in 2/150 samples, 115.801 s after last writer and 38.016 s after runner. Later host check: negligible I/O, healthy quorum, no pending fence |
| BUG-0171 clean | Passed | Sampled pass (150 + 30-sample extension), but a 17.110 s gap (main observer ended 22.989 s after runner, extension began 40.099 s) spans the earlier ~38 s failure point; continuous cooldown unverified |
| BUG-0173 clean | Passed: writes started within ~43 ms, concurrent 170–189 s, hashes on two rereads | Passed; no deadline, lock-probe, fence or withdraw lines |
| Refactor clean | Passed: start spread 20 ms, 159–190 s concurrent | Passed |

## NFS and SMB six-guest runs

| Candidate / run | Functional | Health |
|---|---|---|
| Combined clean, NFS | Passed: same two guests/disks per Core; prior 32 MiB+tag payloads checked, archived, reread, removed; writes 3.57–4.21 s; reboot hashes; lock refusal until release | Passed: observer from before writes to ~282 s after; two samples per writer window |
| Combined clean, SMB | Passed | Failed: Core3 1 obs (4 samples), Core2 1 obs (3 samples), both before writes |
| Three-file overlay, SMB repeat | Passed | Passed; slowest idle NFS call 2.735 s (file proof 2.710 s: create 1.319 s, chmod 0.519 s) |
| Three-file overlay, three start/stop cycles | Passed: 18 starts, one native owner each, fresh stopped each cycle | Passed; slowest NFS reconciliation 2.950 s (50 ms margin) |
| BUG-0171 clean, NFS | Passed | Failed: Core2 obs 05:08:10.933 UTC (2 samples, 12.075 s before first write); Core3 obs 05:10:04.252 (97.209 s after last write, 52.998 s after runner). Low I/O at capture |
| Four-file monitor diagnostic, NFS | Passed | Failed: Core2 4 samples, 2 repository-failure obs; one SMB invocation errored in the lock stage. 12 same-Core NFS monitor/reconciliation file-proof overlaps of 0.589–2.065 s (BUG-0172) |
| Same diagnostic, second NFS run | Passed | Failed: 300-sample collector, 5 projections of one Core2 SMB obs at 05:46:43.100622926 “shared storage OFD lock is not exclusive across Cores”; invocation 261 ms incl. 95 ms lock/report stage (not lease expiry) |
| BUG-0173 clean, SMB | Passed: starts within ~25 ms, overlap 6–13 s, reboot hashes, second Core `qemu-io` lock refused | Passed: fresh storage health before/after |
| BUG-0173 clean, NFS | Passed: starts within ~29 ms, overlap 15–17 s, reboot hashes, lock refused | Passed |
| Refactor clean, SMB and NFS | Passed: start spread 40–41 ms, reboot hashes, lock refused | Passed |

Exclusive iSCSI: fixture attempt 1 failed (comparator treated ACL reordering as change; target,
backstore, image and session confirmed absent). Retry provisioned a separate fixture with
initiator-observed WWID, ext4 UUID and Core2-only ACL; temporary non-Core session removed. No
Core login, registration or VM ran.

## Native probe and SMB lock experiments

| Experiment | Result |
|---|---|
| GFS2 shared-root vs per-Core directory probe, attempt 1 | Failed: generic Core1 command error, no complete round retained; script used wall clock and whole-FS sync (invalid method). Guards passed |
| Monotonic probe comparison | Passed: 5 AB/BA rounds, 10 phases, 30 samples; start spread 0.493–4.052 ms; shared-root median 65.837 ms / max 2,566.965 ms; per-Core leaf median 18.441 ms / max 84.717 ms; creation medians 50.621 / 0.597 ms. Three largest samples in the first shared phase; background reconciliation and serial leaf creation are confounders. Contention is a contributor, not proven cause. See [GFS2 performance guidance](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/9/html/configuring_gfs2_file_systems/assembly_gfs2-performance-configuring-gfs2-file-systems) |
| gfs2-utils 3.6.1-1 journal inspection | [Source](https://sources.debian.org/data/main/g/gfs2-utils/3.6.1-1/gfs2/edit/hexedit.c) issues buffer-cache flushes at startup; [mounted use](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/7/html-single/global_file_system_2/global_file_system_2) is supported |
| Descriptor-based GFS2 probe (`e4c8c5…` test binary, rebuilt reproducibly) | Passed as libvirt-qemu on all three mounts: RUN/PASS, no skips, 0.02/0.03/0.02 s. Root ownership, mode 2770, device/inode unchanged; three empty root-owned 0711 leaves created and reused; 16 pre-existing root remnants untouched |
| NFS export inspection | Root 0:0, mode 0777, `no_root_squash`; root-squash fallback not natively exercised |
| NFS held-leaf probe (test binary `af7384bee3d38952ebb470e408ac832fbe21767d92a85280aad797779fd3a6e6`) | Passed once per Core: full QEMU probe via held descriptors, root and temp names unchanged, 0711 leaf left empty, binaries removed. Service still on diagnostic build |
| NFS permission-change timing (idle) | 3 proofs per Core: file already regular 0600 owned by QEMU user; 9 stats 3–6 µs; 9 chmods 4.055–6.424 ms (loaded service proofs had spent hundreds of ms). Test binary removed |
| SMB OFD diagnostic 1 | Failed setup: Core2 open returned ENOENT for Core1's new file before any lock attempt. Traces had no drops |
| SMB OFD warmed repeat | 100 serial cross-Core cycles passed; same-process two-descriptor control failed on a stale client absence check (server and clients later confirmed clean) |
| SMB OFD distinct ranges | 100 cycles passed, then loss of exclusion: Core2 acquired A and still-held B after Core1 unlocked+closed only A |
| Matched SMB controls (Samba 4.22, Linux 6.12 cifs, SMB 3.1.1) | Product flags, two descriptions: B held after A unlock, lost after A close. One description: B held in 20/20 rounds; 20 serial challenge-lock cycles passed (BUG-0173) |

## Master-agent outage

| Run | Result |
|---|---|
| Wrapper attempt 1 | Stopped before any action: expected the inactive legacy Controller unit (guard corrected, 7 private tests) |
| Attempts 2–3 | Stopped before outage: guard compared quorum/DLM clocks (9 private tests); runner rejected an adopted failed-create outcome (BUG-0161) and exposed BUG-0160. 12 prior payloads preserved; guest-running sets empty |
| Attempt 4 | Failed: six writes, reboot hashes and lock probe passed; stopped-fixture start refused with a raw transport error the harness did not accept (BUG-0162). 164-sample timeline; 2.674 s normal overlap had one sample, both members failed from pre-write observations. Agents active, native zero afterward |
| Corrected diagnostic outage | Passed, ~468 s: counters 18–30 → 62–66 during outage; boot IDs unchanged; stopped fixture start failed against master inventory with state unchanged; members fresh-failed then recovered; six payloads matched hashes and rereads; 12 archives kept. Journal: master stop 16:44:19 UTC, restart 16:44:37, member recoveries ~16:47:06–09 |

## Placement

- Local/NFS attachment on a new stopped fixture (18 original guests kept): helper first
  misread the member journal layout and omitted MAC/default disk fields; post-add proof then
  stopped on omitted `boot_order: 0` (disk and fixture retained). Resume passed: disk counts
  1 → 2 → 1, local disk hash unchanged, detached shared disk hash intact, master owner record
  unchanged with no pending update, VM stopped throughout (BUG-0132). Not migration or running
  reconfiguration.

## Fencing, partition and activation replay

| Run | Result |
|---|---|
| Cold-master fence/replay executor | Failed: helper restarted the outer master before fencing settled; libvirtd destroyed it 6 s later; no replay observed. Controlled return after two matching fence-history observations 10 s apart: activation and registration matched pre-fence records (pins/autostart not captured). Healthy activated operations take the health-refresh path, so queued-to-worker replay cannot be observed from that checkpoint |
| Master activation replay v1 | Stopped at read-only preflight: equality check rejected six transient capability differences (321 readable journal entries, one matching receipt). No fence issued |
| Master activation replay v2 | Passed: intent, survivor proof and target bound to one fence request; two survivors stable 10 s; master new boot ID, members unchanged; six trace events in order with preserved activation, native apply and resume; activation and registration byte-identical. Wrapper kept only its final marker, not the raw final inventory |
| Lost-quorum force-stop, attempt 1 | Inconclusive: stop posted after quorum loss but helper waited on the operation; native fence ~4.3 s after request; operation succeeded only after return. Final guards passed |
| Replacement helper | Failed before stop: no-quorum state not captured, no POST; cleanup missed the fence (only matched "turning off"). Quiescence guard later matched reboot fence events ~9 s after intent, held 10 s, one return; guards passed. Receipt lacks survivor guest listings, target boot ID and return count |
| Frozen-I/O stop (Core3) | Inconclusive: writer counter stalled three observations; one force-stop reached `powering`, native captures timed out, fence completed first. Cleanup passed epoch to ISO parser; separate recovery adapter returned target, guards passed; operation succeeded only after return |
| Partition 1 (six writers, Core3 heartbeat) | Failed harness: waited for an API proof it never requested; recovery flag set too late; cleanup failed. Return attempt stopped: Corosync display date in stability signature. After date fix, return succeeded but readiness preflight required all VMs stopped while four survivors ran. Cleanup then passed |
| Partition 2 | Failed harness: post-fence on→off verification produced failed proof “configured target was not confirmed powered on”; cleanup SSH'd to the off target. Controlled recovery passed. Genuine on→off verification later restored a passed proof. Corrected helper: 13 focused tests |
| Partition rerun | Pre-return checks passed: two target writers interrupted, survivor counters 23–29 → 53–60, four payloads hashed and reread. Return blocked: survivors timestamped the off event 388 µs apart. Resumed recovery returned Core3; inline comparison mismatched only on tuple vs list types; separate completion receipt passed |
| Final natural partition | Passed in one receipt: native target-off fence, two-node quorum, two target writers interrupted, survivor counters 25–34 → 44–54, four payloads with rereads, survivors stopped, common-intent quiescence, Core3 new boot ID, no cleanup errors, 20 guests stopped |
| Master found off before timing-diagnostic rollout | Survivors recorded membership loss and master fence at 23:16:31 UTC (09-13; cause not established). Recovery: target off, fresh fence record, four stable samples over 11.3 s, one return; pins and 20 stopped guests restored (no end timestamp) |
| Failed-fence helper | Not rerun: blocking the shared fence port also blocks monitors; prior receipt lacks a native failed-action event |

## Builds and rollouts

| Candidate | Pins / manifest | Rollout |
|---|---|---|
| BUG-0156 | Accepted archive + three agent files | Passed |
| BUG-0158, BUG-0159 clean and diagnostic | Private receipts | Passed |
| BUG-0163 capped | Private receipts (raw PCS output not retained) | Passed; uncapped candidate never deployed |
| BUG-0164 clean; correlated diagnostic (371 paths, five diagnostic Go files) | Private receipts | Passed |
| BUG-0165/0166 partial; BUG-0165 master-handler complete | Private receipts | Passed |
| BUG-0167 clean | Exact manifest, overlays removed | Passed Core2 → Core3 → Core1 |
| BUG-0167 descriptor-based | Core `e4c8c5e85ae2ba6bd4746d65dbf9513e0933749ca1c3fb82d358a0f95544f50a`, source `87a286c9287ab2a0780a42f372dd294363b7a668958c8cc958dd56dc1e347626` | Passed |
| Timing diagnostic (375 entries: three instrumented Go files, five docs) | Core `aacabf211542b23836d5b4a294518895d7f58e128245cffde0da83b1a2883d61`, source `f9a4384cb0195dfde9c224afcdf361e24ad17da5e2b2bfd793f9ed2ce064132b` | Passed |
| Combined clean BUG-0168/0169/0170 (378 payloads) | Core `cee98f99befceac867e469b65f970315e175ec48ba52242f7ee3c7cd0e35cc71`, source `d8d81791664c5fc220812d873396fb190d0a09113258abbf763dc1a4b90122ec`, manifest `ebcb4f3bb40573e1974b0ed61461064c91b0643aa126f45fc84b8c9e3357b989` | Passed; a predecessor-pinned post-check failure is retained as a helper mismatch |
| BUG-0171 clean | Core `d250ee3b9f36f4306c0bb7bb2acdc512ea8e6fad4be036fa64a28127fb27fdec`, source `2461419960fc97f7afb1aad9aec2fcd7627c9f1fecea2d9cabacea64ab06cdc3` | Passed |
| Three-file and four-file timing overlays | Private receipts | Passed |
| BUG-0172 clean (380 payloads) | Core `4f30a9e56a4b770d944bde4f710141a52c18840858d2c7478fc76b6b6c3a15c6`, source `ac20d9bbf26f71c874a96ab3220e12a1545ac63c5d696d1fd03e78f91578fced` | Base for BUG-0173 |
| BUG-0173 clean (BUG-0172 snapshot + lease fix) | Private receipts | Passed with fresh session capture |
| Storage-cluster refactor clean | Private receipts | Live CIB checked against current-layout requirements first; passed; CIB byte-identical after |

Lab interference: before the combined-candidate NFS probe, two members restarted Pacemaker
controllers (scheduler IPC unresponsive), storage clones were incomplete and a fence monitor timed
out; package versions matched. A separate agent session's whole-lab hashing of live VM disk images
was found and its process group terminated; clones recovered without restart or configuration
change. Not proven to explain earlier deadlines. Artifact verification must hash only named
frozen artifacts.

## Automated checks

- Before BUG-0156: Go race all packages, 230 Python tests, docs gate 143 paths, privacy,
  whitespace. BUG-0156 old dispatch failed
  `go test ./internal/agent -run '^TestPeriodicPoolStorageReconcileSkipsDuplicateClusterRefresh$' -count=1`;
  fixed `go test -race ./internal/agent -run '^TestPeriodic' -count=1` passed.
- Later: 238 Python (docs 145 paths), 240 (docs 146), 242, then 243 (~9 s, resource warnings,
  none failed or skipped). BUG-0163: 77 focused Python API/workload tests. Outage harness suites
  53, 55, 56 tests. Combined clean: full local `GOMAXPROCS=2 go test -race ./...` passed. Focused
  normal/race results per fix are in the ledger. Both Actions providers run Go race and Python
  discovery on push/PR; no hosted run.

## Open acceptance gates

- Continuous post-workload cooldown observation; repeat attribution if any deadline recurs.
- Lost-quorum/frozen-I/O stop completing before fencing; failed-fencing with a target-scoped
  fault primitive; remainder of the partition/fencing matrix.
- Exclusive iSCSI: one-host registration, write, reboot, reconnect, adoption and other-host
  placement refusal on the provisioned fixture.
- Replacement-Controller recovery with all four storage profiles; broader placement transitions;
  root-squash NFS fallback natively.
- Five-host scale, physical devices, long soak, full current-ISO installation, hosted CI and
  release evidence. Backup, restore, migration and automatic HA are separate; nested UEFI excluded.

API impact: **changed contract** for BUG-0163 Core-peer lock negotiation and authenticated lease
scoping (legacy fallback, Controller-facing schemas unchanged); other corrections and harnesses use
existing contracts. Guide impact: **updated** (awaiting fresh member reports after master
management restart). State owner: **core**; pool-master cluster, repository and ownership records
stay authoritative via existing inventory/adoption; member access observations are process-local
and rebuilt from authenticated reports.
