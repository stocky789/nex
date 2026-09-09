# Create a virtual machine

## Status

This development workflow registers existing local storage and guest networking, uploads
installation media, and creates a **stopped VM** through Nex Controller. The VM belongs to
a pool with a separate assignment to one host. Storage and network access are checked on
that host; a pool registration does not make a local disk shared.

Editing and deletion are not yet available in this development build. Start the VM with
its [Power controls](vm-power.md), then install the guest through
[Open a VM console](vm-console.md).
An adopted Controller appliance remains visibly protected and is not recreated by this workflow.
Use an explicitly selected disposable lab host; this is not production acceptance.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md), with a site and an
  enrolled host. Enrollment creates the host's pool automatically with that host as pool master. Use matching Controller, Core and web assets from this development build;
  an older agent cannot execute these operations.
- A freshly connected, active host with working KVM/QEMU and system libvirt, enough CPU
  and memory for the guest, and local recovery access.
- An existing writable local ext4, XFS or Btrfs directory for additional storage, or the
  host's default **ISO** registration at `/var/lib/libvirt/images` (created on install and
  registered automatically when the host reports). That default path holds both uploaded
  ISOs and guest disks. Extra paths must not contain symlinks. The directory and every
  ancestor must be root-owned directories without group or other write permission, and permit
  directory traversal by other users so QEMU can access the guest files. ACL-only access is
  not supported in this build. The standalone agent service permits writes under
  `/var/lib/libvirt/images`; another location also needs an explicit service write allowance.
  Manual registration does not create the selected directory or format a disk.
- For a VLAN network: the name of the host NIC that carries the VLAN (the installer names
  NICs `nex0`, `nex1`, …; the host's **Networking** panel lists them with their kind) and a
  switch port that trunks that VLAN. Nex creates the guest bridge itself and never changes
  the management bridge, host addresses or routes. Use the same NIC names on every host.
- Otherwise an existing Linux bridge such as the installer-created `br0`, or an active
  libvirt network on the selected host; that registration changes no host networking.
- A guest installation `.iso` containing an ISO-9660 volume descriptor, at most 8 GiB.
  Retain enough local space on Controller for staging and on Core for the transferred image.
  Select firmware supported by the guest; UEFI must be advertised by the host's libvirt
  capabilities for KVM, x86-64 and q35. Allow 2 GiB of available memory for the host in
  addition to the requested guest memory.
  Upgrade Controller before Core; an older Controller rejects the new Core report fields.

## Steps

1. Sign in to Controller and, in **Resources**, select the pool that was created for the
   host (it is named after the host, which its **General** panel shows as **Pool master**).
2. Open **Storage**. The host's default **ISO** storage (`/var/lib/libvirt/images`) is
   registered automatically once the host reports; wait for it to show `succeeded` and use
   it for ISO upload and VM disks. To add another directory, select **Register local storage**,
   enter a **Name**, select the **Host**, and enter its **Existing directory**, then
   **Register and check access**. Wait for the access check to show `succeeded`; follow its
   link to **Task history** if it remains queued or reports an error.
3. Open **Networking**. The host's untagged network (for example **nex0 untagged** on the
   installer-created `br0`) is registered automatically once the host reports; use it for
   VMs on the management LAN. For a tagged VLAN, select **Register network**. Enter a name and select the same
   host. Under **Connection**, choose the host NIC that carries the network and enter its
   **VLAN ID** (1–4094 tagged; 0 uses the NIC untagged, which is only accepted on a NIC
   without host addresses). The bridge name is shown as `<NIC>v<VLAN>`. To use an existing
   bridge untagged, choose it from the same list; choose **Other** to enter an existing
   bridge or libvirt network by name. Select **Register and check access**, then wait for
   `succeeded`. The table shows each network's connection, bridge and VM count.
