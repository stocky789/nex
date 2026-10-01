# Public Controller demo verification

Date: 2026-10-01

## Scope and environment

This is a sanitized verification narrative, not an untouched log capture. Deployment
addresses, administrator identities, SSH keys, cookies and machine identifiers are
intentionally omitted. The selected website server runs Ubuntu 24.04.5 and Docker
29.1.3 with the containerd image store. It hosts a separate, fictional read-only demo;
no Nex Core host, libvirt instance, real VM, backup destination or restore was exercised.

Published image revision: `f37168f3f60abdc5d6fed4dd05e776cc8c477eea`.
This identifies an uncommitted worktree snapshot, not a Git commit or released version.
The matching downloadable source archive SHA-256 is
`b3287683f5b4e7c19e580857f92e4c030d0bd63d4ef7278d173b8a6ede42645b`.
The shared UI and demo executable were extracted from the actual built image for checks.
The initial installation, rollback and reboot checks below used the earlier `d9a4671`
snapshot. The demo-only follow-up below covers the expanded image; no second reboot
was needed or claimed for this presentation/data update.

## Demo-only follow-up: login and expanded inventory

The project owner requested a prominent demo login hint plus more fictional hosts,
VMs, tags and quorum/cluster examples. The demo build now presents a credential panel
on the login card: Username `demo`, Password `demo`. The normal Controller build keeps
its original administrator login wording and has no demo credential panel.

Observed inventory: 44 VMs and 11 hosts across the same three pools; every VM has five
tags, with 32 distinct tags across the inventory. Colored chips reuse the normal
name-derived tag palette. The production example has five hosts and 20 VMs; research
and edge each have three hosts and 12 VMs. Existing Members → About details show
simulated pool quorum/emulated-watchdog reports: production/edge mock-armed, research
off. Every added report detail discloses simulation; no extra Cluster hierarchy,
real quorum, watchdog or host capability was implemented.

Executed checks:

- `go test -race -timeout 5m ./cmd/nex-demo ./internal/demo` passed after adapting
  catalog regressions to choose an actually foreign VM and compare matching record
  identities, rather than freezing fixture names/counts. New fixture regressions
  check placement, source references, access and fencing/inventory consistency.
- `npm run typecheck && npm run build` in `web/` passed for the normal Controller.
  The Docker build also typechecked and built the demo variant.
- Actual browser tabs inspected both login builds; only the demo had the credential
  panel. Real demo login succeeded, with no page errors. Desktop and phone login
  captures, colored tag grouping, five-host armed and three-host off quorum examples
  were inspected.
- The full browser script passed on the extracted final executable/UI/source inputs:
  login/refusal, scoped inventory, host hardware, storage/networks, read-only backup
  action, recovery points, quorum, repository settings, themes/phone layout, source,
  API reference and logout. No page errors or failed API reads were reported.
  The final image's UI assets were byte-compared with the browsed assets and matched.
- The validated root importer actually published the final image healthy and retained
  the initial `d9a4671` image for rollback. The installed private proxy returned 200;
  Unix healthcheck passed, `demo` login returned 200, and authenticated installed reads
  reported 44 VMs, 11 hosts and 32 distinct tags. Its source download matched the
  current SHA-256 above.

The browser walkthrough used development loopback HTTP, not the externally restricted
hostname. Public TLS subsequently validated, but the edge returned 401 even for the
login page. The project owner confirmed intentional proxy IP allowlisting; it was
preserved. No public-browser success or broader access is claimed. No product API
contract, durable Core state or real host behavior changed. Independent source review
of the login separation, fixture consistency, catalog regressions and guide/API changes
found no unresolved must-fix finding; the parent executed runtime checks.

## Browser walkthrough

The actual installed service was exercised through a disposable local HTTPS front end
that forwarded requests to the installed private nginx listener without mocking API
responses. Its self-signed certificate was explicitly trusted only for this verification;
it translated the local test Origin to the configured public Origin. This establishes
installed demo behavior, not the external public DNS, certificate or proxy route.

