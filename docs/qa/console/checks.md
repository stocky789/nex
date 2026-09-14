# Browser VM console and power control development checks

Date: 2026-09-09. Scope: [console slice](../../plans/2026-09-09-vm-console.md) and
[power slice](../../plans/2026-09-09-vm-power.md) (API impact: new contract for both). Status: Go,
contract, documentation and disposable-lab browser checks passed; review findings were fixed before the final run.

## Executed root checks (final code)

- `go test -race -count=1 ./...`: all `ok` (agent 2.519s, controller 62.743s, localcore 9.933s,
  nexupdate 1.016s, vmhost 1.375s, websecurity 1.007s, wsock 1.008s). vmhost and controller were rerun green after review.
  Power: idempotent start/force-stop, refused shutdown, invalid action, paused guest, interruption around
  the libvirt call, 404/400/409, capability gate, replay, pending conflict, dispatch/result. Console:
  RFC 6455 framing and rejected upgrades, VNC socket resolution, ticket refusals, single delivery,
  cross-origin/wrong-ticket/wrong-host/duplicate tunnels, relay both ways, ticket consumption, close,
  timeout, agent tunnel refusals, host-local bridge session/Origin/stopped refusals.
- `go vet ./...` clean. Discovery: 81/83 passed, including 21 API contract checks. The 2 `test_apt_publish` errors
  come from `dpkg-deb` on Arch and belong to another slice. `generate_api.py --check`: current.
- Web typecheck/build passed. `check_docs.py --all` and `git diff --check` passed.

## Disposable-lab browser evidence

```sh
GO="$PWD/.cache/toolchains/go/bin/go" .cache/phase1-qa-venv/bin/python tests/integration/console_lab.py --copy-from .cache/nex-appliance-lab.<redacted>
```

[results.json](results.json) records a copied Debian-13-based Nex Core 0.1.0-alpha1 host with nested KVM, libvirt 11.3.0,
loopback forwards, a workstation Controller (`-stale-after 3s`, 500 ms heartbeat), the real Debian 13.6
netinst transferred through the agent, a guest created via the API, and Playwright Chromium 151. Artifact hashes are in the
results. All result groups passed. Captures are named below; they are local only (`docs/qa/**/*.png` is gitignored).

| Check | Evidence |
|---|---|
| Agent reports `console-v1` and `vm-power-v1` | results |
| Ticket refused: stopped VM 409, no CSRF 403, unknown VM 404 | results |
| Stopped VM console tab explains, no manual connect | `controller-console-blocked-stopped.png` |
| Stopped power panel: only Start; invalid action 400, no CSRF 403 | `controller-power-stopped.png` |
| UI Start ran as audited `vm-power` task, guest running | `controller-power-started.png` |
| Reboot confirmed and issued; boot menu ignores ACPI | `controller-power-confirm-reboot.png` |
| Force stop stopped guest; host-local page reflects it | `controller-power-force-stopped.png` |
| Open console tab auto-connected when the VM started | `controller-console-ready.png` |
| Live console via host tunnel, boot menu rendered, `console.open` audited | `controller-console-connected.png` |
| Keyboard input moved boot-menu selection | `controller-console-input.png` |
| 360 px layout while connected | `controller-console-connected-360.png` |
| Leaving the tab ends the session; reopening issues one new ticket | `controller-console-disconnected.png` |
| Stopping the agent shows unexpected-close message | `controller-console-lost.png` |
| Stale host blocks tickets in UI and API (409) | `controller-console-blocked-stale.png` |
| Tab reconnects when the host reports again | `controller-console-reconnected.png` |
| Unattached ticket times out 504 after 20 s | results |
| Host-local Open console for running guest | `local-overview-open-console.png` |
| Host-local console connected without Controller | `local-console-connected.png` |
| Host-local hides console for stopped VM | `local-overview-stopped.png` |

Captures were inspected: layout, letterboxing, reconnecting status and state-appropriate power actions were correct. An earlier run
showed duplicated power panels (shared React key). This was fixed and checked with a mocked-API render before the final run.

## Limits and notes

- Nested KVM, not physical hardware; no hosted CI.
- The lab image predates the root helper, so `nex-root-auth` was built on the copy and a root password set there.
  The results.json `limits` wording "from the installed lab package" is inaccurate; the script was corrected. The real PAM path was exercised.
- The local service ran from a pushed binary under `systemd-run`, not the installed package.
- One viewer only; concurrent viewers, long sessions, clipboard and serial are out of scope.
- Read-only reviews by this session (not independent) found issues, all fixed before the final run: orphaned tunnel sweep, context cancellation, OpenAPI
  descriptions, focus, typings, ambiguous shutdown/reboot reconcile, fail-open pending guard, UI gating, accessibility.
- Shutdown of an ACPI-honoring guest was not exercised.
