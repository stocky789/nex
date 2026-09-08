# Web security hardening checks

Date: 2026-09-08. Scope: implemented Controller and host-local web platforms, their
agent trust and appliance delivery boundaries. Unrelated installer changes are preserved.

## Assessment

One independent baseline auditor, one HTTP investigator and one architecture reviewer
inspected current source; the primary validated the reported shared-login admission
weakness. The completed plugin scan retained one medium availability finding against
the pre-fix source. Its report is a bounded review, not complete repository or standards
certification. Concurrent unrelated working-tree changes caused a snapshot warning;
source fixes were made only after that audit was recorded.

The plugin advisory connector was disconnected. Its report's measured usage is a
cumulative rollout total (5,970,514 tokens, including 5,644,160 cached input tokens across
four threads), not an isolated measure of this security task. No separate task-only token
measurement is available.

## Executed checks

- Login starvation regression reproduced HTTP 429 before the fix with two incomplete
  request bodies; the fixed test allows an independent valid login.
- The appliance password regression rejected the old eight-character/sixteen-byte
  acceptance bug before the correction and passes with Unicode character counting.
- Full `go test -race -count=1 ./...` passed after runtime fixes: agent, Controller,
  localcore, vmhost and websecurity tests executed; command/protocol packages compiled.
- Python discovery with `ResourceWarning` treated as an error passed 55 tests in the
  final combined run, including CI artifact prerequisites and appliance caller alignment.
- UI typecheck and production build passed. API generation freshness and all 16 contract
  tests passed, including cookie/lifetime reference coverage.
- `npm --prefix web audit --json` returned zero reported advisories for the locked graph.
  `govulncheck` v1.7.0 against current source with Go 1.27.1 returned no vulnerabilities.
  These results are time-specific and do not establish whole-image safety.
- Binary inspection found the earlier package used Go 1.24.4: the scanner reported
  28 vulnerable stdlib symbols for local service and 25 for Controller. These are
  advisory matches, not proof each route can exploit them. The compiler was
  upgraded; both actual rebuilt binaries report Go 1.27.1 and scan with zero findings.
  See [compiler evidence](compiler.md) and [package evidence](package.md).

## Review and remaining verification

An independent reviewer checked decoding before hashing admission, peer limit isolation,
ignored forwarding headers, bounded memory/concurrency, secure cookies, expiry/caps,
legacy database migration, preserved existing credentials, safe headers and Unicode
validation. Its independent race suites passed; no remaining runtime blocker was found.
The reviewer requested firstboot password alignment and explicit downgrade documentation;
owners applied those corrections.

[Actual browser checks](browser.md), rebuilt binary scans and clean Debian package
installation/removal smoke passed. Browser verification exposed inherited ownership locks;
real subprocess regressions failed before atomic close-on-exec fixes and pass afterward,
as do actual service-restart browser checks. All three raw lock-open callers were reviewed.
The primary inspected the expired Controller login and 360px local recovery captures.
[Native security1 deployment](lab.md) passed against the actual packaged binaries:
Unicode bootstrap/login, verified readiness, cookie/CSP/throttling assertions, outages,
service restart, guest cold start and actual host reboot/autostart retained identity.
All disposable VMs were shut down. Security1 separate-host recovery was not rerun.
Independent delivery review verified artifact hashes, retained compiler notices, frozen
runtime source, CI prerequisites and the separation of simulated and native evidence.
Documentation structure and scoped Git-range verification passed for 88 changed paths
(87 declared files plus the assessment) in an isolated snapshot containing unrelated
workspace prerequisites. This verifies the declared security slice, not a future combined
PR or hosted run. The shared working tree was not committed. Independent delivery review
also inspected the post-reboot Controller capture and native result/cleanup records;
its two stale status sentences were corrected before the final gate.

## Operational limits

API impact: **changed contract** for existing client session/cookie/lifetime/admission
and bootstrap password behavior; no agent command protocol change.
Guide impact: **updated** with reauthentication and security deployment guidance.

MFA, common/compromised-password screening, credential rotation/recovery, detailed roles, complete authentication/security audit
retention, managed certificate renewal and production penetration testing remain open.
Self-signed development trust is fingerprint-verified; HSTS was not enabled blindly.
Use private management networking/VPN. No broad compliance or public-Internet readiness
claim is made. OS component assessment is separate from application dependency checks.
Both CI provider workflows run regressions and advisory checks on push/PR and weekly;
no hosted run or branch-protection enforcement was verified in this task.
