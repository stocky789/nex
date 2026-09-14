# Site selector browser verification

Date: 2026-09-09. Scope: searchable site selection and selected site's pool → host → VM navigation.

## Method

[site_selector_browser.py](../../../tests/integration/site_selector_browser.py) serves the built UI
with **simulated API responses** (500 sites, each one pool/host/VM; creation changes in-memory JSON
only); Chromium 1440×1000 and 360×800. Build hashes pinned in [results.json](results.json); run
rejects a changing build; no screenshot or DOM editing.

```sh
.cache/phase1-qa-venv/bin/python tests/integration/site_selector_browser.py --evidence docs/qa/site-selector
```

## Results: all eight popup groups passed

- One site button opens a popup with search and bounded results; case-insensitive filter over 500;
  clear restores; no match leaves selection/tree unchanged; arrows+Enter select; Escape and selection
  return focus to trigger; outside click/Tab dismiss without stealing focus; reopening scrolls active
  result into view.
- Tree holds only the selected site's pool/host/VM; resource search cannot reveal another site; task
  navigation and reload keep the site.
- Direct pool/host/VM links select their site; reload and route changes keep it.
- 360px: search, selection and long names usable, no horizontal overflow.
- Saved site removed → first site on reload. Empty inventory offers creation; success selects new site.
- Sole site with no pools reaches **Site overview** and **Create pool** from workspace or tasks.

Inspected (no page errors): `desktop-500-sites.png` (not committed), `dropdown-500-sites.png` (not committed),
`search-matches.png` (not committed), `search-no-matches.png` (not committed), `narrow-dropdown.png` (not committed),
`narrow-long-name.png` (not committed), `narrow-selected-site.png` (not committed), `deep-link.png` (not committed),
`empty-sites.png` (not committed), `first-site-created.png` (not committed), `only-site-overview.png` (not committed),
`last-site-reopened.png` (not committed).

**Regression:** first build's scrollable listbox took an implicit Tab stop, bypassing input handlers;
dismissal assertion failed. `tabIndex=-1` on the listbox fixed it; all eight groups passed
([failure/pass evidence](popup-tab-regression.json)). Route fixtures, not native host.

## Existing Controller walkthrough

```sh
GO="$PWD/.cache/toolchains/go/bin/go" NEX_QA_CAPTURES=.cache/site-selector-phase1-captures .cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser --evidence docs/qa/site-selector/phase1-results.json
```

Passed 16 groups ([results](phase1-results.json)); real Controller/agent, simulated libvirt. Token
isolation now selects the site before its pool; resource search still requires host/pool ancestors.
First run failed on an obsolete assumption that an unselected site's pool is in the tree; fixed
navigation preserved delayed-token assertions. Rerun passed after the Tab fix on the same build.

Independent read-only review (harness, walkthrough diff, eight groups, Site 499 focus, Tab
failure/pass, fix, desktop/360px captures): no unresolved findings.

## Limits

API impact: existing contract. Fixtures prove navigation and presentation, not backend permissions,
native VMs or installed appliance. No hosted CI.
