# Browser security verification

Date: 2026-09-08. Development working tree, after shared web security and local
Unicode password-validation changes. This is executed local verification, not a
claim of hosted CI success, release certification or a comprehensive penetration test.

## Environment and executed checks

Both UIs ran their actual production-built JavaScript against real HTTPS Go service
processes and private disposable state, through Playwright Chromium. The Controller
suite used the existing read-only simulated `virsh` probe. The local suite intercepted
all `virsh`, `qemu-img` and `genisoimage` calls with explicit disposable fixtures;
local VM state changes are simulated. No real guest or host network was changed.
Disposable self-signed certificates were trusted or bypassed only in the test browser;
operator certificate verification was not exercised.

Commands completed successfully:

```sh
NEX_QA_CAPTURES=.cache/security-controller .cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser --evidence .cache/security-controller/results.json
.cache/toolchains/go/bin/go build -o .cache/nex-core-local-security ./cmd/nex-core-local
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --blocked-only --binary .cache/nex-core-local-security --captures .cache/security-local-blocked
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --binary .cache/nex-core-local-security --captures .cache/security-local-full
```

The Controller suite completed all existing enrollment, outage, rotation, revocation,
re-enrollment and browser acceptance checks plus the new security checks. Both local
modes printed `PASS`; full mode also completed its existing deployment review,
lost-response/reload, failed simulated start, retry, power confirmation and recovery
checks. Exact binary hashes and local UI hashes are retained in
[Controller results](browser-captures/controller-results.json),
[local blocked-mode results](browser-captures/local-blocked-results.json), and
[local full-mode results](browser-captures/local-full-results.json).

## Security behavior actually exercised

The shared [browser security assertions](../../../tests/integration/web_security.py)
run from both suites against real responses, without fabricated authentication or
policy responses:

- Successful login sets the expected `__Host-nex_session` or
  `__Host-nex_local_session` cookie. Chromium records Secure, HttpOnly,
  SameSite=Strict, root path and a host-only domain. JavaScript cannot read the cookie.
- An isolated real browser context with the same token under the legacy unprefixed
  cookie name receives HTTP 401 from the protected API.
- HTML responses contain `nosniff`, `no-referrer`, `DENY`, `no-store` and restrictive
  CSP directives for defaults, objects, forms, base URLs and frames. A script element
  containing an inline assignment is actually inserted; CSP prevents its execution.
  Existing application forms and navigation remain functional under that policy.
- Sign-out removes the browser cookie and a subsequent protected request receives
  HTTP 401. Invalid credentials remain rejected without leaking password values.
- Repeated wrong logins eventually return HTTP 429 within six attempts while each
  request spoofs a different `X-Forwarded-For` value. Changing that header does not
  bypass the actual-peer throttle. These requests run last and do not reset or disable
  the throttle to make another step pass. Precise window expiry/concurrency limits
  remain the focused Go tests' responsibility.
- Local appliance password validation rejects eight supplementary Unicode characters
  even though they occupy sixteen JavaScript UTF-16 code units. It also rejects
  513 `é` characters, which exceed 1024 UTF-8 bytes, and accepts sixteen supplementary
  characters. The native form remains on configuration when invalid and reaches
  review with a valid password; server checks remain authoritative.

## Session expiry and recovery methods

For Controller, only the current disposable browser session's `idle_expires` database
column was adjusted. A future deadline remains unchanged after GET polling; creating
a site through an authenticated CSRF-valid mutation extends that deadline. Moving
that one deadline into the past makes the actual UI return to login on reload, and
valid login restores access. The host clock and other sessions were untouched.
This accelerates the expiry condition; it does not claim thirty minutes elapsed.

Local Core sessions live only in process memory, so the browser suite terminates and
restarts its actual disposable service, then verifies the old session is rejected and
login works again. **This proves session-loss recovery, not local idle-time expiry.**
The local thirty-minute idle and eight-hour absolute lifetime boundaries are covered
by focused Go checks; no test-only runtime clock or expiry endpoint was added.

The new process-restart walkthrough initially failed twice with `local service already
running`. Independent diagnosis reproduced a subprocess retaining the service lock:
the raw descriptor lacked close-on-exec. After the owner's `O_CLOEXEC` correction,
both browser modes restarted and authenticated successfully. The browser assertion
remains in the integration suite; a focused subprocess regression also covers the
root cause. No sleep was added to hide the inherited-lock failure.

## Inspected visual evidence

- [Controller invalid login](browser-captures/controller-invalid-login.png): clear error, masked password and visible focus.
- [Controller expired session](browser-captures/controller-idle-expired-login.png): clean 360px login after expiry, no old privileged view or secrets retained on screen.
- [Local session lost](browser-captures/local-session-lost-login.png): clean local login after process restart with keyboard focus and empty password.
- [Local review](browser-captures/local-review.png): form still reaches review under CSP after valid Unicode checks; no administrator password appears in the review.
- [Local 360px overview](browser-captures/local-overview-360.png): observed state, controls and failed-operation recovery remain readable without horizontal page overflow.
- [Local stale/error state](browser-captures/local-inventory-error-360.png): actual fixture-process inventory failure produces honest stale messaging and disabled mutation controls; retry recovers.

All six retained screenshots were visually inspected. No material clipping or focus
regression was observed. Both suites reported no JavaScript page errors. The deliberate
CSP inline-script test creates an expected browser console policy rejection; that is
not an application execution failure.

## Impact and remaining limits

API impact: **existing contract** for these test/evidence changes — the suites exercise
session cookies, authenticated reads/mutations and error responses; runtime security
contract changes are documented by their owning implementation slice.
Guide impact: **updated evidence** — this records the actual login, expiry/session-loss,
password-validation and recovery walkthroughs supporting the revised operator guidance.

There is no claim of real appliance boot, guest storage integrity, actual thirty-minute
browser waiting, an eight-hour browser session, reverse-proxy deployment or live-host
security testing. Full local mode requires existing read-only preflight prerequisites;
portable `--blocked-only` deliberately covers the smaller authentication/blocker slice.

Current root-login harness note: the historical commands above describe their recorded
run. New full simulations require `--disposable-full`; the harness now runs host
authentication in disposable Debian containers. See [root-login checks](../appliance/root-auth.md) for current evidence.
