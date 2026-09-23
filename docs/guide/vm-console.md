# Open a VM console

## Status

This development workflow opens the graphical console of a **running** virtual machine
in the browser, from Nex Controller and from the host-local Nex Core page. Both use the
VM's local VNC display on its host; nothing listens for VNC on the network. The console
shows what a monitor attached to the VM would show, so it works before any guest
operating system or agent is installed. With the [guest tools](guest-tools.md) installed,
the console also exchanges clipboard text with the guest and can resize its display.

A VM created through Controller starts stopped. Open the **Console** tab first and use
**Start VM** on the console toolbar: the tab is already open, so it attaches as the VM boots,
in time to answer *Press any key to boot from CD or DVD*, which waits about five seconds. The
[Power controls](vm-power.md) in the page toolbar start the VM too, but a console opened after
that start usually attaches too late for the prompt. The host-local page offers local power
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

To watch usage while operating the VM, expand **Realtime** beneath the display and tick
**Show realtime stats (1-second samples)**. The display stays above the charts; a longer
chart panel scrolls independently. This also works in the separate VM console window.
Closing the drawer or leaving the console stops requests. Sampling pauses in a hidden
browser page and turns off after ten minutes. Historical charts remain in **Stats**;
host consoles and the host-local console are unchanged. See [usage history](usage-history.md).

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
2. If the VM is not running, select **Start VM** on the console toolbar and keep watching the
   screen. The tab connects as soon as the host reports the VM running, which is early enough
   to answer *Press any key to boot from CD or DVD* or to work a firmware menu; hold the key
   down if you want to be certain. The button is shown only while the VM is not running and is
   unavailable while a start is in progress; it disappears once the VM is running. Starting the
   VM elsewhere and opening the console afterwards usually misses the prompt, and a UEFI guest
   with an empty disk then stops at *BdsDxe: No bootable option or device was found* or the
   firmware front page. From there, use the firmware's own boot menu, or shut the VM down and
   start it again from this toolbar.
3. Use the CD/DVD controls on the console toolbar to insert an ISO from the pool library that
   is already on this VM's host, or **Eject** to clear a tray. A VM has two drives:
   **Install media** and **Driver disc**, each with its own list and **Eject**, so a driver
   ISO (for example virtio-win) can stay in the second drive while the installer runs from the
   first. A host running an older Nex Core reports one drive and shows a single **CD/DVD**
   control, as before. Linux guests usually see `/dev/sr0` (often also `/dev/cdrom`); Windows
   guests see a DVD drive letter per drive. A running VM that was created without any ISO
   needs to be shut down once before the first insert so the host can add a CDROM tray.
   **Inserting the install media…** or **Ejecting the driver disc…** clears when the task
   succeeds or the host reports the requested tray contents. If the task fails or needs
   attention, its reason remains visible; inspect it in the **Tasks** strip at the bottom of
   the page (select it to expand; it shows the last five tasks and a count of running and
   failed ones).
   This works the same for a VM whose disks are on pool-shared storage; Core records the
   change on its pool master, so the ISO is still attached after the VM is re-adopted. The
   selected host and pool master must be upgraded for master-owned placement; a queued media
   task can still fail when Core validates the current tray or ISO state.
4. A guest can eject its own disc: the Debian installer does after it has copied the base
   system, and then asks you to reinsert it. The control shows the ISO Nex recorded with
   **(ejected in the VM)** and says *The VM ejected the disc. Insert it again to reconnect
   it.* Select the same ISO in the list to put it back in the drive, then continue in the
   guest. Nex does not change what it recorded because a guest opened the tray, and does
   not need the VM stopped. A guest's own eject does not survive a restart: the reset closes
   the tray with the recorded ISO still inserted, and because the CD drive keeps its boot
   position the VM boots the installer again. When the installer has finished and asks you to
   remove the disc before it restarts, select **Eject** on **Install media** first and wait for
   **Empty**, then continue in the guest.
