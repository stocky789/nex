# Recovery implementation handoff

Date: 2026-09-27. **Phase 5 closed for disposable virtual-lab scope (2026-09-27).**
Close record: [docs/qa/2026-09-26-phase5-gate/CLOSED.md](../qa/2026-09-26-phase5-gate/CLOSED.md).
**Phase 6 closed for disposable virtual-lab scope (2026-09-27).**
Close record: [docs/qa/2026-09-27-phase6-gate/CLOSED.md](../qa/2026-09-27-phase6-gate/CLOSED.md).
Index: [docs/qa/2026-09-27-phase6-implementation-index.md](../qa/2026-09-27-phase6-implementation-index.md).
Physical/production certification is not claimed.
Resume the [recovery plan](2026-09-25-recovery-snapshots-backups-restores.md) for residuals
and later phases. Agents never commit, push, tag or release; project owner owns Git remotes.

## Delivery status

| Slice | Actual status | Remaining boundary |
|---|---|---|
| S0 native feasibility | Accepted for recorded disposable-lab scope | Not product Controller/Core workflows |
| S1 chain/authority | Accepted for recorded lab matrix (r8/r9 evidence) | Broader physical/multi-host residuals elsewhere |
| S2 stopped lifecycle | Product paths + browser evidence recorded | Accepted under Phase 5 close |
| S3 running create | Crash/quiesced BIOS lab + UEFI refusal + freeze/thaw | Accepted under Phase 5 close |
| Phase 5 gate | **Closed** (2026-09-27, disposable virtual-lab scope) | [Close record](../qa/2026-09-26-phase5-gate/CLOSED.md) |
| S4 destination / stopped backup / restore-as-new | **Accepted (disposable)** | [Product lab](../qa/2026-09-27-phase6-product-backup-lab.md); [lab install](../qa/2026-09-27-phase6-lab-install.md) |
| S5 online full backup | **Accepted (bounds)** | Crash unit/contract; quiesced/UEFI online refused; live online crash guest residual |
| S6 policies / schedules / retention | **Accepted (bounded)** | Campaign A Controller-absent Run now; full wall-clock span residual |
| S7 replace / protect / test restore / catalog / Core-local | **Accepted (bounded)** | Replace cutover + Campaign B re-run PASS; full three-host / physical residual |
| S8 Phase 6 close | **Closed** (disposable virtual-lab scope) | [Close record](../qa/2026-09-27-phase6-gate/CLOSED.md) |

## What is already proven (do not re-litigate)

- Stopped create / list / rename / revert / leaf-delete through Core + Controller UI.
- Running BIOS crash and quiesced create; UEFI running create refused before effects.
- Bulk VMs-list stopped Snapshot; Snapshots confirm-dialog / focus / a11y follow-up **PASS**.
- Controller replacement / relay / exact-retry recovery (including BUG-0647).
- Support matrix:
  [docs/qa/2026-09-26-phase5-snapshot-support-matrix.md](../qa/2026-09-26-phase5-snapshot-support-matrix.md).
- Browser QA (snapshots):
  [docs/qa/2026-09-26-recovery-snapshot-browser.md](../qa/2026-09-26-recovery-snapshot-browser.md).
- Native multi-backend S0 lifecycle + midcommit consolidation (local / NFS / SMB / iSCSI)
  on libvirt `11.3.0-3+deb13u3+nex4`:
  [docs/qa/2026-09-26-recovery-s0-multi-backend-dr.md](../qa/2026-09-26-recovery-s0-multi-backend-dr.md).
  That is **native** DR evidence, not Phase 6 product backup/restore.
- S4–S7 unit/contract suites for the implemented backup/restore surfaces (see change
  records under `docs/changes/2026-09-27-phase6-s*.json`).
- Product NFS stopped backup → disks aside → restore-as-new on disposable lab.
- Bounded Campaign A (Controller-absent Run now + restore + fresh Controller adoption).
- Bounded Campaign B re-run after BUG-0651 (promote backup continuity + restore + adoption).
- Browser Backups rail walkthrough; BUG-0649/0650 settlement fixed in code (live re-walk residual).
- Hermetic Phase 6 backup CI regression (`internal/agent/backup_ci_test.go`).

## Accepted residuals (Phase 6 disposable close — not reopen blockers)

- Nested disposable lab ≠ physical failure domains.
- Quiesced online / UEFI online refused.
- Live browser re-walk after settlement fix may remain outstanding.
- Full three-host simultaneous join + schedule wall-clock span not fully proven.
- Package `0.4.12` + binary overlay (not ISO install acceptance).
- Forge CI on published push not yet inspected (agents do not push) — owner push residual.
- Measured durations / formal usability matrix / incremental CBT remain later or residual.

## Working tree / lab notes

- Large uncommitted Phase 5 + Phase 6 surface may still be present; inspect `git status`
  before editing. Do not invent a clean tree.
- Disposable lab prefix `recovery-s0-core.*` under ignored `.cache/` may still be running.
- Project owner owns all Git remotes; agents still do not push. After owner push, inspect
  the actual forge run (recorded Phase 6 residual).

## Immediate resume order

1. Owner push of Phase 6 close batch; inspect forge CI on the published push.
2. Optional: live browser re-walk of backup-now / restore-as-new settlement after BUG-0649/0650
   overlay redeploy.
3. Later phases (migration / HA / CBT) per platform plan — do not reopen Phase 6 disposable
   close without an owner decision.

## Pointers

| Doc | Role |
|---|---|
| [Recovery plan](2026-09-25-recovery-snapshots-backups-restores.md) | Slices, failure matrix, Phase 5/6 checklists |
| [Phase 6 close record](../qa/2026-09-27-phase6-gate/CLOSED.md) | Gate close evidence + accepted residuals |
| [Phase 6 implementation index](../qa/2026-09-27-phase6-implementation-index.md) | S4–S7 change records, guides, APIs |
| [Phase 5 close record](../qa/2026-09-26-phase5-gate/CLOSED.md) | Phase 5 gate close evidence |
| [Platform plan §5–§6](2026-09-08-nex-platform.md#5-snapshots-and-rollback) | Parent gates |
| [Support matrix](../qa/2026-09-26-phase5-snapshot-support-matrix.md) | Supported / refused / untested |
| [Website roadmap](../../website/src/routes/roadmap/+page.svelte) | Public milestone — keep in sync with proven status |
