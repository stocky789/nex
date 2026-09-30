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
  QEMU accounts on intended hosts must be able to use it.
- SMB3: an existing share that supports SMB 3.1.1 with encryption, a dedicated
  account and writable directory. Keep the account available while its recovery
  points are retained.
- NFS and SMB3: a **Directory within export** or **share** you enter must
  already exist, be a real directory (not a symlink) and be writable; Core does
  not create it. Core mounts these repositories `nosuid,nodev,noexec` (data
  only); SMB3 mounts also use `vers=3.1.1,seal`.
- S3: an HTTPS S3-compatible endpoint, bucket/prefix and scoped access key. Core
  hosts must trust the endpoint certificate. Core signs requests for region
  `us-east-1` and uses path-style addressing (`https://endpoint/bucket/key`);
  there is no region field, so the provider must accept both. The provider must
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
2. Enter **Name** and choose **Destination type**. Fill the fields for that type:
   - **NFS:** **NFS server**, **Export path**, and optional **Directory within export**.
   - **SMB3:** **SMB server**, **Share**, optional **Directory within share**,
     **Username**, **Password** and, for a domain account, **Domain**.
   - **S3 compatible:** **HTTPS endpoint**, **Bucket**, optional **Prefix**, optional
     **Region** (default `us-east-1`; lowercase letters, digits and `-`, used to
     sign requests, so set it for a provider that requires its own region),
     **Access key ID** and **Secret access key**.
   - **SFTP:** **Server**, optional **Port** (default 22), **Repository root**,
     **Pinned SSH host key fingerprint**, **Username** and **SSH private key**.

   **Save** stays unavailable until the fields have a form Core accepts: an S3
   endpoint is `https://host` with no path, bucket and share names contain no
   `/`, directories and prefixes are relative without `..`, an SFTP root is an
   absolute path, the fingerprint is `SHA256:` plus 43 characters, an SMB3
   username has no `=`, and an SMB3 domain is a plain name with no spaces,
   commas, `=`, `\`, `/` or other punctuation. The secret is not sent until the destination is valid.
3. Select **Save** and follow the task until Core reports success or refusal. An
   HTTP 202 only means Controller queued the request. For SMB3, S3 and SFTP,
   Controller hands the secret to Core once and clears it from the form.
4. On the repository row, select **Test access**. Check fresh **Health** and **Host
   access** for the master and each intended member. Create a disposable two-disk
   backup, restore it to another compatible host, and verify both disks before
   relying on the repository.
5. Open **Policies** to select the repository for a schedule, or use **Backups** →
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
point is forgotten too, once you have confirmed the counts shown. Core refuses, changing
nothing, while a backup job uses the repository, a backup, restore, test restore or catalog
operation on it is running, a recovery point of it is held by an unfinished restore or test
restore, the name is wrong, or the recovery points changed since the dialog counted them;
the task shows the reason. Sending the same request again returns the same outcome. To use
the destination again, register it again as a new repository, then select **Import
points** to bring its recovery points back.

## Expected result

The repository row shows its type and target. **Health** shows Core's latest online
or offline probe with its observation time; it becomes unknown when no fresh report
is available. **Host access** lists only Core-observed member access. Old capacity
and access remain last reported, not proof of current access. A fresh Controller
adopts the same repository from authenticated pool-master inventory. A backup or
restore continues on Core after the browser or Controller disconnects.

Prune, protect and catalog rebuild check each artifact's presence and recorded
size without reading its content, so they stay fast on large points. Restore
and test restore read every artifact and verify its digest.

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
- An S3 upload the provider refuses, such as an object over its size limit,
  fails the backup run with the provider's error in the run's message. Core does
  not pre-check sizes.
- S3 registration fails: verify HTTPS trust, bucket/prefix permissions and the
  provider's conditional operations and multipart behavior. A wrong-ETag delete
  that still removes an object is unsafe and Core refuses it. A refused
  registration releases the prefix, so correct the provider and register again.
  Core refuses a prefix of `.` and a prefix inside another repository's prefix.
- Core refuses a destination field with control characters, a server name with a
  `:port` (SFTP has its own **Port**; an IPv6 address is accepted), a directory or
  prefix of `.`, or an S3 endpoint port outside 1-65535.
- SFTP registration fails: verify the root exists, the pinned host key matches,
  the key/account has the needed rights, and the server supports durable sync and
  exclusive rename. Core refuses a missing owner on adoption rather than claiming
  an established repository as new.
- To move a target or rotate SMB3/S3/SFTP credentials, create and test a **new
  repository ID** at a separate directory, prefix or root. Use **Change repository**
  on each policy and select the new ID for new jobs. Verify a new backup and restore.
  Keep the old target and credentials for its retained points, then forget the old
  repository when you no longer need them; Core refuses
  same-ID source replacement, and credential replacement while it can still read the old one. External credential revocation can
  make old points unavailable once existing sessions end.
- The host check **backup-credentials** says a receipt is unreadable: Core still
  uses and reports every other credential. Recreate the credential for the
  affected repository if a registration or backup reports it unavailable.
- A repository shows **offline** with "backup credential is unreadable on this
  host; register the repository again with its credentials" when the pool master can no longer
  open its credential (for example the sealing key was lost). Other repositories are
  unaffected. To recover, create a new credential and register the repository again
  with the **same repository ID and the same destination**. Core replaces
  the credential only because it cannot read the old one, keeps the ID, recovery points and history,
  and refuses a different destination. Registering again while Core can still read the
  credential is refused. Members receive the new credential from the master on their next
  health pass; until then the member's access shows why delivery failed. Only S3 has been exercised for this. If the destination is gone
  or its identity changed, [forget the repository](#forget-a-repository) and register the
  destination as a new one.
- A member that was unreachable when a repository was forgotten keeps an unused record
  and mount until the master is asked to forget that repository ID again; the request
  succeeds when the repository is already gone. A conflict on registering a changed
  destination under that ID means a member still holds the old record.
- A host that joins another pool or is ejected unmounts and forgets the backup
  repositories of the pool it left and erases their backup credentials; its new
  pool delivers its own. Data on the share stays.
- Every Core stores backup credentials encrypted; nothing readable is kept on
  disk. An SMB3 password is sealed with the host's TPM2 chip (or, without one, the
  host key) and the repository's mount unit decrypts it each time it starts, so an
  SMB3 repository remounts after a host reboot without Core preparing it again.
  Sealed passwords only decrypt on the same host: after rebuilding or replacing a
  host, recreate its SMB3 credentials. A credential file that is not encrypted or
  was altered is refused: the **backup-credentials** check fails and you
  recreate that repository credential. The encryption key for these credentials is
  itself sealed with the host key (or TPM2 when present) in its own root-only
  directory (`backup-credential-key`) beside the Core state, and the service loads
  it at start. A copy of the state directory alone therefore does not open the
  credentials on another host; root access on this host still does. If the key is
  lost the check reports it missing instead of making a new one; recreate the
  credentials.
- If Core stopped in the middle of removing a protection marker, a `.nex-protected.rm-*`
  file may remain beside the recovery point. Core puts it back when the point has no
  marker, and removes it when it matches the current one; any other leftover is kept for
  you to inspect.
- Retiring or replacing an SMB3 credential also deletes its sealed mount file.
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
switch and SFTP credential handoff. A final browser walkthrough on the tracked
enabled build and the real two-slot missed-schedule check remain open.
