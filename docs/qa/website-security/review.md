# Website security review — 2026-09-09

Status: Website/source and authenticated host configuration review completed;
hardening and external-network/recovery verification remain open. Not production
security acceptance.

The subsequent [authorized hosting hardening](hardening.md) records remediation of
the web-server findings and its actual verification. The findings below describe the
earlier review state; excluded firewall and SSH work was not performed.

## Scope and evidence

Reviewed the private website on the website host listener, its local static-site source and
existing deployment evidence. No host configuration, packages, firewall, DNS or
services were changed. Existing unrelated working-tree changes were preserved.

At approximately 01:46–01:47 UTC, read-only TCP connections found SSH and the website
listener reachable from the review workstation; ports 80 and 443 refused connections. This
was a four-port check, not a complete port scan or proof of Internet isolation.
SSH as the authorized operator initially failed with
`Permission denied (publickey,password)`. A subsequent SSH session as the authorized
user succeeded, and sudo-backed read-only host checks completed. No credentials
are retained in this report or repository files.

HTTP GET requests to `/`, `/gallery/` and `/_app/` returned 200. The gallery slash
redirect worked. `/.git/HEAD`, `/.env`, `/package.json` and a deliberately unknown
path returned 404. No response bodies from suspected secret files were printed.
All 11 served files matched the SHA-256 hashes in [the deployment manifest](../website/build.json).
Those matches tie this review to that artifact; they do not establish build-system integrity.

## Findings

1. **Medium — cleartext transport.** The site is served over HTTP, with no listener
   on the tested HTTPS port. An attacker able to manipulate traffic on the network
   path can replace page content. There are currently no credentials or forms to
   steal. Before production, use a maintained web server with trusted HTTPS; for a
   private preview use an appropriately trusted internal certificate or protected
   tunnel. Public DNS/certificates are not configured by this review.
2. **Low — production-hardening gap: development server.** Responses identify
   `SimpleHTTP/0.6 Python/3.12.3`, consistent with the recorded Python static server.
   Python explicitly warns that `http.server` is not recommended for production.
   Replace it before public exposure. Run the replacement as a dedicated,
   unprivileged, supervised service with read-only access to the selected release;
   inspect actual permissions before specifying changes. The banner alone cannot
   establish package CVE status because distributions backport patches.
3. **Low — hardening gap: directory listing enabled.** `GET /_app/` returned generated HTML with
   `Directory listing for`. This allows asset enumeration; no confidential file
   disclosure was demonstrated. Disable autoindex and prevent serving dotfiles,
   symlinks outside the release, source trees, credentials and deployment archives.
4. **Low — hardening gap: browser security headers absent.** Successful page responses had no
   Content-Security-Policy, X-Content-Type-Options, framing restrictions or
   Referrer-Policy. Current static content has no demonstrated injection or
   sensitive clickjacking action. Establish a restrictive CSP suited to the actual
   generated HTML, `nosniff`, `frame-ancestors 'none'` and a restrictive referrer
   policy; test the existing pages and gallery after enabling them. Enable HSTS
   only once trusted HTTPS is working for the intended hostname. Do not blindly
   preload or cover subdomains whose HTTPS readiness is unknown.

## Positive controls and boundaries

The website is prerendered, with client rendering disabled in
`website/src/routes/+layout.ts` and the static adapter in `website/svelte.config.js`.
It has no login, database, Controller connection or credential-entry workflow.
The three probed sensitive paths were absent, and served artifact hashes matched.
`noindex, nofollow` is present but is only a crawler hint, not privacy enforcement.
Private addressing alone cannot prove absence of NAT, proxy or IPv6 exposure.

## Independent source review

The independent source reviewer found no confirmed exploitable source vulnerability.
The local artifact contains exactly 11 HTML/CSS/PNG/license files, with no JavaScript,
source maps or symlinks. Parsed HTML has no scripts, forms, iframes or external
resource/navigation URLs. Gallery PNGs contain no metadata chunks.

