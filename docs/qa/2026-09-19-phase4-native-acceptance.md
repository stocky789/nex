# Phase 4 native acceptance

## Status

**Closed for disposable virtual-lab scope (project owner, 2026-09-20).** This report records a
disposable-lab campaign begun on 2026-09-19 and accepted within the permanent limits named below.
It does not accept physical/BMC fencing, five-host scale, package-installed hook enforcement, an
authentic owner-conflict S6-12/16 fixture, or a release. Individual reviewed candidate results below
remain valid only within their stated scope. Historical native results were inspected from retained
local evidence; later code fixes and current dirty work were not in those deployed binaries unless
stated otherwise.

**Close note (2026-09-20).** Phase 4 and the initial manual-recovery gate are accepted for
disposable virtual-lab scope only. OD-6 is approved: the strict unreachable-ready-member rule
(fence or fail-closed) is the product default. Post-review Core binary `327b7ec9…` was dual-deployed
to the disposable lab (service and `/usr/lib/nex` hook target). Ordered S2 maintenance native
revalidation passed: prep, core 24/24, autostart 17/17 (including local-storage return after exit),
restarts 5/5, hook 9/9 (master and member), hosts 9/9. Raw results remain under
`.cache/phase4-acceptance/s7/results/s2-*.json`. Permanent C-class limits below are unchanged.

The historical build used base revision `15b6e47f`, with a dirty closure worktree. Its recorded
SHA-256 fingerprints begin `e0b7964e` for Nex Core, `c8bea480` for Nex Controller, and
`eeaea9f1` for the source archive. These fingerprints identify the historical binaries only; they
are not a release or final-candidate fingerprint.

The lab used disposable virtual hosts, a separate Controller process, shared NFS storage, guest
VLANs and a lab-only libvirt fence environment. Host names, addresses, resource identifiers,
credentials and local paths are intentionally omitted. Raw evidence remains local and untracked.

## Historical native results

| Area | Historical result | Limitation of this historical run |
| --- | --- | --- |
| Controller-driven join and convergence | Passed: join completed through Controller; Controller loss during join and joining-host reboot resumed from host state; member storage, VLAN and ISO convergence, interrupted peer delivery, Controller replacement and no-duplicate-start checks passed. | Capability-skew join check was not run. Defects found later in this campaign require a final-build rerun. |
| Placement and resource faults | Passed: missing uplink and unavailable storage failed closed; a stale-access request carried an advisory warning; storage identity and dispatch interruption checks produced no duplicate writer. | One retryable enrollment scenario was not reproducible on that build, so it remains not run rather than passed. |
| Maintenance mode | Passed: drain kept running guests running, refused placement and starts, survived Core and Controller restarts, and suppressed managed autostart across real reboots. | The documented manual-start limit on a member's local-storage guest was not runnable then; post-fix revalidation is required. |
| Authority replica | Passed: ready members received verified signed copies; generation advanced, a lagging member caught up, a master restart re-earned observations, and the replica did not grant placement authority. | Tampered-push and incomplete-journal cases were not run. |
| Planned master change and eject | Partial: the executed healthy handovers, authority freeze, cancellation, Controller restart and one eject passed. | Several interruption cases, unreachable-member eject and adopted-media checks were not run. The later ejection defects mean this is not final evidence. |
| Management network | Partial: a genuine Core-rendered network layout confirmed only traffic over the management bridge, preserved the bridge and guest tap during an address edit, restored byte-identical configuration after a real connectivity loss and after reboot. | This historical build lost resolver configuration under DHCP. The later reviewed candidate baseline is recorded separately below; this row is not evidence for it. |
| Manual promotion and safe rejoin | Not run. | No real fence, promotion, restricted-host, returning-master or fence-backed resume result exists. |
| Browser and guide walkthroughs | Not run in this historical campaign. | Later retained intercepted browser evidence is recorded separately below. Complete native guide mutation/recovery walkthroughs remain open. |

The historical lab and later review found defects now recorded as BUG-0354 through BUG-0366. Their
reported focused regressions are evidence of implementation only; they do not replace a native
rerun. An initial native same-identity rejoin passed before the attached-drivers-media reservation
correction and is therefore superseded, not final evidence.

## Simulated and in-process evidence

