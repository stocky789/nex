# Website roadmap page verification

Date: 2026-09-11
Status: Build, local browser walkthrough, capture inspection **and deployed verification** passed.
The maintainer published release `20260911-roadmap`; the walkthrough was then re-run against the
live private backend. Open: independent review.

## Scope and environment

Adds a `/roadmap/` page to the private website recording which platform milestones are built,
which are built but still in testing, and which have not been started. Content is derived from
the [platform plan](../../plans/2026-09-08-nex-platform.md) build order and phases, the per-slice
plans under `docs/plans/`, the [user guide index](../../guide/README.md), the
[API reference](../../API.md) and the source tree, not from the plan's status lines alone.

Build environment: Node 24.20.0, npm 12.0.2 and the locked website dependencies. Browser:
existing Playwright Python environment (`.cache/phase1-qa-venv`) with Chromium 151.0.7922.34.

The walkthrough ran against the generated static build served by a local
`python3 -m http.server` listener on `127.0.0.1`, **not** against the deployed private site.
Earlier website evidence in [the website checks](../website/checks.md) was captured against the
deployed address; this run establishes the built output only. Deployment, served-file hashes,
HTTPS headers and rollback are unexercised here and remain covered by
[private HTTPS operation](../../guide/operate-website.md).

API impact: **none** — a prerendered static page with no client JavaScript
(`csr = false`) issues no Controller operation, Core agent command, observed-state flow or
client-facing API call. Guide impact: **updated** in the
[website preview guide](../../guide/website-preview.md).

## Actual checks

```sh
npm --prefix website run check
npm --prefix website run build
.cache/phase1-qa-venv/bin/python tests/integration/website_browser.py \
  --url http://127.0.0.1:8099 --evidence docs/qa/website-roadmap
python3 scripts/check_docs.py --worktree
git diff --check
```

`svelte-check` reported 0 errors and 0 warnings across 266 files; the static build succeeded and
emitted `build/roadmap/index.html` (SHA-256
`2f0cf0795a5829bc0264b10e030c31f85d44cff30495a8722fd1008c4330e62f` for the inspected build).

The browser check passed all 12 groups in [results.json](results.json), up from 8 before this
change. At 1440, 768 and 360 pixels it exercised roadmap navigation from the header, a single
`h1`, the keyboard skip link, no horizontal overflow, the three-entry status legend, and every
milestone having a heading, exactly one status pill and at least one list item. It asserts both
that built items are listed **and** that outstanding items are listed, so a future edit cannot
quietly turn the page into a features list. It also asserts the page links no `.iso`, `.deb` or
`.qcow2` media. The JavaScript-disabled context rendered the milestones and both list kinds,
which matters because the site is prerendered with client rendering disabled. No browser page
errors occurred.

Two defects were found and fixed during this work:

- [BUG-0071](../../BUG_LEDGER.md#bug-0071--website-browser-check-asserted-release-copy-the-site-no-longer-had):
  the existing check asserted retired Development status copy and therefore failed at `HEAD`,
  before any roadmap change. Reproduced by serving the `HEAD` build and running the check.
- [BUG-0072](../../BUG_LEDGER.md#bug-0072--site-header-navigation-could-not-wrap-so-a-sixth-item-overflowed-at-360px):
  the shared header navigation could not wrap, so the sixth link overflowed the 360px viewport
  (`scrollWidth` 411 against `innerWidth` 360). Fixed in the shared stylesheet rather than in the
  new page's scoped styles, which is where the Gallery page had been hiding the same defect.

## Inspected captures

The primary inspected each capture rather than relying on the assertions alone.

- [Desktop roadmap](roadmap-1440.png) — the two-column milestone layout, status pills and
  built/outstanding lists render as intended; the sticky milestone heading stays readable.
- [Tablet roadmap](roadmap-768.png)
- [Mobile roadmap](roadmap-360.png) — single column, legend and checklists legible, no clipping.
- [Mobile header](header-360.png) — the wrapped navigation reads as deliberate: five text links on
  one row and the Releases control on the next, with the Roadmap link marked as the current page.

## Content accuracy

Each status was checked against the repository rather than copied from a plan heading:

- Built-and-checked and built-still-in-testing items correspond to code present in `internal/`,
  `cmd/` and `web/src/`, to guide pages under `docs/guide/`, and to recorded QA evidence.
- The outstanding Phase 2 items are the VM deletion slice, which is not implemented, and the
  Phase 2 acceptance gate, recorded as not run in the
  [VM creation system plan](../../plans/2026-09-10-vm-creation-system.md#acceptance).
- The outstanding Phase 3 items are UEFI installation, Secure Boot, physical Intel/AMD hardware,
  multiple-disk safety and the host-initiated "connect to an existing Controller" choice, each
  recorded as unverified or unimplemented in the installer QA and the platform plan.
- Phases 4 to 10 are presented as not started. Confirmed by source inspection: there is no
  snapshot, backup, NFS, migration, evacuation, quorum, fencing, Swarm or Docker implementation;
  the only matches for those terms are an unrelated inventory `Snapshot()` accessor, a libvirt
  migration-job guard and a CPU `migratable` comment.

## Deployed verification

The maintainer published the staged build as release `20260911-roadmap` using the documented
release tool; the agent has no root on that host and did not perform the switch. `current` now
resolves to `/srv/nex-website/releases/20260911-roadmap`, with `20260911-updates`,
`20260909-hardened` and `01b7f6ad207f` retained for rollback, and both `nex-website` and
`nex-website-health.timer` active.

Before publishing, the deployed `index.html` was confirmed byte-identical to a build of `HEAD`,
so this release added the roadmap and the shared navigation change and removed nothing. After
publishing, all **13** served files were fetched from the private backend and every SHA-256
matched the local build manifest, including `roadmap/index.html`
(`2f0cf0795a5829bc0264b10e030c31f85d44cff30495a8722fd1008c4330e62f`).

nginx behavior on the live release:

| Request | Result |
|---|---|
| `/roadmap/` | 200, 18322 bytes |
| `/roadmap` (no trailing slash) | 200 |
| `/gallery/` | 200 |
| unknown path | 404 |
| `/_app/` directory | 403, no listing |

Response headers carry the strict policy: `content-security-policy` with `script-src 'none'`,
`object-src 'none'`, `frame-ancestors 'none'` and `form-action 'none'`;
`strict-transport-security max-age=31536000`; `x-content-type-options: nosniff`;
`x-frame-options: DENY`; `referrer-policy: no-referrer`. `script-src 'none'` also confirms the
page's no-JavaScript design is enforced by policy, not merely by convention.

The browser walkthrough then passed all 12 groups against the live backend with no page errors;
[results.json](results.json) records that run and the captures in this directory were regenerated
from it. The deployed capture was inspected and renders fully styled under the CSP, with the
Roadmap link marked as the current page.

Two defects surfaced only once the check ran against the real deployment:

- [BUG-0073](../../BUG_LEDGER.md#bug-0073--website-browser-check-crashed-against-the-hardened-deployments-image-policy):
  the pre-existing logo backdrop assertion re-loads its capture through a `data:` URI, which the
  deployment's `img-src 'self'` correctly refuses, aborting the whole run. It now skips with that
  reason recorded where the policy applies and still runs everywhere else, including both CI
  workflows, which serve the build over `python3 -m http.server` without a CSP.
- A navigation race in the new roadmap checks: `locator.count()` does not retry, so clicking
  through to `/roadmap/` and counting immediately could sample a partially parsed document — the
  heading existed while the milestone list further down did not. Both roadmap navigations now wait
  for the load state. This was caught before the change landed; it is recorded here rather than in
  the ledger because it never reached CI.

## Presentation rebuild — 11 September 2026

The first layout was rejected as too much reading. The milestone data is unchanged; only its
presentation was rebuilt as a scannable board: a count strip (built / in testing / ahead), a
one-line legend, and a card per milestone carrying its number, status pill, one-line summary and a
segmented meter, with every item moved behind a native `<details>` disclosure. The page now reads
at a glance and still holds every fact one activation away.

Two constraints shaped it. The deployed CSP is `style-src 'self'`, which blocks inline `style`
attributes, so no percentage-width progress bars: each meter is one segment per listed item,
which also avoids implying a precision the data does not have. And `csr = false` means no
JavaScript, so the disclosures are native `<details>` rather than scripted accordions — verified
by the JavaScript-disabled group, which still finds the milestones and both list kinds.

The meter counts **items listed on this page**, not effort or time, and the page says so beside
the legend. It is a reading aid, not a schedule.

Re-verified after the rebuild: `svelte-check` 0 errors and 0 warnings, clean build, and all 12
browser groups passing at 1440, 768 and 360 pixels and with JavaScript disabled. The expanded
state was exercised by keyboard — focusing a card's summary and pressing Enter reveals all 13
items of the second milestone — and the desktop, expanded-card and 360px captures were inspected.
`roadmap/index.html` for the rebuilt page is
`989b1c281a0dd53ed308dd6fbc644e03775a0f62395440d5ea3f407dab39efe8`.

## Keeping the board honest in CI

A roadmap is only worth publishing if it stays true, so the change-record gate now carries a third
assessment beside guide and API impact. Every record declares `roadmap.impact` as `updated` — naming
the board page, which must actually change in the same range — or `none` with a reason. Both
`.github/workflows/documentation.yml` and `.gitea/workflows/documentation.yml` already run
`scripts/check_docs.py` in range mode on pushes and pull requests, and its unit tests alongside, so
enforcement needed no workflow file change; adding a separate job would have duplicated an existing
one. As with guide and API impact, the gate proves the question was answered, not that `none` was
honest — that stays a reviewer's judgement.

Records are not retrofitted. The date in each record's filename is the boundary
(`ROADMAP_REQUIRED_FROM = '2026-09-12'`), so all 122 existing records and the 20 still pending in
the working tree stay valid and no migration commit rewrites other people's unlanded work. A record
whose filename carries no date must answer regardless, so dropping the date cannot opt out.

Four new checker tests cover it: a record from the cutoff onward missing the field, an `updated`
claim that never touched the board, pages pointing outside the board area, and earlier records
staying valid under both range and `--all` modes. The suite passes 27 tests;
`check_docs.py --all` passes against all 122 records and `--worktree` passes for 164 uncommitted
paths, confirming no in-flight work was broken.

## Limits

The page states no dates, and "built" here means present in a build with recorded checks — it is
not a claim of production readiness, hardware certification or a supported release. Independent
review of this slice has not been carried out. The roadmap reflects the repository on 2026-09-11
and will drift unless it is updated with the work it describes.

Verification reached the private backend on its own address. The Pangolin HTTPS route, browser
TLS, access policy and Internet isolation were not exercised here and remain covered by
[private HTTPS operation](../../guide/operate-website.md); the `strict-transport-security` header
observed on the backend is the configured response, not evidence of a working public TLS path.
Rollback was not exercised — the retained releases are present but no rollback was performed.