Command:

```sh
.cache/demo-qa-venv/bin/python tests/integration/demo_browser.py \
  --url https://127.0.0.1:18932 --ignore-https-errors \
  --source-archive .cache/demo-source.tar.gz \
  --evidence .cache/demo-browser-scoped
```

The walkthrough passed against the installed service before and after the authorized
reboot. The final scoped walkthrough also passed with these observed checks:

- Invalid credentials followed by successful `demo` / `demo` login.
- VM search, detail and configuration browsing.
- Change to a pool in another fictional site: its VM row appeared and the original
  pool's VM row disappeared; returning restored the original inventory.
- Host detail and hardware, storage and network registrations.
- Expanded backup jobs and their covered VMs; a real Pause submission returned 403
  and left the job unchanged.
- Recovery-point inspection, backup repository settings and Accessible master status.
- Tasks, Events, both light/dark Overview themes and a phone-sized Backups layout.
- Downloaded corresponding source matched the shipped archive byte-for-byte.
- The API reference opened, and logout removed browser-authenticated access.

Results: no browser page errors and no failed API read requests. Local captures of VM
details, pool scope, recovery points, light/dark Overview and phone Backups were
inspected; they remain disposable local evidence, not published environment captures.
Transient loading was observed. A further passing walkthrough launched the executable
and UI extracted from the published image on development loopback HTTP. Its settled
recovery capture additionally required the history-loading indicator to disappear; that
capture was inspected. This extra run is not evidence of production TLS/cookie settings.

## Authentication, isolation and hosting

Observed on the installed deployment:

- Login cookies were Secure, HttpOnly, SameSite=Strict and scoped to `/`. An off-origin
  login returned 403. Oversized login input returned 413 at the private proxy.
- Console and upgrade attempts through nginx returned 404, not a stream or successful
  response. Direct adapter tests also cover upgrade refusal and mutation boundaries.
- Container UID/GID 64050, network mode `none`, no published ports, read-only root,
  all capabilities dropped and no-new-privileges enabled. The only host mount is the
  bounded Unix socket directory; no Docker socket, credentials or host data is mounted.
- Runtime limits: 192 MiB memory without additional swap, one CPU, 64 PIDs,
  1024 file descriptors, 8 MiB temporary space and rotated container logs.
- Socket directory: separate 1 MiB tmpfs, 128 inodes, owner-only permissions,
  `nosuid,nodev,noexec`; restored automatically before Docker on boot.
- Separate non-root nginx service: private listener on 8081, 128 MiB memory limit,
  restricted bind/peer addresses, filesystem protection and no-new-privileges.
- Default-deny host ingress; SSH permits only selected administration/publishing
  origins. Website/demo HTTP listeners permit the selected HTTPS proxies.
- Effective SSH settings disable passwords, keyboard-interactive login, root login,
  agent forwarding and X11 forwarding. Host security updates were installed; no
  upgradable packages remained in the checked APT metadata after reboot.
- Dedicated publishing account has no Docker-group membership and only the anchored,
  single-revision deployment sudo rule. Its home/key/helper are root-owned. Its
  publishing key rejected shell execution, arbitrary sudo and SSH forwarding.
- Four separate `NEX_DEMO_DEPLOY_*` Gitea secrets were created successfully. Existing
  website deployment credentials were not changed or passed to the demo job.
- Proxy access logging is disabled. A harmless oversized-request error was actually
  received by the dedicated journal namespace. Loaded settings use volatile storage,
  an 8 MiB runtime retention limit, rate limiting and no forwarding to other log sinks.

## Publication, rollback and reboot

`demo-ci-deploy.sh` was executed with the configured restricted publishing credentials;
it published the reviewed image, reported a healthy Unix endpoint and retained the
previous image. This is a real script execution, not a Gitea workflow run.

Two controlled disposable candidates were published through that same script:

