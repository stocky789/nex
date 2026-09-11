# Pool tab form visibility checks

Date: 2026-09-11. Development working tree, UI built from `web/src`. No host, storage,
network or ISO was changed: every check ran against fixture data in the browser only.

## Runnable check in the repository

- `npm --prefix web run test` passes, including the new `web/src/managed.test.mjs`. It renders
  the pool view with `react-dom/server` for each form (**Register network**, **Upload ISO**,
  **Create VM**) across all six pool views and asserts the panel stays mounted everywhere but
  is hidden on every view except the one that opens it.
- The same check fails on the pre-fix markup:
  `AssertionError: network form on Virtual machines: wrong visibility`.
- `npm --prefix web run typecheck` and `npm --prefix web run build` pass.
- The check runs as part of `npm --prefix web run test` in the GitHub and Gitea `phase1`
  workflows on pushes and pull requests.

## Browser review

A throwaway harness outside the repository mounted the real pool view component with fixture
hosts and storage, loaded the built stylesheet, and replayed operator clicks in headless
Chromium at 1000x900. Captures were inspected; they are local only and not committed.

- **Networking → Register network**: the registration panel opens above the network list.
- **Register network open → ISO library**: the ISO library view shows only **Upload ISO** and
  the empty ISO library. The registration panel is gone without using **Close** — the reported
  defect.
- **Register network open → Virtual machines**: only the VM list and **Create VM** are shown.
- **Register network open → ISO library → Networking**: the panel returns, still open, which
  also shows the view was not remounted (the harness has no stored pending request to restore).
- **Create VM wizard open → Storage**: the storage view renders alone, with no gap where the
  wizard was.

## Not exercised

- `tests/integration/managed_vm_browser.py --simulated` could not complete in this working
  tree: it stops at `Register local storage`, a control renamed by unrelated in-progress
  storage work. Both of its first phases passed before that point. This change was therefore
  not covered by the simulated Controller suite; rerun it once the storage work updates the
  labels the test drives.
- No real host, libvirt, VM creation or ISO transfer was involved.

API impact: none. Only which pool view shows an already-rendered form changed; no Controller
or Core request, response, command or stored record is involved.
