# Console toolbar collapse verification

Date: 2026-09-11. Development working tree, React 19.2.8/noVNC 1.7.0,
headless Chromium via the existing Python Playwright QA environment. Real built UI
and noVNC, simulated Controller API and RFB framebuffer; fictional resources only.

## Checks

- `npm --prefix web run typecheck` and `npm --prefix web run build`: passed.
- `.cache/phase1-qa-venv/bin/python tests/integration/console_controls_browser.py --evidence .cache/console-toolbar`:
  passed. Embedded and separate-window collapse increase screen height, hide all controls
  and the open clipboard, and restore the draft and View only setting with Enter/Space.
  Canvas identity and ticket count remain unchanged. Fullscreen grows and restores too.
- Ticket failures remain visible while collapsed; automatic reconnect recovers. Stale
  host warnings remain visible at a narrow viewport, with the arrow usable to reopen.
- The existing regression runs in both push/pull-request phase1 workflow definitions;
  hosted CI has not been run for this change.

## Evidence and limits

Browser results and captures are generated in `.cache/console-toolbar/` by the command
above. Captures cover expanded, collapsed embedded/popout/fullscreen, error and narrow
stale-host states. Simulated framebuffer dimensions do not prove real guest resize,
clipboard delivery or QEMU rendering quality. Core-local login and physical hosts were
not exercised; they reuse the reviewed shared Console component. No deployment performed.

All six generated PNG captures were visually inspected: arrow direction/location, focus
outline, restored toolbar, collapsed connection badge and readable warning/error text.
The simulated connected canvas is black in these captures; they verify console chrome
and geometry, not guest pixels or effective image scaling.

`python3 scripts/check_docs.py --all`, `git diff --check` and
`python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: passed.
`python3 scripts/check_docs.py --worktree`: blocked by missing assessments for unrelated
in-progress power/agent changes (`internal/agent/client.go`, `queue_test.go`, `state.go`,
`internal/controller/power.go`, `power_test.go`, `internal/protocol/power.go`,
`internal/vmhost/power_test.go`, `tests/integration/phase1_browser.py`). This toolbar
slice's changed paths have an assessment; whole-worktree acceptance remains open.
