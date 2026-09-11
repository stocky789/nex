# Controller UI icon and color refresh

Date: 2026-09-09
Status: Build, typecheck and real browser captures passed. No layout, sizing or spacing change.

## Scope

project owner asked for more vibrant colors and nicer icons in the Nex Controller UI without
touching the layout. The change replaces the muted single-color line icons with
two-tone filled SVG icons, uses the brand blue "n" mark for the workspace/home
entries, gives the title bar the brand navy, and brightens the status, badge,
selection, tab, alert and form tokens in `web/src/style.css`. All dimensions,
padding, grid columns and breakpoints are unchanged.

API impact: **none** — presentation only; no client request, Controller operation,
Core command or observed-state contract changes.
Guide impact: **none** — the guide describes workflows and labels, not icon shapes
or colors; no documented step changes.

## Verification

```sh
npm --prefix web run typecheck
npm --prefix web run build
GO="$PWD/.cache/toolchains/go/bin/go" \
NEX_QA_CAPTURES="$PWD/docs/qa/controller-ui-colors/captures" \
.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser \
  --evidence docs/qa/controller-ui-colors/results.json
```

All passed with all 16 result groups completed on the rebuilt bundle. The suite runs
the real Controller and agent in a disposable lab and drives headless Chromium at
1440x1000 and 360x800; [results.json](results.json) records the executed checks and
[captures/](captures/) holds the 23 full-page views it produced, replacing the
partial gallery-script set used for the first inspection. Inspected captures:

| View/state | Capture |
| --- | --- |
| Desktop shell, brand mark, toolbar and tree icons | [General](captures/desktop-shell.png) |
| Networking, Storage, Activity tabs | [Networking](captures/networking-desktop.png), [Storage](captures/storage-desktop.png), [Activity](captures/activity-desktop.png) |
| Failed libvirt and timed-out probe badges | [Failed](captures/libvirt-failed-desktop.png), [Timeout](captures/probe-timeout-desktop.png) |
| Stale host warning and amber dots at 360px | [Stale](captures/stale-host-360.png) |
| Revoke confirmation and dispatched badge | [Revoke](captures/revoke-confirmation.png) |
| Revoked host and network error at 360px | [Revoked](captures/revoked-host-360.png), [Error](captures/network-error-360.png) |
| Task dock, loading and sign-in states | [Tasks](captures/tasks-desktop.png), [Loading](captures/loading.png), [Expired](captures/idle-expired-login.png) |

A 3x point-scaled crop of the title bar, toolbar and tree confirmed icon legibility
at 16px and 20px. The badge and alert palettes were chosen for text contrast at or
above 4.5:1. The earlier desktop-layout captures remain as that slice's pinned
evidence; this folder shows the current look.

The website gallery images under `website/static/gallery` still show the previous
palette; that untracked website slice was left untouched.
