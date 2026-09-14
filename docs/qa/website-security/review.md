# Website security review — 2026-09-09

Status: website/source and authenticated host configuration review completed; not production
security acceptance. Findings describe the pre-hardening state; web-server findings were
remediated in [hosting hardening](hardening.md) ([BUG-0035](../../BUG_LEDGER.md#bug-0035--website-preview-hosting-exposed-listings-and-lacked-browser-policy)).
Firewall and SSH findings were excluded from that work. API/guide impact **none** (review only).

## Scope and evidence

Read-only review of the private website listener, static-site source and deployment evidence;
no host configuration, packages, firewall, DNS or services changed.

| Probe (~01:46–01:47 UTC) | Result |
| --- | --- |
| TCP to SSH, website listener, 80, 443 (four ports, not a full scan) | SSH and website reachable; 80/443 refused |
| SSH as authorized operator | First `Permission denied (publickey,password)`; later session succeeded with sudo read-only checks |
| GET `/`, `/gallery/`, `/_app/` | 200; gallery slash redirect works |
| GET `/.git/HEAD`, `/.env`, `/package.json`, unknown | 404 (bodies not printed) |
| Served file hashes | All 11 match [deployment manifest](../website/build.json) (artifact tie, not build-system integrity) |

## Findings

| # | Severity | Finding | Recommendation |
| --- | --- | --- | --- |
| 1 | Medium | Cleartext HTTP, no HTTPS listener; path attacker can replace content (no credentials/forms) | Maintained server with trusted HTTPS or protected tunnel |
| 2 | Low | Development server `SimpleHTTP/0.6 Python/3.12.3` (not for production) | Dedicated unprivileged supervised server, read-only release; banner cannot establish CVE status |
| 3 | Low | `GET /_app/` returns `Directory listing for`; asset enumeration, no confidential disclosure shown | Disable autoindex; deny dotfiles, outside symlinks, sources, credentials, archives |
| 4 | Low | No CSP, `nosniff`, framing or referrer headers; no demonstrated injection | Restrictive CSP, `nosniff`, `frame-ancestors 'none'`, referrer policy; HSTS only after HTTPS works, no blind preload/subdomains |
| 5 | Medium | `ufw` inactive; nft/iptables/ip6tables empty; SSH on all interfaces; HTTP on private IPv4; NIC private IPv4 + link-local IPv6. Not proof of public exposure | Default-deny with explicit management/client ranges, testing a second session first |
| 6 | Medium | Web server runs as login user (sudo, lxd), owns served files; AppArmor `unconfined`, NoNewPrivs 0, Seccomp 0, zero effective caps | Dedicated service user, read-only release, separate deploy ownership, sandboxing |
| 7 | Low | `sshd -T`: password + pubkey, `AuthenticationMethods any`, no AllowUsers/Groups; kbd-interactive and empty passwords off; root password locked, root authorized_keys empty, `PermitRootLogin without-password`. No bypass shown | After proving key login and recovery, disable passwords and root SSH; review X11/TCP forwarding |
| 8 | Low | Server in SSH session scope; stdout/stderr to `preview.log`; no logrotate/unit found. Flooding not attempted | Supervised service with bounded logs; verify restart/rollback |

## Positive controls

- Prerendered, client rendering disabled (`website/src/routes/+layout.ts`, `website/svelte.config.js`);
  no login, database, Controller connection or credential entry. `noindex, nofollow` is only a hint.
- Independent source review: no confirmed exploitable vulnerability; artifact is exactly 11
  HTML/CSS/PNG/license files, no JS, source maps, symlinks, scripts, forms, iframes or external
  URLs; gallery PNGs have no metadata chunks.
- `python3 tests/test_website_public_files.py` passed both tests (denylist, not proof).
- `npm audit --json --ignore-scripts`: three low entries from
  [cookie GHSA-pxg6-pf52-xh8x](https://github.com/advisories/GHSA-pxg6-pf52-xh8x) via SvelteKit;
  cookie serialization and Node runtime absent from served artifact. Lockfile uses HTTPS URLs with
  SHA512 integrity (not code certification).
- Host: Ubuntu 24.04.3 LTS, kernel `6.8.0-139-generic`; OpenSSH `1:9.6p1-3ubuntu13.19`, Python
  `3.12.3-1ubuntu0.16`, AppArmor `4.0.1really4.0.1-0ubuntu0.24.04.7`, unattended-upgrades
  `2.9.1+nmu4ubuntu1`.
- Unattended upgrades daily with security origin; `apt-daily-upgrade.timer` enabled; log shows
  `All upgrades installed` 2026-09-09 00:56:13 (earlier distro-info warning followed by success).
- python-apt comparison against cached security pocket (index 2026-09-08 15:52, not refreshed):
  **zero pending security versions**; four non-security candidates (base-files, motd-news-config,
  python-apt-common, python3-apt). `dpkg --audit` clean; no reboot-required marker.
- AppArmor 23 enforced / 116 profiles; NTP synchronized; SSH config validates.
- Home 750, `.ssh` 700, authorized_keys 600, sudoers root 440; served files 644; HTTP root
  excludes logs, PID file and archive. Only SSH, preview and loopback DNS listening.
  Sudo credential cache invalidated after checks.

## Limits

Router/NAT/proxy controls, Internet reachability, account/key provenance, backup restoration,
reboot recovery and per-package CVE assessment unverified. No brute force, exploitation, load or
destructive tests; not a penetration-test certification.

References: [Python http.server](https://docs.python.org/3/library/http.server.html),
[OWASP headers](https://cheatsheetseries.owasp.org/cheatsheets/HTTP_Headers_Cheat_Sheet.html),
[OWASP TLS](https://cheatsheetseries.owasp.org/cheatsheets/Transport_Layer_Security_Cheat_Sheet.html),
[Ubuntu OpenSSH](https://ubuntu.com/server/docs/how-to/security/openssh-server/),
[Ubuntu firewalls](https://documentation.ubuntu.com/server/how-to/security/firewalls/index.html),
[Ubuntu security versioning](https://wiki.ubuntu.com/SecurityTeam/FAQ),
[Ubuntu automatic updates](https://ubuntu.com/server/docs/how-to/software/automatic-updates/).

Checks: docs structure and 13 checker unit tests passed; `git diff --check` passed; range and
hosted CI not run. Independent review applied a severity correction (finding 2) and accepted the
host findings without repeating the SSH inspection.
