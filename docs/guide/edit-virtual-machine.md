# Edit a virtual machine

## Status

This development workflow changes an existing VM's definition from the **Configuration** tab
of its panel in Nex Controller: name, description and guest OS hint; vCPUs, topology and CPU model; memory
mode and size; disks and network interfaces (add and remove); firmware, machine type, Secure
Boot, TPM, boot order, install ISO and driver disc; and autostart. What a running guest can
take is applied while it runs; the rest waits for the VM to be stopped and the form says so
per field. Deleting a VM is
covered in [Delete a virtual machine](delete-virtual-machine.md). Resizing a disk, moving a
disk to other storage and moving a VM to another host are not available in this build.
Tags are edited on the same tab's **Tags** card; see [Tag virtual machines](tag-virtual-machines.md).
Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

With the updated Core, saving a stopped VM applies the automatic disk I/O thread and
VirtIO network queue defaults described in [VM creation](create-virtual-machine.md).
During a live edit, existing devices keep their configuration and newly attached devices
receive the defaults where supported. A SCSI disk added to an existing controller uses that
controller's existing I/O thread configuration until a stopped save. Updating Core or rebooting
the guest alone does not rewrite an existing VM's definition. No restart is forced.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md) and a VM
  [created through Controller](create-virtual-machine.md) whose creation task succeeded. The
  Controller appliance is not edited here; it is managed from its host's local page.
- The VM's host and pool master freshly connected, running Core components that report
  `vm-update-v1` and master-owned VM placement. Core validates the current storage, network,
  ISO, capacity and runtime state; a Controller mirror is not placement authority.

## Steps

1. Sign in, choose the VM's pool (or its site's **all pools**) in the top-bar scope menu, select
   **VMs** in the rail and select the VM's row. In its panel, open **Configuration** and, on the
   **Definition** card, select **Edit hardware**. The details turn into a form in the same place
   and the panel widens. The line above it says whether
   the VM is running and which fields therefore wait for a stop.
2. Change what you need:
   - **Name**, **Virtual CPUs**, **Sockets / Cores / Threads**, **CPU model**, **Memory mode**,
     **Memory (MiB)**, **Firmware**, **Machine type**, **Secure Boot**, **TPM** and every
     **Boot order** field are only editable while the VM is stopped.
     A name change and a **Secure Boot** change can be saved together in one edit.
   - **Firmware** from **UEFI** to **BIOS** permanently deletes the VM's UEFI variable store:
     its boot entries and Secure Boot variables. Switching back to UEFI later starts from a fresh
     store, and an installed guest may not boot without repair. The form shows a warning, and
     **Save changes** stays disabled until you tick **I understand the UEFI variable store will
     be deleted**.
     The CPU selector keeps every host-usable named model and the host modes, independently
     of guest OS. **Pool CPU baseline** applies the currently verified common model and fixed
     features. An existing explicit definition appears as **Saved fixed CPU baseline** and
     is preserved even if current discovery is unavailable. The current pool baseline is
     marked **(Recommended)**; a saved definition receives the same label only when it
     matches that baseline. Individual feature flags are not displayed. Selecting another
     model clears the saved explicit feature list.
     Core checks explicit features before applying the stopped edit. Future hosts may not
     support the saved definition, and this does not certify live migration.
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
     adapter model, MTU and MAC change only while stopped. Changing an existing interface's MAC
     replaces that interface (it is then labelled **new**), so the guest sees a new network
     adapter. Existing interfaces show their actual MAC, including on a VM that has never been
     edited, and it otherwise stays fixed across edits. A new interface with an automatic MAC
     never reuses the address of an interface removed in the same save, and takes the next
     address above the VM's highest one rather than refilling a gap left by an earlier removal.
   - **Secure Boot** and **TPM**: **Secure Boot** is **On · only signed boot software runs** or
     **Off · unsigned boot media and drivers allowed**; **TPM** is **TPM 2.0 · emulated device**
     or **None**. Both are disabled with **Not available with BIOS** on a BIOS VM. A UEFI VM
     created before these fields existed shows **On** and **None**, which is what it is already
     running: libvirt gave every UEFI VM the enrolled-keys firmware, and no VM had a TPM.
     Leaving them alone does not change the VM definition or its saved NVRAM; a host OVMF
     package replacement is separate and needs a guest boot check. Both are part of the
     firmware a guest is installed against, so both need the VM stopped. Turning Secure Boot off
     or on changes the firmware build. A TPM can be added, but Nex refuses to remove one because
     the guest may have sealed BitLocker keys or other state to it; recreate the VM if it must have
     no TPM. Selecting **None** for a VM that already has a TPM therefore fails without changing
     the VM. Record a BitLocker recovery key inside the guest before adding a TPM or changing
     Secure Boot.
   - **Install ISO**: choose another ISO already on this VM's host, or **No install media**
     to eject the current one. The ISO library does not have to be the repository that holds
     the VM's disks. Both work while the VM runs; the CD drive keeps its place in the boot order.
   - **Driver disc**: the second CD drive, for the virtio-win ISO a Windows installer needs
     beside its install media. Current Core media seeds Fedora virtio-win into this host's ISO
     library after enrollment; select it here. On an older host without that seed, import matching
     media first. Choose another ISO on this host or **No driver disc**; the list excludes whatever
     **Install ISO** holds. It changes while the VM runs and is never in the boot order. The
     field is disabled, with *This host's Nex Core does not provide a second CD drive yet*, on
     a host running an older Nex Core. A VM defined before the second drive existed gains it
     the first time it is saved: with the VM stopped the drive is there at the next start, and
     a running VM gets the drive added to its definition (SATA cannot be hot-plugged, so the
     guest sees it after its next stop and start).
