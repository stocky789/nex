# Controller UI modernization: checks

Date: 2026-09-23. Plan: [Controller UI modernization](../../plans/2026-09-23-controller-ui-modernization.md).
Change record: [2026-09-23-controller-ui-modernization.json](../../changes/2026-09-23-controller-ui-modernization.json).

## Environment

- Working-tree build of the Controller and web UI (commit `0cc49ef` plus the uncommitted Tasks 7–13
  changes), compared with a build of the UI at `3068134`, the last commit before the modernization.
- Headless Chromium 151 through Python Playwright; Node 24; Go 1.27.
- Two local Controllers on loopback, each with a private data directory and a self-signed
  certificate. The capture harness answers every read from the labeled simulated inventory in
  `tests/integration/fixtures/ui-review.json` (fictional names, RFC 5737 addresses); sign-in,
  session and writes go to the real Controller. No host is behind the fixture.
- The phase 1 acceptance lab starts its own Controller and agent processes with temporary state
  and a simulated `virsh`. Nothing touches real hardware, storage or host services.
- Viewports: 1440×900, 1440×2000, 1024×768 and 390×844 in the harness, plus the lab's 360×800.

## Commands and results

| Command | Result |
|---|---|
| `npm --prefix web run typecheck` | pass |
| `npm --prefix web test` (every `src/*.test.mjs`, including tokens, ui, labels, dock) | pass, exit 0 |
| `node web/src/tokens.test.mjs` | `tokens ok`: no gradients, no literal colours outside `tokens.css`, 16 token pairs ≥ 4.5:1 in the light and the dark block |
| `npm --prefix web run build` | pass |
| `python3 scripts/check_docs.py --worktree` | pass |
| `python3 tests/integration/ui_review_browser.py --url https://127.0.0.1:PORT --password-file FILE --captures DIR` against the old UI | 40 captures, no page errors, exit 0 |
| The same against the working tree | 40 captures, no page errors, exit 0 |
| `NEX_QA_CAPTURES=DIR python3 tests/integration/phase1.py --browser --evidence docs/qa/controller-ui-modernization/results.json` | exit 0, 22 result groups passed ([results.json](results.json)) |

The first lab runs failed on assertions the earlier tasks had made stale: status-class badge
selectors (`.badge.stale`), lowercase status text, the removed "Recent tasks" heading, notice text
naming "Recent tasks", and an ambiguous **Task history** link now that the Tasks strip has one.
Those assertions were updated to the delivered UI; the same run also found a real defect, the header
**Actions** menu opening off screen at 360 px, which was fixed (see below) before the passing run.

## What was inspected

Every before/after capture pair was opened. Named checks:

- **Content height** measured in the browser with the Tasks strip closed, on a host page:
  1440×900 went from 432 px to 653 px (target ≥ 640); 1024×768 from 300 px to 521 px (target
  ≥ 520). The old UI opened the task drawer by default.
- **Status badges** keep the square dot-and-text form: compare `16-host-general.png` before and
  after (Pool master, Fresh, Healthy). Only colour and title-case text changed.
- **No horizontal page scroll** at 390 px on the workspace, pool and VM pages
  (`document.documentElement.scrollWidth <= window.innerWidth`), and at 360 px on every host tab in
  the lab.
- **Toolbar fit**: the full toolbar needs about 1250 px. Between 901 and 1280 px, navigation and VM
  power are icon-only with tooltips and the toolbar fits at 950, 1024, 1100, 1180 and 1280 px (the
  old UI clipped **Refresh view** below about 1140 px). Under 900 px every group is icon-only.
- **Pool Hosts table** (`05-pool-hosts.png`) fits at 1440 px with the Actions column visible, the
  fencing summary once above the table, relative times with the ISO time in the title.
- **Keyboard**: the lab drives the Actions menu (opens, Escape, closes after an item), tabs, the
  tree toggle and rows, the Tasks strip summary (Enter opens and closes), the skip link and every
  confirmation dialog. At 390 px Tab does not enter the closed off-canvas tree, the Resources toggle
  moves focus to the search field, and Escape closes the tree and returns focus to the toggle.
  Focus mode was checked by a separate browser script, not by a committed test: it hides the top bar, toolbar, tree, header, tabs, Tasks strip and status bar and gives
  the console the full width; **Leave focus mode** and Escape (while the display does not hold the
  keyboard) restore them.
- **Dark theme** (`prefers-color-scheme: dark`, captured with a temporary copy of the harness):
  pool Hosts, host General, VM Console, the force-stop dialog and sign-in read on the navy canvas;
  badges stay square; the brand lockup keeps its porcelain plate; the console screen stays black.
  Stats was not captured in dark; chart series have their own dark values, checked at 3:1 or better
  against the panel by `tokens.test.mjs`.

## Defects found and fixed during this pass

- Header **Actions** menu opened off the left edge at 360 px once the heading wrapped: the actions
  now stay at the right edge.
- Toolbar clipped between 901 and about 1250 px: navigation and power groups are icon-only there.
- Focus mode left the tree's grid column in place: the shell collapses to one column in focus mode.
- Field values and help text inherited the label's bold weight and 13 px size.
- Pool Hosts table was 81 px wider than the pane at 1440 px, so the sticky Actions cell covered
  the Fencing column.

## Limits

- Console tabs never connect: no VM is behind the fixture. Console connections are covered by the
  earlier console QA reports, not by this pass.
- No real hardware, storage or Nex Core host was touched.
- Visual judgements are one reviewer's pass over captures. Captures are not committed.
- No vector brand lockup exists yet; the sign-in and top-bar marks remain crops of the approved
  raster board.
