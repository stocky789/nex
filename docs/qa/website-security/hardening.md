# Website hosting hardening — 2026-09-09

Status: authorized hardening deployed; browser, service, release and restore checks passed.
Remediates the web-server findings of the [security review](review.md). project owner
excluded firewall, SSH and non-allowed-IP testing. Pangolin terminates HTTPS and forwards to port
8080. API impact **none**; guide impact **updated** for hosting and recovery.

## Baseline and preparation

- Existing HTTPS route returned 200 with a valid certificate (curl per-request DNS override, since
  the workstation cached a negative answer). Pre-change backend CSP assertion failed (absent).
- Release `01b7f6ad207f` copied off the VM first; archive SHA-256
  `ca3fb045ad24fff1246edc1cb109a97959a5a9d4e1440d24ae17028413085b86`, stored in an ignored mode-700
  workstation directory (recovery copy, not scheduled backup).
- Ubuntu nginx/nginx-common `1.24.0-2ubuntu7.17` installed with the distribution service masked
  before install and kept masked (no default port-80 listener). Python kept serving during staging.
  No services needed restart.
- `/usr/share/doc/nginx/copyright`: BSD-2-Clause core with Expat, GPL-2-or-later and public-domain
  portions; separate system package, not linked or relicensed.
- `nex-website` system account, no login shell; release copied to root-owned
  `/srv/nex-website/releases/01b7f6ad207f` (755/644); service user cannot write the index.

## Deployed configuration and checks

At 02:19 UTC nginx replaced the checked Python process on the same listener; `nginx -t -c
/etc/nex-website/nginx.conf` as `nex-website` and systemd verification of all three units passed;
prepared fallback not needed.

| Check | Result |
| --- | --- |
| Service | `User=nex-website`, `NoNewPrivileges=yes`, active/running; master and workers on dedicated account |
| Health timer (5 min) | `Result=success`, `ExecMainStatus=0`; fired independently 02:25:31 UTC, exit 0 |
| Real Pangolin HTTPS | HTTP/2 200, certificate verified |
| Headers (also on errors) | CSP blocking scripts/frames/forms/connections, `nosniff`, `DENY`, `no-referrer`, HSTS `max-age=31536000` without includeSubDomains/preload |
| `/gallery` | Relative `/gallery/` redirect |
| Directories, dotfiles / unknown | 403 / 404 |
| HEAD / POST | Succeeds without body / denied (nginx integration check) |

### Runnable checks

- nginx policy tests on the VM with real nginx on loopback and disposable artifacts: 8 initial
  checks passed after correcting an expectation (refused symlink returns 404 via `try_files`; test
  now accepts either denial). Covers current-selector and unsafe symlinks, redirects, methods,
  headers, directory/dotfile protection, missing files.
- Python-server fixture reproduces missing headers and listing; pre-cutover live CSP assertion
  failed as expected.
- Release tests: atomic selection, retention, failed-health restoration, unsafe source symlinks,
  invalid rollback, partial-body curl failure, concurrent locks, second bootstrap refusal — 7 passed.
- [HTTPS browser walkthrough](browser/results.json): 4 groups passed (desktop/mobile skip and FAQ,
  gallery, JS-disabled, forbidden/missing paths); 22 same-origin GETs; no browser, CSP or network
  error; DNS override scoped to that browser launch. Captures `browser/home-faq-360.png` and
  `browser/gallery-1440.png` inspected by the primary (git-ignored, not in the repository).
- Both GitHub and Gitea workflows install nginx and run the hardened browser check on push/PR; no
  hosted run or branch protection claimed.

### Release, monitoring and restore walkthrough

| Step | Result |
| --- | --- |
| `website-release.py`: publish `20260909-hardened`, roll back to `01b7f6ad207f`, reselect | Each health check passed (identical bytes, so no broken-content exposure) |
| Publishing the `current` symlink itself | Refused before mutation; concrete directory succeeded |
| `systemctl restart nex-website.service` | Pass, health OK; `Restart=on-failure` installed, no crash or reboot induced |
| `logrotate --force /etc/logrotate.d/nex-website` | Both logs rotated; new request in new log. 7 daily, 10 MiB maxsize (not a hard cap) |
| Backup `/var/backups/nex-website-20260909.tar.gz` (releases, config, units, scripts, notices), mode 640, copied off-VM | Both copies `5372d05822506f7d6e46637e0f3b0292765382c625dbaa96f445dc1b823fd625`; no credentials |
| Isolated restore with fixture paths and loopback nginx | All 11 files matched saved bytes; fixture removed |
| [HTTPS served hashes](deployed-hashes.json) | All 11 match retained release backup |

Health failures surface only in systemd/journal. Health env file tightened to mode 600 after the
snapshot (private URL and expected text only). Only the private website port bound by nginx.

## Final check results

Disposable VM `python3 -m unittest discover -s tests -p 'test_website_*.py' -v`: **10 tests**
passed (3 hosting, 7 release/health). Public-file suite 2 tests, docs checker `--all` and 13 unit
tests, `git diff --check` passed. Independent pre-cutover review accepted config after log-path,
redirect and test-expectation corrections; final review reran release tests and docs checker, no
blockers.

## Reproduction and limits

[Hosting installation reference](../../../packaging/website/README.md) and
[operator guide](../../guide/operate-website.md). Initial release copied from the running preview,
not rebuilt from source; prior preview files kept, Python listener stopped.

- No SSH, firewall, Pangolin, DNS or allowlist changes; isolation not certified.
- No external alerting, automatic pruning or rollback, failure injection or host reboot (boot
  start unproven). Retained releases need manual space management.
- Restore proves static artifact/config only, not full-VM restore, mirror availability or
  scheduled off-site backup.