3. Select **Save changes**. Controller validates the request shape and queues a `vm-update`
   task. Core asks the pool master to approve the exact definition, then rechecks it before
   applying it. Select **Cancel** to discard the form.
4. Follow the task on **Tasks** (the rail item, or the activity pill in the top bar). The
   **Definition** card updates with the host's next report once the task has succeeded.

## Expected result

The task shows `succeeded`, and after the next host report the **Definition** card shows the new
definition. For a stopped VM the host redefines it; for a running VM new devices appear in
the guest immediately, removed ones disappear once the guest releases them (or after the next
stop and start), and a changed ISO is visible in the guest's CD drive. The panel's **Activity** tab records `vm-update` with the administrator.

## Observe VM usage

The **Current usage** card at the top of the VM panel's **Performance** tab shows the latest host
sample for a running VM.
**CPU** is a percentage of this VM's allocated vCPUs, not of the host; **Memory allocation** is
the amount assigned and its configured maximum, not memory used inside the guest; and **Host
memory cost** is the VM process's resident host memory. Disk used and capacity describe only
the VM's disk images. Every value can be **Not reported**: that is unknown, not zero, when the
host could not read it without waiting for another VM operation. Check the fresh/stale badge
before relying on the numbers; a stale status does not prove the VM stopped.

## Troubleshooting

- **Pool CPU baseline unavailable:** current member readiness, reachability, CPU vendor or
  compatibility could not be verified. Existing settings remain unchanged. Restore member
  availability, update older Core peers, or explicitly choose a CPU model while stopped.
- **Explicit CPU features need a Core upgrade:** upgrade Controller first, then Core hosts,
  before saving the feature-bearing definition. Model-only definitions keep their previous
  semantics, and Nex does not rewrite an existing VM when membership changes.

- **Edit hardware is greyed:** hover it for the reason. The VM's host is not freshly
  connected, does not report `vm-update-v1`, or the VM is the protected Controller appliance.
  **Save changes** is refused if the selected host or pool master lacks master-owned placement;
  upgrade Controller, then Core on those two hosts.
- **A field is greyed with "Stop the VM to change this":** that change needs a stopped guest.
  Shut the VM down with its [Power controls](vm-power.md), then edit.
