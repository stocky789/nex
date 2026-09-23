# Production-readiness handoff

Created: 2026-09-14. Updated: 2026-09-20. Status: **not production-ready**.

## Current status

**Phase 4 is closed for disposable virtual-lab scope (2026-09-20); production remains open.** S1–S6
are implemented in the dirty working tree. The final `f714` source-validation run passed Go race
checks and the 374-test Python suite; it is not a release. Reviewed native evidence includes the
repeated G1 lifecycle and missing-uplink recovery/cleanup, and S6-14 boot restriction, autostart
suppression, refusal, rejoin and post-clear normal start/stop. `BUG-0372`–`BUG-0381` are fixed and
the newest Core keeps its boot marker private. The final `f714` S6-18 strict harness is
partial/FAIL: safe eject refusal for a live shared-storage reservation differed from its expected
wording, so that run exited before timed-suppression observation. Its older scoped S6-18 result is
separate historical evidence.

Phase 4 and the initial manual-recovery gate are accepted for disposable virtual-lab scope only.
OD-6 is decided: the strict unreachable-ready-member rule is approved (fence or fail-closed).
Permanent limits stay open forever for this lab class: physical/BMC, five-host, package-install,
authentic owner-conflict S6-12/16, OD-5 no-escape, and broader duplicate-writer/workload evidence.
These results make no production or release claim.

The BUG-0173 clean build (with BUG-0172) and the later storage-cluster refactor
rebuild each passed guarded three-Core rollout, a clean 150-sample idle health
observation, fresh six-guest (two per Core) concurrent write workloads on the single
pool-wide SMB, NFS and clustered-iSCSI GFS2 repositories with hash rereads and
cross-Core image lock refusal, and a clean post-workload observation. The shared
concurrent read/write profile therefore works for all three shared storage types in
bounded, no-fault lab evidence. The earlier intermittent repository deadlines
(investigated under BUG-0155 through BUG-0173) did not recur in those observations.

Still open: exclusive-iSCSI registration/guest/adoption acceptance and the fault,
scale, soak, physical-device and CI/release gates below. Results, receipts and
failed attempts: [reliability follow-up](../qa/2026-09-14-cluster-storage-reliability.md)
and [concurrent storage QA](../qa/2026-09-12-concurrent-pool-storage.md).

## What changed in the 2026-09-18 review and implementation

These are working-tree achievements, not a new release or phase closure. Phase 2
was already closed before this work; the review corrected stale status wording.

| Initial finding | Outcome now | Remaining evidence |
|---|---|---|
| Release publication checked package metadata but could miss a Controller that exits at startup (BUG-0246). | Both release workflows now extract and start the exact Controller package, check SQLite, UI/source and authenticated sessions before publication. Real CGO-enabled and CGO-disabled fixture packages prove acceptance and rejection locally and in Debian 13. | Exact 0.3.2 package startup and installed systemd restart/crash/reinstall/reboot checks passed in the [artifact lab](../qa/2026-09-18-release-artifact-lab.md). Hosted acceptance remains open: inspected Gitea HEAD jobs failed, GitHub evidence was unavailable, and the tested snapshot is uncommitted. |
| Controller resource/capacity mirrors still decided VM placement. | Core master now approves create/update and media definition changes, obtains authenticated target preflight and persists owner/specification or update reservation. Local-storage definitions are included; ordinary local-only power remains independent of the master. | Bounded two-host KVM/NFS lab now passes local master/member create/edit/start and retries; replacement, storage-identity faults and interrupted-operation acceptance remain open. See [placement QA](../qa/2026-09-18-master-vm-placement.md). |
| README and handoff mixed historical gaps with current implementation. | Phase 2 closure and published package history are reconciled. API contracts, guide prerequisites/retries, changelog and roadmap distinguish implementation from acceptance. | Phase 3/4 and production gates remain open; no new acceptance was inferred from documentation. |
| Verification needed to match the new behavior. | Full Go race checks and final agent/vmhost race checks passed; API generation, 29 contract checks, simulated browser workflows, docs/privacy checks and independent review passed. Exact retry and stale-membership checks preserve durable records; refused replay cannot enter native reconciliation. | Native commands and browser Core responses use fixtures where stated. The late namespace-dependent test was removed and replaced with portable admission checks. Native two-node KVM/NFS evidence is now recorded separately; no hosted CI or physical-host proof is claimed. |

