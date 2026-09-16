# Edit a virtual machine

## Status

This development workflow changes an existing VM's definition from its **General** tab in
Nex Controller: name, description and guest OS hint; vCPUs, topology and CPU model; memory
mode and size; disks and network interfaces (add and remove); firmware, machine type, Secure
Boot, TPM, boot order, install ISO and driver disc; and autostart. What a running guest can
take is applied while it runs; the rest waits for the VM to be stopped and the form says so
per field. Deleting a VM is
covered in [Delete a virtual machine](delete-virtual-machine.md). Resizing a disk, moving a
disk to other storage and moving a VM to another host are not available in this build.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md) and a VM
  [created through Controller](create-virtual-machine.md) whose creation task succeeded. The
  Controller appliance is not edited here; it is managed from its host's local page.
- The VM's host freshly connected, running Nex Core components that report the
  `vm-update-v1` capability, with a fresh observation of the VM's state.
- Any storage, network or ISO the change refers to already registered and verified on the
  VM's host, as for creation.

## Steps

1. Sign in, select the VM in **Resources** and open **General**. Under **VM details**, select
   **Edit**. The details turn into a form in the same place. The line above it says whether
   the VM is running and which fields therefore wait for a stop.
2. Change what you need:
   - **Name**, **Virtual CPUs**, **Sockets / Cores / Threads**, **CPU model**, **Memory mode**,
     **Memory (MiB)**, **Firmware**, **Machine type**, **Secure Boot**, **TPM** and every
     **Boot order** field are only editable while the VM is stopped.
     The CPU selector contains the named models this host can run, plus Host passthrough and
     Host model; choose a baseline reported by every intended destination before migration.
     Nex Core checks that named model again before applying the edit.
   - **Description**, **Guest operating system** and **Start automatically when the host boots**
     can be changed at any time.
   - **Disks**: select **Add disk** to add one; choose its storage, name, size, bus and
     provisioning. Existing disks keep their storage, size and provisioning; their bus and
     boot order can be changed while stopped. **Remove** detaches a disk from the VM; its
     file is kept on the host and never deleted by an edit. A VirtIO or SCSI disk can be added
     to a running VM; a SATA disk cannot, and removing a disk that is in the boot order changes
     the boot order, so both wait for a stop. Removing a device from a running VM succeeds once the
     definition is updated; the device leaves the running guest only when the guest releases it
     (Linux with VirtIO drivers does so at once, a guest sitting at an installer boot menu never
     does), and it is gone after the VM's next stop and start either way.
   - **Network interfaces**: **Add network interface** adds one, with an automatic or manual
     MAC; **Remove** detaches one. Both work on a running VM. An existing interface's network,
     adapter model, MAC and MTU change only while stopped. Existing interfaces show their
     actual MAC, which stays fixed across later edits.
   - **Secure Boot** and **TPM**: **Secure Boot** is **On · only signed boot software runs** or
     **Off · unsigned boot media and drivers allowed**; **TPM** is **TPM 2.0 · emulated device**
     or **None**. Both are disabled with **Not available with BIOS** on a BIOS VM. A UEFI VM
     created before these fields existed shows **On** and **None**, which is what it is already
     running: libvirt gave every UEFI VM the enrolled-keys firmware, and no VM had a TPM.
     Leaving them alone changes nothing about how the guest boots. Both are part of the
     firmware a guest is installed against, so both need the VM stopped, and both can break an
     installed guest: turning Secure Boot off or on changes the firmware build, and removing a
     TPM leaves a guest that sealed BitLocker keys to it unable to unlock. Record a BitLocker
     recovery key inside the guest before touching its TPM.
   - **Install ISO**: choose another ISO already on this VM's host, or **No install media**
     to eject the current one. The ISO library does not have to be the repository that holds
     the VM's disks. Both work while the VM runs; the CD drive keeps its place in the boot order.
   - **Driver disc**: the second CD drive, for the virtio-win ISO a Windows installer needs
     beside its install media. Choose another ISO on this host or **No driver disc**; the list
     excludes whatever **Install ISO** holds. It changes while the VM runs and is never in the
     boot order. The field is disabled, with *This host's Nex Core does not provide a second CD
     drive yet*, on a host running an older Nex Core. A VM defined before the second drive
     existed gains it the first time it is saved: with the VM stopped the drive is there at the
     next start, and a running VM gets the drive added to its definition (SATA cannot be
     hot-plugged, so the guest sees it after its next stop and start).
3. Select **Save changes**. Controller checks the definition against the host and pool the
   same way creation does and queues a `vm-update` task for the host. Select **Cancel** to
   discard the form.
4. Follow the task under **Recent tasks** or **Task history**. The details update with the
   host's next report once the task has succeeded.

## Expected result

