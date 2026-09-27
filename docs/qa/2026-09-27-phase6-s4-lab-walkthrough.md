# Phase 6 / S4 disposable-lab product walkthrough

Date: 2026-09-27. Scope: product-path probe on the disposable recovery Core lab
(fixture `cluster-ha-v1`, ignored `.cache/recovery-s0-core.*`). Authorized
disposable lab only.

**Status update (same day):** After Path A package install
([lab install](2026-09-27-phase6-lab-install.md)) and overlay redeploy, the
Core-local NFS backup/restore product path **passed**. Evidence:
[phase6 product backup lab](2026-09-27-phase6-product-backup-lab.md).
Campaign A and Campaign B remain **not run**.

## Earlier probe (pre-install)

The first walkthrough session found no `nex-core-local` package and no `:9443`
listener; product backup APIs were blocked. That historical matrix is retained
below for the pre-install state only.

### Lab readiness (observed at first probe)

| Guest alias | SSH (loopback) | QEMU | Notable services |
|---|---|---|---|
| core1 | `127.0.0.1:56209` | running | `libvirtd.socket` active |
| core2 | `127.0.0.1:58477` | running | `libvirtd.socket` active; SMB VM-disk + iSCSI mounts present |
| core3 | `127.0.0.1:60563` | running | `libvirtd.socket` active; capacity NFS mount present |
| storage | `127.0.0.1:37101` | running | `nfs-server` and `smbd` active |

### Product-path attempts (pre-install)

| Step | Result |
|---|---|
| Host-local backup APIs | **Blocked** — no Core service |
| Register NFS backup repository | **Blocked** |
| Stopped full backup | **Blocked** |
| Restore-as-new | **Blocked** |
| Campaign A / B | **Not run** |

## Current product walkthrough (post-install)

| Step | Result |
|---|---|
| Overlay redeploy SHA-256 `49bffb9d…2e5c` on core1/2/3 | **pass** |
| Enroll pool master + managed stopped VM | **pass** |
| NFS backup repository register + test | **pass** |
| Stopped backup-now + catalog | **pass** |
| Original disks unavailable + restore-as-new | **pass** |
| Online crash backup | **skip** |
| Policy schedule / Controller-absent tick | **fail** (invalid local policy body in this run) |
| Campaign A / B | **not_run** |

## What this does **not** prove

- Campaign A (Controller disappears; Core remains) or Campaign B (master +
  Controller host loss)
- Typed NFS VM-disk storage registration (VM disks used local managed storage;
  backup destination was NFS)
- Online crash-consistent backup on a running BIOS guest
- Live schedule dispatch with Controller stopped

## API, guide, state

- API impact: none (lab exercise of existing contracts).
- Guide impact: none (contributor QA evidence).
- Durable backup state owner: Core (pool master).
