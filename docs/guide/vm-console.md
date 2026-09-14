# Open a VM console

## Status

This development workflow opens the graphical console of a **running** virtual machine
in the browser, from Nex Controller and from the host-local Nex Core page. Both use the
VM's local VNC display on its host; nothing listens for VNC on the network. The console
shows what a monitor attached to the VM would show, so it works before any guest
operating system or agent is installed. With the [guest tools](guest-tools.md) installed,
the console also exchanges clipboard text with the guest and can resize its display.

A VM created through Controller starts stopped; start it with the
[Power controls](vm-power.md) in the toolbar first. The host-local page offers local power
only for the Controller appliance it deployed.

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

Use the small arrow at the top-left edge of the console screen to **Collapse console
toolbar** and give the display more room. Select **Expand console toolbar** to restore
all controls, including any open clipboard draft. Connection status and warnings remain
visible; collapsing does not disconnect or change **View only**. The toolbar starts
expanded whenever you reopen the console. This also works in host consoles, the
host-local console and full-screen or separate-window views.

1. Sign in, select the VM in **Resources**, and open its **Console** tab. Opening the tab
   connects: Controller checks the host connection, the host's console support and the
   VM's observed state, then waits up to 20 seconds for the host to attach the display.
   The status badge changes to **connected** and the screen shows the VM display scaled
   to fit the panel. If the VM is not running, the tab explains why and connects on its
   own once a fresh report shows it running.
2. Use **CD/DVD** on the console toolbar to insert an ISO from the pool library that is
   already on this VM's host, or **Eject** to clear the tray. Linux guests usually see
   `/dev/sr0` (often also `/dev/cdrom`); Windows guests see a DVD drive letter. A running
   VM that was created without any ISO needs to be shut down once before the first insert
   so the host can add a CDROM tray. **Inserting ISO…** or **Ejecting ISO…** clears
   when the task succeeds or the host reports the requested tray contents. If the task
   fails or needs attention, its reason remains visible; inspect it in Recent tasks.
   This works the same for a VM whose disks are on pool-shared storage; the host records
   the change on its pool master, so the ISO is still attached after the VM is re-adopted.
3. A guest can eject its own disc: the Debian installer does after it has copied the base
   system, and then asks you to reinsert it. The control shows the ISO Nex recorded with
   **(ejected in the VM)** and says *The VM ejected the disc. Insert it again to reconnect
   it.* Select the same ISO in the list to put it back in the drive, then continue in the
   guest. Nex does not change what it recorded because a guest opened the tray, and does
   not need the VM stopped.
4. Click inside the screen or select **Focus console** to give the VM keyboard focus.
   Keys and mouse input go to the guest; the pointer is absolute in VMs created with the
   USB tablet device, so it does not drift. Use **Send Ctrl+Alt+Del** for that sequence
   and **Send key…** for combinations the browser would otherwise keep, such as
   Ctrl+Alt+F1–F7 (Linux text consoles), Ctrl+C (interrupt a command, mainly on the host
   console), Alt+Tab, Alt+F4, Ctrl+Esc, the Windows key and Escape.
5. Choose how the display is shown with **View**: **Fit to window** scales it into the
   panel, **Actual size** shows it 1:1 with scrolling, and **Resize guest to window** asks
   the guest to change its resolution to the panel size. The last option is hidden for a VM
   whose host reports no VirtIO display, and takes effect in guests with a virtio-gpu
   driver (Linux, including text consoles; Windows with the virtio-win display driver).
   The View choice is remembered in this browser.
6. Exchange text with the guest through **Clipboard**:
   - **Read my clipboard** loads the browser clipboard into the text box (the browser asks
     for permission once); you can also paste or type into the box.
   - **Paste into VM** places the text on the guest clipboard. It needs the
     [guest tools](guest-tools.md) with a graphical session; the box above the text says
     whether the guest is **ready**.
   - **Type into VM** sends the text as keystrokes instead. It works in any guest, including
     installers and text consoles, at about 40 characters per second; newlines press Enter.
   - Text copied inside the guest appears under **Latest text copied inside the VM**;
     select **Copy** to place it on your clipboard. The guest never writes your clipboard
     on its own.
