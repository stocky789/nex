# Browser VM console and power control development checks

Date: 2026-09-09. Status: both slices passed Go, contract, documentation and
disposable-lab browser checks; independent review findings for each slice resolved
before the final run.

## Scope

The [console slice](../../plans/2026-09-09-vm-console.md) adds ticketed browser
consoles in Nex Controller (browser WebSocket paired with the host's outbound mTLS
tunnel) and a direct local VNC bridge on the host-local page. The
[power slice](../../plans/2026-09-09-vm-power.md) adds Start, Shut down, Reboot and
Force stop on the Controller VM page as durable `vm-power` host tasks; the lab starts,
reboots and force-stops the created guest through that UI.
API impact: **new contract** for both; guide impact: **updated**
([Open a VM console](../../guide/vm-console.md), [Control VM power](../../guide/vm-power.md)).

## Executed root checks (final code)

- `go test -race -count=1 ./...`: agent 2.519s, controller 62.743s, localcore 9.933s,
  nexupdate 1.016s, vmhost 1.375s, websecurity 1.007s, wsock 1.008s, all `ok`; the
  reconcile tightening after review re-ran vmhost and controller green. Power coverage:
  idempotent start/force-stop, refused shutdown, invalid action, paused guest, and
  interruption before and after the single libvirt call including shutdown/reboot
  ambiguity (`internal/vmhost`); 404/400/409 refusals, capability gate, replay, pending
  conflict, dispatch payload, result and no leakage into the VM list
  (`internal/controller`). Console coverage: RFC 6455 handshake/relay/ping/close and rejected unmasked or invalid upgrades
  (`internal/wsock`), VNC socket resolution for stopped, network-display, missing-socket
  and no-display domains (`internal/vmhost`), ticket refusals (unknown VM, stale host,
  missing capability, stopped VM, unknown-field body), single delivery in the report
  response, cross-origin and wrong-ticket WebSocket refusals, wrong-host and duplicate
  tunnels, both relay directions, ticket consumption, close propagation and the browser
  timeout with a late tunnel refused (`internal/controller`), the agent tunnel against a
  fake Controller and VNC socket including invalid token, stopped VM and refused session
  (`internal/agent`), and the host-local bridge with session, Origin and stopped-VM
  refusals plus both relay directions (`internal/localcore`).
- `go vet ./...` clean. `python3 -m unittest discover -s tests -p 'test_*.py'`: 81 of 83
  checks pass, including the 21 API contract checks covering the new routes, schemas,
  command and report-response members; the two `test_apt_publish` errors are `dpkg-deb`
  exiting 2 on this workstation's Arch dpkg build and belong to another slice. `python3 scripts/generate_api.py --check`: both references current.
- `npm --prefix web run typecheck` and `npm --prefix web run build` pass (ESM output is
  required by noVNC 1.7.0's top-level await).
- `python3 scripts/check_docs.py --all` and `git diff --check` pass at the final source point.

## Disposable-lab browser evidence

Command (the retained appliance lab is copied; only the copy is operated):

```sh
GO="$PWD/.cache/toolchains/go/bin/go" .cache/phase1-qa-venv/bin/python tests/integration/console_lab.py --copy-from .cache/nex-appliance-lab.<redacted>
```

[results.json](results.json) records the final run: a private Debian-13-based Nex Core
0.1.0-alpha1 host with nested KVM and libvirt 11.3.0, loopback-only QEMU forwards, a
fresh Controller on the workstation (`-stale-after 3s`, agent heartbeat 500 ms), the real
Debian 13.6 netinst ISO uploaded and transferred through the enrolled agent, one guest
created stopped through the managed-VM API and started with the lab host's `virsh`, and
Playwright Chromium 151 driving both pages. Artifact SHA-256 digests of the tested
Controller, agent, local binaries, both UI bundles and the ISO are in the results.
All fifteen result groups passed (the console connects automatically when its tab is shown; there are no Connect or Disconnect controls):

| Check | Evidence |
|---|---|
| Enrolled agent reports `console-v1` (and `vm-power-v1`, asserted by the UI gate) | results |
| Ticket refused for a stopped VM (409), without CSRF (403), unknown VM (404) | results |
| Console tab explains a stopped VM and offers no manual connect | [capture](controller-console-blocked-stopped.png) |
| Power panel for a stopped VM: only Start enabled; invalid action 400, missing CSRF 403 | [capture](controller-power-stopped.png) |
| Start from the UI ran as a `vm-power` task, guest observed running, audited | [capture](controller-power-started.png) |
| Reboot confirmation, then request issued; boot menu ignores ACPI and stays running | [capture](controller-power-confirm-reboot.png) |
| Force stop from the UI stopped the guest; host-local page reflects it | [capture](controller-power-force-stopped.png) |
| Console tab left open during Start connected by itself once the VM was observed running | [capture](controller-console-ready.png) |
| Live console through the host tunnel, Debian boot menu rendered, `console.open` audited | [capture](controller-console-connected.png) |
| Keyboard input from the browser moved the boot-menu selection | [capture](controller-console-input.png) |
| Narrow (360 px) layout while connected | [capture](controller-console-connected-360.png) |
| Leaving the tab ends the session; reopening it issues one new audited ticket and re-attaches | [capture](controller-console-disconnected.png) |
| Stopping the host agent ends the live console with the unexpected-close message | [capture](controller-console-lost.png) |
| Stale host blocks new tickets in the UI and API (409) | [capture](controller-console-blocked-stale.png) |
| The open tab reconnects by itself once the host reports again | [capture](controller-console-reconnected.png) |
| Ticket the host never attaches times out with 504 after 20 s | results |
| Host-local page shows Open console for the running guest | [capture](local-overview-open-console.png) |
| Host-local console connected directly on the host, without Controller | [capture](local-console-connected.png) |
| Host-local page hides the console for a stopped VM | [capture](local-overview-stopped.png) |

Captures were inspected: heading, badge, toolbar and screen are visible together on
desktop after the final fix that focuses the console without scrolling; the guest display
is letterboxed and scaled; the lost-tunnel state shows the reconnecting status line; the
Power panel shows observed state and freshness, enables only state-appropriate actions,
and confirms reboot and force stop inline. An earlier run exposed duplicated power panels
on tab switches (a shared React key with the console panel); fixed and re-verified with a
local mocked-API render before the final run.

## Limits and notes

- Nested KVM lab, not physical hardware; hosted CI was not executed for this slice.
- The lab image predates the local root helper, so the run builds `nex-root-auth` from
  this tree on the disposable copy with the Debian toolchain and sets a known root
  password on that copy only; the recorded `limits` entry in the final results.json still
  carries the earlier wording ("from the installed lab package"), which is inaccurate and
  corrected in the script for future runs. The PAM path exercised is the real one.
- The new local service ran from a pushed binary under `systemd-run` on the copy, not
  from the installed package; the installed package is unchanged.
- One viewer per session was exercised; concurrent viewers, long sessions, clipboard and
  serial consoles are out of scope.
- Independent reviews (read-only, this session) verified the API boundary, lifecycle,
  framing, docs consistency and licensing note for the console, and authorization, replay,
  journal reconcile and docs consistency for power; their findings (orphaned tunnel sweep,
  browser context cancellation, OpenAPI descriptions, focus control, trimmed typings,
  ambiguous shutdown/reboot reconcile, fail-open pending guard, UI state gating and
  accessibility) were fixed before the final run, which used that code.
- Shut down of a guest that honors ACPI was not exercised (the boot-menu guest ignores it).
