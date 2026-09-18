# Storage dialog and release workflow investigation

Date: 2026-09-18. Development checkout: 0.3.3, based on `368ec02`.

## Hosted evidence

The latest hosted foundation run passed Go, contracts, web and website lanes.
The repository-storage browser failed at the second **Browse exports** click:
an open, empty storage dialog intercepted pointer events for the full 30-second
action deadline. Later browser scenarios were skipped. Runner container cleanup
also timed out after this test failure; it was not the initial failure.

The release run built the ISO and packages, verified their versions and passed
packaged Controller startup. Publication then failed after three TLS EOF errors
uploading the ISO, each attempt taking approximately 54 seconds. Website delivery
was skipped. Raw hosted logs remain outside the tracked tree; this is a sanitized
summary, not an untouched capture.

## Browser fix and local verification

Native Escape closes a dialog before its queued close event clears React state.
A discovery reply between those events could reopen the modal; the subsequent
close event removed its content, leaving an invisible input blocker. The shared
NFS/SMB picker now aborts discovery synchronously on cancel and ignores old close
notifications delivered after a new opening.

The regression defers a discovery response until Escape and delays the close
notification. It failed against the original built UI with an empty open dialog.
A second scenario closes and reopens the picker before delivering the old close
event; temporarily removing the new close guard failed that scenario too.
Both mutations were restored. These are deterministic event-order simulations,
not measurements of the original runner's scheduling.

The complete repository browser walkthrough passed against the built UI and mocked
API, recording 12 checks with no page errors. It exercises loading, cancellation,
keyboard navigation, NFS/SMB selection, empty/error replies, stale-host exclusion,
permissions and destructive-action confirmation. The reopened-picker screenshot
was visually inspected: folder results and controls remained visible, with focus
on Cancel. Local captures and result JSON remain untracked.

Commands: `npm --prefix web run typecheck`, `npm --prefix web run test`,
`npm --prefix web run build`, and
`.cache/phase1-qa-venv/bin/python tests/integration/storage_repositories_browser.py --evidence .cache/storage-browser-escape-fixed`.
Both existing providers' push/PR foundation workflows execute this browser script.
No native storage operations or new hosted success are claimed.

## Release investigation and remaining work

The publisher streams an octet-stream body with Content-Length, supported by the
installed Gitea version. All six `test_gitea_release.py` checks passed locally,
including dropped-connection retry, truncated upload rejection and draft cleanup.
These use a simulated forge and cannot prove large hosted uploads.

The ISO is approximately 3350 MiB. Gitea's documented release attachment default,
`[repository.release] FILE_MAX_SIZE`, is 2048 MB. The actual configured release limit
has not been inspected. The API's issue-attachment limit is a different setting.
Upstream Gitea limits streamed release bodies while reading them, so a mid-upload
size rejection is one possible cause; TLS EOF alone does not establish that cause
or distinguish it from a proxy timeout or connection failure.

Inspect actual Gitea and proxy limits/logs and confirm the runner's upload route
before another release attempt. No live infrastructure, release or tag was changed.
Hosted upload recovery and a hosted CI rerun remain outstanding.

Sources: [Gitea release settings](https://docs.gitea.com/administration/config-cheat-sheet/#repository---release-repositoryrelease),
[versioned upload handler](https://github.com/go-gitea/gitea/blob/v1.26.2/routers/api/v1/repo/release_attachment.go),
[versioned attachment service](https://github.com/go-gitea/gitea/blob/v1.26.2/services/attachment/attachment.go).

## API and state assessment

API impact: existing contract. The picker uses `POST /api/v1/storage/discover`;
Controller validates the administrator request, dispatches transient discovery to
Core through reports and returns the matching host/pool result. Only local dialog
cancellation changes; request, authorization, Core execution, error and result
contracts remain unchanged. No API reference change is required. Discovery remains
read-only; cancellation does not promise to stop already-dispatched host work.
State owner: none. No durable product data is introduced or changed.
Guide impact: updated picker cancellation and release upload troubleshooting.
No phase acceptance gate changes.
