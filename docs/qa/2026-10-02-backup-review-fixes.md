# Backup review-fix verification — 2026-10-02

## Scope and state ownership

Development version 0.4.19, uncommitted review-fix worktree. Covers the 32 deduplicated
findings in BUG-1801–BUG-1832 and integration corrections BUG-1833–BUG-1838.
No real host, disk, destination, deployment server or published release was changed.

Core retains pool repository definitions, receipts, policies, runs, catalog, pins and
orphan results on the pool master and in its verified authority replicas. Credentials
are delivered and sealed separately on individual Core hosts; pending-detach snapshots
are host-local Core records, not part of that authority stream. Executing hosts retain
managed release journals and exact retained-vTPM manifests. An acknowledged authority
replica alone does not prove possession of credentials or pending-detach records.
A fresh Controller recovers pool inventory and outcomes through authenticated host APIs;
no new Controller-owned pool state is introduced. Repository mutation reservations
and live-worker tracking are process-local exclusion, not a second durable record.

API impact: **changed contract**. Core peer repository cleanup now obtains exact member
snapshots with authenticated, count/byte-bounded `after_id` pages from
`GET /api/v1/pool/backup/retain`; POST retain/forget compare those complete exact
snapshots instead of deleting from a negative held-ID list. Paged orphan results
use a bounded cleanup header and individual point frames. Other operator-facing routes
retain their payloads, authentication and authorization; safer refusal/uncertainty and
exact-retry behavior are corrections to existing contracts. Upgrade Core peers together
and repush the active master's complete authority before considering promotion.

Guide impact: **updated**. Repository retry/forgetting, uncertainty, member eligibility,
retained-state release, catalog concurrency, history loading and deployment timeout
instructions changed with the implementation. No roadmap milestone was closed.

## Environment and method

- Linux amd64; `go version` reported `go1.27.1-X:nodwarf5 linux/amd64`.
- Node.js v24.21.0; Python 3.14.7; existing repository test tools and Playwright environment.
- Playwright Chromium 151.0.7922.34 for fixture browser checks; managed Chromium for direct visual review.
- Controller UI compiled from this worktree. Generic Controller mode was used for
  simulated operator mutations; `node build.mjs --demo` selected the shipped read-only
  entry point for actual demo/API browsing.
- S3 uses a local fake object server; SFTP uses an in-process SSH/SFTP server.
  Native placement/libvirt/systemd/mount observations in Core fixtures are simulated.
- SMB3 filesystem/credential rollback runs in a private root user namespace with
  read-only system binaries and disposable `/var/lib` and `/tmp`. Native commands are
  simulated; no SMB server or real mount is contacted.
- A workstation restart interrupted integration workers and services. Saved changes
  were inspected, interrupted lanes restarted, and their final checks rerun afterward.

## Executed regressions

The original review reproduced nine failing disposable probes before fixes, including
live-worker staging deletion, unsynced SFTP reuse, protection loss, resumed symlink
deletion, oversized orphan framing, false Controller settlement, queued silence timing,
cross-VM release replay and failed member credential retry. Other original findings
were source-traced; no unexecuted before-fix run is claimed for them.

The first combined `go test -race -count=1 -timeout 20m ./...` exposed integration
failures rather than passing: GC fixture interception matched page uploads by substring,
registration fixtures depended on synchronous peer delivery, an uncommitted credential
was reopened through the durable-use health resolver, a returning-member fixture lacked
its repository, a missing-point result did not settle, and handshake cancellation raced
connection reassignment. These were corrected, not hidden by skipped assertions.

Independent review also identified same-repository S3 claim overlap, uncertain SMB
rollback being classified as failed, quadratic unchanged catalog reimport and oversized
aggregate retained-vTPM journals. The new journal regression genuinely failed before
its fix with `Status:succeeded`; after the fix:

```sh
go test -race -count=1 ./internal/vmhost -run '^TestReleaseRetained'
```

Passed. Sixteen valid retained trees cannot cause deletion followed by an unreadable
journal; refusal keeps originals and a readable failure record. Partial deletion,
changed remaining trees, new/current targets and admitted-empty-versus-unbound intents
are covered by the same focused run.

`npm run typecheck`, `npm test` and `npm run build` passed for the shared UI. Regression
checks cover frozen complete registration bodies, pool-bound receipts, surviving
coverage overrides and lazy flat/tag-group row construction.

`python3 -m unittest discover -s tests` passed 569 checks in 53.429 seconds, with one
skip. This includes the archive receiver's finite idle/overall reception, lock release
and no Docker import on refusal, repository privacy including binary scanning, and
the updated peer API implementation/reference coverage scanner.

The three SMB3 replacement regressions passed under isolated namespace root, including
mount, publication, QEMU and final-path preparation failures, confirmed old-access
restoration and uncertain rollback retaining both credential files. This proves the
fixture's real filesystem effects, not SMB authentication or actual systemd rollback.

The post-restart `go test -race -count=1 -timeout 20m ./internal/agent` passed in
240.540 seconds after correcting recovered-health transitions and direct versus
peer-admitted missing-run settlement. Focused Controller settlement and retained-release
checks also passed. The full `./...` run did not pass: an unchanged console pairing
test accepted a second agent tunnel during the first tunnel's pairing window. That
separate console race was outside these 32 findings. It was subsequently corrected
in the [console pairing follow-up](2026-10-02-console-pairing-race.md), which records
its own reproduction, regression and full-suite results.
API generation and the documentation structure/impact gate passed; these do not
establish semantic completeness or remote CI success.

