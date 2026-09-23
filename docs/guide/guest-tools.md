# Install guest tools and read guest information

## Status

Development feature of matching Nex Controller and Nex Core builds. Every VM created
through Controller carries the host-local channels that the guest tools use: the QEMU
guest agent channel and a clipboard channel for the console. Once the tools run inside
the VM, its panel in Controller shows the operating system, hostname and IP addresses the
guest reports, and the [console](vm-console.md) clipboard works in both directions.

The guest tools are standard packages, not Nex-specific software. VMs created before this
feature keep their original definitions: their panel shows **Clipboard: Not available**
(*Channel missing*) and no display resize until they are recreated.

Display resize is one of the things the tools enable, not something the VM has on its own.
Until the tools run, **Display resize** reads **Not available** and the console does not
offer **Resize guest to window**, because a guest with no agent to act on the request
ignores it.

On Windows the same virtio-win package carries the memory balloon driver, so a VM created
with the **Maximum** [memory mode](create-virtual-machine.md#memory-modes) returns no freed
memory to the host until that package is installed; Linux has the driver in its kernel. See
[Memory reclaim](#memory-reclaim) below.

Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

## Prerequisites

- A VM [created through Controller](create-virtual-machine.md) on a host running matching
  Nex Core components, with an operating system installed and running.
- Access inside the guest to install packages (a shell, or the console with a login).
- For the console clipboard: a graphical session in the guest. Text consoles have no
  clipboard; use **Type into VM** there instead.

## Steps

1. Install the tools inside the guest:
   - **Debian and Ubuntu:** `apt install qemu-guest-agent spice-vdagent`. Both services
     start on their own; `spice-vdagent` also starts inside each graphical session.
   - **Other Linux:** install the distribution's `qemu-guest-agent` and `spice-vdagent`
     packages and enable the `qemu-guest-agent` and `spice-vdagentd` services.
   - **Windows:** run the virtio-win guest tools installer, which includes the QEMU guest
     agent and the SPICE vdagent (both start as services).
2. In Nex Controller, select **VMs** in the rail, select the VM's row, and read the
   **Guest tools** block on its panel's **Overview** tab. It is a status grid with one row each
   for **Guest agent**, **Clipboard** and **Display resize**, each with one word: **Ready**,
   **Not answering**, **Not running**, **Waiting**, **Not available** or **Unknown**. Select a
   row (**Why?**, or **Details** when it reads **Ready**) for the full reason. **How to enable
   guest tools** below the grid opens the install commands for Linux and Windows, so you do not
   need to leave the page to find them. Within about ten seconds of the agent starting,
   **Guest agent** reads **Ready** and the block lists the operating system, guest hostname and
   IP addresses.
3. Select **Console** in the VM panel's quick actions to open the full-screen console.
   **Clipboard** in the **Guest tools** block reads **Ready** once a graphical session with
   spice-vdagent is running and qemu-guest-agent is running; the console then exchanges
   clipboard text with the guest as described in [Open a VM console](vm-console.md#steps).

## Expected result

The **Guest tools** block shows:

- **Guest agent: Ready**, with the reported operating system, hostname and addresses.
  Loopback and link-local addresses are left out.
- **Clipboard: Ready** (graphical session with spice-vdagent, and qemu-guest-agent running), or
  **Waiting** until then; its **Why?** reads *Waiting for guest tools (qemu-guest-agent and
  spice-vdagent) in the guest*.
- **Display resize: Ready** only when the VM has a VirtIO display **and** the guest is running
  the vdagent that acts on a resize request and qemu-guest-agent — that is, when **Clipboard**
  reads **Ready**. The console then offers **Resize guest to window**. A VirtIO display on
  its own means the host would send the request, not that the guest would follow it, so
  Nex does not offer the option until the guest can use it.

Channel states refresh with every host report, about every ten seconds; the operating
system, hostname and addresses are asked from the agent about once a minute. While the VM
is stopped every row reads **Unknown** and the block says guest tools are observed while the VM
is running. A connected agent reports facts and lets
the host ask the guest for information; it is not proof that the guest's applications work.

### Reported operating system

The operating system is the guest's own answer, not something Nex detects. Nex corrects one
known misreport: Windows 11 kept the internal product name Windows 10 wrote, so the QEMU
guest agent calls a Windows 11 guest "Windows 10 …". Nex reads the build number the same
answer carries and reports **Windows 11** from build 22000 upwards. A guest that reports no
build number, or one Nex cannot read, is shown exactly as it described itself.

### Memory reclaim

A VM created with the **Maximum** [memory mode](create-virtual-machine.md#memory-modes) only
returns memory it frees while the **virtio balloon driver** runs inside the guest. Until it
does, the guest keeps every page it has ever touched, so the VM drifts up to its ceiling and
holds its touched pages; maximum mode does not preallocate or lock RAM. A **Dedicated** VM
with the physical reservation applied is unaffected: its RAM is allocated and locked while
running, and its definition carries no balloon device. See the
[existing-VM upgrade steps](create-virtual-machine.md#memory-modes).

- **Linux** has the balloon driver in the kernel and loads it automatically for the device,
  so reclaim needs no extra package. The guest tools remain worth installing for everything
  else on this page.
- **Windows** needs the virtio-win guest tools from step 1, which carry the balloon driver
  and its service alongside the QEMU guest agent. Without that package there is no reclaim.

Controller does not report reclaim as its own state, so read the **Guest tools** block as the
practical per-VM signal: on Windows, **Guest agent: Ready** means the virtio-win package
that carries both is installed, while **Not running** or **Not answering** on a Maximum Windows
VM means freed memory is probably not coming back yet.

Nothing in this build grows or shrinks a guest under host memory pressure. The driver lets a
guest hand back pages it has already freed; it does not make Nex rebalance memory between VMs.

## Troubleshooting

- **Guest agent: Not running** (*Not running in the guest*): the channel exists but nothing in the guest opened it.
  Install and start `qemu-guest-agent` (Linux) or the QEMU guest agent service (Windows).
- **Guest agent: Not answering** (*Attached but not answering*): the guest agent opened the channel but did not answer
  within one second. It is usually starting or the guest is under load; the host asks again
  every ten seconds. A guest that never answers has a hung agent: restart the service.
- **Guest agent: Not available** (*Channel missing*): the VM was created before guest tools
  support. Its console still
  works and **Type into VM** sends text as keystrokes; recreate the VM to gain the channels.
- **Clipboard: Waiting** (*Waiting for guest tools (qemu-guest-agent and spice-vdagent) in the
  guest*): both agents must run. Nex reports the clipboard ready only while the
  QEMU guest agent channel is also open, so a guest running spice-vdagent without
  qemu-guest-agent still reads waiting; install and start `qemu-guest-agent` as in step 1
  (the **Guest agent** row shows whether it is running). Then sign in to a graphical
  session. On Linux the session agent is started by the desktop; for a bare X session run `spice-vdagent` after `spice-vdagentd` is enabled. A session
  started outside a login (no systemd-logind session, for example from a script) is ignored
  by `spice-vdagentd` until it runs without session integration: set
  `SPICE_VDAGENTD_EXTRA_ARGS=-X` in `/etc/default/spice-vdagentd` and restart it.
- **Display resize: Not available** on a VM that has a VirtIO display: the guest is not
  running both the vdagent and qemu-guest-agent, so Nex does not treat it as able to change
  resolution. Check **Clipboard** in the same block — it reads **Ready** exactly when
  resize becomes available. Install both tools as in step 1, make sure `qemu-guest-agent` is
  running, and sign in to a graphical session. On a VM whose display is not VirtIO, resize stays unavailable whatever is installed; recreate the VM.
- **The guest reports a different operating system than you installed:** the panel shows
  what the guest's agent answers. Nex corrects only the Windows 11 build case described in
  [Reported operating system](#reported-operating-system); it does not otherwise second-guess
  the guest. Use the console to inspect the guest itself.
- **No guest observation yet:** the host runs an older Nex Core build that does not report
  guest tools, or the first report after starting the VM has not arrived. Upgrade Nex Core
  or wait for the next report.
- **Guest agent stays Not running after installing:** confirm the services are running
  (`systemctl status qemu-guest-agent`), and that the VM has the guest agent channel (a
  **Channel missing** VM was created before guest tools support and must be recreated).
- **Addresses look wrong:** the panel shows what the guest reports, including addresses on
  networks Controller does not manage. Use the console to inspect the guest.
- **A Maximum VM holds all its memory:** on Windows, check the **Guest tools** block for that
  VM; a **Guest agent** that is not **Ready** means the virtio-win package is missing, so install
  it as in step 1. On Linux the driver is in the kernel, and what returns is what the guest
  counts as free — pages held as file cache stay with the guest, so a busy guest can sit near
  its ceiling legitimately. This build reclaims nothing under host pressure, so lower the
  ceiling or use **Dedicated** for a predictable reservation instead.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

Linux guest tools, the **Guest tools** panel and clipboard were walked through in a disposable
nested-KVM lab; Windows guests and physical hardware were not. See the
[console features QA report](../qa/console-features/checks.md).

The display-resize condition and the Windows 11 name correction described above were
observed in the [Windows 11 UEFI lab](../qa/2026-09-16-windows-11-uefi-lab.md) (issues W6 and
W16) and are covered by host-side checks over recorded libvirt definitions and guest agent
answers (`go test ./internal/vmhost/`). The Linux side was re-walked in the [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md)
(`0.2.10~gate5`, 2026-09-17, disposable nested-KVM lab, headless Chromium, browser plus host and
API checks): step 1 in a Debian 13.6 UEFI guest, then a fresh **Guest tools** panel with the agent
connected, OS, hostname, addresses, clipboard Ready and display resize Available, and steps 2–3 as
the agents came up. The API read clipboard waiting and resize unavailable in firmware and GRUB,
during a guest reboot and with qemu-guest-agent stopped, and ready again after it restarted; the
**Guest tools** badge read disconnected, never stale, including on a fresh VM with no OS. Paste
delivery and resize in a signed-in desktop session were not exercised end to end (Ready was
observed at the login greeter), a guest that never had qemu-guest-agent was not observed, and
Windows guests have **not** been re-walked on a lab host since the change.

