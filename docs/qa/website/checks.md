# Private website verification

Date: 2026-09-09
Status: Build, private VM browser walkthrough and independent review passed.

## Scope and environment

The [website plan](../../plans/2026-09-09-public-website.md) authorizes the SvelteKit
static site and private preview. Actual destination: Ubuntu 24.04.3 LTS VM
172.16.4.3, Python 3.12.3 static HTTP server bound to 172.16.4.3:8080. The future
domain nexvirtualization.com was not connected to this VM; no DNS, router ingress,
TLS, system package installation or public publication was performed.

Build environment: Node 24.20.0, npm 12.0.2 and locked website dependencies. Browser:
existing Playwright Python environment with Chromium 151.0.7922.34. This exercises
the real deployed static files, not a simulation of a Controller or Core host.

API impact: **none** — static HTML/CSS/artwork requests, anchor navigation and native
FAQ disclosures call no Controller operation, Core command or observed-state flow.
No client-facing service API is introduced. Guide impact: **updated** in the
[website preview guide](../../guide/website-preview.md).

Capture harness API impact: **existing contract** — temporary administrator session,
site/pool creation, token issuance, CSR enrollment, mTLS inventory reports and browser
inventory/audit reads exercise existing contracts without changing their behavior.

## Actual checks

```sh
npm --prefix website run check
npm --prefix website run build
.cache/phase1-qa-venv/bin/python tests/integration/website_browser.py \
  --url http://172.16.4.3:8080 --evidence docs/qa/website
python3 scripts/check_docs.py --all
python3 -m unittest discover -s tests -p 'test_check_docs.py'
git diff --check
```

Svelte check passed with zero errors/warnings; the static build passed. The browser
check passed all eight groups in [results.json](results.json): 1440, 768 and 360px
layouts, visible/clickable navigation, every anchor target, no horizontal overflow,
keyboard skip link and FAQ open/close. Reduced-motion computed animation/transition
durations are asserted, rather than merely setting the browser preference. The
JavaScript-disabled context rendered the same content and interactive FAQ.
Gallery checks cover visible images/alt text, keyboard skip and full-size links,
browser Back and Home return, and navigation with JavaScript disabled. The final
test waits for keyboard-triggered image navigation to finish; its first run asserted
the URL too early, a test synchronization issue corrected before the passing run.
The release section explicitly says downloads are unavailable, and the check
asserts no ISO/package/appliance download links. No browser page errors occurred.

All eleven deployed files were fetched over the preview HTTP address and their
SHA-256 values matched the local build, including CSS, artwork and license notices.
See [build.json](build.json) for exact file and deployment archive digests. SSH
verified the listener uses only 172.16.4.3:8080. An unknown path returned HTTP 404.
This does not audit upstream router rules or certify Internet isolation.

The primary inspected initial and final rendered desktop/mobile captures, then the
final keyboard-focused FAQ capture. Initial review prompted keeping all navigation
links visible on mobile and increasing meaningful small text before acceptance.

- [Desktop final page](page-1440.png)
- [Tablet final page](page-768.png)
- [Mobile final page](page-360.png)
- [Mobile keyboard FAQ](faq-360.png)
- [Fixed header wordmark](header-1440.png)
- [Controller gallery desktop](gallery-1440.png)
- [Controller gallery mobile](gallery-360.png)

The follow-up retains the approved design and replaces marketing copy with plain
**community-driven project** wording. The user explicitly rejected a homelab-only
audience; both pages and metadata now omit that wording, checked by the browser test.
The original gallery reused internal QA images containing personal workstation data;
those website images have now been replaced with fresh captures of the actual
Controller running against entirely fictional demo inventory.

The capture command passed all three groups in
[fresh Controller capture results](../website-controller-captures/results.json):

```sh
GO="$PWD/.cache/toolchains/go/bin/go" \
  .cache/phase1-qa-venv/bin/python tests/integration/website_controller_captures.py \
  --output website/static/gallery \
  --evidence docs/qa/website-controller-captures/results.json
```

