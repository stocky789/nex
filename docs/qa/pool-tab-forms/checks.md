# Pool tab form visibility checks

Date: 2026-09-11. Development tree, UI from `web/src`, fixture data only.

| Check | Result |
|---|---|
| `npm --prefix web run test` incl. `web/src/managed.test.mjs` (server-renders pool view per form — **Register network**, **Upload ISO**, **Create VM** — across six views; panel mounted everywhere, visible only on its own view) | Pass; runs in GitHub and Gitea `phase1` on push/PR |
| Same test on pre-fix markup | Fails: `AssertionError: network form on Virtual machines: wrong visibility` |
| `npm --prefix web run typecheck`, `run build` | Pass |
| Throwaway browser harness (real pool component, fixtures, built CSS, headless Chromium 1000x900; captures inspected, not committed) | Networking → Register network opens panel; → ISO library shows only Upload ISO and empty library (reported defect gone without Close); → Virtual machines shows only VM list; → back to Networking panel still open (not remounted); Create VM open → Storage renders alone, no gap |
| `managed_vm_browser.py --simulated` | **Not completed**: stops at `Register local storage`, renamed by unrelated storage work; first two phases passed. Rerun after label update |

Limits: no host, libvirt, VM creation or ISO transfer. API impact: none (view visibility only).
