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
| Latest ISO full installation | The tenth ISO passed artifact audit and the native wizard only. The full BIOS install/persistence evidence belongs to the seventh ISO. UEFI and physical hardware are unaccepted. |
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
| Firmware variable store probe | **Gates the firmware slice; run first.** Whether libvirt accepts `<os firmware='efi'>` with an explicit `<nvram>` path carrying no template attribute and still resolves firmware autoselect. Established from libvirt source only. If it refuses, the fallback is an explicit `<loader>` read from the host's firmware descriptors, which is a different implementation. |
| Secure Boot takes effect in a guest | A guest must report Secure Boot enabled, then disabled after the setting is changed (`mokutil --sb-state` or equivalent). Nex currently accepts the change, reports it applied, and the guest keeps its previous state; enforcement lives in the firmware variable store, which no code re-instantiates. Unit tests cannot establish this. |
| UEFI VM rename keeps its firmware variables | Rename a UEFI VM, then confirm the guest still boots with its boot entries intact. The variable store path libvirt generates is keyed on the domain name, which Nex rewrites on a stopped edit. Found by code reading; never reproduced. |
| Orphaned firmware variable store | **Assigned to this slice, 2026-09-16.** BUG-0214 is closed here rather than by a separate fix, by project-owner decision. Still to establish: whether the orphan occurs as recorded, and whether a bare `undefine` succeeds for a defined-but-never-started VM. Both are gated on file existence in libvirt, so the failure is intermittent by VM lifecycle. Note the related decision that firmware stays changeable on a stopped VM, so refusing the transition is not an available fix. |
| Enrolled Secure Boot certificates | Which Microsoft certificates the host's enrolled-keys template actually holds. The 2011 UEFI CA expired in June 2026, so a 2011-only template refuses a bootloader signed with the 2023 chain and presents to an operator as "Secure Boot is on and Windows will not start". Not visible from Nex's code. **Unchanged by the 2026-09-16 lab:** that host had no enrolled-keys build at all, so one was generated locally with `virt-fw-vars --enroll-redhat`. Those are not Microsoft's certificates and prove nothing about Core's shipped template. |
| ~~Virtual TPM (BUG-0196)~~ **closed 2026-09-16** | Verified on real libvirt/QEMU/swtpm: the `tpm-crb` device reached the live domain, swtpm manufactured an EK certificate and the guest wrote TPM state, Windows reported `IsActivated_InitialValue: TRUE, SpecVersion: 2.0`, and **Windows 11 Enterprise LTSC — the non-IoT edition — installed and booted**. Deleting a TPM VM removed its swtpm directory and NVRAM. Evidence: [verification report](../qa/2026-09-16-windows-11-fix-verification.md). **Still open:** the TPM and Secure Boot failing *independently* was not exercised — both were present throughout. |
| ~~Graceful shutdown (BUG-0213)~~ **closed 2026-09-16** | Verified by A/B on one Windows guest with the guest agent connected, which is the condition that makes libvirt's shutdown hold the domain job. Pre-fix binary: `wall=20.138s`, `needs_attention`, `virsh failed: signal: killed` — the recorded symptom. Fixed binary: `wall=1m0.073s`, `succeeded`. The guest reached `shut off` 17 s after the pre-fix run had declared failure, confirming that report was false. **Still open:** the original entry's unexplained ~4 minute delay is not accounted for; this reproduced a 20 s kill, not that delay. |
| Windows 11 walkthrough re-run | **Two of three done 2026-09-16.** Non-IoT Windows 11 (Enterprise LTSC) installed with a TPM present, and a clean shutdown reported succeeded. **Still open: a VirtIO *system disk* install using the driver disc.** virtio-win was delivered on the second CD drive and installed after the fact, but the guest's system disk was SATA, so loading a storage driver at Setup's disk-selection step was never exercised — which is the half W3 was actually about. |
| Phase 2 acceptance gate | Never executed end to end on one build. Its halves were run in different labs on different code, and the "reboot it and verify persisted data" clause has no evidence at all. Folded into the VM creation system slice by decision on 2026-09-10; see that plan's open checkboxes. |
| VM CPU/RAM/disk usage | **Core side implemented 2026-09-16**, UI not. Core samples each active VM with `virsh domstats --raw --nowait` and reports CPU/memory/disk on the host inventory; Controller validates and passes it through on `GET /api/v1/vms`. `--nowait` is deliberate: without it the probe takes the domain's job and would stall behind exactly the long shutdown BUG-0213 describes. **Still open:** no UI surfaces it, and it has never run against real libvirt — key names and units come from libvirt's reference, and whether `domstats` works on Core's read-only connection is unconfirmed. Roadmap item 8 therefore stays open. |

**BUG-0097 nested UEFI remains explicitly excluded.** Do not work on it or claim
UEFI/Secure Boot acceptance from BIOS tests. Never infer host death or writable
exclusion from a timeout.

API impact: **none**, guide impact: **none**, state owner: **none**. This is a
status and handoff document.
