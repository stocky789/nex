# Website hosting hardening — 2026-09-09

Status: Authorized hosting hardening deployed; browser, service, release and restore
checks passed. External isolation and full-host recovery limits remain below.

## Scope

Matt authorized replacing the Python preview server, adding security headers and
listing restrictions, and implementing logs, health monitoring and release rollback.
He explicitly excluded firewall, SSH and non-allowed-IP testing. Pangolin continues
to terminate HTTPS for the domain and forward to the website's existing port 8080.
No Controller/Core operation or client service API changes: API impact **none**.
Guide impact **updated** for hosting and recovery operations.

## Baseline and preparation

The existing HTTPS route returned 200 with a valid hostname certificate when tested
with curl's per-request DNS override (the workstation still cached a negative DNS
answer). The Python backend served the same HTML as the domain. A pre-change CSP
assertion against the backend failed because the header was absent.

The current release `01b7f6ad207f` was copied off the VM before changes. Its compressed
archive SHA-256 is `ca3fb045ad24fff1246edc1cb109a97959a5a9d4e1440d24ae17028413085b86`.
The copy is in the review workstation's ignored, mode-700 backup directory; it is not
committed or web-served. This is a recovery copy, not scheduled off-site backup proof.

Installed Ubuntu nginx and nginx-common `1.24.0-2ubuntu7.17` after refreshing package
indexes. The distribution nginx service was masked before installation, then
persistently masked, so the package did not start its default port-80 listener.
The existing Python process continued serving during staging. No firewall or SSH
configuration was modified. Package installation reported no services needing restart.

Inspected `/usr/share/doc/nginx/copyright`: upstream core is BSD-2-Clause, with Expat,
GPL-2-or-later and public-domain portions listed. This is a separately executed
system package; no upstream code is linked into Nex or relicensed. The original
package notices remain installed; no ISO/package redistribution is performed here.

A dedicated `nex-website` system account has no login shell. The current artifact was
copied to root-owned `/srv/nex-website/releases/01b7f6ad207f` with 755 directories and
644 files. A read-only permission check confirmed the service user cannot write its
index. Complete deployed hashes will be rechecked after cutover.

## Deployed configuration and checks

At 02:19 UTC nginx replaced the selected Python process on the same private
website listener. The process identity was checked before stopping it.
`nginx -t -c /etc/nex-website/nginx.conf` ran as `nex-website` successfully, and
systemd verified all three units before starting the service. A fallback to the
existing preview was prepared but was not needed. No website HTML or assets changed.

The dedicated service and five-minute health timer are enabled. Actual service
inspection showed `User=nex-website`, `NoNewPrivileges=yes`, `ActiveState=active`,
`SubState=running`. The health service completed with `Result=success` and
`ExecMainStatus=0`. The distribution nginx service remains masked to prevent its
unrelated default configuration starting. nginx master and workers use the dedicated
account; root-owned static files are not writable by it.

The replacement returned HTTP/2 200 through the real Pangolin HTTPS route, with
certificate verification enabled. Responses carry restrictive CSP (scripts, frames,
forms and connections blocked; local images/styles allowed), `nosniff`, `DENY`,
`no-referrer`, and HSTS `max-age=31536000`. HSTS has no includeSubDomains/preload.
`/gallery` returns a **relative** `/gallery/` redirect, avoiding a redirect to the
private HTTP port. Directory and dotfile requests return 403; unknown pages return
404. Error responses retain the same security headers. HEAD succeeds without a body;
POST is denied in the real nginx integration check.

### Runnable checks

- The nginx policy and release tests ran in a temporary directory on the Ubuntu VM
  using real nginx bound to loopback and disposable artifacts. Eight initial checks
  passed after correcting a test that expected 403 for a refused symlink: nginx
  safely returned 404 through `try_files`. The test now permits either denial.
  It exercises the actual current-selector symlink, an unsafe artifact symlink,
  relative redirects, methods, headers, directory/dotfile protection and missing files.
- The original Python-server fixture reproduces missing headers and directory listing;
  the nginx policy check enforces the corrected behavior. The pre-cutover live CSP
  assertion also failed as expected. These failures were not presented as green tests.
- The release tests cover atomic selection, retention, failed-health restoration,
  unsafe source symlinks, invalid rollback, a partial-body curl failure, concurrent
  lock holders, and refusing a second bootstrap without health checks. The final
  seven release tests passed locally; the final combined nginx/release VM result is
  recorded after the last test update below.
