# Backup workspace UI review

Date: 2026-10-01. Scope: Controller-only presentation of existing backup workflows.
The primary implementation assistant inspected the rendered captures and exercised
all scenarios below against the built React UI. No Core operation or real host
mutation was performed, and no recovery phase is closed by this report.

## Environment and method

- Frontend: `nex-controller-ui@0.1.0`, built from this worktree with `npm --prefix web run build`.
- Interactive browser: Chrome `150.0.7871.24`; regression browser: Chromium
  `151.0.7922.34` with Playwright `1.62.0` in a disposable Python environment.
- Linux, loopback static server, fictional pool/host/VM/repository inventory.
  Reads and submissions receive simulated Core API replies; the shipped React
  components, routing, form controls and styles are real.
- Interactive checks used browser clicks, selects, fills, keyboard navigation,
  viewport/theme changes, capture inspection, and console/network inspection.
  Delayed reads and HTTP 403/409/503 replies were intentionally injected.
- [Structured results](results.json) records the scenarios, regression outcomes,
  normal-session health and SHA-256 digests of the inspected `app.js` and `app.css`.
- The comprehensive workflow walkthrough preceded the final review corrections.
  The final bundle received refreshed responsive captures, a targeted unavailable-
  repository walkthrough, and the complete eight-case browser regression run.
  Normal-session health below was recorded during the comprehensive walkthrough.

## Observed workflows

| Workflow | Observed result |
|---|---|
| Workspace navigation | Separate Backup jobs and Recovery points views; keyboard ArrowRight/Home moves the view and focus; the Paused filter survives switching views. |
| Inspect a recovery point | Selecting a row opens details and reads its test history without a write. Point rows have one detail opener rather than repeated action clusters. |
| Back up now | Destination then Review and submit, with fixed footer navigation. A simulated 503 freezes Back/Cancel/Close; retry sends the same VM, repository, mode and request identity before a queued reply closes the panel. |
| Restore as new | Default choice is a separate stopped VM. Review names its new identity and destination. A simulated 409 freezes choices and keeps the exact body for retry. |
| Replace original VM | Wrong exact name and stale VM state block review. A freshly reported shut-off source enables review; submission names the original VM and explicit confirmation. Review correctly states that current NICs/MACs are retained. |
| Missing original VM | Replace is disabled with a reason; Restore as new remains available from the retained catalog point. |
| Test restore | The test choice submits a separate VM identity. Record failed targets the exact test VM, point and repository. Wrong cleanup ID is blocked; the exact ID submits cleanup only for that test VM. |
| Create a job | Coverage, Timing and retention, Review; empty VM coverage and a weekly schedule with no days cannot advance. Review lists selected VM names and the UTC schedule. An uncertain save freezes navigation and retains its body through a scoped route change and exact retry. |
| Expanded job controls | Repository change, Pause and per-VM Run now remain reachable. View points opens the VM-scoped recovery view. Repository change retries retain the original policy revision and body; an absent task does not discard the unresolved intent. |
| Loading and failed reads | Delayed jobs display Loading, not an empty list. Unavailable run history displays Unknown and prevents cleanup; unavailable catalog reports an error rather than zero points. Clearing the injected failure restores the lists. |
| Empty state and prerequisites | Empty jobs offer creation; missing repositories link to Settings and block progression. No repositories is distinguished from unavailable inventory. |
| Phone interaction | Filters disclosure applies the existing VM filter and collapses. Restore body scrolls while its footer stays fixed; footer controls are at least 36 px tall and review remains reachable. |
| Reduced motion | Views, choice controls and the panel have `transition-property: none` with reduced motion. |
| Pagination | Load more extends the visible catalog from 8 to the Core-reported 14 points. |
| Definite refusal | Simulated HTTP 403 displays Administrator access required, enables Back/Close again and permits destination correction; it does not queue an operation. Real authorization was not tested. |

In the normal session, all 16 observed fetch/XHR responses were HTTP 200, with
zero console errors and zero page errors. Intentional refusal/unavailability
responses above are separate from that normal-session observation.

## Visual review

Both list views were inspected at **1440×900**, **1024×768** and **390×844** in
light and dark themes. Every combination had zero horizontal page overflow.
Desktop/tablet rows hide phone-only summaries; phone rows expose the compact
summary and preserve usable name widths. Expanded job context spans two phone
columns and six wider-screen columns.

