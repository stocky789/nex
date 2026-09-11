# Console quality menu removal verification

Date: 2026-09-11. Environment: local development working tree, noVNC 1.7.0,
React 19.2.8, headless Chromium through Python Playwright. Real built UI and noVNC;
simulated Controller API and minimal RFB handshake (no guest pixel updates). No live host changes.

## Cause and scope

Core uses native libvirt VNC sockets without the lossy emulator wrapper removed in
BUG-0069. Quality setters reached noVNC but could not provide the advertised image
fidelity changes. The old lab check only proved the stream survived dropdown changes.
Remove the menu and saved-quality handling from the shared Controller/Core-local
component; use noVNC defaults and retain View controls. This does not claim QEMU can
never support lossy quality; supported, verified host integration would be needed.

## Executed checks

- Before rebuilding with the fix, the browser regression failed at its first assertion:
  Image quality expected count 0, actual 1.
- `npm --prefix web run typecheck` and `npm --prefix web run build`: passed.

- `.cache/phase1-qa-venv/bin/python tests/integration/console_controls_browser.py
  --evidence .cache/console-controls-browser`: passed, four check groups, no page errors.
  Old low/high preferences are ignored; View changes persist across reload and drop the
  obsolete preference. Manual reconnect preserves view-only protection. Ticket failure
  disables screenshot and automatic retry reconnects. Stale hosts disable connection/input.
- Inspected `mock-connected.png`, `mock-error.png` and `mock-stale-mobile.png` from that
  local evidence directory: quality menu absent, View control visible, connection/error
  labels readable and narrow toolbar wraps. These are simulated captures, not guest images.
- `python3 scripts/check_docs.py --worktree`: initially passed for 55 changed paths.
  Final rerun found unrelated concurrently changed paths without assessments:
  `internal/agent/client.go`, `internal/agent/queue_test.go`, `internal/agent/state.go`,
  `internal/protocol/power.go`, `internal/vmhost/power_test.go`,
  `tests/integration/phase1_browser.py`, `web/src/console.css`.
  This slice's paths are assessed; the whole-worktree gate is currently blocked by those
  other changes, which were left intact.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: passed (1 test).
- `git diff --check`: passed.

Independent review found no implementation issues; its request to include the Core-local
direct console API in the impact assessment was addressed. API impact: none. Guide impact:
updated. No new durable state.

## Limits

The four-group result above predates concurrent toolbar-collapse additions to the same
browser script/component. Those additions are outside this quality-removal review and
need their own final build/browser verification; this report does not certify them.

The browser test simulates its API and RFB server; it cannot prove native QEMU image
fidelity, bandwidth, guest resize, clipboard or physical-host behavior. No live VM or
Core-local login walkthrough, hosted CI run or deployment was performed. The native
lab script now asserts menu absence but was not rerun. Both push/pull-request phase1
workflows invoke the new browser regression after building the UI.
