# Register VM storage repositories

## Status

This development workflow registers an existing storage source on one Nex Core host for VM
disks and ISO images. Supported repository types are **Local directory**, **NFSv4 export**,
**OpenZFS dataset**, **SMB3 share** and **iSCSI LUN**. Nex Core owns the registration,
connection details and credentials; Controller shows the state reported by Core. A rebuilt
Controller is designed to recover the same repositories when the host reconnects; native
replacement testing for these backends is still in progress.

Each repository is assigned to one Core host. It is not shared storage placement: multi-host
writers, live migration, snapshots and backups are unavailable. Use an explicitly selected
disposable lab host. This page documents the development build; see
[storage repository checks](../qa/storage-repositories/checks.md) for the executed checks and
their limits.

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
- **NFSv4 export:** an existing NFSv4 server and absolute export path. Configure the export
  for the selected Core and QEMU access. Nex mounts it as a hard NFSv4 mount and does not make
  it usable by another Core. Nex verifies the mounted server and its reported filesystem
  identity. A changed server address or filesystem blocks use; this profile does not promise
  transparent server failover.
- **OpenZFS dataset:** optional ZFS support requires the matching Debian OpenZFS userspace
  tools and kernel module already installed and working on Core. Supply an existing imported,
  mounted filesystem dataset, its pool and its reported mount point. Encrypted datasets must
  already have their key available at boot. Nex never creates or imports a pool, upgrades it,
  mounts or unmounts the dataset, or changes pool or dataset properties. The reported mount
  point must be a clean absolute path without whitespace, control characters, `%` or `\`.
- **SMB3 share:** an existing SMB 3.1.1 server/share with encryption support, and an account and
  password with access for the selected Core and QEMU. Nex uses the supported encrypted,
  strict-cache profile; SMB1 and relaxed cache/locking modes are not supported. New SMB
  registration also needs permission to create and retain one hidden identity file at the share
  root.
- **iSCSI LUN:** an existing target portal, target IQN, LUN, WWID and one ext4 or XFS filesystem
  UUID. Restrict the target to this Core initiator only before registration, then confirm that
  restriction in the form. CHAP is optional and authenticates access; it does not make a LUN
  exclusive. Do not present a blank LUN, a LUN mounted elsewhere, multipath paths, or a LUN
  available to another writer. This initial profile permits one repository per target/portal
  session; use a separate target for another repository. Nex never formats the LUN.

## Steps

1. In **Resources**, select the pool for the selected Core host, then open **Storage**.
   The default **ISO** storage at `/var/lib/libvirt/images` may already be listed. It is a local
   directory registration and can hold both ISOs and VM disks.
2. Select **Register storage**. In **Register VM storage**, enter a **Name**, select the
   **Host**, then choose **Repository type**. The available types are **Local directory**,
   **NFSv4 export**, **OpenZFS dataset**, **SMB3 share** and **iSCSI LUN**.
3. Enter the existing source details for the selected type:

   - **Local directory:** enter its **Existing directory**.
   - **NFSv4 export:** enter **NFS server** and the absolute **Export path**.
   - **OpenZFS dataset:** enter **Imported pool**, **Existing dataset** and its original
     **Reported mount point**. Nex verifies that existing mount, then creates its own managed
     bind alias under the Core storage root; VM disk paths use the alias. Do not enter that
     Core-managed alias in the form.
   - **SMB3 share:** enter **SMB server**, **Share**, **Account** and **Password**. Core creates
     `.nex-storage-marker` at the share root if it is absent; otherwise it verifies and reuses
     the existing valid marker. Keep that file and its contents unchanged: do not rename, delete
     or replace it.
   - **iSCSI LUN:** enter **Target portal**, **Target IQN**, **LUN**, **LUN WWID** and
     **Existing filesystem UUID**. Select **This LUN is restricted to this Core initiator only**.
     Enter **CHAP account** and **CHAP secret** only when the target requires CHAP. Nex Core
     chooses its managed mount point after it allocates the repository identity; do not supply
     a mount path.

4. Select **Register and check access**. Registration is queued until Core has connected or
   inspected the source, confirmed its identity and tested access for Core and QEMU. For SMB
   and iSCSI, credentials are handed to the selected Core and are not shown in the repository
   table or Task history. Wait for the row to report `healthy`, **Readable**, **Writable** and
   a recent check time. `Unknown` or `unavailable` is not ready for VM disks.
5. To use the repository, open **Virtual machines** and select **Create VM**. On **Disks**, select
   it from **Storage**. Only storage that is healthy, writable, VM-disk capable and assigned to
   the placement host appears as **Select VM-ready storage**. An install ISO must be on storage
   used by one of the VM's disks; see [Create a virtual machine](create-virtual-machine.md).
6. Use **Recheck** after repairing a server, share, dataset, target or permission issue. A
   name-only **Update** for local, NFS, ZFS, or iSCSI without new CHAP fields does not reconnect
   the repository and is allowed while it is in use. The **Host** and **Repository type** stay
   fixed. SMB updates always include its account and password. Changing a local path or any ZFS,
   NFS, SMB or iSCSI source detail reconnects the repository, so remove or complete every
   VM-disk, retained-disk and media dependency first.
7. To rotate SMB credentials, use **Update / rotate credentials**. To rotate iSCSI CHAP
   credentials, use **Update** and enter the new **CHAP account** and **CHAP secret**. A
   credential-only update is permitted while the repository remains attached, but first shut off
   every VM using it and complete or reconcile pending or uncertain disk and media operations.
   Nex does not stop VMs automatically. Wait for any earlier storage task to settle before
   starting a rotation. A configuration update can be refused when the repository generation
   changed; reload the row, recheck it and retry with the current state.
8. To remove an unused credential, in **Unused storage credentials** select its **Host**, then
   select **Find unused credentials**. The table shows only Core-calculated, opaque receipts and
   their status and creation time; it never shows an account or secret. Select **Retire unused
   credentials**, review the receipt in the confirmation dialog, then confirm. Core checks again
   when it executes the request and rejects it if the receipt became attached to a repository or
   a pending storage operation. Retiring removes only the unused Core-local credential material;
   it never deletes repository data.
9. To stop using a repository, select **Detach**, type the exact **Repository name**, then select
   **Detach storage**. Core refuses detach while VM disks, retained disks or media depend on it.
   Detach removes Nex configuration and only Nex-managed mount/session setup; for a new ZFS
   repository it removes the Core bind alias only. It never deletes repository data, formats a
  LUN, mount or unmount the original ZFS dataset, or change ZFS properties. Detaching an SMB
  repository also leaves `.nex-storage-marker` on the share.

## Expected result

**Pool storage repositories** shows each reported repository's backend/source, **Accessible
host**, Core access state, capacity and check time. A healthy repository can store VM disks and
ISO images on its assigned host. Core checks its identity and access again before disk, media and
guest starts, so a disconnected share, changed mount, read-only filesystem or failed permission
check blocks new writes and guest starts using that repository.

Core preserves the repository identity and configuration on the host. Controller loss does not
stop resident VMs or authorize another host to write the storage. After a Controller replacement,
reconnect the same Core and let inventory refresh; do not register the repository again or provide
its credentials again unless Core reports that a credential rotation is needed.

If an accepted storage task is interrupted or needs attention, Core periodically retries that
same task and updates **Task history** when it reaches an outcome. Correct the reported cause,
then wait for the result and use **Recheck** to obtain a fresh repository observation before a
new dependent action. A terminal failed task is not automatically retried.

## Troubleshooting

- **The Host list is empty or registration is rejected as unsupported:** reconnect or upgrade the
  selected Core, then use matching Controller, Core and web builds.
- **Registration or Recheck fails:** inspect the exact Core error in the repository row and
  **Task history**. Check server reachability, the existing source identity, filesystem health,
  capacity, mount permissions and QEMU access. Correct the source, then use **Recheck**; do not
  create an empty local directory at an unavailable remote mount point.
- **A storage task is pending or needs attention:** correct the original reported cause, then
  wait for **Task history** to refresh from Core. Do not register the source again, submit a new
  rotation, or detach it while that uncertainty remains. After Core reports the outcome, use
  **Recheck** if the repository remains registered. A
  terminal `failed` task needs a new request after its cause is fixed.
- **NFS access fails:** confirm NFSv4, the server/export path and server-side UID/GID or
  root-squash policy. Do not replace the hard mount with a soft mount. If the server or
  filesystem identity changed, restore the original source before rechecking. Earlier
  development registrations with an unavailable filesystem ID stay unavailable; Nex does not
  silently assign them a replacement identity.
- **ZFS access fails:** confirm the pool is imported, the dataset is mounted at the reported
  mount point, `zpool` reports `ONLINE`, its module/tools work on the running Debian kernel, and
  the dataset has capacity. Unlock encrypted datasets through the host boot configuration before
  using Nex; do not force-import or change pool settings from this workflow. Enter the original
  clean absolute mount point, not the Core-managed alias; whitespace, control characters, `%`
  and `\` are refused in this field.
- **SMB access fails:** confirm the share name, SMB3 account/password, server transport-encryption
  policy, share permissions and locking support. Update the repository to rotate credentials;
  passwords are not recoverable from Controller. For a new SMB repository, a missing or changed
  `.nex-storage-marker` fails access safely. Restore the original marker from a trusted recovery
  copy if available, then **Recheck**. Do not delete, rename or recreate the marker, and do not
  detach or re-register a repository that still contains VM disks just to work around this error.
- **iSCSI access fails:** verify the portal, IQN, LUN, WWID and filesystem UUID against the
  intended already-formatted LUN. Confirm the target ACL restricts writable access to this Core
  initiator and that the filesystem is not mounted elsewhere. Do not use `/dev/sdX`, format the
  LUN, or attach a second initiator to the filesystem.
- **A VM cannot select or start on storage:** wait for a fresh `healthy`, readable and writable
  result on the VM's placement host. Check free capacity including headroom and place the ISO on
  one of the VM's disk repositories. An old successful check is not a current access guarantee.
- **Detach is refused:** remove or complete the listed VM/media dependency first. If an operation
  is uncertain, wait for Core to reconcile it; do not retry by registering the same source under
  another name.
- **Credential rotation or retirement is refused:** for rotation, shut off every VM using the
  repository and reconcile pending disk or media operations. For retirement, refresh **Unused
  storage credentials** and retry only if Core still lists the receipt; a changed attachment is
  rejected safely. Nex never stops a VM or detaches storage to complete either action.
- **An APT removal or downgrade is refused:** a host with version-2 repository records,
  credential receipts or tombstones needs a matching storage-repository package. Restore that
  package before using the host. A manual `dpkg` transaction can bypass this protection and is
  unsupported.

## Verification

The local Controller/Core lab has passed guest write-and-reboot checks on all four backends.
See [storage repository checks](../qa/storage-repositories/checks.md) for each executed workflow
and its limits, including NFS outage recovery and the first Controller replacement. Final
graceful Core reboot, the final Controller replacement and remaining backend fault checks
are still in progress; these results do not establish other server or hardware profiles.

[Back to the user guide](README.md)