Captures use `<width>-<jobs|points>-<light|dark>.webp`, for example
`1440-jobs-light.webp` and `390-points-dark.webp`. Additional inspected captures:
`job-review-light.webp`, `loading-jobs-light.webp`,
`unknown-run-history-light.webp`, `unavailable-catalog-light.webp`,
`no-repository-light.webp`, `phone-restore-dark.webp`, `phone-review-dark.webp`,
`phone-unavailable-repositories-light.webp`, `phone-backup-refusal-light.png` and
`repository-poll-focus-light.png`.

Raster captures are ignored by repository policy and remain local evidence;
they are not available in a fresh checkout. The structured results and this
written report are the reviewable record. Captures contain fictional inventory,
not production identifiers or operator credentials.
All 20 WebP captures contain only the VP8 image chunk, with no EXIF or XMP chunk.
Both PNG captures contain only IHDR, IDAT and IEND chunks, with no textual or
Exif metadata.

## Regression checks and frontend checks

The existing test runner executes behavior-focused web tests. Source-text tests
that pinned the old retry/selection implementation were removed rather than
rewritten to pin the redesign.

`tests/integration/backup_workspace_browser.py` ran against the built UI in the
isolated Playwright environment, with an explicit local evidence directory.
The first five checks failed on the initial redesigned candidate. Final review
added three checks that failed on the candidate where those first five already
passed. After both correction batches, the complete eight-case suite passed:

| Check | Before its correction | Final candidate |
|---|---|---|
| `retained-metadata` | Failed | Passed |
| `draft-route` | Failed | Passed |
| `uncertain-job` | Failed | Passed |
| `repository-focus` | Failed | Passed |
| `phone-layout` | Failed | Passed |
| `backup-refusal` | Failed | Passed |
| `repository-inventory` | Failed | Passed |
| `repository-poll-focus` | Failed | Passed |

They cover newer selected-point metadata after filtering, ordinary restore-draft
retention, exact unresolved job intent across scope navigation, focus restoration
after closing a repository panel, and phone-to-tablet table-column transitions.
The final three additionally cover panel-scoped refusal feedback, pending/failed/
confirmed-empty repository transitions, and focus restoration after a polled run
changes from failed to succeeded. See BUG-0986 through BUG-0993 in the
[bug ledger](../../BUG_LEDGER.md).

The forge browser lane invokes this script on pushes and pull requests;
`.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` contain the same
new step. Remote CI was not executed here.

Executed successfully after integration:

- `npm --prefix web run typecheck`
- `npm --prefix web test`
- `npm --prefix web run build`
- `python3 scripts/generate_api.py --check` — all three generated references current
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` — 1 test passed
- `python3 scripts/check_docs.py --worktree` — supplied records and structure passed

Net implementation change: **+393 lines**, including UI code/styles, removal of
the two source-text tests, the behavior-focused browser regression script and
its two CI steps; documentation and evidence are excluded from that count.

Independent read-only UI and documentation reviewers accepted the final
corrections with no remaining findings. They did not run or rerun checks.

## API, guide and state assessment

**API impact: existing contract.** Catalog/run reads, backup, restore-as-new,
replace, test restore, protect, verify/cleanup, job create/pause/run-now and
revision-checked repository changes retain their URLs, bodies, authorization,
CSRF, errors and retry identities. Controller still relays Core operations;
there is no new endpoint, schema, telemetry or backup engine.

**Guide impact: updated.** Backup-now, job setup, restore-as-new, replacement and
test-restore steps now describe the separate views, inspection-first selection,
named forms, fixed footer and uncertainty behavior. The walkthrough above covers
their changed UI portions, not unchanged native recovery instructions.

**State owner: none for this change.** Presentation drafts, tabs, filters and
selection remain transient React state. Existing durable policies, runs,
repositories, points and verification records remain on Core and are adopted
through the existing host APIs.

## Limits

- No native capture, restore allocation/cutover, disk deletion, boot verification,
  application recovery, scheduler tick or retention was executed.
- Authentication, authorization and CSRF were simulated, not proven against a
  running Controller/Core pair.
- Existing native recovery and phase-gate limitations remain unchanged.
- No deployment, commit, push or remote CI run was performed.
