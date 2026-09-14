# Register VM storage repositories

## Status

This development workflow registers an existing storage source on a Nex Core host for VM
disks and ISO images. Supported repository types are **Local directory**, **NFSv4 export**,
**OpenZFS dataset**, **SMB3 share** and **iSCSI LUN**. Nex Core owns the registration,
connection details and credentials; Controller shows the state Core reports, and a rebuilt
Controller recovers the same repositories when the host reconnects.

Local directories, OpenZFS datasets and ordinary ext4/XFS iSCSI LUNs are assigned to one Core
host. NFSv4 and SMB3 repositories can be shared from a pool master to joined members (see
[Join an enrolled Nex Core to a pool](join-a-pool.md)). This is not live migration, HA,
snapshots or backups.

The clustered-GFS2 iSCSI mode is development-only: native acceptance remains open. Do not use it
for production workloads. Use an explicitly selected disposable lab host.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md), an enrolled, freshly
  connected host with managed-VM and storage-repository support, and matching Controller,
  Core and web builds. An older Core does not offer these repository types.
- Enough capacity for every VM disk's full size plus about 4 GiB headroom on its repository.
  Thin disks still need that capacity at creation and can later fill the backing storage.
  This build reports thick allocation for local, NFS and iSCSI repositories; use thin disks on
  a ZFS or SMB repository unless Core later reports thick capability for that source.
- Core and the QEMU service account must be able to traverse and write the selected source.
  Fix server-side ownership, ACLs and export/share access instead of making a repository
  world-writable.
- **Local directory:** an existing writable directory on ext4, XFS or Btrfs. It must not
  contain symlinks; its ancestors must be root-owned, not group/other writable, and allow
  QEMU traversal. Registration neither creates the directory nor formats a disk.
- **NFSv4 export:** an existing NFSv4 server and absolute export path, exported to every
  intended member Core with QEMU access. Nex uses a hard NFSv4 mount and verifies the server and
  filesystem identity; a changed server address or filesystem blocks use (no transparent server
  failover). Both the Core root identity and the QEMU service account must write. A root-squash
  export works only when its anonymous root mapping is explicitly the QEMU UID/GID.
