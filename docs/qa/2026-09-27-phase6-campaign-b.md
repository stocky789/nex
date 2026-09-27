# Phase 6 Campaign B (bounded nested lab)

Date: 2026-09-27. Scope: disposable nested recovery Core lab (fixture
`cluster-ha-v1`, ignored `.cache/recovery-s0-core.*` run directory). Authorized
disposable lab only.

**Verdict: pass (bounded nested re-run after BUG-0651)** for backup continuity,
restore-as-new while the former master is down, and fresh-Controller adoption on
the survivor. The **original live nested run earlier the same day failed** backup
takeover (kept below). Core fix BUG-0651 plus overlay redeploy
`4f569d0e0ac3fb0e383dc3846c3d38122cd090c04eb4437396b96e99aa4095d9` closed that
gap on a second fence-verified promote. Full three-host / physical-domain
Campaign B remains **not_run** (accepted residual under disposable Phase 6 close).

Prerequisite: [Campaign A bounded subset](2026-09-27-phase6-campaign-a.md) on the
same lab; promotion patterns from
[r8 promotion QA](2026-09-26-recovery-snapshot-promotion-r8.md).

## Topology (exact)

| Guest alias | Role in this run |
|---|---|
| core1 | Initial pool master with Campaign A backup state; QMP power-off + product virsh lab fence; left `replica_ack_excluded` |
| core2 | Enrolled, joined core1's pool, then fence-verified promoted to master (membership generation 3) |
| core3 | Overlay installed; **not joined** |
| storage | NFS destination still holding published recovery-point payloads |

| Artifact | Identity |
|---|---|
| Package / overlay | `nex-core-local` `0.4.12` + Phase 6 overlay |
| Overlay binary SHA-256 | `49bffb9dd583eaf1a04013ab46196f88359b07fc8e76d06aa318b96f1d1f2e5c` |
| Pool | same Campaign A pool (core1 hostname-named) |
| Controller | ephemeral host-loopback Controller B from Campaign A (not an on-guest Controller appliance) |
| Fence | lab-only `virsh` profile via disposable outer-host SSH stub that QMP-powers the core1 guest |

Nested limits: one outer virtualization host; Controller was not literally colocated
inside core1. Master loss was simulated by powering off core1 while using the
existing outer Controller only to deliver the promote command to the survivor
(r8-style), not to keep pool backup state.

## Pass / fail matrix

| Step | Result |
|---|---|
| Enroll core2 + Controller pool join to core1's pool | **pass** (join stage `ready` / `succeeded`) |
| Record virsh lab fence profile for core1 while master alive | **pass** |
| Power-off core1 (QMP) | **pass** (guest SSH port refused afterward) |
| Promotion preflight `ready_to_fence=true` naming core1 | **pass** |
| Confirmed promote of core2; Core fence proof `exclusion_verified=true` | **pass** (membership generation 3; core2 `role=master`) |
| Backup policies / catalog / runs present on new master with Campaign A IDs | **fail** on first live nested overlay; **pass** on BUG-0651 re-run (see below) |
| NFS repository payload still present for prior points | **pass** |
| Repository test / catalog rebuild / backup ops on survivor | **fail** on first live nested overlay (`503`); **pass** on re-run |
| Restore from NFS while core1 down | **not_run** on first attempt; **pass** on re-run |
| Fresh Controller on survivor adopts backup jobs/history | **not_run** on first attempt; **pass** on re-run (Controller C) |
| Full three-host / physical-domain Campaign B | **not_run** |

## Observed sequence

1. core2 was enrolled to Controller B (own solo pool), then joined core1's pool
   through `POST /api/v1/pools/{id}/members` with peer origins on the private lab
   network. Join reached stage `ready`.
2. While core1 was still master, a lab `virsh` fence profile was recorded for
   core1 (disposable outer SSH endpoint + QMP-backed domain UUID stub).
3. core1 was powered off via QMP. Preflight then reported `ready_to_fence=true`
   for core2 (with `can_promote=false` until the confirmed fence runs — expected).
4. Confirmed promote named exactly core1 in `fence_host_ids`. After fixing the
   lab fence TTY/`fence_virsh` EXPECT prompt path, Core completed fence with
   `exclusion_verified=true` and advanced the transfer to `stage=done` /
   `status=succeeded`. core2 became pool master at membership generation 3;
   core1 remained powered off and excluded from replica acknowledgements.
5. On the new master, `GET /api/local/v1/pool/backup-{repositories,policies,catalog,runs}`
   returned empty policies/catalog/runs. The authority replica still carried
   `backup_authority` summaries for Campaign A policy and run IDs. NFS still
   listed the published point directories. Re-registering repository
   `repo-0d58508be006` succeeded, but repository test and catalog rebuild
   returned `503 backup operation unavailable`, so restore and fresh-Controller
   backup adoption were not attempted.

