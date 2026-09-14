# Create a virtual machine

## Status

This development workflow registers existing storage repositories and guest networking, uploads or downloads
installation media, and creates a VM through Nex Controller with a six-page wizard:
**Identity**, **CPU and memory**, **Disks**, **Networks**, **Boot and media** and **Review**.
One VM can have up to eight disks and up to eight network interfaces, either
[memory mode](#memory-modes), UEFI or BIOS firmware, an explicit boot order and optional
autostart. The VM belongs to a pool with a separate assignment to one host. Storage and
network access are checked on that host; a pool registration does not make a local disk shared.

The VM is created stopped. **Start after creation** makes the host start it once, as the last
step of the same creation task, after it has verified the definition.

After creation the VM can be changed in place; see [Edit a virtual machine](edit-virtual-machine.md).
An existing disk's storage, size and provisioning cannot be changed, and deleting a VM is not
yet available in this development build. Start the VM with its [Power controls](vm-power.md),
then install the guest through [Open a VM console](vm-console.md).
An adopted Controller appliance remains protected and is not recreated by this workflow.
Use an explicitly selected disposable lab host.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md), with a site and an
  enrolled host. Enrollment creates the host's pool automatically with that host as pool master. Use matching Controller, Core and web assets from this development build;
  an older agent cannot execute these operations.
- A freshly connected, active host with working KVM/QEMU and system libvirt, enough CPU
  and memory for the guest, and local recovery access.
- One or more repositories registered and healthy on the placement host. The default **ISO**
  registration at `/var/lib/libvirt/images` is a local directory created on install and can hold
  both uploaded ISOs and guest disks. For local-directory ownership, remote storage preparation,
  supported filesystem types, credentials and single-host limits, see
  [Register VM storage repositories](storage-repositories.md).
- Free space on each storage target for the **full size of every disk placed on it plus about
  4 GiB of headroom**, whether the disks are thin or thick. Thin disks are checked at their
  full size on creation but are not reserved afterwards, so later growth can still fill the
  filesystem.
