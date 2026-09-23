# Controller UI modernization: handover

Date: 2026-09-23
Plan: [2026-09-23-controller-ui-modernization.md](2026-09-23-controller-ui-modernization.md)
State: Tasks 1–13 are implemented and reviewed. Tasks 1–6 and part of Task 7 are in commit
`0cc49ef`. The rest is unstaged in the working tree for the project owner to commit. Evidence is in
[the QA checks](../qa/controller-ui-modernization/checks.md).

## Gates

Last run on the final working tree:

- `npm --prefix web run typecheck`, `npm --prefix web test` and `npm --prefix web run build` pass.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` passes.
- `python3 scripts/generate_api.py --check` passes.
- `tests/integration/phase1.py --browser` passes all 22 groups.
- `node web/src/tokens.test.mjs` passes. It now also checks chart series at 3:1 against the panel in both themes.

`python3 scripts/check_docs.py --worktree` passes. The change record's guide pages are the ones
changed in the working tree. The pages changed in `0cc49ef` are listed under its paths.

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
- **R5 (Task 8).** `.panel` keeps `overflow: visible` rather than the plan's `hidden` (BUG-0050), and
  no table header is sticky: inside `.table-scroll` it could never stick. Tables get tabular
  numerals globally instead of a `.num` class. The pool fencing summary is one info banner above
  the Hosts table; each row keeps its badge, with the reasons behind a **Why** disclosure.
- **R6 (Task 9).** The sign-in lockup's porcelain plate is a spread `box-shadow`, because the mark
  is a positioned sprite and padding would expose the neighbouring artwork. The dialog padding
  stayed at 14 px 16 px: eight dialogs pull their headings to the edge with matching margins.
- **R7 (Task 10).** The Updates badge uses the reported `update_available` flag. The plan's
  `versionLight` call only parses "Nex Core" version strings.
- **R8 (Task 13).** The toolbar clipped between 901 and about 1250 px, so navigation and VM power
  are icon-only with tooltips up to 1280 px. The heading and tab bar were tightened by 8 px so
  1024×768 reaches the 520 px content target.


## Open minor findings

- `storage.tsx` `ZFSPoolLines` still renders a raw `badge warn` span outside `Badge`.
- The "Creation ${status}" sentence in `main.tsx` and `HostHealthSummary` still show raw values.
  The operation kinds `pool-member-join` and `pool-storage-reconcile` use the fallback label.
- The multi-failure notice logic sits inline in `reload()` and has no unit test.
- No test pins the delete-VM notice text that `docs/guide/delete-virtual-machine.md` quotes.
- `ui_review_browser.py` does not URL-decode query values. This comes verbatim from the plan's code.
- At 390 px the icon toolbar takes two rows, not one.
- Console status text built from an API error can lack its full stop ("VM not found Retrying…").

## Local review setup

This is git-ignored and local to the machine that ran it, under `.cache/ui-review/`:

- `start.sh` serves the working-tree UI on `https://127.0.0.1:18443`.
- `start.sh base` serves the pre-modernization UI (`3068134`) on port 18444.
- `admin-password` holds the local administrator password for user `root`.
- `captures/ui-before/` and `captures/ui-after/` hold the 40-view capture sets.
- Browser checks need Python Playwright.

API impact: none; presentation only. State owner: none; `nex.dock` and `nex.tree` are per-browser
preferences.