`python3 tests/test_website_public_files.py` passed both tests. This is a targeted
denylist check, not proof that all possible secrets are absent. A fresh
`npm audit --json --ignore-scripts` reported three low entries arising from
[cookie GHSA-pxg6-pf52-xh8x](https://github.com/advisories/GHSA-pxg6-pf52-xh8x),
propagated through SvelteKit and adapter-static. Cookie serialization and the Node
runtime are absent from the served artifact, so this is not a demonstrated
deployed vulnerability. Reassess before adding dynamic routes. Lockfile package
URLs use HTTPS npm registry URLs with SHA512 integrity; this does not certify
dependency code. No rebuild or dependency modification was performed.

## Authenticated host findings

5. **Medium — no host-level network access restriction.** `ufw status verbose`
   reports inactive; `nft list ruleset`, `iptables-save` and `ip6tables-save` returned
   no rules. SSH listens on all IPv4/IPv6 interfaces; HTTP binds the private IPv4
   address. The observed NIC has private IPv4 and link-local IPv6 only. Any client
   with routed access can reach the services unless upstream controls stop it.
   This does not prove public exposure. Establish a default-deny host policy with
   explicit management and preview-client allowances, preserving the existing
   session and testing a second connection before enforcing it. Obtain the actual
   trusted management/client ranges first; do not assume the VM subnet is trusted.
6. **Medium — web process shares the administrator identity.** The Python server
   runs as the login user, who belongs to sudo and lxd groups, and owns all served
   files. Its AppArmor label is `unconfined`, `NoNewPrivs` is 0, Seccomp is 0 and
   effective capabilities are zero. This is not root execution or a demonstrated
   exploit, but a web-process compromise would inherit access to the user's files
   and writable website. Use a dedicated service user with read-only release
   access, separate deployment ownership and appropriate service sandboxing.
7. **Low — SSH authentication hardening remains.** `sshd -T` and connection-specific
   evaluation allow password authentication, public keys and `AuthenticationMethods
   any`; no AllowUsers/AllowGroups restriction was returned. Keyboard-interactive
   and empty passwords are disabled. Root password is locked; root authorized_keys
   is empty, while PermitRootLogin remains `without-password`. This does not show
   an available root login or an authentication bypass. After proving a fresh
   key-based login and recovery path, disable password authentication and explicitly
   disable root SSH. Remove unneeded X11/TCP forwarding only after confirming
   administration needs; forwarding is not itself an authentication bypass.
8. **Low — service/log lifecycle is temporary.** The website belongs to an SSH
   session scope rather than a dedicated service. Both output streams write to
   `preview.log`; no matching logrotate or system service configuration was found
   in the inspected system directories. An enabled system logrotate timer does not
   rotate this file automatically. Move serving into a supervised service with
   bounded logs, then verify restart and rollback in a disposable environment.
   Log flooding or disk exhaustion was not attempted or demonstrated.

## Host checks and positive controls

- Ubuntu 24.04.3 LTS, running kernel `6.8.0-139-generic`. Installed OpenSSH package
  `1:9.6p1-3ubuntu13.19`, Python `3.12.3-1ubuntu0.16`, AppArmor
  `4.0.1really4.0.1-0ubuntu0.24.04.7`, unattended-upgrades `2.9.1+nmu4ubuntu1`.
  Package revisions are recorded instead of inferring CVEs from upstream banners.
- Automatic list updates and unattended upgrades are configured daily with the
  security origin allowed; apt-daily-upgrade.timer is enabled. The unattended
  upgrade log records `All upgrades installed` on 2026-09-09 at 00:56:13.
  The earlier distro-info warning is followed by successful installation including
  distro-info-data; no continuing failure was established.
- A read-only python-apt comparison of every installed package against all newer
  versions in the cached security pocket found **zero pending security versions**.
  Security index timestamp was 2026-09-08 15:52. Indexes were not refreshed during
  this review, so this is not a claim about advisories published since that snapshot.
  Four non-security candidate upgrades remain: base-files, motd-news-config,
  python-apt-common and python3-apt. `dpkg --audit` reported no issues and no
  reboot-required marker existed. No updates or reboot were performed.
- AppArmor is loaded with 23 enforced profiles (116 total), but the web process is
  unconfined. NTP reports synchronized. SSH configuration syntax validates.
- Home directory permissions are 750, SSH directory 700 and authorized_keys 600.
  Sudoers is root-owned mode 440. Served files are owned by the web/login user at
  mode 644. The release selector points to the expected release; the HTTP root
  excludes sibling logs, PID file and deployment archive. Eleven served file
  hashes were already verified; the source reviewer found no asset symlinks.
- Root-backed socket inspection found only SSH and the preview externally bound,
  plus loopback DNS. No separate database or application backend listener appeared.
  Logs were inspected only for update outcomes and storage size, not copied wholesale.
  Sudo credential caching was invalidated after the checks.

## Remediation order and remaining limits

First preserve verified administrative access, then constrain network access and
separate the web service from the administrator account. Replace temporary hosting
with a maintained server, trusted HTTPS, disabled listings and tested response
headers. Preserve private availability until publication is explicitly authorized.
Finally verify restart, log retention, previous-release rollback and independent
backup/restore. These are recommendations, not changes performed by this review.

Router/NAT/reverse-proxy controls, Internet reachability, full account/key provenance,
backup restoration, reboot recovery and a comprehensive per-package CVE assessment
remain unverified. No brute force, exploitation, load or destructive tests ran.
The review is not a penetration-test certification or a guarantee of no vulnerabilities.

## Impact and checks

API impact: **none** — review-only documentation. Browser requests retrieve static
files; no client-facing API, Controller operation, Core command or observed-state
contract changes. Guide impact: **none** — this report changes no user workflow;
the existing guide already labels HTTP hosting as a temporary private preview.
No remediation is claimed, and no regression or hosted CI pass is claimed.

## Current primary guidance consulted

- [Python HTTP server warning](https://docs.python.org/3/library/http.server.html)
- [OWASP HTTP response headers](https://cheatsheetseries.owasp.org/cheatsheets/HTTP_Headers_Cheat_Sheet.html)
- [OWASP TLS guidance](https://cheatsheetseries.owasp.org/cheatsheets/Transport_Layer_Security_Cheat_Sheet.html)
- [Ubuntu OpenSSH configuration](https://ubuntu.com/server/docs/how-to/security/openssh-server/)
- [Ubuntu firewall configuration](https://documentation.ubuntu.com/server/how-to/security/firewalls/index.html)
- [Ubuntu package security versioning](https://wiki.ubuntu.com/SecurityTeam/FAQ)
- [Ubuntu automatic updates](https://ubuntu.com/server/docs/how-to/software/automatic-updates/)

These guide the recommendations; no universal standards certification or complete
absence of vulnerabilities is asserted.

Documentation structure checking passed, and all 13 documentation-checker unit
tests passed. Committed-range coverage and hosted CI were not run on this dirty
working tree. Independent report review checked scope, evidence and API/guide
impact; its severity correction for the development server was applied.

Independent review also accepted the authenticated-host findings and revised
assessment, checking severity, evidence limits and impact decisions. It did not
repeat the SSH inspection. Final documentation checks and all 13 checker tests
passed; `git diff --check` passed.