- **OpenZFS dataset:** the matching Debian OpenZFS userspace tools and kernel module already
  installed and working on Core, and an existing imported, mounted filesystem dataset with its
  pool and reported mount point. Encrypted datasets must already have their key available at
  boot. Nex never creates, imports or upgrades a pool, mounts or unmounts the dataset, or changes
  pool or dataset properties. The mount point must be a clean absolute path without whitespace,
  control characters, `%` or `\`.
- **SMB3 share:** an existing SMB 3.1.1 server/share with encryption support, and an account and
  password with access for every intended member Core and QEMU. SMB1 and relaxed cache/locking
  modes are not supported. New registration needs permission to create and retain one hidden
  identity file at the share root.
- **Ordinary exclusive iSCSI LUN:** an existing target portal, target IQN, LUN, WWID and one ext4
  or XFS filesystem UUID. Restrict the target to this Core initiator only before registration.
  CHAP is optional and authenticates access; it does not make a LUN exclusive. Do not present a
  blank LUN, a LUN mounted elsewhere, multipath paths, or a LUN available to another writer. Use a
  separate target for each repository (one per target/portal session). Nex never formats the LUN.

## Steps

1. In **Resources**, select the pool for the selected Core host, then open **Storage**.
   The default **ISO** storage at `/var/lib/libvirt/images` may already be listed. It is a local
   directory registration and can hold both ISOs and VM disks.
2. Select **Register storage**. In **Register VM storage**, enter a **Name**, select the
   **Host**, then choose **Repository type**.
3. Browse the destination before entering paths manually:

   - **SMB3 share:** enter **SMB server**, **Account** and **Password**, then select
     **Browse shared folders**. The picker lists the server's shares for that account.
   - **NFSv4 export:** enter **NFS server**, then select **Browse exports**.
   - In either picker, select a row and **Select folder**, or double-click a row to open it.
     **Up** returns to the parent and **Refresh** lists it again. **Cancel** or Escape closes the
     picker without changing the form.
   - **iSCSI LUN:** enter **Target portal**, then **Discover targets** and select a target
     to fill its IQN. Discovery does not log into or mount a target.
   - Local directories and mounted ZFS datasets offer **Discover safe existing sources**.

   Discovery is read-only and never creates, formats or changes a source. It is not proof of
   writable VM access; registration performs those checks. Manual entry remains available.
4. Enter or confirm the existing source details for the selected type:

   - **Local directory:** enter its **Existing directory**.
   - **NFSv4 export:** confirm **NFS server**, absolute **Export path**, and optional existing
     directory within that export.
   - **OpenZFS dataset:** enter **Imported pool**, **Existing dataset** and its original
     **Reported mount point**. Core creates its own managed bind alias for VM disk paths; do not
     enter that alias in the form.
   - **SMB3 share:** confirm **Share** and optional existing directory, and keep **SMB server**,
     **Account** and **Password** filled. Core creates or reuses `.nex-storage-marker` at the
     share root. Do not rename, delete or replace that file.
   - **iSCSI LUN:** enter **Target portal**, **Target IQN**, **LUN**, **LUN WWID** and
     **Existing filesystem UUID**. Select **This LUN is restricted to this Core initiator only**.
     Enter **CHAP account** and **CHAP secret** only when the target requires CHAP. Core chooses
     the mount point.

5. Select **Register and check access**. Core connects or inspects the source, confirms its
   identity and tests Core and QEMU access. SMB and iSCSI credentials go to Core and are not shown
   in the repository table or **Task history**. Wait for `healthy`, **Readable**, **Writable** and
   a recent check time. `Unknown` or `unavailable` is not ready for VM disks.
6. To use the repository, open **Virtual machines** and select **Create VM**. On **Disks**, select
   it from **Storage**. Only healthy, writable, VM-disk-capable storage available on the placement
   host appears under **Select VM-ready storage**. An install ISO must be on storage used by one of
   the VM's disks; see [Create a virtual machine](create-virtual-machine.md).
7. Use **Recheck** after repairing a server, share, dataset, target or permission issue. A
   name-only **Update** (without new CHAP fields) does not reconnect and is allowed while in use.
   **Host** and **Repository type** stay fixed. SMB updates always include account and password.
   Changing a local path or any ZFS, NFS, SMB or iSCSI source detail reconnects the repository, so
   remove or complete every VM-disk, retained-disk and media dependency first.
8. To rotate SMB credentials, use **Update / rotate credentials**; for iSCSI CHAP, use **Update**
   with the new **CHAP account** and **CHAP secret**. First shut off every VM using the repository
   and settle pending disk, media and storage tasks; Nex does not stop VMs automatically. If an
   update is refused because the repository generation changed, reload, recheck and retry.
9. To remove an unused credential, in **Unused storage credentials** select its **Host**, then
   **Find unused credentials**. The table shows only opaque receipts, status and creation time.
   Select **Retire unused credentials**, review the receipt and confirm. Core rejects it if the
   receipt became attached in the meantime. Retiring never deletes repository data.
10. To stop using a local, NFS, SMB, ZFS or ordinary iSCSI repository, select **Detach**, type the
    exact **Repository name**, then select **Detach storage**. Core refuses while VM disks,
    retained disks or media depend on it. Detach removes Nex configuration and Nex-managed
    mount/session setup (for ZFS, only the bind alias). It never deletes data, formats a LUN,
    touches the original ZFS dataset, or removes `.nex-storage-marker`.

### Use NFS or SMB on a joined member

Register the NFS or SMB repository on the **pool master** only; do not register it again on a
member. The member's Core receives the master's record (SMB credentials move Core-to-Core) and
reports its own access. In **Pool storage repositories**, wait for that member to show a current
generation plus **Readable**, **Writable**, **QEMU writable** and **Locking** before VMs placed on
it can select the repository. A failed, stale or missing report is not overridable: fix the
member's export/share, identity, QEMU permissions, capacity or locking and wait for recheck.

The member's **pool-storage-reconciliation** health check is **unknown** before its first run or
when stale, **failed** after a failure, and **healthy** only after a fresh success. After the
master's management service restarts, member access shows **awaiting member reconciliation** until
new reports arrive; registrations and VM assignments are retained, so do not register again.

OpenZFS and ordinary exclusive iSCSI stay single-host. Never attach the same exclusive iSCSI
filesystem to another Core.

### Set up clustered iSCSI / GFS2 (development only)

Use **Resources → Storage → Clustered iSCSI / GFS2** only for shared block storage; the ordinary
**iSCSI LUN** form stays exclusive to one Core. In a selected disposable lab, provide:

- Three to five fresh, enrolled Cores in the pool that advertise clustered-iSCSI support, and a
  matching Controller (an older Controller does not show session-monitor readiness).
- One already-formatted GFS2 LUN with known portal, IQN, LUN number, WWID, filesystem UUID, lock
  table, and a journal for every eligible Core. Nex does not format the LUN.
- A separate Corosync address for every selected Core.
- A real, pre-authorized destructive `virsh` fence profile per Core: reachable fence SSH host,
  pinned SSH host key, SSH login/port/private key, and the target's exact libvirt domain UUID. The
  libvirt URI must be `qemu:///system` or
  `qemu+ssh://<same-SSH-login>@<same-fence-host>[:<same-SSH-port>]/system` (omit the port only for
  SSH port 22); passwords, queries, fragments and other paths are refused. A one-host known-hosts
  entry `[DNS-or-IPv4]:22` is treated as bare `DNS-or-IPv4`. BMC and SCSI persistent-reservation
  profiles are not available.

