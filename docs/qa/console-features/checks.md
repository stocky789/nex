# Console features and guest tools observation checks

Date: 2026-09-09. Status: Go, contract, UI, documentation and disposable-lab browser checks
passed; independent review findings resolved before the final run.

## Scope

The [console features slice](../../plans/2026-09-09-console-features.md) extends the shared
console component (send keys, display modes including guest resize, quality presets, view
only, screenshots, reconnect, full screen, console-only window, clipboard both ways, Type
into VM), defines new VMs with a qemu-vdagent clipboard channel, USB tablet and VirtIO
display, and has Core observe per-VM guest agent facts and channel readiness that Controller
shows in a Guest tools panel.
API impact: **changed contract** (report `vms[].guest`, `guest` check, VM list `guest`);
console routes are existing contract. Guide impact: **updated**
([Open a VM console](../../guide/vm-console.md), [Install guest tools](../../guide/guest-tools.md)).

## Executed root checks (final code)

- `go test -race -count=1 ./...`: agent 2.638s, controller 65.365s, localcore 10.111s,
  nexupdate 1.103s, vmhost 1.556s, websecurity 1.008s, wsock 1.010s, all `ok`. New
  coverage: the managed definition carries both channels, the USB tablet and the VirtIO
  model with exactly one display and still passes the tampering check (`internal/vmhost`);
  guest observation for a legacy definition, waiting channels, attached channels without
  facts, an agent that does not answer, full facts with loopback/link-local/invalid
  addresses filtered, an invalid ID and invalid XML (`internal/vmhost`); the snapshot
  attaches guest observations to running VMs only, case-insensitively (`internal/agent`);
  the VM list omits `guest` for a stopped VM and passes it through for a running one
  (`internal/controller`).
- `go vet ./...` clean. `python3 -m unittest discover -s tests -p 'test_*.py'`: 84 tests
  pass, including the contract test that ties `GuestInfo`/`GuestAddress` and the new `VM.guest`
  member to the OpenAPI schemas. `python3 scripts/generate_api.py`: reference regenerated.
- `npm --prefix web run typecheck` and `npm --prefix web run build` pass.
- `python3 scripts/check_docs.py --all` and `git diff --check` pass at the final source point.

## Disposable-lab browser evidence

Command (the retained appliance lab is copied; only the copy is operated; the Debian 13
guest-tools image is baked once on the workstation from the generic cloud image):

```sh
GO="$PWD/.cache/toolchains/go/bin/go" .cache/phase1-qa-venv/bin/python \
  tests/integration/console_features_lab.py --copy-from .cache/nex-appliance-lab.<redacted>
```

Environment: a private Debian-13-based Nex Core 0.1.0-alpha1 host with nested KVM, libvirt
11.3.0 and QEMU 10.0.11, loopback-only QEMU forwards, a fresh Controller on the workstation,
and a Debian 13 guest (kernel 6.12.107) running `qemu-guest-agent` and `spice-vdagent` with
an X session, driven by Playwright Chromium 151. noVNC 1.7.0. Artifact SHA-256 digests of the
Controller, agent, the UI bundle (`web/dist/app.js`) and the baked guest image are in
[results.json](results.json). All twenty-one result groups passed (`exit=0`):

| Check | Evidence |
|---|---|
| Host reports a healthy `guest` observation check | results |
| Created VM carries the guest agent channel, qemu-vdagent clipboard channel, USB tablet and VirtIO display | results (host `virsh dumpxml`) |
| VM list carries guest agent facts (OS, hostname, NAT address) | results |
| Host reports the clipboard ready once spice-vdagent attached the channel | results |
| **Guest tools** panel shows agent, OS, hostname, addresses, clipboard readiness and resize | [capture](guest-tools-connected.png) |
| Console connects with the extended toolbar | [capture](console-toolbar.png) |
| Send key Ctrl+Alt+F7 and Ctrl+Alt+F1 switched the guest's active console (`/sys/class/tty/tty0/active`) | [F7](console-send-key-vt7.png), [F1](console-send-key-vt1.png) |
| Resize guest to window changed the guest resolution 1280x800 → 1176x500 matching the panel | [capture](console-resize-guest.png) |
| Actual size scrolls; Fit to window restores scaling | results |
| Type into VM ran the typed command in the guest | [capture](console-type-into-vm.png) |
| Paste into VM placed the browser text on the guest X clipboard (read back with `xclip` through the guest agent) | [capture](console-paste-into-vm.png) |
| Copy-from-VM control present; content delivery recorded as a headless limitation | [capture](console-copy-from-vm.png) |
| Read my clipboard loads the browser clipboard into the send box | results |
| Screenshot downloaded a PNG named after the VM | [download](console-screenshot-download.png) |
| View only disables input; Reconnect re-attaches with a new audited ticket | [capture](console-view-only.png) |
| Quality presets switch without breaking the stream | results |
| Open in new window shows a console-only page that connects on its own | [capture](console-popout-window.png) |
| Full screen puts the console panel into the browser fullscreen element | results |
| A stopped VM shows no stale guest facts | [capture](guest-tools-stopped.png) |
| Guest tools **install advisory** (Linux + Windows) shows when the agent is not responding, a recreate note for pre-tools VMs, and full facts when connected | [capture](guest-tools-advisory.png) |
| No browser page errors during the walkthrough | results |

Captures were inspected: the Guest tools panel shows the connected agent with the Debian
version, hostname and `192.168.122.x` address; the console panel shows the guest xterm with
the clipboard controls and the "Sent to the VM clipboard." status; the resize capture shows
the guest reflowed to the panel size. The advisory capture renders the real `GuestTools`
component in the three guest states (disconnected, no-channel, connected); the lab guest has
the tools connected, so the advisory was verified by component render rather than in the live
lab session.

## Limits and notes

- Nested KVM lab, not physical hardware; one viewer at a time.
- The guest disk is a Debian 13 generic cloud image baked once with the guest tools and
  substituted onto the disposable copy; the Controller creation path itself is proven in the
  [first managed VM](../managed-vm/) and [console](../console/checks.md) QA.
- **Clipboard direction:** browser → guest paste is verified end to end (the pasted text was
  read back inside the guest with `xclip` through the guest agent). Guest → browser copy
  exercises the same qemu-vdagent handshake (the guest's clipboard grab reaches the client and
  the request/provide round-trip runs, seen in the spice-vdagent debug log), but a bare-X guest
  with no clipboard manager lets spice-vdagent and `xclip` contend for the X CLIPBOARD
  selection, so the exact copied content was not reliably reproduced headless. It is expected
  to work in a normal desktop guest; recorded, not claimed as a green content match.
- Windows guests (virtio-win guest tools), physical hardware and hosted CI were not exercised.
