# Phase 1 process and browser acceptance

Date: 2026-09-08. Status: local process and browser acceptance passed.

## Environment and method

The independent acceptance harness is [phase1.py](../../../tests/integration/phase1.py),
with [browser actions](../../../tests/integration/phase1_browser.py). It builds actual
Go Controller and agent binaries and uses newly created temporary directories,
a localhost HTTPS listener, an ephemeral server certificate, and a unique lab
administrator password. It removes processes and private state on exit. It never
installs services, changes host networking, operates on disks, or starts/stops guests.

Local environment: CachyOS Linux 7.2.3-1-cachyos, Go 1.27.1, Python 3.14,
Node 24.20.0/npm 12.0.2, Python Playwright 1.62.0 and Chromium 151.0.7922.34.
Browser installation and headless browser launch succeeded. The browser context
accepts its ephemeral self-signed localhost certificate; the actual agent and
Python API client verify the configured certificate and hostname. This browser
exception is not a documented operator setup or evidence for browser CA provisioning.

The agent reads this machine's real CPU, RAM, OS, NIC and disk observations.
Only `virsh` is replaced by a temporary executable, explicitly simulating a healthy
empty libvirt inventory, failure, and a stalled command. No real guest, libvirt,
Debian hardware, storage recovery, or installer integration claim follows from it.
The test uses 250ms heartbeats/probes, a 150ms probe deadline and a 2s stale window
to exercise transitions; these are accelerated test settings, not measured
production defaults or failover promises.

## Executed results

The complete process and browser command exited **0**, with all 14 scenario groups
passing. The real flows exercised were administrator bootstrap/login; unauthorized,
CSRF and invalid-input rejection; site/pool creation; verified-TLS enrollment;
single-use token rejection for another identity; native inventory; simulated libvirt
failure/stall/recovery with responsive heartbeats; genuine stopped-agent stale state;
controller/agent restart with stable identity; durable refresh serialization and
request-ID deduplication; controller outage/reconnect; certificate rotation;
revocation and explicit re-enrollment into the existing host identity; audit attribution.

Browser interaction exercised keyboard login and invalid credentials, site/pool
creation, empty pool, enrollment token generation and removal on close, host and task
views, inventory refresh, exact-host revoke confirmation, desktop and 360px layouts,
skip-to-content focus, interrupted request and Retry, stale/unknown warning,
revocation, re-enrollment entry and sign-out. No page JavaScript errors occurred.
Delayed token responses cannot appear in a different pool's new enrollment panel;
a delayed older host response cannot overwrite a newer revoked snapshot. Both
regressions ran in the real browser against the disposable Controller.
The final run also checked the pool's aggregate healthy status, showed a failed
libvirt check and a timed-out probe while the connection remained fresh, and
recovered both. Final host captures show virtualization-specific CPU flags.

The harness initially reproduced a Controller ServeMux startup panic and rejection
of real reports because 139 CPU flags exceeded a 128-entry limit. Owners corrected
both and the complete rerun passed. The focused owner tests cover those regressions;
the process harness also fails if startup or real native reporting breaks again.

The separate guide writer independently ran the guide's actual CLI sequence against
freshly built binaries and native read-only probes with **default** timing: bootstrap,
site/pool/token via API, enroll, status, run, stop, rotate, status, run, controller
restart, agent restart and renewed fresh reporting with unchanged identity. This
run also checked UI, API documentation and source routes returned HTTP 200. It did
not use a real libvirt installation or browser, and its served build assets/source
were from an earlier snapshot, so it makes no matching-source assertion.

## Inspected artifacts

[Process results and binary SHA-256 digests](process-results.json) identify the
actual passing build. Screenshots were inspected directly by the verification
worker; the UI owner also inspected desktop/mobile host, empty, login failure,
network failure and revoked views, and the independent reviewer inspected both
host layouts. The content and controls are visible without page overflow at 360px;
wide activity tables scroll within their panel.

| State | Capture |
|---|---|
| Delayed session request | [Loading](captures/loading.png) |
| Invalid credentials and keyboard focus | [Unauthorized](captures/unauthorized.png) |
| Genuine newly created empty pool | [Empty pool](captures/empty-pool-desktop.png) |
| Real inventory, simulated healthy libvirt | [Desktop host](captures/host-desktop.png), [360px host](captures/host-360.png) |
| Simulated failed libvirt, fresh heartbeat | [Failed service](captures/libvirt-failed-desktop.png) |
| Simulated stalled probe, other reports responsive | [Probe timeout](captures/probe-timeout-desktop.png) |
| Durable task outcomes and attribution | [Tasks](captures/tasks-desktop.png) |
| Wrong hostname cannot confirm revocation | [Confirmation](captures/revoke-confirmation.png) |
| Injected browser request failure | [Error and Retry](captures/network-error-360.png) |
| Actually stopped agent | [Stale connection and checks](captures/stale-host-360.png) |
| Revoked credential, re-enroll action | [Revoked host](captures/revoked-host-360.png) |

## Reproduction

```sh
python3 tests/integration/phase1.py
npm --prefix web ci
npm --prefix web run build
python3 -m pip install playwright==1.62.0
python3 -m playwright install chromium
python3 tests/integration/phase1.py --browser --evidence .cache/phase1-result.json
```

The browser run writes captures under `.cache/phase1-captures` by default.
`NEX_QA_CAPTURES` selects another output directory. The harness never records
passwords, enrollment tokens, cookies or private key material in evidence files.
Screenshots must be inspected before being accepted as visual evidence.

## Limits and impact

API impact: **existing contract** — this harness exercises the new Phase 1 admin,
enrollment, agent-report, rotation, revocation and durable operation contracts;
it adds no API behavior. Guide impact: **updated** — evidence is required for
the [Controller guide](../../guide/manage-controller.md); the verified CLI/browser scope above supports its development walkthrough.
See the separate [independent review](review.md) for review results.

Hosted CI, real systemd service installation, Debian host compatibility, guest
survival, and physical KVM/storage correctness are not established by this run.
