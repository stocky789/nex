# Control VM power

## Status

This development workflow starts, shuts down, reboots, force-reboots and force-stops a
virtual machine from Nex Controller. Each request becomes a durable task for the VM's
assigned host; the host's next report shows the resulting state. Shut down and Reboot are
ACPI requests that a guest without ACPI support, such as a firmware boot menu, ignores.
Force reboot is a hard guest reset, not ACPI. Nex VMs are defined without ACPI sleep or
hibernate, so a guest never offers them; a guest that slept anyway is shown as asleep and
**Start** wakes it. It also covers the offer to eject install
media once a guest is installed, so later starts no longer boot the ISO first. Editing a VM is covered in
[Edit a virtual machine](edit-virtual-machine.md); deletion is covered in
[Delete a virtual machine](delete-virtual-machine.md).
Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md) and a VM Controller knows:
  an adopted Controller appliance or a VM [created through Controller](create-virtual-machine.md)
  whose creation task succeeded.
- The VM's host freshly connected, running matching Nex Core components (the host reports
  the `vm-power-v1` capability), with a fresh libvirt observation of the VM.
- In the recovery development build, the current pool master must also be reachable for a
  managed VM's Start, wake, Reboot, Force reboot or media change, including VMs on local storage.
  Shut down and Force stop remain available through the assigned host during master loss.

## Steps

1. Sign in, choose the VM's pool (or its site's **all pools**) in the top-bar scope menu,
   select **VMs** in the rail and select the VM's row. Its panel opens with the observed state
   as a badge beside the name (**Unknown** while its host is not reporting). The panel's quick
   actions hold **Start** and **Shut down**; **More** holds **Reboot**, **Force stop** and
   **Force reboot**. Only the actions that fit the observed state are enabled, and hovering a
   greyed action shows the reason, for example "Power controls need a freshly connected host."
   **Migrate** beside them is not available yet (Phase 7).
2. Select **Start** for a stopped VM, or for a VM observed `asleep (the guest suspended
   itself; Start wakes it)`: the same button wakes a sleeping guest instead of starting a
   second copy. The request is submitted immediately and the notice
   says it was queued. If an earlier task is still working on this VM, the host runs
   the power task after it finishes; you do not need to keep pressing Start.
3. For a running VM, select **Shut down**, **Reboot**, **Force reboot** or **Force stop**.
   A confirmation dialog opens; read it, then select **Confirm shut down**, **Confirm reboot**,
   **Confirm force reboot** or **Confirm force stop**. **Cancel** or Escape closes the dialog
   without any request. If the Controller rejects a request the dialog stays open (for Start,
   it opens) and shows the error; confirming again retries the same request. An already-pending
   task is queued behind automatically on an updated host, without opening a busy-error dialog.
4. Follow the `vm-power` task on **Tasks** (the rail item, or the activity pill in the top
   bar), then check the state badge beside the VM's name after the next host report.
5. After you install a guest from an ISO, open the VM panel's **Overview** tab. While the VM
   is running with its install ISO still in the CD drive ahead of every bootable disk, the
   **Install media** card offers **Eject install media**, with the reason: every start boots
   the ISO first, so the installer starts again, or the VM waits at a boot prompt such as
   "Press any key to boot from CD or DVD" on BIOS VMs or Windows media. Select it once the
   guest is installed, and only then: ejecting while an
   installer is still running interrupts it. Nex never ejects by itself and never rewrites
   the VM's definition for you.
6. The eject is the same durable `vm-media` task as the console's **Eject** control. Follow
   it on **Tasks**; the offer disappears once the host reports the empty drive.
   The ISO stays in the pool's library and can be inserted again from the **Install media**
   card or the console's CD/DVD control.
7. To start or shut down several VMs at once, select their checkboxes in the **VMs** list and
   choose **Start** or **Shut down** on the bar above the table. One confirmation dialog lists
   every selected VM with *Will be requested* or the reason it is skipped (for example a VM
   already in that state, a stale host, or the Controller appliance, which is left to its own
   panel). Each VM gets its own request; confirming again after a failure retries only the
   failed ones. **Snapshot** on that bar takes a stopped snapshot for every selected shut-off
   VM that is eligible (enter one shared name; running guests, unknown/stale VM state, hosts
   that are not freshly connected, and the Controller appliance are skipped — use each VM's
   Snapshots tab for running capture). **Migrate** is not available yet (Phase 7).

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
change, the **Install ISO** row on the **Configuration** tab's **Definition** card becomes
`None · CD drive empty`, and the offer disappears at
the next host report. The boot order is deliberately unchanged: the empty CD drive keeps its
place, so the firmware skips it and boots the disk, and inserting an ISO again needs no edit.

Managed pool autostart in the recovery development build follows the same ownership admission.
Core retains the desired setting and makes one attempt per VM per host boot; native libvirt
autostart stays disabled. If the attempt is refused, resolve the reported cause and use **Start**.
The separately managed Controller appliance retains its own boot behavior.

## Troubleshooting

- **Buttons are disabled with "Power controls need a freshly connected host":** the host
  has not reported within the freshness window. Restore host connectivity; the VM's actual
  state is unchanged by the Controller connection.
- **"The host agent does not support central power requests":** upgrade Nex Core on that
  host to a build reporting `vm-power-v1`.
- **"still pending; upgrade Nex Core to queue power requests":** this host runs an older
  Core. Upgrade Controller and Core together to queue power behind existing tasks.
