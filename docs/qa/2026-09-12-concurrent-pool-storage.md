# Concurrent pool storage verification

Date: 2026-09-12 to 2026-09-14. Status: native acceptance in progress; not accepted for
production. Scope and gates: [concurrent storage plan](../plans/2026-09-12-concurrent-pool-storage.md).
Causes and fixes: [bug ledger](../BUG_LEDGER.md). Later reliability work:
[cluster storage reliability](2026-09-14-cluster-storage-reliability.md).

## Environment and method

- Required scenario: three Cores in one pool, two nested VMs per host, all six on one shared
  iSCSI/GFS2 LUN, then separately on one NFS export and one SMB share; distinct disks per VM;
  concurrent fsynced writes, checksums, reboot persistence and interruption tests.
- Disposable nested lab: three Core guests, one storage-target guest and a private virtual switch,
  inside an isolated libvirt control container (container fixes: virtlogd, child reaping, private
  device access, guest-forward subprocess). All four domains run nested KVM (SSH and KVM checked).
  Cores use the standard Debian kernel (GFS2 and DLM modules present); lab-only services restore
  networking after reboot. Binary-staged Core/Controller builds from the development worktree,
  except where an ISO is named. Fence SSH is loopback-only through a QEMU user-network forward.
- Method: private helpers and runners drive the product API, sample native state (libvirt,
  Corosync/Pacemaker, DLM, GFS2, iSCSI) and outer hypervisor state; independent review checks raw
  receipts. Failed receipts are retained separately from passing reruns. Raw receipts, journals,
  overlays and captures (`screenshots/cluster-storage/`, `screenshots/production-readiness/`)
  are private; this record is a sanitized summary.
- Standard check terms: "native zero" = no running guest domain on any Core; "preflight" = the
  read-only three-host quorum/DLM/GFS2/iSCSI/stopped-guest check; "pins" = running executable and
  source-archive hashes.

## Lab bring-up and Nex-driven fencing

| Step | Result |
|---|---|
| Native `fence_virsh off` from Core-1 against Core-3 UUID | Passed: hypervisor reported shut off; only that domain restarted. Lab power path only |
| First pool Storage page | Capture shows pending setup, no runtime errors; mocked browser checks cover fence confirm/cancel/mismatch/uncertain-no-retry |
| iSCSI fixture logged in outside Core | Only that fixture node/session removed after confirming LUN unused; Core recreated its session. Node-file formatting mistaken for credential rotation; after the replay fix the repository resolved its GFS2 lock table and reached `awaiting_fencing` (capture `08-live-awaiting-fencing-readonly.png`) |
| GUI fence of Core-3 | Passed exclusion (durable proof + outer off); browser got no timely response, proof reconciled without resend. Exposed Controller response-deadline and partial-proof status defects |
| Status reports missing | Lab Controller predated the fence-proof request-ID field; strict decoder rejected Core reports. Fixed by aligned builds (no old-Controller compatibility claimed) |
| Core-2 fence via confirmation dialog (aligned builds, same DB/certs/pool/six VMs) | Passed: accepted, durable proof, outer off; restarted |
| Delegated master fence, attempt 1 | Failed closed: master-to-member TLS expected a master cert; member handler lacked current-master auth. Repaired without weakening pins |
| Delegated master fence, attempt 2 | Passed: survivor recorded proof while master observed off; master restarted and recovered all three proofs |
| Native activation | Refused unowned Corosync config: binary staging skipped the pristine-marker helper. A certificate-SAN diagnosis was unsupported and discarded |
| Activation after shipped helper | Exposed Corosync inline-quorum renderer syntax error; after fix, three-node quorum and three Pacemaker-managed DLM instances (GFS2 still disabled) |
| Further reconciliation | Exposed clone-level stopped-state recognition, concurrent member CIB writes and a health-check panic (wrong state dir). Agents held; native state preserved; no manual CIB repair |
| Storage root capacity | Expanded online 32 → 96 GiB after SMB reserve refusal; LUN identity and 32 GiB size preserved |
| Clustered mount | Passed: quorum, DLM and the same GFS2 filesystem mounted on all three by normal reconciliation; live capture shows per-host quorum/mount. Shared root provisioned for the libvirt group by a nonrecursive fixture permission change |
| Guest creation hold | Inventory applied exclusive-iSCSI ext4/XFS checks to GFS2; member import kept a Core-resolved mount path; review rejected inferred journal counts |
| After observation/path repair | Member reports show real GFS2 metadata and writable/QEMU/locking; zero bootstrap capability metadata corrected; NFS/SMB-only Controller adoption/selection and picker conditions fixed; generic clustered update/detach intentionally unsupported |
| UI review | Caught clustered host allowlist hiding NFS/SMB member access; corrected, reviewed, deployed. Shared VM-disk capability for all three hosts; GFS2 **Recheck** succeeded via API; Update/Detach disabled (capture shows an earlier queued task render) |
| Libvirt QEMU startup hook | Binary staging had omitted it (earlier NFS baseline did not exercise permits). Installed hook + narrow AppArmor rule (enforcing); real native start of a stopped VM without permit was denied |
| Rolling 4 → 5 GiB host resize | Passed on all three; first host's outer domain lacked ACPI power-off (halted domain terminated after serial check). Master return exposed BUG-0121 (iSCSI node manual startup) |
| Clustered-form target discovery via master | Passed: IQN populated; no setup submitted |