5. Click inside the screen or select **Focus console** to give the VM keyboard focus.
   Keys and mouse input go to the guest; the pointer is absolute in VMs created with the
   USB tablet device, so it does not drift. Use **Send Ctrl+Alt+Del** for that sequence
   and **Send key…** for combinations the browser would otherwise keep, such as
   Ctrl+Alt+F1–F7 (Linux text consoles), Ctrl+C (interrupt a command, mainly on the host
   console), Alt+Tab, Alt+F4, Ctrl+Esc, the Windows key and Escape.
6. Choose how the display is shown with **View**: **Fit to window** scales it into the
   panel, **Actual size** shows it 1:1 with scrolling, and **Resize guest to window** asks
   the guest to change its resolution to the panel size. That last option is offered only
   while the guest reports that it can act on the request: the VM needs a VirtIO display and
   a running guest agent to apply the resolution, which is what the
   [guest tools](guest-tools.md) install and the same condition that makes the clipboard
   **ready**. Until then — during installation, or on a guest without the tools — the option
   reads **Resize guest to window (guest cannot resize)** and cannot be chosen. Where there is
   no guest report at all, such as a host console or a VM whose host has not reported yet, it
   reads **(no guest report)**. The note under the display says which case applies. The View
   choice is remembered in this browser.
7. Exchange text with the guest through **Clipboard**:
   - **Read my clipboard** loads the browser clipboard into the text box (the browser asks
     for permission once); you can also paste or type into the box.
   - **Paste into VM** places the text on the guest clipboard. It needs the
     [guest tools](guest-tools.md) with a graphical session; the box above the text says
     whether the guest is **ready**.
   - **Type into VM** sends the text as keystrokes instead. It works in any guest, including
     installers and text consoles, at about 40 characters per second; newlines press Enter.
     Nex presses the keys a US keyboard would, holding Shift for capitals and for shifted
     symbols such as `:`, `_`, `~` and `"`, so paths, passwords and commands arrive as typed
     in a guest using a US layout. A guest set to another keyboard layout still interprets the
     key positions its own way; use **Paste into VM** there.
   - Text copied inside the guest appears under **Latest text copied inside the VM**;
     select **Copy** to place it on your clipboard. The guest never writes your clipboard
     on its own.
   - The clipboard panel sits between the toolbar and the display and shares the panel's
     height with it, so the display stays visible while you send text. Select **Clipboard**
     again to close it, or collapse the toolbar, to give the display the whole panel.
8. **Screenshot** downloads a PNG of the current display named after the VM. **View only**
   stops keyboard and mouse input while you watch. **Reconnect** ends the current stream
   and attaches again with a new ticket. **Full screen** enlarges the panel with its
   toolbar; press Escape or the button again to leave. **Focus mode** keeps the browser
   window but hides the Controller around the console: the menus, toolbar, resource tree,
   tabs and Tasks strip. Select **Leave focus mode**, or press Escape while the display does
   not have the keyboard, to bring them back. **Open in new window** opens a
   console-only page (`#/console/<VM>`) in a separate tab or window.
9. Leave the tab, select another resource, or close the page to end the console. The VM
   keeps running. If the link drops while the VM is still eligible, the console
   reconnects by itself every few seconds with a new ticket.

### From a host in Nex Controller

1. Select the host in **Resources** and open its **Console** tab. The same console client
   connects to the host itself (a root shell on Nex Core through the same display path as a
   VM console). The host must be freshly connected and report `host-console-v1`.
2. Type as on a local terminal. **Open in new window** uses `#/host-console/<host-id>`.
   A host console has no guest report, so it offers no CD/DVD drives, no **Start VM** and no
   **Resize guest to window**; the other controls behave as above.
3. Leave the tab when finished; the host keeps running.

### From the host-local page

1. Sign in to the host's local page as `root`. In **Virtual machines**, each running VM
   shows **Open console** under **Local control**; stopped VMs show
   **Console: start the VM first**.
2. Select **Open console**. The console connects immediately and behaves as described
   above. Select **Back to overview** to end it and return.

## Expected result

