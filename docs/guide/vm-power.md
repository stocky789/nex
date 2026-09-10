# Control VM power

## Status

This development workflow starts, shuts down, reboots and force-stops a virtual machine
from Nex Controller. Each request becomes a durable task for the VM's assigned host;
the host's next report shows the resulting state. Shut down and Reboot are ACPI requests
that a guest without ACPI support, such as a firmware boot menu, ignores. Editing and
deleting VMs are not yet available. Verified in a disposable nested-KVM lab; see
[Verification](#verification).

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md) and a VM Controller knows:
  an adopted Controller appliance or a VM [created through Controller](create-virtual-machine.md)
  whose creation task succeeded.
- The VM's host freshly connected, running matching Nex Core components (the host reports
  the `vm-power-v1` capability), with a fresh libvirt observation of the VM.

## Steps

1. Sign in, select the VM in **Resources**. The line under the VM's name shows the observed
   state and its freshness, for example `Observed running · fresh`, followed by the reason
   when power requests are unavailable. The toolbar's **Start**, **Shut down**, **Reboot**
   and **Force stop** buttons act on this VM; only the ones that fit the observed state are
   enabled, and hovering a greyed button shows the same reason.
2. Select **Start** for a stopped VM. The request is submitted immediately and the notice
   says it was requested.
3. For a running VM, select **Shut down**, **Reboot** or **Force stop**. A confirmation
   dialog opens; read it, then select **Confirm shut down**, **Confirm reboot** or
   **Confirm force stop**. **Cancel** or Escape closes the dialog without any request. If
   the Controller rejects a request the dialog stays open (for Start, it opens) and shows
   the error; confirming again retries the same request.
4. Follow the `vm-power` task under **Recent tasks** or **Task history**, then check the
   observed state under the VM's name after the next host report.

## Expected result

The task shows `succeeded` when the host issued the request. Within a host report interval
the observed state changes to `running` after Start, or `shut off` after Force stop. After
Shut down or Reboot the state follows what the guest does: a guest that handles ACPI shuts
down or restarts; the Debian installer boot menu, for example, stays running. **Activity**
records `vm-power` with the administrator.

## Troubleshooting

- **All four buttons are greyed with "Select a virtual machine in Resources.":** no VM is
  selected; the toolbar is fixed and only acts on the selected VM.
- **Buttons are disabled with "Power controls need a freshly connected host":** the host
  has not reported within the freshness window. Restore host connectivity; the VM's actual
  state is unchanged by the Controller connection.
- **"The host agent does not support central power requests":** upgrade Nex Core on that
  host to a build reporting `vm-power-v1`.
- **"a power request for this VM is still pending":** one request per VM is in flight at a
  time. Wait for its task to finish, then submit again.
- **Task reports `failed`:** read its error. Common causes are a VM in a paused or
  transitional state, or libvirt refusing the request on the host. Inspect the VM on its
  host before retrying.
- **Task reports `needs_attention`:** the host was interrupted around the request and the
  observed state cannot confirm the outcome (for example a reboot, or a shutdown of a VM
  still running). Check the VM's current state and submit a new request; Nex never
  re-issues an interrupted power request on its own.
- **Shut down or Reboot succeeded but the VM is still running:** the guest ignored the
  ACPI request. Use the console to shut down from inside the guest, or **Force stop**,
  which cuts power immediately and can lose unsaved guest data.
- **Force stop of the Controller appliance:** stopping the appliance VM interrupts central
  management; recover it from the host-local page.

## Verification

The toolbar placement and confirmation dialog were exercised in the
[toolbar QA report](../qa/controller-toolbar/checks.md). The power behavior itself is
recorded in the [console QA report](../qa/console/checks.md): Go tests
for the host power execution, interruption reconciliation and Controller authorization,
replay and dispatch; contract and documentation checks; and the browser walkthrough of
Start, Reboot (ignored by the boot menu) and Force stop against a real guest in a
disposable nested-KVM lab. Shut down of a guest that honors ACPI, physical hardware and
hosted CI were not exercised.
