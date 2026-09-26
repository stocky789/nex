# Recovery implementation handoff

Date: 2026-09-26 (evening checkpoint). **Phase 5 incomplete / gate open.
Phase 6 / S4 not started.** Resume the
[recovery plan](2026-09-25-recovery-snapshots-backups-restores.md).
Agents never commit, push, tag or release; project owner owns Git remotes.

## Delivery status

| Slice | Actual status | Remaining boundary |
|---|---|---|
| S0 native feasibility | Accepted for recorded disposable-lab scope | Not product Controller/Core workflows |
| S1 chain/authority | Accepted for recorded lab matrix (r8/r9 evidence) | Broader physical/multi-host residuals elsewhere |
| S2 stopped lifecycle | Product paths + browser evidence recorded | Phase 5 checklist close still open |
| S3 running create | Crash/quiesced BIOS lab + UEFI refusal + freeze/thaw | Same Phase 5 gate |
| Phase 5 gate | **Open** | Forge CI on published push; independent review; checklist acceptance |
| S4–S8 backup/restore | **Not started** | Blocked until Phase 5 gate closes |

Do **not** start S4 (Core backup repositories / stopped full backup / restore UI)
until Phase 5 is formally closed in the plans and published roadmap.

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
- Forge CI results on a published push (local suite may be green; forge is the gate).
- Independent phase-acceptance review and roadmap flip to Phase 5 closed.

## Working tree / lab notes for the next agent

- Large uncommitted Phase 5 snapshot surface may still be present; inspect `git status`
  before editing. Do not invent a clean tree.
- Disposable lab prefix `recovery-s0-core.*` under ignored `.cache/` may still be running
  from the 2026-09-26 multi-backend campaign; confirm with QEMU/SSH before creating another.
  Patched libvirt binaries may exist under ignored
  `.cache/recovery-libvirt-build/output/` (`+nex4`); builder containers are disposable.
- Owner intends push that may also trigger the automatic release workflow; that is acceptable
  forge evidence for packaging as well as required CI jobs. Agents still do not push.

## Immediate resume order

1. Project owner commits desired tree and pushes so forge CI (and any auto-release) runs.
2. Next agent: inspect the **actual** forge run — record pass/fail/skip honestly; never claim
   CI from the workflow file alone.
3. If forge green: independent review of Phase 5 checklist + state ownership; then update
   recovery plan checklist, this handoff, platform plan §5, and the published website roadmap
   to **Phase 5 closed** only with owner acceptance.
4. **Only after** that close: start S4 (Core NFS repository registration, stopped full backup,
   restore-as-new, guide/UI). Do not expand into S5–S8 prematurely.

## Pointers

| Doc | Role |
|---|---|
| [Recovery plan](2026-09-25-recovery-snapshots-backups-restores.md) | Slices, failure matrix, Phase 5/6 checklists |
| [Platform plan §5–§6](2026-09-08-nex-platform.md#5-snapshots-and-rollback) | Parent gates |
| [Support matrix](../qa/2026-09-26-phase5-snapshot-support-matrix.md) | Supported / refused / untested |
| [Website roadmap](../../website/src/routes/roadmap/+page.svelte) | Public “testing” milestone — keep in sync on close |
