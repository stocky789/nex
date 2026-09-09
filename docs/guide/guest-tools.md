# Install guest tools and read guest information

## Status

Development feature of matching Nex Controller and Nex Core builds. Every VM created
through Controller carries the host-local channels that the guest tools use: the QEMU
guest agent channel and a clipboard channel for the console. Once the tools run inside
the VM, its page in Controller shows the operating system, hostname and IP addresses the
guest reports, and the [console](vm-console.md) clipboard works in both directions.

The guest tools are standard packages, not Nex-specific software. VMs created before this
feature keep their original definitions: their page shows **Channel missing** for the
clipboard and no display resize until they are recreated. Windows guests have not been
walked through yet; see [Verification](#verification).

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
2. In Nex Controller, select the VM and read the **Guest tools** panel on its **General**
   tab. While a running VM has no responding agent, the panel shows an advisory with these
   install commands for both Linux and Windows, so you do not need to leave the page to find
   them. Within about ten seconds of the agent starting, the advisory disappears and the panel
   shows **Connected · guest agent answering** with the operating system, guest hostname and
   IP addresses.
3. Open the **Console** tab. **Console clipboard** in the panel shows **Ready** once a
   graphical session with spice-vdagent is running; the console then exchanges clipboard
   text with the guest as described in [Open a VM console](vm-console.md#steps).

## Expected result

The **Guest tools** panel shows:

- **Guest agent:** Connected, with the reported operating system, hostname and addresses.
  Loopback and link-local addresses are left out.
- **Console clipboard:** Ready (graphical session with spice-vdagent), or **Waiting for
  spice-vdagent in the guest** until then.
- **Display resize:** Available for VMs with a VirtIO display, so the console option
  **Resize guest to window** works in guests with a virtio-gpu driver (Linux kernels and
  the virtio-win display driver).

Channel states refresh with every host report, about every ten seconds; the operating
system, hostname and addresses are asked from the agent about once a minute. While the VM
is stopped the panel says guest tools are observed only while it runs. A connected agent reports facts and lets
the host ask the guest for information; it is not proof that the guest's applications work.

## Troubleshooting

- **Not running in the guest:** the channel exists but nothing in the guest opened it.
  Install and start `qemu-guest-agent` (Linux) or the QEMU guest agent service (Windows).
- **Attached but not answering:** the guest agent opened the channel but did not answer
  within one second. It is usually starting or the guest is under load; the host asks again
  every ten seconds. A guest that never answers has a hung agent: restart the service.
- **Channel missing:** the VM was created before guest tools support. Its console still
  works and **Type into VM** sends text as keystrokes; recreate the VM to gain the channels.
- **Waiting for spice-vdagent in the guest** on the Console clipboard: install the tools
  and sign in to a graphical session. On Linux the session agent is started by the desktop;
  for a bare X session run `spice-vdagent` after `spice-vdagentd` is enabled. A session
  started outside a login (no systemd-logind session, for example from a script) is ignored
  by `spice-vdagentd` until it runs without session integration: set
  `SPICE_VDAGENTD_EXTRA_ARGS=-X` in `/etc/default/spice-vdagentd` and restart it.
- **No guest observation yet:** the host runs an older Nex Core build that does not report
  guest tools, or the first report after starting the VM has not arrived. Upgrade Nex Core
  or wait for the next report.
- **The advisory does not go away after installing:** confirm the services are running
  (`systemctl status qemu-guest-agent`), and that the VM has the guest agent channel (a
  **Channel missing** VM was created before guest tools support and must be recreated).
- **Addresses look wrong:** the panel shows what the guest reports, including addresses on
  networks Controller does not manage. Use the console to inspect the guest.

## Verification

Executed checks are recorded in the [console features QA report](../qa/console-features/checks.md):
Go tests for the VM definition devices, guest observation parsing and the VM list; contract
checks for the new schema; and a browser walkthrough in a disposable nested-KVM lab with a
Debian 13 guest where the guest tools were installed through cloud-init, the **Guest tools**
panel was inspected, and the clipboard was verified inside the guest with `xclip` through the
guest agent. The install advisory shown when tools are absent (Linux and Windows) was verified
by rendering the panel in the three guest states. Windows guests, physical hardware and hosted
CI have not been exercised.