## NFS six-VM workloads

| Build | Result | Key numbers / limits |
|---|---|---|
| First starts of six NFS definitions | Failed before QEMU: healthy observations treated as failures; member kept preceding generation after third join | Fixed with regressions; six stopped VMs retained |
| After deployment | Six started, two per host (capture `04-six-nfs-vms-running.png`; two failed create records visible) | Guest network fixture needed baked DHCP and unique client IDs (one lease per host observed) |
| Guest disks | MAC-specific DHCP and first-boot SSH host-key unit; key fingerprints verified offline (an apparent key rejection was a diagnostic missing its identity-file argument) | — |
| Exact-six run 1 | Failed: runner duration serialization error | Retained |
| Exact-six baseline | Passed: competing QEMU lock error, open after release, reboot checksums | Overlap ~5.4–5.8 s; stopped, native zero. Not soak/fencing |
| Deadline/picker build, startup hook active | Passed: same six, lock refusal/release, reboot checksums | Overlap > 15 s; capture of six running |
| Monitor build (corrected Core; Controller/UI older) | Passed | Overlap ~3.73 s; 512-byte write after release; native zero |
| Fence-action checkpoint build | Passed | Overlap ~4.3–4.8 s; capture 40 |
| Fresh repeat (32 MiB) | Passed incl. all-guest reboot/hash, non-root lock refusal then 512-byte write | ~12–13 s; native zero |

## SMB six-VM workloads

| Build | Result | Key numbers / limits |
|---|---|---|
| First SMB creation | `needs_attention`: ownership inspection could not read master's unmounted GFS2 disks (BUG-0122); blank placeholder disk | Later settled by Core without redispatch; block map proved disk all zero |
| Attempt 2 | Failed member gate: runner compared host-local CIFS filesystem IDs (BUG-0126) | Two stopped VMs and populated disks retained |
| Source package | Workload fixture missing from allowlist; both helpers + tests added; copied-source `--help` check | Superseded archives preserved, not deployed |
| Attempt 3 (fencing-policy build) | Passed: six disks on one share, reboot checksums, non-root lock refusal, write after holder exit | Overlap ~4.76–4.81 s; all SMB/NFS/GFS2 guests off |
| Monitor build | Passed; capture inspected | Native stopped |
| Fence-action checkpoint build | Passed | Overlap ~4.6–4.9 s; capture 41 |
| Fresh repeat (32 MiB) | Passed | ~7.5–8 s; native zero |

## GFS2 six-VM workloads and write-time fencing

