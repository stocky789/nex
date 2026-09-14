# Console toolbar collapse verification

Date: 2026-09-11. Environment and method as in [console-quality](../console-quality/checks.md)
(built UI, noVNC, simulated API and RFB framebuffer, fictional resources).

## Checks

- `npm --prefix web run typecheck` and `build`: passed.
- `tests/integration/console_controls_browser.py --evidence .cache/console-toolbar`: passed. Embedded and
  separate-window collapse enlarge the screen and hide controls and the clipboard; Enter/Space restores the draft and View only.
  Canvas identity and ticket count stay unchanged. Fullscreen grows and restores. Ticket failures stay visible while collapsed,
  and automatic reconnect recovers. Stale-host warning is visible at a narrow width, and the arrow reopens.
- All six local captures in `.cache/console-toolbar/` inspected: arrow, focus outline, restored toolbar,
  collapsed badge, readable warnings. The canvas is black (simulated), so this shows chrome/geometry only.
- `check_docs.py --all`, `git diff --check`, privacy test: passed. `check_docs.py --worktree` was blocked by
  unrelated in-progress power/agent paths without assessments.
- Both phase1 workflows run the regression; no hosted CI run.

## Limits

No real guest resize, clipboard, QEMU rendering, Core-local login, physical host or deployment.