Reported in-process checks cover the staged join, authority transfer, promotion and restriction
state machines, management-network rollback logic, storage lifecycle and retained resource rehome.
Fence tests inject a double where Core calls the fence. The S7 Core batches also reported failing-
first regressions for the historical defects and package/unit checks. These checks support code
review, but do not prove real fencing, actual libvirt-hook enforcement, storage recovery, browser
behaviour or a final deployed build.

The independent network review reported focused management-network tests passing for DHCP resolver
preservation and optional revision handling. It also found and corrected a stale OpenAPI cancel
description. Neither result is a native DHCP or rollback acceptance run.

Separate ignored reports record UI simulations passing U2, U11, U13, U17, U18, U19 and U20. The
U7–U10 isolated browser captures also passed at 360 px after the hidden-label positioning fix.
U6 then passed simulated 1280 px and 360 px capture review on final UI fingerprint `c4849462…`
(CSS `68c136b1…`): maintenance, missing, stale and unavailable pool-members observations disable
the target with a visible reason. These are UI simulation/capture evidence only, not a full guide
walkthrough, Core placement proof or Phase 4 acceptance. The retained persistent Phase 1 browser
command exited successfully on the reviewed dirty working tree atop base revision `15b6e47`; its
unconditional U7/U10 assertions cover intercepted validation and requested-rollback UI states at
360 px. It is not an exact final-bundle identity or native Core mutation evidence.

U22 browser fixtures reproduced an accepted VM-create warning disappearing at the task-route
change when the unconditional notice clear was restored. The current guard passed isolated 1280 px
and 360 px fixtures and the retained full Phase 1 browser run; it keeps the warning until the task
is terminal or the operator navigates elsewhere. This remains Controller UI evidence only.

## Current reviewed evidence and open gates

The source is a dirty working tree, not a release. The latest retained source-validation run after
BUG-0381 passed `go test -race -count=1 -timeout 20m ./...` and the full Python suite (374 tests,
13.130 seconds in the retained run). Its matching source build begins `f714f71f`; prior web checks remain applicable because
this Core-only batch did not change the web source. These are source-validation results, not native
acceptance or CI results. The Core candidate used for the reviewed
management-network baseline begins `2f50d2ae`. A later Core-only hook delta begins `b2c571e6`; its
separate native restriction proof below is not attributed to the 2f50 network results.

**G3 management-network browser walkthrough.** On a disposable host, the rendered UI staged,
applied and confirmed the documented management-network baseline at revision 25. A second safe
change was then applied and **Roll back now** was selected from the UI; browser-fetched
authoritative state reported a successful cancel, `rolled-back`, `requested_rollback=true`, and
the bridge restored to the confirmed baseline. Desktop and 360 px captures were inspected. This
is native browser/operator-requested rollback evidence only: deadline expiry and reboot recovery
remain separately attributed to C7/C12.

**G1 bootstrap and two-cycle browser walkthrough.** Retained
1280 px and 360 px captures and raw requests bind a bootstrap join plus two typed eject/join cycles
to accepted operations and terminal success. After each cycle, the returned host was ready and
fresh with a verified current replica; NFS access and the shared ISO converged. The reported
network state was also healthy. The independently reviewed two cycles each bind UI eject/join
requests to terminal operations, then to a ready fresh member with a current verified replica and
all seven checks healthy. Inspected 360 px captures and direct native captures confirm the VLAN
bridge, child, port and final owned stanza. An earlier interrupted second-cycle attempt stopped
before eject and is historical, not evidence for this result.

**G1 incompatible-uplink failure and recovery.** In a separate independently reviewed native
run, a typed UI join terminally succeeded while the returned member's fixture uplink was absent.
Core then reported a fresh, actionable network-reconciliation failure. Restoring the uplink without
another join led to autonomous healthy reconciliation and native bridge, VLAN child, bridge-port
and owned-stanza evidence. A normal removal then terminally completed; all participants had the
VLAN device, bridge and stanza absent, and their original uplinks remained present. The first
continuation ended in a harness assertion error caused by reading a bridge-port directory as a
file; the subsequent native capture corrects that assertion and is supplemental evidence, not a
rerun of the join. Separately retained earlier desktop and 360 px captures visibly render the
selected member's *will not converge* missing-uplink warning. The later screenshots show a
compatible preview and an unrelated CPU warning, so they are not cited for that warning. Together
these bounded runs cover the G1 missing-uplink UI warning, post-join Core failure, autonomous
recovery and cleanup. Hardware coverage remains open.

