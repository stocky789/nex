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

API impact: none (presentation only). Guide impact: none (no workflow or label change).

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
`captures/` (not committed) holds the 23 full-page views it produced, replacing the
partial gallery-script set used for the first inspection. Inspected captures:

| View/state | Capture |
| --- | --- |
| Desktop shell, brand mark, toolbar and tree icons | `captures/desktop-shell.png` (not committed) |
| Networking, Storage, Activity tabs | `captures/networking-desktop.png` (not committed), `captures/storage-desktop.png` (not committed), `captures/activity-desktop.png` (not committed) |
| Failed libvirt and timed-out probe badges | `captures/libvirt-failed-desktop.png` (not committed), `captures/probe-timeout-desktop.png` (not committed) |
| Stale host warning and amber dots at 360px | `captures/stale-host-360.png` (not committed) |
| Revoke confirmation and dispatched badge | `captures/revoke-confirmation.png` (not committed) |
| Revoked host and network error at 360px | `captures/revoked-host-360.png` (not committed), `captures/network-error-360.png` (not committed) |
| Task dock, loading and sign-in states | `captures/tasks-desktop.png` (not committed), `captures/loading.png` (not committed), `captures/idle-expired-login.png` (not committed) |

A 3x point-scaled crop of the title bar, toolbar and tree confirmed icon legibility
at 16px and 20px. The badge and alert palettes were chosen for text contrast at or
above 4.5:1. The earlier desktop-layout captures remain as that slice's pinned
evidence; this folder shows the current look.

The website gallery images under `website/static/gallery` still show the previous
palette; that untracked website slice was left untouched.