The task shows `succeeded`, and after the next host report **VM details** show the new
definition. For a stopped VM the host redefines it; for a running VM new devices appear in
the guest immediately, removed ones disappear once the guest releases them (or after the next
stop and start), and a changed ISO is visible in the guest's CD drive. **Activity** records `vm-update` with the administrator.

## Troubleshooting

- **The Edit button is greyed:** hover it for the reason. The VM's host is not freshly
  connected, the host agent does not report `vm-update-v1` (upgrade Nex Core), or the VM is
  the protected Controller appliance.
- **A field is greyed with "Stop the VM to change this":** that change needs a stopped guest.
  Shut the VM down with its [Power controls](vm-power.md), then edit.
- **"stop the VM first to change …":** the VM was running when the request arrived and the
  edit includes a change that needs it stopped; the message names the field. Choosing
  **No install media** is not such a change: if it reports *the boot order*, the host runs a
  Nex Core older than 0.2.4 (see [Use the VM console](vm-console.md#troubleshooting)).
- **"stop the VM and save its settings to apply the dedicated memory reservation before live editing":**
  this dedicated VM has an older memory definition. Shut it down, wait for **shut off**, then
  use **Edit → Save changes** with the same memory settings. After the task succeeds, start
  the VM. Its RAM is now physically allocated and locked while running. The rejected live edit
  makes no device changes; a guest reboot alone does not apply this correction.
- **"VM disk assignment or boot order differs from requested ownership":** the VM's recorded
  definition and its host disagree about a device. On a VM whose disc was ejected by a Nex Core
  older than 0.2.4 this is the CD drive's boot order. Shut the VM down, then **Edit → Save
  changes** once; set **CD drive boot order** in the same edit if you want the drive to boot
  first again.
- **"stop the VM first to change Secure Boot" or "… the TPM":** both are firmware, not
  devices a running guest can take. Shut the VM down, then edit.
- **"this host cannot provide a virtual TPM" / "this host has no emulated TPM backend":**
  adding a TPM was refused because that host's libvirt offers no emulated TPM, which means
  `swtpm` and `swtpm-tools` are missing. Nex Core installs both; install them on the host and
  restart `libvirtd`, then submit the edit again. The VM is unchanged.
- **"VM TPM differs from the requested configuration" or "VM Secure Boot differs from the
  requested configuration":** the host applied the definition but its own read-back does not
  carry the TPM or the Secure Boot state that was asked for. Nex does not report that as
  success. Inspect the VM on its host and its libvirt log; the most likely cause is a host
  whose firmware or swtpm packages changed underneath it.
- **A Windows guest's clock jumps after an upgrade and a stopped save:** a Windows VM now keeps
  **local time** in its virtual RTC, which is what Windows expects, instead of UTC. A guest
  that had been compensating shows the difference once and then settles at the next Windows
  time sync. If a guest must keep UTC in the RTC, set **Guest operating system** to **Other**.
- **"storage, size and provisioning cannot be changed after creation":** add a new disk
  instead, or plan a replacement VM; disk resize is not yet available.
- **"a change or power request for this VM is still pending":** one request per VM is in
  flight at a time. Wait for its task, then submit again.
- **Task reports `failed`:** read its error. Common causes are a running guest that has not
  released a device the edit removes, a storage target without free space for a new disk, or
  libvirt refusing the change on the host. Correct the cause and submit the edit again.
- **Task reports `needs_attention`:** the host was interrupted or a device operation failed
  part-way, and the persistent definition does not match the request. Inspect the VM on its
  host; stopping it and submitting the same edit again converges it, because a stopped edit
  redefines the whole VM.
- **A change to a VM on shared storage (including a console CD/DVD insert or eject) shows
  `needs_attention`:** wait. About a minute after its storage is healthy, the host checks the
  VM and settles the change by itself: to `succeeded` if the change is in place, or to `failed`
  if the stopped VM still has its previous settings, after which you can submit it again.
  Until then only that VM refuses changes and power actions other than **Force stop**; other VMs
  in the pool are not affected. If it stays `needs_attention`, inspect the VM on its host.
- **The details still show the old values after `succeeded`:** wait for the host's next
  report; the mirror follows what the host reports as current.

## Verification

See the [edit QA report](../qa/vm-edit/checks.md) and the
[physical reservation QA report](../qa/dedicated-memory/checks.md) for executed checks and
untested limits.

**Secure Boot, TPM and driver disc editing are not lab-verified.** They are covered by Nex Core
unit tests over the generated libvirt definition, the drive-by-alias resolution, the live-edit
path for a VM that predates the second CD drive, and the TPM state removal on delete
(`internal/vmhost/firmware_test.go`), and by Controller UI tests. Not exercised on a host: the
compatibility case that matters most — an installed Windows VM created before these fields
existed, saved once after a Core upgrade, continuing to boot with the same firmware — and
adding a driver disc to a VM that predates the second drive.

