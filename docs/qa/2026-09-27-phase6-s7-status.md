# Phase 6 / S7 status — disaster recovery product surfaces

Date: 2026-09-27. Scope: product paths for restore replace, protect/pins, test restore,
catalog rebuild/import, Core-local recovery and Controller UI.

## Proven (unit / structural)

| Path | Evidence |
|---|---|
| Restore replace preflight refuses running / missing confirm / in-place overwrite; cutover-ready is needs_attention not succeeded | `internal/protocol/backup_dr_test.go`, `internal/agent/backup_dr_test.go` |
| Protect blocks retention prune; pin blocks prune | `TestProtectBlocksPruneAndSurvivesRebuild` |
| Catalog rebuild/import from validated manifests; incomplete skipped | `TestCatalogRebuildImportsValidatedManifestsOnly` |
| Protect marker + restore pin survive catalog rebuild | same protect/rebuild test |
| ManagedState `backup_*_reported` inventory already present for adoption | existing S4–S6 inventory path; no gap found for repos/catalog/runs/policies |
| Docs gate / OpenAPI HTML generation | `check_docs.py --worktree`, `generate_api.py --check` (run with this change) |

## Unexecuted (do not claim pass)

| Campaign / walkthrough | Status |
|---|---|
| Three-host Campaign A (Controller disappears; Core-local restore; fresh Controller adoption) | **Unexecuted** — no disposable three-host lab run in this change |
| Three-host Campaign B (master+Controller host loss; promotion; restore; adoption) | **Unexecuted** |
| Browser walkthrough of Replace / Protect / Test restore / Import points | **Unexecuted** (UI shipped; not browser-verified here) |
| Bound single-host product API walkthrough under `.cache/recovery-s0-core*` | **Attempted 2026-09-27** — lab up; NFS/versions OK; product APIs blocked (no `nex-core-local`). See [S4 lab walkthrough](2026-09-27-phase6-s4-lab-walkthrough.md) |

## Open for S8 / campaigns

- Close Phase 6 only after Campaign A/B evidence and S8 UX/package review.
- Exclusive replace cutover after `cutover-ready` / `needs_attention` staging still needs
  disposable-lab proof; Core no longer reports that stage as succeeded.
- Controller-absent schedule continuation across outage is covered by S6 unit ticks; full Campaign A schedule span remains lab-gated.


## Restore completion fix (2026-09-27)

Restore-as-new now registers a stopped VM (captured_spec + disk placement) before settling succeeded; restore-replace stays `needs_attention` at cutover-ready until exclusive cutover exists. Unit-proven; live NFS lab still open.