- For a VLAN network: the name of the host NIC that carries the VLAN (the installer names
  NICs `nex0`, `nex1`, …; the host's **Networking** panel lists them with their kind) and a
  switch port that trunks that VLAN. Nex creates the guest bridge itself and never changes
  the management bridge, host addresses or routes. Use the same NIC names on every host.
- Otherwise an existing Linux bridge such as the installer-created `br0`, or an active
  libvirt network on the selected host; that registration changes no host networking.
- A guest installation `.iso` containing an ISO-9660 volume descriptor (at least ~32 KiB).
  Local uploads need free space on Controller staging (256 MiB reserved) and on the host
  storage target. URL downloads need free space only on the destination host, with 1 GiB
  reserved. Neither source has a fixed GiB cap. For URL downloads the host needs direct
  internet access and public DNS resolution; use a direct HTTP or HTTPS ISO link without
  login credentials or a URL fragment. Private-network destinations and proxies are not supported.
  Select firmware supported by the guest; UEFI must be advertised by the host's libvirt
  capabilities for KVM, x86-64 and q35. Allow 2 GiB of available memory for the host in
  addition to the requested guest memory.
  Upgrade Controller before Core; an older Controller rejects the new Core report fields.
- For a Windows guest on a **VirtIO** or **SCSI** disk bus or a **VirtIO** network adapter:
  the virtio-win driver media, because Windows setup cannot see those devices without it.
  Choosing **SATA** and **Intel e1000e** avoids that at some cost in performance.

## Steps

1. Sign in to Controller and, in **Resources**, select the pool that was created for the
   host (it is named after the host, which its **General** panel shows as **Pool master**).
2. Open **Storage**. The host registers its default **ISO** storage (`/var/lib/libvirt/images`)
   itself and reports it once connected; wait for it to show `succeeded` and use it for ISO
   upload and VM disks. If that self-registration fails (the directory is missing or not
   writable, for example), no **Storage** row appears: the failure is a failed task in
   **Task history** naming the host's reason, and the host does not retry it. Correct the
   directory on the host, then register it yourself with **Register storage** below;
   a failed registration does not reserve its path. To add local, NFSv4, OpenZFS, SMB3 or iSCSI
   storage, select **Register storage**, then follow
   [Register VM storage repositories](storage-repositories.md). Wait for the row to show
   `healthy`, readable and writable before using it; follow its link to **Task history** if it
   remains queued or reports an error.
3. Open **Networking**. The host registers its untagged network (for example **nex0 untagged**
   on the installer-created `br0`) itself and reports it once connected, and adds one for any
   bridge it later gains on a physical NIC or bond; use it for VMs on the management LAN. For a tagged VLAN, select **Register network**. Enter a name and select the same
   host. Under **Connection**, choose the host NIC that carries the network and enter its
   **VLAN ID** (1–4094 tagged; 0 uses the NIC untagged, which is only accepted on a NIC
   without host addresses). The bridge name is shown as `<NIC>v<VLAN>`. To use an existing
   bridge untagged, choose it from the same list; choose **Other** to enter an existing
   bridge or libvirt network by name. Select **Register and check access**, then wait for
   `succeeded`. The table shows each network's connection, bridge and VM count. A bridge or
   libvirt network the host has already registered under another name is reported by the host
   as a failed task naming that registration; reuse it instead.
4. Open **ISO library**, then **Upload ISO**. Choose the validated **Destination storage**
   (the default **ISO** registration is fine), then choose **ISO source**:
   - **Upload from computer**: select or drop one **ISO file**, then select
     **Upload and transfer ISO**. Progress, rate and **Cancel** for the upload to Controller
     appear under **Recent tasks**. You can close the form and keep working while the
     upload runs, but keep the Controller browser tab open.
   - **Download from public URL**: enter the direct **Public ISO URL** and an **ISO name**
     ending in `.iso`, then select **Download ISO**. The selected host downloads and
     validates the image; once accepted, you can close the browser. Once Core has received
     the task, its download continues if Controller disconnects. This source requires a freshly connected host with URL
     download support; upgrade Nex Core if the form requests it.

   Wait for the host task to show `succeeded` before using either source. URL download size
   and SHA-256 appear after the host verifies the completed image; there is no byte-progress
   or cancel control for host downloads in this build. Each request targets the selected
   host's storage; it does not copy the image to every host. Choose storage the VM's disks
   will use: an ISO can only be attached from a storage target one of the VM's own disks sits on.
   To check one host, select it in **Resources**, open **Storage**, and look under
   **Host ISO images**. Each row shows the ISO, destination storage/directory, size,
   availability and transfer task. Return to the pool to continue creating the VM.
5. Open **Virtual machines**, then **Create VM**; or select **New VM** in the toolbar or
   **File** menu while the pool, one of its hosts or one of its VMs is selected, which opens
   the pool's **Virtual machines** view with the wizard. The heading shows the current page, for
   example **Create VM · 1 of 6: Identity**. **Next** moves on, **Back** corrects an earlier
   page, and **Close** puts the wizard away. Nothing is submitted until the last page.
   Selecting another view, including with **Open Storage**, **Open Networking** and
   **Open ISO library** below, hides the wizard while you work there and shows it again,
   with your entries, when you return to **Virtual machines**. The same applies to the
   **Register network** and **Upload ISO** forms on their own views.
   1. **Identity**
      - **VM name** — use a normal display name such as **Production Web 01** or
        **guest.example.test**. Spaces, mixed case, punctuation and Unicode are supported.
        Names must be nonblank and at most 200 UTF-8 bytes; slashes and control characters
        are not allowed. The name must be unique on the host and is preserved there, so it
        returns when a replacement Controller adopts the pool. It can be changed later
        while the VM is stopped. Upgrade Controller and Core together for these naming rules.
      - **Description** — optional, at most 4096 characters, stored with the VM definition
        on the pool master.
      - **Guest operating system** — **Linux**, **Windows** or **Other**. A record and a
        hint only: it is shown on the VM's page and drives advice such as the virtio-win
        reminder. It does not change the VM's devices, and Nex never installs or configures
        a guest.
      - **Placement host** — the host this VM is assigned to. Hosts that are not freshly
        connected or lack managed-VM support are listed but cannot be chosen; the entry says
        `agent upgrade required` when the agent is too old. Selecting a host resets the
        storage, network and ISO choices to what that host has validated, so choose the
        host first.
   2. **CPU and memory**
      - **Virtual CPUs** — 1 to 64, and no more than the host reports. This is the number of
        vCPUs the guest sees.
      - **Sockets**, **Cores per socket**, **Threads per core** — leave all three blank for
        the default of **one socket, as many cores as vCPUs, and one thread per core**. If you
        fill any one, the others start from that default so you can adjust; their product must
        equal the vCPU count, and the page states the mismatch in place until it does. Clearing
        any field clears all three. Set a topology only when guest licensing or NUMA-aware
        software needs a particular shape.
      - **CPU model** — **Host passthrough** (the default) gives the guest this host's exact
        processor and the best instruction-set coverage. **Host model** presents a common
        processor instead. The remaining choices are the named QEMU CPU models that Nex Core
        reports as usable on the selected host (models that cannot run here are omitted).
        Core checks the choice again before it defines the VM. Use a baseline that every
        intended destination reports before planning a migration. The choice is part of the
        definition and can only be changed while the VM is stopped.
      - **Memory mode** — **Dedicated** or **Maximum**; see [Memory modes](#memory-modes).
      - **Memory (MiB)** — 512 to 262144, and no more than the host's total memory. The page
        shows the host's reported CPU and memory capacity underneath. This size is the
        ceiling in both modes, and the host must be able to meet it at creation time.
   3. **Disks** — one to eight disks, each its own **Disk 1**, **Disk 2**, … block.
      - **Storage** — a storage repository that is healthy and validated on the selected host.
        Different disks may use different storage targets. If the host has none, use
        **Open Storage** to register one.
      - **Disk name** — 1–63 letters, digits, dots, dashes or underscores, starting with a
        letter or digit, and unique within this VM. It labels the disk in Nex; it is not the
        device name the guest sees.
      - **Size (GiB)** — 1 to 2048 per disk.
      - **Bus** — **VirtIO** is fastest and appears as `/dev/vdX` in the guest. **SCSI**
        attaches through a virtio-scsi controller. Both are VirtIO devices: Linux has the
        drivers built in, Windows needs virtio-win drivers loaded during installation.
        **SATA** is slower but works with the drivers every guest already has.
      - **Provisioning** — **Thin · grows as written** creates a small file that grows with
        the guest's writes. **Thick · allocated now** allocates the whole size immediately,
        which takes longer to create but keeps the guest from meeting an out-of-space error
        on a write the host already accepted. Either way, creation requires free space for
        the full size of every disk on that storage plus about 4 GiB of headroom.
      - **Add disk** and **Remove disk N** manage the list. The last remaining disk cannot be
        removed. Every disk is created inside this VM's own directory on its storage;
        existing files are never reused or overwritten.
   4. **Networks** — zero to eight interfaces, each its own **Network interface N** block.
      - **Network** — a network registration validated on the selected host. Use
        **Open Networking** if the host has none. A registration on the pool does not make a
        host's network reachable from another host.
      - **Adapter model** — which network card the guest sees. The page explains each choice
        as you select it.
        - **VirtIO (paravirtualised)** — fastest and lowest host CPU, and the right choice
          for anything modern. The guest needs a VirtIO driver: built into Linux, and on
          Windows supplied by the [guest tools](guest-tools.md).
        - **Intel E1000** and **Intel E1000E** — emulate Intel gigabit cards that nearly every
          operating system already has a driver for. Slower and more host CPU than VirtIO.
          Use one when a guest, or an installer running before drivers load, cannot see a
          VirtIO adapter at all.
        - **Realtek RTL8139** — emulates a 100 Mbit card for guests too old to have a gigabit
          driver. The 100 Mbit limit is the emulated hardware, not a setting.
        - **VMware vmxnet3** — for guests that already carry VMware drivers, so a machine
          brought over from VMware keeps working without reconfiguring its networking.
      - **MTU** — shown only for **VirtIO**, the one model that can tell the guest its MTU.
        Leave it blank for the network's own MTU; 9000 is the usual jumbo-frame value.
        Every device along the path must agree, or traffic is dropped with no error. On the
        emulated adapters the field is replaced by a note: set the MTU inside the guest
        instead, because those cards have no way to learn it from the host.
      - **MAC address** — leave blank for **Automatic**: a stable address derived from this
        VM's identity and the interface position, which does not change. A manual address
        must be six lower-case hexadecimal octets separated by colons and unicast (an even
        second character), such as `52:54:00:12:34:56`, and unused anywhere else in the pool.
      - **Add network interface** and **Remove network interface N** manage the list, up to
        eight. A VM with no interfaces is allowed and starts with no guest networking; the
        page says so.
   5. **Boot and media**
      - **Firmware** — **UEFI** (the default) or **BIOS**. UEFI must be advertised by the
        host's libvirt capabilities. Firmware cannot be changed after creation, and an
        installed guest generally cannot be moved between the two.
      - **Install ISO** — an optional transferred ISO, or **No install media**. Only ISOs
        stored on a storage target one of this VM's own disks uses can be attached; the host
        resolves the image there. The ISO is attached read-only as a SATA CD-ROM, so any
        guest installer can read it. Use **Open ISO library** if none is available.
      - **Boot order** — the list shows every disk and, when one is selected, the install
        media. **Up** and **Down** reorder it, and Nex renumbers the devices from 1. The host
        tries them in this order: put **Install media** first to install a guest. Removing
        the ISO removes it from the order.
   6. **Review** — every selection is listed for a last check, including the derived or
      manual MAC of each interface and the resolved boot order. Under **Lifecycle**:
      - **Start after creation** — the host starts the VM once, as the last step of the
        creation task, after every disk exists and the definition is verified. The start is
        attempted once and is not retried; if it fails, the VM is still created and the task
        reports `needs_attention` with the reason.
      - **Start automatically when the host boots** — sets the VM's autostart property on the
        host. It does not start the VM now.
6. Select **Create stopped VM** (or **Create and start VM**, when start after creation is
   selected) once. Controller opens the new VM's page. Follow **Creation task** to its
   result and inspect **Observed state** and its freshness independently.

### Memory modes

| Mode | What the VM gets | When to use it |
|---|---|---|
| **Dedicated** | The full amount is allocated and locked in host RAM before the VM runs, with no swapping or page sharing. Stopping the VM releases physical RAM; Nex still counts its capacity commitment. | Predictable performance; guests without the balloon driver; anything you do not want competing for memory. |
| **Maximum** | A ceiling only. The VM reserves nothing, uses the memory it actually touches, and gives back memory it frees. | Fitting more guests on a host when their real use is well below the ceiling. |

A **Dedicated** VM created before physical reservation existed keeps its old definition until
it is saved while stopped: upgrade Nex Core, [shut down the VM](vm-power.md), wait for
**shut off**, open **General → VM details → Edit**, select **Save changes** with the same memory
mode and size, wait for the `vm-update` task to succeed, then start it. An agent upgrade, guest
reboot or stop/start without saving changes nothing. See [Edit a virtual machine](edit-virtual-machine.md).

A running 4096 MiB dedicated VM now holds at least 4096 MiB of host RAM, plus QEMU's overhead,
even if applications inside the guest use only 1 GiB. Allow headroom for the host and that overhead.
If the host cannot allocate or lock the memory, startup fails; inspect the task and host libvirt
logs and free capacity before retrying. The VM definition and disks remain available.

Three limits to know before choosing **Maximum**:

- **Returning freed memory needs the balloon driver running inside the guest.** Linux has it
  built into the kernel. Windows gets it from the virtio-win guest tools; see
  [Install guest tools](guest-tools.md). Until the driver runs, the guest keeps every page it
  has touched, so usage can drift up to its ceiling. Even with the driver, only memory
  the guest counts as free comes back; pages it holds as file cache stay with the guest.
- **Nothing grows or shrinks a guest under host memory pressure.** This build has no daemon
  that reclaims memory when the host runs short, and Nex does not balance memory between guests.
- **The ceiling is checked in both modes.** The host must be able to meet the full size, plus
  a 2 GiB reserve for itself, at creation time, so a VM that cannot reach its maximum is
  refused. **Maximum** lets the ceilings of several VMs add up to more than the host has, and
  nothing prevents them all from claiming it.

Controller does not display live memory use per VM in this build. The VM's page shows the
mode and size that were requested.

## Expected result

The storage, network and ISO rows show successful host operations. The new VM appears under
its pool and assigned host. Its page lists the VM identity, assigned host, observed state,
guest OS, vCPUs with the resolved topology and CPU model, memory with its mode, every disk
with its size, bus, provisioning, boot position and storage, every interface with its adapter
model and MAC, the install ISO with its boot position, the firmware and machine type, and
whether the VM starts when the host boots. A successful creation task means provisioning
completed; fresh libvirt observations establish its current state separately.

With **Start after creation** the creation task itself ends by starting the VM, so the VM's
page can briefly show a stopped VM while the task finishes. No guest operating system is installed by this workflow — start the VM,
open its [console](vm-console.md) and install the guest from the attached ISO.

**Host ISO images** lists Nex's transfer records for the selected host and pool, not a scan of
every ISO on the host. **Transfer succeeded · not rescanned** means import succeeded, storage was
validated and the host is freshly connected; the file is checked again at VM creation. Pending,
failed and uncertain transfers show their own status.

## Troubleshooting

- **A URL download fails:** use a direct public ISO download link, not a web page or a
  login page. Core rejects non-public addresses even after DNS resolution or redirects,
  permits at most five redirects, and stops a transfer after two hours. Check host DNS,
  internet access, TLS trust and available storage. Incomplete or invalid downloads are
  not published as usable ISOs. After a definite failure, correct the cause and submit a
  new download. If the submission response was lost, keep the original entries and retry
  to reconcile that request before starting another.
- **Core restarts during a download:** partial transfers are not resumed. Inspect
  **Task history** after reconnecting; a complete published image is checked against its
  durable size/hash record before success is reported. For an incomplete transfer, resolve
  the reported result and submit a new request.

- **A dropdown closes as soon as it opens:** some desktops dismiss a browser's own popup
  menus. Nex draws its dropdowns inside the page in current Chromium-based browsers (Chrome,
  Brave, Edge 135 or later), where this cannot happen; in other browsers use the keyboard
  (focus the field, then Arrow keys and Enter) or a current Chromium-based browser.
- **No selectable host or rejected capability:** reconnect the selected host, confirm it
  belongs to this pool, and install matching Core components if it predates managed-VM
  creation. Revocation and stale connectivity block new work.
- **Storage check fails:** inspect the Core error, then follow the source-specific recovery
  steps in [Register VM storage repositories](storage-repositories.md). Do not point a local
  registration at a remote mount that may disappear.
- **Network check fails:** for a VLAN network, read the task error: the uplink must be a
  physical NIC or bond reported by the host (a bond member is refused; use the bond), the VLAN must not already belong to another
  bridge (for example the management VLAN), and an untagged network is refused on a NIC that
  carries host addresses; choose a VLAN ID instead. Nothing is changed on a refused request.
  For an existing bridge or libvirt network, verify it exists or is active on this host and
  correct networking separately with a recovery path.
- **The default untagged network is missing:** the host registers it itself, at agent start
  and within a minute of a bridge on a physical NIC or bond appearing, and reports it on its
  next connection. Check the host's **Networking** panel: each physical NIC lists what it
  carries; bridges, VLAN devices and VM ports are under **All interfaces**. If the host's own
  registration failed, its task shows the reason. A host running Nex Core from before this
  behaviour registers nothing itself; upgrade it.
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
- **The wizard refuses to continue:** the page states the reason in place. Sockets × cores ×
  threads must equal the vCPU count (leave all three blank for the default); disk names must
  be unique within the VM; a manual MAC must be lower-case, colon-separated and unicast; and
  the install ISO must sit on storage one of this VM's disks uses. Add a disk on that storage,
  choose another ISO, or continue without install media.
- **A selection is no longer available:** *Some selections are no longer validated on this
  host. Reselect them before continuing.* appears when a storage target, network or ISO you
  picked has stopped being verified for this host — usually because the host went stale or a
  registration was re-checked. Reselect the resource, or go back to **Storage**,
  **Networking** or **ISO library** and confirm its state before continuing.
- **The submission is refused immediately:** unresolvable storage, network or ISO selections
  and capacity beyond what the host reports are rejected by Controller without queuing a host
  task. Nothing was created; correct the entry and submit again.
- **The creation task failed with a duplicate name, a used MAC or dedicated memory the host
  cannot promise:** the host checks its own record and libvirt, so these arrive as a failed
  task in **Task history** with the conflicting VM named. Nothing was created and the name and
  address are free again. Choose another name; clear the MAC field to take an automatic
  address, or choose another one (the host also checks the automatic addresses of its other
  VMs); or lower the dedicated size, free a dedicated VM, or use the **Maximum** memory mode.
- **Resource or firmware validation fails on the host:** the host repeats the checks with its
  real state, so a request Controller accepted can still fail. *insufficient available memory
  including 2 GiB host reserve* means the host's currently available memory cannot meet the
  ceiling — free memory or lower it. *insufficient free storage including reserve* means a
  storage target cannot hold every disk placed on it plus its headroom. *UEFI firmware is not
  available on this host* means that host's libvirt does not advertise EFI for this machine
  type; choose BIOS or fix the host's firmware packages.
- **The VM was created but did not start:** the creation task shows `succeeded` and a
  separate failed power task, *VM created but not started* with the host's reason, appears in
  **Task history**. The VM exists with its disks, stopped, and is a normal managed VM; the
  start is not retried by itself. Inspect the reason, then start it from the
  [Power controls](vm-power.md). *interrupted before the automatic start completed* means the
  host was interrupted at that step and did not find the VM running afterwards; start it the
  same way. A host running Nex Core from before this behaviour ignores the option and always
  leaves the VM stopped.
- **Autostart could not be enabled:** the VM is defined but its autostart property was not
  set. The task says so. Inspect the VM on its host; the VM itself is intact.
- **The guest does not boot from the ISO:** confirm **Install media** is first in the boot
  order on the VM's page and that the firmware matches the installer. UEFI and BIOS installs
  are not interchangeable; a VM created with the wrong firmware must be recreated.
- **The guest installer cannot see the disk or the network:** VirtIO and SCSI disks and
  VirtIO adapters need drivers. On Windows, load them from the virtio-win media during setup,
  or recreate the VM with a SATA disk and an Intel e1000e adapter.
- **A Maximum VM never gives memory back:** on Windows the balloon driver is missing; install
  the virtio-win [guest tools](guest-tools.md). On Linux the driver is in the kernel, and only
  memory the guest counts as free comes back, so a guest holding a large file cache can sit
  near its ceiling legitimately.
- **The response is lost or Controller disconnects:** inspect **Task history** and the
  existing resource before submitting another request. Preserve the original form while
  reconciling an uncertain response. This browser tab retains submitted request identities
  across refresh, and reopening **Create VM** restores those entries so the retry reconciles
  the same request; for uploads, use the original file. If you change the entries first, the
  wizard refuses with *An earlier submission has an uncertain outcome*: restore them, or
  inspect **Task history** before starting another request.
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

Recorded checks and their limits:

- [Managed VM creation](../qa/managed-vm/checks.md) — records the earlier four-page, single-disk,
  single-interface form. The six-page wizard, multiple disks and interfaces, memory modes, boot
  order, autostart, start after creation, host-owned default registrations and background ISO
  upload have no browser or lab walkthrough yet; treat step 5 as untested.
- [URL ISO downloads](../qa/iso-url/checks.md), [host ISO visibility](../qa/host-isos/checks.md)
  and [storage repositories](../qa/storage-repositories/checks.md).
- [Pool VLAN networks](../qa/pool-vlan-networks/checks.md) — simulated browser and namespace
  checks; VLAN connectivity through a real switch is unverified.
- [Pool master](../qa/pool-master/checks.md) and its [lab run](../qa/pool-master/lab/checks.md).
- [Fresh local installation](../qa/fresh-install/checks.md) — packaged stopped-UEFI-VM creation.
- [VM display names](../qa/vm-display-names/checks.md) and
  [dedicated memory](../qa/dedicated-memory/checks.md).

Guest installation, physical hardware and arbitrary storage-failure recovery remain unverified;
see the [platform plan](../plans/2026-09-08-nex-platform.md).

[Back to the user guide](README.md)