**G4 typed-eject browser walkthrough.** The rendered UI performed the typed eject confirmation;
the existing master recorded a terminal successful eject and the ejected host's local Core reported
itself as sole ready master of a new pool. This is the normal eject/recovery lifecycle. It does not
reproduce BUG-0376's missed direct-notice condition, which still lacks stronger raw native
provenance.

**G2 maintenance browser walkthrough.** On the same host as a fresh VM, the UI entered
maintenance while that VM remained running. The UI then stopped it; a UI **Start** request reached
a terminal Core maintenance refusal. The VM-create placement option for that same host was disabled
with its displayed reason. After **Exit maintenance**, normal start and a final shutoff succeeded.
Desktop and 360 px captures were inspected. Earlier G2 evidence targeted a different host and is
historical only; it is not used for this result. This walkthrough does not cover reboot or
autostart behavior.

The initial fixture that lacked cleanup provenance was archived rather than repaired in place.
Its replacement clean lifecycle is the relevant basis for later scoped pool evidence.

**Final-campaign retained-local lifecycle.** A reset campaign with Core fingerprint beginning
`cac7196e` and Controller fingerprint beginning `3f492375` passed the scoped S7-11/S7-9 sequence.
Eject first refused until the master had current target inventory matching the retained local VM;
the retry succeeded while the same host, VM and running QEMU process remained. That VM was stopped
and deleted only after it entered its solo pool. Controller-driven same-ID rejoin reached ready in
three seconds, and the original shared network, repository and ISO IDs reapplied. This proves the
retained-local ejection, solo deletion and same-ID resource convergence scope only. It does not
prove old-pool tombstone pruning, any S6 promotion case, or all-phase CAC acceptance.

**BUG-0378 same-pool resource recovery.** On a reviewed Core delta deployed to the affected
disposable member without repairing or resetting its preserved journal, ordinary reconciliation
consumed the newest applicable signed eject-cleanup provenance. The shared repository mounted and
the shared ISO became present; the master reported healthy repository access and matching media
content. Existing master API read/write, VM runtime, lock and content-digest checks were healthy.
An intentionally unmarked later network removal remained absent, with no reapply receipt or bridge
created; that is desired-state preservation, not a failed rejoin. This proves the preserved-journal
resource recovery scope only, not a fresh full rejoin cycle or all-resource acceptance.

**BUG-0379 repeated-eject cleanup.** A preserved pending second GUI eject was resumed without
repair. The same operation reached terminal completion and wrote distinct current-generation
cleanup receipts for network, repository and media. All three resources were absent as an ejected
host's local cleanup requires, while that host became the sole ready master of its new pool with
its runtime identity preserved. This proves the repeated-eject leave and cleanup scope only; it
does not establish a subsequent join or resource convergence.

**Final-candidate S3 subset.** On the reviewed Core candidate fingerprint beginning `2f50d2ae`, C4 and
C7–C12 passed: DHCP with no DNS override retained resolver state, optional revision handling,
rollback after a reboot with a clock 3,600 seconds behind, operator rollback, staged cancel,
deadline recovery after a stopped agent, guest-VLAN preservation through apply/confirm plus the
refusal matrix, and rollback while Controller was unreachable. C7 used a disposable-host-only
temporary systemd prestart clock fault before Core began; the retained capture shows the offset,
an inactive Core before the power cut, rollback in 0.1 seconds, byte-identical management files,
and restored temporary configuration, guest definition, time service and Core service. It made no
product-code change. Earlier RTC-clock attempts did not create the required skew and remain
historical failed attempts, not C7 evidence. The first C4 result was a harness-only false failure
that confused a spare interface's DHCP client with the management bridge; the br0-specific rerun
passed. C10 used the lab service's `Restart=on-failure` setting, so it does not validate the
packaged service's restart behavior. A first C12 interruption left only its harness firewall rule;
the harness removed it and the final guarded C12 rerun passed. The final record returned to the
C11-confirmed management baseline. This subset is not all-phase CAC replay or Phase 4 acceptance.

**S5-3 corrected source cycle.** The first final-campaign result is retained as failed. The corrected
same-operation rerun passed: after the source restarted from its durable replicate record, the
transfer succeeded and the replica/membership observations converged. This does not establish S6
promotion or close the manual-recovery gate.