- **"stop the VM first to change …":** the VM was running when the request arrived and the
  edit includes a change that needs it stopped; the message names the field. Choosing
  **No install media** is not such a change: if it reports *the boot order*, the host runs a
  Nex Core older than 0.2.4 (see [Use the VM console](vm-console.md#troubleshooting)).
- **"stop the VM and save its settings to apply the dedicated memory reservation before live editing":**
  this dedicated VM has an older memory definition. Shut it down, wait for **shut off**, then
  use **Edit hardware → Save changes** with the same memory settings. After the task succeeds, start
  the VM. Its RAM is now physically allocated and locked while running. The rejected live edit
  makes no device changes; a guest reboot alone does not apply this correction.
- **"VM disk assignment or boot order differs from requested ownership":** the VM's recorded
  definition and its host disagree about a device. On a VM whose disc was ejected by a Nex Core
  older than 0.2.4 this is the CD drive's boot order. Shut the VM down, then **Edit hardware → Save
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
- **Task reports `failed`:** read its error. Core can reject a queued edit because current
  storage, network, media, CPU, memory or runtime state changed after Controller displayed it.
  Correct the reported condition and submit a new edit; do not treat request acceptance as
  success. A running guest that has not released a removed device does **not** make the
  task fail: the task reports `succeeded` while the device stays attached to the running VM until
  the guest releases it or the VM stops. Adding an interface with that device's MAC fails until
  then (see below).
- **Task reports `needs_attention`:** the host was interrupted or a device operation failed
  part-way, and the persistent definition does not match the request. Inspect the VM on its
  host; stopping it and submitting the same edit again converges it, because a stopped edit
  redefines the whole VM. A **UEFI** to **BIOS** change interrupted after the variable store was
  removed is the exception, because the VM may no longer be defined on the host and a new edit is
  refused. Nex Core finishes that change when the host reconnects and receives the original edit
  again, or, for a VM on shared storage, at its periodic recovery about a minute later, so wait for
  the task rather than submitting the edit again. A VM on local storage whose task already reports
  `needs_attention` is not finished automatically: inspect the VM on its host.
- **"VM definition changed during firmware cleanup; inspect the VM on its host":** while Nex Core
  was finishing an interrupted UEFI to BIOS change, a UEFI definition of the VM appeared on the
  host. Nex does not remove it; inspect the VM on its host.
- **"VM name already exists" when repeating a failed rename:** the host may already carry the new
  name from the failed attempt. Inspect the VM on its host before editing again.
- **A change to a VM on shared storage (including a console CD/DVD insert or eject) shows
  `needs_attention`:** wait. About a minute after its storage is healthy, the host checks the
  VM and settles the change by itself: to `succeeded` if the change is in place, or to `failed`
  if the stopped VM still has its previous settings, after which you can submit it again.
  Until then only that VM refuses changes and power actions other than **Force stop**; other VMs
  in the pool are not affected. If it stays `needs_attention`, inspect the VM on its host.
- **"the VM's host agent does not support a driver disc drive; upgrade Nex Core":** the host runs
  an older Nex Core that cannot add a second CD drive. Update Nex Core on that host from
  [Software updates](software-updates.md), or clear the **Driver disc** selection.
- **"the driver disc ISO is not registered and verified on the VM's host and pool":** the ISO
  chosen as the driver disc is still importing, failed to import, or is stored on another host.
  Wait for the import to succeed on **Tasks**, or import the ISO to the VM's host. You can
  also insert or eject the driver disc from the [VM console](vm-console.md).
- **"when removing, reordering or changing a network interface, give every existing interface
  its MAC address" after replacing a VM's only interface:** removing the only interface and adding
  one with a different adapter model in the same save is refused, because the request no longer
  names any existing interface. Nothing changes. A likely workaround, not yet walked on a host:
  add the new interface first and save, then remove the old interface and save again. On a
  running VM, stop it first.
- **Task reports `failed` with "network interface MAC … is still attached to the running VM; wait
  for the guest to release it or stop the VM, then save again":** an interface you removed from the
  running VM in an earlier save still has that MAC, because the guest has not released the device
  (a guest at its firmware or installer menu never does). The host refused to add a second
  interface with the same MAC before doing anything: no disk was created, nothing was attached,
  and the VM keeps its previous settings. Wait for the guest to release the device and save again,
  or stop the VM and save again, or give the new interface a different manual MAC.
- **The Definition card still shows the old values after `succeeded`:** wait for the host's next
  report; the mirror follows what the host reports as current.

## Verification

See the [automatic I/O validation report](../qa/2026-09-24-automatic-vm-io.md) for the
separate live-edit, stopped-save and performance evidence and its limits.

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

See [master placement evidence](../qa/2026-09-18-master-vm-placement.md) for the
unreleased admission change. Browser checks use simulated Core responses; native
multi-host outage and recovery acceptance remains open.

See [pool CPU baseline evidence](../qa/2026-09-17-cpu-baseline.md) for this unreleased
CPU-definition change. In the recovery development build, managed pool autostart uses Core's
start admission and CPU preflight, with native libvirt autostart disabled. No future-host or
live-migration certification is claimed.

The [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) (`0.2.10~gate5`, 2026-09-17,
disposable nested-KVM lab, headless Chromium) walked the edit steps in the browser on a real host:
a rename collision refused by the host with nothing changed, a stopped CPU, memory and added
interface change, and on the running guest an ISO insert the guest could read, an interface
removal the guest released, and an eject, each listed in the VM's **Activity** view (earlier layout). After a
Controller replacement a stopped memory edit and a live interface add also succeeded. A **needs
attention** result was not induced.

**BUG-0254 walked on a real host.** The fix for a save that removes one interface and adds another
(previously refused with "when removing, reordering or changing a network interface, give every
existing interface its MAC address", or given the removed interface's MAC) was checked in the same
run: removing the first interface of a never-edited, stopped VM kept the other interface's MAC; a
removal plus an e1000e addition in one save on a never-edited, stopped VM gave the new interface a
MAC distinct from both originals; and the same on a running VM was accepted with the Controller
and host definitions in agreement. Two of these checks used a VM created through the API with no
guest OS. The gate4 review's report that removing or changing an interface on a never-edited VM
is refused was not reproduced.

**Open defects found in that run.** On a running VM whose guest has not released a removed
interface, a later **Add network interface** can reuse that interface's MAC, so the running VM has
two interfaces with the same MAC while the task reports succeeded (major; code reading indicates
0.2.9 is also affected). The BUG-0254 fix held within one save only. The development tree now
refuses that add (see Troubleshooting) and no longer refills a removed address; this was checked
with unit tests against a simulated libvirt only and has not yet been walked on a host.
Replacing a VM's only interface with one of a different adapter model in one save returns 400
(see Troubleshooting).

See the [edit QA report](../qa/vm-edit/checks.md) and the
[physical reservation QA report](../qa/dedicated-memory/checks.md) for executed checks and
untested limits.

The final disposable-package run verified Secure Boot on → rename → off → on, retained boot
entries and NVRAM identity, and a signed guest's EFI state. The independent Windows TPM/Secure
Boot matrix remains open. See [final ISO and Controller acceptance](../qa/2026-09-16-final-iso-controller-acceptance.md).

The UEFI to BIOS warning and its acknowledgement were exercised in a simulated browser run whose
captures were inspected but are not stored under `docs/qa`. Saving a rename and a Secure Boot
change together, and recovery of an interrupted UEFI to BIOS change, are covered only by Go unit
tests against a simulated `virsh`; neither has been walked through on a real host.

**VM usage verification, 2026-09-16.** A disposable Nex Core/libvirt host and the deployed
Controller UI reported a running VM's CPU, allocation, resident memory and disk use/capacity.
A shut-off VM reported no sample and the UI showed it as unknown rather than zero. The Sample age
row was removed the same day; freshness stays on the panel heading.