| Attempt | Result | Key numbers / limits |
|---|---|---|
| 1 | Failed: second create refused HTTP 409 (access not writable) | First VM/disk kept; later native checks healthy; cause unproven |
| 2 | Failed: member access observation ~81 s old vs 30 s limit | Sampler: 587 snapshots, 88 unknown with probe-deadline errors, no API failures; thread captures show GFS2 lock/flush waits. Diagnosis from an old served source archive discarded (disassembly confirmed running predicate) |
| Freshness-wait runner | 24 runner tests passed | Sampler login expired: initial HTTP 401 records are an evidence gap |
| 3 | Failed sixth create on available memory (2 GiB reserve) | Five stopped VMs; no domain/disk for failed ID. Sampler 586 records: 89 auth failures, 497 valid, 62 unknown |
| 4 | Six ran after proving failed create absent on all mounts; stopped before writes: lock probe PID file in protected root (BUG-0124) | Corrected fixture: 31 checks; standalone probe lock refusal + 512-byte write; 199 Python tests |
| 5 | Six ran; lock holder failed: `--clear-groups` removed traversal group | Group fix: 32 checks, 200 Python; standalone probe with real QEMU account/groups passed |
| 6 | Six ran; write-time fence failed: Pacemaker rebooted target after power-off proof (BUG-0125, no `stonith-action=off`) | UI: target guests off, four survivors running. Policy build later deployed; CIB resource/constraint hashes unchanged |
| 7 (idle, pre-workload) | All three Cores fenced (see incident below) | — |
| 8 | Stopped before start: omitted vs empty media ID (BUG-0136) | Retained |
| 9 (fence-action/activation-resume build) | Passed | Overlap ~28.0–31.9 s; reboot checksums; lock refusal/512-byte; fence proof inside four survivor windows (~177 s), two target writers interrupted; target held off; survivor VM shutdown/start kept spec and checksum; controlled return + recovery checksums. Captures 33–36; capture 34 showed BUG-0137 |
| Checkpoint deploy | Passed no-op upgrade | Core `e6a04b2b48c74dba1cf3b8f83962d7e413a4aa2c1e55d0aac6c8a1a578a11154`, source `be5d6b74c48a7a890517810b14bcf37ab67fdd3fd4700b56fe1361b8d83a3291` on all four hosts; members before master; native PIDs kept; CIB byte-identical; race agent/vmhost 3.356 s/11.244 s, 210 Python |
| 10 | Passed | Overlap ~4.5–6.7 s; proof inside survivor windows (~133–153 s); survivor VM kept definition; return + checksums. Individual checksum outputs not serialized. Capture 38 degraded aggregate, 39 fresh after return (member report 1 s before aggregate; no freshness defect) |
| FILEIO 600 MiB | Failed: helper crashed serializing tuple-keyed counters before the fault; target DataOut failures on two Cores (one fenced, one recovered) | Four survivor guests force-stopped via API, native zero; fenced overlay retained; target config checksum/XML retained |
| FILEIO → IBLOCK restore | First restore gave invalid IBLOCK LUN reference despite tool success; canonical plugin/block reference restored exact config (ACL order only) | Target reboot verified write-through storage and full shared-byte hash (not power-loss proof); three-Core cold return and no-VM transport interruption (target-only fence) passed |
| IBLOCK 1 | Failed convergence poll: fence reported `pending`, three-member membership | Separate survivor evidence: four 600 MiB files matched hashes and reread; recovery passed |
| IBLOCK 2 (corrected poll) | Passed: dual-target transport interruption fenced only target, no DataOut | Four survivor 600 MiB files matched on reread; target new boot ID, unchanged baseline hashes and storage identity; native zero |
| Unchanged full-size member-only fault | Failed (BUG-0143): DataOut before the intended fault, fencing cascade, final member lost quorum | Earlier no-VM member-only TCP interruption had passed |
| Six-writer run (BUG-0143) | Failed: DataOut timeouts, two fences, quorum loss; single-host recovery failed | Guests off by outer power state only; later cold return restored health. Exposed BUG-0144 (narrow native stop check passed only with master management down) |

## Fencing incidents and recovery

