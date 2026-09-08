# Living documentation gate verification

Date: 2026-09-08. Scope: the documentation/guide/checker slice, not concurrent installer changes.
API impact: none — no Nex runtime contract changes; the API reference links to the new gate.

## Checks and evidence

The runnable check is [scripts/check_docs.py](../../scripts/check_docs.py), with focused
[stdlib tests](../../tests/test_check_docs.py). Both commands below passed locally. The test suite
exercises missing assessments, applicable guide/API updates, reasons/evidence, guide structure and
links, removed files, historical references, clean-checkout requirements, multi-commit pushes, PR
merge bases, and first/new branch behavior. It executes the exact shell range-selection blocks from
both workflow files in temporary Git fixtures, including a missing-default-ref failure.

Commands:

```sh
python3 scripts/check_docs.py --all
python3 -m unittest discover -s tests -p 'test_check_docs.py' -v
```

Range validation uses an isolated temporary Git repository containing the existing committed
baseline plus this slice's assessed files. It does not commit, reset, or stage the shared
worktree. Concurrent installer/account-flow files are outside this validation. The fixture
checks both the baseline-to-slice range and an empty-tree first push, then removes itself.

The actual slice first failed as expected without assessments (`changed paths missing documentation
assessment`). With the records included, the push range, PR merge-base range, and first-push full-tree
audit all passed. Local transcript: `/tmp/nex-documentation-validation-6shzkosv.log` (ephemeral evidence,
not a release download). A first attempt completed its checks but could not write a log into the
Docker-owned cache directory; the rerun retained its transcript in a writable temporary location.

## User walkthrough evidence

The [host command walkthrough](guide-host-checks.md) records exact commands run on a disposable
clone of the existing alpha test disk. The [original ISO QA](iso/2026-09-08-alpha1.md) records
its own image hash and automated installation coverage. Guide pages distinguish those results
from current source changes and from untested interactive installation.

## Limits

No GitHub or Gitea hosted run was started, and no branch protection or runner configuration was
changed. Workflow compatibility was checked against official documentation and the shared
local checker was exercised; this does not establish hosted execution. Tests demonstrate
structural and diff-based detection, not semantic understanding of arbitrary feature/API code.
Reviewers still decide applicability and inspect real walkthrough evidence. No GUI or runtime
Nex API exists to simulate or schema-check yet.
