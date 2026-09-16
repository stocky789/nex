# Control VM power

## Status

This development workflow starts, shuts down, reboots, force-reboots and force-stops a
virtual machine from Nex Controller. Each request becomes a durable task for the VM's
assigned host; the host's next report shows the resulting state. Shut down and Reboot are
ACPI requests that a guest without ACPI support, such as a firmware boot menu, ignores.
Force reboot is a hard guest reset, not ACPI. It also covers the offer to eject install
media once a guest is installed, so the VM stops stopping at "Press any key to boot from
CD or DVD" on every start. Editing a VM is covered in
[Edit a virtual machine](edit-virtual-machine.md); deletion is covered in
[Delete a virtual machine](delete-virtual-machine.md).

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md) and a VM Controller knows:
  an adopted Controller appliance or a VM [created through Controller](create-virtual-machine.md)
  whose creation task succeeded.
- The VM's host freshly connected, running matching Nex Core components (the host reports
  the `vm-power-v1` capability), with a fresh libvirt observation of the VM.

## Steps

1. Sign in, select the VM in **Resources**. The line under the VM's name shows the observed
   state and its freshness, for example `Observed running · fresh`, followed by the reason
   when power requests are unavailable. The toolbar's **Start**, **Shut down**, **Reboot**,
   **Force reboot** and **Force stop** buttons act on this VM; only the ones that fit the
   observed state are enabled, and hovering a greyed button shows the same reason.
2. Select **Start** for a stopped VM. The request is submitted immediately and the notice
   says it was queued. If an earlier task is still working on this VM, the host runs
   the power task after it finishes; you do not need to keep pressing Start.
3. For a running VM, select **Shut down**, **Reboot**, **Force reboot** or **Force stop**.
   A confirmation dialog opens; read it, then select **Confirm shut down**, **Confirm reboot**,
   **Confirm force reboot** or **Confirm force stop**. **Cancel** or Escape closes the dialog
   without any request. If the Controller rejects a request the dialog stays open (for Start,
   it opens) and shows the error; confirming again retries the same request. An already-pending
   task is queued behind automatically on an updated host, without opening a busy-error dialog.
4. Follow the `vm-power` task under **Recent tasks** or **Task history**, then check the
   observed state under the VM's name after the next host report.
5. After you install a guest from an ISO, open the VM's **General** tab. While the VM is
   running with its install ISO still in the CD drive ahead of every bootable disk, the
   page offers **Eject install media** above the details, with the reason: every start
   shows "Press any key to boot from CD or DVD", and a key press in the console starts the
   installer again. Select it once the guest is installed, and only then: ejecting while an
   installer is still running interrupts it. Nex never ejects by itself and never rewrites
   the VM's definition for you.
6. The eject is the same durable `vm-media` task as the console's **Eject** control. Follow
   it under **Recent tasks**; the offer disappears once the host reports the empty drive.
   The ISO stays in the pool's library and can be inserted again from the **Console** tab.

## Expected result

The task shows `succeeded` when the host issued the request. Within a host report interval
the observed state changes to `running` after Start, or `shut off` after Force stop. After
Shut down or Reboot the state follows what the guest does: a guest that handles ACPI shuts
down or restarts; the Debian installer boot menu, for example, stays running. Force reboot
resets a running guest immediately; the VM stays in the running set unless the guest fails
to come back. Queue order waits for task completion; it does not wait for an ACPI shutdown
to finish. Check observed state before asking for the next state change. **Activity**
records `vm-power` with the administrator.

After **Eject install media**, the `vm-media` task shows `succeeded` when the host issued the
change, the **Install ISO** row becomes `None · CD drive empty`, and the offer disappears at
the next host report. The boot order is deliberately unchanged: the empty CD drive keeps its
place, so the firmware skips it and boots the disk, and inserting an ISO again needs no edit.

## Troubleshooting

- **Power buttons are greyed with "Select a virtual machine in Resources.":** no VM is
  selected; the toolbar is fixed and only acts on the selected VM.
- **Buttons are disabled with "Power controls need a freshly connected host":** the host
  has not reported within the freshness window. Restore host connectivity; the VM's actual
  state is unchanged by the Controller connection.
- **"The host agent does not support central power requests":** upgrade Nex Core on that
  host to a build reporting `vm-power-v1`.
- **"still pending; upgrade Nex Core to queue power requests":** this host runs an older
  Core. Upgrade Controller and Core together to queue power behind existing tasks.
- **Task is waiting:** follow the earlier task in Recent tasks. Once the host receives your
  request, it retains it across browser closure, Controller outage and agent restart.
  A replacement Controller recovers the host's queue. Acceptance before host delivery does
  not establish that the host has saved it.
- **A prerequisite task failed or needs attention:** the queued power task fails without
  issuing power. Resolve the earlier task, then submit a new power request.
- **Task reports `failed`:** read its error. Common causes are a VM in a paused or
  transitional state, or a prerequisite that failed before the power request was issued.
  Inspect the VM on its host before retrying.
- **Start fails with *insufficient available memory to start this VM including 2 GiB host
  reserve*:** the host's currently available memory cannot meet the VM's memory size plus
  2 GiB for the host. No start was issued and the VM stays stopped. Stop or shrink other VMs
  on that host, or edit this VM to a smaller memory size, then start it again.
  A pool autostart refused this way is not retried until the host boots again; start the VM
  yourself once memory is available.
- **Task reports `needs_attention`:** the host was interrupted, or the libvirt call failed or timed out
  after the request could have taken effect, and the observed state cannot confirm the outcome (for example a reboot or force-reboot, or a shutdown of a VM
  still running). Check the VM's current state and submit a new request; Nex never
  re-issues an interrupted power request on its own.
- **Shut down or Reboot succeeded but the VM is still running:** the guest ignored the
  ACPI request. Use the console to shut down from inside the guest, **Force reboot** for a
  hard reset while keeping the VM running, or **Force stop**, which cuts power immediately
  and can lose unsaved guest data.
- **Force stop of the Controller appliance:** stopping the appliance VM interrupts central
  management; recover it from the host-local page.
- **No Eject install media offer, but the VM still boots from the ISO:** the offer appears
  only for a running VM with a fresh host report whose recorded install ISO is ahead of a
  bootable disk, on a host reporting `vm-media-v1`. Use the console's **Eject** control, or
  **Edit** to change the CD drive boot order, when the offer does not apply — for example
  while the VM is stopped, when no disk has a boot order yet, or when the guest already
  opened its own tray.
- **The eject task reports `failed` or `needs_attention`:** the message is shown with the
  offer. The recorded ISO is unchanged unless the host reported the change, so check the VM
  on its host, then use the offer or the console control again; the retry is the same
  request, not a second change.

## Verification

Start, Reboot and Force stop were walked through in a disposable nested-KVM lab; ACPI shutdown of
a cooperating guest and physical hardware were not. Queueing was checked with simulation. See the
[console QA report](../qa/console/checks.md), [toolbar QA report](../qa/controller-toolbar/checks.md)
and [power queue checks](../qa/power-queue/checks.md).

The **Eject install media** offer has been exercised only against the Controller UI rendered
with fixed sample data in a browser (the offer's wording, its conditions and the greyed cases)
and by the `web/src/managed.test.mjs` regression check. Ejecting the ISO of a real installed
guest through this offer, and the resulting boot without the "Press any key" prompt, have not
been run on a host yet; that walkthrough is outstanding.