Private runner receipts remain under ignored `.cache/phase6-s4-walkthrough/`
(`campaign-b-result.json`, `campaign-b-promote.json`, `campaign-b-preflight.json`).

## Core fix (same day; not a live nested re-run)

Root cause: promote adopted VM ownership and resource mutations from the signed
replica but not backup stores; `backup_authority` only held compact refs.

After BUG-0651, Core replicates repositories, full policy definitions, retained
runs and catalog index in `backup_authority`, materializes them during
`adoptPoolAuthorityFromReplica`, and auto-imports NFS manifests when the catalog
index is empty but a repository mount is usable. Focused checks:

- `go test ./internal/agent/ -run 'PoolPromotionRetainsBackupAuthority|PoolPromotionRebuildsCatalogFromNFSWhenIndexEmpty|AdoptPoolBackupAuthorityAndRebuildFromMount'`

Live nested Campaign B backup-continue / restore / fresh-Controller adoption were
re-proven the same day; see [Re-run after BUG-0651](#re-run-after-bug-0651-same-lab-same-day).

## Limits (exact)

- **Topology:** two-host nested disposable lab (core3 unused). Not three physical
  hosts or separate failure domains.
- **Controller placement:** outer-host ephemeral process, not a Controller VM on
  the lost master. Campaign B “master + Controller disappear together” is only
  partially simulated.
- **Fence:** lab `virsh` + QMP stub, not hardware BMC / independent fencing fabric.
- **Backup takeover:** original live promote succeeded without materializing durable
  backup stores on the survivor. Same-day BUG-0651 re-run proved materialisation,
  NFS restore-as-new, and fresh-Controller adoption on the survivor.
- **Browser UX:** exercised in a separate slice; see
  [browser QA](2026-09-27-phase6-backup-browser.md).
- Phase 6 disposable close: [CLOSED.md](2026-09-27-phase6-gate/CLOSED.md).

## API / guide / state

- API impact: **changed contract** for the additive `backup_authority` replica
  fields used on promote (see BUG-0651 change record). Live nested exercise used
  existing join/fence/promote and Core-local backup routes.
- Guide impact: **none** (contributor QA evidence only; operator steps unchanged).
- State owner: **core** for pool membership/authority after promote; backup durable
  stores are Core-owned and now transfer with the signed replica on promote.

## Re-run after BUG-0651 (same lab, same day)

Date: 2026-09-27 (afternoon). Same disposable nested lab
(`.cache/recovery-s0-core.*`). No Git commit.

### Setup

1. Powered core1 back on (QEMU restart; prior Campaign B left it off).
2. Rebuilt worktree `nex-core-local` and redeployed overlay to core1/core2/core3.
   Overlay SHA-256:
   `4f569d0e0ac3fb0e383dc3846c3d38122cd090c04eb4437396b96e99aa4095d9`.
3. Focused Go promote fixtures re-checked **pass**.
4. Topology after the first failed backup takeover: core2 was already pool master
   (membership generation 3) with empty policies/catalog/runs. Planned master
   change returned authority to core1 (generation 4). On core1 (fixed binary):
   re-registered NFS repo with mount path, catalog rebuild imported Campaign A
   point IDs, recreated policy `pol-7e40601902b5` / schedule `sch-e7af445b173b`,
   and retained a policy run record. core2's verified replica then carried full
   `backup_authority` (repositories, definitions/policies, catalog, runs/history).

### Promote / continue / restore / adoption

| Step | Result |
|---|---|
| QMP power-off core1; preflight `ready_to_fence=true` | **pass** |
| Confirmed promote of core2 (`exclusion_verified` path); generation 5 | **pass** (`stage=done` / `status=succeeded`) |
| Survivor policies / catalog / runs with Campaign A IDs | **pass** (`pol-7e40601902b5`; catalog includes `prun-724d407e0086`, `bkup-7ce8e10d3e42`, …; runs present) |
| NFS repository test on survivor | **pass** (`access.status=ok`) |
| Restore-as-new from NFS while core1 down | **pass** (`rstn-campb-8356df06` → VM `shut off` on survivor) |
| Fresh Controller (Controller C) adopts same repo/policy/schedule/catalog/run IDs | **pass** |
| Full three-host / physical-domain Campaign B | **not_run** |

Private runner receipts under ignored `.cache/phase6-s4-walkthrough/`:
`campaign-b-rerun-result.json`, `campaign-b-rerun-promote.json`,
`campaign-b-rerun-replica-ba.json`, `campaign-b-rerun-verify.json`,
`campaign-b-rerun-adoption.json`.

### Limits (re-run)

Same nested / lab-fence limits as the first attempt. Path used a planned
master-change + NFS catalog re-seed on core1 before the second promote, because
the first promote had already emptied survivor backup stores under the pre-fix
overlay. The second promote itself is the BUG-0651 proof: survivor materialised
policies/catalog/runs from the signed replica without Controller-owned backup
state.
