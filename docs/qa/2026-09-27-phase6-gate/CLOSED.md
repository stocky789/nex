# Phase 6 gate closed

Date: 2026-09-27. Decision: project owner — close Phase 6 now for disposable
virtual-lab scope (“close everything off including any tests etc required”).

## Verdict

**Phase 6 (backups, scheduling and restoration) is closed for disposable nested-lab scope.**

S4–S7 product surfaces, hermetic CI coverage, disposable-lab product NFS
backup/restore, bounded Campaign A/B nested runs, replace cutover implementation
and a real browser walkthrough (with settlement bugs fixed in code) are evidenced
within that boundary. Owner authorized close 2026-09-27. This is **not**
physical or production certification.

## Evidence that closes the gate

| Requirement | Evidence |
|---|---|
| S4–S7 Core / Controller / UI / guides + unit/contract | [Implementation index](../2026-09-27-phase6-implementation-index.md); `docs/changes/2026-09-27-phase6-s4-*.json` through `…-s7-*.json` and related 2026-09-27-phase6-* records |
| Lab install (0.4.12 + Phase 6 binary overlay) | [Lab install](../2026-09-27-phase6-lab-install.md) |
| Product NFS stopped backup → disks aside → restore-as-new | [Product backup lab](../2026-09-27-phase6-product-backup-lab.md) |
| Bounded Campaign A (Controller absent; Core-local restore; fresh Controller adoption) | [Campaign A](../2026-09-27-phase6-campaign-a.md) — single-master nested subset |
| Bounded Campaign B (join + fence-promote; backup continuity after BUG-0651) | [Campaign B](../2026-09-27-phase6-campaign-b.md); [re-run change record](../../changes/2026-09-27-phase6-campaign-b-rerun.json) |
| Restore-replace exclusive cutover | [Replace cutover change record](../../changes/2026-09-27-phase6-replace-cutover.json); unit/contract evidence |
| Browser Backups rail (real Controller↔Core) | [Browser QA](../2026-09-27-phase6-backup-browser.md); settlement fixed in code BUG-0649 / BUG-0650 ([settlement change record](../../changes/2026-09-27-phase6-backup-settlement-fix.json)) |
| Overview Backups tile from catalog | Same settlement change record; `web/src/overview.tsx` |
| Hermetic CI backup regression | `internal/agent/backup_ci_test.go` (`TestPhase6BackupHermeticCIRegression`); [CI coverage change record](../../changes/2026-09-27-phase6-backup-ci-coverage.json) |
| Quiesced / UEFI online refused (honest product limit) | Guides (`docs/guide/backup-now.md`); S5 change record; product lab skipped online crash guest |
| Operator guides + API reference | Guides listed in the implementation index; [docs/API.md](../../API.md) backup contracts |

## Accepted residuals (not Phase 6 blockers for disposable close)

- Nested disposable lab ≠ physical failure domains; hardware fencing / BMC and separate
  production storage domains are untested
- Quiesced online backup remains refused until proven; UEFI online backup remains refused
- Browser settlement bugs (BUG-0649 / BUG-0650) are fixed in code; live browser re-walk
  after fix on the disposable lab may still be outstanding
- Full three-host simultaneous join + schedule wall-clock span is not fully proven
  (Campaign A/B were bounded nested subsets; Campaign A used policy Run now, not a
  multi-occurrence outage span)
- Lab packages were `nex-core-local` `0.4.12` plus a Phase 6 binary overlay — not ISO
  install acceptance
- Forge CI on a published push is not yet inspected (agents do not push); project owner
  push + forge run inspection remains a residual, same class as needing an owner push
- Incremental/CBT, memory snapshots, storage-native snapshots and automated boot drills
  remain later roadmap work outside Phase 6 exit
- Independent formal usability / full responsive-theme matrix not executed as a separate gate
- Measured backup/restore durations for published RPO/RTO claims remain unpublished

## Process note

Owner authorized disposable virtual-lab close 2026-09-27. Agents do not commit or push.
Physical/production certification is **not** claimed.

## Subsequent review

This close decision records the disposable-lab evidence available when it was made.
The [post-close staged-code review](../2026-09-27-phase6-post-close-review.md)
identified additional unresolved data-safety gaps. The current worktree needs fixes,
independent review and focused reruns before the corrected candidate can claim this
acceptance gate again. The earlier lab reports remain historical observations of
their tested binary overlay, not proof of the corrected candidate.
Current Phase 6 acceptance is **open** for that changed candidate. In addition to
restore cutover uncertainty and bounded NFS failure handling, the signed backup
authority replica has a finite history/catalog bound; promotion with a larger
retained inventory must refuse safely and needs a complete-history recovery proof.
No new native or browser walkthrough is added by this review note.
