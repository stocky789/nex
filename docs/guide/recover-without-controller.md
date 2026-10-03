# Recover backups without Controller

## Status

Development Phase 6 / S7. The authenticated Core-local API exposes backup status, restore,
protect, test restore and catalog rebuild while Controller is absent. The CLI provides
catalog rebuild; the local web page provides login, not a backup management console.

## Prerequisites

- Enrolled pool master (or surviving master after supported promotion)
- A Core-local session at `https://<host>:9443` for API calls, or root for CLI rebuild
- The selected repository is reachable from the executing host, with its
  original owner and credential access intact

## Steps

1. Sign in on the Core local page, then call the local API with its session cookie and CSRF token.
2. Status: `GET /api/local/v1/pool/backup-status` — repositories, catalog and runs.
3. Restore as new: `POST /api/local/v1/pool/backup-restore` with
   `{operation_id,repository_id,recovery_point_id,new_vm_id,new_vm_name,storage_id}`.
   The local restore runs on the pool master, so the restored VM is placed on the master,
   not on the host of the VM the point came from, even for a member VM's point. Choose
   `storage_id` on the master.
4. Restore replace: `POST /api/local/v1/pool/backup-restore-replace` with
   `{operation_id,repository_id,recovery_point_id,original_vm_id,storage_id,confirm_replace:true}`.
5. Protect: `POST /api/local/v1/pool/backup-protect` with
   `{operation_id,repository_id,recovery_point_id,protected}`.
6. Test restore: `POST /api/local/v1/pool/backup-test-restore`, then
   `POST /api/local/v1/pool/backup-test-verify` (`result`: `passed`|`failed`), then
   `POST /api/local/v1/pool/backup-test-cleanup` for that test VM only.
7. Catalog loss: follow [Rebuild the backup catalog](catalog-rebuild.md).

## Expected result

Core retains policies and run records while Controller is absent. Check each operation's
status and inspect the restored guest before using it. A fresh Controller reads the
master's reported repository, policy, run and catalog inventory without the old database.

## Troubleshooting

- `409` enroll guidance: this host is not enrolled; join or promote with supported Core paths first.
- Repository unavailable: repair the selected NFS export, SMB3 share, S3
  endpoint/bucket or SFTP root and credentials, then call
  `POST .../backup-repositories/{id}/test` and inspect the actual restore.
- Lost mutation reply: query `GET /api/local/v1/pool/backup-operations/{operation_id}`
  and keep the original operation ID and body for any exact retry. A repository
  or policy mutation replay may return a `Result` with its durable status
  instead of the original resource body; check that status and refresh the
  resource list.
- Restore still `needs_attention` after master promotion: the new master
  retains the run and recovery-point pin but does not repeat uncertain native
  execution on an exact retry. Inspect the target and resolve the retained
  outcome before another restore to that target. A different target may use
  the same recovery point while both operation pins protect it from prune.
- Restore point digest mismatch: Core checks the selected repository manifest
  against the master's catalog digest before native placement. Inspect the
  point and repository; do not replace the manifest under the same point ID.
- Pending test cleanup after promotion: Core retains the point pin and
  reconstructs only an exact pending cleanup proved by its receipt and one
  successful test run. Inspect the owner's native result and the master VM
  inventory; a missing or ambiguous proof needs attention before another
  cleanup attempt.
- Replica admission refusal: large complete histories travel as signed pages,
  but Core can refuse new backup metadata when a required member
  lacks the exact current verified replica or cannot be reached. Inspect the
  retained operation and member replica status before retrying; keep the
  existing master available if an exact complete replica cannot be verified.
  A disposable fixture passed fence-verified promotion with 1,052 retained
  seeded runs beyond 69 MB and exact retained promotion-task settlement;
  bounded post-promotion backup/restore passed for all four destination kinds on an earlier
  build (see the [destination QA matrix](../qa/2026-09-28-backup-repository-destinations.md),
  which does not accept the current tree).
  The final tracked enabled-build and genuine missed-slot gates remain open.
- Promotion at `catalog-rebuild` with `needs_attention`: the new master has
  committed its identity but could not import an empty replicated catalog from
  the registered repositories. Restore repository access and check the
  host-reported transfer status. Core retries the import before re-pointing
  members; do not start another promotion for the same authority. A cancelled attempt stays
  retryable at `catalog-rebuild`; a repository that is simply unreachable no longer blocks the
  change: it is recorded and its catalog is imported automatically once it is reachable.

## Verification

The [bounded Campaign A walkthrough](../qa/2026-09-27-phase6-campaign-a.md) exercised
Core-local restore without Controller in a disposable nested lab. It does not establish
the full three-host outage or physical failure-domain case.
