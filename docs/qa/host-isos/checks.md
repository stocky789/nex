# Host ISO visibility and site selection

Date: 2026-09-09. Status: implemented, deployed to the disposable lab, independently [reviewed](review.md).
API impact: existing contract.

## Verification

- UI owner: `npm --prefix web run typecheck` and `build` passed. Final build: app.js SHA-256
  `fd82675f5553f6ccfbe3f07a2fa8cb7e46769975622b137cacec3baf19887288`, app.css SHA-256
  `570292dc1d37ffc9825190e8a90b014c5eecac3db9d449271445ad2c5757882e`.
- [Host ISO browser check](../../../tests/integration/host_iso_browser.py): [simulated](simulated-results.json)
  and [native](native-results.json) runs passed against that bundle. Details: [browser.md](browser.md).
- [Site selector check](../../../tests/integration/site_selector_browser.py): a 500-site API fixture
  ([results](../site-selector/results.json), [walkthrough](../site-selector/browser.md)) passed. This does not establish backend or real
  multi-site performance.
- The existing [Phase 1 suite](../site-selector/phase1-results.json) passed 16 groups after its tree navigation assumptions were updated.
- [Popup Tab regression](../site-selector/popup-tab-regression.json): a real failing Chromium check and its passing fix
  (listbox removed from Tab order). See BUG-0036.
- Both CI providers run the new checks on push/PR; no hosted run.

## Deployment

[Final deployment](site-dropdown-deployment.json) replaced only the Controller web assets and source archive on
the disposable lab. The backend binary stayed byte-identical, served hashes match, rollback copies were kept and temporary access
was removed. The [earlier two-control deployment](deployment.json) is historical only. This is a UI overlay on the installed
`0.1.0+managed1`: no ISO build, production deployment or new storage verification.

Documentation range verification ran on an isolated Git snapshot; results stay in the local cache.