7. **Screenshot** downloads a PNG of the current display named after the VM. **View only**
   stops keyboard and mouse input while you watch. **Reconnect** ends the current stream
   and attaches again with a new ticket. **Full screen** enlarges the panel with its
   toolbar; press Escape or the button again to leave. **Open in new window** opens a
   console-only page (`#/console/<VM>`) in a separate tab or window.
8. Leave the tab, select another resource, or close the page to end the console. The VM
   keeps running. If the link drops while the VM is still eligible, the console
   reconnects by itself every few seconds with a new ticket.

### From a host in Nex Controller

1. Select the host in **Resources** and open its **Console** tab. The same console client
   connects to the host itself (a root shell on Nex Core through the same display path as a
   VM console). The host must be freshly connected and report `host-console-v1`.
2. Type as on a local terminal. **Open in new window** uses `#/host-console/<host-id>`.
3. Leave the tab when finished; the host keeps running.

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
  Use **Start** in the Controller toolbar or start it on its host; the Console tab
  connects on its own once a fresh report shows the VM running. Controller never starts
  a VM as a side effect of opening its console.
- **"The VM's host is not freshly connected":** the host has not reported within the
  freshness window. Restore host connectivity; running VMs are not affected by the
  Controller connection. Use the host-local page for console access during an outage.
- **"The host agent does not support browser consoles":** the host runs an older Nex
  Core agent. Upgrade Core to a build that reports `console-v1` for VM consoles, or
  `host-console-v1` for the host's own console.
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
- **Keyboard input does not reach the VM:** click inside the screen first, and make sure
  **View only** is off. Browser shortcuts such as Ctrl+W are still handled by the
  browser; use the toolbar for Ctrl+Alt+Del and **Send key…** for other combinations.
- **The guest asks you to reinsert the installation disc:** the guest ejected it itself.
  **CD/DVD** shows the recorded ISO as **(ejected in the VM)**; select the same ISO to put it
  back, then continue in the guest. **Eject** is still available and makes Nex's record match
  the empty drive. If the list shows **Empty** instead, the host has not reported since the
  guest ejected the disc; wait for the next report and try again.
- **Paste into VM does nothing in the guest:** the guest has no running spice-vdagent, or
  the VM was created before clipboard support (**Channel missing** in its Guest tools
  panel). Install the [guest tools](guest-tools.md) or use **Type into VM**.
- **Resize guest to window does not change the resolution:** the guest has no virtio-gpu
  driver (for example an installer before drivers load, or a VM created before this
  feature). The console keeps scaling the display; choose **Fit to window**.
- **Typed text arrives with wrong characters:** keystrokes are interpreted with the
  guest's keyboard layout. Characters outside the guest layout cannot be typed; use
  **Paste into VM** with the guest tools instead.
- **Reading your clipboard is refused:** the browser denied clipboard access or the page is
  not on HTTPS. Paste into the text box manually.
- **The display is not visible after a hardware passthrough change:** the browser console
  shows the virtual display only; it does not mirror a physical GPU output.
- **Wrong VM or unexpected access:** console tickets are bound to the requesting
  administrator session and VM and are single-use; the host only ever connects outbound
  to its enrolled Controller. Revoking a host ends its ability to attach consoles.

## Verification

VM, host-local and toolbar console flows were walked through against a Debian 13 guest in a
disposable nested-KVM lab; Windows guests, physical hardware, concurrent viewers and long
sessions were not. See the [console QA report](../qa/console/checks.md),
[console features](../qa/console-features/checks.md),
[quality-menu removal](../qa/console-quality/checks.md),
[toolbar collapse](../qa/console-toolbar/checks.md) and
[media status](../qa/console-media-status/checks.md) checks.

