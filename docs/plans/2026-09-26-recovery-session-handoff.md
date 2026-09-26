# Recovery implementation handoff

Date: 2026-09-27. **Phase 5 closed for disposable virtual-lab scope (2026-09-27).**
Close record: [docs/qa/2026-09-26-phase5-gate/CLOSED.md](../qa/2026-09-26-phase5-gate/CLOSED.md).
**Phase 6 / S4–S8 not started.** Resume the
[recovery plan](2026-09-25-recovery-snapshots-backups-restores.md).
Agents never commit, push, tag or release; project owner owns Git remotes.

## Delivery status

| Slice | Actual status | Remaining boundary |
|---|---|---|
| S0 native feasibility | Accepted for recorded disposable-lab scope | Not product Controller/Core workflows |
| S1 chain/authority | Accepted for recorded lab matrix (r8/r9 evidence) | Broader physical/multi-host residuals elsewhere |
| S2 stopped lifecycle | Product paths + browser evidence recorded | Accepted under Phase 5 close |
| S3 running create | Crash/quiesced BIOS lab + UEFI refusal + freeze/thaw | Accepted under Phase 5 close |
| Phase 5 gate | **Closed** (2026-09-27, disposable virtual-lab scope) | [Close record](../qa/2026-09-26-phase5-gate/CLOSED.md); accepted residuals in support matrix |
| S4–S8 backup/restore | **Not started** | May begin; Phase 5 no longer blocks |

## What is already proven (do not re-litigate)

- Stopped create / list / rename / revert / leaf-delete through Core + Controller UI.
- Running BIOS crash and quiesced create; UEFI running create refused before effects.
- Bulk VMs-list stopped Snapshot; Snapshots confirm-dialog / focus / a11y follow-up **PASS**.
- Controller replacement / relay / exact-retry recovery (including BUG-0647).
- Support matrix:
  [docs/qa/2026-09-26-phase5-snapshot-support-matrix.md](../qa/2026-09-26-phase5-snapshot-support-matrix.md).
- Browser QA:
  [docs/qa/2026-09-26-recovery-snapshot-browser.md](../qa/2026-09-26-recovery-snapshot-browser.md).
- Native multi-backend S0 lifecycle + midcommit consolidation (local / NFS / SMB / iSCSI)
  on libvirt `11.3.0-3+deb13u3+nex4`:
  [docs/qa/2026-09-26-recovery-s0-multi-backend-dr.md](../qa/2026-09-26-recovery-s0-multi-backend-dr.md).
  That is **native** DR evidence, not Phase 6 product backup/restore.

## Explicitly not done

- Phase 6 product backup repositories, jobs, schedules, restore-as-new/replace, catalog.
- Product Controller/Core snapshot paths on every storage backend (S0 native ≠ S2/S3 product).
- Broader physical / production storage and multi-host replacement beyond recorded nested lab campaigns.

## Working tree / lab notes for the next agent

- Large uncommitted Phase 5 snapshot surface may still be present; inspect `git status`
  before editing. Do not invent a clean tree.
- Disposable lab prefix `recovery-s0-core.*` under ignored `.cache/` may still be running
  from the 2026-09-26 multi-backend campaign; confirm with QEMU/SSH before creating another.
  Patched libvirt binaries may exist under ignored
  `.cache/recovery-libvirt-build/output/` (`+nex4`); builder containers are disposable.
- Project owner owns all Git remotes; agents still do not push.

## Immediate resume order

1. Start S4 when ready (Core NFS repository registration, stopped full backup,
   restore-as-new, guide/UI).
2. Do not expand into S5–S8 prematurely; keep Phase 6 slices gated on their own prerequisites.
3. Keep accepted Phase 5 residuals honest in the support matrix; do not re-open closed
   Phase 5 checklist rows without new evidence that reverses them.

## Pointers

| Doc | Role |
|---|---|
| [Recovery plan](2026-09-25-recovery-snapshots-backups-restores.md) | Slices, failure matrix, Phase 5/6 checklists |
| [Phase 5 close record](../qa/2026-09-26-phase5-gate/CLOSED.md) | Gate close evidence |
| [Platform plan §5–§6](2026-09-08-nex-platform.md#5-snapshots-and-rollback) | Parent gates |
| [Support matrix](../qa/2026-09-26-phase5-snapshot-support-matrix.md) | Supported / refused / untested |
| [Website roadmap](../../website/src/routes/roadmap/+page.svelte) | Public milestone — keep in sync with close |
