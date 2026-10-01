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

1. Choose the pool, then open **Backups** → **Recovery points**, or from the VM
   panel choose **Back up now** (`#/backups?vm=<id>&flow=backup-now`).
2. Select **Back up now** if its slide-over is not already open. In **Destination**,
   choose the VM and repository. For a running guest, choose Crash-consistent
   (Quiesced is shown but refused until supported), then select **Review backup**.
3. In **Review and submit**, check the VM, repository, mode and included disks,
   then select **Back up now**. Navigation stays at the bottom of the panel.
4. Follow the task on **Tasks**. When it succeeds, the recovery point appears in
   **Recovery points** for that VM and repository. A Back up now point is never
   removed by a policy's retention; protect it or remove it yourself when it
   is no longer needed.

## Expected result

Core accepts the request and, after publication, lists a recovery point with capture
time and `stopped` or `crash` consistency. The VM Overview **Last backup** shows that
time when the catalog is available. Closing the browser does not cancel accepted work.

## Troubleshooting

- A backup or run-now for a backup job or repository Core does not hold fails
  (`backup policy not found` or `backup repository not found`). Retrying the same operation
  ID does not help; fix the job or repository and start a new backup.
- A run that settles failed with stage `peer_dispatch` was abandoned: its member left the pool,
  became the pool master, or stayed silent for an hour. The outcome is unknown, so check the
  repository catalog for the point (import the repository if it is missing) before starting a new backup.
- **Stop the VM first** / shut-off required: use stopped mode; this flow does not
  power off automatically.
- **Online backup requires a running VM**: the VM is shut off, so choose stopped mode.
- UEFI online refused: shut down and use stopped backup, or wait for a later slice.
- Quiesced refused: use Crash-consistent for running guests.
- No repositories: register one under **Settings** → **Backup repositories**.
- Repository inventory loading or unavailable: wait for a fresh master report.
  The panel shows the read status; an unavailable read does not mean a destination
  is missing and is not a reason to register another copy of it.
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
  A member that cannot serve the repository does not hold back backups of other repositories; if it never
  started the run, Core marks it failed with the member's reason after 10 minutes.
- A definite refusal, such as insufficient access, shows its reason inside the
  backup panel and permits correcting the selection or closing the form.
- An uncertain reply keeps the panel on review with Back, Cancel and Close disabled.
  Inspect **Tasks** before using **Retry backup request**, which resends the same
  VM, repository, mode and request identity; do not create a second request to
  resolve an unknown outcome.
- A backup still running on a member 12 hours after it was reserved, even if the member has stopped answering: Core marks it
  **Needs attention** with "the backup has not finished ... Check the member host and the
  repository" and frees the pool's backup slot. Core does not cancel it. Check that the member
  is running and the repository responds. If the backup later finishes, Core records its real
  result; do not start another backup of that VM until the run is settled.
  The task keeps that explanation while Core keeps checking the run; it changes only when the
  run really succeeds or fails.
- A backup that has not started: **Tasks** shows *Waiting:* with the reason, for example
  *member unreachable* when the member does not answer, or *waiting for an earlier backup of
  this VM to finish*. A test restore cleanup that cannot reach its member shows the same
  *member unreachable* reason. The note clears when the work moves on.
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

The [backup workspace review](../qa/2026-10-01-backup-ui/README.md) exercised the
two-step slide-over and exact retry after a simulated unavailable response.
Its fictional API fixture proves UI behavior, not native capture or storage integrity.
