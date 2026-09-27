# Default pool scope after enrollment

Date: 2026-09-25. Source candidate: `0afc57f` plus uncommitted UI and browser-check edits.
Built `web/dist/app.js` SHA-256: `2cdcfe3a1bc2bf31dd8980f9a23d434f9ff55916d1d882390f0308f3ee53e8c4`.
Environment: local static UI server, headless Chromium through Playwright 1.62.0,
fictional API route fixtures. No Nex Controller, Nex Core or real host was used.

## Reproduction and result

The fixture starts with one pool, then adds a second pool and host as enrollment
would. Before the fix, after the inventory update, Settings showed **All pools**
and the browser check failed: it expected the original pool but observed two hosts
in site-wide scope. A second red run placed the new pool first in the inventory;
the initial default fix then switched Settings to that new pool. The existing browser script lacked an executable entry point,
so its command could exit without executing its assertions; this change makes
that command run them. No prior CI run was inspected for this conclusion.

After the fix, the command below passed seven reported groups. It checked the
original-pool selection after the new pool appeared first, the explicit **All pools**
choice, fallback to the first pool when the selected temporary pool disappeared,
the visible Settings Members section, and the existing menu keyboard/navigation
cases. The new `first-pool-after-enrollment.png` capture was inspected locally:
the scope named the original pool and its Settings cards were visible. Captures live
under `.cache/` and are not part of this public repository.

```sh
npm --prefix web run build
.cache/phase1-qa-venv/bin/python tests/integration/site_selector_browser.py --evidence .cache/site-selector-browser-scope-regression
```

`npm --prefix web run typecheck` and `npm --prefix web run build` also passed.
The forge's `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml`
already invoke this browser command. No forge run has been observed for this
change, and the fixture does not prove a native enrollment or completed join.

API impact: none. The browser derives its scope from existing inventory and
keeps only an optional local preference; no Controller or Core request, response,
identity or durable record changed. Guide impact: updated in the Controller
console guide.