- The real [HTTPS browser walkthrough](browser/results.json) passed all four groups:
  desktop/mobile keyboard skip and FAQ plus gallery images, JavaScript-disabled
  pages, and forbidden/missing paths. All 22 observed normal browser requests were
  same-origin GETs; no browser, CSP or network error occurred. Test-mode JS evaluation
  did not weaken the site's CSP or disable certificate validation. The DNS override
  applies only to this browser launch because this workstation cached the earlier
  negative lookup; no DNS/system resolver configuration was changed.
- The browser worker inspected all four captures; the primary inspected the
  [mobile page](browser/home-faq-360.png) and [desktop gallery](browser/gallery-1440.png).
  Both render correctly with the policy enabled. No loading, stale inventory,
  credential-entry or destructive-action state exists in this static website.
- Both GitHub and Gitea push/PR workflows install nginx before unit discovery and run
  the hardened browser check against a temporary nginx configuration. No commit,
  push, hosted CI result or branch-protection enforcement is claimed.

### Release, monitoring and restore walkthrough

Executed the installed `/usr/local/lib/nex-website/website-release.py` against the
private health URL: publish the existing artifact as `20260909-hardened`, roll back
explicitly to retained `01b7f6ad207f`, then select `20260909-hardened` again. Each
successful operation passed its health check. Both contain identical website bytes,
so the exercise did not expose outdated or intentionally broken content. An initial
attempt to publish the old `current` symlink was correctly refused before mutation;
using its concrete release directory succeeded.

`systemctl restart nex-website.service` succeeded; a subsequent health check passed.
The native `Restart=on-failure` policy is installed, but no production crash or host
reboot was induced. The enabled service's boot behavior has not been proven by a
VM reboot. `logrotate --force /etc/logrotate.d/nex-website` rotated both logs, and a
subsequent HTTP request appeared in the new access log after the reopen signal.
The policy retains seven daily rotations and uses a 10 MiB maxsize threshold when
logrotate runs. This is retention control, not a hard disk-usage cap between runs.
Health failures are visible through systemd/journal; no external alert destination
or notification service was configured.

A root-owned backup containing both releases, configuration, units, scripts and nginx
notices was created at `/var/backups/nex-website-20260909.tar.gz`, mode 640 for the
administrator group, then copied to the review workstation's protected ignored backup
directory. Both copies had SHA-256
`5372d05822506f7d6e46637e0f3b0292765382c625dbaa96f445dc1b823fd625`.
The archive contains no supplied authentication credentials.

An isolated temporary restore extracted regular files/directories from that archive,
substituted fixture paths and a loopback port in its saved nginx config, validated the
config and started a separate nginx process. **All 11 restored HTTP files matched
saved bytes**. The fixture was removed and its server stopped afterwards. Separately,
[all 11 files served through HTTPS](deployed-hashes.json) matched the retained release
backup. This proves static artifact/config restoration in that environment, not a
bare-metal/full-VM restore, package mirror availability, or scheduled off-site backup.

## Reproduction and limits

The [hosting installation reference](../../../packaging/website/README.md) records
configuration/unit installation; the [operator guide](../../guide/operate-website.md)
records publish, health and explicit rollback steps. In this deployment the initial
release was copied from the running preview into root-owned storage before cutover,
rather than rebuilt from the dirty source tree. The prior preview files remain for
recovery, but its Python listener was stopped.

There were no SSH, firewall, Pangolin rule, DNS or allowlist changes. The user explicitly
excluded external allowlist verification, so isolation is not certified. Upstream
HTTP remains behind the existing proxy/network path. No external alerting, automatic
release pruning, automatic release rollback after arbitrary later faults, production
failure injection, or host reboot was performed. Retained releases need deliberate
space management. The off-VM copy is a one-time recovery copy, not an unattended
backup schedule or an agreed long-term retention service.

## Final check results

The final combined disposable VM command `python3 -m unittest discover -s tests
-p 'test_website_*.py' -v` passed **10 tests** (three hosting and seven release/health
checks). The website public-file suite passed **2 tests**; documentation checker
`--all` passed and its **13 unit tests** passed; `git diff --check` passed.
Committed-range coverage is not applicable to this uncommitted dirty working tree.
Independent pre-cutover review accepted the final runtime configuration after the
log-path, redirect and test-expectation corrections. Final independent review accepted the code, guide/API decisions, deployment hashes,
browser evidence and stated limits; it reran the seven release tests and documentation
checker with no blockers.

The scheduled health timer independently fired at 02:25:31 UTC and exited with status
0; final service inspection remained active/running with its dedicated account and
NoNewPrivileges. Only the existing private website port remained bound by nginx.
The health environment file was tightened to root-only mode 600 after the recovery
snapshot; it contains only the private check URL and expected public text. SSH sudo
caching was invalidated and the interactive inspection session closed.
