# Set up backup policies and schedules

## Status

Development Phase 6 / S6. Core owns backup policies, schedules, retention and run
history on the pool master. Controller lists, creates, pauses and Run now through
authenticated APIs. Core schedules can run while Controller is absent. The Create
policy UI currently submits `stopped`; quiesced capture is refused, and online
crash capture is limited to supported running BIOS guests.
NFS, SMB3, S3-compatible and SFTP repositories are advertised in the current
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
2. Open **Backups** → **Policies**, or select **Create policy** from Recovery points.
3. **Coverage:** name the policy, choose the repository and the VMs to include.
   If Core reports the repository offline or its health is unknown, review the
   warning and correct access before relying on a scheduled run. Saving does not
   select another destination.
4. **Timing and retention:** set IANA time zone, daily or weekly local time, and keep-latest.
   Review the retention preview (protected points stay).
5. **Review:** confirm coverage, schedule and retention, then **Save policy**.
6. Use **Pause** / **Resume** on a row to stop or allow future dispatches without deleting
   the policy. **Run now** starts one identified run for a listed VM without changing the
   next scheduled occurrence.
7. To move a policy to another registered repository, select **Change repository**
   on its row. Choose **New repository**, review the old and new repository names
   and health, check **I confirm this policy should use ...**, then select
   **Save repository change**. Verify the updated policy before starting a new
   backup. New one-off jobs require their own repository selection; existing
   jobs are not switched automatically. The update uses the policy revision
   shown by Core; another change made meanwhile causes a conflict.

## Expected result

The Policies table shows name, VM count, schedule, next dispatch and enabled/paused.
Core records the policy under `pool-backup-policies.json` with a stable schedule ID and
revision. Due occurrences get a unique occurrence ID before dispatch; missed slots after
scheduler downtime are recorded without a catch-up storm. A fresh Controller adopts the
same policies from master inventory and reads retained run history on demand
through Core's authenticated paged API.

## Troubleshooting

- Create fails with invalid policy: check repository ID, time zone name, and at least one VM.
- Next run does not advance while paused: resume the schedule.
- Missed runs appear after Core downtime: expected; use **Run now** rather than expecting
  automatic catch-up of every skipped slot.
- An offline or unknown repository warning reflects Core's latest report; check
  the destination and its observation time before retrying. Core retains failed
  or missed run history rather than silently redirecting a job.
- Backup authority capacity refusal: Core may refuse a new policy or run to
  preserve a complete member replica. The legacy version 0 transfer has a 64 MiB
  limit; the development candidate uses v2 signed pages for larger histories
  but still requires live proof of each required member's exact current
  replica. Check the retained run and member status before retrying. Keep the
  master available when an exact complete replica cannot be verified. A
  disposable fixture passed promotion with directly seeded runs beyond 69 MB;
  bounded post-promotion backup and restore passed later for all four candidate
  destinations. Broader faults and mixed-version recovery remain open.
- A run that remains reserved or reports `needs_attention` needs host-side inspection
  before an operator submits another run for the same VM. If a backup point was
  published but retention could not finish, correct repository access and watch
  the original run: Core retries settlement without capturing the VM again.
  Check the run and retained points before starting another backup.
- If **Change repository** reports a revision conflict, reload the policy,
  review its current settings and submit a new change. If the reply is
  uncertain, keep the panel open and use **Check task status**; any retry must
  use the same request without changing the selected repository.
- For SMB3, S3 or SFTP credential rotation, register and test a new repository
  ID at a separate directory, prefix or root, then use step 7 to switch each
  policy. Verify a new backup and restore before retiring access to the old
  destination. Keep old credentials valid for the old points' restore window;
  Core refuses same-ID source or credential changes. An existing CIFS session
  may temporarily serve an old SMB3 restore after external revocation.

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
