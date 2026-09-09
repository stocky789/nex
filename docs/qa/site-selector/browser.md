# Site selector browser verification

Date: 2026-09-09. Scope: searchable site selection and the selected site's pool → host → VM navigation.

## Environment and executed method

The [focused browser harness](../../../tests/integration/site_selector_browser.py) serves
the real built React UI locally and exercises Chromium at 1440×1000 and 360×800.
Its API responses are explicitly simulated: 500 sites each have one pool, host and VM.
Site creation in this fixture changes only in-memory JSON. No Controller, native host,
disk or VM is operated by this test. No dependency was added.

```sh
.cache/phase1-qa-venv/bin/python tests/integration/site_selector_browser.py --evidence docs/qa/site-selector
```

The [executed results](results.json) retain the exact JavaScript and CSS SHA-256 values,
check groups and capture names. The harness rejects a build changing during the run.
Desktop images capture the real viewport; narrow images capture the full page.
No screenshot editing or DOM alteration is used.

## Observed results

All eight focused popup groups passed against the final JavaScript/CSS hashes in results.json:

- One closed site button opens a popup containing its search field and bounded scrolling
  results. Case-insensitive search filters 500 options; clearing restores them, while no
  matches leaves the selected site/tree unchanged. Arrows and Enter select a site, Escape
  returns focus to the trigger, and outside click/Tab dismiss without stealing focus.
  Reopening at the last site scrolls the active result into view.
- Outside click and Tab close the popup without stealing focus from the destination;
  Escape and selection return focus to the trigger.
- Only the selected site's pool, host and VM links exist in the infrastructure tree.
  Resource search cannot reveal another site's host. Global task navigation and reload
  preserve the chosen site within the browser tab.
- Direct pool, host and VM links select their owning site on load; reload and in-page
  route changes preserve the correct relationship.
- Popup search, selection and long site names remain usable at 360px; the page does not overflow
  horizontally.
- Removing the saved site from the simulated inventory falls back to the first
  available site on reload.
- Empty inventory offers site creation; the simulated success selects the new site
  and does not display unrelated resources.
- A sole default site with no pools can open **Site overview** from either the workspace
  or global tasks and then reach **Create pool**, without needing to change the native
  dropdown's already-selected value.

Inspected [500-site desktop](desktop-500-sites.png),
[open dropdown](dropdown-500-sites.png), [search results](search-matches.png), [no matches](search-no-matches.png),
[narrow popup](narrow-dropdown.png), [long site name](narrow-long-name.png), [narrow selected site](narrow-selected-site.png), [deep link](deep-link.png),
[empty sites](empty-sites.png), [first creation](first-site-created.png) and
[sole-site overview](only-site-overview.png), and [last site reopened](last-site-reopened.png): one closed selector sits directly below Resources and its search appears only inside the popup,
the tree begins at pools, labels and actions remain legible, and the empty/no-match states
explain the next action. No browser page errors occurred.

## Existing Controller walkthrough

The existing real-process walkthrough passed all 16 result groups with its site navigation
assertions updated; see [executed results](phase1-results.json). Its enrollment-token isolation check now selects
the intended site before opening its pool. Its resource-search check still requires host
and pool ancestors, while the selected site is verified through the new selector rather
than an obsolete site tree row. The first run stopped at an obsolete assumption that an
unselected site's pool was still in the tree; adapting that navigation to select its site
preserved the delayed-token safety assertions. The successful final command was:

```sh
GO="$PWD/.cache/toolchains/go/bin/go" NEX_QA_CAPTURES=.cache/site-selector-phase1-captures .cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser --evidence docs/qa/site-selector/phase1-results.json
```

This existing suite runs real HTTPS Controller and agent processes with simulated libvirt.
It passed actual browser sign-in/site/pool creation, enrollment token isolation, stale/error
and reconnect behavior, scoped resource search, native keyboard controls and session safety.

An independent read-only reviewer checked the final popup harness, existing walkthrough diff,
eight focused groups, Site 499 visibility/focus, preserved Tab failure/pass, implementation fix
and final desktop/360px popup captures, with no unresolved findings. The existing 16-group
walkthrough also passed again after the Tab fix against the same frozen UI build.

## Popup keyboard regression

The first popup build exposed Chromium's implicit focus stop on its scrollable listbox:
Tab left the input but stayed inside the popup, bypassing its input keyboard handlers.
The retained focused test failed its dismissal assertion. Giving the listbox `tabIndex=-1`
keeps search as the popup's single tab stop; the same assertion and all eight focused groups
passed on the corrected build. See [preserved failure/pass evidence](popup-tab-regression.json).
This is a real browser failure using route fixtures, not native-host evidence.

## Impact and limits

API impact: **existing contract** — the UI consumes existing site/pool/host/VM inventory
and creation responses; this test adds no runtime endpoint or Core command.
Guide impact: **updated** in the owning site-selector slice; this report provides browser
walkthrough evidence. Route fixtures prove navigation and presentation, not backend
permissions, native VM correctness or an installed appliance. Separate native verification
and deployment belong to their designated owners. No hosted CI execution is claimed.