4. Open **ISO library**, then **Upload ISO**. Choose the validated **Destination storage**
   (the default **ISO** registration is fine) and select or drop one **ISO file**. Select **Upload and transfer ISO**. The progress
   bar measures the upload to Controller; wait for the separate host transfer task to
   show `succeeded` before using the image.
   To check one host, select it in **Resources**, open **Storage**, and look under
   **Host ISO images**. Each row shows the ISO, destination storage/directory, size,
   availability and transfer task. Return to the pool to continue creating the VM.
5. Open **Virtual machines**, then **Create VM**. Complete the four pages:
   enter **VM name** (1–63 lowercase letters, digits or internal hyphens) and **Placement host**; choose the transferred **Install ISO**,
   **Virtual CPUs** (1–64) and **Memory (MiB)** (512–262144); select **VM storage**, **Disk size (GiB)** (1–2048),
   **Guest network** and **Firmware**; then review all selections.
   Choose the same host and storage used for the ISO. Use **Back** to correct selections.
6. Select **Create stopped VM** once. In **VM details**, follow **Creation task** to its
   result and inspect **Observed state** and freshness independently.

## Expected result

The storage, network and ISO rows show successful host operations. The new VM appears
under its pool and assigned host, with a stable VM identity, requested resources and ISO
attached to a newly created guest definition and disk. A successful creation task means
provisioning completed; fresh libvirt observations establish its current state separately.
The guest is not started or installed by this workflow.

**Host ISO images** lists only transfers assigned to the selected host and pool.
**Transferred · last verified** means import succeeded, storage was validated and
the host is freshly connected. Pending, failed and uncertain transfers are listed
with their own status. This is Nex's transfer record, not a scan of every ISO on
the host; files are checked again when creating a VM.

## Troubleshooting

- **No selectable host or rejected capability:** reconnect the selected host, confirm it
  belongs to this pool, and install matching Core components if it predates managed-VM
  creation. Revocation and stale connectivity block new work.
- **Storage check fails:** check the exact existing path, supported local filesystem,
  root ownership and safe permissions throughout the path, service write allowance and
  free space. NFS and other shared storage are
  not supported by this development build. Never format another disk to work around an access error.
- **Network check fails:** for a VLAN network, read the task error: the uplink must be a
  physical NIC or bond reported by the host (a bond member is refused; use the bond), the VLAN must not already belong to another
  bridge (for example the management VLAN), and an untagged network is refused on a NIC that
  carries host addresses; choose a VLAN ID instead. Nothing is changed on a refused request.
  For an existing bridge or libvirt network, verify it exists or is active on this host and
  correct networking separately with a recovery path.
- **The default untagged network is missing:** it appears only after a host with managed-VM
  support reports a bridge whose port is a physical NIC. Check the host's **Networking**
  panel: each physical NIC lists what it carries; bridges, VLAN devices and VM ports are
  under **All interfaces**. If the automatic registration failed, its task shows the reason.