The real Controller starts on loopback with temporary state and credentials. Existing
TLS enrollment and authenticated mTLS reports populate Community demo / Demo pool /
nex-core-01 with explicit example hardware, NICs and disks. No native host probes run.
Chromium signs in and opens General, Networking, Storage and Activity, checking real
rendered text and absence of credentials before saving images. The negative check
then creates the original personal label through the real API and proves the capture
guard rejects its rendered text; no screenshot of that fixture is written. The
temporary Controller and its state are removed afterwards. See
[BUG-0028](../../BUG_LEDGER.md#bug-0028--website-gallery-reused-personal-workstation-identity).

All four final images were visually inspected by the capture owner and independent
reviewer. The primary inspected the Controller and deployed gallery captures. Image
hashes match the capture evidence and all served files match the build manifest.
These are genuine UI captures with simulated infrastructure, not edited screenshots
or hardware acceptance evidence; the gallery explicitly labels fictional demo data.

The logo regression check failed against the old deployed site with `Logo raster
background differs from the page`. The corrected shared CSS whitens the raster
backdrop before multiplication. Pixel assertions now pass for header and footer
at all three widths; the actual header capture confirms the grey rectangle is gone.
See [BUG-0027](../../BUG_LEDGER.md#bug-0027--website-wordmark-retained-a-grey-raster-background).

The guide's navigation, FAQ and gallery steps were exercised against this actual VM. No
loading, inventory-empty, stale-data, credential or destructive-action states exist
in this static information site. Unavailable release content and unknown-path 404
are the applicable unavailable/error states. The guide accurately limits restart
and public-hosting claims; VM reboot/restart recovery was not exercised.

Documentation structure (`--all`), all 13 documentation-checker unit tests and
whitespace checks passed. The scoped assessment's paths and current references were
checked separately; committed-range checking was not run on this uncommitted work.
Both hosted workflow files invoke website build/check and
the same browser script on pushes and PRs. No push or hosted CI run was performed.

Independent reviewer `/root/review_site` checked scope, source, product claims,
guide/API applicability, notices, deployed manifests/results and final desktop/mobile
captures. Its requests to substantiate reduced-motion and unavailable-release checks
were implemented and verified; it found no blocking issues in the final slice.
The follow-up review also inspected the corrected logo and gallery captures,
community copy, screenshot provenance and all eight deployed browser result groups.
The final privacy review inspected all four neutral Controller images, actual
rendered-identity rejection evidence, deployed gallery and updated captions; it
confirmed the capture uses existing APIs with no live probes and found no blockers.

## Limits

This temporary server is not enabled at boot; a VM reboot requires restarting it.
No public DNS/TLS, production web server, account portal or APT repository is
implemented. The noindex tag is a crawler hint, not authentication or network
access control. No credentials or personal/live infrastructure data are served; the
gallery contains saved screenshots of entirely fictional demo infrastructure.

The locked build toolchain has three low-severity npm audit entries from the same
cookie advisory; the affected runtime server cookie path is absent from the static
output. See the [website dependency notes](../../../website/README.md). This is not
an acceptance of that dependency for future dynamic server routes. Only built site
files are deployed; no ISO, appliance, third-party build tool binaries or npm tree
are published. This slice does not close any Nex platform release gate.

## Presentation review — 9 September 2026

Shortened the repository README/product principles and website contributor guide;
tightened homepage/gallery copy and adjusted small-screen informational text.
The approved layout, branding, neutral Controller images and navigation remain.
Detailed operator, installer, appliance and historical acceptance documents remain
in place; reducing front-door copy does not remove safety or verification evidence.

Svelte check: zero errors/warnings; static build passed. Both public-source tests
passed. The deployed browser walkthrough passed all eight groups at 1440, 768
and 360 pixels and with JavaScript disabled. Refreshed captures/results accompany
this report. All 11 served files match the local build; the exact archive and
manifest are in [build.json](build.json). Primary visual inspection of mobile
homepage and desktop header found no clipping or logo backdrop. Existing preview
guide steps still match the unchanged navigation. No public publication occurred.

Independent reviewer `/root/review_site` found no meaningful source or visual
issues after inspecting desktop home, mobile home and mobile gallery captures.
It confirmed API/guide impact is none for the unchanged workflows. Documentation
structure, all 13 checker tests, scoped assessment references, Markdown links and
whitespace checks passed. No committed-range or hosted CI run is claimed.
