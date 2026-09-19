# Website roadmap page verification

Decision: project owner, 2026-09-19 — `roadmap-1440.png` and `roadmap-360.png` are an accepted historical evidence limitation and are not to be recreated. This does not claim the files exist or waive verification for new changes.

Historical-evidence audit, 2026-09-19: no QA raster capture is tracked in this repository — `.gitignore` excludes `docs/qa/**/*.png` and the other image extensions — so every capture named in this report, including `header-360.png`, `roadmap-768.png` and `roadmap-hover-1440.png`, is absent from a fresh checkout and cannot be re-inspected here. The report/results below retain the original run's scope. No replacement images or new run are claimed. See [QA raster captures are untracked](../../DOCUMENTATION.md#qa-raster-captures-are-untracked).

Date: 2026-09-11. Status: build, local and deployed browser walkthroughs and capture inspection
passed; published as release `20260911-roadmap`. Open: independent review.

Scope: `/roadmap/` page of built, in-testing and not-started milestones, derived from the
[platform plan](../../plans/2026-09-08-nex-platform.md), slice plans, [guide index](../../guide/README.md),
[API reference](../../API.md) and source tree. API impact **none** (prerendered, `csr = false`).
Guide impact **updated**: [website preview guide](../../guide/website-preview.md).

Environment: Node 24.20.0, npm 12.0.2, locked dependencies; Playwright Python
(`.cache/phase1-qa-venv`), Chromium 151.0.7922.34. PNG captures below are git-ignored and not
in the repository.

## Actual checks

```sh
npm --prefix website run check
npm --prefix website run build
.cache/phase1-qa-venv/bin/python tests/integration/website_browser.py \
  --url http://127.0.0.1:8099 --evidence docs/qa/website-roadmap
python3 scripts/check_docs.py --worktree
git diff --check
```

| Check | Result |
| --- | --- |
| `svelte-check` | 0 errors, 0 warnings, 266 files |
| Build | Pass; `roadmap/index.html` `2f0cf0795a5829bc0264b10e030c31f85d44cff30495a8722fd1008c4330e62f` |
| Browser against local `http.server` build, 12 groups (was 8) | Pass at 1440/768/360: header nav, single `h1`, skip link, no overflow, 3-entry legend, each milestone has heading, one pill, items; both built **and** outstanding items listed; no `.iso`/`.deb`/`.qcow2` links; JS-disabled renders; no page errors |
| Existing check at `HEAD` | Failed on retired release copy ([BUG-0071](../../BUG_LEDGER.md#bug-0071--website-browser-check-asserted-release-copy-the-site-no-longer-had)) |
| 360px header | Failed: sixth link overflowed (`scrollWidth` 411 vs 360) ([BUG-0072](../../BUG_LEDGER.md#bug-0072--site-header-navigation-could-not-wrap-so-a-sixth-item-overflowed-at-360px)); fixed in shared stylesheet |

Captures inspected by the primary: `roadmap-1440.png`, `roadmap-768.png`, `roadmap-360.png`,
`header-360.png` (five links on one row, Releases on the next, Roadmap marked current).

## Content accuracy

- Built items correspond to code in `internal/`, `cmd/`, `web/src/`, guide pages and QA evidence.
- Phase 2 outstanding: VM deletion (not implemented) and the gate recorded as not run in the
  [VM creation plan](../../plans/2026-09-10-vm-creation-system.md#acceptance).
- Phase 3 outstanding: UEFI, Secure Boot, physical Intel/AMD, multi-disk safety, host-initiated
  connect to existing Controller.
- Phases 4–10 not started; source has no snapshot, backup, NFS, migration, evacuation, quorum,
  fencing, Swarm or Docker implementation (only unrelated `Snapshot()`, migration-job guard,
  CPU `migratable` matches).

## Deployed verification

The maintainer published with the release tool (agent has no root). `current` →
`/srv/nex-website/releases/20260911-roadmap`; `20260911-updates`, `20260909-hardened`,
`01b7f6ad207f` retained; `nex-website` and `nex-website-health.timer` active. Pre-publish,
deployed `index.html` was byte-identical to a `HEAD` build. Post-publish all **13** served files
matched the build manifest (roadmap hash as above).

| Request | Result |
|---|---|
| `/roadmap/` | 200, 18322 bytes |
| `/roadmap` | 200 |
| `/gallery/` | 200 |
| unknown path | 404 |
| `/_app/` | 403, no listing |

Headers: CSP `script-src 'none'`, `object-src 'none'`, `frame-ancestors 'none'`,
`form-action 'none'`; HSTS `max-age=31536000`; `nosniff`; `DENY`; `no-referrer`.
Browser: 12 groups passed against the live backend, no page errors; [results.json](results.json)
and captures regenerated from that run; styled rendering under CSP inspected.

Failures found only against the deployment:

- Logo backdrop assertion loaded a `data:` URI refused by `img-src 'self'`, aborting the run
  ([BUG-0074](../../BUG_LEDGER.md#bug-0074--website-browser-check-crashed-against-the-hardened-deployments-image-policy)).
- Navigation race: `locator.count()` sampled a partially parsed `/roadmap/`; now waits for load
  state. Caught before landing, so not in the ledger.

## Presentation rebuild — 11 September 2026

First layout rejected as too much reading; data unchanged, rebuilt as count strip, legend and
cards with segmented meters (one segment per listed item, since `style-src 'self'` blocks inline
widths) and native `<details>`. Meter counts listed items, not effort. Re-verified: `svelte-check`
clean, build clean, 12 groups pass at 3 widths and JS-disabled; keyboard Enter on a summary
reveals all 13 items of milestone 2; desktop, expanded-card and 360px captures inspected.
`roadmap/index.html` `989b1c281a0dd53ed308dd6fbc644e03775a0f62395440d5ea3f407dab39efe8`.

## Hover preview — floating, not reflowing

CSS-only float of the `<details>` panel (`::details-content`, absolute at `top: 100%`) behind
`@media (hover: hover) and (pointer: fine)`, including `:focus-within`; motion only under
`prefers-reduced-motion: no-preference`; panel capped at `58vh` with contained scroll.

| Measurement at 1440px | Result |
| --- | --- |
| Panel before hover | Not painted |
| On hover | Card bottom 809px, panel top 808px, width 413px |
| Card two rows below | Moved 0.0px; panel overlaps it; no horizontal overflow |
| Keyboard focus / Enter | Same panel / disclosure opens and reflows |
| Reduced-motion context | Panel appears without movement |
| Emulated touch (`hover: none`, `pointer: coarse`) | Tap opens in flow, no overflow |

Guarded in `tests/integration/website_browser.py` (now 13 groups). `roadmap-hover-1440.png` inspected.

## Roadmap impact gate

`scripts/check_docs.py` now requires `roadmap.impact` (`updated` touching the board, or `none`
with reason) for records dated from `2026-09-12` (undated records must answer). Both
documentation workflows already run it in range mode, so no workflow change. Four new checker
tests; suite 27 tests pass; `--all` passes 122 records; `--worktree` passes 164 paths. The gate
proves an answer exists, not that `none` is honest.

## Limits

- No dates; "built" is not production readiness, certification or supported release. Drifts
  unless updated with the work. Independent review not done.
- Verified on the private backend address only: Pangolin HTTPS route, browser TLS, access policy
  and Internet isolation not exercised (see [private HTTPS operation](../../guide/operate-website.md));
  observed HSTS is configured response, not public TLS proof. Rollback not exercised.
