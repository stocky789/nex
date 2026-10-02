# Set up backup jobs and schedules

## Status

Development Phase 6 / S6. Core owns backup policies, schedules, retention and run
history on the pool master. Controller lists, creates, pauses and Run now through
authenticated APIs. Core schedules can run while Controller is absent. The Create
policy UI currently submits `stopped`; quiesced capture is refused, and online
crash capture is limited to supported running BIOS guests.
NFS, SMB3, S3-compatible and SFTP repositories are supported in the current
development build. The final enabled-build browser and timed scheduler gates
remain open; use the selected destination's fresh Core health and host access
before relying on a policy.

## Prerequisites

- A registered and tested backup repository for the pool
  ([Set up backup repositories](backup-repositories.md)).
- Administrator access and a freshly connected pool master that reports
  `backup_policies_reported`.
- Use disposable lab resources for development verification.

## Steps

1. Choose the pool in the top-bar scope menu.
2. Open **Backups** → **Backup jobs** and select **Create backup job**. (Older wording called these "policies"; the API and Core still do.)
3. **Coverage** (step 1 of 3): name the backup job, choose the repository and the VMs to include.
   If Core reports the repository offline or its health is unknown, review the
   warning and correct access before relying on a scheduled run. Saving does not
   select another destination.
4. **Timing and retention** (step 2 of 3): set IANA time zone, daily or weekly local time, and keep-latest.
   Retention applies to each VM separately and only to the recovery points this policy
   created. Manual **Back up now** points and other policies' points are never pruned,
   even in the same repository. Rules are a union: a point stays if any rule keeps it.
   Keep-latest counts only points that are not protected, restoring or newer than the
   age window, so protected, in-use and recent points stay in addition to the newest
   keep-latest. There is no retention preview, because a new policy has created no points yet.
5. **Review** (step 3 of 3): confirm the selected VM names, repository, local schedule
   and retention, then **Save backup job**. Back, Next and Save stay in the panel footer.
6. Select a job or its **Details** button to expand it. Use **Pause** / **Resume**
   in the expanded job to stop or allow future dispatches without deleting the
   policy. Each included VM has **Run now** and **View points**; Run now does not
   change the next scheduled occurrence.
7. To move a policy to another registered repository, expand its job and select
   **Change repository**. Choose **New repository**, review the current repository and the health
   shown for the new one, check **I confirm this backup job should use ...**, then select
   **Save repository change**. Verify the updated policy before starting a new
   backup. New one-off jobs require their own repository selection; existing
   jobs are not switched automatically. The update uses the policy revision
   shown by Core; another change made meanwhile causes a conflict. If the job still lists
   a VM that was deleted from the pool, saving removes that VM from the job (the panel says
   how many); a job whose VMs are all deleted cannot be moved from the UI. Delete such a job
   (step 8), or use `PUT /api/v1/pools/{id}/backup-policies/{policy_id}` with corrected
   `vm_ids` and the current `expected_revision`. Core rejects a job that lists a deleted VM
   with a generic "invalid backup request".
8. To delete a backup job, expand it and select **Delete**. The dialog states what is kept.
   Type the job's name to confirm, then select **Delete backup job**. Core removes only the
   job's definition (schedule, VM list and retention rules) on the pool master, and no
   further backups are scheduled for it. Its recovery points, its run history and the
   repository's data are not deleted. Retention no longer applies to those points, so Nex
   does not remove them automatically; restore them, or delete them from **Recovery points**
   or **Orphaned backups** when you no longer need them. Core refuses while a backup of the
   job is running (the task names the run); wait for it to finish and delete the job again.
   Once Core accepts the delete, no new backup starts for that job.
   A run left in **needs attention** does not block the delete, so a stuck run never traps you:
   its history row stays under **Backup runs** with that status, and Core does not retry it for the
   deleted job. Check the repository for a partial backup before you delete the job.
   Deleting cannot be undone; create a new job to resume backups of those VMs. The delete is
   safe to retry with the same request, and a job that is already gone is reported as not found.

## Expected result

**Backup jobs** and **Recovery points** are separate views on the Backups page.
Job filters and expansion stay in place when switching views. **All**, **Needs
attention** and **Paused** filter jobs; Needs attention includes a VM whose latest
run failed, was missed or needs attention. Those jobs expand automatically unless
you have chosen their expansion state. Each expanded VM shows its latest outcome;
wider screens also show its last successful backup separately. **View points**
opens Recovery points with that VM selected.

The job table shows name, VM count, repository, schedule, next run in the job's
own time zone (blank while paused), separate latest-result counts and **Enabled**,
**Paused** or **Disabled** status. An enabled schedule is not proof of a successful
backup. Unavailable run history is **Unknown**, not an empty history. On phones,
rows keep the name, result, time and controls while secondary columns are hidden.
The time-zone list starts on this browser's zone; start time uses its time picker.

Core records the policy under `pool-backup-policies.json` with a stable schedule ID and
revision. Due occurrences get a unique occurrence ID before dispatch; missed slots after
scheduler downtime are recorded without a catch-up storm. A fresh Controller adopts the
same policies from master inventory and reads retained run history on demand
through Core's authenticated paged API.

## Troubleshooting

- Create fails with invalid policy: check repository ID, time zone name, and at least one VM.
- No repository is registered: the creation panel links to **Backup repositories**
  in Settings and cannot advance until a destination is available.
- Repository inventory loading or unavailable: the panel shows that status instead
  of a registration link. Wait for a fresh master report; a failed read does not
  prove the pool has no repositories.
- An uncertain save keeps Review open and disables Back, Cancel and Close. Inspect
  **Tasks** before **Retry exact request**; the retry keeps the original coverage,
  repository, schedule, retention and request identity.
