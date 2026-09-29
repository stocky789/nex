# Set up backup repositories

## Status

Development Phase 6 / S4. A current pool master advertises NFS, SMB3, S3-compatible
and SFTP destinations to matching Controller builds. Core owns repository identity,
credentials, health, access observations and recovery points. Controller submits
requests and shows Core results. The four kinds passed bounded disposable backup
and restore checks; the final enabled-build browser and timed scheduler gates remain
open. Do not treat one tested server profile as certification of another.

Policies and schedules are described in [Set up backup policies and schedules](backup-policies.md).
Use **Import points** on a repository row, or [Rebuild the backup catalog](catalog-rebuild.md),
when Core must rediscover retained points.

## Prerequisites

- Administrator access, the correct pool scope, a connected pool master with backup
  inventory, and matching Controller/Core versions. Prepare destination access on
  every host that may run a backup or restore; pool registration alone does not
  establish member access.
- NFS: an existing writable NFSv4 export separate from VM disk storage. Core and
  QEMU accounts on intended hosts must be able to use it.
- SMB3: an existing encrypted SMB3 share, a dedicated account and writable
  directory. Keep the account available while its recovery points are retained.
- S3: an HTTPS S3-compatible endpoint, bucket/prefix and scoped access key. Core
  hosts must trust the endpoint certificate. The provider must pass Core's
  conditional create/delete, multipart, readback and owner checks; registration
  refuses a provider that does not.
- SFTP: an existing confined repository root, an account with an SSH private key,
  and the server's pinned `SHA256:` host-key fingerprint. The server must support
  durable file/directory sync and exclusive rename without replacing a target.
- Use disposable targets when evaluating a new server or service version. Preserve
  the old destination until every retained point there has expired or been restored.

## Steps

1. Choose the pool in the top-bar scope menu. Open **Settings** → **Backup repositories**
   (`#/settings/backups`) and select **Create**.
2. Enter **Name** and choose **Destination type**. Fill the fields for that type:
   - **NFS:** **NFS server**, **Export path**, and optional **Directory within export**.
   - **SMB3:** **SMB server**, **Share**, optional **Directory within share**,
     **Username** and **Password**.
   - **S3 compatible:** **HTTPS endpoint**, **Bucket**, optional **Prefix**,
     **Access key ID** and **Secret access key**.
   - **SFTP:** **Server**, optional **Port** (default 22), **Repository root**,
     **Pinned SSH host key fingerprint**, **Username** and **SSH private key**.
3. Select **Save** and follow the task until Core reports success or refusal. An
   HTTP 202 only means Controller queued the request. For SMB3, S3 and SFTP,
   Controller hands the secret to Core once and clears it from the form.
4. On the repository row, select **Test access**. Check fresh **Health** and **Host
   access** for the master and each intended member. Create a disposable two-disk
   backup, restore it to another compatible host, and verify both disks before
   relying on the repository.
5. Open **Policies** to select the repository for a schedule, or use **Backups** →
   **Back up now** for one VM. Core keeps the selected repository ID on each job.

## Expected result

The repository row shows its type and target. **Health** shows Core's latest online
or offline probe with its observation time; it becomes unknown when no fresh report
is available. **Host access** lists only Core-observed member access. Old capacity
and access remain last reported, not proof of current access. A fresh Controller
adopts the same repository from authenticated pool-master inventory. A backup or
restore continues on Core after the browser or Controller disconnects.

## Troubleshooting

- **Create** or **Test access** fails while the master is unavailable: restore its
  connection and matching version, then inspect the exact task. After a lost reply,
  check **Tasks** and retry only the identical request; a new request ID may start
  separate work.
- Health is offline or unknown: check the report time, destination service,
  credentials, server identity and each host's access. Unknown includes an
  unreachable master. Correct the cause, then run **Test access** and an actual
  disposable restore. A past online report is not a current write guarantee.
- NFS mount missing or changed: verify the exact export and filesystem identity
  before retrying. A local directory at the same path is not an independent backup.
- SMB3 access fails: check the share, account, encrypted mount and owner/share
  markers. A write-denied share can refuse new backups while an intact point
  remains readable; a cached CIFS session may temporarily keep old access alive.
- S3 registration fails: verify HTTPS trust, bucket/prefix permissions and the
  provider's conditional operations and multipart behavior. A wrong-ETag delete
  that still removes an object is unsafe and Core refuses it.
- SFTP registration fails: verify the root exists, the pinned host key matches,
  the key/account has the needed rights, and the server supports durable sync and
  exclusive rename. Core refuses a missing owner on adoption rather than claiming
  an established repository as new.
- To move a target or rotate SMB3/S3/SFTP credentials, create and test a **new
  repository ID** at a separate directory, prefix or root. Use **Change repository**
  on each policy and select the new ID for new jobs. Verify a new backup and restore.
  Keep the old target and credentials for its retained points; Core refuses
  same-ID source or credential replacement. External credential revocation can
  make old points unavailable once existing sessions end.
- A published point with unfinished retention may leave a task in
  `needs_attention`. Restore destination access and inspect that exact task and
  catalog before retrying; Core reconciles the original run without recapturing.

## Verification

The [destination QA report](../qa/2026-09-28-backup-repository-destinations.md)
records bounded native registration, backup, restore, outage, replay, member,
retention and authority checks for all four kinds. The [NFS product lab](../qa/2026-09-27-phase6-product-backup-lab.md)
and [backup browser report](../qa/2026-09-27-phase6-backup-browser.md) record
older NFS workflows. Later live candidate browser checks covered SMB3 policy
switch and SFTP credential handoff. A final browser walkthrough on the tracked
enabled build and the real two-slot missed-schedule check remain open.