**S5-4 activate-before-persist interruption.** On candidate fingerprint beginning `2f50`, a
disposable-lab debugger stopped the source at the symbolic handoff boundary after it had durably
recorded `Frozen: true` at activate and before it recorded `HandedOff`. The source was then cycled;
the three-host transfer converged and the Controller operation succeeded. Earlier polling and
inotify attempts missed that synchronous boundary and remain invalid historical attempts, not this
result. This evidence does not prove VM continuity or absence of a foreign owner record.

**S5-5 target-persisted interruption.** On the same candidate, a debugger stopped the target after
it durably recorded the new master and before it returned the activation reply. The old source had
already recorded `HandedOff`; a Controller-routed maintenance write to that source received the
documented handed-off refusal. After the source cycle and cleanup, the three-host transfer
converged and the Controller operation succeeded. Earlier TLS-fault attempts are invalid historical
attempts, not this result. The test inventory was empty, so it does not prove VM-workload
continuity or broader recovery safety.

**Three-host S6 promotion.** A clean-cycle run passed the scoped candidate preflight, fence
exclusion, promotion, retained external power-off observation and old-master restoration. A
missing-profile refusal also passed against a signed current replica. This is not the complete
manual-recovery gate: the observed VM inventory was empty, so it does not prove workload
continuity or absence of duplicate live execution; broader failure and recovery cases remain open.

**Manual-promotion browser walkthrough.** The rendered promotion dialog required the typed pool
confirmation and explicit fence acknowledgement. Its request returned a queued operation, polling
preserved that exact operation through terminal success, and the final members response named the
requested candidate as the fresh authoritative master. Desktop and 360 px captures were inspected.
This proves the UI confirmation, operation/result binding and post-operation master projection. It
does not establish returned-master recovery, resource reconciliation, all failure paths, or S6-14.

**S6-4 two-host authority state.** A retained run records the former master externally off before
preflight, a fresh operation-bound passed fence proof, and durable membership/authority state with
the candidate as master and the returning host ready. A later health observation has a separate
timestamp and is not evidence of post-restore cross-host health convergence or a single-authority
sampler.

Separate deployment evidence associates the reviewed S6-5, S6-6, S6-9, S6-10 and S6-15 results
below with the Core build beginning `d0bc8c48`; individual result files are not treated as build
provenance on their own.

**S6-9 higher-candidate preflight refusal.** With a candidate holding a signed authority replica
below that of a reachable member, and the old master externally off, preflight returned neither
promotion permission nor readiness to fence and named the higher candidate. No activation was
attempted. After the old master returned, ordinary signed convergence completed. This proves a
safe lower-candidate refusal and convergence only, not promotion success or workload continuity.

**S6-6 reachable-master refusal.** A reachable current master blocked the other host's promotion
preflight, with neither promotion permission nor readiness to fence. Durable promotion state was
unchanged on all participating hosts. This is a preflight refusal only; no fence action is expected
or claimed.

**S6-15 ordinary former-master rejoin.** The returned former master became a member with no
restriction and a verified current replica; the current master retained its accepted inventory.
This proves ordinary rejoin and accepted empty-VM inventory only. It does not prove an
ownership-conflict resolution.

**S6-14 returned-master restriction.** The corrected debugger boundary held the master before it
published the new authority. The same valid managed VM was refused through the API, the installed
local hook and an authority offer while the returned host was superseded. Managed autostart remained
suppressed for 53.4 seconds. After the ordinary rejoin cleared the restriction, the same VM
started normally through the API with matching native QEMU observation, then force-stopped to a
final shutoff state; its requested autostart setting was false. This is independently reviewed
native S6-14/OD-5 boot-autostart safety and post-clear start/stop evidence on the f714 candidate.
It does not establish duplicate-writer safety, foreign-owner behavior, permanent-failure recovery,
other start paths, any other OD gate or full Phase 4 acceptance. The earlier scoped S6-18 result is
separate evidence and neither expands nor replaces this scope.

**S6-12 pending-update reservation and recovery.** A debugger paused the master immediately
after it durably saved one VM's pending update and before it replied. The recorded pending update
and the candidate's verified signed replica agreed on its operation, VM, owner and revision-one
definition. With that VM's owning Core off, the candidate's real preflight blocked that exact VM
and did not activate it. The owner then returned and completed the same operation at revision one,
after which the disposable fixture was deleted. This proves the pending-update reservation,
per-VM block and ordinary recovery only; no contradictory-owner or accepted-ownership-mismatch
claim is made.