| Event | Result |
|---|---|
| All-host fence (after SMB baseline, guests stopped) | Journal: iSCSI ping timeout, session recovery ~8 s later, all three GFS2 monitors exceeded implicit 20 s, power-off fencing of all three (outer logs confirm), fence monitor timeout (BUG-0127). Overlays preserved |
| Master-only restart | Two-of-three quorum, exclusion confirmed, GFS2 mount blocked in DLM lockspace creation and master re-fenced: BUG-0128 (`libstonithd.so.26` requested, `.so.56` present; `kick_helper error -79`) |
| First matching DLM build | Stopped: Pacemaker 3 dev header kept old library constant |
| Offline checks | `crm_diff`: nonempty patch exit 1, applies to matching source, refuses advanced version (103). Race suites, 205 Python |
| VM page with hosts off | Unknown/stale with "not proof VMs stopped" warning (capture 26). Remaining Core halted without ACPI power-off; outer domain powered off |
| DLM `4.3.0-3+nex1` offline install on three overlays | Passed hashes; `dlm.service` disabled; rollback copies; staging suite 10 checks |
| Two-host runtime recovery | Passed: Core-2 then Core-1 quorum, Core-3 exclusion before DLM/GFS2, lockspace two members zero pending, writable mounts; Core-1 added `timeout="40s"`; readiness gate before Corosync/Pacemaker. First read-only capture failed on JSON key casing (retained) |
| Storage page after recovery | BUG-0130: healthy majority rejected (capture 27). First fix: master healthy, member still no quorum (observer bound to owner). Observer fix: both survivors healthy; third host returned, all clones and reports healthy, activation recorded (capture 28) |
| Failed-fence scenario (earlier) | Not a pass: parser defect; blocking fence endpoint also blocked monitors; target and a survivor fenced, remaining host lost quorum; cold recovery + preflight passed |
| Idle failed-fence experiment | Not a pass: first failed off 17:49:25 UTC; helper expected `failed:` not `failed (reason):`, no negative start sent; monitor timeouts → target exclusion 17:52:43, survivor self-fence 17:52:52. Survivor restart fenced again; cold two-Core recovery mounted GFS2 18:04:55 UTC; third returned; preflight passed (209 Python) |
| Heartbeat partition 1 | Stopped before fault: victim lacked `nft` |
| Heartbeat partition 2 (guests stopped) | Passed, reviewed: victim Corosync UDP dropped; native off event + outer off with two-of-three quorum; normal start; DLM/GFS2 healthy |
| Stopped-VM storage-path interruption (monitor build) | Passed, reviewed: only target fenced, survivor off-success ~22 s after injection; 28 observations healthy; no SSH firewall check possible on off target; explicit return + preflight passed |
| Stopped-VM transport repeat (compatibility build) | Passed, reviewed: 27 observations + final over ~217 s; return + preflight passed |
| Idle storage-path fault (TCP/3260 on one Core) | Failed (BUG-0138): 90 s watchdog removed table; ~94 s artifact showed target running; later all three Cores off. Journal: NOP timeout 19:22:21; unkillable GFS2 monitors 19:22:59/19:23:00, isolated 19:23:09; 19:23:42 all monitors > 40 s, fence all; session recovered 19:23:43. Cold recovery passed without clearing/repair |
| Session query cost | 90/90 healthy local queries; per-Core max ~3.25, 5.46, 11.36 ms. Parser fixture with a nonexistent header rejected before deployment |
| Pacemaker one-time restarts (guests stopped) | Core3: strict post-check failed (DLM/GFS2 still starting), later preflight passed; Core2 passed ~20 s; master passed ~25 s with one transient readiness failure. Boot IDs and CIB unchanged |
| Workload preparation after cold return | Six guests booted; 24 failed-run scratch files archived, checksum-verified and removed; headroom, API stop and empty-guest preflight passed |
| Target NIC rename after BUG-0145 fixture | Fault helper rejected old binding (retained); corrected after MAC/PCI/route review |

## Master returning-host boot

| Run | Result |
|---|---|
| Recovery build, master reboot 1 | Failed: readiness gate kept Pacemaker inactive; iSCSI device appeared just after. Later CLI probe set auto-start (not a boot test) |
| Master reboot 2 (auto-login disabled first) | Passed ~18 s: gate established session, validated metadata, restored auto-login; all three mounted the registered UUID, three-node quorum; peers unchanged; capture inspected |

## Master-agent outage (GFS2 writers)

