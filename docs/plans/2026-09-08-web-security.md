# Controller and host web security hardening

Date: 2026-09-08
Status: Scoped hardening accepted with independent review and local verification; production requirements below remain open.

## Scope and standard

Matt requested modern security practices for both web platforms. Audit the implemented
Controller and host-local APIs, browser applications, agent trust, appliance and delivery
boundaries, fix source-backed weaknesses, and retain regression/advisory checks.
Unrelated Core installer work remains outside this slice.

Use [OWASP ASVS 5.0](https://owasp.org/www-project-application-security-verification-standard/)
as the control reference and current
[NIST authentication guidance](https://pages.nist.gov/800-63-4/sp800-63b.html)
for authentication assessment. These are assessment references, not a claim that every
requirement, assurance level, regulation or cryptographic certification is satisfied.
The default deployment assumption is a private management network or VPN; direct Internet
exposure has not been authorized or established as supported.

## Concrete changes

- Fix unauthenticated login starvation: complete bounded request validation before
  reserving password-hashing work, isolate throttling by the actual network peer,
  bound tracking memory, and retain a global expensive-work limit.
- Use exclusively host-prefixed Secure/HttpOnly/Strict cookies, server-side idle and
  absolute expiry, and bounded/pruned sessions. Background reads must not keep a session
  alive indefinitely; changed cookie names require signing in again after upgrade.
- Require at least 16 Unicode characters and at most 1024 UTF-8 bytes for newly provisioned
  administrator passwords, including appliance deployment; retain existing password hashes.
- Align CSP, framing, referrer, MIME, cache and unused browser-feature restrictions.
  HSTS requires deliberate trusted-certificate provisioning/recovery; do not apply an
  untested blanket policy to the current fingerprint-verified self-signed workflow.
- Check actual build toolchains and artifacts as well as source dependencies. Add pinned
  advisory tooling to both CI providers and scheduled rechecks; do not claim hosted execution.
- Rebuild and verify the development delivery artifacts if their runtime changes.

## API and guide impact

API impact: **changed contract** — existing Controller and local session/login APIs
change cookie names, admission limits and expiry behavior; bootstrap password validation
changes. Update both client-facing references and all affected callers/tests. Existing
agent enrollment/report, VM power and reconciliation command contracts remain unchanged.

Guide impact: **updated** — document reauthentication, limits, safer deployment assumptions,
updated package availability and explicit production security gaps. Record actual browser,
contract, dependency and disposable-package checks beside the implementation.

## Acceptance and limits

- [x] Reproduce the login admission failure and retain a failing-before/passing-after check.
- [x] Verify authorization, peer separation, expiry, existing database migration and headers.
- [x] Exercise actual browser login/logout/error states and bootstrap handoff with new cookies.
- [x] Run relevant Go race, Python, UI, API and dependency checks; inspect actual results.
- [x] Build and verify matching development artifacts; distinguish old tested builds.
- [x] Update guide/API, bug ledger and scoped assessment; independent review and docs gate pass.

Evidence: [executed checks](../qa/security/checks.md), including browser, actual package
and native reboot reports. No hosted CI or full security certification is claimed.

MFA, common/compromised-password screening, administrator credential rotation/recovery, finer-grained roles, complete security-event
retention, managed certificate renewal, production penetration testing and operating-system
patch/release processes remain explicit production requirements. This hardening slice does
not certify the platforms as safe for unrestricted Internet exposure or close full Phase 8.
