# Phase 5 snapshot support matrix (disposable lab)

Date: 2026-09-26; status updated 2026-09-27. Consolidated record of **supported**,
**refused**, and **untested** profiles for Phase 5 product snapshot work.
Phase 5 closed for disposable virtual-lab scope (2026-09-27);
[close record](2026-09-26-phase5-gate/CLOSED.md).
Private lab captures stay under ignored `.cache`; linked QA pages hold the detail.

## Product Controller/Core paths (S2/S3)

| Profile | Operation | Result | Evidence |
|---|---|---|---|
| Stopped, local qcow2, multi-disk, BIOS/UEFI | create / revert / leaf-delete / rename | supported (lab) | recovery plan S2 rows; [browser QA](2026-09-26-recovery-snapshot-browser.md) |
| Stopped, matching-disk never-started (`defined`) | revert / leaf-delete | supported (lab) | [browser QA](2026-09-26-recovery-snapshot-browser.md) |
| Running BIOS, crash-consistent | create | supported (lab) | [running-mode QA](2026-09-26-recovery-snapshot-running-mode.md) |
| Running BIOS, quiesced (agent connected) | create | supported (lab) | [running-mode QA](2026-09-26-recovery-snapshot-running-mode.md) |
| Running BIOS, quiesced (no agent) | create | refused honestly | [running-mode QA](2026-09-26-recovery-snapshot-running-mode.md) |
| Running UEFI | create | refused before effects | [running-mode QA](2026-09-26-recovery-snapshot-running-mode.md) |
| Parent / non-leaf delete | delete | refused before effects | Core admission + S2 delete QA |
| CDROM driver-type drift (qcow2 vs raw, same ISO) | revert / delete ownership | accepted (lab) | managed ownership regression |
| VMs-list bulk Snapshot | stopped create, shared name | supported (lab) | [browser QA](2026-09-26-recovery-snapshot-browser.md) (`bulk-snap-11665`) |
| Bulk Snapshot of running guests | create | skipped in UI; use Snapshots tab | [browser QA](2026-09-26-recovery-snapshot-browser.md) |
| Memory snapshots | any | untested / out of Phase 5 scope | platform plan |
| Physical mixed-host / production storage | any | untested | disposable nested lab only |

## Native feasibility backends (S0; not product workflows)

Synthetic marker domains on the selected nested Core lab with the recorded libvirt
package set. Detail: [S0 native](2026-09-25-recovery-s0-native.md),
[S0 interruption](2026-09-25-recovery-s0-interruption.md), firmware/branch QA under
`docs/qa/2026-09-25-*` and `docs/qa/2026-09-26-*`.

| Backend | Stopped lifecycle (create/revert/delete) | Mid-commit interruption |
|---|---|---|
| Local filesystem | supported (S0 lab) | supported (S0 lab) |
| NFSv4.2 VM storage | supported (S0 lab) | supported (S0 lab; one midcommit retry on 2026-09-26) |
| SMB 3.1.1 sealed | supported (S0 lab) | supported (S0 lab) |
| Exclusive iSCSI LUN (ext4) | supported (S0 lab) | supported (S0 lab) |

2026-09-26 reconfirmation on libvirt `+nex4`:
[recovery-s0-multi-backend-dr](2026-09-26-recovery-s0-multi-backend-dr.md).

These S0 backend rows do not by themselves prove Controller product paths on every
backend; product S2/S3 evidence above is the Controller/Core path scope.

## Accepted residuals (Phase 5 closed)

- Broader physical / production storage and multi-host replacement coverage beyond recorded
  nested disposable-lab campaigns (remain honest `untested` until executed).
- Product Controller/Core snapshot paths on every S0 storage backend (S0 native ≠ S2/S3 product;
  remain honest until product evidence exists per backend).
- Memory snapshots remain out of Phase 5 scope / untested.
- Resume order and lab notes:
  [recovery handoff](../plans/2026-09-26-recovery-session-handoff.md).

Native multi-backend S0 DR (2026-09-26) is recorded in
[recovery-s0-multi-backend-dr](2026-09-26-recovery-s0-multi-backend-dr.md). It strengthens the
S0 backend table above; it does **not** claim product S2/S3 on every storage type or Phase 6.
