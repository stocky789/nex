# Browser security verification

Date: 2026-09-08. Development tree after shared web security and Unicode password validation.
Local verification; no hosted CI, release certification or penetration-test claim. API impact
**existing contract** (tests exercise sessions, reads/mutations, errors). Guide impact **updated
evidence** for login, expiry/session-loss, password validation and recovery.

## Environment and executed checks

Production-built UIs against real HTTPS Go services with disposable state, Playwright Chromium.
Controller used the read-only simulated `virsh` probe; local suite intercepted `virsh`, `qemu-img`
and `genisoimage` with fixtures (VM state simulated). Self-signed certificates trusted only in the
test browser; operator certificate verification not exercised.

```sh
NEX_QA_CAPTURES=.cache/security-controller .cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser --evidence .cache/security-controller/results.json
.cache/toolchains/go/bin/go build -o .cache/nex-core-local-security ./cmd/nex-core-local
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --blocked-only --binary .cache/nex-core-local-security --captures .cache/security-local-blocked
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --binary .cache/nex-core-local-security --captures .cache/security-local-full
```

All passed: Controller suite (enrollment, outage, rotation, revocation, re-enrollment, acceptance,
security); both local modes `PASS`, full mode including deployment review, lost-response/reload,
failed start, retry, power confirmation, recovery. Binary and UI hashes:
[Controller](browser-captures/controller-results.json),
[local blocked](browser-captures/local-blocked-results.json),
[local full](browser-captures/local-full-results.json).

## Security behavior actually exercised

Shared [assertions](../../../tests/integration/web_security.py), real responses only:

- Login sets `__Host-nex_session` / `__Host-nex_local_session`: Secure, HttpOnly, SameSite=Strict,
  root path, host-only; unreadable from JavaScript.
- Same token under legacy unprefixed name: 401.
- HTML: `nosniff`, `no-referrer`, `DENY`, `no-store`, restrictive CSP; an inserted inline script
  does not execute; app forms/navigation work.
- Sign-out removes cookie, next request 401; invalid credentials rejected without leaking values.
- Wrong logins return 429 within six attempts despite a different `X-Forwarded-For` each time; run
  last, throttle not reset. Window/concurrency limits are in Go tests.
- Password validation rejects 8 supplementary characters (16 UTF-16 units) and 513 `é` (>1024
  UTF-8 bytes), accepts 16 supplementary characters; invalid stays on configuration, valid reaches
  review.

## Session expiry and recovery methods

- Controller: only this session's `idle_expires` adjusted. GET polling leaves a future deadline
  unchanged; CSRF-valid site creation extends it; past deadline returns UI to login on reload;
  login restores. Accelerated condition, not 30 elapsed minutes.
- Local Core (in-memory sessions): service restarted, old session rejected, login works — proves
  session-loss recovery, **not** idle expiry. 30-minute idle and 8-hour absolute limits in Go checks.
- Restart walkthrough first failed twice with `local service already running` (inherited lock,
  [BUG-SEC-0003](../../BUG_LEDGER.md#bug-sec-0003--child-processes-inherited-ownership-locks));
  both modes pass after `O_CLOEXEC`, no sleep added.

## Inspected visual evidence

Six screenshots, all inspected; git-ignored and not in the repository:

- `browser-captures/controller-invalid-login.png`: clear error, masked password, visible focus.
- `browser-captures/controller-idle-expired-login.png`: clean 360px login after expiry, no privileged view retained.
- `browser-captures/local-session-lost-login.png`: clean local login after restart, focus, empty password.
- `browser-captures/local-review.png`: reaches review under CSP; no password shown.
- `browser-captures/local-overview-360.png`: state, controls, failed-operation recovery readable, no overflow.
- `browser-captures/local-inventory-error-360.png`: honest stale messaging, mutations disabled; retry recovers.

No clipping, focus regression or JavaScript page errors; the deliberate CSP rejection is expected.

## Limits

No real appliance boot, guest storage, 30-minute or 8-hour browser wait, reverse proxy or live-host
testing. Full local mode needs read-only preflight prerequisites; `--blocked-only` covers the
smaller slice. Commands above are the historical run; new full simulations need `--disposable-full`
(host auth in disposable Debian containers) — see [root-login checks](../appliance/root-auth.md).
