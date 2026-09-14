# Console media status checks

Date: 2026-09-11. Development 0.1.0 working tree. Built UI in headless Chromium with simulated API and
RFB responses. No real host media changed. API impact: existing contract (`POST /api/v1/vms/{id}/media`,
`GET /operations`, `GET /vms`); feedback state is browser-local.

- Before the fix, `console_controls_browser.py` failed: **Ejecting ISO…** remained after success, with CD/DVD **Empty**.
- `npm --prefix web run build` and `typecheck`: passed. An earlier typecheck was blocked by unrelated storage work.
- `tests/integration/console_controls_browser.py`: passed. Eject/insert pending feedback clears on refresh;
  a failed eject keeps its error and ISO; completed tasks leaving history do not revive progress; failed-task
  eviction keeps the error. Existing toolbar, keyboard, fullscreen, error and recovery checks passed.
- Inspected local `.cache/console-controls-browser/mock-media-eject-succeeded.png` (Empty, Eject disabled, no
  progress) and the failed-eject capture (ISO and host reason kept).

Both phase1 workflows run the suite. Independent review passed after completed-state cleanup and error retention were added.
Native media changes and hosted CI were not exercised.
