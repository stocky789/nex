# Web security hardening checks

Date: 2026-09-08. Scope: Controller and host-local web platforms, agent trust and appliance
delivery. API impact **changed contract** (client session/cookie/lifetime/admission, bootstrap
password; no agent protocol change). Guide impact **updated** (reauthentication, deployment).

Evidence: [browser](browser.md), [native lab](lab.md), [compiler](compiler.md).

## Assessment

Independent baseline auditor, HTTP investigator and architecture reviewer inspected source; the
primary validated the shared-login admission weakness. Plugin scan retained one medium availability
finding on pre-fix source (bounded review; snapshot warning from concurrent unrelated changes,
fixes made after the audit). Reported plugin usage was a cumulative rollout total (5,970,514 tokens,
5,644,160 cached), not a task-only measure.

## Executed checks

| Check | Result |
| --- | --- |
| Login starvation regression | 429 with two incomplete bodies before fix; valid login admitted after ([BUG-SEC-0001](../../BUG_LEDGER.md#bug-sec-0001--unauthenticated-requests-consumed-shared-login-capacity)) |
| Appliance password regression | Old 8-char/16-byte acceptance rejected; Unicode counting passes ([BUG-SEC-0002](../../BUG_LEDGER.md#bug-sec-0002--password-byte-length-did-not-enforce-the-displayed-character-minimum)) |
| `go test -race -count=1 ./...` | Pass (agent, Controller, localcore, vmhost, websecurity) |
| Python discovery, `ResourceWarning` as error | 55 tests pass |
| UI typecheck/build; API generation freshness; 16 contract tests | Pass |
| `npm --prefix web audit --json` | Zero advisories (time-specific) |
| `govulncheck` v1.7.0, Go 1.27.1 source | No vulnerabilities |
| Earlier Go 1.24.4 binaries | 28 (local) / 25 (Controller) stdlib symbol matches; rebuilt Go 1.27.1 binaries scan clean |
| Inherited ownership locks | Subprocess regressions failed before close-on-exec fix, pass after; all three raw lock-open callers reviewed ([BUG-SEC-0003](../../BUG_LEDGER.md#bug-sec-0003--child-processes-inherited-ownership-locks)) |
| Docs structure and scoped range check | Pass for 88 paths in an isolated snapshot (not a future combined PR) |

Independent review checked decode-before-hash admission, peer isolation, ignored forwarding
headers, bounded memory/concurrency, cookies, expiry/caps, legacy DB migration, credential
preservation, headers and Unicode validation; its race suites passed. Requested firstboot password
alignment and downgrade documentation were applied. Delivery review verified hashes, notices,
frozen source, CI prerequisites and simulated/native separation, and inspected the post-reboot
capture; two stale status sentences corrected.

## Limits

- Open: MFA, password screening, credential rotation/recovery, detailed roles, full security audit
  retention, managed certificate renewal, production penetration testing.
- Self-signed development trust is fingerprint-verified; HSTS not enabled. Use private
  management networking/VPN. No compliance or public-Internet readiness claim.
- Security1 separate-host recovery not rerun. OS component assessment is separate.
- Both CI workflows run regressions and advisory checks on push/PR and weekly; no hosted run or
  branch protection verified.