The console shows the VM's current display, for example a firmware boot menu, an
installer, or a login prompt, and reacts to keyboard input within a moment. Controller's
**Activity** view records `console.open` with the VM identity and administrator.
Leaving the console leaves the VM running. While a reconnect is pending the badge shows
**disconnected** and the status line says it is reconnecting.

## Troubleshooting

- **"The VM is observed shut off"** or **Console: start the VM first:** the VM is not running.
  Use **Start VM** on the console toolbar, **Start** in the page toolbar, or start it on its
  host; the Console tab connects on its own once a fresh report shows the VM running.
  Controller never starts a VM as a side effect of opening its console.
- **The VM is observed asleep and the console shows nothing or refuses to open:** the guest
  put itself to sleep (for example Windows Sleep) on a definition written before Nex disabled
  guest sleep. Select **Start** in the toolbar to wake it, wait for the next host report, then
  connect; see [Control VM power](vm-power.md#troubleshooting).
- **The boot prompt is gone, or the guest stops at "No bootable option or device was found":**
  the *Press any key to boot from CD or DVD* prompt waits about five seconds, which a console
  opened after the start usually misses; the same happens when Setup reboots by itself. Shut
  the VM down, open the **Console** tab, and use **Start VM** on the console toolbar with a
  hand on a key. From the firmware front page you can also choose the DVD entry in the
  firmware's own boot menu.
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
- **The VM boots the installer again after installation:** the ISO is still recorded in
  **Install media**, and the guest's restart reinserted it. Select **Eject** on
  **Install media**, wait for **Empty**, then restart the guest (for example with
  **Force reboot**). The VM then boots from its disk.
- **Eject fails with "stop the VM first to change the boot order":** the VM's host runs a
  Nex Core older than 0.2.4, which recorded an eject as taking the CD drive out of the boot
  order and so refused it on a running VM whose disc boots first. Ejecting never changes the
  boot order: the empty drive keeps its place. Update Nex Core on that host from
  [Software updates](software-updates.md), then eject with the VM running. Ejecting from a
  stopped VM on the older version does work, but it takes that VM's CD drive out of the boot
  order; set **CD drive boot order** again in [Edit VM](edit-virtual-machine.md) if you want
  the drive to boot first.
- **Paste into VM does nothing in the guest:** the guest is not running both spice-vdagent
  and qemu-guest-agent (**Waiting for guest tools (qemu-guest-agent and spice-vdagent) in the
  guest**), or
  the VM was created before clipboard support (**Channel missing** in its Guest tools
  panel). Install the [guest tools](guest-tools.md) or use **Type into VM**.
- **Resize guest to window cannot be chosen:** the guest has not reported that it can act on
  a resize request — an installer before drivers load, or a guest without the
  [guest tools](guest-tools.md), whose agent is what applies the new resolution. The option
  stays visible as **(guest cannot resize)**, or as **(no guest report)** where nothing has
  reported at all, and the note under the display explains it. Install the guest tools; until
  then the console scales the display with **Fit to window**. Nex does not offer the option on
  the strength of the VM having a VirtIO display, because that alone never resized a guest.
- **Typed text arrives with wrong characters:** keystrokes are key presses, interpreted with
  the guest's keyboard layout. Nex presses US-layout positions, with Shift for capitals and
  shifted symbols; a guest set to another layout produces that layout's characters for those
  positions, and characters on no US position cannot be typed. Use **Paste into VM** with the
  guest tools there. A Windows guest that received lowercase text, `-` for `_` or `;` for `:`
  is running a Controller older than this fix.
- **Setup lists no disks for a VirtIO disk:** insert the virtio-win ISO in the **Driver disc**
  drive and load the driver from it in Setup, leaving the Windows ISO in **Install media**. On
  a host that reports only one **CD/DVD** drive, update Nex Core on that host from
  [Software updates](software-updates.md) to gain the second drive.
- **Inserting or ejecting the driver disc fails with "invalid JSON request":** the Controller
  predates driver disc support in its media request (the 0.2.9 release and earlier). Update Nex
  Controller; after that, insert and eject the driver disc from the console's **Driver disc**
  control, while the install drive keeps working throughout.
- **"the VM's host agent does not support a driver disc drive; upgrade Nex Core":** the VM's host
  runs a Nex Core without the second CD drive. Update Nex Core on that host from
  [Software updates](software-updates.md), or use the **Install media** drive.
- **"the driver disc must differ from the install media":** the ISO you chose is already in the
  VM's other drive. Pick a different ISO, or eject that drive first and then insert the ISO where
  you want it.
- **"the driver disc ISO is not registered and verified on the VM's host and pool"** (or "the
  install ISO …"): the ISO is still importing, failed, or lives on another host. Wait for its
  import to finish in the ISO library, or import it to this VM's host.
- **Reading your clipboard is refused:** the browser denied clipboard access or the page is
  not on HTTPS. Paste into the text box manually.
- **The display is not visible after a hardware passthrough change:** the browser console
  shows the virtual display only; it does not mirror a physical GPU output.
- **Wrong VM or unexpected access:** console tickets are bound to the requesting
  administrator session and VM and are single-use; the host only ever connects outbound
  to its enrolled Controller. Revoking a host ends its ability to attach consoles.

## Verification

**Focus mode** and the grouped console toolbar were checked in the [Controller UI modernization checks](../qa/controller-ui-modernization/checks.md) (2026-09-23) against a console with no VM attached; the connection itself was not exercised.

See [master placement evidence](../qa/2026-09-18-master-vm-placement.md) for the
unreleased admission change. Browser checks use simulated Core responses; native
multi-host outage and recovery acceptance remains open.

Realtime placement and polling controls have [separate simulated-browser evidence](../changes/2026-09-17-console-realtime-stats.json); this does not establish native guest sampling.

VM, host-local and toolbar console flows were walked through against a Debian 13 guest in a
disposable nested-KVM lab; Windows guests, physical hardware, concurrent viewers and long
sessions were not. See the [console QA report](../qa/console/checks.md),
[console features](../qa/console-features/checks.md),
[quality-menu removal](../qa/console-quality/checks.md),
[toolbar collapse](../qa/console-toolbar/checks.md) and
[media status](../qa/console-media-status/checks.md) checks.

**Start VM, the two CD/DVD drives, typing shifted characters and the panel layout are newer
than those walkthroughs.** They were exercised in a browser against the real console component
in a harness with no VM attached, at 1440×1000 and 900×700: typing
`Nex-Test_Upper D:\Path\virtio-win-guest-tools.exe 123` produced key presses that a US-layout
guest reads back unchanged, **Start VM** appeared only while the VM was not running, one-drive
and two-drive toolbars behaved as described above, and the toolbar, an open clipboard and the
display stayed visible together. The [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) (`0.2.10~gate5`, 2026-09-17,
disposable nested-KVM lab, headless Chromium) re-ran the Linux side on a host in the browser:
**Start VM** from the Console toolbar attached in time for the Debian 13.6 UEFI installer menu, a
full install was driven with canvas keys and **Clipboard** > **Type into VM**, the guest's own
eject showed "(ejected in the VM)", and step 4 (**Eject** on **Install media** before rebooting)
booted straight into the installed system. Step 2 did not fully match: the console connected
about 10 s after the host reported the VM running, on the page's next refresh, not at once; this is
an open defect, and a short BIOS "Press any key" prompt could be missed (not testable on that UEFI
guest). Ticket refusals (no session, wrong Origin, reused, unknown, expired and other-VM tickets,
stopped VM) returned their documented errors when checked at the API over HTTP and WebSocket; the
expired-ticket message was not captured in the UI. Windows guests, the BIOS prompt timing,
real-keyboard canvas typing beyond GRUB keys and a second administrator's ticket remain unverified
on a host. The findings
these changes answer are recorded in the
[Windows 11 UEFI lab report](../qa/2026-09-16-windows-11-uefi-lab.md).
