# Phase 6 Campaign A (bounded): Controller disappears; Core remains

Date: 2026-09-27. Scope: disposable nested recovery Core lab (fixture
`cluster-ha-v1`, ignored `.cache/recovery-s0-core.*` run directory). Authorized
disposable lab only.

**Verdict: pass** for this **bounded single-master nested-lab Campaign A
subset**. This is **not** the full three-host physical failure-domain Campaign A
from the recovery plan. **Campaign B was not run and is not claimed.**

Prerequisite product path:
[2026-09-27-phase6-product-backup-lab.md](2026-09-27-phase6-product-backup-lab.md).

## Environment

| Guest alias | Role in this run |
|---|---|
| core1 | Pool master; only enrolled host |
| core2 | Overlay binary present; **not joined** |
| core3 | Overlay binary present; **not joined** |
| storage | NFS `repo-nfs` destination |

| Artifact | Identity |
|---|---|
| Package | `nex-core-local` `0.4.12` + Phase 6 overlay |
| Overlay binary SHA-256 | `49bffb9dd583eaf1a04013ab46196f88359b07fc8e76d06aa318b96f1d1f2e5c` |
| Ephemeral Controller A | loopback HTTPS; state moved aside after stop |
| Ephemeral Controller B | fresh empty data dir + new CA on a separate loopback port |

## Pass / fail matrix

| Step | Result |
|---|---|
| Reviewed backup policy covering ≥1 VM + recorded IDs | **pass** (`pol-7e40601902b5` / `sch-e7af445b173b`, VM `phase6-restore-new`) |
| Baseline policy Run now completed | **pass** (`prun-a8be6982ba8c` published) |
| Optional second run started before Controller loss | **pass** (`prun-766da8e6c9c9`; reconciled without Controller) |
| Stop/remove Controller A; Core continues | **pass** (A DB moved aside; Core host/pool IDs unchanged) |
| Controller-absent policy Run now; same job identity | **pass** (`prun-724d407e0086`; policy/schedule IDs unchanged) |
| Controller-absent restore-as-new with source disks unavailable | **pass** (`rstn-d599a64ade1b` → new stopped VM; source tree moved aside) |
| Multi-host member join | **limit recorded** — single-master only; core2/core3 not joined |
| Fresh Controller B adopts repos/policies/runs/catalog without recreating jobs | **pass** (same IDs; `jobs_recreated=false`) |
| Full three-host Campaign A | **not_run** |
| Campaign B | **not_run** |

## Observed sequence

1. Reused enrolled pool master on core1 with existing NFS repository
   `repo-0d58508be006` and prior recovery point `bkup-7ce8e10d3e42`.
2. Core-local upsert of enabled policy `phase6-campa-policy` covering one stopped
   VM (local managed storage under the host images tree; backup destination NFS).
3. Policy Run now baseline published recovery point `prun-a8be6982ba8c`. A second
   Run now was accepted before Controller A was stopped.
4. Ephemeral Controller A was stopped; its data directory was renamed so the
   replacement could not import it. Core kept answering local APIs with the same
   host and pool IDs. The in-flight optional run finished without Controller.
5. With Controller absent, policy Run now published `prun-724d407e0086` under the
   same `policy_id` / `schedule_id`. Source disks for the policy VM were moved
   aside, then Core-local restore-as-new registered a new stopped guest from that
   outage point.
6. Fresh Controller B (empty state, new CA) was started on loopback. Only the pool
   master re-enrolled through the reverse tunnel. Controller B inventory APIs then
   listed the same repository, policy, schedule, catalog points and run IDs —
   including the Controller-absent run and restore — without recreating the job.

Private runner output retained under ignored `.cache/phase6-s4-walkthrough/`
(`campaign-a-result.json`).

## Limits (exact)

- **Topology:** single-master nested disposable lab. Not three physical hosts,
  not separate failure domains, not member-join continuity, not Campaign B
  (master+Controller loss).
- **Schedule span:** exercised policy **Run now** while Controller was absent,
  not two wall-clock schedule occurrences across a long outage.
- **Workload:** one BIOS stopped guest on core1 local storage; NFS used only as
  the backup repository. No multi-disk / multi-host VM set.
- **Restore verification:** restore registered and `virsh` showed shut off;
  marker/application boot verification was not required for this subset pass.
- **Controller B:** host-loopback ephemeral process with SSH reverse tunnel into
  the guest — not an on-appliance retire/redeploy of a Controller VM on core1.
- **Browser UX:** not exercised; adoption checked via Controller HTTPS APIs.
- Phase 6 gate remains **OPEN** until full Campaign A/B and remaining lab items
  close.

## API / guide / state

- API impact: **none** (lab exercise of existing Core-local and Controller adoption contracts).
- Guide impact: **none** (contributor QA evidence only; operator guides unchanged).
- State owner: **core** for pool backup repositories, policies, catalog, runs, and
  restored VM definitions on the pool master; Controller B adopted via master
  inventory without the prior Controller database.
