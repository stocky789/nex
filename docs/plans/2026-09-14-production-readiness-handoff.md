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
| Physical SAN/NAS and fence devices | Needs an explicitly disposable supported hardware profile covering durability, power loss and exclusion. Nested virsh fencing does not certify physical devices. |
| Recovery, mobility and HA | Snapshots, verified backup/restore, permanent host/master loss, migration, upgrades and separately gated automatic HA/failback keep their platform gates. Shared storage does not provide them. |
| Latest ISO full installation | The tenth ISO passed artifact audit and the native wizard only. The full BIOS install/persistence evidence belongs to the seventh ISO. UEFI and physical hardware are unaccepted. |
| Release and CI | Needs real hosted GitHub/Gitea runs, a supported-version/hardware scope, source-delivery/component/firmware evidence and an artwork redistribution review. |

**BUG-0097 nested UEFI remains explicitly excluded.** Do not work on it or claim
UEFI/Secure Boot acceptance from BIOS tests. Never infer host death or writable
exclusion from a timeout.

API impact: **none**, guide impact: **none**, state owner: **none**. This is a
status and handoff document.
