# Console quality menu removal verification

Date: 2026-09-11. Development working tree, noVNC 1.7.0, React 19.2.8, headless Chromium (Python
Playwright). Real built UI and noVNC; simulated Controller API and minimal RFB handshake. No live host changes.
API impact: none. Cause: quality setters had no effect on native libvirt VNC sockets (see BUG-0069);
the menu was removed.

## Executed checks

- The browser regression failed before the fix: Image quality count expected 0, got 1.
- `npm --prefix web run typecheck` and `build`: passed.
- `tests/integration/console_controls_browser.py --evidence .cache/console-controls-browser`: 4 groups
  passed, no page errors. Old quality preferences ignored and dropped; View persists across reload;
  reconnect keeps view-only; ticket failure disables screenshot, and automatic retry reconnects; stale host disables input.
- Inspected local `mock-connected.png`, `mock-error.png`, `mock-stale-mobile.png`: no menu, View visible,
  labels readable, narrow toolbar wraps.
- `check_docs.py --worktree`: passed for 55 paths at first. The final rerun was blocked by unrelated concurrent power/agent
  paths without assessments.
- Privacy test and `git diff --check`: passed.
- Independent review found no implementation issues, only an API-impact scope note, which was addressed.

## Limits

These results predate the concurrent toolbar-collapse additions ([console-toolbar](../console-toolbar/checks.md)).
Simulated API/RFB cannot prove native image fidelity, bandwidth, resize, clipboard or host behavior. There was no live VM,
Core-local login walkthrough, hosted CI or deployment. The native lab script asserts the menu is absent but was not rerun.
Both phase1 workflows run the regression.