- **Task is waiting:** follow the earlier task on **Tasks**. Once the host receives your request, it retains it across browser closure,
  Controller outage and agent restart.
  A replacement Controller recovers the host's queue. Acceptance before host delivery does
  not establish that the host has saved it.
- **A prerequisite task failed or needs attention:** the queued power task fails without
  issuing power. Resolve the earlier task, then submit a new power request.
- **The pool master cannot confirm ownership:** restore connectivity to the current master
  before retrying Start, Reboot, Force reboot or a media change. An unreachable master does
  not mean the VM stopped. Shut down and Force stop can still be requested on its assigned host.
- **Snapshot recovery is pending:** conflicting changes remain blocked until the host confirms
  the recovery outcome. A timeout does not release this protection; inspect the reported task
  and VM state before retrying. Emergency Shut down and Force stop remain available.
- **The VM is observed asleep and its console is black:** the guest put itself to sleep
  (Windows Sleep, for example) on a definition written before Nex disabled guest sleep. Select
  **Start** to wake it. **Shut down** and **Reboot** wake the guest first and then send the
  ACPI request; **Force stop** and **Force reboot** work as usual. Editing or deleting the VM
  is refused until it is awake or stopped. Once the guest is up, use **Edit hardware** and save the VM so
  its definition is rewritten without sleep support; the change applies at its next boot.
  Other VMs on the host are not affected by a sleeping guest.
- **Task reports `failed`:** read its error. Common causes are a VM in a paused or
  transitional state, or a prerequisite that failed before the power request was issued.
  Inspect the VM on its host before retrying.
- **Start fails with *insufficient available memory to start this VM including 2 GiB host
  reserve*:** the host's currently available memory cannot meet the VM's memory size plus
  2 GiB for the host. No start was issued and the VM stays stopped. Stop or shrink other VMs
  on that host, or edit this VM to a smaller memory size, then start it again.
  A pool autostart refused this way is not retried until the host boots again; start the VM
  yourself once memory is available.
  In recovery development builds, reconciliation of this failed start is under integration.
  If a later edit is refused because the VM is still reserved as starting, retain the failed
  task and keep the VM stopped for investigation. Earlier development requests can lack
  the evidence needed to release that reservation safely; Force stop succeeding does not
  establish that the pool master has reconciled it.
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
  bootable disk, on a host reporting `vm-media-v1`. Use the **Install media** card's or the
  console's **Eject** control, or **Edit hardware** to change the CD drive boot order, when the offer does not apply — for example
  while the VM is stopped, when no disk has a boot order yet, or when the guest already
  opened its own tray.
- **The eject task reports `failed` or `needs_attention`:** the message is shown with the
  offer. The recorded ISO is unchanged unless the host reported the change, so check the VM
  on its host, then use the offer or the console control again; the retry is the same
  request, not a second change.

## Verification

The recovery development admission changes (2026-09-25) have focused agent checks for
local-storage power, appliance exclusion, pending recovery, emergency stop and Core autostart;
the protocol and agent race suites pass. These checks use simulated peers and do not establish
a native master-outage or browser walkthrough of the new refusals; those remain open.

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

The [Controller UI modernization checks](../qa/controller-ui-modernization/checks.md) (2026-09-23) re-ran the power flow from the earlier layout's toolbar in the phase 1 browser lab with simulated Core responses at 1440 and 360 px. The icon-only power buttons between 901 and 1280 px were checked by separate browser measurements, which no committed test repeats.

Guest sleep handling (2026-09-17) is covered by runnable checks only: the Core power engine
test wakes a `pmsuspended` domain for Start, wakes then requests for Shut down and Reboot,
and destroys or resets it directly; the pool admission test proves a sleeping shared VM no
longer blocks inventory and is admitted as a wake without a start permit; the UI check proves
the earlier layout's toolbar offered Start and the running-VM actions while asleep. The originating lab case (a
Windows 11 guest asleep on SMB storage, every power request and console refused, the host's
other VM unable to start) was diagnosed through the Controller and host-local APIs but the fix
has not yet been run against that host; a browser walkthrough of the wake is outstanding.


The [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) (`0.2.10~gate5`, 2026-09-17,
disposable nested-KVM lab, headless Chromium) walked steps 1–4 in the browser from the earlier layout's toolbar on
an installed Debian UEFI guest, through a replacement Controller: Escape on **Shut down** and on
**Force stop** sent nothing, and Shut down (ACPI), Start, Force stop, Start, Reboot and Force reboot
each sent one request, showed the queued notice and settled to the host's outcome, with the ACPI
confirmations styled primary and the force confirmations styled as danger. It also confirmed that
power controls are disabled while the host is stale. The VM header can lag the settled state until
the next 10 s refresh (open defect). Physical hardware and the power queue behind pending tasks
were not exercised.

Earlier, Start, Reboot and Force stop were walked through in a disposable nested-KVM lab. Queueing was checked with simulation. See the
[console QA report](../qa/console/checks.md), [toolbar QA report](../qa/controller-toolbar/checks.md)
and [power queue checks](../qa/power-queue/checks.md).

The **Eject install media** banner was shown on a running installed guest in the gate5 run
after the ISO was inserted, and its **Eject** cleared it (its text was logged; the capture
scrolled past the banner). The boot without the
"Press any key" prompt after that eject has not been run on a host: the gate guest used a UEFI
ISO with no such prompt, so the BIOS walkthrough is still outstanding. The offer's greyed cases
are covered only by fixed sample data in a browser and the `web/src/managed.test.mjs` check.
