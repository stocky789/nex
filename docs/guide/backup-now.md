# Back up a VM now

## Status

Development Phase 6 / S5 Controller UI for stopped and online full backup into a
registered NFS, SMB3, S3-compatible or SFTP repository. Core validates the guest,
captures disks
(stopped convert or online libvirt push-mode), publishes a complete manifest, then
catalogs the recovery point. Quiesced online capture and UEFI online remain refused.

## Prerequisites

- A registered backup repository ([Set up backup repositories](backup-repositories.md)).
- A managed VM that is shut off or running (BIOS for online), with fresh host and VM
  state, not the Controller appliance.
- Matching Controller and Core builds that expose backup Controller routes.

## Steps

1. Choose the pool, then open **Backups** (`#/backups`), or from the VM panel choose
   **Back up now** (`#/backups?vm=<id>&flow=backup-now`).
2. Select **Back up now**. Step 1: choose the VM and repository. For a running guest,
   choose Crash-consistent (Quiesced is shown but refused until supported). Step 2:
   review mode and included disks. Step 3: submit.
3. Follow the task on **Tasks**. When it succeeds, the recovery point appears on
   **Backups** for that VM and repository. A Back up now point is never removed by
   a policy's retention; protect it or remove it yourself when it is no longer needed.

## Expected result

Core accepts the request and, after publication, lists a recovery point with capture
time and `stopped` or `crash` consistency. The VM Overview **Last backup** shows that
time when the catalog is available. Closing the browser does not cancel accepted work.

## Troubleshooting

- **Stop the VM first** / shut-off required: use stopped mode; this flow does not
  power off automatically.
- **Online backup requires a running VM**: the VM is shut off, so choose stopped mode.
- UEFI online refused: shut down and use stopped backup, or wait for a later slice.
- Quiesced refused: use Crash-consistent for running guests.
- No repositories: register one under **Settings** → **Backup repositories**.
- Backup authority capacity refusal: Core could not admit more history within
  a member's verified replica state. Large histories travel as signed pages,
  but Core requires live proof that each required member holds the exact current
  replica. A run Core already admitted still records its outcome when a
  member is offline; only new runs and metadata wait. Inspect the retained
  Core run and member status before retrying; keep the master available when
  an exact complete replica cannot be verified.
  A disposable fixture passed complete-history promotion with directly seeded
  runs beyond 69 MB and bounded post-promotion backup and restore on all four
  destinations. Check the current authority and member access before retrying.
- Request refused or stuck: inspect the task and host operation before retrying. Reuse
  a request ID only for the identical request after the host reports a safe retry state.
- Backup interrupted while publishing, for example by a Core restart: Core does not
  capture the VM again. It settles a point that finished publishing, or removes the
  partial point and fails the run with "backup publication was interrupted". Start
  a new backup.

## Verification

The [product NFS lab](../qa/2026-09-27-phase6-product-backup-lab.md) exercised stopped
full backup; the later [browser report](../qa/2026-09-27-phase6-backup-browser.md)
records its actual UI flow and settlement limits. Focused Go tests cover online job
failure refusal. A later [destination lab](../qa/2026-09-28-backup-repository-destinations.md)
passed bounded online two-disk backup and isolated cross-host booted restore
for NFS, SMB3, S3 and SFTP. Final tracked enabled-build browser review and
the genuine two-slot missed-schedule gate remain open.