Steps:

1. Enter the target portal and use **Discover targets** to select the IQN. Discovery does not log
   in or find the LUN number, WWID or filesystem UUID; enter those from your storage inventory.
   Core validates them on every selected Core.
2. Enter the repository name and each Core's Corosync and fence values. Do not put a password in
   the private-key field, weaken host-key checking, use a hostname instead of the domain UUID, or
   point a profile at an unselected VM.
3. Submit **Core setup** and use **Refresh Core status**. Initial setup is not offered until the
   master returns current status; an already healthy cluster still accepts another repository.
4. For every target, use **Verify power fence**, review the external VM/domain target, and type the
   exact `FENCE <host-id>` confirmation. This powers off the target and is not retried
   automatically. For the pool master, another Core performs the proof, and the master must return
   and report before setup progresses.

`accepted`, `queued`, `awaiting_fencing`, `verifying_fence` and `applying` mean work remains; none
means ready. Setup completes only after every selected Core reports fresh health, membership and
fence proof. Only a Core-derived `healthy` is a readiness observation, and it is not HA, failover,
migration or hardware certification. After activation, **Degraded** means a fresh voter majority
including the master is healthy while some members are not; it does not permit another writer.
Without that majority Core reports **Needs attention** and resumes the same durable operation; it
does not return a fenced Core, restart a VM or repeat a fence. A successful **Verify power fence**
makes a healthy cluster **Degraded** until the fenced target reports again.

Clustered GFS2 has no update, credential rotation or detach: Core refuses them
(`clustered iSCSI updates require a coordinated Core cluster operation`, `clustered iSCSI detach
requires a coordinated Core cluster teardown`). There is no supported manual logout, unmount or
re-registration workaround.

Core manages the boot gate (`nex-cluster-storage-ready.service` before Pacemaker), the
`cluster-iscsi-session` monitor, and a Pacemaker policy that leaves a fenced Core off. These are
automatic. Do not run the Core CLI commands, edit the OCF resource or CIB, use Pacemaker commands
or failure-counter cleanup to return a fenced Core, or relax fencing. A CIB that differs from
Core's expected layout is left untouched and reported as needing attention.

If a pool-shared VM on this Core must be reduced while clustered storage is unavailable,
**Shutdown** and **Force stop** remain available for that locally journaled VM only. A frozen GFS2
path can still make them block or fail, and success is not proof the guest stopped; wait for a
fresh Core inventory rather than retrying a start or using native commands.

Pool-shared VM **Autostart** tries a shut-off VM at most once per OS boot, after fresh master
ownership and storage validation. It is not keepalive, HA or failover.

## Expected result

**Pool storage repositories** shows each repository's backend/source, **Accessible host**, Core
access state, capacity and check time, plus each member's access for master-owned NFS/SMB storage.
A healthy repository can store VM disks and ISO images on its eligible hosts. Core rechecks
identity and access before disk, media and guest starts, so a disconnected share, changed mount,
read-only filesystem or failed permission/lock check blocks new writes and starts.

If the assigned Core is unavailable, access and capacity are labelled **last report** and lifecycle
actions and ISO destinations are withheld until Core reports fresh access again.

Controller loss does not stop resident VMs or authorize another host to write the storage. After a
Controller replacement, reconnect the same Core; do not register the repository or re-enter
credentials unless Core reports a rotation is needed.

Core periodically retries an interrupted storage task and updates **Task history** with its outcome.
A terminal `failed` task is not retried; fix the cause and submit a new request.

## Troubleshooting

- **Pool storage reconciliation health check fails:** read its stage and outcome. A deadline does
  not prove storage is lost or VMs stopped; Core retries automatically. Master-list/client stages
  point to pool connectivity or trust, repository stages to storage identity/access, local
  observation or orphan detachment to that member's storage or retained dependencies. Do not delete
  and recreate a repository or weaken fencing to clear it.
- **A shared VM start is refused by the pool master:** ensure the master and members report current
  inventory and the owning host is known; an upgraded pool must reconcile ownership first. Do not
  use native autostart, duplicate definitions or a second writable copy. A timeout or unreachable
  host never proves the original VM stopped.
- **A remote-storage VM creation is uncertain after a restart:** Core settles it only when the
  defined, stopped VM matches the master's record; otherwise it stays **Needs attention**. Core
  does not recreate, start or delete it. Do not native-start it or define another copy.