- **Controller was rebuilt:** once the host reconnects, its storage registrations, networks,
  ISO records and VM definitions come back automatically with their original names and IDs.
  Not recovered: sites, administrator accounts, audit history and enrollment tokens; the host
  appears in a new pool named after its hostname. See
  [Replace a failed Controller](manage-controller.md#troubleshooting).
- **VMs on a VLAN network have no connectivity:** a succeeded check proves the host bridge,
  not the switch. Confirm the switch port for that NIC trunks the VLAN. Nex does not remove
  networks in this build; delete `/etc/network/interfaces.d/nex-<bridge>` on the host and
  run `ifdown <bridge>` from local recovery access if a bridge must go.
- **ISO is rejected or transfer fails:** use a complete `.iso` and check space at both
  ends. Controller receipt alone is not successful Core transfer. Length, ISO format and
  SHA-256 checks must pass before the image is available for VM creation.
- **Host ISO availability is unknown or unavailable:** refresh Controller data and
  check host connectivity, access and storage verification. A prior successful
  transfer remains visible but does not establish current availability while the
  host is stale or revoked. Follow the transfer task for errors; uploading to one
  host does not copy the image to other hosts in the pool.
- **Resource or firmware validation fails:** reduce the requested capacity or choose
  firmware supported by the host and guest. Select successfully validated storage,
  network and media belonging to this exact host and pool.
- **The response is lost or Controller disconnects:** inspect **Task history** and the
  existing resource before submitting another request. Preserve the original form while
  reconciling an uncertain response. This browser tab retains submitted request identities
  across refresh; retry with the original entries and, for uploads, the original file.
  Do not create a differently named replacement to
  bypass an ambiguous task; unknown state does not prove that no disk or VM exists.
- **A task reports `failed` or `needs_attention`:** inspect its error and the selected
  host's actual resources. After correcting a definite failed storage/network check, submit
  a new registration; an uncertain registration retains its reservation. Failed or uncertain
  VM creation keeps the VM name reserved, and this build has no VM retry/deletion action.
  Never treat a failed task as proof that its disk or guest definition is absent.
- **Observed state is unknown or stale:** restore connectivity and wait for fresh libvirt
  observations. Controller loss does not stop resident VMs and does not authorize another
  writable instance. Retain host-local recovery access.

## Verification

**Pool VLAN networks (2026-09-09):** the [VLAN network checks](../qa/pool-vlan-networks/checks.md)
record the simulated browser walkthrough of the new **Connection** / **VLAN ID** form and the
disposable network-namespace check of real bridge creation and refusals. The native
nested-KVM lab and physical switches were not exercised; the "Other" path reuses earlier
evidence. Treat VLAN connectivity through a real switch as unverified.

**Pool master change verified (2026-09-09):** the pool in step 1 is now created automatically at
enrollment; the [pool master checks](../qa/pool-master/checks.md) (simulated host) and the
[real lab run](../qa/pool-master/lab/checks.md) show the pool page with **Pool master** and the
retained storage, network and media registrations after upgrade. Steps 2–6 use unchanged
contracts and were not re-run in the lab after this change.

The [host ISO visibility report](../qa/host-isos/checks.md) verifies host/pool
filtering, transfer labels, missing/stale/error states and keyboard/narrow layouts;
it separates simulated cases from the retained local Controller's actual ISO record.

The [fresh local installation](../qa/fresh-install/checks.md) adds packaged
`0.1.0+managed1` evidence: Controller deployed from the fresh Core ISO, automatic
enrollment with the installed embedded agent, successful first storage/network
registrations, genuine Debian ISO upload and creation of a stopped UEFI VM through
the browser. Desktop, 360-pixel and keyboard workflows were exercised without
mocking the APIs. Consult that report for subsequent boot/recovery results and limits.

The [development verification report](../qa/managed-vm/checks.md) records build identities,
checks and limits. Default **ISO** auto-registration is covered by Controller unit tests;
a live enroll walkthrough that skips manual Register local storage is not yet recorded.
The [native browser walkthrough](../qa/managed-vm/browser-native-results.json)
used Chromium, a real HTTPS Controller and a disposable Debian host with nested KVM/libvirt.
It exercised sign-in, resource registration/reuse, ISO upload, all four creation pages, stopped-VM
details, lost-response/reload recovery, keyboard use and desktop/360px layouts. The final
run rejected duplicate storage and network registrations and reused the directory and
bridge successfully registered in preceding native browser attempts. Its stale/error/response-loss presentation was
injected; these checks do not represent physical failures.

The [simulated browser walkthrough](../qa/managed-vm/browser-simulated-results.json) separately
covers the full registration-to-creation flow with a simulated agent and synthetic ISO.
Its screenshots verify browser behavior, not native storage execution. Desktop review and
mobile VM-details captures from both walkthroughs were inspected against these instructions.

The [native execution checks](../qa/managed-vm/results.json) and
[definition checks](../qa/managed-vm/definition-verification.json) additionally exercised
BIOS/UEFI creation, locally initiated lab boots, Controller outage and process restart/replay.
Those local boots are separate from this stopped-VM GUI workflow. Guest installation,
central power/console controls, physical hardware and arbitrary storage-failure recovery
remain unverified; see the [remaining platform gates](../plans/2026-09-08-nex-platform.md).

[Back to the user guide](README.md)