- Next run does not advance while paused: resume the schedule.
- **Delete backup job** fails with "type the backup job name": the typed name must match
  exactly. It fails with "backup run ... is still running": wait for that run to finish
  (or resolve it) and delete again. "backup policy not found" means the job is already gone.
- Missed runs appear after Core downtime: expected; use **Run now** rather than expecting
  automatic catch-up of every skipped slot.
- An offline or unknown repository warning reflects Core's latest report; check
  the destination and its observation time before retrying. Core retains failed
  or missed run history rather than silently redirecting a job.
- Backup authority capacity refusal: Core may refuse a new policy or run to
  preserve a complete member replica. Large histories travel as signed pages,
  but Core still requires live proof of each required member's exact current
  replica. A run Core already admitted still records its outcome when a
  member is offline; only new runs and metadata wait. Check the retained run
  and member status before retrying. Keep the master available when an exact
  complete replica cannot be verified. A
  disposable fixture passed promotion with directly seeded runs beyond 69 MB;
  bounded post-promotion backup and restore passed later for all four candidate
  destinations. Broader faults remain open.
- A run that remains reserved or reports `needs_attention` needs host-side inspection
  before an operator submits another run for the same VM.
- If history is unavailable or loading, **Unknown** is not evidence that a VM has
  never been backed up. Repository and policy actions remain available while Core's
  history request is outstanding. Changing pools discards the old scope's pending reads.
- Retention removes only points created by the same policy, and the catalog records
  that policy on each point and in the point's stored manifest. A catalog rebuild
  from the repository reads the policy back from the manifest, so rebuilt points
  are still pruned by their policy; manual points have none and are never removed
  automatically. When a VM is deleted from the pool, its remaining points are no longer
  pruned; free their space with **Orphaned backups** in
  [Backup repositories](backup-repositories.md#delete-orphaned-backups).
- Retention keeps the newest points by capture time, and never removes the point
  its backup just published, even when the capturing host's clock is behind; such
  a point is removed by a later backup if it is not among the newest. If retention cannot remove an expired point,
  the backup still succeeds: Core removes the other expired points, names the
  ones it skipped in the run's error and tries them again on the VM's next backup.
  Correct repository access or the damaged point. Backups shows the note as a
  warning in that backup's recovery point panel.
- A member VM's run that reports `needs_attention` stays linked to that member:
  the pool master re-reads the member's outcome about every 30 seconds and settles
  the run once the member resolves it, adding a published point to the catalog.
  A run the member never started, including one whose VM moved to another host
  before it began, settles `failed` and the next scheduled occurrence runs normally.
  Silence, departure from the pool, or an unverified outcome does not prove a run
  stopped; the master keeps its reservations until authenticated host evidence settles it.
- If **Change repository** reports a revision conflict, reload the policy,
  review its current settings and submit a new change. If the reply is
  uncertain, keep the panel open and use **Check task status**; any retry must
  use the same request without changing the selected repository.
- For a different SMB3, S3 or SFTP destination, register a new repository ID at a
  separate directory, prefix or root, then use step 7 to switch each policy. Verify
  a new backup and restore before retiring access to old points. For a credential
  replacement at the same destination, follow [Reuse a repository ID](backup-repositories.md#reuse-a-repository-id)
  and its typed confirmation. Keep old credentials usable until replacement or
  rollback is confirmed; an existing CIFS session can temporarily survive external revocation.
- Backup authority limits: the pool master keeps every repository, policy,
  run, recovery point, restore pin and verification receipt in its signed
  authority, so a takeover host holds them all. Core enforces at most 256
  repositories, 1,000 policies and 256 KiB per record. Run history, catalog
  points, restore pins and verification receipts have no count limit: up to
  64 MiB and 100,000 records of each travel in the small form, and beyond that
  Core uses paged replication, which still works but sends more data on each
  change. When the number of repositories or policies reaches 80% of its limit,
  the master's host reports a degraded `pool-backup-authority-capacity` check
  naming the limit and usage. The check clears by itself once repositories or
  policies are removed and the next minute's measurement runs.

## Verification

Go unit tests cover DST gap/repeat, duplicate refusal, Controller-absent tick logic
and retention vs protect. [Campaign A](../qa/2026-09-27-phase6-campaign-a.md)
exercised policy **Run now** during a Controller outage in a bounded nested lab.
A full wall-clock missed-slot span on the corrected candidate remains open;
member-host dispatch passed in a bounded native fixture. The repository-switch panel was reviewed in a browser with
simulated API responses, including pending task status and exact retry; a later
live SMB3 UI switch passed. A disposable Controller API switch and old-point
restore passed. A clean old-repository disconnect blocked restore in the
selected Samba profile; broader revocation behavior remains unverified
([destination QA](../qa/2026-09-28-backup-repository-destinations.md)).
The disposable replacement Controller's live browser view displayed adopted
policies, an offline warning and the Change repository panel; no switch was
submitted in that session.

Deleting a backup job is covered by Go tests against a real pool master with a member (name
refusal, refusal while a run is pending, removal with run history and recovery points kept,
no further scheduled runs, request replay, changed-payload conflict, absent job, crash retry),
Controller route tests (authentication, CSRF, validation, replay and conflict) and web unit
tests. It has not been walked through in a browser or against a lab pool.

The [backup workspace review](../qa/2026-10-01-backup-ui/README.md) exercised
weekly creation, empty-weekday refusal, named-VM review, retained job filters,
contextual actions and exact creation/repository retries with fictional API
responses. It does not establish scheduler, retention or destination correctness.