- **A shared VM update remains at Needs attention:** Core commits the next revision only when its
  journal and the master agree. Do not retry a changed update, edit the master record, or native
  start/reboot to clear it; wait for Core to reconcile. A known failed update can be aborted.
  A VM that has used pool-shared storage stays pool-controlled even after its shared disk is removed.
  Shared media changes are refused.
- **The Host list is empty or registration is rejected as unsupported:** reconnect or upgrade the
  selected Core, then use matching Controller, Core and web builds.
- **Clustered storage capability negotiation is pending (503):** a temporary reconnect state. Wait
  for the next Core report, refresh status, then retry the same action. Do not re-enter
  credentials. A Core that stays unsupported needs reconnecting or upgrading.
- **Clustered status is stale, blocked or Needs attention with a DLM/stonith library mismatch:**
  install the matching supported Debian DLM/Pacemaker package set and wait for Core to revalidate.
  Do not add a compatibility symlink, substitute a library, run the helper by hand, or redo a fence.
- **No fence response reached the browser:** Nex marks that target's outcome unknown and disables
  it. Do not resubmit; a successful **Refresh Core status** clears the block. If Core then reports
  `failed` or `uncertain`, review it and use **Reverify power fence**, which is a new destructive
  request with its own confirmation. A pending `unverified` proof stays disabled.
- **Browsing fails or returns no folders:** check server reachability and account permissions. For
  SMB, correct the credentials and browse again; for NFS, the server must advertise exports. An
  empty result is not proof the server has no storage. Listings stop at 64 results; enter a known
  path manually if needed. SMB names with internal spaces and `$` are supported; other
  unsupported names are not offered. A stale or unsupported Core cannot browse.
- **Registration or Recheck fails:** read the Core error in the row and **Task history**. Check
  reachability, source identity, filesystem health, capacity, mount permissions and QEMU access,
  then **Recheck**. Do not create an empty local directory at an unavailable remote mount point.
- **A storage task is pending or needs attention:** fix the reported cause and wait for **Task
  history**. Do not register again, rotate or detach meanwhile. Then **Recheck**.
- **NFS access fails:** confirm NFSv4, server/export path and UID/GID or root-squash policy. Do not
  switch to a soft mount. If server or filesystem identity changed, restore the original source.
  Earlier registrations without a filesystem ID stay unavailable.
- **ZFS access fails:** confirm the pool is imported and `ONLINE`, the dataset is mounted at the
  reported mount point, the module/tools work on the running kernel, and there is capacity. Unlock
  encrypted datasets through boot configuration; do not force-import or change pool settings.
- **SMB access fails:** confirm share, account/password, transport-encryption policy, permissions
  and locking. Passwords are not recoverable from Controller; rotate them with an update. A missing
  or changed `.nex-storage-marker` fails safely: restore it from a trusted copy, then **Recheck**.
  Do not recreate it or detach/re-register a repository with VM disks.
- **iSCSI access fails:** verify portal, IQN, LUN, WWID and filesystem UUID, that the target ACL
  allows only this Core, and that the filesystem is not mounted elsewhere. Do not use `/dev/sdX`,
  format the LUN, or attach a second initiator.
- **A VM cannot select or start on storage:** wait for a fresh `healthy`, readable, writable result
  on the placement host, check capacity including headroom, and place the ISO on one of the VM's
  disk repositories.
- **Detach is refused:** remove or complete the listed VM/media dependency, or wait for Core to
  reconcile an uncertain operation. Do not register the same source under another name.
- **A master NFS/SMB update, rotation or detach is refused for a member workload:** a member still
  references the repository for a VM disk or ISO. Remove that dependency and wait for the member to
  release it. Do not force a change or detach the share on the member.
- **Credential rotation or retirement is refused:** for rotation, shut off every VM using the
  repository and settle pending operations. For retirement, refresh **Unused storage credentials**
  and retry only if the receipt is still listed.
- **An APT removal or downgrade is refused:** a host with version-2 repository records, credential
  receipts or tombstones needs a matching storage-repository package. Restore it. A manual `dpkg`
  transaction bypasses this protection and is unsupported.

## Verification

Local, NFS, SMB, ZFS and iSCSI registration, guest write/reboot, outage recovery and Controller
replacement are recorded in [storage repository checks](../qa/storage-repositories/checks.md). The
SMB/NFS folder picker is covered in [network picker verification](../qa/2026-09-12-network-storage-picker.md),
the two-Core NFS/SMB member workflow in
[bug-ledger lab evidence](../qa/2026-09-12-bug-ledger.md#bug-0094-primary-native-pool-walkthrough),
and clustered GFS2 in [concurrent storage verification](../qa/2026-09-12-concurrent-pool-storage.md).
Untested: failed-fence admission, broader outages/adoption, HA/failover, migration, physical hardware
and the unavailable-Core browser presentation.

[Back to the user guide](README.md)
