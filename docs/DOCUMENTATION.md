# Maintaining the Nex documentation

Every change assesses documentation impact. Every implemented user workflow gets instructions
and verification alongside its code. This is a docs-as-code workflow: documentation uses the same
version control, review, and automated checks as implementation. It is an established practice,
not a universal certification standard. [Write the Docs: docs as code](https://www.writethedocs.org/guide/docs-as-code/).

## Where information belongs

- [User guide](guide/README.md): tasks operators can perform in the released or explicitly labeled
  development version, including GUI, installation, configuration, errors, and recovery.
- [API reference](API.md): implemented contracts, inputs, outputs, authorization, errors, and examples.
- [Plans](plans/2026-09-08-nex-platform.md): future behavior and delivery gates; planned features are
  not presented as available instructions.
- `docs/qa/`: actual walkthrough/test results, version/environment, inspected captures, and limits.
- `docs/changes/`: small review records tying changed files to guide/API applicability and evidence.

Keep one owner for each fact. Guide pages link to API details or evidence rather than copying
endpoint tables and raw logs. Build/developer instructions stay outside the operator guide.

## Author and verify a guide page

Use a descriptive filename under `docs/guide/`, one H1 title, and these H2 sections in order:

1. **Status** — product/version, availability, and known limits.
2. **Prerequisites** — required access, resources, compatibility, and safety conditions.
3. **Steps** — numbered operator actions; actual UI labels/locations or runnable commands as applicable.
4. **Expected result** — what the operator should observe and how to confirm completion.
5. **Troubleshooting** — relevant failures, limitations, safe recovery, and destructive-action cautions.
6. **Verification** — link to actual evidence and state exactly which steps were exercised or remain untested.

Mark an inapplicable section with a reason; do not leave empty headings. Add every page to
`docs/guide/README.md`, which is the navigation index and is exempt from these six sections.
Use ordinary Markdown with inline links (`[label](relative-path.md)`), repository-relative links and
images, plain section headings, stable filenames, descriptive link text,
and useful image alt text. Avoid workstation paths, embedded secrets, framework-specific components,
and a second website-only copy. A future static site can render these same files; choose its framework
when publishing is needed. Keep versioned documentation with release tags; label development guidance.

Follow the instructions against the actual feature in a disposable environment before accepting it.
Check relevant successful, invalid-input, permission, failure, and recovery paths. For GUI work,
interact through the browser and inspect screenshots, including relevant empty/loading/error states.
Record the exact version or artifact digest, environment, commands or actions, observed results,
and evidence actually inspected. Simulated hardware or mocked services must be identified explicitly.
Existing applicable evidence may be reused for a docs-only change; inspect it and state its limits.
An automated backend test does not prove that every interactive UI step was walked through.

If verification cannot run, record the reason and leave that acceptance item open. A green link checker
cannot turn an untested restore, migration, or installation path into a verified feature.

## Record each change

Create a uniquely named `docs/changes/YYYY-MM-DD-short-description.json` in the same change.
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

For applicable changes set `impact` to `updated` and list the guide/API pages updated in the same
diff. `guide.pages` names guide Markdown; `api.pages` names `docs/API.md` or files under `docs/api/`.
The API field concerns documentation impact; retain the more specific runtime API impact classification
from [AGENTS.md](../AGENTS.md#required-api-impact-check) in the summary/plan. An existing contract
whose behavior changes can still require updated documentation. Explain any `none` honestly.

`state.owner` says where the change's durable state lives, because Nex Controller is replaceable
and must be able to be thrown away and rebuilt from the hosts. Declare one of:

- `none` — the change persists nothing new. Omit the `record` key entirely; an empty `""` is
  rejected because it hedges instead of answering.
- `core` — state lives on a Nex Core host. `record` names the durable host-side record on the pool
  master and the inventory/adoption path a fresh Controller uses to recover it with the same
  identity and name.
- `controller` — one of the closed Controller-scoped exceptions (sites, administrator accounts and
  roles, audit history, enrollment tokens, the Controller CA, backup/restore jobs). `record` must
  name which one — the checker matches it, case-insensitively, against those terms and refuses a
  record that names none of them — and how it is backed up and restored. Anything else with this
  owner is a design defect: fix the design.

The checker cannot judge whether the named record actually exists or whether the owner is honest;
it refuses a change that never answers the question. Reviewers verify the answer against the
[architecture invariant](../AGENTS.md#the-architecture-invariant-nex-core-does-the-work-nex-controller-only-asks).
Unlike the `impact: updated` page rule below, `state` is required on **every** record, merged
ones included: `--all` validates the whole `docs/changes/` directory, so the records stay a
complete map of where state lives rather than one with unassessed gaps. Every record that
predated the field has been backfilled; do not remove `state` from a merged record.

The `impact: updated` requirement that listed pages change in the same diff applies when a record
is **added**, which is when it has to prove the pages were updated with it. Correcting a merged
record afterwards - fixing a reason, adding a path that was missed - does not ask you to edit its
pages again. Prefer a new record for follow-up work regardless: records are a history of changes,
not living documents.

Evidence paths in new or updated assessments must exist. Historical records retain their original
paths when files are later moved or deleted; inspect the recorded revision to resolve those paths.
A test source is a runnable check, not proof it passed: the verification
method and reviewed results must explain what was actually run. Feature walkthroughs should link
to a `docs/qa/` report with results. Do not change an unrelated page simply to satisfy the gate.
Removed features update navigation and surviving guidance, including availability/removal information.

## Local and hosted checks

Run from the repository root:

```sh
# Run this BEFORE committing. It applies the full gate, including changed-file
# coverage, to uncommitted and untracked work, so a missing assessment is caught
# here instead of as a red build after the push.
python3 scripts/check_docs.py --worktree

python3 scripts/check_docs.py --all
python3 -m unittest discover -s tests -p 'test_check_docs.py'
# After committing the change locally, compare its actual Git range:
python3 scripts/check_docs.py --base <full-base-sha> --head "$(git rev-parse HEAD)"
```

`--worktree` is the one to reach for while working: it runs the same coverage check CI runs, over
`git status` rather than a pushed range, and treats untracked records as newly added. `--all` checks
the current guide structure/links and assessment schemas; it does not resolve historical
assessment references or establish changed-file coverage. Range checking verifies new/updated record
references and uses the specified Git commits; uncommitted files are not part of their diff.
Range mode requires a clean checkout at the specified head, so uncommitted documentation cannot
accidentally satisfy an older committed change. Ignored build/cache artifacts do not block it.
For a PR use its merge base as the base. CI handles that range selection automatically.

Both workflow locations, `.github/workflows/` and `.gitea/workflows/`, invoke the same checker and
its tests on pushes and pull requests. Pushes assess the net before/after diff, including a multi-commit
push; PRs assess the merge-base-to-head diff. A new non-default branch has no previous SHA, so its
push is compared with its merge base against the fetched default branch; a missing default ref fails
explicitly. A first default-branch push assesses the full tree. The baseline assessment inventories
the previously committed pre-gate files for that initial audit; it cannot excuse future changes because
ordinary ranges use only added/modified records. These checks do not demand a separate guide edit for
every intermediate commit, nor do they run on unpushed local commits.

The gate requires changed paths to be covered by an added/modified assessment, validates reasons
and evidence references, requires a declared `state.owner` on every record with a named record
where one is owed (a `controller` owner must name a closed-list exception),
requires applicable guide/API pages to change, and checks guide structure,
local links, and navigation. External URLs are not fetched; reference-style Markdown links are outside
the supported guide format. It deliberately does not infer whether arbitrary code changes need an
API or judge prose correctness. Reviewers must challenge an incorrect `none`. API schema/operation
coverage checks accompany the implemented Phase 1 API; use the [development checks](development.md)
and [API reference](API.md) alongside the documentation gate.

Hosted enforcement needs Actions enabled, a compatible runner, and the documentation job configured
as a required branch-protection check. Inspect a real run on each host, including its executed steps,
before claiming enforcement. Workflow files and local passes alone are not hosted CI proof. This
change does not configure remote branch protection or execute a hosted run. On self-hosted Gitea,
use isolated runners for untrusted PR code and do not expose production credentials to these jobs.

Independent review checks the change diff, impact decisions, guide accuracy, actual walkthrough
evidence, and remaining limits. The gate supports that review; it does not replace it.
