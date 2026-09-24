# VM creation navigation regression

Date: 2026-09-24. Environment: current working-tree Controller UI, Chromium via Python
Playwright, 1440 × 1000, disposable local static server with explicit API fixtures.
No real VM, host or storage was changed.

The primary agent ran the accepted-create block from
`tests/integration/phase1_browser.py` in an isolated local fixture runner
(`.cache/check-vm-create.py`). The surrounding session, inventory and scope were simulated;
the wizard, submission callback, routing, rendering and automatic polling used the built UI.

- Before the fix, the new Creation task visibility assertion failed: the wizard disappeared
  and only the shell and placement notice remained, reproducing the reported blank page.
- After the fix, the queued creation task appeared without reloading. Publishing a succeeded
  operation and VM in the fixture let the normal poll show the VM list and detail panel.
  The placement warning survived navigation and cleared on the terminal operation.
- The primary agent inspected `create-result-without-refresh.png` and
  `created-vm-without-refresh.png` in the local `.cache/` directory.
- `npm --prefix web run typecheck`, `npm --prefix web test` and
  `npm --prefix web run build` passed.

The persistent regression lives in the existing Phase 1 browser test, run by both forge
workflow copies via `python tests/integration/phase1.py --browser` on pushes and pull requests.
This run exercised its isolated fixture block, not the full integration suite or hosted CI.
Native VM creation, storage correctness and recovery were not retested for this UI state fix.
