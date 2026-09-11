# Public URL ISO verification

Date: 2026-09-11.
Scope: development source changes; installed packages and installation media are unchanged.

## Behavior and ownership

The ISO form offers a local upload or a public URL, both with an explicit validated
host storage destination. URL imports are queued by Controller and executed by Core,
using the existing durable `iso-import` operation. Only complete, verified ISO images
become usable. Host inventory supplies measured size and SHA-256; the pool master's
managed journal and media storage retain the identity, name and content a fresh
Controller adopts. This does not implement replication to every host.

API impact: **new contract** — `POST /api/v1/media/download`, optional `Media.url`
on `iso-import`, and `iso-download-v1` admission/dispatch gating. Existing administrator
sessions, same-origin CSRF protection, request replay, results and inventory adoption
remain in use. The [API reference](../../API.md) documents public URL validation,
connection-time DNS/address checks, redirect and time limits, free-space bounds,
publication/recovery, and compatibility. Guide impact: **updated** in the
[VM creation guide](../../guide/create-virtual-machine.md).

## Browser evidence

The [browser check](../../../tests/integration/iso_url_browser.py) ran against the
actual built React UI in headless Chromium with **fully mocked API responses** and
fictional resources. It started its own disposable local HTTP server. No real host
or public download was exercised by this browser check.

`npm --prefix web run typecheck` and `npm --prefix web run build` passed. The browser
check passed native invalid URL/name rejection, disabled submission controls while
waiting, keyboard submission, uncertain-response retention across reload and replay
with the original request identity, queued size/hash placeholders, old Core/stale
host/empty storage blocking, narrow layout, and the preserved local upload request.
The [recorded results](browser-results.json) contain no page errors.

The [error form](mock-error.png), [queued download](mock-queued.png), and
[narrow empty-storage form](mock-empty-mobile.png) were visually inspected. Labels,
controls and feedback were readable; captures contain only fictional demo inventory.
The PNG metadata was inspected and contains only image chunks, with no private
workstation or deployment data. The browser check is wired into both GitHub and
Gitea phase1 workflows; no hosted CI run is claimed.

## Backend and review

`go test ./...` passed across all Go packages using the available local Go
1.26.5 toolchain on Linux. The focused Controller check exercised real handler,
SQLite and host-report code with an in-process test host: missing authentication,
CSRF refusal, invalid URL/name/storage, capability admission and dispatch refusal,
unchanged replay identity, changed-payload conflict, absence of Controller staging,
completed metadata, wrong-host isolation and simulated Controller mirror loss followed
by adoption with the original identity and no redispatch.

Core tests use a real local HTTP test server and synthetic ISO bytes, replacing only
the socket destination for accepted-download cases. Public DNS/address policy is
checked separately through the production dialer and protocol validation. This is
not a real public Internet transfer. Tests exercise bounded declared/chunked streams,
invalid ISO content, rejected HTTP status, private/credential redirects and redirect
loops, metadata persistence failure, atomic publication, original command replay,
computed metadata after reopening the journal, and recovery before/after publication.

`python3 scripts/generate_api.py --check` passed for both generated references;
`python3 -m unittest discover -s tests -p 'test_api_contract.py'` passed all 21 checks.
The required repository privacy check passed, and
`python3 scripts/check_docs.py --worktree` passed structural/link/impact coverage.
`git diff --check` passed. These checks do not establish hosted CI results.

After the final recovery changes, `go test -v ./internal/vmhost -run
'TestURLDownload|TestPublicISO'` passed every listed test and subtest without skips,
and `go test ./internal/agent` passed. The Core owner also ran
`go test ./internal/protocol ./internal/vmhost ./internal/agent` successfully on that
snapshot. Cancellation cleanup, successful public redirects, HTTPS downgrade refusal
and source Referer stripping passed. Recovery tests assert partial-file removal during
inventory adoption **before** replay, both before and after metadata persistence; a
completed publication is adopted after a journal reopen without another download.

Independent read-only review covered implementation, contracts, guide applicability,
state ownership, tests and all three browser captures. It found and verified corrections
for the ISO filename pattern and restart/adoption cleanup. No unresolved findings remain.
This accepts the bounded source feature with the simulation and delivery limits below;
it does not close the full VM phase gate.

## Limits

Browser APIs and test ISO content are simulated. No real public distribution ISO,
physical host, installation, or packaged Controller/Core upgrade was exercised in
this slice. Host download byte-progress, cancellation, resumption of partial files,
and distribution to multiple hosts are not implemented. Remaining VM phase acceptance
and release checks stay open in the [platform plan](../../plans/2026-09-08-nex-platform.md).
