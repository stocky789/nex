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
4. Select **Delete VM** (or **Delete VM and disks** when wiping). The request is queued as a
   durable task; follow it under **Recent tasks** / **Task history**. When the host reports
   success, the VM disappears from **Resources**.

## Expected result

The VM is undefined on the host and no longer listed in Controller. With disks retained, the
qcow2 files remain under the storage repository; with wipe selected, those files are removed.
HTTP acceptance is not proof of completion — wait for the task and an updated inventory.

## Troubleshooting

- **Delete is greyed with "Stop the VM before deleting it":** shut down or force-stop the VM
  and wait for observed **shut off**.
- **"The Controller appliance is managed from its host's local page":** appliances are not
  deleted through this workflow.
- **"The host agent does not support deleting VMs":** upgrade Nex Core on that host.
- **Task needs attention:** inspect the host; an interrupted undefine or wipe is reconciled by
  observation and may need a new delete request after the domain and files are verified.

## Verification

Unit and contract coverage for `DELETE /api/v1/vms/{id}`, Core retain/wipe paths, and UI
typecheck. Disposable-lab walkthrough of retain and wipe against real libvirt is not claimed
in this slice. See also [Edit a virtual machine](edit-virtual-machine.md) and
[Control VM power](vm-power.md).
