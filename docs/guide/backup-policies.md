# Set up backup policies and schedules

## Status

Development Phase 6 / S6. Core owns backup policies, schedules, retention and run
history on the pool master. Controller lists, creates, pauses and Run now through
authenticated APIs. Core schedules can run while Controller is absent. The Create
policy UI currently submits `stopped`; quiesced capture is refused, and online
crash capture is limited to supported running BIOS guests.

## Prerequisites

- A registered NFS backup repository for the pool ([Set up backup repositories](backup-repositories.md)).
- Administrator access and a freshly connected pool master that reports
  `backup_policies_reported`.
- Use disposable lab resources for development verification.

## Steps

1. Choose the pool in the top-bar scope menu.
2. Open **Backups** → **Policies**, or select **Create policy** from Recovery points.
3. **Coverage:** name the policy, choose the repository and the VMs to include.
4. **Timing and retention:** set IANA time zone, daily or weekly local time, and keep-latest.
   Review the retention preview (protected points stay).
5. **Review:** confirm coverage, schedule and retention, then **Save policy**.
6. Use **Pause** / **Resume** on a row to stop or allow future dispatches without deleting
   the policy. **Run now** starts one identified run for a listed VM without changing the
   next scheduled occurrence.

## Expected result

The Policies table shows name, VM count, schedule, next dispatch and enabled/paused.
Core records the policy under `pool-backup-policies.json` with a stable schedule ID and
revision. Due occurrences get a unique occurrence ID before dispatch; missed slots after
scheduler downtime are recorded without a catch-up storm. A fresh Controller adopts the
same policies and runs from master inventory.

## Troubleshooting

- Create fails with invalid policy: check repository ID, time zone name, and at least one VM.
- Next run does not advance while paused: resume the schedule.
- Missed runs appear after Core downtime: expected; use **Run now** rather than expecting
  automatic catch-up of every skipped slot.
- Backup authority capacity refusal: Core may refuse a new policy or run to
  preserve a complete member replica. Check the retained run and replica
  status before retrying; keep the master available while the limit remains.
- A run that remains reserved or reports `needs_attention` needs host-side inspection
  before an operator submits another run for the same VM.

## Verification

Go unit tests cover DST gap/repeat, duplicate refusal, Controller-absent tick logic
and retention vs protect. [Campaign A](../qa/2026-09-27-phase6-campaign-a.md)
exercised policy **Run now** during a Controller outage in a bounded nested lab.
A full wall-clock schedule span and member-host dispatch on the corrected candidate
remain unverified.
