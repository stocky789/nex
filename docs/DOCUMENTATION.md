# Maintaining the Nex documentation

Material changes assess documentation impact, and implemented user workflows get instructions
and verification alongside their code. Small local tweaks follow the lightweight rule in
[AGENTS.md](../AGENTS.md#living-user-guide-and-documentation-gate); they need no standalone change
record or QA report.

## Where information belongs

- [User guide](guide/README.md): tasks operators can perform in the released or explicitly labeled
  development version, including GUI, installation, configuration, errors, and recovery.
- [API reference](API.md): implemented contracts, inputs, outputs, authorization, errors, and examples.
- [Plans](plans/2026-09-08-nex-platform.md): future behavior and delivery gates; planned features are
  not presented as available instructions.
- `docs/qa/`: actual walkthrough/test results, version/environment, inspected captures, and limits.
- `docs/BUG_LEDGER.md`: bug symptoms, root causes, fixes and regression evidence.
- [Changelog](../CHANGELOG.md): short user-facing entries.
- [Development](development.md): build and developer instructions, kept out of the operator guide.
- `docs/changes/`: review records for material changes, tying files to guide/API applicability and evidence.

Keep one owner for each fact. Guide pages link to API details or evidence rather than copying
endpoint tables and raw logs.

## Author and verify a guide page

Use a descriptive filename under `docs/guide/`, one H1 title, and these H2 sections in order:

1. **Status** — product/version, availability, and known limits.
2. **Prerequisites** — required access, resources, compatibility, and safety conditions.
3. **Steps** — numbered operator actions; actual UI labels/locations or runnable commands as applicable.
4. **Expected result** — what the operator should observe and how to confirm completion.
5. **Troubleshooting** — relevant failures, limitations, safe recovery, and destructive-action cautions.
6. **Verification** — a short statement of which steps were exercised or remain untested, linking
   to the `docs/qa/` evidence.

Mark an inapplicable section with a reason; do not leave empty headings. Add every page to
`docs/guide/README.md`, which is the navigation index and is exempt from these six sections.
Use ordinary Markdown with inline relative links and images, stable filenames, descriptive link
text and useful alt text. Avoid framework-specific components and a second website-only copy; a
future static site renders these same files. Keep versioned documentation with release tags; label
development guidance.

For changes outside the lightweight rule, walk through the page against the actual feature in a disposable environment, covering relevant
success, invalid-input, permission, failure and recovery paths; for GUI work use the browser and
inspect screenshots. Record exact version or artifact digest, environment, actions, observed results
and evidence inspected; identify simulated hardware or mocked services. Existing evidence may be
reused for a docs-only change once inspected and its limits stated. An automated backend test does
not prove interactive UI steps were walked through. If verification cannot run, record why and
leave that acceptance item open.

## Record material changes

For material changes, create a uniquely named `docs/changes/YYYY-MM-DD-short-description.json` in the same change.
Assess all changed files, including tooling and docs. One record can cover a coherent batch;
multiple records can cover a larger PR. Use exact repository-relative paths, not glob patterns.
Change records themselves do not need recursive assessment records.

Example for an internal tooling change (replace paths and evidence with the real change):

```json
{
  "summary": "Improve installer input validation without changing operator steps",
  "paths": ["installer/check.sh"],
  "guide": {
    "impact": "none",
    "reason": "Build-time validation only; installed behavior and operator instructions are unchanged.",
    "pages": []
  },
  "api": {
    "impact": "none",
    "reason": "The installer has no Nex Controller/Core API.",
    "pages": []
  },
  "roadmap": {
    "impact": "none",
    "reason": "No milestone changed status, so the published board still describes the project accurately.",
    "pages": []
  },
  "state": {
    "owner": "none",
    "reason": "Validation runs at build time and persists nothing."
  },
  "verification": {
    "method": "Run the affected validation check and inspect its actual result.",
    "evidence": ["installer/check.sh"]
  }
}
```

For applicable guide and roadmap changes, set `impact` to `updated` and list the updated
pages. New records use `api.impact: none | existing contract | changed contract | new contract`,
matching the [required API impact check](../AGENTS.md#required-api-impact-check); `updated` is
accepted only in tracked historical records. Explain the decision in the record and summary/plan.
`guide.pages` names guide Markdown; `api.pages` names `docs/API.md` or files under `docs/api/`.
List applicable reference changes in the same diff; clarifying an existing contract does not
imply a runtime behaviour change.

`roadmap.impact` says whether the change moves the published
[roadmap board](../website/src/routes/roadmap/+page.svelte). Set it to `updated` and name the page
whenever a milestone's status or its built/outstanding items change; set it to `none` with a reason
otherwise (the ordinary case for bug fixes, refactors and documentation). A milestone is not marked
built until its acceptance checks have run: the board keeps *built* separate from *in testing*. The
checker enforces that the question was answered and that an `updated` claim changed the page. The
field is required for records dated from `2026-09-12` (`ROADMAP_REQUIRED_FROM` in the checker);
earlier records stay valid, and a record without a date must answer.

`state.owner` declares where the change's durable state lives, per the
[architecture invariant](../AGENTS.md#the-architecture-invariant-nex-core-does-the-work-nex-controller-only-asks):

- `none` — persists nothing new. Omit `record` entirely; an empty `""` is rejected.
- `core` — `record` names the durable host-side record on the pool master and the adoption path a
  fresh Controller uses to recover it with the same identity and name.
- `controller` — `record` must name one of the closed Controller-scoped responsibilities: sites,
  administrator accounts and roles, audit history, enrollment tokens or the Controller CA,
  and how it is backed up and restored. Pool backup/restore jobs, schedules, history and catalog
  belong to Core (project owner, 2026-09-25, superseding the earlier exception).

The checker no longer accepts backup/restore as a Controller ownership exception and rejects
explicit recovery-job records even when they also mention a permitted responsibility. This is a
wording check, not semantic proof: review historical and new records against their actual state,
without rewriting history as if implementation had changed.

The checker cannot judge whether the named record exists or the owner is honest; reviewers do.
`state` is required on **every** record, merged ones included (`--all` validates the whole
directory); do not remove it from a merged record.

The `impact: updated` same-diff page requirement applies when a record is **added**; correcting a
merged record later does not require editing its pages again. Prefer a new record for follow-up work.

Evidence paths in new or updated assessments must exist; historical records keep their original
paths. A test source is a runnable check, not proof it passed: the verification method must say
what was run. Feature walkthroughs should link a `docs/qa/` report. Do not change an unrelated page
to satisfy the gate. Removed features update navigation and surviving guidance.

## QA raster captures are untracked

`.gitignore` excludes `docs/qa/**/*.png`, `*.jpg`, `*.jpeg`, `*.webp` and `*.gif`, so no QA
screenshot is tracked in this repository. Change records and QA reports therefore cite capture
filenames that are not present in a fresh checkout and cannot be re-inspected from the
repository; the captures exist only on the machine that produced them. At the 2026-09-19 audit,
374 of the 412 image paths cited across 39 change records were absent from the checkout.

This is a standing reviewability limitation, not a closed finding. Reviewers may and should
raise evidence reviewability, including missing captures, when it affects a judgement they
have to make. What a reviewer cannot do is treat an untracked capture as proof of anything,
or accept a claim that captures were inspected without the report saying who inspected them,
when, and in what environment. Written QA reports under `docs/qa/` and results files remain
the reviewable record; keep them specific enough to stand without the images.

The project owner separately closed three missing historical website screenshots as accepted,
recorded in the [2026-09-19 audit note](qa/2026-09-19-documentation-sweep.md#historical-website-screenshots).
That decision means those three are not to be recreated or treated as outstanding work; it
does not establish that every other cited capture is present, and it does not waive
verification for new changes.

## Local and hosted checks

Run from the repository root:

```sh
# Run this before committing to check guide structure and supplied records
# against uncommitted and untracked work.
python3 scripts/check_docs.py --worktree

python3 scripts/check_docs.py --all
python3 -m unittest discover -s tests -p 'test_check_docs.py'
# After committing locally, check the actual Git range:
python3 scripts/check_docs.py --base <full-base-sha> --head "$(git rev-parse HEAD)"
```

`--worktree` checks the guide and any records present in `git status`, treating untracked records
as added. `--all` checks guide structure/links and record schemas, not historical references.
Range mode requires a clean checkout at the given head; ignored build/cache
artifacts do not block it. For a PR use its merge base; CI selects ranges automatically.

Forge CI runs the checker and its tests on every push and pull request; on github.com only
`.github/workflows/github.yml` runs, on main pushes and pull requests. Pushes assess the net before/after diff; PRs assess merge-base-to-head. A new non-default
branch push is compared with its merge base against the fetched default branch (a missing default
ref fails); a first default-branch push assesses the full tree. The baseline assessment covers only
pre-gate files.

The gate checks supplied records' reasons, evidence references, `state.owner` (with the
closed-list rule for `controller`), applicable guide/API page changes, `roadmap.impact` from the
cutoff onward, guide structure, local links and navigation. External URLs are not fetched and
reference-style links are unsupported. It does not infer whether code needs an API change or judge
prose; reviewers must challenge an incorrect `none`. Use the [development checks](development.md)
alongside it.

Hosted enforcement needs Actions enabled, a compatible runner, and the documentation job as a
required branch-protection check; inspect a real run on each host before claiming enforcement. On
self-hosted Gitea, use isolated runners for untrusted PR code without production credentials.
The checker validates records that are supplied but cannot detect an omitted record for a material
change. Independent review must decide whether a change qualifies for the lightweight rule and
whether its guide and verification fit the actual behavior.
