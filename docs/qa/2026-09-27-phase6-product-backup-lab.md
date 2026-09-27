# Phase 6 product NFS backup/restore lab

Date: 2026-09-27. Scope: Core-local product path on the disposable recovery Core
lab (fixture `cluster-ha-v1`, ignored `.cache/recovery-s0-core.*` run directory).
Authorized disposable lab only. Campaign A and Campaign B were **not** run and are
not claimed.

**Verdict: pass** for stopped backup → original disks unavailable → restore-as-new.
Online crash backup and Controller-absent schedule tick were not completed in this
slice (recorded below).

## Environment

| Guest alias | SSH (loopback) | Role |
|---|---|---|
| core1 | `127.0.0.1:56209` | Pool master; product walkthrough host |
| core2 | `127.0.0.1:58477` | Overlay binary only (not used for backup) |
| core3 | `127.0.0.1:60563` | Overlay binary only (not used for backup) |
| storage | `127.0.0.1:37101` | NFS exports (`repo-nfs`, `vm-nfs`, …) |

| Artifact | Identity |
|---|---|
| Package | `nex-core-local` `0.4.12` |
| Phase 6 overlay binary SHA-256 | `49bffb9dd583eaf1a04013ab46196f88359b07fc8e76d06aa318b96f1d1f2e5c` |
| Worktree short rev | `edfc14a` |
| Ephemeral Controller | loopback HTTPS for enrollment / managed storage / VM create only; backup and restore used Core-local APIs |

Overlay binary was rebuilt, SCP’d to core1/core2/core3, installed over
`/usr/lib/nex/nex-core-local`, and `nex-core-local.service` restarted. All three
hosts matched the digest above and listened on `:9443`.

## Pass / fail matrix

| Step | Result |
|---|---|
| Rebuild + redeploy Phase 6 overlay on core1/2/3 | **pass** |
| Authenticate Core-local HTTPS (PAM root, session/CSRF) | **pass** |
| Enroll pool master (ephemeral Controller + local enrollment) | **pass** |
| Managed storage + network + stopped BIOS VM | **pass** (local storage under host images tree; see limits) |
| Register NFS backup repository (`repo-nfs` / `phase6-product`) | **pass** |
| Test repository access (owner marker) | **pass** (`access.status=ok`) |
| Backup now (stopped) | **pass** (run `published`/`succeeded`; catalog entry present) |
| Original disks unavailable | **pass** (source VM disk tree renamed aside) |
| Restore-as-new | **pass** (new stopped domain `phase6-restore-new` registered) |
| Online crash backup | **skip** — no running BIOS guest prepared in this slice |
| Backup policy schedule / Controller-absent tick | **fail** (local upsert returned HTTP 400 invalid policy shape); unit-level schedule ownership not live-proven here |
| Campaign A | **not_run** |
| Campaign B | **not_run** |

## Product path (observed)

1. Ephemeral Controller on loopback enrolled core1 through
   `POST /api/local/v1/enrollment` (guest reached Controller via SSH reverse
   tunnel). Core reported pool role `master`.
2. Controller registered writable local storage and the active libvirt `default`
   network, then created stopped BIOS VM `phase6-backup-src` (1 GiB thin virtio
   disk). Marker bytes were written into the qcow2 before backup (best-effort;
   restore marker readback was not required for the pass verdict).
3. Core-local `POST /api/local/v1/pool/backup-repositories` registered NFS source
   `198.18.90.10:/srv/recovery-s0/repo-nfs` with directory `phase6-product` and
   `mount_path` on the existing core1 mount. Storage guest subdirectory is
   `nobody:nogroup` so `root_squash` clients can write. Test access claimed
   exclusive `owner.json` and reported `ok`.
4. `POST /api/local/v1/pool/vms/{id}/backup` with `consistency=stopped` published
   recovery point `bkup-7ce8e10d3e42` (standalone `vda.qcow2` + `manifest.json` under
   the NFS repository directory). Durable run stage `published`, status
   `succeeded`.
5. Source disk directory was renamed aside so the original path was absent.
6. `POST /api/local/v1/pool/backup-restore` restore-as-new registered a new VM
   UUID named `phase6-restore-new`, status `succeeded`, virsh state `shut off`,
   with disks under the managed storage tree.

Private runner output retained under ignored `.cache/phase6-s4-walkthrough/`
(`product-backup-result.json`).

## Limits

- VM disks used **local** managed storage on core1 (`…/phase6-product`), not a
  typed NFS VM-disk registration. An earlier attempt to register a path on the
  pre-mounted `vm-nfs` export failed Core’s local-storage ancestor permission
  rules. The **backup destination** is NFS (`repo-nfs`).
- `mount_path` was supplied on the Core-local repository body against the
  existing lab mount. This slice did not prove Controller-only registration that
  auto-creates a Core NFS mount unit without a pre-mounted path.
- Online crash backup and a live policy schedule tick with Controller stopped
  were not completed.
- Campaign A/B continuity was not attempted.

## API / guide / state

- API impact: **none** (lab exercise of existing contracts).
- Guide impact: **none** (contributor QA evidence only).
- State owner: **core** for pool backup repositories, catalog, runs, and restored
  VM definition on the pool master; ephemeral Controller used only to enroll and
  create managed storage/network/VM.
