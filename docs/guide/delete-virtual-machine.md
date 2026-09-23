# Delete a virtual machine

## Status

This development workflow removes a stopped guest VM from Nex Controller and its assigned
host. You choose whether disk files stay on storage or are permanently deleted. The
Controller appliance cannot be deleted here.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md) and a guest VM
  [created through Controller](create-virtual-machine.md) whose creation task succeeded.
- The VM observed **shut off** with a fresh host report. Use [Control VM power](vm-power.md)
  to stop it first.
- The VM's host freshly connected and reporting the `vm-delete-v1` capability (matching Nex
  Core).

## Steps

1. Sign in and select the VM in **Resources**. Open **General**, or right-click the VM in the
   tree and choose **Delete…**.
2. On **General**, select **Delete VM**. Type the exact VM name to confirm.
3. Leave **Permanently delete disks from storage** unchecked to keep the disk files on the
   host after the VM is removed. Check it only when you intend to erase those files.
   Keeping disks keeps only the disk files. The VM's UEFI variable store (boot entries and
   Secure Boot settings) and its emulated TPM state are always removed with the VM, so a guest
   that seals secrets to the TPM, such as BitLocker, needs its recovery key before it can use a
   retained disk again.
4. Select **Delete VM** (or **Delete VM and disks** when wiping). The request is queued as a
   durable task. If the VM's page or console was open, Controller returns to the VM's host
   page. The page shows *Delete queued for <name>. Follow its progress in the Tasks strip.*;
   follow the task in the **Tasks** strip at the bottom of the page (select it to expand; it
   shows the last five tasks and a count of running and failed ones) or **Task history**. When
   the host reports success,
   the VM disappears from **Resources**.

## Expected result

The VM is undefined on the host and no longer listed in Controller. Its UEFI variable store and
TPM state are removed in both cases. With disks retained, the qcow2 files remain under the
storage repository; with wipe selected, those files are removed.
HTTP acceptance is not proof of completion — wait for the task and an updated inventory.

## Troubleshooting

- **Delete is greyed with "Stop the VM before deleting it":** shut down or force-stop the VM
  and wait for observed **shut off**.
- **"The Controller appliance is managed from its host's local page":** appliances are not
  deleted through this workflow.
- **"The host agent does not support deleting VMs":** upgrade Nex Core on that host.
- **Delete task still pending after the host agent restarted mid-delete:** wait. When the host
  reconnects, Controller delivers the same delete again and Nex Core finishes it with the original
  disk choice, including a wipe that was interrupted after the VM was undefined.
- **Task needs attention after an interruption:** what you can do depends on whether the VM is
  still defined on its host.
  - If the VM is still listed and observed **shut off** (the interruption came before libvirt
    undefined it), open **Delete VM** again and make the **same** choice for **Permanently delete
    disks from storage** as before. Nex Core first finishes the earlier delete with that choice.
    The new task then shows as **failed** with *VM is already deleted on this host: an earlier
    interrupted delete has now finished*; that is expected, and the earlier task settles as
    succeeded once the host reports. If the VM was started in the meantime, stop it first.
  - If the VM was already undefined (for example every interruption during the disk wipe), its
    state is no longer observed and Controller refuses a new delete with "stop the VM before
    deleting it". A task that already reports needs attention is not delivered again, so Nex does
    not finish it by itself: inspect the VM and its disk files on its host.
- **"an earlier interrupted delete for this VM made a different disk choice …":** the new request
  did not match the interrupted one, so Nex changed nothing. Delete again with the earlier choice
  (the message names it as `delete_disks=true` for wiping or `false` for keeping), or resolve the
  VM on its host. Nex never wipes disks that a later request asked to keep, and it does not offer a
  way to keep the disks of an interrupted wiping delete.
- **Retrying after an uncertain response:** submitting again with the same disk choice is safe and
  repeats the same request. Changing the disk choice submits a separate request; check **Recent
  tasks** for a delete that is already queued.
- **Storage refuses to detach:** a VM deleted with its disks wiped no longer holds its storage
  repository; a VM deleted with its disks kept still does, because its files remain.

## Verification

Unit and contract coverage for `DELETE /api/v1/vms/{id}`, Core retain/wipe paths, and UI
typecheck. The refusal when disk choices differ and the storage detach change are covered only
by Go unit tests against a simulated `virsh`.

The [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) (`0.2.10~gate5`, 2026-09-17,
disposable nested-KVM lab, headless Chromium) walked steps 1–4 in the browser on a real installed
host with libvirt, as did the earlier gate4 run: **Delete VM** and **Delete…** were disabled with a
reason (and the API refused with 409) for a running VM and for the Controller appliance;
typed-name confirmation refused empty, wrong and wrong-case names; the disk checkbox was unchecked
by default and sat next to its label; a delete with disks retained, from the tree menu, kept the
qcow2; two deletes with disks, from **General**, removed the disk directories and the NVRAM and
TPM state; the page returned to the host with the **Delete queued** notice; and an unrelated
running VM was untouched. The running VM was stopped through the API before its delete.
**Interrupted delete:** killing Nex Core while a wipe delete (submitted through the API to control
timing) was pending at its wiping stage was recovered on the real host: after restart the entry
stayed pending and then completed as succeeded, needs attention was never observed across every
journal write, and the domain and disk directory were gone (that VM had no TPM and had never
started, so it had no NVRAM or TPM state). Not produced on a host: the held
needs-attention case, an interrupt while cleaning NVRAM, the same-choice retry, the
different-disk-choice refusal and the storage detach. See also [Edit a virtual machine](edit-virtual-machine.md) and
[Control VM power](vm-power.md).