| Run | Result |
|---|---|
| Monitor build | Failed (BUG-0141): writers progressed, restart OK, recovery status HTTP 504 (older Controller rejected readiness field with 400; 30 s broker timeout). Native zero, preflight passed |
| Compatibility build before Controller upgrade | Mixed-version clustered status accepted; outage gate failed: one host's two writers showed no progress in 5.5 s sample (host I/O pressure; cause not established). Helper now keeps all samples, 45 s window |
| Matching build | Failed: all six progressed, immediate post-restart identity/state check failed (response not retained); cleanup falsely claimed stopped with two running (BUG-0142); stopped via API, preflight passed |

## Controller outage and replacement

| Run | Result |
|---|---|
| Controller-only outage 1 | Failed harness: transient lab unit unloaded on stop; progress sample read a previous file. Controller restored from journal command (hashes, boot ID verified); cleanup evidence passed; persistent lab unit installed |
| A → B strict continuous-writer | Failed from private fixture connectivity (writers completed before adoption) |
| Fresh Controller B adoption | Passed: membership, peer trust, storage config/proofs, native cluster, pool/host/network/media IDs, six boot IDs and 600 MiB rereads unchanged; 18 VMs, three repositories; four retained failed outcomes as operations. Not full route equivalence |
| 2026-09-13 outage attempt | Failed safe: guest free-space preflight before any writer or stop |
| Outage after archiving six payloads | Failed helper: TLS EOF before API readiness, cleanup failed. Separate reconciliation: six payloads matched, reboot checksums, native zero |
| B → C replacement | Strict runner failed identity comparison (volatile SMB diagnostic; old baseline kept only a hash). Separately: growth continued, boot IDs/QEMU/ownership unchanged, six 64 MiB payloads matched, native zero |
| Controller C outage | Failed: writers advanced while off; status 409 during preflight after restart (BUG-0151). Six partial files archived and checksum-reread; native zero |
| C → D strict | Passed, reviewed: six 64 MiB writers through adoption by empty D; 18 VMs, three networks, three repositories (no media records); 17 historical failed outcome IDs, six boot IDs, QEMU PIDs preserved; D stopped all six |
| Corrected Controller outage | Passed: six 600 MiB payloads with expected hashes and reboot checksums; progress while off and after return. Pre-boot identities enforced but not retained; the 503 branch did not occur naturally. Separate sequential Core rollout kept CIB, quorum and mounts. BUG-0148 rollout also kept CIB/quorum/GFS2; no rejected or uncertain enrollment path exercised |
| Task-history browser | Passed: 688 adopted outcomes and recent-five view labelled reobserved (BUG-0152); earlier locator failure retained |

## Autostart and policy

| Run | Result |
|---|---|
| Autostart walkthrough 1 | Failed: BUG-0131 stale definition blocked shutdown (capture 30) |
| Policy-recovery build | Passed recovery: shutdown, autostart disabled, revision 2, no pending update |
| Retry on BUG-0134/0135 build | Stopped: fixture parsed filesystem type from a moved column. Core added three reboot-to-off mappings |
| Autostart walkthrough 2 | Passed, reviewed: 32 same-boot samples over ~69.8 s stayed stopped across agent PID change; real OS reboot → new boot ID and claim, only assigned Core ran VM; five API operations; master revision 4, autostart false; activation receipt intact (capture 32) |

## Deployments and ISO candidates

