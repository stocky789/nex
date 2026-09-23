# Create a virtual machine

## Status

This development workflow registers existing storage repositories and guest networking, uploads or downloads
installation media, and creates a VM through Nex Controller with a six-page wizard:
**Identity**, **CPU and memory**, **Disks**, **Networks**, **Boot and media** and **Review**.
One VM can have up to eight disks and up to eight network interfaces, either
[memory mode](#memory-modes), UEFI or BIOS firmware with a **Secure Boot** and **TPM** choice,
an install ISO and an optional **driver disc** in a second CD drive, an explicit boot order and
optional autostart. The VM belongs to a pool with a separate assignment to one host. Nex Core
checks storage and network access from the pool master using a fresh observation of that host; a
pool registration does not make a local disk shared.

The VM is created stopped. **Start after creation** makes the host start it once, as the last
step of the same creation task, after it has verified the definition.
For a VM on local storage, ordinary local power control remains available through its host when
the pool master is unavailable; this placement check does not add a power dependency.

After creation the VM can be changed in place; see [Edit a virtual machine](edit-virtual-machine.md).
An existing disk's storage, size and provisioning cannot be changed. To remove a stopped VM, see
[Delete a virtual machine](delete-virtual-machine.md). To install the guest, open the VM's
**Console** tab and use **Start VM** on the console toolbar, so the console is attached in time for
the boot prompt; see [Open a VM console](vm-console.md). [Power controls](vm-power.md) start and
stop the VM afterwards.
An adopted Controller appliance remains protected and is not recreated by this workflow.
Use an explicitly selected disposable lab host.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md), with a site and an
  enrolled host. Enrollment creates the host's pool automatically with that host as pool master. Use matching Controller, Core and web assets from this development build;
  an older agent cannot execute these operations.
- A freshly connected, active placement host and reachable pool master with working KVM/QEMU
  and system libvirt. Both must report the current master-owned VM placement capability; upgrade
  Controller, then Core on the selected host and pool master if the wizard says an upgrade is
  required.
- One or more repositories registered and healthy on the placement host. The default
  **Local storage** registration at `/var/lib/libvirt/images` is a local directory created on
  install and can hold both uploaded ISOs and guest disks. A host that first connected to an
  earlier Nex Core keeps the name recorded then, usually **ISO**: the registration is the host's
  own record and an upgrade never renames it. For local-directory ownership, remote storage
  preparation,
  supported filesystem types, credentials and single-host limits, see
  [Add VM storage repositories](storage-repositories.md).
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
  capabilities for KVM, x86-64 and q35. To start the VM, the host needs its memory size
  available plus 2 GiB for itself; creating a stopped VM does not need that memory free.
  Upgrade Controller before Core; an older Controller rejects the new Core report fields.
- For a Windows guest on a **VirtIO** or **SCSI** disk bus or a **VirtIO** network adapter:
  the virtio-win driver ISO on the same host, attached as the VM's **Driver disc**, because
  Windows Setup cannot see those devices without it. Current Core media seeds Fedora virtio-win
  into the host ISO library on enroll; select it as **Driver disc**. On an older host without
  that seed, import matching media first. Choosing **SATA** and **Intel e1000e** avoids that
  at some cost in performance.
- For **Windows 11**: UEFI firmware, **Secure Boot** on and a **TPM 2.0**. Setup refuses every
  non-IoT edition without them, with only *This PC doesn't currently meet Windows 11 system
  requirements* to say why. The emulated TPM needs `swtpm` and `swtpm-tools` on the host; Nex
  Core installs both, and a host that lacks them refuses the VM rather than defining a TPM that
  cannot start.

## Steps

1. Sign in to Controller and, in **Resources**, select the pool that was created for the
   host (it is named after the host, which its **General** panel shows as **Pool master**).