A subsequent verbose Core run measured unchanged catalog reimport: 1,828 filesystem
read calls for 64 points. The bounded selected-record/capacity regressions passed.
That combined run exposed invalid NFS pagination fixtures, reload attempts while the
original Store still held its lock, and an overly broad persistence-error classification;
these were corrected without weakening authority validation or the admission test.
Independent follow-up review caught a null continuation cursor being mistaken for
completion, successful receipt replay after repository removal, and implicit SMB
credential repair lacking prior-registration rollback ownership. All received focused
consumer-visible regression coverage; the final corrected run's outcome is recorded below.

The final corrected command passed:

```sh
go test -race -v -count=1 -timeout 20m ./internal/agent
```

Core reported 285.993 seconds. Its inspected output shows successful registration
receipt replay for all three outcomes, blocked-rollback shutdown, the real
permission-refusal variant without skipping, and implicit unreadable-credential
repair refusing commit without retiring replacement material. Authenticated peer
HTTP checks passed for more than 256 rows, aggregate inventory beyond 4 MiB,
recordless pending detach, concurrent replacement/insertion, malformed and null
cursors, later-page refusal, authentication/owner refusal and encoded-body limits.
These exercise real Store persistence, Run ownership and peer HTTP handlers with
simulated native mount operations; they do not certify a real host or destination.

`python3 scripts/generate_api.py --check` passed and the focused peer/API suite
passed 82 checks after its route inventory adapter learned function-local URL
aliases used by paging. Independent persistence and paging reviewers reported
no remaining concrete findings after the corrections; they ran no checks themselves.

## Browser walkthroughs and visual review

The primary assistant exercised the compiled UI and inspected captures on 2026-10-02.

`tests/integration/backup_workspace_browser.py` exercised 11 scenarios with fictional
API responses. Ten passed in the combined run; delayed-history initially failed its
navigation assertion, which was corrected to open the actual job inspector. The focused
`--check delayed-history` rerun passed. Covered retained metadata, route-preserved drafts,
uncertain jobs, repository focus, phone layout, refusal, independent inventory, polling
focus, scope cancellation, deleted-VM overrides and delayed history completion.

A disposable browser script exercised two additional operator paths:

1. Existing S3 repository replacement, typed ID confirmation, credential handoff,
   lost accepted reply, failed repository observation, then exact retry. Both registration
   POST bodies were identical, including confirmation and credential receipt; the secret
   was not sent again and the accepted form closed.
2. Accepted pool-A credential followed by uncertain registration, then pool-B scope.
   The old draft closed, the new draft was empty, Save required fresh credentials,
   and new request IDs/receipt belonged to pool B.

Both passed. The screenshots `frozen-registration.png`, `pool-bound-receipt.png`,
`delayed-history.png`, `history-scope-cancellation.png`, `repository-deleted-override.png`
and `phone-layout.png` are local evidence, not tracked images.

The actual `nex-demo` executable and built demo UI then passed
`tests/integration/demo_browser.py` without browser response mocks. Fifteen checks
covered invalid/successful login, VM filtering/detail, pool scope, host hardware,
storage/networks, backup job refusal, recovery-point inspection, simulated quorum,
repository settings/tasks/events, themes/phone layout, source download, API reference,
logout, and newly added stacked host/VM CPU rendering. There were no page errors or
failed read requests. CPU legends matched configured vCPU/reported CPU counts and
real canvases were visible. Direct managed-browser review also observed 32 host-core
series and a populated stacked CPU plot, rather than the former empty chart.

The demo smoke initially used generic rather than demo build mode, then a stale host
row selector. Build mode and route-readiness/accessibility selectors were corrected;
no production behavior was changed to satisfy those failures. Captures include
`vm-stacked-cpu.png`, `host-stacked-cpu.png`, `overview-light.png`, `overview-dark.png`
and `backups-phone.png` under the local demo evidence directory.

## Second review wave (follow-up fixes)

A second review of the branch against `origin/dev` produced a follow-up fix set recorded in
[the review-findings change record](../changes/2026-10-02-review-findings-fixes.json) and
BUG-1840 to BUG-1866 in the [bug ledger](../BUG_LEDGER.md). Each fixer ran focused Go,
web and Python tests for its own area (named per entry in the ledger) while sibling fixes were
still landing; several of those runs hit failures in files owned by another fixer that were
later corrected. This report claims no whole-suite result for the follow-up: the project-wide
validation is a separate step. New regressions were mostly added with their fix and were
**not** run against the old code, except where a ledger entry says otherwise (for example
`TestActivationVerifiesWholeRootBeforeActingOnPages`, checked by mutation). The demo deploy
wrapper was exercised with a fake Docker, not with real Docker and a real compression bomb, and
the Playwright browser tests were compiled but not run for these fixes. The two commits that
added and removed a private-path test executable remain in branch history; that history was not
touched, and merging the branch as a single squash commit avoids publishing the executable.

## Limits

- Fixture APIs and fictional demo resources do not prove real backups, guest recovery,
  S3/SFTP server crash durability, SMB replacement, fencing or hardware promotion.
- Pre-fix partial vTPM deletion lacking an admitted per-file manifest remains fail-closed;
  the remaining tree cannot safely reconstruct the original deletion proof.
- Removing the private-path executable from current files does not remove Git history,
  releases or cached artifacts. No history or remote operation was performed.
- QA raster captures are untracked by repository policy and cannot be reinspected from
  a fresh checkout. This written report and regression sources are the reviewable record.
- CI configuration was inspected/updated, not executed on a remote forge. No CI success,
  publication or phase acceptance is claimed.
