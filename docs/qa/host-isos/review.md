# Independent review: host ISO visibility and site selection

Date: 2026-09-09. Verdict: no unresolved findings in the final single-dropdown
site selector and host ISO visibility slice. This review supersedes the earlier
separate search/native-select review.

## Scope and corrections

Reviewed `HostISOs`, its host Storage caller, retained-data refresh handling,
selected-site state, route-derived ownership, resource filtering and popup focus.
Media and destination storage require both the selected host and pool; historical
records remain visible without a libvirt inventory. No filesystem scan or new
Controller/Core action is introduced.

The owner corrected two misleading labels during review: successful imports now
say **Transferred · last verified**, and dispatched work says **Dispatched ·
awaiting result**. Fresh connectivity does not prove that a previously imported
file still exists. Stale/revoked hosts, old agents, missing storage verification
and failed Controller refreshes downgrade the successful record's access label.

The final site control is a single closed button. Opening it focuses search
inside a bounded popup with a labelled combobox/listbox, active-descendant arrow
navigation and Enter selection. Escape returns focus; outside clicks and Tab
close without stealing focus. Filtering alone never changes the selected site.
The **Site overview** link opens the current site, including an empty default
site. The tree includes only its pools, preserves global tasks, and follows
owning sites for pool/host/VM deep links.

Browser QA caught Chromium making the scrollable listbox an unintended Tab stop.
The owner added `tabIndex={-1}`; reviewed the exact correction and retained
[before/after regression](../site-selector/popup-tab-regression.json). The same
dismissal assertion now passes, and remains in both CI workflows.

## Evidence reviewed

- Independent UI typecheck passed. Read both complete focused browser harnesses
  and the existing Phase 1 assertion changes; original enrollment/recovery checks
  remain present.
- [ISO results](simulated-results.json): real HTTPS Controller with simulated mTLS
  agent, plus explicitly injected status, scope and failure responses. Checks
  include foreign-host/pool storage with matching IDs, pending/dispatched/failure,
  stale/revoked/old-agent/no-inventory, retained records on refresh error and Retry,
  keyboard task navigation and bounded 360px layout.
- [Site results](../site-selector/results.json): eight groups using the real UI
  with an explicit 500-site API fixture. Search/no-match, site-scoped resources,
  keyboard/focus/dismissal, deep links, reload, missing saved site, empty inventory
  and sole-site overview/Create pool are exercised. Reopening site 499 verifies
  its active option scrolls into view while search retains focus; long names and
  popup bounds are checked at 360px. This does not establish backend
  performance across 500 real sites.
- [Existing Phase 1 results](../site-selector/phase1-results.json): 16 reported
  browser/process groups passed, with libvirt simulation explicitly identified.
- [Native results](native-results.json): read-only browser checks on the retained
  disposable fresh-install host show its two existing transferred ISOs, scoped
  destination name/path, exact byte counts and task links. The actual site popup
  is opened, searched and selected using Enter. Independently inspected final
  native popup/360 captures and simulated desktop/360 popup captures; earlier
  host-status/error and sole-site captures were reviewed before the refinement. The narrow table scrolls internally; the simulated
  keyboard check proves the clipped task column is revealed on focus.

The simulated ISO, 500-site and native served UI evidence agrees on app.js SHA-256
`fd82675f5553f6ccfbe3f07a2fa8cb7e46769975622b137cacec3baf19887288`.
The site fixture records app.css SHA-256
`570292dc1d37ffc9825190e8a90b014c5eecac3db9d449271445ad2c5757882e`.

[Deployment evidence](site-dropdown-deployment.json) records the same served
JS/CSS and updated corresponding source on the retained disposable lab. The
Controller binary remains unchanged and temporary access cleanup is recorded.
This is a UI/source overlay, not a new installer or package.

## Contract, documentation and limits

API impact: **existing contract**. Existing authenticated site/pool/host/VM,
media/storage and operation reads supply this presentation. Import still follows
Controller dispatch, host validation and reported result; no endpoint, command,
permission or schema change is needed.

Guide impact: **updated**. Reviewed the Controller and VM guides, both relevant
plans and the scoped change assessment. They describe site selection, Site
overview, host transfer records and their freshness limitations. Both GitHub and
Gitea push/pull-request workflows invoke the two new checks with existing browser
prerequisites. Hosted CI execution is not claimed.

This review does not establish continuous ISO availability, new physical storage
correctness, a newly rebuilt installer, or production deployment. Native storage
and installation acceptance remain the separately recorded fresh-install work.
