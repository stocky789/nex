# Console features, clipboard and guest tools observation

Date: 2026-09-09
Status: implemented in Controller, Core and the shared console component; verified in a
disposable nested-KVM lab with a Debian 13 guest running the guest tools (21-group browser
walkthrough, exit 0). One documented limitation: guest→browser clipboard content is not
reproducible in the bare-X headless lab guest.
Parent: [platform plan](2026-09-08-nex-platform.md) Phase 2 items 5 and 7; extends the
[browser VM console](2026-09-09-vm-console.md) and implements slice 1 (observe) of the
[guest tools assessment](2026-09-09-pool-vlan-networks.md#guest-tools-assessment-requested-2026-09-09).

## Scope and decisions

Matt asked for every console feature that is practical to implement, bidirectional clipboard
included, and expected that guest tools would be needed so VMs can talk to Core/Controller.

Research into what the noVNC 1.7.0 API and QEMU/libvirt VNC support make possible, alongside
the console features operators expect, gave this catalogue; each item is either in this slice
or listed under follow-ups with its reason:

| Feature | Mechanism | This slice |
|---|---|---|
| Send key combinations (Ctrl+Alt+F1–F7, Alt+Tab, Alt+F4, Ctrl+Esc, Super, Escape) | noVNC `sendKey` with QEMU extended key events | yes |
| Display scaling: fit, actual size with scrolling | noVNC `scaleViewport` / container overflow | yes |
| Guest resolution follows the browser window | noVNC `resizeSession` (ExtendedDesktopSize) + VirtIO display in the VM definition | yes |
| Clipboard browser → guest and guest → browser | RFB extended clipboard; QEMU `qemu-vdagent` channel + `spice-vdagent` in the guest | yes |
| Type text as keystrokes (works in installers and text consoles) | noVNC `sendKey` per character | yes |
| Screenshot of the current display | noVNC `toBlob`, browser download | yes |
| View-only mode, quality presets, manual reconnect | noVNC `viewOnly`, `qualityLevel`/`compressionLevel`, ticket re-issue | yes |
| Full screen with the toolbar, console-only browser window | Fullscreen API on the panel; `#/console/<vm>` route | yes |
| Absolute pointer in any guest | USB tablet input device in the VM definition | yes |
| Guest agent facts (OS, hostname, addresses) and channel readiness | `qemu-agent-command` and live channel states reported per running VM | yes |
| Install advisory when guest tools are absent (Linux + Windows instructions) | Guest tools panel detects a non-responding agent on a running VM | yes |
| VNC power control (`xvp`) | QEMU `power-control` grants power to anyone holding the stream | no: Nex power controls sit above the console already |
| Serial console (xterm.js) | needs a serial device, a second tunnel kind and a new dependency | follow-up |
| Multiple viewers, session listing/termination, recording, role scoping | Phase 8 authorization work | follow-up |
| Set guest IP / run commands through `guest-exec`, Nex Guest Tools package, cloud-init seeds | guest tools slices 2 and 3 | follow-up |

Decisions:

- **Guest tools are the standard QEMU guest agent plus spice-vdagent.** The channels are
  host-local virtio-serial, so nothing new listens in the guest or on the network and
  authorization stays at the Controller API and host mTLS boundaries. A Nex-specific guest
  package or protocol is not needed for this slice; the guide names the Debian/Ubuntu packages
  and the Windows virtio-win guest tools installer.
- **Every new VM definition carries the console devices:** the existing guest agent channel,
  a `qemu-vdagent` channel with `clipboard copypaste='yes'` and `mouse mode='server'` (this one
  channel is what enables the VNC server's clipboard; QEMU renders it as
  `-chardev qemu-vdagent,clipboard=on` and the VNC display needs no separate flag, confirmed by
  the disposable-lab run), a USB tablet and a VirtIO video model (libvirt ≥ 8.4, QEMU ≥ 6.1;
  Nex Core ships libvirt 11 and QEMU 10). Definition validation compares disks, NIC, OS/CPU/boot
  and the single VNC display, so these devices pass the tampering check.
- **VMs created before this slice keep their definitions.** The host reports `no-channel`
  for their clipboard and `resize: false`; the UI says so and offers Type into VM, which works
  everywhere. Editing stopped VMs is the next Phase 2 slice and is where a device upgrade
  belongs; nothing is rewritten silently.
- **Observation is separate from the libvirt state probe.** A new `guest` probe reads the live
  definition of each running VM for channel states and video model, asks the agent for OS,
  hostname and interface addresses with a 1-second agent timeout, caches facts per VM for a
  minute (10 seconds while attached but unanswered), and never blocks the power-state probe.
  Agents are asked only after every VM was observed, oldest cache first, and asking stops when
  the probe deadline is near, so a hung guest delays other guests' facts by a tick rather than
  losing the observation. Facts are dropped when the VM leaves the running list, and a failed
  or overdue guest probe attaches nothing rather than stale facts. Guest strings are bounded
  on the host and refused by Controller beyond the documented limits.
- **Guest text never writes the operator's clipboard by itself.** Text copied inside the VM is
  shown in the console and reaches the browser clipboard only through the Copy button.
- **The console stream is unchanged.** Clipboard and resize are in-band RFB messages the
  existing relay already carries; no side channel was added.

API impact: **changed contract** — the report inventory gains an optional `vms[].guest`
(`GuestInfo`: agent, os, hostname, addresses, clipboard, resize) and the VM list
(`GET /api/v1/vms`) passes it through as `guest` for a VM observed running in a fresh report.
The host also reports a `guest` check. No route, command or capability changes; the console
routes are the **existing contract**. Compatibility follows the managed-VM rule: upgrade
Controller before Core, because an older Controller's strict JSON decoding rejects the new
member; an older Core never sends it and the UI shows guest tools as unknown. Recorded in the
[API reference](../API.md#guest-tools-observation) and the OpenAPI schemas.

Guide impact: **updated** — [Open a VM console](../guide/vm-console.md) documents the toolbar,
clipboard and display controls; the new [Install guest tools](../guide/guest-tools.md) page
covers packages, what the Guest tools panel shows and its limits.

## Acceptance

- [x] New VM definitions include the guest agent channel, qemu-vdagent clipboard channel,
      USB tablet and VirtIO display; the tampering check still passes (Go tests).
- [x] Guest observation parses channel states, video model and agent answers, refuses invalid
      IDs and definitions, filters loopback/link-local addresses and caps addresses (Go tests).
- [x] Snapshot attaches guest facts to running VMs only; the VM list passes `guest` through
      from a fresh report and omits it otherwise (Go tests).
- [x] OpenAPI schemas match the protocol structs; generated reference current (contract tests).
- [x] Browser walkthrough in a disposable lab with a Debian 13 guest (21 groups, exit 0):
      Guest tools panel, send key, guest resize, type into VM, browser→guest paste verified
      inside the guest through `guest-exec`, screenshot download, view only, reconnect, quality
      presets, console-only window, honest stopped state; no page errors. Guest→browser copy
      exercises the handshake but its content is a documented headless limitation.
- [x] Independent review of implementation and evidence; findings resolved (guest-string bounds,
      probe freshness, agent-timeout budgeting, view-fallback, view-only on reconnect, no
      auto-write of the operator clipboard).

Evidence: [console features QA report](../qa/console-features/checks.md).

## Follow-ups

Serial console; stopped-VM editing including a console device upgrade for older VMs; guest
tools slices 2 and 3 (set guest address through `guest-exec`, first-boot seeds); Windows guest
walkthrough (virtio-win guest tools); multiple viewers, session listing and role scoping in
Phase 8; graceful shutdown through the guest agent when it is connected.
