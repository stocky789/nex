# Phase 5 gate closed

Date: 2026-09-27. Decision: project owner — close Phase 5 now for disposable
virtual-lab scope.

## Verdict

**Phase 5 (snapshots and rollback) is closed for disposable nested-lab scope.**

S0–S3 and the support matrix are evidenced on the disposable nested lab. Core owns
the snapshot catalog; Controller is relay-only. Owner authorized close 2026-09-27.
Phase 6 / S4–S8 are not started and remain open.

## Evidence that closes the gate

| Requirement | Evidence |
|---|---|
| S0–S3 + support matrix (disposable lab) | [support matrix](../2026-09-26-phase5-snapshot-support-matrix.md); [recovery plan](../../plans/2026-09-25-recovery-snapshots-backups-restores.md) Phase 5 rows; [handoff](../../plans/2026-09-26-recovery-session-handoff.md) |
| Stopped create/list/rename/revert/leaf-delete (Core + UI) | [browser QA](../2026-09-26-recovery-snapshot-browser.md); [Core API prep](../2026-09-26-recovery-core-api-preparation.md); [vm-snapshots guide](../../guide/vm-snapshots.md) |
| Multi-disk / firmware / consolidation / interruption | Recovery plan S0/S2 QA under `docs/qa/2026-09-25-*` and `2026-09-26-*`; [multi-backend S0 DR](../2026-09-26-recovery-s0-multi-backend-dr.md) (native, not product-on-all-backends) |
| Running crash/quiesced + UEFI refusal + thaw | [running-mode QA](../2026-09-26-recovery-snapshot-running-mode.md) |
| Authority, conflicts, Controller replacement | [promotion r8](../2026-09-26-recovery-snapshot-promotion-r8.md); [core crash r8](../2026-09-26-recovery-snapshot-core-crash-r8.md); fresh Controller adoption in [Core API prep](../2026-09-26-recovery-core-api-preparation.md); BUG-0641 / BUG-0647 ledger entries |
| Core-owned snapshot catalog (invariant) | `internal/agent/pool_vm.go` (`pool-vms.json` Snapshots); `internal/vmhost/managed_snapshot.go`; Controller relay-only `internal/controller/snapshots.go`; adoption `internal/controller/snapshot_adoption_test.go` |
| Forge CI on published push | HEAD `ac92f407` branch `phase5`; forge Actions runs **4524, 4525, 4526** all success (title: ci: limit forge AppArmor recovery probe to midcommit) |
| Independent acceptance review | Phase 5 close review 2026-09-27 (state ownership pass; disposable-lab residuals named; no blocking findings) |

## Accepted residuals (not Phase 5 blockers)

- Product Controller/Core snapshot paths not certified on every S0 backend (NFS/SMB/exclusive iSCSI); S0 native ≠ S2/S3 product
- Physical / production storage and hardware fencing untested (nested disposable lab only)
- Memory snapshots out of Phase 5 scope
- Integrated libvirt snapshot data-delete unsupported; public blockcommit + metadata-only delete is the selected path
- Full responsive/theme matrix and formal two-operator usability campaign not executed as a separate gate
- Running write-workload marker-across-revert not in the S3 running-mode receipt set
- Broader multi-host replacement / disaster coverage beyond recorded nested campaigns (Phase 6 three-host campaign still open by design)
- ISO/package-install acceptance and independent physical failure domains not claimed
- BUG-0639 historical second-disk / forensic-image uncertainty remains an explicit limitation
- Phase 6 / S4–S8 not started

## Process note

Forge CI inspected on published push `ac92f407` (runs 4524–4526 success). Independent
review closed with no blocking findings. Owner authorized close 2026-09-27. Agents do
not commit/push.
