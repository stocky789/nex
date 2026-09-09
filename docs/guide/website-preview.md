# Explore the Nex website

## Status

The SvelteKit website is a private HTTPS information site. It introduces Nex Core and
Nex Controller and explains their current release status. It is separate from the
Controller management interface. The selected HTTPS hostname is available through
Pangolin under the maintainer's private access policy; unrestricted publication is not
part of this service.

The site describes Nex as a community-driven project and explains ways to
help with testing, bug reports, documentation and code. Public contribution channels
are still being prepared; the preview does not provide a signup or submission form.

## Prerequisites

Use a browser that the maintainer has authorized through Pangolin, with the site
hostname supplied by the maintainer. No Nex account is required. This site contains no
infrastructure management or credential-entry forms.

## Steps

1. Open the supplied HTTPS website address in your browser.
2. Follow **Platform** to read about Nex Core and Nex Controller.
3. Follow **Approach** to explore the platform principles.
4. Follow **Releases** to check availability before looking for installation media.
5. Follow **Questions** and select a question to expand its answer. Keyboard users
   can Tab to a question and press Enter to open or close it.
6. Follow **Gallery** to see Controller development screenshots with fictional demo
   infrastructure. Follow a screenshot's
   full-size link to inspect it, then use your browser's Back action to return.

## Expected result

The page introduces the two products and distinguishes development work from future
capabilities. Release information does not offer an ISO, appliance or package download
before a public release is ready. Navigation and questions also work with JavaScript
disabled. The architecture illustration is explanatory, not live host inventory.

## Troubleshooting

- If the site is unreachable, confirm that your Pangolin access is authorized and ask
  the maintainer to check the website service.
- An unavailable route does not establish that the site is isolated from other networks.
  Router, firewall and allowlist verification is outside the recorded deployment checks.
- There is no portal login or download enrollment. No signup is necessary to read
  the preview, and it cannot connect to or update a Nex Controller or Core host.

## Verification

See the [website walkthrough](../qa/website/checks.md) for the original build and
browser captures, and [private HTTPS operation](operate-website.md) for deployment,
monitoring and rollback. The hardening evidence records the exercised HTTPS checks and
their limits.
