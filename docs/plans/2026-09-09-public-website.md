# Nex website and private HTTPS service

Date: 2026-09-09
Status: Static website and authorized private HTTPS hardening are deployed. Browser,
service, release and isolated static-restore checks passed; external isolation and
full-host recovery remain unverified.

## Scope and decisions

project owner authorized a simple Nex website/portal on a fresh Ubuntu website host,
with SSH as deployer. He selected SvelteKit with custom CSS after
considering Astro, and selected nexvirtualization.com as the future primary
domain. He explicitly requested that the website remain private until finished.
No public DNS, ingress, certificates, or public release publication is authorized
by this preview. The existing Controller React UI is unchanged.

project owner subsequently approved the visual design but requested plain community-focused
copy: Nex is a project for homelabbers to contribute to and build together. Replace
marketing slogans with concrete product/development information and contribution
ideas, without inventing a public contribution channel or download availability.
Nex continues to use Debian/KVM/libvirt rather than implementing a new hypervisor
engine. Also remove the pale raster backdrop visible behind both website logos.
API impact remains **none**; guide impact is **updated** for community positioning.

project owner also requested a gallery page showing the existing Controller web GUI. Reuse
actual screenshots from the disposable Controller QA environment, clearly labelled
as development captures. Provide descriptive captions and full-size image links;
do not imply the captured host data is a live service or a production installation.
The gallery is static content and does not add a Controller API connection.

project owner clarified that the project is **community-driven**, not specifically for
homelabbers; this supersedes the earlier audience wording. Keep the design and
remove audience-limiting terms throughout both pages and metadata. He also rejected
gallery screenshots containing his workstation identity. Capture the real Controller
in a fresh disposable environment using entirely fictional inventory and neutral
names, rather than reusing machine telemetry or editing screenshot pixels. Prevent
personal host identity from entering future gallery captures and exercise the guard
in both CI workflows. Use existing session/enrollment/inventory contracts for the
capture environment; no API behavior or reference change is needed.

Use the approved porcelain, graphite and blue Nex identity with a precise,
engineering-oriented composition, original architectural illustration and native
accessible navigation. Present Core and Controller clearly, with honest development
status and no invented download availability, customer metrics or account portal.
Use static SvelteKit output and custom CSS without a component library. No database,
account backend, APT publisher or Controller integration is needed for this slice.

The VM preview initially used its installed Python static server on its private address.
project owner has since authorized replacing that temporary server with a private HTTPS service
behind Pangolin. This authorization supersedes the earlier restriction on HTTPS setup,
but does not authorize unrestricted public publication. Keep the site private through
the selected Pangolin route and its access policy; Internet isolation was not exercised
in this slice.

The deployment is deliberately small: nginx serves the generated static release from
the root-owned `/srv/nex-website/releases` tree through the `current` selector, and
the dedicated `nex-website` service account has read-only access. Deployments retain
the previous verified release for rollback. The maintainer performs deployment as root;
the web service must not own releases. Disable directory listings, block dotfiles, and
set static response headers appropriate to the generated site. Retain journal-based
service monitoring and release rollback instructions in the operator guide. SSH,
host-firewall and trusted-network allowlist changes are excluded from this authorization.

**Roadmap page, 2026-09-11:** the project owner asked for a roadmap on the website that is
factual against the original plan and the repository — what is already done against what still
needs doing. Added as a prerendered `/roadmap/` page and a header navigation entry, structured on
this repository's own [build order](2026-09-08-nex-platform.md#build-order-basics-first) so the
website and the plan cannot describe two different products.

Statuses are derived from inspected code, guide pages and recorded QA evidence, not from plan
headings, and the page carries an explicit legend: built and checked, built but still in testing,
and next. Two consequences are deliberate and must be preserved by later edits. First, a milestone
stays marked as in testing while any of its acceptance checks remain unrun, so Phase 2 and Phase 3
are shown as in testing rather than complete. Second, every milestone shows its outstanding work
beside what is built, including UEFI, Secure Boot and physical-hardware verification; the browser
check asserts that both lists are present. The page publishes no dates.

This is website content only. It adds no Controller or Core behavior, and it does not change any
phase, gate or acceptance requirement in the platform plan — that plan remains the authority, and
the roadmap page is a rendering of it for readers. Keeping the two consistent is part of the work
that changes a milestone's status.

## API and guide impact

API impact: **none** — browser navigation loads static site files and local anchors.
There is no Controller operation, Core agent command, observed-state/result flow,
or new client-facing service API. Architecture artwork is illustrative, not live
inventory. Existing API contracts and their reference remain unchanged.

Guide impact: **updated** — document website navigation and the private HTTPS
operation, including deployment, monitoring and rollback. This is a separate static
information service, not a Controller management workflow.

## Acceptance

- Build and Svelte checks pass using locked dependencies; retain dependency licenses.
- Inspect desktop and narrow browser captures, keyboard navigation, FAQ interaction,
  anchor targets, reduced-motion behavior and absence of browser errors/overflow.
- Exercise navigation with JavaScript disabled and the unavailable-release state.
  No loading, stale inventory or destructive action states exist in this static site.
- Serve only the generated site through nginx as `nex-website`; preserve a previous
  release and verify a rollback before accepting it.
- Verify the selected HTTPS hostname through Pangolin, the intended static headers,
  unavailable-path handling and absence of directory listings. Do not claim Internet
  isolation because router, firewall and allowlist checks are excluded.
- Run the documentation checker and obtain independent implementation/evidence review.
- Record actual results and outstanding deployment/publication limits in QA evidence.

## Result

The earlier [VM walkthrough](../qa/website/checks.md) records clean Svelte checks/build,
matching deployed file hashes, desktop/tablet/mobile and JavaScript-disabled browser
passes. The [website hardening evidence](../qa/website-security/hardening.md) records
passed HTTPS browser, service, header, listing, release rollback and isolated static
restore checks. Router/NAT isolation, SSH/firewall/allowlist checks, host reboot
recovery and hosted CI remain unverified. Public publication, an account portal and APT
publication remain outside this slice.

API impact: **none** — the change serves static files through nginx and Pangolin. It
does not add or change a Nex Controller/Core/client service API, operation, command,
event or observed-state/result contract. Changed static headers and error behavior are
web-server behavior only.

## Presentation review — 9 September 2026

Keep the approved visual identity and private hosting. Tighten repository entry
pages and website copy, remove placeholder product wording and duplicate build
instructions, and improve small-screen text readability. Preserve development
status, licensing, fictional screenshot disclosure and detailed acceptance evidence.
API impact: **none** — static prose/styles and contributor navigation only; no
client API, Controller operation, Core command or result flow changes. Guide impact:
**none** — existing preview routes and operator steps are unchanged.
