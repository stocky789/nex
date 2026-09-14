# Public URL ISO verification

Date: 2026-09-11. Development source changes; installed packages and media unchanged.
API impact: new contract (`POST /api/v1/media/download`, `Media.url` on `iso-import`, `iso-download-v1`
gating); details in the [API reference](../../API.md).

## Browser evidence

The [browser check](../../../tests/integration/iso_url_browser.py) ran the built UI in headless Chromium with **fully
mocked API responses** and fictional resources. No real host or download.

- Web typecheck/build passed.
- Passed: invalid URL/name rejection, controls disabled while waiting, keyboard submit, uncertain response
  kept across reload and replayed with the same identity, queued size/hash placeholders, old-Core/stale/empty-storage blocking,
  narrow layout, local upload preserved. [Results](browser-results.json): no page errors.
- Inspected (local only, gitignored): `mock-error.png`, `mock-queued.png`, `mock-empty-mobile.png`. Readable;
  metadata holds image chunks only.
- Wired into both phase1 workflows; no hosted CI.

## Backend and review

- `go test ./...` passed (Go 1.26.5). Controller: real handler/SQLite/report code covering missing auth, CSRF,
  invalid URL/name/storage, capability admission/dispatch refusal, replay, changed-payload conflict, no
  Controller staging, metadata, wrong-host isolation, and mirror loss → adoption without redispatch.
- Core: a local HTTP test server with synthetic ISO bytes; DNS/address policy tested separately through the production
  dialer. Covered: bounded streams, invalid content, bad status, private/credential redirects and loops,
  metadata persistence failure, atomic publication, replay, journal reopen, recovery before/after publication.
- Final run: `go test -v ./internal/vmhost -run 'TestURLDownload|TestPublicISO'` passed with no skips.
  `go test ./internal/agent` passed, and the Core owner passed `protocol`/`vmhost`/`agent`. Cancellation cleanup, public redirects,
  HTTPS downgrade refusal and Referer stripping passed. Partial files are removed during adoption before replay.
- `generate_api.py --check`, 21 API contract tests, privacy test, `check_docs.py --worktree`, `git diff --check`: passed.
- Independent review (implementation, contracts, guide, ownership, tests, three captures) found issues in the ISO filename pattern
  and restart cleanup, since fixed. No unresolved findings.

## Limits

Browser APIs and ISO content were simulated. No real public ISO, physical host, installation or packaged upgrade. Byte progress,
cancellation, partial resume and multi-host distribution are not implemented. VM phase acceptance remains open in the
[platform plan](../../plans/2026-09-08-nex-platform.md).