2. Open **Storage**. The host registers its own **Local storage** (`/var/lib/libvirt/images`)
   itself and reports it once connected; wait for its **Core access** to show `healthy`,
   readable and writable, then use it for ISO upload and VM disks. If that self-registration fails (the directory is missing or not
   writable, for example), no **Storage** row appears: the failure is a failed task in
   **Task history** naming the host's reason, and the host does not retry it. Correct the
   directory on the host, then register it yourself with **Register storage** below;
   a failed registration does not reserve its path. To add local, NFSv4, OpenZFS, SMB3 or iSCSI
   storage, select **Register storage**, then follow
   [Add VM storage repositories](storage-repositories.md). Wait for the row to show
   `healthy`, readable and writable before using it; follow its link to **Task history** if it
   remains queued or reports an error.
3. Open **Networking**. The host registers its untagged network (for example **nex0 untagged**
   on the installer-created `br0`) itself and reports it once connected, and adds one for any
   bridge it later gains on a physical NIC or bond; use it for VMs on the management LAN. For a tagged VLAN, select **Register network**. Enter a name and select the same
   host. Under **Connection**, choose the host NIC that carries the network and enter its
   **VLAN ID** (1–4094 tagged; 0 uses the NIC untagged, which is only accepted on a NIC
   without host addresses). The bridge name is shown as `<NIC>v<VLAN>`. To use another existing
   bridge untagged, choose **Existing bridge <name> · untagged** from the same list. The list
   leaves out bridges this host has already registered, such as `br0` behind **nex0 untagged**:
   use that registered network for VMs instead of registering the bridge again. Choose
   **Other: existing bridge or libvirt network by name** to enter an unlisted bridge or libvirt
   network. Select **Register and check access**, then wait for
   `succeeded`. The table shows each network's connection, bridge and VM count. A bridge or
   libvirt network the host has already registered under another name is reported by the host
   as a failed task naming that registration; reuse it instead.
