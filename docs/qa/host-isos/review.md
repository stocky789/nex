# Independent review: host ISO visibility and site selection

Date: 2026-09-09. Verdict: no unresolved findings. Supersedes the earlier separate search/native-select review.

## Scope and corrections

Reviewed `HostISOs`, its Storage caller, retained-data refresh, selected-site state, route-derived ownership,
resource filtering and popup focus. No filesystem scan or new Controller/Core action.

Corrections made during review:

- Labels: success now says **Transferred · last verified** (later **Transfer succeeded · not rescanned**), and dispatched
  work says **Dispatched · awaiting result**. Stale/revoked/old-agent/unverified-storage/failed-refresh states downgrade access.
- Chromium made the scrollable listbox a Tab stop. Fixed with `tabIndex={-1}`, with a
  [before/after regression](../site-selector/popup-tab-regression.json) in both CI workflows.

Site control checked: a single button; search focused in a bounded combobox/listbox; arrow/Enter selection; Escape
returns focus; outside click/Tab close; filtering never changes the site; Site overview works for an empty default site; deep links
follow owning sites.

## Evidence reviewed

- Independent UI typecheck passed. Both browser harnesses and the Phase 1 assertion changes were read; enrollment/recovery checks remain.
- [ISO results](simulated-results.json) and [browser.md](browser.md): foreign IDs, statuses, retained records, Retry,
  keyboard task navigation, 360 px.
- [Site results](../site-selector/results.json): 8 groups (search/no-match, scoping, focus/dismissal, deep links,
  reload, missing saved site, empty inventory, sole site; site 499 scrolls into view; 360 px bounds).
- [Phase 1 results](../site-selector/phase1-results.json): 16 groups, libvirt simulated.
- [Native results](native-results.json): two real ISOs, scoped storage, exact bytes, task links, popup
  search/Enter. The reviewer inspected the final native popup/360 captures and the simulated desktop/360 popup captures.
- All three runs agree on the hashes in [checks.md](checks.md). The [deployment evidence](site-dropdown-deployment.json)
  records the same served assets.

## Limits

Guide and plan updates were reviewed. No hosted CI, continuous ISO availability, new storage correctness, rebuilt installer or
production deployment. Native storage/install acceptance belongs to fresh-install QA.
