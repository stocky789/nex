# Open a VM console

## Status

This development workflow opens the graphical console of a **running** virtual machine
in the browser, from Nex Controller and from the host-local Nex Core page. Both use the
VM's local VNC display on its host; nothing listens for VNC on the network. The console
shows what a monitor attached to the VM would show, so it works before any guest
operating system or agent is installed.

A VM created through Controller starts stopped; start it with the
[Power controls](vm-power.md) on its page first. The host-local page offers local power
only for the Controller appliance it deployed. Consoles are development features verified
in a disposable nested-KVM lab; see [Verification](#verification).

## Prerequisites

- **Controller console:** administrator access to [Nex Controller](manage-controller.md),
  a freshly connected host running matching Nex Core components (the host reports the
  `console-v1` capability), and a VM that Controller knows: an adopted Controller appliance
  or a VM [created through Controller](create-virtual-machine.md) whose creation task
  succeeded. The VM must be observed **running** in a fresh host report.
- **Host-local console:** the host's root password and the local page described in
  [Deploy the Controller appliance](deploy-controller-appliance.md); the VM must be running
  on that host with the local VNC display Nex defines for every VM it creates.
- A browser with WebSocket support (all current browsers). Keep the same browser tab
  open; a console ticket expires after 60 seconds if the stream is not opened.

## Steps

### From Nex Controller

1. Sign in, select the VM in **Resources**, and open its **Console** tab. Opening the tab
   connects: Controller checks the host connection, the host's console support and the
   VM's observed state, then waits up to 20 seconds for the host to attach the display.
   The status badge changes to **connected** and the screen shows the VM display scaled
   to fit the panel. If the VM is not running, the tab explains why and connects on its
   own once a fresh report shows it running.
2. Click inside the screen or select **Focus console** to give the VM keyboard focus.
   Keys and mouse input go to the guest. Use **Send Ctrl+Alt+Del** for that sequence, and
   **Full screen** to enlarge the display; press Escape to leave full screen.
3. Leave the tab, select another resource, or close the page to end the console. The VM
   keeps running. If the link drops while the VM is still eligible, the console
   reconnects by itself every few seconds with a new ticket.

### From the host-local page

1. Sign in to the host's local page as `root`. In **Virtual machines**, each running VM
   shows **Open console** in the **Console** column; stopped VMs show **Start the VM first**.
2. Select **Open console**. The console connects immediately and behaves as described
   above. Select **Back to overview** to end it and return.

## Expected result

The console shows the VM's current display, for example a firmware boot menu, an
installer, or a login prompt, and reacts to keyboard input within a moment. Controller's
**Activity** view records `console.open` with the VM identity and administrator.
Leaving the console leaves the VM running. While a reconnect is pending the badge shows
**disconnected** and the status line says it is reconnecting.

## Troubleshooting

- **"The VM is observed shut off"** or **Start the VM first:** the VM is not running.
  Use **Start** in the Power panel (Controller) or start it on its host; the Console tab
  connects on its own once a fresh report shows the VM running. Controller never starts
  a VM as a side effect of opening its console.
- **"The VM's host is not freshly connected":** the host has not reported within the
  freshness window. Restore host connectivity; running VMs are not affected by the
  Controller connection. Use the host-local page for console access during an outage.
- **"The host agent does not support browser consoles":** the host runs an older Nex
  Core agent. Upgrade Core to a build that reports `console-v1`.
- **"The host did not connect the console in time":** the host received the request but
  could not attach the display within 20 seconds, usually because the VM stopped, has no
  local VNC socket, or the host lost its Controller connection. Check the host's agent
  log for `console for VM …` and confirm the VM is running; the tab retries every few
  seconds, and each attempt uses a new ticket.
- **"Console connection ended unexpectedly. Reconnecting…":** the WebSocket closed
  without a normal close, for example because the VM was stopped, the host tunnel
  dropped, or Controller restarted. The tab reconnects automatically while the VM stays
  eligible; if the VM stopped or the host went stale, the tab shows that instead. An
  in-memory console session does not survive a Controller restart.
- **Keyboard input does not reach the VM:** click inside the screen first. Browser
  shortcuts such as Ctrl+W are still handled by the browser; use the toolbar for
  Ctrl+Alt+Del.
- **The display is not visible after a hardware passthrough change:** the browser console
  shows the virtual display only; it does not mirror a physical GPU output.
- **Wrong VM or unexpected access:** console tickets are bound to the requesting
  administrator session and VM and are single-use; the host only ever connects outbound
  to its enrolled Controller. Revoking a host ends its ability to attach consoles.

## Verification

Executed checks are recorded in the [console QA report](../qa/console/checks.md): Go
tests for the WebSocket bridge, ticket authorization, host pairing and relay, and the
host-local bridge; contract and documentation checks; and browser walkthroughs of the
Controller **Console** tab and the host-local **Open console** flow against a running
guest in a disposable nested-KVM lab. Hosted CI execution, physical hardware, multiple
concurrent viewers and long-running sessions have not been exercised.
