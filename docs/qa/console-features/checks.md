# Console features and guest tools observation checks

Date: 2026-09-09. Scope: [console features slice](../../plans/2026-09-09-console-features.md)
(API impact: changed contract — report `vms[].guest`, `guest` check, VM list `guest`). Status: Go,
contract, UI, documentation and disposable-lab browser checks passed; review findings were resolved before the final run.

## Executed root checks (final code)

- `go test -race -count=1 ./...`: all `ok` (agent 2.638s, controller 65.365s, localcore 10.111s,
  nexupdate 1.103s, vmhost 1.556s, websecurity 1.008s, wsock 1.010s). New tests cover: definition channels,
  USB tablet and VirtIO display passing the tampering check; guest observation states and address
  filtering; guest facts attached to running VMs only; the VM list omitting `guest` when stopped.
- `go vet ./...` clean. Discovery: 84 passed, including the `GuestInfo`/`VM.guest` schema contract.
- Web typecheck/build, `check_docs.py --all` and `git diff --check`: passed.

## Disposable-lab browser evidence

```sh
GO="$PWD/.cache/toolchains/go/bin/go" .cache/phase1-qa-venv/bin/python \
  tests/integration/console_features_lab.py --copy-from .cache/nex-appliance-lab.<redacted>
```

Copied Debian-13-based Nex Core 0.1.0-alpha1 host with nested KVM, libvirt 11.3.0 and QEMU 10.0.11, running a Debian 13
guest (kernel 6.12.107, `qemu-guest-agent`, `spice-vdagent`, X session). Playwright Chromium 151, noVNC 1.7.0.
Hashes are in [results.json](results.json). All 21 groups passed (`exit=0`):

| Check | Evidence |
|---|---|
| Host reports healthy `guest` check | results |
| VM has guest agent and qemu-vdagent channels, USB tablet, VirtIO display | results (`virsh dumpxml`) |
| VM list carries OS, hostname, NAT address | results |
| Clipboard ready once spice-vdagent attached | results |
| Guest tools panel shows agent, OS, hostname, addresses, readiness | [capture](guest-tools-connected.png) |
| Console connects with extended toolbar | [capture](console-toolbar.png) |
| Send key Ctrl+Alt+F7/F1 switched `/sys/class/tty/tty0/active` | [F7](console-send-key-vt7.png), [F1](console-send-key-vt1.png) |
| Resize guest 1280x800 → 1176x500 | [capture](console-resize-guest.png) |
| Actual size scrolls; Fit restores | results |
| Type into VM ran the command | [capture](console-type-into-vm.png) |
| Paste into VM reached guest X clipboard (read back with `xclip`) | [capture](console-paste-into-vm.png) |
| Copy-from-VM control present; content delivery a headless limit | [capture](console-copy-from-vm.png) |
| Read my clipboard fills the send box | results |
| Screenshot downloaded PNG named after VM | [download](console-screenshot-download.png) |
| View only disables input; Reconnect issues new audited ticket | [capture](console-view-only.png) |
| Quality presets switch without breaking stream (control later removed: [console-quality](../console-quality/checks.md)) | results |
| New window console-only page connects | [capture](console-popout-window.png) |
| Full screen uses browser fullscreen element | results |
| Stopped VM shows no stale guest facts | [capture](guest-tools-stopped.png) |
| Install advisory (Linux + Windows), recreate note, full facts | [capture](guest-tools-advisory.png) |
| No page errors | results |

Captures inspected (these PNGs are local only; `docs/qa/**/*.png` is gitignored). The advisory was checked by rendering the real `GuestTools`
component in three states, because the lab guest had tools connected.

## Limits and notes

- Nested KVM, one viewer, no physical hardware, Windows guests or hosted CI.
- Guest disk: a Debian 13 generic cloud image baked with the tools. Creation is covered in [managed VM](../managed-vm/checks.md)
  and [console](../console/checks.md) QA.
- Clipboard: browser → guest verified end to end. The guest → browser handshake runs (spice-vdagent debug log), but
  without a clipboard manager `xclip` contends for CLIPBOARD, so the copied content was not reproduced
  headless. Not claimed.
