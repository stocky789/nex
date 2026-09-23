# Controller UI modernization: handover

Date: 2026-09-23
Plan: [2026-09-23-controller-ui-modernization.md](2026-09-23-controller-ui-modernization.md)
State: Tasks 1–6 are complete and reviewed. Task 7 is implemented, but its first fix round is half
done. Tasks 8–13 have not started. Nothing is committed: every change is unstaged in the working tree
for the project owner to commit.

## Where things stand

| Task | State | Notes |
|---|---|---|
| 1 Capture harness | Complete, reviewed | `tests/integration/ui_review_browser.py`, `tests/integration/fixtures/ui-review.json` |
| 2 Tokens, no gradients | Complete, reviewed | `web/src/tokens.css`, `tokens.test.mjs`; see ruling R1 |
| 3 Badge and Banner | Complete, reviewed | `web/src/ui.tsx`, `ui.test.mjs`; every badge site routed through `Badge` |
| 4 Labels, relative time | Complete, reviewed | `web/src/labels.ts`, `labels.test.mjs` |
| 5 Menubar → Actions menu, underline tabs | Complete, one fix round | `phase1_browser.py` now matches the new chrome; login eyebrow stays hidden |
| 6 Task strip, failure notices | Complete, one fix round | `web/src/dock.ts`, `dock.test.mjs`; one combined notice per poll; no duplicate heading in the strip |
| 7 Tree, off-canvas, icon toolbar | **Fix round 1 interrupted** | See "Finish Task 7" below |
| 8 Tables, panels, pool Hosts table | Not started | |
| 9 Forms, dialogs, editor, sign-in | Not started | Adds `--brand-plate`; replaces the dialog backdrop (see R1) |
| 10 Stats, console focus mode, Updates | Not started | Guide: `vm-console.md` Focus mode |
| 11 Icons | Not started | |
| 12 Dark theme | Not started | |
| 13 Evidence, changelog, change record | Not started | `check_docs.py --worktree` fails until this task adds the change record (expected) |

Last verified on the current working tree: `node web/src/tokens.test.mjs`, `npm --prefix web run
typecheck`, `npm --prefix web test` (every `src/*.test.mjs`) and `npm --prefix web run build` all pass.
`python3 scripts/check_docs.py --worktree` still fails because the change record is missing; Task 13
adds it.

## Finish Task 7 first

The review of Task 7 raised four Important findings. Fix round 1 was stopped partway:

1. **Done, not re-reviewed.** Focus on a restored-open tree: focus now moves only on user actions,
   never from the `[treeOpen]` effect, so an off-canvas tree restored from `nex.tree` does not grab
   focus. Still open: the toggle click must focus the resource search input on open. Check that
   `onClick` for `.tree-toggle` in `main.tsx` does this; the stop came while this edit was in progress.
2. **Done, not re-reviewed.** The tree row's `aria-label` now includes ", pool master".
3. **Done, not re-reviewed.** The aside gets `inert` when it is off-canvas and closed (a new `narrow`
   state follows the 900 px media query).
4. **Not done.** Between 901 and 1050 px, a host name wraps onto three lines when a non-fresh badge
   sits inline. Fix it with a small change to the `@media (max-width: 1050px)` block: widen the aside
   column, or let the badge wrap under a name kept on one line.
5. **Not done (minor).** The `.toolbar-label` visually-hidden rule duplicates `.sr-only`; combine
   the selectors.

Then re-run the checks above and a 390 px browser check: no horizontal page scroll; Tab from the top
does not enter the closed tree; the toggle opens it with focus in search; Escape closes it and focus
returns to the toggle. After that, run a scoped re-review of the fix.

## Rulings made during execution

- **R1 (Task 2).** A reviewer flagged that `rgba()` values remain in `style.css` (dialog backdrop,
  sign-in card shadow) while the token test only catches hex literals. Plan text governs: Tasks 8
  and 9 themselves specify `rgba()` shadow and backdrop values, and Task 9 rewrites the backdrop.
  No change was made.
- **R2 (Task 6).** The brief's notice was meant to link to the failed task, but the notice bar never
  rendered links. The notice now names the task and its operation ID in its text. Passing
  `operationID` would have made the notice clear itself immediately, because a failed operation is
  already terminal.
- **R3 (Task 6).** When several tasks fail in one poll, they are announced together in one notice.
- **R4 (Tasks 6–7).** Guide pages beyond the three the plan names (`edit-`, `delete-`,
  `create-virtual-machine.md`, `replace-a-failed-system-disk.md`, `software-updates.md`) were updated
  because they described the removed menubar or the always-open "Recent tasks" drawer. Notice text
  now says "the Tasks strip".

## Deferred minor findings (for the final review to triage)

- `ui_review_browser.py`: an unused `time` import, and query values that are not URL-decoded. Both
  come verbatim from the plan's code.
- `.topbar` border colour equals its background. Task 5 was meant to set `border-bottom: 0`; check it.
- `storage.tsx` `ZFSPoolLines` keeps a raw `badge warn` span, which wraps, outside `Badge`.
- Badge text is now title case everywhere ("Fresh", "Unknown"), as the plan intends.
- Raw status still appears in two sentences: "Inventory refresh ${op.status}." and "Creation
  ${status}" in `main.tsx`. `HostHealthSummary` still shows raw check names. The operation kinds
  `pool-member-join` and `pool-storage-reconcile` use the fallback label.
- `phase1_browser.py` lost its menu auto-close assertion when the File menu went. Add it back on the
  Actions menu after "Revoke access" is clicked.
- The multi-failure notice logic sits inline in `reload()` and has no unit test.
- No test pins the delete-VM notice text that `docs/guide/delete-virtual-machine.md` quotes.

## How to resume

- The ledger of task state, with briefs, reports and review diffs for each task, is local and
  git-ignored under `.superpowers/sdd/2026-09-23-controller-ui-modernization/`. Each task was
  implemented by one agent and reviewed by another; the reports there hold the command output.
- Local review setup, also git-ignored, is under `.cache/ui-review/`:
  - `start.sh` serves the working-tree UI on `https://127.0.0.1:18443`.
  - `start.sh base` serves the pre-modernization UI on port 18444.
  - `admin-password` holds the local administrator password for user `root`.
  - `captures/ui-before/` holds the 40 baseline captures that Task 13 compares against.
  - Browser checks run with `.cache/ui-qa-venv/bin/python`.
  - Rebuild the UI with `npm --prefix web run build`; a page reload picks it up.
- Continue with the plan's Task 8, then Tasks 9–13 in order; almost every task edits `main.tsx` and
  `style.css`, so run them one at a time. Task 13 runs the after-captures and the phase 1 browser
  lab, and writes `docs/qa/controller-ui-modernization/checks.md`, the changelog entry and
  `docs/changes/2026-09-23-controller-ui-modernization.json`. That change record must also list the
  extra guide pages from R4 and this handover file.

API impact: none; presentation only. State owner: none; `nex.dock` and `nex.tree` are per-browser
preferences.
