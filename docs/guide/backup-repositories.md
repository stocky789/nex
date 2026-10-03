# Set up backup repositories

## Status

Development Phase 6 / S4. A current pool master supports NFS, SMB3, S3-compatible
and SFTP destinations. Core owns repository identity,
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
  QEMU accounts on intended hosts must be able to use it. Core writes as root, so
  export it with `no_root_squash`, or with an export option that maps root to the
  directory's owner; on a root-squashed export registration fails with a bare
  `permission denied` and the task gives no hint about the squash.
- SMB3: an existing share that supports SMB 3.1.1 with encryption, a dedicated
  account and writable directory. Keep the account available while its recovery
  points are retained.
- NFS and SMB3: a **Directory within export** or **share** you enter must
  already exist, be a real directory (not a symlink) and be writable; Core does
  not create it. Core mounts these repositories `nosuid,nodev,noexec` (data
  only); SMB3 mounts also use `vers=3.1.1,seal`.
- S3: an HTTPS S3-compatible endpoint, bucket/prefix and scoped access key. Core
  hosts must trust the endpoint certificate. Core uses path-style addressing
  (`https://endpoint/bucket/key`) and signs requests for the optional **Region** you enter
  (default `us-east-1`), so the provider must accept both. The provider must
  pass Core's conditional create/delete, multipart, readback and owner checks;
  registration refuses a provider that does not.
- SFTP: an existing confined repository root, an account with an SSH private key,
  and the server's pinned `SHA256:` host-key fingerprint. The server must support
  durable file/directory sync and exclusive rename without replacing a target.
- Use disposable targets when evaluating a new server or service version. Preserve
  the old destination until every retained point there has expired or been restored.

## Steps

1. Choose the pool in the top-bar scope menu. Open **Settings** → **Backup repositories**
   (`#/settings/backups`) and select **Create**.