**S6-5/S6-10 failed fence and retry.** A candidate-only fence-endpoint fault left the same
promotion operation pending at fencing, with no authority activation or peer repoint. After the
fault was removed, that operation recorded a fresh bound fence result, promoted and repointed the
other members. The returned former master then refused an authority-writing local offer while
restricted as superseded. This does not prove guest workload continuity, normal former-master
rejoin/clear, or any S6-12 contradictory-owner behavior.

**S6-13 proof retry and reuse.** On the Core candidate fingerprint beginning `2466e2cf`, a
candidate held an aged operation-bound proof at activation while the old master was externally off.
The same operation then replaced that proof for the same host, target and configuration before
promotion completed. In a separate retained restart, the candidate had a new boot identity after
the proof persisted; the same operation-bound fresh proof remained byte-identical while the old
master stayed off, then promotion completed and cleanup returned the old master unrestricted.
Earlier incomplete fresh-proof attempts are superseded historical evidence. These two branches do
not prove the remaining S6 failure, partition, ownership or workload-safety cases.

**S6-17 boot and cleanup under the lab hook overlay.** With a member off, a rebooted master
reported neither self-fencing nor restriction. The same managed VM then terminally started, was
observed running with its expected identity, and terminally stopped while the member remained off.
The first eject was correctly refused until current inventory was available; a later retained raw
retry succeeded and recorded the ejected host's solo state and removed inventory. This is a
lab-hook-overlay behavior result, not packaged hook or AppArmor installation evidence.

**S6-18 bounded CHECKPLAN refusal and recovery.** On the reviewed Core build beginning
`d0bc8c48`, an opted-in master rebooted while its ready member was off and reported
`Restricted · unconfirmed` with the matching fresh health reason. The same valid stopped managed
VM received a terminal API-start refusal, and a direct libvirt-hook refusal with the same reason
left it shut off. An eject request against the unavailable member also terminally refused. Once
the member returned, the local restriction cleared and the same VM terminally started normally.
A later API force-stop cleanup terminally succeeded; both libvirt and Controller inventory then
reported the VM shut off, with all Core services active and no scenario fault present. A later
timed-autostart run observed suppression while restricted and restoration after return. Together
with the reviewed UI/source audit that found no force, override, declare-lost or timeout control,
this passes the exact CHECKPLAN S6-18 scope. It does not claim permanently-dead-member recovery,
broader duplicate-writer safety, every start path, or full Phase 4 acceptance.

**BUG-0377 all-producer health proof.** A later retained Controller capture reports all seven
fixed checks fresh and healthy on a fresh current master, and a separately fetched members response
projects the same seven healthy empty-detail checks with `restricted` clear. This follows the
earlier stale restriction observation, which diagnosed the defect and is not evidence that the
durable restriction remained. The contemporary raw evidence supports the corrected observed
contract outcome; a different unretained direct-shell generation claim is excluded. This is not a
Controller state repair or a broader Phase 4 result.

- The remaining S6 evidence is specific: S6-12's contradictory-owner subcase remains unexercised,
  and S6-16 lacks a supported authentic fixture. Do not claim OD-5 beyond its documented
  restricted-master limit.
- The older S6-18 CHECKPLAN scope passed. The latest `f714f71f` strict S6-18 harness remains
  partial/FAIL as described above; separate S6-14 evidence does not close it. Broader
  duplicate-writer and every-start-path evidence remains outside those scopes. For a permanently dead member, preserve OD-5's documented
  restriction/no-escape behavior; do not claim a recovery that OD-5 forbids.
- Remaining guide evidence is specific: G1 hardware coverage, a current G2 maintenance
  reboot/autostart walkthrough, planned-master-change terminal visual completion and applicable
  transfer/promotion failure and recovery paths. G3 operator-requested rollback does not prove a
  separate GUI deadline-expiry walkthrough. Retained native and browser results keep their own
  build and method boundaries; intercepted UI evidence does not replace native execution.
- OD-6 is **decided (project owner, 2026-09-20):** the strict unreachable-ready-member rule is
  approved. An unreachable ready member blocks promotion and returned-master clearance unless the
  same operation fences it; silence is never agreement and the rule stays fail-closed.
- Remaining C-class limits (physical/BMC, five-host, package-install, authentic S6-12/16,
  OD-5 no-escape) stay open forever for this lab class and are not claimed by this acceptance.

## Verification

This document was prepared by inspecting the retained historical S7 results, handover and fix
reports. It deliberately records historical, simulated and unexecuted work separately. No lab
command, browser session, fence operation or deployment was run while preparing this report.
