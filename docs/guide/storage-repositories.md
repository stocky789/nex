# Add VM storage repositories

## Status

This development workflow registers an existing storage source or provisions a new local
ZFS repository from explicitly selected disks on Nex Core. Registered sources can hold VM
disks and ISO images according to their reported capabilities; newly provisioned ZFS
repositories currently hold VM disks only. An existing OpenZFS dataset is a separate
registration path and can hold ISO images when Core reports that capability. Supported repository types are **Local directory**, **NFSv4 export**,
**OpenZFS dataset**, **SMB3 share** and **iSCSI LUN**. Nex Core owns the registration,
connection details and credentials; Controller shows the state Core reports, and a rebuilt
Controller recovers the same repositories when the host reconnects.
Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

Local directories, existing OpenZFS datasets and ordinary ext4/XFS iSCSI LUNs are assigned
to one Core host. A provisioned ZFS repository can cover every configured host, with separate
local disks on each configured host. Its data is not shared, and moving a VM between these
hosts would require a disk copy; that migration workflow is not part of this feature. NFSv4 and SMB3 repositories can be shared from a pool master to joined members (see
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
- **New ZFS repository:** matching Core with ZFS provisioning support, working OpenZFS tools
  and kernel module, and explicitly selected disposable lab disks. The distribution recipe
  includes OpenZFS; exact-image and hardware acceptance remains open. System, mounted and
  in-use disks cannot be selected. Existing data requires an exact typed device confirmation.
- **Existing OpenZFS dataset:** an already imported, mounted filesystem dataset with its
  pool and reported mount point. Encrypted datasets must already have their key available at
  boot. This advanced registration path never creates, imports or upgrades a pool, mounts or
  unmounts the original dataset, or changes pool or dataset properties. The mount point must be a clean absolute path without whitespace,
  control characters, `%` or `\`.
- **SMB3 share:** an existing SMB 3.1.1 server/share with encryption support, and an account,
  optional domain/workgroup, and password with access for every intended member Core and QEMU. SMB1 and relaxed cache/locking
  modes are not supported. New registration needs permission to create and retain one hidden
  identity file at the share root.
- **Ordinary exclusive iSCSI LUN:** an existing target portal, target IQN, LUN, WWID and one ext4
  or XFS filesystem UUID. Restrict the target to this Core initiator only before registration.
  CHAP is optional and authenticates access; it does not make a LUN exclusive. Do not present a
  blank LUN, a LUN mounted elsewhere, multipath paths, or a LUN available to another writer. Use a
  separate target for each repository (one per target/portal session). Nex never formats the LUN.

## Steps

1. In the top-bar scope menu, choose the pool for the selected Core host, then select **Storage**
   in the rail (`#/storage`; the older link `#/settings/storage` opens the same screen). The
   **Storage** table shows each repository's **Type**, **Scope**, **Capacity**, **Health**,
   **Host access** (one light per pool host: **Accessible**, **Degraded**, **Pending**,
   **No access**, **Not eligible**, **Error**, or a hollow **Unknown** when that host is not
   reporting current access), **Last check**, and the **Manage** buttons. Point at a light for the
   host's own detail; select it to open that host. **About storage**, below the table, explains
   the two ways to add storage and when a member may use shared storage.
   The host's own **Local storage** registration at `/var/lib/libvirt/images` may already be
   listed. It is a local directory registration and can hold both ISOs and VM disks. A host
   that first connected to an earlier Core keeps the name it recorded then, usually **ISO**;
   that record belongs to the host and is never renamed by an upgrade, so rename it yourself
   with **Update** if you want to.
2. Select **Register storage**. In **Register VM storage**, enter a **Name** and choose
   **Repository type**. For local directory, ZFS and exclusive iSCSI storage, then select the
   **Host** that owns it. Shared NFS, SMB and clustered iSCSI storage has no host choice: the form
   shows the **Pool master**, which records the repository, and every host in the pool receives it.
3. Browse the destination before entering paths manually:

   - **SMB3 share:** enter **SMB server**, **Account**, optional **Domain**, and **Password**, then select
     **Browse shared folders**. The picker lists the server's shares for that account. Folders that
     already have a Nex repository marker show **E**.
   - **NFSv4 export:** enter **NFS server**, then select **Browse exports**.
   - In either picker, select a row and **Select folder**, or double-click a row to open it.
     **Up** returns to the parent and **Refresh** lists it again. **Cancel** or Escape closes the
     picker without changing the form, including while folders are loading. A late discovery
     reply must not reopen it; select the browse button again to start a new request.
   - **iSCSI LUN:** enter **Target portal**, then **Discover targets** and select a target, or
     select **Inspect LUNs** to inspect the typed target again. Inspection never runs while you
     type. Core inspects that target's LUNs without mounting or formatting them. A single data LUN
     is selected automatically; for several LUNs, select the intended one. Nex fills the
     actual **LUN**, **LUN WWID** and **Existing filesystem UUID** that Core can detect,
     including LUN **0**. A missing identity stays blank; example placeholders are not values.
     A blank, unsupported or ambiguous filesystem is reported, not created or repaired.
     Enter **CHAP account** and **CHAP secret** before selecting the target if it requires them;
     both are needed, and they are sent only for that selected-target inspection, never for
     listing targets. Editing CHAP later keeps the saved LUN identities; changing the portal or
     target clears them. Inspection logs in as a second session with this host's own initiator
     name; some single-session targets may reject or disturb it, in which case enter the values
     manually. A target reporting more than 64 LUNs is refused; enter the values manually.
     Autofill requires an updated Core advertising iSCSI inspection support; older Core hosts
     still support target discovery and manual entry.
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
   - **SMB3 share:** confirm **Share**. Pick a nested folder in **Browse shared folders** instead of
     typing a path; the selected relative folder appears under **Share**. Keep **SMB server**,
     **Account**, optional **Domain** and **Password** filled. Core creates or reuses `.nex-storage-marker` at the
     share root. Do not rename, delete or replace that file.
   - **iSCSI LUN:** enter **Target portal**, **Target IQN**, **LUN**, **LUN WWID** and
     **Existing filesystem UUID**. Select **This LUN is restricted to this Core initiator only**.
     Enter **CHAP account** and **CHAP secret** only when the target requires CHAP. Core chooses
     the mount point.
   - After autofill, explicitly confirm the exclusive-initiator restriction for ordinary
     ext4/XFS storage. Detection cannot prove that another initiator lacks write access.
     A new, unformatted target has no existing filesystem UUID and cannot be registered as
     an existing filesystem. Discovery never formats it; prepare it separately only after
     verifying the exact target and accepting the resulting data-loss risk.

5. Select **Register and check access**. Core connects or inspects the source, confirms its
   identity and tests Core and QEMU access. SMB and iSCSI credentials go to Core and are not shown
   in the repository table or on **Tasks**. Wait for **Health** to read **Healthy**, the host's
   **Host access** light to read **Accessible** and a recent **Last check**. **Unknown** or
   **Unavailable** is not ready for VM disks.
6. To use the repository, select **VMs** in the rail and select **Create VM** below the list. On **Disks**, select
   it from **Storage**. Only healthy, writable, VM-disk-capable storage available on the placement
   host appears under **Select VM-ready storage**. An install ISO can live on a different
   repository from the VM's disks; see [Create a virtual machine](create-virtual-machine.md).
7. Use **Recheck** after repairing a server, share, dataset, target or permission issue. If an
   NFS or SMB repository is not mounted, for example because Core started before the file server,
   Core retries the mount about once a minute and **Recheck** retries it immediately. If its mount
   root has a stale file handle and no disk, retained disk or ISO uses it, **Recheck** cycles the
   Nex-managed mount unit; it names the dependency instead of remounting while anything uses it. A
   name-only **Update** (without new CHAP fields) does not reconnect and is allowed while in use.
   **Host** and **Repository type** stay fixed. SMB updates always include account, optional domain and password.
   Changing a local path or any ZFS, NFS, SMB or iSCSI source detail reconnects the repository, so
   remove or complete every VM-disk, retained-disk and media dependency first.
8. To rotate SMB credentials, use **Update / rotate credentials**; for iSCSI CHAP, use **Update**
   with the new **CHAP account** and **CHAP secret**. First shut off every VM using the repository
   and settle pending disk, media and storage tasks; Nex does not stop VMs automatically. If an
   update is refused because the repository generation changed, reload, recheck and retry.
9. To stop using a local, NFS, SMB, ZFS or ordinary iSCSI repository, select **Detach**, type the
    exact **Repository name**, then select **Detach storage**. Core refuses while VM disks,
    retained disks, active work or media that has not been removed depend on it. Detach removes Nex configuration and Nex-managed
    mount/session setup where applicable, including any SMB or iSCSI credentials Core held for that
    repository. For ZFS it unregisters Nex storage while
    leaving the native pool and dataset intact. It never deletes data, formats a LUN,
    touches the original ZFS dataset, or removes `.nex-storage-marker`. **Detach** is disabled
    for the host's automatic `/var/lib/libvirt/images` repository, even if it has been renamed.
    The row says **Host default; cannot detach**. **Update** still permits renaming that
    repository, but its directory cannot be changed in Controller. The registration stays
    available for local VM disks and ISOs through this workflow.

### Provision local ZFS storage from selected disks

Use matching development builds on disposable lab hosts. The
[verification report](../qa/2026-09-19-zfs-provisioning.md) distinguishes tested paths from
open native and image-delivery checks.

1. With the pool chosen in the scope menu, select **Storage** in the rail and select **Add ZFS storage**. The **Add ZFS storage**
   dialog opens on **Choose disks** and asks every connected host for its disks.
2. Enter a **Storage name**, then tick the hosts to include. Ticking several hosts, or the
   only host of a one-host pool, gives each configured host an independent local zpool under
   the same repository name, and other hosts can be added to it later. Ticking one host of
   several creates storage for that host only. Neither choice makes disks shared.
3. Under each ticked host, tick only the disks intended for this repository. Each row shows
   size, type, model and serial; **Has old data — will be erased** marks a disk that needs a
   typed confirmation on the next screen. Open **unavailable disks** to see disks Core
   refuses, with its reason; they cannot be ticked, and a confirmation cannot override an
   in-use or system-device refusal. To leave a host for later, keep it ticked and tick none
   of its disks. Untick a host only when the storage should never include it: with one host
   left ticked, the storage belongs to that host alone and no other host can join it later.
   A newly joined host needs an explicit later disk selection; Nex never copies
   another host's selection to it. Use **Add this host** in **ZFS storage by host** to add
   disks for that host under the existing repository identity; the dialog opens for that
   host only and is not offered for hosts Core has already configured for the repository.
   If one host cannot report its devices, its message appears under that host and it is
   left as it is while the other hosts remain selectable. **Refresh disks** asks Core again.
   Once three or more data disks are ticked — enough that holding one back still leaves a
   redundant pool — a **Spare** column appears. Tick it on a disk to
   make that disk a hot spare instead of a data disk. A hot spare sits
   idle in the pool until a data disk fails, and ZFS then resilvers onto it by itself without
   waiting for anyone (one sentence of help says the same beside the list). A spare is erased exactly like any other selected disk, needs the same
   typed confirmation when it holds old data, and counts toward no layout: the data disks
   alone have to satisfy the layout, so two data disks and a spare is still a two-disk mirror
   with a spare, not a three-way mirror. Each Spare tick needs three data disks still ticked on
   that host; otherwise the wizard shows **Keep at least two data disks** and refuses the spare.
   Unticking a data disk after marking a spare clears the spare again if fewer than two data disks
   would remain.
4. Check the summary under each host. It follows the ticks as they change and reads, for
   example, `3 data disks + 1 hot spare → RAIDZ1, ~16 GiB usable, survives 1 disk failure`.
   The usable figure is the plain arithmetic on the smallest ticked disk, so it is shown as
   approximate: ZFS's own overhead is not in it. **Layout** lists every layout with one line
   each, and **Automatic** is marked as Core's default for the number of disks ticked: a
   single disk for one device, a mirror for two, and RAIDZ1 for three or more. A layout the
   ticked disks cannot build stays in the list with what it needs instead — a mirror of two
   **or three** disks, RAIDZ1 from three, striped mirrors from an even number of four or
   more, RAIDZ2 from four and RAIDZ3 from five — so nothing disappears without a reason. When
   the ticked disks allow only one layout, the page says so instead of offering a choice of
   one. A three-disk mirror survives two failed disks and is also the way to add a disk to a
   mirror before removing a suspect one. Spares are not counted here. A choice that no longer
   fits the ticked disks returns to **Automatic**.
5. Select **Review and erase →** and review every selected device and its host. For old
   data, type the exact displayed by-id name. Select **Erase disks and create storage** only
   after verifying the list: provisioning destroys data on the selected devices. Core checks
   identity and eligibility again before acting.
6. Follow the operation and each host's outcome. A healthy configured host can use its
   own local repository; a failed or unconfigured host cannot. Wait for fresh Core access
   and capacity before creating a VM. ZFS supports thin VM disks; thick allocation stays
   unavailable.

Core creates its own pool name and `vms` filesystem dataset with a Core-owned mountpoint,
4 KiB alignment, LZ4 compression, access-time updates disabled and extended attributes
stored in system attributes. Synchronous writes remain enabled. The advanced **Use an
existing dataset** path retains the registration procedure above.

The pool master carries out **Update**, **Recheck** and **Detach** for a provisioned repository.
**Update** and **Detach** act on the whole repository and Core refuses them when aimed at a
member; **Detach** unregisters every configured host, and **Recheck** checks the selected
configured host. For a provisioned repository, **Update** can only change the repository name —
device selection, layout and pool settings are fixed once provisioned; add disks for a host that is not
set up yet with **Add this host** instead. Core refuses these actions while any configured host has
no successful provisioning outcome ("hosts that are not currently configured"); a repository
with a host whose provisioning ended in **Needs attention** therefore cannot yet be detached
from Nex, which is a recorded open limitation.

If the browser loses a reply, retry the same unchanged request; do not start a second
provisioning operation over the same devices. Resubmitting the same hosts, devices, name and
layout repeats the same request, including after stepping back or reopening the dialog; changing
any of them makes a new request. A reopened dialog starts again at **Choose disks** with nothing
ticked, asks Core for current devices, and never remembers a typed erase confirmation: tick the
disks and type it again before **Erase disks and create storage** becomes available. Core continues accepted work without the
browser or Controller. An ambiguous destructive step stays **Needs attention** until it
can be safely reconciled; it is not permission to erase again. **Detach** unregisters
storage and keeps the zpool and data intact.

#### When a disk in a provisioned ZFS repository fails

A mirror or RAIDZ pool that loses a disk keeps working. The repository's host row in the
**Storage** section shows **Pool degraded**, the failed device by its by-id name and its state
(for example `offline`, `faulted` or `unavail`), and the scrub or resilver that is running,
with the percentage it has finished and how much of the pool it has examined. The same line
appears on the host's row under **ZFS storage by host**, beside the **Replace disk** button
that repairs it — or **resilvering** / **being replaced** in that Action cell while a
resilver or in-flight replacement is already under way, so a second submit is not offered.
Nex Core reports this from the pool itself; it is what `zpool status` on
that host shows.

A registered OpenZFS dataset (Register storage, not Add ZFS storage) shows the same pool
line on its repository row under **Health**, because that path has no per-host ZFS
table.

The repository stays readable and writable while it is degraded. Running VMs keep running,
their disks keep being written, and new VMs can still be created and started on it. A single
disk repository has no redundancy: a lost disk there makes the repository unavailable
instead, as does a pool that ZFS reports as faulted, unavailable, removed or suspended.

Degraded means the redundancy the repository was built for is now spent. Replace the disk
promptly: a second failure before the pool is repaired loses the data. Fit a replacement of
at least the same size in the affected host and follow **Replace a failed disk** below. Once
the resilver finishes, the host row returns to **Pool online**.

If the repository was built with a hot spare, ZFS has already started resilvering onto the
spare by itself; the host row shows that resilver. Replace the failed disk anyway, so the
spare goes back to being a spare.

#### Replace a failed disk

Do this on the host whose row reports the failure. The repository stays in service while it
runs, and the work belongs to Nex Core: it carries on if the browser is closed or the
Controller is unreachable.

1. Fit the replacement disk in that host. It must be at least as large as the disk it
   replaces. Nothing else has to be prepared: the disk is erased as part of the replacement.
2. With the pool chosen in the scope menu, select **Storage** in the rail and scroll to **ZFS storage by host** for that repository.
   Find the host row that reports **Pool degraded** and select **Replace disk** in its
   **Action** column. The button is only on that panel, and only while the host is freshly
   connected, its pool observation is current, and it reports ZFS replace and storage discovery.
   A row that still shows an older pool line is prefixed **Last report** and does not offer
   **Replace disk**.
3. Choose the failed disk. Nothing is chosen for you, even when only one disk has failed. It
   is named by the same by-id name the host row reports, with the state ZFS gives it. A disk
   ZFS still reports as ONLINE is not offered at all — if the disk you expected is missing from
   the list, the pool does not consider it failed; check the pool before going further. An
   ONLINE leaf that somehow reaches submit is refused with the other **Needs attention**
   messages.
4. Choose the replacement from the host's disk list. It shows the same size, type, model and
   serial as the provisioning wizard, and refuses the same disks for the same reasons — a
   disk in use, a system device, or one the host cannot observe safely.
5. If the replacement is marked **Has old data — will be erased**, type its exact displayed
   by-id name to confirm. The confirm button names the disk it erases — **Erase
   _disk_ and replace** — so check that name against the disk you fitted before selecting it.

**What happens next.** Nex Core checks both disks against the host again, clears the
replacement and hands it to ZFS. The repository's host row then shows a **resilver** with how
much of the pool it has copied. A resilver takes as long as the data on the pool needs —
minutes for a nearly empty pool, many hours for a full one — and it is not something to wait
in front of. VMs on the repository keep running and keep being written to throughout, and
nothing is lost if the browser or the Controller goes away.

When the resilver finishes, ZFS removes the old disk from the pool itself. Nothing has to be
detached by hand, and the host row returns to **Pool online**. The old disk can then be taken
out of the host.

If the operation ends in **Needs attention**, read its message before retrying: it says what
Nex Core found instead — the disk is no longer part of the pool, it is online, it is already
being replaced, the replacement is already part of the pool, it is too small, the
replacement changed or is in use since it was picked, or it holds old data that was not
confirmed. Nothing was erased in those cases. Two other messages can only appear after the
replacement disk was cleared: **the replacement disk could not be cleared**, and
**… could not be replaced with …**. In those cases the disk has been wiped, the pool is
unchanged, and retrying the same replacement is what finishes it. Retrying the same
replacement is safe when the pool is already resilvering onto the replacement: that case is
recognized rather than started again.
While the host row's Action cell shows **resilvering** or **being replaced**, **Replace disk**
is not offered.

### Use NFS or SMB on a joined member

Register the NFS or SMB repository on the **pool master** only; do not register it again on a
member. The member's Core receives the master's record (SMB credentials move Core-to-Core) and
reports its own access. On **Storage**, wait for that member's **Host access** light to read
**Accessible** — Core has checked the current registration there and found it readable, writable,
QEMU-writable and locking — before VMs placed on it can select the repository. **Pending** means the
member has not checked the current registration yet. A failed, stale or missing report is not overridable: fix the
member's export/share, identity, QEMU permissions, capacity or locking and wait for recheck.
Only current **ready** pool members with access reported for the current repository
generation are eligible. An old successful observation does not make a joining,
ejecting, failed or removed member a usable target.

ISO library entries imported onto that shared repository also reach joined members after the
repository is healthy there: the member verifies the file digest and reports access. Import the
ISO on the master only; do not upload the same ISO again on the member.

The member's **pool-storage-reconciliation** health check is **unknown** before its first run or
when stale, **failed** after a failure, and **healthy** only after a fresh success. After the
master's management service restarts, member access shows **awaiting member reconciliation** until
new reports arrive; registrations and VM assignments are retained, so do not register again.

The pool-scoped provisioned-ZFS aggregate is local on each configured host, with different data and
capacity on each host. Existing-dataset ZFS and ordinary exclusive iSCSI stay single-host.
Never attach the same exclusive iSCSI filesystem to another Core.

### Set up clustered iSCSI / GFS2 (development only)

Use **Storage → Clustered iSCSI / GFS2** (pool chosen in the scope menu) only for shared block storage; the ordinary
**iSCSI LUN** form stays exclusive to one Core. The panel sits folded at the bottom of the
**Storage** section until Core reports a configuration or a problem; select its heading to open
it. In a selected disposable lab, provide:

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

1. Enter the target portal and use **Discover targets** to select the IQN. On a Core with
   read-only LUN inspection support, Nex fills a single detected LUN automatically or offers
   **Detected LUN** when several are available. Confirm the intended GFS2 LUN, WWID and
   filesystem UUID; missing values require verified storage inventory, not the placeholders.
   Inspection does not prove fencing, quorum or writable access. Core validates the selected
   identities independently on every selected Core.
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
A healthy repository can store the resource types shown by its capabilities on eligible
hosts. Newly provisioned ZFS repositories hold VM disks; select another ISO-capable
repository for installation media. Core rechecks
identity and access before disk, media and guest starts, so a disconnected share, changed mount,
read-only filesystem or failed permission/lock check blocks new writes and starts.

If the assigned Core is unavailable, access and capacity are labelled **last report** and lifecycle
actions and ISO destinations are withheld until Core reports fresh access again.

Controller loss does not stop resident VMs or authorize another host to write the storage. After a
Controller replacement, reconnect the same Core; do not register the repository or re-enter
credentials unless Core reports a rotation is needed.

Core periodically reconciles interrupted storage tasks when it can prove continuation is
safe, and updates **Tasks** with the outcome. For ZFS provisioning, an ambiguous
erase or changed device remains **Needs attention**; automatic recovery never authorizes
another erase. Inspect the reported cause before taking further action.
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
  unsupported names are not offered. A stale or unsupported Core cannot browse. An **E** badge
  marks a folder that already has a Nex repository marker.
- **Registration or Recheck fails:** read the Core error in the row and on **Tasks**. Check
  reachability, source identity, filesystem health, capacity, mount permissions and QEMU access,
  then **Recheck**. Do not create an empty local directory at an unavailable remote mount point.
- **`remote storage is not mounted`:** the NFS or SMB server or share was unreachable when Core
  tried to mount it. Restore the server or network; Core retries about once a minute, or select
  **Recheck** to retry now. Core also mounts a registered repository again if its mount was stopped by
  hand, so to keep a repository offline, detach it.
- **A storage task is pending or needs attention:** fix the reported cause and wait for the result
  on **Tasks**. Do not register again, rotate or detach meanwhile. Then **Recheck**.
- **NFS access fails:** confirm NFSv4, server/export path and UID/GID or root-squash policy. Do not
  switch to a soft mount. If server or filesystem identity changed, restore the original source.
  Earlier registrations without a filesystem ID stay unavailable.
- **ZFS access fails:** confirm the pool is imported, the dataset is mounted at the
  reported mount point, the module/tools work on the running kernel, and there is capacity. Unlock
  encrypted datasets through boot configuration; do not force-import or change pool settings.
  A `DEGRADED` pool is not an access failure: it stays in service, and the host row names the
  failed device to replace.
- **SMB access fails:** confirm share, account, domain/workgroup, password, transport-encryption policy, permissions
  and locking. Passwords are not recoverable from Controller; rotate them with an update. A missing
  or changed `.nex-storage-marker` fails safely: restore it from a trusted copy, then **Recheck**.
  Do not recreate it or detach/re-register a repository with VM disks.
- **No iSCSI targets were returned:** confirm the target service is enabled and advertised on the
  selected portal, and that the Core initiator is allowed by its discovery ACL. An empty list
  does not identify a LUN or prove that a target's data is missing.
- **A target was found but its LUN identities are incomplete:** verify its LUN mapping and
  initiator ACL, then select the target again, or select **Inspect LUNs**, to inspect it afresh. For CHAP, enter the username
  and password before selecting the target; read-only inspection accepts passwords up to
  255 UTF-8 bytes. A Core without inspection support keeps the manual fields available.
- **No filesystem UUID was detected:** a blank LUN, an unsupported filesystem or several
  filesystem signatures cannot supply a safe registration identity. Inspection never formats
  the LUN. Supply an independently verified existing identity or prepare the storage separately;
  do not invent a UUID or confirm exclusive access merely because inspection succeeded.
- **Inspection was cancelled or failed:** manual entries remain editable. Selecting the same
  discovered target again, or **Inspect LUNs**, starts a fresh inspection. Editing an existing registration's name
  leaves its saved identities unchanged unless you deliberately change its source.
- **The target reports only an EUI device identifier:** this profile needs a compatible native
  LUN WWID. Inspection leaves it missing rather than filling an unusable value. Check whether
  the target can report a stable NAA device identity; do not invent a WWID.
- **iSCSI access fails:** verify portal, IQN, LUN, WWID and filesystem UUID, that the target ACL
  allows only this Core, and that the filesystem is not mounted elsewhere. Do not use `/dev/sdX`,
  format the LUN, or attach a second initiator.
- **A VM cannot select or start on storage:** wait for a fresh `healthy`, readable, writable result
  on the placement host, check capacity including headroom, and place the ISO on one of the VM's
  disk repositories.
- **A URL ISO download was refused:** read the reason in the **ISO library** row (on **Storage**,
  below the repositories) and on **Tasks**. Core reports why it refused, and never repeats the URL,
  because a download link can carry a credential. `ISO download refused this server's redirect from HTTPS to plain HTTP; use a
  direct HTTPS link to the ISO` means the mirror answers its HTTPS link with a redirect to plain
  HTTP; Core never follows that downgrade, so use the mirror's direct HTTPS file URL instead of a
  "latest" or "stable" alias. The other refusals name themselves too: more than five redirects, a
  redirect target that is not a public HTTP or HTTPS URL without credentials, a destination that is
  not a public address, or a DNS or connection failure. Only `ISO download failed: connection,
  redirect or timeout` means an unexplained network fault; check the host's own connectivity.
- **A failed ISO import stays in the library:** for a `failed` or `needs_attention` row, select
  **Discard failed import** and confirm. This removes the failed import record and any Controller
  staging bytes; the task and audit history of what happened are kept. Then submit the transfer or
  download again, which starts a new import. An ISO a host actually holds cannot be
  removed this way: it is Core's record and file, so Controller refuses with `this ISO is stored on
  its host; Nex Core cannot remove imported media yet`, and a removal attempted directly in the
  Controller database reappears at the host's next report. Deleting the file on the host by hand is
  not supported. An import that is still `queued` or `dispatched` belongs to its host task; wait for
  the task to end before removing the entry.
- **A registration the host refused is not listed:** a repository registration whose task shows
  **failed** was never created anywhere, so no repository row is kept for it. Read the reason in
  **Tasks**, fix the cause and register again. Re-sending the same request returns the same failed
  result; a new registration uses a new request.
- **Detach is refused:** remove or complete the listed VM/media dependency, or wait for Core to
  reconcile an uncertain operation. Do not register the same source under another name.
- **A master NFS/SMB update, rotation or detach is refused for a member workload:** a member still
  references the repository for a VM disk or ISO. Remove that dependency and wait for the member to
  release it. Do not force a change or detach the share on the member.
- **Credential rotation is refused:** shut off every VM using the
  repository and settle pending operations, then retry.
- **An APT removal or downgrade is refused:** retained repository state needs a matching
  storage-capable package. Hosts with ZFS provisioning records and Controllers managing
  ZFS-provisioning Cores require the newer ZFS-capable package. Restore that matching build;
  do not delete journals to bypass the guard. A manual `dpkg` transaction bypasses this
  protection and is unsupported.

## Verification

Read-only iSCSI LUN detection was exercised against real userspace targets with disposable
file-backed ext4, XFS and GFS2 media, and both storage forms were walked through in Chromium
with simulated API replies. Single/multiple selection, incomplete identities, CHAP, cancellation,
manual fallback, failed-submit draft preservation and existing name-only updates were checked.
See [iSCSI LUN detection verification](../qa/2026-10-03-iscsi-lun-autofill.md) for actual native
results, inspected captures and limits; no production NAS or installed-host upgrade is claimed.

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

**Replace a failed disk** was walked through against a running build in a disposable lab; see
[replacing a failed disk in a provisioned ZFS pool](../qa/2026-09-22-zfs-replace.md). The
API path of the mirror replacement was observed with output retained. Three-way mirror and
hot-spare checks were run but retained no evidence. The reworked disk tables, Spare column,
layout fieldset, live summary, degraded host row and confirm button have not been seen in a
browser. RAIDZ3 provisioning, automatic spare engagement and the post-`zpool replace`
interrupted retry remain outstanding.

ZFS disk provisioning has separate [rollout verification](../qa/2026-09-19-zfs-provisioning.md).
Its acceptance remains open; earlier existing-dataset checks do not prove disk provisioning,
pool-wide recovery or the new image delivery path.

The Escape/discovery race and immediate reopening were exercised in Chromium against
the built UI with simulated Core replies; see [storage dialog workflow verification](../qa/2026-09-18-storage-dialog-workflow.md).

Local, NFS, SMB, ZFS and iSCSI registration, guest write/reboot, outage recovery and Controller
replacement are recorded in [storage repository checks](../qa/storage-repositories/checks.md). The
SMB/NFS folder picker is covered in [network picker verification](../qa/2026-09-12-network-storage-picker.md)
and the SMB domain/existing-folder picker in
[SMB domain and existing-folder picker](../qa/2026-09-14-smb-domain-existing-folder.md),
the two-Core NFS/SMB member workflow in
[bug-ledger lab evidence](../qa/2026-09-12-bug-ledger.md#bug-0094-primary-native-pool-walkthrough),
and clustered GFS2 in [concurrent storage verification](../qa/2026-09-12-concurrent-pool-storage.md).
The disposable Controller walkthrough confirmed the HTTPS-downgrade refusal, discard of its failed
import with task history retained, and the upload banner changing to its live host-ready state.
See [final ISO and Controller acceptance](../qa/2026-09-16-final-iso-controller-acceptance.md).
The [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) (`0.2.10~gate5`, 2026-09-17, disposable
nested-KVM lab, headless Chromium, row 2) walked steps 2–4 in the browser for local directories:
two missing paths failed with their reason, a new directory went from queued to healthy in 6 s,
and a 75 s watch of the table saw no duplicate or stale queued row. The registrations survived a
Controller replacement (failed ones as failed tasks).
Untested: failed-fence admission, broader outages/adoption, HA/failover, migration, physical hardware
and the unavailable-Core browser presentation.

[Back to the user guide](README.md)
