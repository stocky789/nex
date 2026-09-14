# Private website verification

Date: 2026-09-09. Status: build, private VM browser walkthrough and independent review passed.
Scope: [website plan](../../plans/2026-09-09-public-website.md). API impact **none** (static
files, no Controller/Core call); capture harness uses existing contracts. Guide impact
**updated**: [website preview guide](../../guide/website-preview.md).

PNG captures named below are git-ignored (`docs/qa/**/*.png`) and not in the repository.

## Environment

| Item | Value |
| --- | --- |
| Host | Ubuntu 24.04.3 LTS, Python 3.12.3 static HTTP server on the private listener |
| Build | Node 24.20.0, npm 12.0.2, locked website dependencies |
| Browser | Playwright Python, Chromium 151.0.7922.34, real deployed static files |
| Not done | DNS for nexvirtualization.com, router ingress, TLS, system packages, publication |

## Actual checks

```sh
npm --prefix website run check
npm --prefix website run build
.cache/phase1-qa-venv/bin/python tests/integration/website_browser.py \
  --url https://nexvirtualization.com --evidence docs/qa/website
python3 scripts/check_docs.py --all
python3 -m unittest discover -s tests -p 'test_check_docs.py'
git diff --check
```

| Check | Result |
| --- | --- |
| Svelte check / static build | Pass, zero errors/warnings |
| Browser, 8 groups ([results.json](results.json)) | Pass: 1440/768/360px layouts, navigation, anchors, no overflow, skip link, FAQ, asserted reduced-motion durations, JS-disabled rendering, gallery images/alt/keyboard/Back/Home, no download links, no page errors |
| First gallery run | Fail: test asserted URL before keyboard navigation finished; synchronization fixed |
| Served files | All 11 SHA-256 match local build ([build.json](build.json)); listener bound to private address only; unknown path 404 |
| Logo backdrop pixels | Failed on old site (`Logo raster background differs from the page`); pass header/footer at 3 widths after fix ([BUG-0027](../../BUG_LEDGER.md#bug-0027--website-wordmark-retained-a-grey-raster-background)) |
| Docs checker `--all`, 13 checker unit tests, whitespace | Pass; committed-range and hosted CI not run |

Captures inspected by the primary: `page-1440.png`, `page-768.png`, `page-360.png`,
`faq-360.png`, `header-1440.png`, `gallery-1440.png`, `gallery-360.png`. Initial review
led to keeping all navigation visible on mobile and enlarging small text.

Follow-up: copy changed to **community-driven project**; homelab-only wording removed at the
owner's request and asserted by the browser test. Gallery images that reused personal
workstation data were replaced ([BUG-0028](../../BUG_LEDGER.md#bug-0028--website-gallery-reused-personal-workstation-identity))
by fresh captures, passing all three groups in
[capture results](../website-controller-captures/results.json):

```sh
GO="$PWD/.cache/toolchains/go/bin/go" \
  .cache/phase1-qa-venv/bin/python tests/integration/website_controller_captures.py \
  --output website/static/gallery \
  --evidence docs/qa/website-controller-captures/results.json
```

Method: real Controller on loopback with temporary state; TLS enrollment and mTLS reports
populate Community demo / Demo pool / nex-core-01 with fictional hardware; no native probes.
Chromium captures General, Networking, Storage, Activity after checking rendered text has no
credentials. Negative check: the original personal label created via the API is rejected by
the capture guard (no screenshot written). All four images inspected by capture owner,
reviewer and primary; hashes match evidence. Genuine UI captures of simulated infrastructure.

Guide navigation, FAQ and gallery steps exercised against the VM. Applicable error states:
unavailable release content and unknown-path 404. VM reboot/restart recovery not exercised.
Both hosted workflows run the website build/check and browser script on push/PR; no hosted run.

Independent review (scope, claims, guide/API, notices, manifests, captures): reduced-motion
and unavailable-release assertions requested and added; follow-up and privacy reviews of logo,
gallery, provenance and capture guard found no blockers.

## Limits

- Temporary server not enabled at boot. No public DNS/TLS, production server, portal or APT repo.
- `noindex` is a crawler hint, not access control. Gallery shows fictional demo infrastructure.
- Three low npm audit entries (cookie advisory); runtime cookie path absent from static output.
  See [website dependency notes](../../../website/README.md); not accepted for future dynamic routes.
- Only built site files deployed. Closes no platform release gate.

## Presentation review — 9 September 2026

Shortened README/product principles and contributor guide; tightened homepage/gallery copy.
Svelte check clean, build passed, both public-source tests passed, browser 8 groups passed at
3 widths and JS-disabled; all 11 served files match [build.json](build.json). Primary inspected
mobile homepage and desktop header (no clipping or logo backdrop). Independent review found no
issues; docs structure, 13 checker tests, links and whitespace passed. No publication, no hosted CI.
