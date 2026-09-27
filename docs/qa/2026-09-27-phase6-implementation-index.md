# Phase 6 implementation index (S4–S7 + S8 close)

Date: 2026-09-27. **Phase 6 gate: CLOSED for disposable nested-lab scope.**
Close record: [2026-09-27-phase6-gate/CLOSED.md](2026-09-27-phase6-gate/CLOSED.md).

This index records worktree implementation, unit/contract evidence for S4–S7, S8
closeout, disposable-lab product backup, bounded nested Campaign A/B (Campaign B
re-run PASS after BUG-0651), browser walkthrough (settlement bugs fixed in code),
hermetic CI and owner-authorized disposable close. It does **not** claim physical
or production certification.

Parent plans: [recovery plan](../plans/2026-09-25-recovery-snapshots-backups-restores.md),
[session handoff](../plans/2026-09-26-recovery-session-handoff.md),
[platform §6](../plans/2026-09-08-nex-platform.md#6-backups-scheduling-and-restoration).

## Verdict

| Claim | Status |
|---|---|
| S4–S7 product code (Core / Controller / UI / guides) | Present in worktree |
| Unit / contract tests for implemented slices | Passing (recorded in slice change records) |
| Disposable-lab product NFS backup / restore | **Pass** — [product backup lab](2026-09-27-phase6-product-backup-lab.md) |
| Lab install (`0.4.12` + overlay) | **Pass** — [lab install](2026-09-27-phase6-lab-install.md) |
| Bounded Campaign A (single-master nested lab subset) | **Pass** — [Campaign A](2026-09-27-phase6-campaign-a.md); not full three-host / physical failure domains |
| Bounded Campaign B (nested two-host join + promote) | **Pass (re-run)** — [Campaign B](2026-09-27-phase6-campaign-b.md); BUG-0651 re-run |
| Browser UX walkthrough | **Executed** — [browser QA](2026-09-27-phase6-backup-browser.md); settlement fails recorded then fixed in code (BUG-0649/0650); live re-walk after fix accepted residual |
| Hermetic CI backup regression | **Present** — `internal/agent/backup_ci_test.go` |
| Three-host physical-domain Campaign A / Campaign B | **Not executed** (accepted residual) |
| Phase 6 acceptance (disposable virtual-lab scope) | **CLOSED** — [close record](2026-09-27-phase6-gate/CLOSED.md) |
| Physical / production certification | **Not claimed** |

## Slice change records

| Slice | Change record | Summary |
|---|---|---|
| S4 Core | [2026-09-27-phase6-s4-core-backup.json](../changes/2026-09-27-phase6-s4-core-backup.json) | NFS repositories, stopped full backup, restore-as-new |
| S4 Controller | [2026-09-27-phase6-s4-controller-backup.json](../changes/2026-09-27-phase6-s4-controller-backup.json) | Controller relay for repos, backup-now, catalog/runs, restore-as-new |
| S4 UI / guide | [2026-09-27-phase6-s4-backup-ui-guide.json](../changes/2026-09-27-phase6-s4-backup-ui-guide.json) | Backups rail UI and operator guides |
| S4 lab probe | [2026-09-27-phase6-s4-lab-walkthrough.json](../changes/2026-09-27-phase6-s4-lab-walkthrough.json) | Early probe; superseded by lab install + product lab |
| S5 | [2026-09-27-phase6-s5-online-backup.json](../changes/2026-09-27-phase6-s5-online-backup.json) | Online crash-consistent full backup (libvirt push-mode) |
| S6 | [2026-09-27-phase6-s6-backup-policies.json](../changes/2026-09-27-phase6-s6-backup-policies.json) | Policies, Core scheduler, retention, Controller/UI |
| S7 | [2026-09-27-phase6-s7-backup-dr.json](../changes/2026-09-27-phase6-s7-backup-dr.json) | Replace, protect/pins, test restore, catalog rebuild, Core-local |
| S7 status QA | [2026-09-27-phase6-s7-status.md](2026-09-27-phase6-s7-status.md) | Earlier status; superseded by campaigns + close record |
| Restore completion | [2026-09-27-phase6-restore-completion-fix.json](../changes/2026-09-27-phase6-restore-completion-fix.json) | Restore-as-new registration; honest replace cutover-ready |
| Replace cutover | [2026-09-27-phase6-replace-cutover.json](../changes/2026-09-27-phase6-replace-cutover.json) | Exclusive restore-replace cutover |
| Related fix | [2026-09-27-backup-authority-peer-lock.json](../changes/2026-09-27-backup-authority-peer-lock.json) | BUG-0648 poolPeerMu re-entry during snapshot reservation |
| Settlement / Overview | [2026-09-27-phase6-backup-settlement-fix.json](../changes/2026-09-27-phase6-backup-settlement-fix.json) | BUG-0649/0650; Overview catalog tile |
| Hermetic CI | [2026-09-27-phase6-backup-ci-coverage.json](../changes/2026-09-27-phase6-backup-ci-coverage.json) | `TestPhase6BackupHermeticCIRegression` |
| S8 status | [2026-09-27-phase6-s8-closeout-status.json](../changes/2026-09-27-phase6-s8-closeout-status.json) | Pre-close documentation batch |
| Lab install | [2026-09-27-phase6-lab-install.json](../changes/2026-09-27-phase6-lab-install.json) | `0.4.12` + overlay on core1/2/3 |
| Product NFS lab | [2026-09-27-phase6-product-backup-lab.json](../changes/2026-09-27-phase6-product-backup-lab.json) | Stopped backup → disks aside → restore-as-new |
| Campaign A (bounded) | [2026-09-27-phase6-campaign-a.json](../changes/2026-09-27-phase6-campaign-a.json) | Controller-absent run-now/restore + Controller B adoption |
| Campaign B (bounded) | [2026-09-27-phase6-campaign-b.json](../changes/2026-09-27-phase6-campaign-b.json) | First attempt: join + fence-promote passed; backup takeover failed |
| Campaign B re-run | [2026-09-27-phase6-campaign-b-rerun.json](../changes/2026-09-27-phase6-campaign-b-rerun.json) | After BUG-0651: survivor policies/catalog/runs, restore-as-new, adoption |
| Browser | [2026-09-27-phase6-backup-browser.json](../changes/2026-09-27-phase6-backup-browser.json) | Real Backups rail walkthrough |
| Gate closed | [2026-09-27-phase6-gate-closed.json](../changes/2026-09-27-phase6-gate-closed.json) | Disposable virtual-lab close |

## Operator guides

| Guide | Slice |
|---|---|
| [backup-repositories.md](../guide/backup-repositories.md) | S4 |
| [backup-now.md](../guide/backup-now.md) | S4 / S5 |
| [restore-as-new.md](../guide/restore-as-new.md) | S4 |
| [backup-policies.md](../guide/backup-policies.md) | S6 |
| [restore-replace.md](../guide/restore-replace.md) | S7 |
| [catalog-rebuild.md](../guide/catalog-rebuild.md) | S7 |
| [recover-without-controller.md](../guide/recover-without-controller.md) | S7 |

Guide Status sections name Campaign A/B and lab limits where applicable; do not
treat guide presence as walkthrough verification.

## API reference

Implemented contracts are documented under [docs/API.md](../API.md):

- Backup development contract (Phase 6 / S4–S5) — repositories, backup-now (stopped/crash),
  catalog/runs, restore-as-new, plus S7 DR routes on the same tables
- Backup policies and schedules (Phase 6 / S6)

Machine-readable: `docs/api/openapi.json`, `docs/api/local-openapi.json` (and generated HTML).

Durable Core state (adoption via master inventory `backup_*_reported` flags):
`pool-backup-repositories.json`, `pool-backup-catalog/`, `pool-backup-runs/`,
`pool-backup-policies.json`, `pool-backup-pins.json`, repository `.nex-protected` markers.

## Known limits (accepted residuals under disposable close)

See [close record](2026-09-27-phase6-gate/CLOSED.md). Highlights:

- Nested disposable lab ≠ physical failure domains.
- Quiesced online backup refused; UEFI online backup refused.
- Browser settlement fixed in code; live re-walk after fix may remain outstanding.
- Full three-host simultaneous join + schedule wall-clock span not fully proven.
- Package `0.4.12` + binary overlay (not ISO install acceptance).
- Forge CI on published push not yet inspected (agents do not push).
- Incremental/CBT, memory snapshots, storage-native snapshots and automated boot drills
  remain later roadmap work outside Phase 6 exit.