| Artifact | Result |
|---|---|
| Fixed CLI/OCF staging | First copy failed on helper syntax (retained); retry passed exact-session probes, rejected malformed IDs, native PIDs/hashes/CIB unchanged |
| Offline PCS check | Clone serialization and guarded patching (diff 1, apply 0, equality 0, stale 103); start-action attributes enforced |
| Management-only rollout (member, member, master) | Passed: pins, unchanged boots/native PIDs/GFS2 mount IDs, exactly the replicated transport clone + ordering constraint; monitor started on all three |
| Compatibility build | Passed artifact and authenticated readback; boots/native/mounts/CIB unchanged |
| BUG-0145 fixture | Storage reproduced ACPI/no-VGA reset loop; two CPUs with explicit VGA; target checksum, WWN/LUN, three initiator identities unchanged; Cores two CPUs, preflight and API health passed |
| BUG-0146 rollout | Before (30 s): 120 generator calls, 60 reloads, 60 `Reloading` per member, none on master. After (35 s): zero; Corosync inode/timestamp unchanged. BUG-0147 UI and outage workflow passed |
| Third ISO | Audited; eight distribution entries replaced prior set; predates later corrections |
| Fifth ISO `a5efa0112b6a1d766f2b4508fdd92d81b400149f2a6cbc3548633e90f62bd84d` | Wizard install on blank disk; unchecked erase refused; disk boot; package 0.1.8 and source matched. Failed: state dir 0755 (BUG-0149); old helper false pass (BUG-0150 refused it); BIOS guest fixture failed (NAT inactive) |
| Seventh ISO `3ce9bc1b7bc9adcc49db48b8af785352151f1516efd405ff6d48d16bf2d1d7ef` | Audit matched 849 paths (source, Core package, appliance, DLM, notices, boot catalog). BIOS install on blank disk passed: state dirs root 0700, Core healthy zero restarts, hashes matched; BIOS guest fsynced checksum survived guest and host reboots; local browser login and desktop/mobile review passed. Disk-page clipping (BUG-0153) |
| Eighth ISO `0c895237fbff124e6fdaaa78e7ba7023e8a962498880c491c0068ad87bffc023` | Audit vs 850 files; wizard at 800×600 and 1024×768 (extracted kernel/initrd, new file disks) passed disk text, review and erase refusal. First attempt failed on a screenshot coordinate (retained). Not a full install |
| Tenth ISO `9c3871f2a7f7b965feebb07a96f9a0b25be726c2279217751da9b803beb70b8a` | Audit vs 852 files; source `730e6cc82bd53ce2927303a9da3f2e70c75f45161a8ab7f57184fc3553d06bb4`; 800×600 wizard walkthrough passed. Not a full install. Its Core deployed to three idle Cores: pins, boots, CIB, Controller D unchanged; CPU healthy with 59 models (BUG-0154); member reports show repository-stage deadlines and recovery (BUG-0155) |

Exact accepted tenth-candidate pins (the full BIOS installation/persistence result belongs to the earlier seventh ISO; the tenth passed artifact and native wizard checks plus deployment of its exact Core binary):

```text
ISO:    9c3871f2a7f7b965feebb07a96f9a0b25be726c2279217751da9b803beb70b8a
Source: 730e6cc82bd53ce2927303a9da3f2e70c75f45161a8ab7f57184fc3553d06bb4
Core:   9b114c4ed3c418eb84b14c31942e4f9d7c9e62d43361327b7a9f72ff729f4cee
```

## UI captures

- Capture 42: zero fresh connections yet cached NFS/SMB rows green (BUG-0139). Corrected fixture
  (capture 43, simulated API) passed: stale rows read as last reports, lifecycle/ISO actions
  withheld, update/detach after fresh-to-stale refused, SMB credential handoff held without POST.
- First native stale-UI attempt stopped before browser: wait budget shorter than 30 s freshness
  window (retained).

## Automated checks (last recorded)

- Full Go race tests and 230 Python tests; documentation, generated API and privacy checks passed
  (tenth-ISO checkpoint). Earlier checkpoints: 171, 175, 184, 185, 199, 200, 205, 209, 210, 211,
  212, 215 Python tests; docs gate 85/88/108 paths. Both CI providers run these suites; no hosted
  CI run.

## Open acceptance gates

- Recurring member repository deadlines (BUG-0155); see the reliability follow-up.
- Transport fault during guest writes on current build; original DataOut stall (BUG-0127/0143).
- Failed-fencing admission with a monitor-preserving fault; lost-quorum/frozen-I/O stop (BUG-0144).
- Strict continuous-writer handover with a transport fault; master-agent outage on current build.
- Full current-ISO installation; UEFI, physical fencing/SAN, five-host scale, soak, migration,
  automatic HA, failback, hosted CI and production sign-off.

API impact, guide impact and state owner: recorded per slice in `docs/changes/` and the plan.
