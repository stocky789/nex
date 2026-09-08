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

Evidence paths in new or updated assessments must exist. Historical records retain their original
paths when files are later moved or deleted; inspect the recorded revision to resolve those paths.
A test source is a runnable check, not proof it passed: the verification
method and reviewed results must explain what was actually run. Feature walkthroughs should link
to a `docs/qa/` report with results. Do not change an unrelated page simply to satisfy the gate.
Removed features update navigation and surviving guidance, including availability/removal information.

## Local and hosted checks

Run from the repository root:

```sh
python3 scripts/check_docs.py --all
python3 -m unittest discover -s tests -p 'test_check_docs.py'
# After committing the change locally, compare its actual Git range:
python3 scripts/check_docs.py --base <full-base-sha> --head "$(git rev-parse HEAD)"
```

`--all` checks the current guide structure/links and assessment schemas; it does not resolve historical
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
and evidence references, requires applicable guide/API pages to change, and checks guide structure,
local links, and navigation. External URLs are not fetched; reference-style Markdown links are outside
the supported guide format. It deliberately does not infer whether arbitrary code changes need an
API or judge prose correctness. Reviewers must challenge an incorrect `none`. API schema/operation
coverage tests are required with the first implemented API; no runtime Nex API exists today.

Hosted enforcement needs Actions enabled, a compatible runner, and the documentation job configured
as a required branch-protection check. Inspect a real run on each host, including its executed steps,
before claiming enforcement. Workflow files and local passes alone are not hosted CI proof. This
change does not configure remote branch protection or execute a hosted run. On self-hosted Gitea,
use isolated runners for untrusted PR code and do not expose production credentials to these jobs.

Independent review checks the change diff, impact decisions, guide accuracy, actual walkthrough
evidence, and remaining limits. The gate supports that review; it does not replace it.