1. Missing executable: Docker startup failed; the prior image was restored healthy.
2. Existing executable without required assets: service health failed; the prior image
   was restored healthy.

After the cleanup correction, both cases were repeated: the prior service remained
healthy and each failed candidate image was absent. Replaying the real image through
its importer preserved its immutable configuration on Docker/containerd.

The project owner explicitly authorized the host update and reboot. The boot identifier
changed, the kernel advanced from `6.8.0-139-generic` to `6.8.0-146-generic`, and Docker,
the demo proxy and the existing website service recovered active. The selected demo
container recovered healthy without republishing; its socket tmpfs and journal
namespace were recreated. A pre-reboot session returned 401, as documented; new login
and the full browser walkthrough passed. The existing public website returned HTTP 200
with its security headers after reboot.

## Focused checks and independent review

Executed successfully:

```sh
go test -race -timeout 5m ./cmd/nex-demo ./internal/demo
python3 -m unittest discover -s tests -p 'test_demo_archive.py'
python3 tests/integration/demo_deploy_image.py --image "nex-demo:$RELEASE"
python3 scripts/generate_api.py --check
npm --prefix web audit --omit=dev
```

The container build also ran the UI typecheck and shared UI demo build. Production npm
audit reported zero vulnerabilities; this is not an exhaustive dependency/security audit.
Backend checks cover public-account authentication, cookie/origin/CSRF boundaries,
read-only actions, pagination and real Core backup-destination validation. The archive
regression passed with rejection before oversized normalized output could be created.
The real-Docker regression passed on the published image.

Repository privacy checks passed. The full `scripts/check_docs.py --worktree` gate
failed in the unrelated `2026-10-01-backup-docs-destination-honesty.json` assessment:
its `guide` impact is updated without a page. That other change was left untouched.
Calling the existing checker on this demo assessment and the current guide structure/
links passed; this does not claim the complete dirty worktree passes the gate.

Independent read-only behavior and security reviews found and closed fixture/source
shape issues, ambiguous smoke selectors, unbounded proxy file logging, Docker image
identity comparison and normalized-archive size amplification. No unresolved must-fix
finding remained in their final source reviews. Runtime evidence above was gathered by
the primary, not attributed to the reviewers. See BUG-1142 and BUG-1143 in the
[bug ledger](../BUG_LEDGER.md).

## Remaining external gates and limits

- Public TLS is configured and validates. The website operator intentionally limits
  the proxy resource to selected IPs; an outside browser received 401 before the
  demo page, so public-browser verification remains unavailable from that origin.
  The restriction was preserved; no application/firewall access was widened.
  The temporary direct verification allowance was removed from both the firewall and
  service allowlist. A direct administration-origin HTTP request timed out; a permitted
  loopback-origin request through the installed private proxy returned 200, its Unix
  healthcheck passed and the existing public website still returned 200.
- Git history remains owner-controlled. Automatic publication starts after the change
  is committed/pushed; the first actual Gitea run and runner isolation are not verified
  by local script execution or the existence of workflow files.
- Fictional inventory, metrics and restore-test results establish no real Core, disk,
  backup, restore, migration, HA or workload recovery correctness.
- No public-internet penetration test or sustained load test was performed. Containers
  share the host kernel; these restrictions are not a guarantee against future defects.
- Logout clears the browser cookie, not copied stateless cookies; restart/expiry
  invalidate them. Volatile proxy diagnostics are deliberately discarded on reboot.

API impact: **existing contract** — restricted reuse of Controller client contracts;
production Controller/Core and peer contracts do not change. Guide impact: **updated**.
State owner: **none** — fictional build inputs and disposable sessions are not durable
product/pool state; deployment selection is hosting infrastructure metadata.

Initial delivery new-file net change: **+3,452 / −0 lines across 33 files**. Modifications to
pre-existing shared files and unrelated dirty work are excluded from this count.

Follow-up scoped net line change: **+364 lines**, excluding unrelated dirty work.