## Recommended next work

This orders the existing gates below; it does not expand scope or reopen accepted
Phase 2 work. The immediate objective is **one exact current build, with delivery
and Phase 3 installation evidence**, followed by native placement acceptance. Items 1 to 3 are
closed as of 2026-09-23 (items 2 and 3 for disposable virtual-lab scope); physical hardware
remains open.

1. **Signed off by project owner, 2026-09-23; no further hosted-delivery acceptance is
   required for this item.** Historical record follows.
   **Local artifact verification signed off; finish hosted delivery acceptance.**
   The [recorded 0.3.2 snapshot](../qa/2026-09-18-release-artifact-lab.md) passed real
   builders, artifact/source hash binding, both installed package services,
   Controller same-version reinstall and package-service crash/reboot recovery.
   The exact appliance boot and offline BIOS ISO install/reboot also passed.
   Later storage edits are excluded. Build/test the final source before release;
   after the project owner's push, obtain green required hosted jobs and a release
   run for that revision. Inspection found failed Gitea HEAD checks, no HEAD
   release run and unavailable GitHub evidence; this is not green CI sign-off.
2. **Closed for disposable virtual-lab scope, 2026-09-23.** The ISO-installed chain (offline
   install under enforcing Secure Boot and under BIOS, one-of-two and mirrored disks, absent and
   real management networking, interrupted install, eject/reboot, appliance bootstrap,
   interrupted appliance deployment, second-host enrollment, first managed VM, Controller-outage
   recovery access) passed in the [2026-09-23 lab](../qa/2026-09-23-phase3-placement-lab.md);
   defects BUG-0533, BUG-0534 and BUG-0540 were fixed. The BUG-0540 fix was deployed as a binary
   swap, not a fresh ISO install. Physical Intel/AMD hardware stays open.
   Historical record follows.
   **Finish Phase 3 integration on the release candidate.** The bounded offline
   BIOS installation, media-free first boot/reboot and service checks are complete
   for that snapshot. Still connect the exact installed host through local appliance
   bootstrap or external-Controller enrollment to its first managed VM. Exercise
   multiple disks, real management networking, interrupted installation/bootstrap
   and recovery; retain physical Intel/AMD and host Secure Boot gates. UEFI menu
   review does not establish current-build UEFI installation. Appliance assembly
   and separate-host export/restore already exist; verify the integrated chain
   rather than rebuilding them. See the
   [appliance acceptance record](2026-09-08-controller-appliance.md#acceptance-evidence).
3. **Closed for disposable virtual-lab scope, 2026-09-23.** Lost replies, shared definition and
   media changes, storage loss and identity change, membership races, interrupted create/update
   and Controller replacement passed on ISO-installed hosts; BUG-0535 to BUG-0538 were found and
   fixed (owner decisions, 2026-09-23: settle a create interrupted before allocation as failed;
   the member journals a refused operation so the master can release its reservation). See the
   [lab record](../qa/2026-09-23-phase3-placement-lab.md). Historical record follows.
   **Finish native placement fault/recovery acceptance.** Local master/member
   create/update/start, NFS member placement, missing network/capacity rejection,
   exact retries and service-outage continuity now have bounded native evidence.
   Still cover shared definition/media changes, storage identity loss, lost replies,
   membership changes and Controller replacement. Prove one durable
   owner and no second writer. Use the [placement plan](2026-09-18-master-vm-placement.md#acceptance)
   as the checklist; code implementation is complete, this evidence is not.
4. **Phase 4 acceptance — closed for disposable virtual-lab scope (2026-09-20).** Every closure
   slice is implemented and reviewed native/browser evidence is recorded in the
   [native acceptance report](../qa/2026-09-19-phase4-native-acceptance.md) and
   [closure plan](2026-09-19-phase4-closure.md). OD-6 strict unreachable-member rule is approved.
   Permanent limits (physical/BMC, five-host, package-install, authentic S6-12/16, OD-5 no-escape)
   are not claimed for this lab class. Post-review binary redeploy and S2/maintenance revalidation
   remain underway. Automatic succession retains its own quorum/fencing gate and is untouched.
   **Historical note (2026-09-19):** two independent reviews of the unreleased slices ran before the
   campaign; every blocking finding is fixed (`BUG-0333`–`BUG-0353`). Four named design limits
   remain documented: only a `virsh` fence profile can be recorded; an opted-in pool whose master
   restarts while one other ready member is permanently dead stays restricted with no escape
   (OD-5); the management-network rollback needs the Nex Core agent running (both units are now
   `Restart=always`, but a deliberately stopped or masked unit arms nothing); a manual `virsh start`
   of a managed guest on a drained **member** is not refused by the libvirt hook; a
   management-network spec that omits a NIC carrying guest VLANs leaves that device unconfigured at
   the next boot; and the 1 MiB pool-authority replica ceiling ends the ability to promote with no
   operator lever. **OD-6 (project owner, 2026-09-20):** a promotion blocks while a ready member is
   unreachable unless the same promotion fences it — the approved strict rule; a promotion can
   power off more than one host.
5. **Only then expand into the dependent roadmap.** Snapshots precede independent
   backup and verified restoration, followed by migration and later HA. Managed
   container applications stay after the VM foundation. Shared storage alone does
   not make those features available.

API impact: **none** for this status/priority update; the implemented placement
contract is assessed in its own plan and change record. Guide impact: **none**;
this adds no operator workflow. State owner: **none**; no durable product state changes.

## Start here

- [AGENTS.md](../../AGENTS.md) for working rules, the architecture invariant and checks.
- [Platform plan](2026-09-08-nex-platform.md) for phases and gates.
- [Concurrent-storage plan](2026-09-12-concurrent-pool-storage.md) for scope, safety design and acceptance gates.
- [Bug ledger](../BUG_LEDGER.md), the authoritative bug record.
- Preserve the uncommitted worktree. Private lab evidence (credentials, addresses,
  receipts, helpers) lives only under the ignored `.cache/`; re-observe the lab and
  inspect receipt bindings before any disruptive action. Several historical helpers
  pin old binaries or failed assumptions, so do not run them by filename alone.

## Open work

This table is the single list of open production-readiness gates. The failure and
unexecuted items are kept apart.

| Item | Missing evidence or prerequisite |
|---|---|
| Exclusive iSCSI profile | Fixture provisioned; registration, guest write/reboot and adoption acceptance not run on the current build. |
| Failed fencing / no duplicate writer | An exact-target native fence failure while status and monitoring stay valid, proving that uncertain ownership blocks replacement starts. Blocking the shared fence port or generic SSH/Polkit interception does not substitute. |
| Cluster/management partitions | One six-writer Core3 natural heartbeat partition passed fence, survivor integrity and guarded return. The rest of the partition/quorum matrix is open. |
| Stop under lost quorum or frozen I/O | BUG-0144 covers only an unavailable master with healthy storage. The force-stop fault attempts remain inconclusive. |
| Master-agent and Controller outage/replacement | The bounded six-writer master-agent outage, Controller C outage and C-to-D replacement passed. This is not credential-failure, transport-fault, aggregate-health or HA acceptance. |
| Storage placement transitions | BUG-0132's stopped local → NFS → local walkthrough passed. Broader transitions and native disk-placement conversion remain open. |
| Activation failure/replay | The selected BUG-0135 master fence/replay passed. The remaining failure modes are open. |
| Five-host scale and soak | Three-host results do not certify five hosts, long-duration load or repeated fault recovery. |
| Member authority replica | **Implemented in the dirty working tree; full acceptance open.** The master signs its durable pool authority with the pool CA key, seals it per member, declares per-section digests and pushes it to ready members; members store it read-only and act on none of it. Reviewed G1/S5/S6 lifecycle evidence observes signed replicas, while complete replica-push/master-restart, fault and browser acceptance remains open. It is a recoverable copy, not independent authority. See [pool authority transfer](2026-09-08-nex-platform.md#pool-authority-transfer-and-master-succession) and the [pool authority guide](../guide/pool-authority-and-recovery.md). |
| Authority transfer / automatic succession | **Closed for disposable virtual-lab scope (2026-09-20).** Planned master change, member eject, fence-verified promotion and safe rejoin are implemented; scoped reviewed native/browser evidence covers selected eject, transfer, promotion and rejoin cases with external `virsh` power-off. It does not establish physical fencing, workload continuity, duplicate-writer safety, authentic owner-conflict handling or all recovery paths. S6-14 boot restriction, autostart suppression, rejoin and post-clear start/stop were independently reviewed. The final `f714` S6-18 strict harness is partial/FAIL; its older scoped result is separate. Only a `virsh` fence profile can be recorded. OD-5 remains restricted/no-escape; OD-6 strict unreachable-member rule is approved. Automatic succession is still planned and not implemented. |
| Master-owned VM placement acceptance | **Closed for disposable virtual-lab scope, 2026-09-23.** The full fault matrix (lost replies, shared edits, storage loss and identity change, membership races, interrupted create/update, Controller replacement) passed on ISO-installed hosts; BUG-0535 to BUG-0539 were fixed ([lab record](../qa/2026-09-23-phase3-placement-lab.md)). Earlier record: **Implemented, acceptance open.** Core now approves create/update and VM media definition changes from the pool master's records using a fresh target-host preflight; Controller mirrors no longer grant or reject placement. Focused tests, browser review and bounded two-node nested-KVM/NFS checks passed ([native evidence](../qa/2026-09-18-native-placement-lab.md)). Controller replacement, storage-identity faults, membership races and interrupted multi-host recovery remain open; the Phase 4 closure lab exercised the join/convergence matrix on the old build but has not yet run the placement fault matrix at all. One related defect was found and fixed there: a create aimed at a member whose shared-storage access the master last reported as stale sat `dispatched` with no operator signal, and now carries an advisory warning that refuses nothing and clears itself (`BUG-0326`). |
| Physical SAN/NAS and fence devices | Needs an explicitly disposable supported hardware profile covering durability, power loss and exclusion. Nested virsh fencing does not certify physical devices. |
| Recovery, mobility and HA | Snapshots, verified backup/restore, permanent host/master loss, migration, upgrades and separately gated automatic HA/failback keep their platform gates. Shared storage does not provide them. |
| Latest ISO installation evidence | **2026-09-23:** the 0.4.5 development ISO passed the full Phase 3 chain for disposable virtual-lab scope, including UEFI installation under emulated enforcing Secure Boot, multiple disks, interrupted installation and interrupted appliance deployment ([lab record](../qa/2026-09-23-phase3-placement-lab.md)). Physical Intel/AMD hardware remains open. Earlier record: the local 0.3.2 snapshot (`ddb3887a…cac6d5`) passed offline BIOS/KVM installation, first boot/reboot, installed Core service and nested KVM in the [artifact lab](../qa/2026-09-18-release-artifact-lab.md); its UEFI coverage is menu-only and management was explicitly loopback-configured. The earlier exact 0.2.9 release ISO (`4c44133a…2f2b9`) passed BIOS/UEFI menu boots and unattended offline UEFI installation, first boot and reboot under TCG; Secure Boot host installation, the full Phase 3 multi-disk/network/recovery gate and physical hardware remain unaccepted. |
| Release and CI | Release tooling creates a whole-media source bundle, final-ISO/source binding manifest, and publisher-side binding check; it omits six unreviewed firmware packages and records the Nex artwork license. Exact hashes, component/source closure, retained notices, BIOS/UEFI menus and unattended install/reboot passed. The 0.3.1 ISO, matching source, manifest and both signed APT packages were published and independently hash-verified over public HTTPS on 2026-09-17; its extracted Controller package started with CGO support, an isolated SQLite database and HTTPS 200 ([release verification](../qa/2026-09-17-nex-core-0.3.1-release.md)). **BUG-0246:** Controller packages from 0.1.6~alpha1 through 0.2.9 were built without cgo and exit at start; those retained historical packages were superseded by 0.3.0+ publication, not removed. This is resolved for Phase 2 scope ([close record](../qa/2026-09-17-phase2-gate/CLOSED.md)). Both release workflows now gate publication on packaged-Controller startup, SQLite, UI/source and authenticated-session checks; the real good/bad binary fixture regression passed locally ([evidence](../qa/2026-09-18-controller-package-smoke.md)). The exact local 0.3.2 package and installed services now pass ([artifact evidence](../qa/2026-09-18-release-artifact-lab.md)); green hosted execution for the final release revision remains required evidence. A supported-version/hardware scope also remains open. |

### VM foundation, added 2026-09-16

Recorded during a roadmap position review. These are host-verification debts against the
existing VM foundation, not new scope, and they share one disposable nested-KVM host. That
host must be separate from the one carrying the Windows 11 defect work, because these items
reboot it. Ordering matters: the firmware probe decides an implementation shape, so it runs
before the slice it gates is built, and the rest run after that slice lands.

**Partly discharged 2026-09-16.** A disposable lab on a contributor workstation ran the Nex Core
engine against real libvirt 12.7 / QEMU 11.1 / swtpm 0.10.2 and closed the TPM and graceful-shutdown
items and two thirds of the Windows 11 re-run; see the [verification report](../qa/2026-09-16-windows-11-fix-verification.md)
for what it did and did not establish. It was **not** Nex Core: no ISO was built, no Controller ran,
and the library versions are newer than Core ships, so nothing below about packaging, the installer
or the browser is affected. The firmware items were deliberately untouched — they need the probe
first, on a host that may be rebooted.

| Item | Missing evidence or prerequisite |
|---|---|
| ~~Firmware variable store probe~~ **closed 2026-09-16** | Disposable nested-lab probe passed: libvirt accepted firmware auto-selection with an explicit template-free UUID-keyed NVRAM path. The Core lifecycle create/start/stop/rename retained it, and simultaneous rename plus UEFI-to-BIOS cleanup removed the exact regular file. Focused race checks passed, including `cleaning-nvram` recovery. Evidence: [firmware variable-store verification](../qa/2026-09-16-firmware-variable-store-verification.md). |
| ~~Secure Boot takes effect in a guest~~ **closed 2026-09-16** | A signed Debian guest reported EFI Secure Boot `01` on and no SecureBoot variable after the stopped off edit; host SecureBootEnable was `00`. |
| ~~UEFI VM rename keeps its firmware variables~~ **closed 2026-09-16** | Final-package on → rename lifecycle retained UUID-keyed NVRAM, hash and boot entries, and the renamed guest booted. |
| ~~Orphaned firmware variable store (BUG-0214)~~ **closed 2026-09-16** | A simultaneous rename and UEFI-to-BIOS stopped edit removed the exact validated NVRAM regular file. The journal records `cleaning-nvram` before destructive work and recovery observes the domain before retrying; focused race checks passed. |
| ~~Enrolled Secure Boot certificates~~ **closed 2026-09-16** | The final ISO package/template inspection found both Microsoft UEFI CA 2023 and Windows UEFI CA 2023 in the enrolled OVMF variables template. |
| ~~Virtual TPM (BUG-0196)~~ **closed 2026-09-16** | Verified on real libvirt/QEMU/swtpm: the `tpm-crb` device reached the live domain, swtpm manufactured an EK certificate and the guest wrote TPM state, Windows reported `IsActivated_InitialValue: TRUE, SpecVersion: 2.0`, and **Windows 11 Enterprise LTSC — the non-IoT edition — installed and booted**. Deleting a TPM VM removed its swtpm directory and NVRAM. Evidence: [verification report](../qa/2026-09-16-windows-11-fix-verification.md). **Still open:** the TPM and Secure Boot failing *independently* was not exercised — both were present throughout. |
| Windows TPM/Secure Boot independence matrix | **Open.** Fresh 60 GiB cases created, booted and deleted with expected live XML combinations, but Windows Setup did not reach the non-IoT requirements decision. Optical-prompt timing and manual Setup flow limited the harness. Do not close from definition or start evidence alone. |
| ~~BUG-0213 false-failure settlement~~ **fixed 2026-09-16** | Native A/B: pre-fix `wall=20.138s`, `needs_attention`, `virsh failed: signal: killed`; fixed `wall=1m0.073s`, `succeeded`. The guest reached `shut off` 17 s after the pre-fix false failure, proving the prior report was wrong. |
| BUG-0213 graceful-shutdown timing | **Open.** The historical ~215 s task gap has no recoverable cause. A current final-package attempt submitted no graceful shutdown because guest-agent readiness did not arrive within bounded attempts. |
| ~~Windows 11 walkthrough re-run~~ **closed 2026-09-16** | Non-IoT Windows 11 Enterprise LTSC installed with a TPM present and a clean shutdown reported succeeded. The remaining VirtIO system-disk clause also passed: Setup showed no disk until the Red Hat VirtIO SCSI driver was loaded from virtio-win 0.1.302 on the second CD drive, then exposed the 64 GiB VirtIO disk and reached OOBE after installation. The disposable VM and disks were deleted. Evidence: [firmware variable-store verification](../qa/2026-09-16-firmware-variable-store-verification.md). |
| Bundled virtio-win driver ISO | **Closed 2026-09-17 on 0.3.2.** Fedora virtio-win 0.1.302 is on the Core ISO with retained Fedora corresponding source ([ISO build](../qa/iso/2026-09-17-core-0.3.2-build.md)). |
| Phase 2 acceptance gate | **Closed 2026-09-17** by project owner ([close record](../qa/2026-09-17-phase2-gate/CLOSED.md)). Gate5 functional clauses; BUG-0256 + admission on published 0.3.1; VirtIO on 0.3.2. Accepted residuals and deferred follow-ups are listed in the close record. |
| ~~VM CPU/RAM/disk usage~~ **closed 2026-09-16** | Core samples active VMs with `virsh domstats --raw --nowait`, Controller passes the nullable report through `GET /api/v1/vms`, and VM details renders it. Real-browser acceptance against deployed current Core/Controller passed: 2% CPU over 5 s, 4 GiB assigned/maximum and host cost, 5.9 GiB used/32 GiB capacity, and the explicit not-zero state for a shut-off VM with null usage. The Sample age row was removed the same day; freshness stays on the panel heading. `--nowait` remains required because a job-taking probe can stall behind a long shutdown. |
| Native pool CPU baseline | **Open, implemented for 0.3.1 on 2026-09-17.** Core discovers and compares the full native CPU definition across every current pool member; the chosen model/features persist per VM. Native single-host runtime and synthetic reduced-input checks, authenticated simulated peers, browser checks and the full Go race suite passed ([evidence](../qa/2026-09-17-cpu-baseline.md)). Physical heterogeneous pools, migration acceptance and the original installer reboot diagnosis remain unverified. Native libvirt autostart bypasses the Nex preflight. No OS-specific CPU policy was added. |
| VM and host usage history | **Open, implemented 2026-09-17.** Stats tab history (10 minutes to 1 year, kept by Core) and the Realtime drawer (1 s, host-owned lease) passed every exercised check on a simulated nested-KVM host, including Controller replacement ([QA record](../qa/2026-09-16-usage-history.md)). Still needs: the guide walkthrough with the default 10 s heartbeat, the 8-view realtime cap and 30-day/1-year tiers on a host, rates under load, and a host re-check of two later changes (agent `After=time-sync.target`, compact Disk chart labels). See the [plan](2026-09-16-usage-history.md#remaining-work). |

**BUG-0097 nested UEFI remains explicitly excluded.** Do not work on it or claim
UEFI/Secure Boot acceptance from BIOS tests. Never infer host death or writable
exclusion from a timeout.

API impact: **none**, guide impact: **none**, state owner: **none**. This is a
status and handoff document.
