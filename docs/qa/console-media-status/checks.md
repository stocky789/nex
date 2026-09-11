# Console media status checks

Date: 2026-09-11. Development 0.1.0 working tree, built UI in headless Chromium with
simulated API and RFB display responses. No real host media was changed.

- Before the fix, `console_controls_browser.py` failed: **Ejecting ISO…** remained after
  the media task succeeded and the mounted console's CD/DVD selection became **Empty**.
- `npm --prefix web run build` and `npm --prefix web run typecheck` passed. An initial
  typecheck was blocked by unrelated storage work in progress; the later full check passed.
- `.cache/phase1-qa-venv/bin/python tests/integration/console_controls_browser.py` passed.
  Eject and insert show pending feedback, then clear it on successful task/inventory refresh
  without reloading the page. A failed eject retains its host error and attached ISO.
  After completed tasks leave recent history, later tray changes do not revive progress;
  failed-task eviction preserves the error. Existing console toolbar, keyboard, fullscreen,
  connection error and recovery checks also passed.
- Inspected local `mock-media-eject-succeeded.png`: CD/DVD reads Empty, Eject is disabled,
  and the progress strip is gone. Failed-eject capture retains the ISO and host reason.
  Results and captures are under `.cache/console-controls-browser`.

The existing browser suite runs in both GitHub and Gitea phase1 workflows on pushes and
pull requests. Independent scoped review passed after adding completed-state cleanup and
terminal-error retention. Native hypervisor media changes and hosted CI were not exercised.

API impact: existing contract — POST /api/v1/vms/{id}/media supplies the operation; existing
GET /operations and GET /vms report completion and tray contents. Core execution, authorization,
request identity and retry contracts are unchanged. Feedback state is browser-local only.