2. Enter **Name**. Leave **Repository ID** empty for a new repository (Core generates one);
   to bring a forgotten destination back, or to replace a lost credential or an SFTP host key,
   enter the repository's original ID as described in [Reuse a repository ID](#reuse-a-repository-id).
   Choose **Destination type** and fill the fields for that type:
   - **NFS:** **NFS server**, **Export path**, and optional **Directory within export**.
   - **SMB3:** **SMB server**, **Share**, optional **Directory within share**,
     **Username**, **Password** and, for a domain account, **Domain**.
   - **S3 compatible:** **HTTPS endpoint**, **Bucket**, optional **Prefix**, optional
     **Region** (default `us-east-1`; lowercase letters, digits and `-`, used to
     sign requests, so set it for a provider that requires its own region),
     **Access key ID** and **Secret access key**.
   - **SFTP:** **Server**, optional **Port** (default 22), **Repository root**,
     **Pinned SSH host key fingerprint**, **Username** and **SSH private key**.

   For SMB3, S3 and SFTP, **Save** stays unavailable until the fields have a form Core
   accepts: an S3 endpoint is `https://host` with no path, bucket and share names contain no
   `/`, directories and prefixes are relative without `..`, an SFTP root is an
   absolute path, the fingerprint is `SHA256:` plus 43 characters, an SMB3
   username has no `=`, and an SMB3 domain is a plain name with no spaces,
   commas, `=`, `\`, `/` or other punctuation. The secret is not sent until the destination is valid.
   The form does not check every value: an NFS **Export path** without a leading `/`
   shows the browser's own format message when you select **Save**, and other values,
   such as an S3 endpoint with a port of 0 or a server name with a `:port`, are sent
   to Core, which refuses them. Core's reason stays above the form until you change it
   or close the form, so read it before retrying.
3. Select **Save** and follow the task until Core reports success or refusal. An
   HTTP 202 only means Controller queued the request. For SMB3, S3 and SFTP,
   Controller hands the secret to Core once and clears it from the form.
4. On the repository row, select **Test access**. Check fresh **Health** and **Host
   access** for the master and each intended member. A new repository can show
   members as errors for about a minute, and **Test access** does not refresh member
   access: the pool master delivers the repository to each member on its next health
   pass, and their access follows then, so wait and reload before treating it as a fault. Create a disposable two-disk
   backup, restore it to another compatible host, and verify both disks before
   relying on the repository.
5. Select **Backup jobs** (it opens **Backups**, where the **Backup jobs** section has
   **Create backup job**) to select the repository for a schedule, or use **Backups** →
   **Back up now** for one VM. Core keeps the selected repository ID on each job.

### Forget a repository

Forget a repository when its destination is gone or has changed identity, or when you no
longer use it. Forgetting never deletes anything from the destination.

1. Move every backup job that uses the repository to another one with **Change repository**
   on the policy, and let running backups, restores and test restores that use it finish.
   Clean up its test restore VMs.
2. On the repository row, select **Forget**. The dialog counts the repository's recovery
   points and how many are protected.
3. Type the repository name and select **Forget repository**. Follow the task until Core
   reports success or refusal.

Core forgets the repository, its recovery points and its run history, and removes its
mount and saved credential from the pool master and each member. A protected recovery
point is forgotten too, once you have confirmed the counts shown. Core refuses before
removal while a backup job uses the repository (move each such job with **Change repository**
or delete it), a backup, restore or test restore of it is `pending` or `needs_attention`, a
point remains reserved or pinned, cleanup or retention is active, another change to the
repository is running, the name is wrong, or the counted points changed. Missed, skipped,
succeeded and failed runs are history and are removed with the repository. A catalog import
or rebuild creates no run, so Core does not refuse for one; instead the import or rebuild is
refused at its commit once the repository is forgotten. Core checks again after it detaches
the mount and refuses if a new job, run or pin appeared meanwhile. New runs and jobs for the
repository are refused (conflict) while it is being registered or forgotten. The refusal
names unfinished restores and their test VMs where applicable.

Mount detachment and credential removal can be interrupted after some local work has
finished. Restore access, let the mount become idle, and retry the **same request**:
Core records a removal intent before it removes records, and a retry under the same request
resumes that removal (without re-running the blocker and count checks) and its receipt
rather than starting another forget. While that intent exists new jobs and runs for the
repository get a conflict. An already recorded terminal refusal retains that outcome;
correct its cause before submitting a new request. Unreachable members retain their exact
old registration until the master's reconciliation reaches them; newer registrations are
not removed by an old retry.

To use a forgotten destination again, register it again with its **original repository
ID** (see [Reuse a repository ID](#reuse-a-repository-id)), then select **Import points** to
bring its recovery points back. Read the ID from the repository row before you forget it (it
is shown under the name). Registering the same destination under a new ID is refused with
"backup request conflicts with retained work", because its owner marker names the original ID.

### Reuse a repository ID

The **Create** form has an optional **Repository ID**. Core keeps a repository's identity,
recovery points and history under its ID, so reuse the ID whenever the destination is the
same one:

- **Forgotten repository:** enter its original ID, the destination's fields and a credential,
  then **Save**. Core verifies the destination's owner marker, recreates the repository with
  that ID, and you select **Import points**.
- **Credential lost or rotated, or the SFTP server's host key changed:** enter the existing
  repository's ID. The form recognises it, warns that saving replaces its credential and, for
  SFTP, its pinned host key, and asks you to type the repository ID to confirm. Enter the
  new credential (and, for SFTP, the new **Pinned SSH host key fingerprint**), leave the host,
  port, root and every other destination field as they were, and **Save**. Core checks the
  destination's owner marker with the new key instead of claiming the destination, keeps the
  repository's recovery points, history and creation time, and members adopt the new key and
  credential on their next health pass. Check the new fingerprint against the server yourself
  first: pinning an attacker's key would trust it.

Core refuses, changing nothing, a replacement that names a different kind, host, port, root,
bucket, share or export, an ID it does not hold, or a destination whose owner marker names another
repository. The typed ID is only a confirmation; it never lets Core adopt a destination it cannot
verify. Without the typed confirmation, registering an existing ID with a different working
credential or host key is refused, as before.

### Delete orphaned backups

Backups of a VM that was deleted from the pool stay in the repository and keep using its
space. Retention does not remove them once no backup job covers the VM.

1. On the repository row, select **Orphaned backups**. Core lists the VMs that have recovery
   points in this repository but are not in the pool's VM inventory: the VM's last known name,
   its recovery points, how many are protected, their size, and the newest and oldest. A VM
   on an offline host is still in the inventory, so it is never listed. If Core cannot read
   its VM inventory it says so and lists nothing; nothing can be deleted then.
2. Read any warning about a backup job that still lists a missing VM. **Change repository**
   sends the job's VM list to Core unchanged, and Core refuses the change while the list
   names a VM that is gone, so the job stays on its old repository. To stop such a job, select
   **Delete** on it (see [Backup policies](backup-policies.md)), which removes only the job
   definition; its recovery points stay and appear here. Its VM list can also be corrected
   with `PUT /api/v1/pools/{id}/backup-policies/{policy_id}`.
3. Select the VMs to clean up. Tick **Include protected recovery points** only if their
   protected points must go too; otherwise protected points are kept.
4. Type the text shown, such as `delete 5 recovery points`. The number counts every point of
   the VMs you selected, including protected points that will be kept because **Include
   protected recovery points** is off; the line above the field says how many will be deleted.
   With protected points included the text states how many are protected, such as
   `delete 5 recovery points including 2 protected`. Core's wording is fixed, so a single point
   reads `delete 1 recovery points`. Select **Delete recovery points**.
5. The panel follows Core's record: each recovery point ends deleted, skipped with the reason,
   or failed, listed with its VM's name. Skipped points stay in the repository and the catalog.
   When some points could not be deleted the panel still shows every point's result with Core's
   error above it. If the panel cannot read the result for about 30 seconds it says so and points
   to **Tasks**; the cleanup may still be running. When Core refuses the plan, or a cleanup
   stalls (a failed or needs-attention task with a running record), the panel offers
   **Review again**; the retry and replay behaviour is unchanged.

Deleting removes each recovery point's data from the destination and then its catalog entry, the
same way retention does; this cannot be undone. Core skips protected points unless included, points
held by an unfinished restore or test restore or named by a running backup, and the points of a VM
that exists again. Core checks the VMs and counts again when it runs and refuses, deleting
nothing, if they changed since the panel counted them; open the panel again and review. Sending
the same request again returns the same outcome, and a run that was interrupted continues from
its recorded plan; Core saves progress every 25 points or two seconds, so after a crash up
to 25 points are confirmed again rather than deleted twice. A point the destination could
not delete is shown as failed with its reason (cut off at 4,096 bytes); fix the
destination and run the cleanup again. The list is read from the pool master, so a new
Controller shows the same list and history of cleanups. On S3, Core can clear a point whose
manifest is sound but whose artifacts are missing or resized; a point with a missing or
invalid manifest cannot be cleared from here and needs manual cleanup on the destination.

## Expected result

The repository row shows its type and target. **Health** shows Core's latest online
or offline probe with its observation time (**Last check** reads **Not checked** when Core has
no observation); it becomes unknown when no fresh report
is available. **Host access** lists only Core-observed member access. Old capacity
and access remain last reported, not proof of current access. A fresh Controller
adopts the same repository from authenticated pool-master inventory. A backup or
restore continues on Core after the browser or Controller disconnects.

Prune, protect and catalog rebuild check each artifact's presence and recorded
size without reading its content, so they stay fast on large points. Restore
and test restore read every artifact and verify its digest.

## Troubleshooting

- After an uncertain registration reply, retry the unchanged draft; the UI preserves the
  exact request and credential receipt without sending the secret again. Changing pools
  clears that draft and requires a fresh credential for the new pool. It does not cancel
  work Core already accepted.
- A failed SMB3 credential replacement restores the previous mount and access when
  rollback is confirmed. If rollback is uncertain, keep both credentials and inspect
  the reported mount state before another replacement; Core does not discard the old
  usable credential merely because preparation failed.
- If Core restarts while registration or replacement cleanup is finishing, inspect
  the original task. A saved successful or uncertain receipt is replayed rather than
  preparing the destination again. Unfinished or unrecordable rollback remains
  pending or needs attention and retains both credentials; do not delete them or
  treat a disconnected Controller as proof of a clean failure.
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
- An S3 upload the provider refuses, such as an object over its size limit,
  fails the backup run with the provider's error in the run's message. Core does
  not pre-check sizes.
- S3 registration fails: verify HTTPS trust, bucket/prefix permissions and the
  provider's conditional operations and multipart behavior. A wrong-ETag delete
  that still removes an object is unsafe and Core refuses it. A refused
  registration releases the prefix, so correct the provider and register again.
  Core refuses a prefix of `.` and a prefix inside another repository's prefix.
- Core refuses a destination field with control characters, an SMB3, SFTP or S3 server name
  with a `:port` (SFTP has its own **Port**; an IPv6 address is accepted), a directory or
  prefix of `.`, or an S3 endpoint port outside 1-65535. It does not refuse an NFS server
  with a `:port`; the mount then fails to resolve the name, so enter the bare host name or address.
- SFTP registration fails: verify the root exists, the pinned host key matches,
  the key/account has the needed rights, and the server supports durable sync and
  exclusive rename. Core refuses a missing owner on adoption rather than claiming
  an established repository as new. "server offers no supported host key type" means the
  server offers no key algorithm Core can pin; "host key differs from pinned key" means the
  key changed (see the rotation steps below).
- To rotate SMB3/S3/SFTP credentials or change an SFTP host key on the same destination,
  [reuse the repository ID](#reuse-a-repository-id). To move a target, create and test a **new
  repository ID** at a separate directory, prefix or root. Use **Change repository**
  on each policy and select the new ID for new jobs. Verify a new backup and restore.
  Keep the old target and credentials for its retained points, then forget the old
  repository when you no longer need them; Core refuses
  same-ID source replacement, and credential replacement without the typed confirmation while it can still read the old one. External credential revocation can
  make old points unavailable once existing sessions end.
- The host check **backup-credentials** says a receipt is unreadable: Core still
  uses and reports every other credential. Recreate the credential for the
  affected repository if a registration or backup reports it unavailable.
- A repository shows **offline** with "backup credential is unreadable on this
  host; register the repository again with its credentials" when the pool master can no longer
  open its credential (for example the sealing key was lost). Other repositories are
  unaffected. To recover, create a new credential and register the repository again
  with the **same repository ID and the same destination**: enter the ID in **Create**
  (see [Reuse a repository ID](#reuse-a-repository-id)). Core replaces
  the credential because it cannot read the old one, keeps the ID, recovery points and history,
  and refuses a different destination with "a repository with this ID already exists for a different destination". Registering again while Core can still read the
  credential is refused with "this repository already has a working credential; confirm replacing it with the repository ID, or forget the repository first" unless you type the repository ID to confirm the replacement.
  Members receive the new credential, the new credential reference and the repository name from the master on their next
  health pass; until then the member's access shows why delivery failed. If a credential request ID is reused after the key was lost, the credential request is refused with that reason (send the credential again with a new request ID). Only S3 has been exercised for this. If the destination is gone
  or its identity changed, [forget the repository](#forget-a-repository) and register the
  destination as a new one.
- A member that was unreachable when a repository was forgotten keeps its old record
  and mount until the master's next successful reconciliation. Detachment or unsettled
  journal work may require attention; restore connectivity and resolve the reported work.
  A stale cleanup cannot remove a newly adopted registration under the same ID.
- A host that joins another pool or is ejected unmounts and forgets the backup
  repositories of the pool it left and erases their backup credentials; its new
  pool delivers its own. Data on the share stays.
- Core stores the credential receipts for every backup destination encrypted under a
  host-local sealing key; Core never keeps the cleartext secret beside the repository
  record. For SMB3, systemd 257 cannot pass a decrypted systemd credential into a
  `.mount` helper, so Core also writes the mount password in plaintext to a root-only
  (mode 0600) `mount.cifs` credentials file at the fixed path
  `/var/lib/nex-core-agent/backup-smb-credentials/<reference>` and points the mount unit
  at it with `credentials=`. That path does not follow `--state-dir`, so a copy of the
  default state directory contains the SMB3 password; protect backups of the host
  accordingly. The sealing key for the encrypted receipts lives in a root-only
  `backup-credential-key` directory in Core's state directory; a copy of the state
  directory alone does not open the S3, SFTP or SMB3 receipts on another host, while root
  on this host still can. If the key file is lost while receipts remain, Core refuses to
  create a new one: restore the key file, or remove the receipts and register the
  repositories again. After rebuilding or replacing a host, recreate its backup
  credentials. A damaged or unreadable sealed receipt marks only that repository
  offline until you register it again.
- If Core stopped in the middle of removing a protection marker, a `.nex-protected.rm-*`
  file may remain beside the recovery point. Core puts it back when the point has no
  marker, and removes it when it matches the current one; any other leftover is kept for
  you to inspect.
- Retiring or replacing an SMB3 credential also deletes its mount credentials file.
- Core refuses an SMB3 username that contains `=`.
- A published point with unfinished retention may leave a task in
  `needs_attention`. Restore destination access and inspect that exact task and
  catalog before retrying; Core reconciles the original run without recapturing.
- NFS or SMB3 prune names an unexpected entry in a recovery point: Core removes
  file-manager litter (`.DS_Store`, `Thumbs.db`, `desktop.ini`, `._*`) itself but
  refuses anything else, such as an `@eaDir` folder. Move that entry out of the
  point directory, then let the next backup's retention run.

## Verification

The [destination QA report](../qa/2026-09-28-backup-repository-destinations.md)
records bounded native registration, backup, restore, outage, replay, member,
retention and authority checks for all four kinds. The [NFS product lab](../qa/2026-09-27-phase6-product-backup-lab.md)
and [backup browser report](../qa/2026-09-27-phase6-backup-browser.md) record
older NFS workflows. Later live candidate browser checks covered SMB3 policy
switch and SFTP credential handoff. The [review-fix walkthrough](../qa/2026-10-02-backup-review-fixes.md)
exercised typed replacement confirmation, an uncertain accepted reply followed by an
identical credential-receipt retry, and clearing the draft when changing pools in the
built browser UI with fictional APIs. This is not a real-destination credential replacement
or SFTP host-key rotation walkthrough; those and the real two-slot missed-schedule check remain open.
