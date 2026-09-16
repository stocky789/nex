# Production-readiness handoff

Date: 2026-09-14. Status: **not production-ready**.

## Current status

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
| Member authority replica | Planned, not implemented: signed, generation-numbered master push of pool authority to every ready member, with observed replica generation. See [pool authority transfer](2026-09-08-nex-platform.md#pool-authority-transfer-and-master-succession). |
| Authority transfer / automatic succession | Planned, not implemented: planned handover, eject, manual promotion and quorum-plus-fence automatic succession, with their lab gates in the same section. |
| Placement approval on master | Controller still validates placement from its mirror of host reports; the master Core must approve it from its own records. |
| Physical SAN/NAS and fence devices | Needs an explicitly disposable supported hardware profile covering durability, power loss and exclusion. Nested virsh fencing does not certify physical devices. |
| Recovery, mobility and HA | Snapshots, verified backup/restore, permanent host/master loss, migration, upgrades and separately gated automatic HA/failback keep their platform gates. Shared storage does not provide them. |
| Latest ISO full installation | Nex Core 0.2.9 passed BIOS/UEFI boot-menu checks and an unattended offline UEFI installation, first boot and reboot under TCG. Secure Boot host installation, the full Phase 3 multi-disk/network/recovery gate and physical hardware remain unaccepted. |
| Release and CI | Needs real hosted GitHub/Gitea runs, a supported-version/hardware scope, source-delivery/component/firmware evidence and an artwork redistribution review. |

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
| Phase 2 acceptance gate | Never executed end to end on one build. Its halves were run in different labs on different code, and the "reboot it and verify persisted data" clause has no evidence at all. Folded into the VM creation system slice by decision on 2026-09-10; see that plan's open checkboxes. |
| ~~VM CPU/RAM/disk usage~~ **closed 2026-09-16** | Core samples active VMs with `virsh domstats --raw --nowait`, Controller passes the nullable report through `GET /api/v1/vms`, and VM details renders it. Real-browser acceptance against deployed current Core/Controller passed: 2% CPU over 5 s, 4 GiB assigned/maximum and host cost, 5.9 GiB used/32 GiB capacity, current sample age, and the explicit not-zero state for a shut-off VM with null usage. `--nowait` remains required because a job-taking probe can stall behind a long shutdown. |

**BUG-0097 nested UEFI remains explicitly excluded.** Do not work on it or claim
UEFI/Secure Boot acceptance from BIOS tests. Never infer host death or writable
exclusion from a timeout.

API impact: **none**, guide impact: **none**, state owner: **none**. This is a
status and handoff document.
