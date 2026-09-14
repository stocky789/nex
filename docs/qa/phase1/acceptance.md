# Phase 1 process and browser acceptance

Date: 2026-09-08. Result: **passed** (exit 0, all 14 scenario groups).

## Environment and method

Harness: [phase1.py](../../../tests/integration/phase1.py) with
[browser actions](../../../tests/integration/phase1_browser.py). Builds real Controller and agent
binaries; temporary state, localhost HTTPS with an ephemeral certificate, unique admin password;
cleans up on exit. No services, host networking, disks or guests are touched.

CachyOS Linux 7.2.3-1-cachyos, Go 1.27.1, Python 3.14, Node 24.20.0/npm 12.0.2, Playwright
1.62.0, Chromium 151.0.7922.34. The browser accepts the self-signed localhost certificate (not an
operator setup); agent and API client verify certificate and hostname.

**Simulation:** real CPU/RAM/OS/NIC/disk observations; only `virsh` is a stub (healthy empty
inventory, failure, stall). Accelerated timing: 250ms heartbeats/probes, 150ms probe deadline,
2s stale window — not production defaults.

## Results

| Area | Exercised |
|---|---|
| Process | Admin bootstrap/login; unauthorized, CSRF, invalid input rejection; site/pool creation; verified-TLS enrollment; single-use token rejection for another identity; native inventory; simulated libvirt failure/stall/recovery with responsive heartbeats; stopped-agent stale state; Controller/agent restart with stable identity; durable refresh serialization and request-ID dedup; Controller outage/reconnect; certificate rotation; revocation and re-enrollment into the existing identity; audit attribution |
| Browser | Keyboard login and invalid credentials, site/pool creation, empty pool, token generation and removal on close, host/task views, inventory refresh, exact-host revoke confirmation, desktop and 360px, skip-to-content, interrupted request and Retry, stale/unknown warning, revocation, re-enrollment, sign-out; no page JS errors |
| Races | Delayed token cannot appear in another pool's panel; delayed older host response cannot overwrite a newer revoked snapshot |
| Final run | Aggregate pool health; failed libvirt and timed-out probe with fresh connection, both recovered; virtualization CPU flags shown |
| Failed first run | ServeMux startup panic; 139 CPU flags exceeded a 128-entry limit. Fixed; rerun passed ([review](review.md)) |
| Guide CLI walk (default timing) | Bootstrap, site/pool/token via API, enroll, status, run, stop, rotate, status, run, Controller and agent restart, fresh reporting with unchanged identity; UI/API docs/source routes 200. No libvirt or browser; served assets were an earlier snapshot, so no matching-source claim |

## Evidence

[Process results and binary SHA-256](process-results.json). Captures were inspected by the
verification worker, UI owner and reviewer; 360px has no page overflow, wide tables scroll in their
panel. Captures are local only (`docs/qa/**/*.png` is git-ignored).

| State | Capture |
|---|---|
| Delayed session request | `captures/loading.png` (not committed) |
| Invalid credentials and keyboard focus | `captures/unauthorized.png` (not committed) |
| Empty pool | `captures/empty-pool-desktop.png` (not committed) |
| Real inventory, simulated healthy libvirt | `captures/host-desktop.png` (not committed), `captures/host-360.png` (not committed) |
| Simulated failed libvirt, fresh heartbeat | `captures/libvirt-failed-desktop.png` (not committed) |
| Simulated stalled probe | `captures/probe-timeout-desktop.png` (not committed) |
| Task outcomes and attribution | `captures/tasks-desktop.png` (not committed) |
| Wrong hostname cannot confirm revocation | `captures/revoke-confirmation.png` (not committed) |
| Injected request failure | `captures/network-error-360.png` (not committed) |
| Stopped agent | `captures/stale-host-360.png` (not committed) |
| Revoked credential | `captures/revoked-host-360.png` (not committed) |

## Reproduction

```sh
python3 tests/integration/phase1.py
npm --prefix web ci
npm --prefix web run build
python3 -m pip install playwright==1.62.0
python3 -m playwright install chromium
python3 tests/integration/phase1.py --browser --evidence .cache/phase1-result.json
```

Captures go to `.cache/phase1-captures` (override with `NEX_QA_CAPTURES`). Evidence files exclude
passwords, tokens, cookies and keys.

## Limits

Hosted CI, systemd installation, Debian host compatibility, guest survival and physical
KVM/storage correctness are not established. API impact: existing contract. Guide impact: evidence
for the [Controller guide](../../guide/manage-controller.md).
