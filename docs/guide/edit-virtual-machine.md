# Edit a virtual machine

## Status

This development workflow changes an existing VM's definition from its **General** tab in
Nex Controller: name, description and guest OS hint; vCPUs, topology and CPU model; memory
mode and size; disks and network interfaces (add and remove); firmware, machine type, boot
order and install ISO; and autostart. What a running guest can take is applied while it runs;
the rest waits for the VM to be stopped and the form says so per field. Deleting a VM,
resizing a disk, moving a disk to other storage and moving a VM to another host are not
available in this build.

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
     **Memory (MiB)**, **Firmware**, **Machine type** and every **Boot order** field are only
     editable while the VM is stopped.
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
   - **Install ISO**: choose another ISO already on this VM's host, or **No install media**
     to eject the current one. The ISO library does not have to be the repository that holds
     the VM's disks. Both work while the VM runs; the CD drive keeps its place in the boot order.
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
  edit includes a change that needs it stopped; the message names the field.
- **"stop the VM and save its settings to apply the dedicated memory reservation before live editing":**
  this dedicated VM has an older memory definition. Shut it down, wait for **shut off**, then
  use **Edit → Save changes** with the same memory settings. After the task succeeds, start
  the VM. Its RAM is now physically allocated and locked while running. The rejected live edit
  makes no device changes; a guest reboot alone does not apply this correction.
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
- **The details still show the old values after `succeeded`:** wait for the host's next
  report; the mirror follows what the host reports as current.

## Verification

See the [edit QA report](../qa/vm-edit/checks.md) and the
[physical reservation QA report](../qa/dedicated-memory/checks.md) for executed checks and
untested limits.