4. Open **ISO library**, then **Upload ISO**. Choose the validated **Destination storage**
   (the host's own **Local storage** registration is fine), then choose **ISO source**:
   - **Upload from computer**: select or drop one **ISO file**, then select
     **Upload and transfer ISO**. Progress, rate and **Cancel** for the upload to Controller
     appear under **Recent tasks**. You can close the form and keep working while the
     upload runs, but keep the Controller browser tab open.
   - **Download from public URL**: enter the direct **Public ISO URL**. **ISO name** is
     filled from the filename in that URL (edit it if you want; it must end in `.iso`),
     then select **Download ISO**. The selected host downloads and validates the image;
     once accepted, you can close the browser. Once Core has received the task, its
     download continues if Controller disconnects. This source requires a freshly connected
     host with URL download support; upgrade Nex Core if the form requests it.

   Wait for the host task to show `succeeded` before using either source. URL download size
   and SHA-256 appear after the host verifies the completed image; there is no byte-progress
   or cancel control for host downloads in this build. Each request targets the selected
   host's storage; it does not copy the image to every host. Disks can use any VM-ready
   repository on that host; they do not need to share storage with the ISO library.
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
      - **Guest operating system** — **Linux**, **Windows** or **Other**. Shown on the VM's
        page, and used for two things. It sets the **TPM** default on **Boot and media**
        (**Windows** starts with TPM 2.0, which Windows 11 requires; changing the TPM yourself
        stops it following the guest OS). It also gives a **Windows** VM the two device
        settings Windows expects: the emulated clock keeps **local time** in the RTC rather
        than UTC, and the guest is offered the Hyper-V enlightenments (relaxed timing, virtual
        APIC and spinlock hints) that let Windows run efficiently under KVM. Every VM, whatever
        its guest OS, is defined without ACPI sleep or hibernate, so the guest offers neither: a
        server VM that sleeps holds its disks while doing nothing. Nex never installs
        or configures a guest.
      - **Placement host** — the host this VM is assigned to. Hosts that are not freshly
        connected or lack managed-VM support are listed but cannot be chosen; the entry says
        `agent upgrade required` when the agent is too old. Selecting a host resets the
        storage, network and ISO choices to what that host has validated, so choose the
        host first.
   2. **CPU and memory**
      - **Virtual CPUs** — 1 to 64. This is the number of
        vCPUs the guest sees.
      - **Sockets**, **Cores per socket**, **Threads per core** — leave all three blank for
        the default of **one socket, as many cores as vCPUs, and one thread per core**. If you
        fill any one, the others start from that default so you can adjust; their product must
        equal the vCPU count, and the page states the mismatch in place until it does. Clearing
        any field clears all three. Set a topology only when guest licensing or NUMA-aware
        software needs a particular shape.
      - **CPU model** — new drafts use **Pool CPU baseline** when Core can verify a common
        CPU across every current pool member. This choice is marked **(Recommended)**.
        Selecting it saves its named model, fixed
        features and machine type with this VM. They do not change when pool membership
        changes. If the baseline is unavailable, wait for all members to be ready and
        reachable, or explicitly choose a CPU model. All host-usable named choices remain
        available, including `qemu64`; the list is independent of the guest OS.
        **Host passthrough** and **Host model** follow the current host's CPU. They do not
        establish a common pool baseline. Existing selections are preserved.
        Core checks an explicit CPU definition against the actual host runtime before
        defining or starting it. A current pool baseline does not guarantee future-host or
        live-migration compatibility; migration is not implemented in this slice. Change
        a VM's CPU only while stopped. Individual CPU feature flags are not displayed;
        the saved settings remain intact. A saved baseline is marked **(Recommended)**
        only while it matches the currently available pool baseline.
      - **Memory mode** — **Dedicated** or **Maximum**; see [Memory modes](#memory-modes).
      - **Memory (MiB)** — 512 to 262144. The page shows the host's reported CPU and memory
        capacity underneath as observation, not an admission decision. This size is the ceiling
        in both modes; Core checks current capacity at admission and again before execution.
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
        host's libvirt capabilities. Firmware can be changed later while the VM is stopped
        (see [Edit a virtual machine](edit-virtual-machine.md)), but an installed guest
        generally cannot be moved between the two: an operating system installed under one
        firmware will usually not boot under the other. Choose it before installing.
      - **Secure Boot** — **On · only signed boot software runs** (the default) or
        **Off · unsigned boot media and drivers allowed**. On, the VM boots the firmware
        build with the Microsoft keys enrolled, which is what a stock Windows or a signed
        Linux shim expects. Off disables Secure Boot so unsigned boot media and drivers load;
        changing an existing VM preserves its NVRAM boot entries and enrolled keys. The field is disabled with **Not available with BIOS**
        when firmware is **BIOS**; BIOS has no Secure Boot. Secure Boot is part of the
        firmware a guest is installed against and can only be changed while the VM is stopped.
      - **TPM** — **TPM 2.0 · emulated device** or **None**. The default follows **Guest
        operating system**: Windows starts with TPM 2.0 because Windows 11 refuses to install
        without one, everything else starts with None. The device is emulated per VM, and its
        state is kept on the host with that VM and removed when the VM is deleted. Disabled
        with **Not available with BIOS** under BIOS firmware. A TPM can be added later while the
        VM is stopped; Nex refuses to remove one because guest secrets may depend on its state.
      - **Install ISO** — an optional transferred ISO, or **No install media**. Any ISO
        already imported on this placement host can be attached, including one stored on a
        different repository from the VM's disks. The ISO is attached read-only as a SATA
        CD-ROM, so any guest installer can read it. Use **Open ISO library** if none is available.
      - **Driver disc** — an optional second ISO, or **No driver disc**, in the VM's second CD
        drive. For Windows on **VirtIO** or **SCSI**, use virtio-win here so Setup can load the
        disk driver (**Load driver**, then the `amd64` folder for the guest's Windows version).
        Current Core media seeds Fedora virtio-win into the selected host's ISO library after
        enrollment; select it here. On an older host without that seed, import matching driver
        media first. The list
        excludes whatever is selected as **Install ISO**, so one image cannot fill both drives.
        The field is disabled, with *This host's Nex Core does not provide a second CD drive
        yet*, when the placement host runs an older Nex Core; upgrade it to attach a driver
        disc. Both drives exist on every VM whether or not a disc is in them, so a disc can be
        inserted later from **Edit** or the [console](vm-console.md) CD/DVD control.
      - **Boot order** — the list shows every disk and, when one is selected, the install
        media. Install media is first by default. **Up** and **Down** reorder it, and Nex
        renumbers the devices from 1. The host tries them in this order. Removing the ISO
        removes it from the order. The driver disc is never in the boot order: it holds
        drivers the installer loads from inside Setup, not something to boot.
   6. **Review** — every selection is listed for a last check, including the derived or
      manual MAC of each interface and the resolved boot order. Under **Lifecycle**:
      - **Start after creation** — the host starts the VM once, as the last step of the
        creation task, after every disk exists and the definition is verified. The start is
        attempted once and is not retried; if it fails, creation still reports `succeeded` and a
        separate failed power task records why the created VM stayed stopped.
      - **Start automatically when the host boots** — sets the VM's autostart property on the
        host. It does not start the VM now.
6. Select **Create stopped VM** (or **Create and start VM**, when start after creation is
   selected) once. Controller opens the new VM's page for this creation task. Until the host
   reports the VM, the page shows **Creation** followed by the task status, with *This VM appears
   here after its host reports it.* and a **Task history** link. A queued task means the request
   reached Core, not that placement succeeded. If the task fails, the page shows **Creation
   failed** with Core's reason instead. Once the VM is reported, follow
   **Creation task** to its result and inspect **Observed state** and its freshness independently.
7. **If a warning appears with the accepted request**, read it but do not treat it as a refusal.
   It names a pool-shared repository, network or ISO whose access on the host you selected the
   pool master's last report does **not** show as current — for example *the pool master's last
   report does not show this host's access to storage repository `<id>` as current; the request
   may sit dispatched until it does*. The request was accepted and the pool master still decides;
   the warning only tells a request aimed at a host with a shared-access problem apart from one
   that is merely slow. The same warning stays on the task while it is dispatched and **clears by
   itself** when the master reports working access again. If it persists, fix that host's access
   to the named resource — see [Join a pool](join-a-pool.md#troubleshooting).

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
- **The ceiling is checked in both modes when the VM starts.** The host must have the full
  size available, plus a 2 GiB reserve for itself, so a VM that cannot reach its maximum is
  not started. Creation does not need that memory free, so you can create a VM now and start
  it once memory is available. **Maximum** lets the ceilings of several VMs add up to more than
  the host has, and
  nothing prevents them all from claiming it.

Controller does not display live memory use per VM in this build. The VM's page shows the
mode and size that were requested.

## Expected result

The storage, network and ISO rows show successful host operations. The new VM appears under
its pool and assigned host. Its page lists the VM identity, assigned host, observed state,
guest OS, vCPUs with the resolved topology and CPU model, memory with its mode, every disk
with its size, bus, provisioning, boot position and storage, every interface with its adapter
model and MAC, the install ISO with its boot position, the driver disc, the firmware with its
Secure Boot and TPM state, the machine type, and
whether the VM starts when the host boots. A successful creation task means provisioning
completed; fresh libvirt observations establish its current state separately.

With **Start after creation** the creation task itself ends by starting the VM, so the VM's
page can briefly show a stopped VM while the task finishes. No guest operating system is installed
by this workflow: open the VM's **Console** tab, use **Start VM** on its toolbar, and install the
guest from the attached ISO; see [Open a VM console](vm-console.md).

**Host ISO images** lists Nex's transfer records for the selected host and pool, not a scan of
every ISO on the host. **Transfer succeeded · not rescanned** means import succeeded, storage was
validated and the host is freshly connected; the file is checked again at VM creation. Pending,
failed and uncertain transfers show their own status.

## Troubleshooting

- **Pool CPU baseline unavailable:** wait for every pool member to be ready and reachable,
  or select a CPU model explicitly. An offline member is not ignored. Upgrade Controller
  first and then all Core hosts if an older peer cannot answer CPU discovery requests.
- **Explicit CPU features need a Core upgrade:** the selected fixed definition needs the
  newer Core CPU contract. Upgrade Core before submitting it; existing model-only VMs
  keep their previous settings.
- **CPU definition is not compatible:** stop the VM and select a CPU definition the host can
  provide. A supported CPU model is not proof that a particular guest OS can run on it;
  check the guest vendor's requirements separately.

- **An upload to Local storage fails with *storage ancestors must be root-owned directories,
  executable by others for QEMU…*:** hosts installed from 0.3.0 up to and including 0.4.4
  created the local ISO folder without the permission the hypervisor needs. On that host's
  console run `chmod 0711 /var/lib/libvirt/images/nex-media`, discard the failed import in
  **ISO library**, and upload again. Hosts installed from a later build are not affected.
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
  belongs to this pool, and upgrade Controller followed by Core on the selected host and pool
  master until both report master-owned VM placement. Revocation and stale connectivity block
  new work.
- **Storage check fails:** inspect the Core error, then follow the source-specific recovery
  steps in [Add VM storage repositories](storage-repositories.md). Do not point a local
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
  the install ISO must already be transferred to this placement host. Choose another ISO,
  or continue without install media.
- **A selection is no longer available:** *Some selections are no longer validated on this
  host. Reselect them before continuing.* appears when a storage target, network or ISO you
  picked has stopped being verified for this host — usually because the host went stale or a
  registration was re-checked. Reselect the resource, or go back to **Storage**,
  **Networking** or **ISO library** and confirm its state before continuing.
- **The submission is refused immediately:** the selected host or pool master is stale,
  unauthorized, in a different pool, or has not been upgraded for master-owned placement.
  Restore the connection or finish the upgrade, then submit again.
- **A creation task failed:** Core validates the current storage, network, ISO, CPU and memory
  state from the pool master and target host. Read the task error, correct that Core-reported
  condition, then submit a new request. A failed task does not prove a prior interrupted request
  had no effect; reuse the original request while its outcome is uncertain.
- **The creation task failed with a duplicate name, a used MAC or dedicated memory the host
  cannot promise:** the host checks its own record and libvirt, so these arrive as a failed
  task in **Task history** with the conflicting VM named. Nothing was created and the name and
  address are free again. Choose another name; clear the MAC field to take an automatic
  address, or choose another one (the host also checks the automatic addresses of its other
  VMs); or lower the dedicated size, free a dedicated VM, or use the **Maximum** memory mode.
- **Resource or firmware validation fails on the host:** the host repeats the checks with its
  real state, so a request Controller accepted can still fail. *insufficient free storage including reserve* means a
  storage target cannot hold every disk placed on it plus its headroom. *UEFI firmware is not
  available on this host* means that host's libvirt does not advertise EFI for this machine
  type; choose BIOS or fix the host's firmware packages.
- **"this host cannot provide a virtual TPM" or "this host has no emulated TPM backend":**
  that host's libvirt does not offer an emulated TPM, which means `swtpm` and `swtpm-tools`
  are not installed on it. Nex Core installs both; a host installed from an older ISO, or one
  they were removed from, must have them installed and `libvirtd` restarted before it can run
  a VM with a TPM. Nothing was created. Until then, choose **TPM → None** — but a non-IoT
  Windows 11 edition will refuse to install.
- **The VM was created but did not start:** the creation task shows `succeeded` and a
  separate failed power task, *VM created but not started* with the host's reason, appears in
  **Task history**. The VM exists with its disks, stopped, and is a normal managed VM; the
  start is not retried by itself. Inspect the reason, then start it from the
  [Power controls](vm-power.md). *interrupted before the automatic start completed* means the
  host was interrupted at that step and did not find the VM running afterwards; start it the
  same way. *insufficient available memory to start this VM including 2 GiB host reserve*
  means the host cannot currently give the VM its memory size plus 2 GiB for itself; free
  memory on that host or lower the VM's memory, then start it. A host running Nex Core from
  before this behaviour ignores the option and always
  leaves the VM stopped.
- **Autostart could not be enabled:** the VM is defined but its autostart property was not
  set. The task says so. Inspect the VM on its host; the VM itself is intact.
- **The guest does not boot from the ISO:** confirm **Install media** is first in the boot
  order on the VM's page and that the firmware matches the installer. UEFI and BIOS installs
  are not interchangeable; a VM created with the wrong firmware must be recreated.
- **The guest installer cannot see the disk or the network:** VirtIO and SCSI disks and
  VirtIO adapters need drivers. On Windows, attach the virtio-win ISO as the **Driver disc**
  after importing matching media to the VM host's ISO library and, at *Where do you want to
  install Windows?*, select **Load driver**, browse the driver drive and choose the
  `amd64` folder for the guest's Windows version. Both discs stay inserted, so Setup continues
  from the install media afterwards. If the VM was created before the driver disc existed, add
  it from **Edit** while the VM is stopped, or recreate the VM with a SATA disk and an Intel
  e1000e adapter.
- **Windows 11 Setup says the PC does not meet system requirements:** Setup does not say which
  requirement. Check on the VM's page that firmware is **UEFI**, **Secure Boot** is **On** and
  **TPM** is **TPM 2.0**, and that the VM has at least 2 vCPUs, 4096 MiB of memory and a 64 GiB
  system disk. Secure Boot can be changed and a missing TPM can be added from **Edit** while the
  VM is stopped. Nex refuses TPM removal; recreate the VM if it must have no TPM. Restart Setup
  after either addition or firmware change, because it checks the requirements at the start.
- **A Maximum VM never gives memory back:** on Windows the balloon driver is missing; install
  the virtio-win [guest tools](guest-tools.md). On Linux the driver is in the kernel, and only
  memory the guest counts as free comes back, so a guest holding a large file cache can sit
  near its ceiling legitimately.
- **"the selected host agent does not support a driver disc drive; upgrade Nex Core":** the host runs
  an older Nex Core that cannot add a second CD drive. Update Nex Core on that host from
  [Software updates](software-updates.md), or clear the **Driver disc** selection.
- **"the driver disc ISO is not registered and verified on the selected host and pool":** the ISO
  chosen as the driver disc is still importing, failed to import, or is stored on another host.
  Wait for the import to succeed in **Task history**, or import the ISO to the VM's host. After
  creation you can also insert or eject the driver disc from the [VM console](vm-console.md).
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
  a new registration; an uncertain registration retains its reservation. A terminal VM creation
  failure before Core defines a VM remains in task history but does not create a VM row or reserve
  its name or MAC. An uncertain outcome can still represent a disk or guest definition, so inspect
  Core's fresh inventory before retrying and never treat uncertainty as proof of absence.
- **Observed state is unknown or stale:** restore connectivity and wait for fresh libvirt
  observations. Controller loss does not stop resident VMs and does not authorize another
  writable instance. Retain host-local recovery access.

## Verification

See [master placement evidence](../qa/2026-09-18-master-vm-placement.md) for the
unreleased admission change. Browser checks use simulated Core responses; native
multi-host outage and recovery acceptance remains open.

The [pool CPU baseline evidence](../qa/2026-09-17-cpu-baseline.md) separates native platform,
backend and simulated-browser checks for this unreleased change. It does not certify every
CPU/guest combination or live migration. Native libvirt host-boot autostart bypasses Nex's
explicit CPU preflight; Core-mediated starts use it.

Recorded checks and their limits:

- [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) — Nex Core and Controller
  `0.2.10~gate5`, 2026-09-17, disposable nested-KVM lab with headless Chromium. Steps 1–6 were
  walked in the browser: host-owned Local storage and `nex0 untagged` adoption, two failed missing
  paths, the duplicate-bridge refusal, ISO upload by file picker and by (synthetic) drag and drop
  with non-ISO, over-size and cancelled refusals, and the six-step wizard at desktop and at 360 px
  driven by keyboard. Traversal, slash and non-`.iso` file names were refused through the API, not
  the browser. It created a UEFI Secure Boot VM with dedicated memory, thin and thick disks, two
  interfaces, ISO-first boot order and autostart, a VM with **Start after creation**, and UEFI VMs
  with TPM 2.0 and with an ISO attached, then installed Debian through the console. One test VM was
  created through the API only. In step 6 a host over-capacity create showed **Creation queued**,
  then **Creation dispatched**, then **Creation failed**, with no VM details, **Edit** or
  **Delete** at any point and never "Object not found". Open cosmetic defects: the VM name, an
  "Observed unknown" state line and the tabs still appear above the **Creation** panel, a "Your
  workspace" heading appears above **Creation failed**, and the failure shows only on the page's
  next 10 s refresh. Not walked: the maximum memory mode, a background upload after leaving the
  page, the host-side ISO free-space refusal and the failed-create **Edit** refusal wording.
- [Managed VM creation](../qa/managed-vm/checks.md) — records the earlier four-page, single-disk,
  single-interface form.
- [URL ISO downloads](../qa/iso-url/checks.md), [host ISO visibility](../qa/host-isos/checks.md)
  and [storage repositories](../qa/storage-repositories/checks.md).
- [Pool VLAN networks](../qa/pool-vlan-networks/checks.md) — simulated browser and namespace
  checks; VLAN connectivity through a real switch is unverified.
- [Pool master](../qa/pool-master/checks.md) and its [lab run](../qa/pool-master/lab/checks.md).
- [Fresh local installation](../qa/fresh-install/checks.md) — packaged stopped-UEFI-VM creation.
- [VM display names](../qa/vm-display-names/checks.md) and
  [dedicated memory](../qa/dedicated-memory/checks.md).
- [ISO library independent of VM disks](../qa/2026-09-14-iso-separate-from-vm-disks.md) —
  Controller and Core unit tests; no browser walkthrough of the wizard.
- [Windows 11 UEFI lab](../qa/2026-09-16-windows-11-uefi-lab.md) — the run that found the
  missing TPM, the hidden Secure Boot state, the single CD drive and the unchanged Windows
  defaults. It describes Nex Core 0.2.7, before those were fixed.

The disposable final-package walkthrough created and booted UEFI VMs, preserved NVRAM and boot
entries through rename, exercised Secure Boot on → off → on, and installed Windows with the
virtio-win driver disc. A signed Debian guest observed Secure Boot on and its absence after the
off edit. The independent Windows TPM/Secure Boot matrix remains open: fresh 60 GiB cases
created, booted and deleted with expected live XML combinations, but Windows Setup did not reach
the non-IoT requirements decision because optical-prompt timing and the manual Setup flow limited
the harness.
See [final ISO and Controller acceptance](../qa/2026-09-16-final-iso-controller-acceptance.md).

The second driver-disc workflow uses the bundled Fedora virtio-win media when the host was
installed from current Core media; older releases required a manual import. A simulated browser
run checked that **Driver disc** stays empty and disabled on a host without the second CD drive.

A Debian guest installation on UEFI was verified in the Phase 2 gate run on `0.2.10~gate5`; Windows installation
through the Controller, physical hardware and arbitrary storage-failure recovery remain unverified;
see the [platform plan](../plans/2026-09-08-nex-platform.md).

[Back to the user guide](README.md)
